package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// SettingPut replaces one section of a bot's settings document.
type SettingPut struct {
	BotID int64         `cbor:"1,keyasint"`
	Key   string        `cbor:"2,keyasint"`
	Value string        `cbor:"3,keyasint"` // JSON
	Actor journal.Actor `cbor:"9,keyasint"`
}

// sections that are set through settings.put, and their checks.
var sections = map[string]func(json.RawMessage) error{
	"antiflood": func(b json.RawMessage) error {
		var v struct {
			Limit    int    `json:"limit"`
			Strikes  int    `json:"strikes"`
			BanHours int    `json:"ban_hours"`
			Window   string `json:"window"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if v.Window != "" {
			if d, err := time.ParseDuration(v.Window); err != nil || d <= 0 {
				return wire.Errorf(wire.CodeBadRequest, "окно антифлуда: например 10s")
			}
		}
		if v.Limit < 0 || v.Strikes < 0 || v.BanHours < 0 {
			return wire.Errorf(wire.CodeBadRequest, "значения антифлуда не могут быть отрицательными")
		}
		return nil
	},
	"texts": func(b json.RawMessage) error {
		var v map[string]string
		return json.Unmarshal(b, &v)
	},
	"tz": func(b json.RawMessage) error {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if _, err := time.LoadLocation(v); err != nil {
			return wire.Errorf(wire.CodeBadRequest, "неизвестный часовой пояс %q", v)
		}
		return nil
	},
	"stop": func(b json.RawMessage) error {
		var v struct {
			DropPending bool `json:"drop_pending"`
		}
		return json.Unmarshal(b, &v)
	},
	"log_patterns": func(b json.RawMessage) error {
		var v []logs.Pattern
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if err := logs.Validate(v); err != nil {
			return wire.Errorf(wire.CodeBadRequest, "%v", err)
		}
		return nil
	},
	"crash_updates": func(b json.RawMessage) error {
		var v bool
		return json.Unmarshal(b, &v)
	},
}

// PutSetting stores a section and tells every cache about it.
func (c *Core) PutSetting(ctx context.Context, in SettingPut) error {
	check, ok := sections[in.Key]
	if !ok {
		return wire.Errorf(wire.CodeBadRequest, "unknown settings section %q", in.Key)
	}
	raw := json.RawMessage(in.Value)
	if !json.Valid(raw) {
		return wire.Errorf(wire.CodeBadRequest, "value is not JSON")
	}
	if err := check(raw); err != nil {
		if _, isWire := err.(*wire.Error); isWire {
			return err
		}
		return wire.Errorf(wire.CodeBadRequest, "%v", err)
	}
	if err := c.SetSetting(ctx, in.BotID, in.Key, raw); err != nil {
		return err
	}
	c.Flood.Invalidate(in.BotID)
	for _, fn := range c.onSettings {
		fn(ctx, in.BotID, in.Key)
	}
	// Texts and the time zone also live at the edge.
	go c.PushEdge(context.Background())
	return nil
}

func (c *Core) registerBotSettings() {
	c.Mux.Handle("settings.put", wire.Func(func(ctx context.Context, _ *wire.Conn, in SettingPut) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "settings."+in.Key, in.BotID, json.RawMessage(in.Value), func() error {
			if err := c.botExists(ctx, in.BotID); err != nil {
				return err
			}
			return c.PutSetting(ctx, in)
		})
	}))
}
