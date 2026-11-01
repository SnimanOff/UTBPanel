package broadcast

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
)

// markup turns link buttons into an inline keyboard, one per row.
func markup(bs []Button) json.RawMessage {
	if len(bs) == 0 {
		return nil
	}
	rows := make([][]map[string]string, 0, len(bs))
	for _, b := range bs {
		rows = append(rows, []map[string]string{{"text": b.Text, "url": b.URL}})
	}
	raw, _ := json.Marshal(map[string]any{"inline_keyboard": rows})
	return raw
}

// deliver sends the message to one chat; the photo goes by file_id once
// Telegram has given one.
func (s *Service) deliver(ctx context.Context, token string, b *B, chat int64) error {
	if len(b.Photo) == 0 && b.fileID == "" {
		_, err := s.TG.SendMessage(ctx, token, telegram.SendMessage{ChatID: chat, Text: b.Text, ParseMode: b.ParseMode, ReplyMarkup: markup(b.Buttons)})
		return err
	}
	p := telegram.SendPhoto{ChatID: chat, Caption: b.Text, ParseMode: b.ParseMode, ReplyMarkup: markup(b.Buttons)}
	if b.fileID != "" {
		p.Photo = b.fileID
	} else {
		name := b.PhotoName
		if name == "" {
			name = "photo.jpg"
		}
		p.PhotoFile = telegram.InputFile{Name: name, Data: b.Photo}
	}
	m, err := s.TG.SendPhoto(ctx, token, p)
	if err == nil && b.fileID == "" && len(m.Photo) > 0 {
		b.fileID = m.Photo[len(m.Photo)-1].FileID
		if b.ID != 0 {
			s.DB.W.ExecContext(ctx, `UPDATE broadcasts SET photo_file_id = ? WHERE id = ?`, b.fileID, b.ID)
		}
	}
	return err
}

// SendTest sends the message once, to the owner (preview).
func (s *Service) SendTest(ctx context.Context, b B, chat int64) error {
	token, err := s.Token(ctx, b.BotID)
	if err != nil {
		return err
	}
	if b.ParseMode == "" {
		b.ParseMode = "HTML"
	}
	return s.deliver(ctx, token, &b, chat)
}

func (s *Service) save(b *B, final bool) error {
	errs, _ := json.Marshal(b.Errors)
	q := `UPDATE broadcasts SET cursor = ?, total = ?, sent = ?, blocked = ?, failed = ?, errors = ? WHERE id = ?`
	args := []any{b.cursor, b.Total, b.Sent, b.Blocked, b.Failed, string(errs), b.ID}
	if final {
		q = `UPDATE broadcasts SET cursor = ?, total = ?, sent = ?, blocked = ?, failed = ?, errors = ?, state = ?, finished_at = ? WHERE id = ? AND state = ?`
		args = []any{b.cursor, b.Total, b.Sent, b.Blocked, b.Failed, string(errs), Done, store.Now(), b.ID, Running}
	}
	// Progress is written even when the run was cancelled.
	_, err := s.DB.W.ExecContext(context.Background(), q, args...)
	return err
}

func (s *Service) markBlocked(ctx context.Context, botID, user int64) {
	s.DB.W.ExecContext(ctx, `UPDATE bot_users SET blocked_at = ? WHERE bot_id = ? AND user_id = ? AND blocked_at IS NULL`, store.Now(), botID, user)
}

// next returns the next recipients after the cursor.
func (s *Service) next(ctx context.Context, b *B, n int) ([]int64, error) {
	q, args := audience(*b)
	rows, err := s.DB.R.QueryContext(ctx, `SELECT user_id FROM bot_users WHERE `+q+` AND user_id > ? ORDER BY user_id LIMIT ?`,
		append(args, b.cursor, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// send runs one broadcast until it ends, is paused or cancelled.
func (s *Service) send(ctx context.Context, id int64) error {
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if b.State == Scheduled {
		total, _ := s.Estimate(ctx, b.BotID, b.Audience, b.ActiveDays)
		b.Total = total
		if _, err := s.DB.W.ExecContext(ctx, `UPDATE broadcasts SET state = ?, started_at = ?, total = ? WHERE id = ? AND state = ?`,
			Running, store.Now(), total, id, Scheduled); err != nil {
			return err
		}
	}
	token, err := s.Token(ctx, b.BotID)
	if err != nil {
		return err
	}
	every := s.SaveEvery
	if every <= 0 {
		every = 25
	}
	n := 0
	defer func() { s.save(&b, false) }()
	for {
		ids, err := s.next(ctx, &b, 200)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		for _, user := range ids {
			attempts := 0
			for {
				if err := s.Limit.Wait(ctx, b.BotID); err != nil {
					return err
				}
				err := s.deliver(ctx, token, &b, user)
				if err == nil {
					b.Sent++
					break
				}
				if ctx.Err() != nil {
					// Paused or cancelled mid-call: this user gets it on resume.
					return ctx.Err()
				}
				te, isTG := telegram.AsError(err)
				switch {
				case isTG && te.Code == 429:
					wait := time.Duration(te.RetryAfter) * time.Second
					if wait <= 0 {
						wait = time.Second
					}
					s.Limit.Pause(b.BotID, wait)
					continue
				case isTG && te.Code == 403:
					b.Blocked++
					s.markBlocked(ctx, b.BotID, user)
				case isTG && te.Code >= 400 && te.Code < 500:
					b.Failed++
					b.addError(te.Description)
				default:
					// Network or 5xx: try a few times, then count it.
					attempts++
					if attempts < 3 {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(time.Duration(attempts) * time.Second):
						}
						continue
					}
					b.Failed++
					b.addError(telegram.MaskToken(err.Error()))
				}
				break
			}
			b.cursor = user
			if n++; n%every == 0 {
				s.save(&b, false)
			}
		}
	}
	if b.Total < b.Sent+b.Blocked+b.Failed {
		b.Total = b.Sent + b.Blocked + b.Failed
	}
	if err := s.save(&b, true); err != nil {
		return err
	}
	if s.Event != nil {
		s.Event(b.BotID, "broadcast_finished", map[string]any{"id": b.ID, "sent": b.Sent, "blocked": b.Blocked, "failed": b.Failed})
	}
	return nil
}

// addError counts errors by Telegram's description (at most 20 kinds).
func (b *B) addError(desc string) {
	if b.Errors == nil {
		b.Errors = map[string]int{}
	}
	desc = strings.TrimSpace(desc)
	if _, ok := b.Errors[desc]; !ok && len(b.Errors) >= 20 {
		desc = "other"
	}
	b.Errors[desc]++
}
