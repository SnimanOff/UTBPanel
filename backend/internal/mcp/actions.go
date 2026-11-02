package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ActionTTL is how long a request waits for the owner.
var ActionTTL = 15 * time.Minute

// MaxWait bounds one wait_action call: MCP calls have timeouts, and the
// owner may come to the computer much later, so the AI calls it again.
const MaxWait = 60 * time.Second

// Action is a confirm-level operation an AI asked for.
type Action struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	BotID     int64           `json:"bot_id,omitempty"`
	Summary   string          `json:"summary"`
	Detail    json.RawMessage `json:"detail"`
	Requested string          `json:"requested_by"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	State     string          `json:"state"` // pending | running | done | failed | rejected | expired
	Result    json.RawMessage `json:"result,omitempty"`
	DecidedBy string          `json:"decided_by,omitempty"`
	DecidedAt time.Time       `json:"decided_at,omitzero"`

	args   string
	caller api.Caller
}

// Final reports whether the action will not change any more.
func (a Action) Final() bool { return a.State != "pending" && a.State != "running" }

const actionColumns = `id, op, bot_id, args, summary, detail, caller, created_at, expires_at, state, result, decided_by, decided_at`

func scanAction(row interface{ Scan(...any) error }) (Action, error) {
	var a Action
	var detail, caller, result string
	var created, expires, decided int64
	err := row.Scan(&a.ID, &a.Op, &a.BotID, &a.args, &a.Summary, &detail, &caller, &created, &expires, &a.State, &result, &a.DecidedBy, &decided)
	if err != nil {
		return a, err
	}
	a.Detail = json.RawMessage(detail)
	if result != "" {
		a.Result = json.RawMessage(result)
	}
	json.Unmarshal([]byte(caller), &a.caller)
	a.Requested = a.caller.Name
	a.CreatedAt, a.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	if decided > 0 {
		a.DecidedAt = time.Unix(decided, 0).UTC()
	}
	return a, nil
}

func (s *Service) action(ctx context.Context, id string) (Action, error) {
	s.expire(ctx)
	return scanAction(s.DB.R.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM mcp_actions WHERE id = ?`, id))
}

// Actions returns open requests and those decided in the last hour.
func (s *Service) Actions(ctx context.Context) ([]Action, error) {
	s.expire(ctx)
	rows, err := s.DB.R.QueryContext(ctx, `SELECT `+actionColumns+` FROM mcp_actions
		WHERE state IN ('pending', 'running') OR MAX(decided_at, expires_at) > ? ORDER BY created_at DESC LIMIT 50`, time.Now().Add(-time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// wake returns a channel closed on the next change of any action.
func (s *Service) wake() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (s *Service) signal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
}

// expire closes requests nobody answered in time.
func (s *Service) expire(ctx context.Context) {
	now := time.Now().Unix()
	rows, err := s.DB.R.QueryContext(ctx, `SELECT id FROM mcp_actions WHERE state = 'pending' AND expires_at <= ?`, now)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		res, err := s.DB.W.ExecContext(ctx, `UPDATE mcp_actions SET state = 'expired' WHERE id = ? AND state = 'pending'`, id)
		if n, _ := res.RowsAffected(); err == nil && n > 0 {
			s.resolve(ctx, id)
		}
	}
	if len(ids) > 0 {
		s.signal()
	}
}

// RunExpiry closes expired requests and their notifications every minute.
func (s *Service) RunExpiry(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.expire(ctx)
		}
	}
}

func (s *Service) resolve(ctx context.Context, id string) {
	if s.Notes != nil {
		s.Notes.Close(context.WithoutCancel(ctx), "mcp:"+id)
	}
}

