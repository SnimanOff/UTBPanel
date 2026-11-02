package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
)

func inputID(value any) int64 {
	n, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (s *Service) botMode(ctx context.Context, id int64) (string, error) {
	var mode string
	err := s.Groups.DB.R.QueryRowContext(ctx, `SELECT COALESCE((SELECT g.mcp_mode FROM bot_meta m JOIN groups g ON g.id=m.group_id WHERE m.bot_id=?),'read')`, id).Scan(&mode)
	return mode, err
}
func (s *Service) allowBot(ctx context.Context, id int64, level string) error {
	if id <= 0 {
		return fmt.Errorf("forbidden: a bot is required")
	}
	mode, err := s.botMode(ctx, id)
	if err != nil {
		return err
	}
	if mode == "off" || (mode != "read" && mode != "full") {
		return fmt.Errorf("forbidden: MCP is disabled for this group")
	}
	if level != spec.Read && mode != "full" {
		return fmt.Errorf("forbidden: the group permits reading only")
	}
	return nil
}
func (s *Service) authorize(ctx context.Context, op operation, args map[string]any) error {
	if op.MCP == spec.Never {
		return fmt.Errorf("forbidden: operation is never available through MCP")
	}
	if s.Groups == nil {
		if op.MCP == spec.Read {
			return nil
		}
		return fmt.Errorf("forbidden: MCP is read-only")
	}
	if op.ID == "create_bot" {
		body, ok := args["body"].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid_arguments: body is required")
		}
		groupID := inputID(body["group_id"])
		var mode string
		if _, explicit := body["group_id"]; !explicit {
			if err := s.Groups.DB.R.QueryRowContext(ctx, `SELECT id,mcp_mode FROM groups WHERE mcp_mode='full' ORDER BY position,id LIMIT 1`).Scan(&groupID, &mode); err != nil {
				return fmt.Errorf("forbidden: choose a group in full mode")
			}
			body["group_id"] = json.Number(strconv.FormatInt(groupID, 10))
		} else if groupID <= 0 {
			return fmt.Errorf("forbidden: a new bot must belong to a group in full mode")
		} else if err := s.Groups.DB.R.QueryRowContext(ctx, `SELECT mcp_mode FROM groups WHERE id=?`, groupID).Scan(&mode); err != nil {
			return fmt.Errorf("forbidden: unknown group")
		}
		if mode != "full" {
			return fmt.Errorf("forbidden: group must be in full mode")
		}
		return nil
	}
	if strings.HasPrefix(op.Path, "/api/bots/{id}") {
		return s.allowBot(ctx, inputID(args["id"]), op.MCP)
	}
	if op.ID == "save_alert_rule" || op.ID == "delete_alert_rule" {
		var id, bot int64
		if op.ID == "delete_alert_rule" {
			id = inputID(args["rid"])
		} else if body, ok := args["body"].(map[string]any); ok {
			id = inputID(body["id"])
			bot = inputID(body["bot_id"])
		}
		// Existing rules keep their original bot, irrespective of the submitted
		// bot_id. Resolve it before checking the group's permissions.
		if id > 0 {
			if err := s.Groups.DB.R.QueryRowContext(ctx, `SELECT bot_id FROM alert_rules WHERE id=?`, id).Scan(&bot); err != nil {
				return fmt.Errorf("forbidden: unknown rule")
			}
		}
		return s.allowBot(ctx, bot, op.MCP)
	}
	if bot := inputID(args["bot"]); bot > 0 {
		return s.allowBot(ctx, bot, op.MCP)
	}
	if op.MCP != spec.Read {
		return fmt.Errorf("forbidden: operations without a bot are read-only")
	}
	return nil
}

// Global lists must not provide a second route to bots in disabled groups.
func (s *Service) filter(ctx context.Context, op operation, value any) (any, error) {
	if s.Groups == nil || strings.HasPrefix(op.Path, "/api/bots/{id}") {
		return value, nil
	}
	rows, err := s.Groups.DB.R.QueryContext(ctx, `SELECT m.bot_id FROM bot_meta m JOIN groups g ON g.id=m.group_id WHERE g.mcp_mode NOT IN ('read','full')`)
	if err != nil {
		return nil, err
	}
	off := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		off[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if op.ID == "panics_summary" {
		if summary, ok := value.(map[string]any); ok {
			for key := range summary {
				if off[inputID(key)] {
					delete(summary, key)
				}
			}
		}
		return value, nil
	}
	var scrub func(any) (any, bool)
	scrub = func(value any) (any, bool) {
		switch node := value.(type) {
		case map[string]any:
			if off[inputID(node["bot_id"])] {
				return nil, false
			}
			if op.ID == "list_bots" && off[inputID(node["id"])] {
				return nil, false
			}
			if op.ID == "list_groups" && node["mcp_mode"] == "off" {
				return nil, false
			}
			for key, child := range node {
				if next, keep := scrub(child); keep {
					node[key] = next
				} else {
					delete(node, key)
				}
			}
		case []any:
			out := make([]any, 0, len(node))
			for _, child := range node {
				if op.ID == "problem_bots" && off[inputID(child)] {
					continue
				}
				if next, keep := scrub(child); keep {
					out = append(out, next)
				}
			}
			return out, true
		}
		return value, true
	}
	out, _ := scrub(value)
	return out, nil
}
