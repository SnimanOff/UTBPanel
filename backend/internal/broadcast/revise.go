package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
)

// Revisions of a sent broadcast.
const (
	Editing  = "editing"
	Deleting = "deleting"
	Edited   = "edited"
	Deleted  = "deleted"
)

// RevisableFor is how long a broadcast can be edited or recalled: Telegram
// lets a bot delete its messages for 48 hours.
var RevisableFor = 48 * time.Hour

// ErrTooLate: the 48 hours are over.
var ErrTooLate = errors.New("broadcast: older than 48 hours")

// Editor is the part of the Telegram client revisions use.
type Editor interface {
	EditMessageText(ctx context.Context, token string, p telegram.EditMessage) error
	EditMessageCaption(ctx context.Context, token string, p telegram.EditMessage) error
	DeleteMessage(ctx context.Context, token string, chat, message int64) error
}

// revisable checks that b may be edited or recalled now.
func (s *Service) revisable(ctx context.Context, id int64) (B, error) {
	b, err := s.Get(ctx, id)
	if err != nil {
		return b, err
	}
	if b.State == Running || b.State == Scheduled || b.Revision == Editing || b.Revision == Deleting || b.Revision == Deleted {
		return b, ErrState
	}
	if b.RevisableUntil.IsZero() || time.Now().After(b.RevisableUntil) {
		return b, ErrTooLate
	}
	if _, ok := s.TG.(Editor); !ok {
		return b, errors.New("broadcast: the Telegram client cannot edit messages")
	}
	return b, nil
}

// Edit replaces the text and the buttons of every delivered message. A
// broadcast with a picture gets a new caption; the picture stays.
func (s *Service) Edit(ctx context.Context, id int64, text string, buttons []Button) error {
	b, err := s.revisable(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return ErrEmpty
	}
	bs, _ := json.Marshal(nonNil(buttons))
	_, err = s.DB.W.ExecContext(ctx, `UPDATE broadcasts SET text = ?, buttons = ?, revision = ?, rev_cursor = 0, rev_done = 0, rev_failed = 0 WHERE id = ? AND revision = ?`,
		text, string(bs), Editing, id, b.Revision)
	return err
}

// Recall deletes every delivered message; a paused broadcast is cancelled.
func (s *Service) Recall(ctx context.Context, id int64) error {
	b, err := s.revisable(ctx, id)
	if err != nil {
		return err
	}
	if b.State == Paused {
		if err := s.setState(ctx, id, Cancelled, Paused); err != nil {
			return err
		}
	}
	_, err = s.DB.W.ExecContext(ctx, `UPDATE broadcasts SET revision = ?, rev_cursor = 0, rev_done = 0, rev_failed = 0 WHERE id = ? AND revision = ?`,
		Deleting, id, b.Revision)
	return err
}

// Prune forgets message ids older than RevisableFor.
func (s *Service) Prune(ctx context.Context) {
	if _, err := s.DB.W.ExecContext(ctx, `DELETE FROM broadcast_messages WHERE sent_at < ?`, time.Now().Add(-RevisableFor).Unix()); err != nil && s.Log != nil {
		s.Log.Warn("pruning broadcast message ids failed", "err", err)
	}
}

// harmless: the message is already as wanted or gone.
func harmless(te *telegram.Error) bool {
	d := strings.ToLower(te.Description)
	return te.Code == 403 || strings.Contains(d, "message is not modified") ||
		strings.Contains(d, "message to delete not found") || strings.Contains(d, "message to edit not found") ||
		strings.Contains(d, "message can't be deleted")
}

func (s *Service) saveRevision(b *B, final string) {
	q := `UPDATE broadcasts SET rev_cursor = ?, rev_done = ?, rev_failed = ? WHERE id = ?`
	args := []any{b.revCursor, b.RevDone, b.RevFailed, b.ID}
	if final != "" {
		q = `UPDATE broadcasts SET rev_cursor = ?, rev_done = ?, rev_failed = ?, revision = ? WHERE id = ? AND revision = ?`
		args = []any{b.revCursor, b.RevDone, b.RevFailed, final, b.ID, b.Revision}
	}
	// Progress is written even when the run was cancelled.
	if _, err := s.DB.W.ExecContext(context.Background(), q, args...); err != nil && s.Log != nil {
		s.Log.Warn("saving broadcast revision failed", "id", b.ID, "err", err)
	}
}

// revise runs an edit or a recall until it ends; a restart continues it.
func (s *Service) revise(ctx context.Context, id int64) error {
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if b.Revision != Editing && b.Revision != Deleting {
		return nil
	}
	ed, ok := s.TG.(Editor)
	if !ok {
		return errors.New("broadcast: the Telegram client cannot edit messages")
	}
	token, err := s.Token(ctx, b.BotID)
	if err != nil {
		return err
	}
	photo := len(b.Photo) > 0 || b.fileID != ""
	n := 0
	defer func() { s.saveRevision(&b, "") }()
	for {
		rows, err := s.DB.R.QueryContext(ctx, `SELECT user_id, message_id FROM broadcast_messages WHERE broadcast_id = ? AND user_id > ? ORDER BY user_id LIMIT 200`,
			b.ID, b.revCursor)
		if err != nil {
			return err
		}
		type target struct{ user, msg int64 }
		var batch []target
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.user, &t.msg); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, t)
		}
		_ = rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, t := range batch {
			for {
				if err := s.Limit.Wait(ctx, b.BotID); err != nil {
					return err
				}
				var err error
				if b.Revision == Deleting {
					err = ed.DeleteMessage(ctx, token, t.user, t.msg)
				} else {
					p := telegram.EditMessage{ChatID: t.user, MessageID: t.msg, ParseMode: b.ParseMode, ReplyMarkup: markup(b.Buttons)}
					if photo {
						p.Caption = b.Text
						err = ed.EditMessageCaption(ctx, token, p)
					} else {
						p.Text = b.Text
						err = ed.EditMessageText(ctx, token, p)
					}
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				te, isTG := telegram.AsError(err)
				switch {
				case err == nil || (isTG && harmless(te)):
					b.RevDone++
				case isTG && te.Code == 429:
					wait := time.Duration(te.RetryAfter) * time.Second
					if wait <= 0 {
						wait = time.Second
					}
					s.Limit.Pause(b.BotID, wait)
					continue
				default:
					b.RevFailed++
				}
				break
			}
			b.revCursor = t.user
			if n++; n%25 == 0 {
				s.saveRevision(&b, "")
			}
		}
	}
	final := Edited
	if b.Revision == Deleting {
		final = Deleted
		if _, err := s.DB.W.ExecContext(ctx, `DELETE FROM broadcast_messages WHERE broadcast_id = ?`, b.ID); err != nil && s.Log != nil {
			s.Log.Warn("forgetting recalled message ids failed", "id", b.ID, "err", err)
		}
	}
	s.saveRevision(&b, final)
	if s.Event != nil {
		s.Event(b.BotID, "broadcast_revised", map[string]any{"id": b.ID, "revision": final, "done": b.RevDone, "failed": b.RevFailed})
	}
	return nil
}