// describe says what the action will do: a diff for apply_config, the
// version and its tgpanel.yaml diff for deploy_release.
func (s *Service) describe(ctx context.Context, op operation, args map[string]any) (string, map[string]any, error) {
	detail := map[string]any{"operation": op.ID}
	caller := api.CallerFrom(ctx)
	bot := url.PathEscape(fmt.Sprint(args["id"]))
	switch op.ID {
	case "apply_config":
		raw, err := json.Marshal(args["body"])
		if err != nil {
			return "", nil, err
		}
		status, diff, err := s.internal(ctx, "POST", "/api/bots/"+bot+"/config/diff", nil, "application/json", bytes.NewReader(raw), caller.IP)
		if err != nil {
			return "", nil, err
		}
		if status >= 400 {
			text, _ := json.Marshal(diff)
			return "", nil, fmt.Errorf("diff_failed: %s", text)
		}
		detail["diff"] = diff
		n := 0
		if m, ok := diff.(map[string]any); ok {
			if changes, ok := m["changes"].([]any); ok {
				n = len(changes)
			}
		}
		return fmt.Sprintf("применить настройки (%d изменений)", n), detail, nil
	case "deploy_release":
		version := fmt.Sprint(args["version"])
		detail["version"] = version
		status, cfg, err := s.internal(ctx, "GET", "/api/bots/"+bot+"/releases/"+url.PathEscape(version)+"/config", nil, "application/json", nil, caller.IP)
		if err == nil && status == 200 {
			detail["config"] = cfg
			// The owner confirms the diff shown here: bind the deploy to it,
			// so changed settings make it fail instead of applying another diff.
			body, _ := args["body"].(map[string]any)
			if m, ok := cfg.(map[string]any); ok && body["config_version"] == nil {
				if body == nil {
					body = map[string]any{}
				}
				body["config_version"] = m["settings_version"]
				args["body"] = body
			}
		}
		return "развернуть версию " + version, detail, nil
	}
	shown := map[string]any{}
	for k, v := range args {
		shown[k] = v
	}
	detail["arguments"] = shown
	return op.Summary, detail, nil
}

