// Package telegram is the Bot API client used by the core for calls on
// behalf of the panel: getMe, webhooks, profile, broadcasts and answers.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultBase is the Bot API address.
const DefaultBase = "https://api.telegram.org"

// Client calls the Bot API.
type Client struct {
	Base string
	HTTP *http.Client
}

// New returns a client with sane timeouts.
func New() *Client {
	return &Client{Base: DefaultBase, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Error is an error answered by Telegram.
type Error struct {
	Method      string `json:"-"`
	Code        int    `json:"error_code"`
	Description string `json:"description"`
	RetryAfter  int    `json:"retry_after,omitempty"`
	MigrateTo   int64  `json:"migrate_to_chat_id,omitempty"`
	// Raw is Telegram's answer as is, shown in the interface.
	Raw json.RawMessage `json:"-"`
}

func (e *Error) Error() string {
	s := fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(" (retry after %d s)", e.RetryAfter)
	}
	return s
}

// AsError returns the Telegram error inside err, if any.
func AsError(err error) (*Error, bool) {
	var te *Error
	ok := errors.As(err, &te)
	return te, ok
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int   `json:"retry_after"`
		MigrateTo  int64 `json:"migrate_to_chat_id"`
	} `json:"parameters"`
}

var tokenInURL = regexp.MustCompile(`/bot[0-9]+:[A-Za-z0-9_-]+`)

// MaskToken hides bot tokens in URLs and messages.
func MaskToken(s string) string { return tokenInURL.ReplaceAllString(s, "/bot***") }

// InputFile is a file sent with multipart/form-data.
type InputFile struct {
	Name string
	Data []byte
}

// Call invokes method with params (a struct or map encoded as JSON, or as
// multipart if it holds an InputFile) and decodes the result into out.
func (c *Client) Call(ctx context.Context, token, method string, params, out any) error {
	body, ctype, err := encode(params)
	if err != nil {
		return err
	}
	base := c.Base
	if base == "" {
		base = DefaultBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/"+method, body)
	if err != nil {
		return errors.New(MaskToken(err.Error()))
	}
	req.Header.Set("Content-Type", ctype)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// *url.Error prints the URL, and the URL holds the token.
		return fmt.Errorf("telegram %s: %s", method, MaskToken(err.Error()))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d, not JSON: %.200s", method, resp.StatusCode, raw)
	}
	if !env.OK {
		return &Error{Method: method, Code: env.ErrorCode, Description: env.Description,
			RetryAfter: env.Parameters.RetryAfter, MigrateTo: env.Parameters.MigrateTo, Raw: raw}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

func encode(params any) (io.Reader, string, error) {
	if params == nil {
		return strings.NewReader("{}"), "application/json", nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, "", err
	}
	files := map[string]InputFile{}
	collectFiles(params, files)
	if len(files) == 0 {
		return bytes.NewReader(b), "application/json", nil
	}
	// Multipart: plain fields as strings (objects as JSON), files as parts.
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range m {
		if _, isFile := files[k]; isFile {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			s = string(v)
		}
		w.WriteField(k, s)
	}
	for k, f := range files {
		part, err := w.CreateFormFile(k, f.Name)
		if err != nil {
			return nil, "", err
		}
		part.Write(f.Data)
	}
	w.Close()
	return &buf, w.FormDataContentType(), nil
}

// fileParams lets a params struct report its files.
type fileParams interface{ files() map[string]InputFile }

func collectFiles(params any, into map[string]InputFile) {
	if fp, ok := params.(fileParams); ok {
		for k, v := range fp.files() {
			if v.Data != nil {
				into[k] = v
			}
		}
	}
}

// MarshalJSON keeps files out of the JSON body.
func (InputFile) MarshalJSON() ([]byte, error) { return []byte(`null`), nil }

// Values helps build query-like params.
func Values(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return m
}
