// Package mcp exposes the OpenAPI operation catalog over Streamable HTTP.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Service struct {
	API     http.Handler
	Audit   *audit.Log
	handler http.Handler
}

func New(handler http.Handler, auditLog *audit.Log, log *slog.Logger) (*Service, error) {
	ops, err := operations()
	if err != nil {
		return nil, err
	}
	s := &Service{API: handler, Audit: auditLog}
	server := sdk.NewServer(&sdk.Implementation{Name: "utbpanel", Version: "1"}, &sdk.ServerOptions{Logger: log})
	for _, op := range ops {
		server.AddTool(&sdk.Tool{Name: op.ID, Description: op.Summary, InputSchema: op.Schema, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: op.MCP == spec.Read}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return s.call(ctx, op, req.Params.Arguments), nil
		})
	}
	s.handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: log, MaxRequestBodyBytes: 4 << 20, PropagateRequestCancellation: true})
	return s, nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller := api.CallerFrom(r.Context())
	if caller.Source != api.SourceMCP || caller.Serial == "" {
		api.WriteError(w, http.StatusForbidden, "mcp_token_required", "используйте токен MCP из меню gate")
		return
	}
	// Individual calls use the API audit; protocol initialization/polling does
	// not create indistinguishable POST /mcp entries.
	audit.Skip(r)
	s.handler.ServeHTTP(w, r)
}

func toolError(message string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: message}}}
}
func (s *Service) denied(ctx context.Context, op operation, args map[string]any, message string) *sdk.CallToolResult {
	if s.Audit != nil {
		caller := api.CallerFrom(ctx)
		var bot int64
		if strings.HasPrefix(op.Path, "/api/bots/{id}") {
			bot, _ = strconv.ParseInt(fmt.Sprint(args["id"]), 10, 64)
		}
		code, _, _ := strings.Cut(message, ":")
		s.Audit.Write(context.WithoutCancel(ctx), audit.Entry{Key: caller.Name, IP: caller.IP, Source: string(api.SourceMCP), Action: "mcp." + op.ID, BotID: bot, Result: code})
	}
	return toolError(message)
}

func (s *Service) call(ctx context.Context, op operation, raw json.RawMessage) *sdk.CallToolResult {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil || args == nil {
		return s.denied(ctx, op, nil, "invalid_arguments: expected an object")
	}
	validation, err := validationValue(args)
	if err != nil {
		return s.denied(ctx, op, args, "invalid_arguments: number is out of range")
	}
	if err := op.Validator.Validate(validation); err != nil {
		return s.denied(ctx, op, args, "invalid_arguments: "+err.Error())
	}
	caller := api.CallerFrom(ctx)
	if caller.Source != api.SourceMCP || caller.Serial == "" {
		return s.denied(ctx, op, args, "forbidden: device identity is required")
	}
	if op.MCP != spec.Read {
		return s.denied(ctx, op, args, "forbidden: MCP is currently read-only")
	}
	path := op.Path
	query := url.Values{}
	for _, p := range op.Parameters {
		value, ok := args[p.Name]
		if !ok {
			continue
		}
		text := fmt.Sprint(value)
		if p.In == "path" {
			if text == "" || text == "." || text == ".." || strings.ContainsAny(text, "/\\\x00") {
				return s.denied(ctx, op, args, "invalid_arguments: invalid path parameter")
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(text))
		} else {
			query.Set(p.Name, text)
		}
	}
	var body io.Reader
	contentType := "application/json"
	if value, ok := args["body"]; ok {
		if op.MediaType == "multipart/form-data" {
			fields, ok := value.(map[string]any)
			if !ok {
				return s.denied(ctx, op, args, "invalid_arguments: expected form fields")
			}
			var form bytes.Buffer
			writer := multipart.NewWriter(&form)
			for name, value := range fields {
				text, ok := value.(string)
				if !ok {
					return s.denied(ctx, op, args, "invalid_arguments: form fields must be strings")
				}
				if name == "file" {
					data, err := base64.StdEncoding.DecodeString(text)
					if err != nil {
						return s.denied(ctx, op, args, "invalid_arguments: file must be base64")
					}
					part, err := writer.CreateFormFile(name, "bot")
					if err != nil {
						return toolError("invalid_arguments")
					}
					if _, err := part.Write(data); err != nil {
						return toolError("invalid_arguments")
					}
				} else if err := writer.WriteField(name, text); err != nil {
					return toolError("invalid_arguments")
				}
			}
			if err := writer.Close(); err != nil {
				return toolError("invalid_arguments")
			}
			contentType = writer.FormDataContentType()
			body = &form
		} else {
			data, err := json.Marshal(value)
			if err != nil {
				return s.denied(ctx, op, args, "invalid_arguments: invalid body")
			}
			body = bytes.NewReader(data)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, op.Method, "http://127.0.0.1:47100"+path, body)
	if err != nil {
		return s.denied(ctx, op, args, "invalid_arguments: invalid request")
	}
	r.URL.RawQuery = query.Encode()
	r.Header.Set("X-Panel", "1")
	r.Header.Set("Content-Type", contentType)
	r.RemoteAddr = netAddress(caller.IP)
	response := &responseBuffer{header: make(http.Header)}
	s.API.ServeHTTP(response, r)
	if response.err != nil {
		return toolError("response_too_large: narrow the query or request a smaller page")
	}
	if response.status == 0 {
		response.status = http.StatusOK
	}
	var value any = map[string]string{"status": "ok"}
	if response.body.Len() > 0 {
		decoder := json.NewDecoder(&response.body)
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return toolError("invalid_response: the API did not return JSON")
		}
	}
	structured := map[string]any{"result": value}
	text, err := json.Marshal(structured)
	if err != nil {
		return toolError("invalid_response")
	}
	return &sdk.CallToolResult{IsError: response.status >= 400, StructuredContent: structured, Content: []sdk.Content{&sdk.TextContent{Text: string(text)}}}
}

// Keep json.Number for forwarding to the API so int64s never pass through
// float64. The schema library's type checker requires Go numeric values.
func validationValue(value any) (any, error) {
	switch x := value.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, nil
		}
		return x.Float64()
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			v, err := validationValue(value)
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for index, value := range x {
			v, err := validationValue(value)
			if err != nil {
				return nil, err
			}
			out[index] = v
		}
		return out, nil
	default:
		return value, nil
	}
}
func netAddress(ip string) string {
	if ip == "" {
		return "127.0.0.1:0"
	}
	return net.JoinHostPort(ip, "0")
}

const maxResponse = 8 << 20

var errResponseTooLarge = errors.New("MCP response exceeds 8 MiB")

type responseBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

func (b *responseBuffer) Header() http.Header { return b.header }
func (b *responseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *responseBuffer) Write(data []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	if b.err != nil {
		return 0, b.err
	}
	if len(data) > maxResponse-b.body.Len() {
		b.err = errResponseTooLarge
		return 0, b.err
	}
	return b.body.Write(data)
}