// request records a confirm-level call and returns {status: "pending", id}
// at once; the owner decides in the feed, the tray or the gate window.
func (s *Service) request(ctx context.Context, op operation, args map[string]any) *sdk.CallToolResult {
	if s.DB == nil {
		return s.denied(ctx, op, args, "confirmation_required: требуется подтверждение владельца")
	}
	summary, detail, err := s.describe(ctx, op, args)
	if err != nil {
		return s.denied(ctx, op, args, err.Error())
	}
	b := make([]byte, 12)
	rand.Read(b)
	id := hex.EncodeToString(b)
	caller := api.CallerFrom(ctx)
	rawArgs, _ := json.Marshal(args)
	rawDetail, _ := json.Marshal(detail)
	rawCaller, _ := json.Marshal(caller)
	now := time.Now()
	expires := now.Add(ActionTTL)
	var bot int64
	if strings.HasPrefix(op.Path, "/api/bots/{id}") {
		bot = inputID(args["id"])
	}
	if _, err := s.DB.W.ExecContext(ctx, `INSERT INTO mcp_actions (id, op, bot_id, args, summary, detail, caller, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, op.ID, bot, string(rawArgs), summary, string(rawDetail), string(rawCaller), now.Unix(), expires.Unix()); err != nil {
		return s.denied(ctx, op, args, "unavailable: the request could not be saved")
	}
	if s.Notes != nil {
		s.Notes.Notify(context.WithoutCancel(ctx), bot, alerts.Note{Kind: "mcp_confirm", Severity: "warn", Key: "mcp:" + id, Text: "нейросеть просит: " + summary, Href: "/ai"})
	}
	if s.Audit != nil {
		s.Audit.Write(context.WithoutCancel(ctx), audit.Entry{Key: caller.Name, IP: caller.IP, Source: string(api.SourceMCP), Action: "mcp." + op.ID, BotID: bot, Object: id, Result: "pending"})
	}
	s.signal()
	structured := map[string]any{"status": "pending", "id": id, "summary": summary, "detail": detail, "expires_at": expires.UTC().Format(time.RFC3339),
		"next": "владелец подтверждает в панели; вызовите wait_action с этим id"}
	text, _ := json.Marshal(structured)
	return &sdk.CallToolResult{StructuredContent: structured, Content: []sdk.Content{&sdk.TextContent{Text: string(text)}}}
}

var errDecided = errors.New("запрос уже решён или истёк")

// Decide confirms (runs) or rejects a pending action for the owner.
func (s *Service) Decide(ctx context.Context, id string, confirm bool, owner string) (Action, error) {
	s.expire(ctx)
	state := "rejected"
	if confirm {
		state = "running"
	}
	now := time.Now().Unix()
	res, err := s.DB.W.ExecContext(ctx, `UPDATE mcp_actions SET state = ?, decided_by = ?, decided_at = ? WHERE id = ? AND state = 'pending' AND expires_at > ?`, state, owner, now, id, now)
	if err != nil {
		return Action{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.action(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return Action{}, err
		}
		return Action{}, errDecided
	}
	s.signal()
	a, err := s.action(ctx, id)
	if err != nil {
		return a, err
	}
	if confirm {
		result := s.run(ctx, a)
		state = "done"
		if result.IsError {
			state = "failed"
		}
		var raw []byte
		if result.StructuredContent != nil {
			raw, _ = json.Marshal(result.StructuredContent)
		} else {
			text := ""
			for _, c := range result.Content {
				if t, ok := c.(*sdk.TextContent); ok {
					text += t.Text
				}
			}
			raw, _ = json.Marshal(map[string]string{"error": text})
		}
		s.DB.W.ExecContext(context.WithoutCancel(ctx), `UPDATE mcp_actions SET state = ?, result = ? WHERE id = ?`, state, string(raw), id)
	}
	s.resolve(ctx, id)
	s.signal()
	return s.action(context.WithoutCancel(ctx), id)
}

// run performs a confirmed action as the device that asked for it. Its
// rights are checked again: the group mode may have changed meanwhile.
func (s *Service) run(ctx context.Context, a Action) *sdk.CallToolResult {
	op, ok := s.ops[a.Op]
	if !ok || op.MCP != spec.Confirm {
		return toolError("forbidden: unknown operation")
	}
	dec := json.NewDecoder(strings.NewReader(a.args))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return toolError("invalid_arguments")
	}
	ctx, cancel := context.WithTimeout(api.WithCaller(context.WithoutCancel(ctx), a.caller), time.Minute)
	defer cancel()
	if s.Groups != nil {
		var unlock func()
		var err error
		ctx, unlock, err = s.Groups.Guard.Lock(ctx)
		if err != nil {
			return toolError("unavailable: permission check timed out")
		}
		defer unlock()
	}
	if err := s.authorize(ctx, op, args); err != nil {
		return toolError(err.Error())
	}
	return s.execute(ctx, op, args)
}

func waitTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        "wait_action",
		Description: "Wait up to 60 s for the owner's decision on a pending action (id from a confirm-level tool). Returns state: pending, running, done, failed, rejected or expired, and the result once done. Call it again while it is pending; a request expires after 15 minutes.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"id"},
			"properties": map[string]any{
				"id":      map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer", "minimum": 0, "maximum": 60, "description": "seconds to wait, 30 by default"},
			},
			"additionalProperties": false,
		},
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}
}

func (s *Service) wait(ctx context.Context, raw json.RawMessage) *sdk.CallToolResult {
	var in struct {
		ID      string `json:"id"`
		Timeout *int   `json:"timeout"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ID == "" {
		return toolError("invalid_arguments: id is required")
	}
	timeout := 30 * time.Second
	if in.Timeout != nil {
		timeout = min(max(time.Duration(*in.Timeout)*time.Second, 0), MaxWait)
	}
	caller := api.CallerFrom(ctx)
	if s.DB == nil || caller.Source != api.SourceMCP || caller.Serial == "" {
		return toolError("forbidden: device identity is required")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Second) // expiry and decisions of another panel process
	defer poll.Stop()
	for {
		woke := s.wake()
		a, err := s.action(ctx, in.ID)
		// Another device's requests are not visible.
		if err != nil || a.caller.Serial != caller.Serial {
			return toolError("not_found: no such action")
		}
		if a.Final() {
			return actionResult(a)
		}
		select {
		case <-woke:
		case <-poll.C:
		case <-deadline.C:
			return actionResult(a)
		case <-ctx.Done():
			return actionResult(a)
		}
	}
}

func actionResult(a Action) *sdk.CallToolResult {
	structured := map[string]any{"id": a.ID, "status": a.State, "summary": a.Summary, "expires_at": a.ExpiresAt.Format(time.RFC3339)}
	if a.Result != nil {
		structured["result"] = a.Result
	}
	text, _ := json.Marshal(structured)
	return &sdk.CallToolResult{StructuredContent: structured, Content: []sdk.Content{&sdk.TextContent{Text: string(text)}}}
}

func actionID(s string) bool {
	if len(s) != 24 {
		return false
	}
	_, err := strconv.ParseUint(s[:12], 16, 64)
	_, err2 := strconv.ParseUint(s[12:], 16, 64)
	return err == nil && err2 == nil
}
