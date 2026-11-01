package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// ProfileReq reads or changes the Telegram profile for one language
// ("" = default). Only fields that are set are sent.
type ProfileReq struct {
	BotID            int64                  `cbor:"1,keyasint"`
	Lang             string                 `cbor:"2,keyasint,omitempty"`
	Name             *string                `cbor:"3,keyasint,omitempty"`
	Description      *string                `cbor:"4,keyasint,omitempty"`
	ShortDescription *string                `cbor:"5,keyasint,omitempty"`
	Commands         *[]telegram.BotCommand `cbor:"6,keyasint,omitempty"`
	Menu             *telegram.MenuButton   `cbor:"7,keyasint,omitempty"`
	Actor            journal.Actor          `cbor:"9,keyasint"`
}

// ProfileView is the profile with the menu button (one for all languages).
type ProfileView struct {
	Profile telegram.Profile    `cbor:"1,keyasint" json:"profile"`
	Menu    telegram.MenuButton `cbor:"2,keyasint" json:"menu"`
}

// FieldError is Telegram's refusal for one field; the rest are saved.
type FieldError struct {
	Message string `cbor:"1,keyasint" json:"message"`
	// RetryAt is when the field can be saved again (setMyName is limited).
	RetryAt time.Time `cbor:"2,keyasint,omitzero" json:"retry_at,omitzero"`
}

// ProfileSaved lists fields that failed; empty means all were saved.
type ProfileSaved struct {
	Errors map[string]FieldError `cbor:"1,keyasint" json:"errors"`
}

// Profile reads name, descriptions, commands and the menu button.
func (c *Core) Profile(ctx context.Context, botID int64, lang string) (ProfileView, error) {
	var v ProfileView
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return v, err
	}
	defer unlock()
	tok, err := c.Token(ctx, botID)
	if err != nil {
		return v, err
	}
	if v.Profile, err = c.TG.GetProfile(ctx, tok, lang); err != nil {
		return v, telegramError(err)
	}
	if v.Profile.Commands == nil {
		v.Profile.Commands = []telegram.BotCommand{}
	}
	if v.Menu, err = c.TG.GetChatMenuButton(ctx, tok); err != nil {
		return v, telegramError(err)
	}
	in := ProfileReq{BotID: botID, Lang: lang, Name: &v.Profile.Name, Description: &v.Profile.Description, ShortDescription: &v.Profile.ShortDescription, Commands: &v.Profile.Commands, Menu: &v.Menu}
	return v, c.cacheProfile(ctx, in, nil)
}

// SaveProfile sends the changed fields one by one.
func (c *Core) SaveProfile(ctx context.Context, in ProfileReq) (ProfileSaved, error) {
	out := ProfileSaved{Errors: map[string]FieldError{}}
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return out, err
	}
	defer unlock()
	if in.Commands != nil {
		for _, cmd := range *in.Commands {
			if !validCommand(cmd.Command) {
				out.Errors["commands"] = FieldError{Message: "команда " + cmd.Command + ": 1–32 символа a-z, 0-9, _"}
				return out, nil
			}
		}
	}
	tok, err := c.Token(ctx, in.BotID)
	if err != nil {
		return out, err
	}
	try := func(field string, fn func() error) {
		err := fn()
		if err == nil {
			return
		}
		fe := FieldError{Message: telegram.MaskToken(err.Error())}
		if te, ok := telegram.AsError(err); ok {
			fe.Message = te.Description
			if te.RetryAfter > 0 {
				fe.RetryAt = time.Now().UTC().Add(time.Duration(te.RetryAfter) * time.Second)
			}
		}
		out.Errors[field] = fe
	}
	if in.Name != nil {
		try("name", func() error { return c.TG.SetMyName(ctx, tok, in.Lang, *in.Name) })
	}
	if in.Description != nil {
		try("description", func() error { return c.TG.SetMyDescription(ctx, tok, in.Lang, *in.Description) })
	}
	if in.ShortDescription != nil {
		try("short_description", func() error { return c.TG.SetMyShortDescription(ctx, tok, in.Lang, *in.ShortDescription) })
	}
	if in.Commands != nil {
		try("commands", func() error { return c.TG.SetMyCommands(ctx, tok, in.Lang, *in.Commands) })
	}
	if in.Menu != nil {
		try("menu", func() error { return c.TG.SetChatMenuButton(ctx, tok, *in.Menu) })
	}
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return out, c.cacheProfile(cacheCtx, in, out.Errors)
}

// Keep successful profile values for exports and offline configuration reads.
func (c *Core) cacheProfile(ctx context.Context, in ProfileReq, failed map[string]FieldError) error {
	m, err := c.Settings(ctx, in.BotID)
	if err != nil {
		return err
	}
	var d botconfig.Document
	json.Unmarshal(m["profile_cache"], &d.Profile)
	botconfig.Normalize(&d)
	lang := in.Lang
	if lang == "" {
		lang = "default"
	}
	if _, bad := failed["name"]; in.Name != nil && !bad {
		d.Profile.Name[lang] = *in.Name
	}
	if _, bad := failed["description"]; in.Description != nil && !bad {
		d.Profile.Description[lang] = *in.Description
	}
	if _, bad := failed["short_description"]; in.ShortDescription != nil && !bad {
		d.Profile.ShortDescription[lang] = *in.ShortDescription
	}
	if _, bad := failed["commands"]; in.Commands != nil && !bad {
		d.Profile.Commands[lang] = *in.Commands
	}
	if _, bad := failed["menu"]; in.Menu != nil && !bad {
		d.Profile.Menu = in.Menu
	}
	return c.SetSetting(ctx, in.BotID, "profile_cache", d.Profile)
}

func validCommand(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func (c *Core) registerProfile() {
	c.Mux.Handle("profile.get", wire.Func(func(ctx context.Context, _ *wire.Conn, in ProfileReq) (ProfileView, error) {
		return c.Profile(ctx, in.BotID, in.Lang)
	}))
	c.Mux.Handle("profile.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in ProfileReq) (ProfileSaved, error) {
		var out ProfileSaved
		err := c.Journal.Do(ctx, in.Actor, "profile.set", in.BotID, map[string]string{"lang": in.Lang}, func() error {
			var err error
			out, err = c.SaveProfile(ctx, in)
			return err
		})
		return out, err
	}))
}
