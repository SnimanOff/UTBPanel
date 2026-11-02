// Package alerts turns core and panel events into notifications: the feed
// under the bell, red frames on cards and Windows notifications from gate.
// Repeats are glued by a key and shown at most once per RepeatEvery; a
// recovery closes the problem with a separate "back to normal".
package alerts

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// RepeatEvery: a repeat within it adds to the count, after it is new.
var RepeatEvery = 30 * time.Minute

// Notification is one row of the feed.
type Notification struct {
	ID       int64     `json:"id"`
	TS       time.Time `json:"ts"`
	LastTS   time.Time `json:"last_ts"`
	BotID    int64     `json:"bot_id,omitempty"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Text     string    `json:"text"`
	Href     string    `json:"href,omitempty"`
	Count    int       `json:"count"`
	Read     bool      `json:"read"`
	Resolved bool      `json:"resolved"`
}

// Note is what an event becomes: Key glues repeats, Resolves closes keys.
type Note struct {
	Kind, Severity, Key, Text, Href string
	Resolves                        []string
}

// Service keeps notifications and pushes new ones to subscribers.
type Service struct {
	DB  *store.DB
	Now func() time.Time
	// Name returns a bot's name for texts.
	Name func(botID int64) string

	mu    sync.Mutex
	subs  map[chan Notification]struct{}
	queue chan events.Event
	once  sync.Once
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) name(id int64) string {
	if id == 0 {
		return "Сервер"
	}
	if s.Name != nil {
		if n := s.Name(id); n != "" {
			return n
		}
	}
	return "бот " + strconv.FormatInt(id, 10)
}

// OnCoreEvent queues an event: it arrives on the core connection, which
// must not wait for the bot name lookup. One worker keeps the order
// (a crash before the restart that resolves it).
func (s *Service) OnCoreEvent(ev events.Event) {
	if _, ok := Map(ev); !ok {
		return
	}
	s.once.Do(func() {
		s.queue = make(chan events.Event, 1024)
		go func() {
			for ev := range s.queue {
				s.Handle(ev)
			}
		}()
	})
	select {
	case s.queue <- ev:
	default: // a flood of events: drop rather than block the core link
	}
}

// Handle maps an event and stores the notification right away.
func (s *Service) Handle(ev events.Event) {
	if n, ok := Map(ev); ok {
		s.Notify(context.Background(), ev.BotID, n)
	}
}

// Notify stores a note: a new row, a repeat, or a recovery.
func (s *Service) Notify(ctx context.Context, botID int64, n Note) {
	now := s.now()
	for _, key := range n.Resolves {
		res, err := s.DB.W.ExecContext(ctx, `UPDATE notifications SET resolved = 1 WHERE key = ? AND resolved = 0`, key)
		if err != nil {
			continue
		}
		if k, _ := res.RowsAffected(); k > 0 && n.Text == "" {
			s.insert(ctx, botID, Note{Kind: "recovered", Severity: "ok", Key: key + ":ok", Text: "вернулось в норму", Href: n.Href}, now)
		}
	}
	if n.Text == "" {
		return
	}
	if n.Key == "" {
		n.Key = n.Kind + ":" + strconv.FormatInt(botID, 10)
	}
	var id int64
	err := s.DB.R.QueryRowContext(ctx, `SELECT id FROM notifications WHERE key = ? AND resolved = 0 AND last_ts >= ? ORDER BY id DESC LIMIT 1`,
		n.Key, now.Add(-RepeatEvery).Unix()).Scan(&id)
	if err == nil {
		// A repeat: counted, not shown again.
		s.DB.W.ExecContext(ctx, `UPDATE notifications SET count = count + 1, last_ts = ?, text = ? WHERE id = ?`, now.Unix(), s.text(botID, n.Text), id)
		return
	}
	if err != sql.ErrNoRows {
		return
	}
	s.insert(ctx, botID, n, now)
}

func (s *Service) text(botID int64, t string) string {
	return s.name(botID) + ": " + t
}

func (s *Service) insert(ctx context.Context, botID int64, n Note, now time.Time) {
	text := s.text(botID, n.Text)
	res, err := s.DB.W.ExecContext(ctx, `INSERT INTO notifications (ts, last_ts, bot_id, kind, severity, key, text, href) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		now.Unix(), now.Unix(), botID, n.Kind, n.Severity, n.Key, text, n.Href)
	if err != nil {
		return
	}
	id, _ := res.LastInsertId()
	s.publish(Notification{ID: id, TS: now.UTC(), LastTS: now.UTC(), BotID: botID, Kind: n.Kind, Severity: n.Severity, Text: text, Href: n.Href, Count: 1})
}

func (s *Service) publish(n Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- n:
		default: // a slow reader misses it; the list has it
		}
	}
}

// Subscribe returns new notifications until cancel is called.
func (s *Service) Subscribe() (<-chan Notification, func()) {
	ch := make(chan Notification, 16)
	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[chan Notification]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// List returns the newest notifications.
func (s *Service) List(ctx context.Context, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT id, ts, last_ts, bot_id, kind, severity, text, href, count, read, resolved
		FROM notifications ORDER BY last_ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var ts, last int64
		if err := rows.Scan(&n.ID, &ts, &last, &n.BotID, &n.Kind, &n.Severity, &n.Text, &n.Href, &n.Count, &n.Read, &n.Resolved); err != nil {
			return nil, err
		}
		n.TS, n.LastTS = time.Unix(ts, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkRead marks ids, or everything when ids is empty.
func (s *Service) MarkRead(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		_, err := s.DB.W.ExecContext(ctx, `UPDATE notifications SET read = 1 WHERE read = 0`)
		return err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := s.DB.W.ExecContext(ctx, `UPDATE notifications SET read = 1 WHERE id IN (`+ph+`)`, args...)
	return err
}

// Problems are bots with an unread, unresolved error of the last day: their
// cards get a red frame.
func (s *Service) Problems(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT DISTINCT bot_id FROM notifications WHERE severity = 'error' AND read = 0 AND resolved = 0 AND bot_id != 0 AND last_ts >= ?`,
		s.now().Add(-24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

// Prune keeps 30 days.
func (s *Service) Prune(ctx context.Context) {
	s.DB.W.ExecContext(ctx, `DELETE FROM notifications WHERE last_ts < ?`, s.now().Add(-30*24*time.Hour).Unix())
}

func str(ev events.Event, k string) string {
	v, _ := ev.Data[k].(string)
	return v
}

func num(ev events.Event, k string) string {
	switch v := ev.Data[k].(type) {
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	}
	return fmt.Sprint(ev.Data[k])
}

// Map turns a core event into a note; ok=false means no notification.
func Map(ev events.Event) (Note, bool) {
	id := strconv.FormatInt(ev.BotID, 10)
	bot := "/bots/" + id
	switch ev.Type {
	case "bot_crashed":
		return Note{Kind: "crash", Severity: "error", Key: "crash:" + id, Text: "упал (код " + num(ev, "exit") + ")", Href: bot + "/logs"}, true
	case "bot_crash_loop":
		return Note{Kind: "crash_loop", Severity: "error", Key: "loop:" + id, Text: "рестарт по кругу: " + num(ev, "crashes") + " падений", Href: bot + "/logs"}, true
	case "bot_started":
		return Note{Resolves: []string{"crash:" + id, "loop:" + id}, Href: bot}, true
	case "autorollback":
		return Note{Kind: "autorollback", Severity: "error", Key: "rollback:" + id + ":" + num(ev, "id"),
			Text: "автооткат " + str(ev, "from") + " → " + str(ev, "to") + ": " + str(ev, "reason"), Href: bot + "/deploy"}, true
	case "deploy":
		if str(ev, "result") == "failed" {
			return Note{Kind: "deploy_failed", Severity: "error", Key: "deployfail:" + id, Text: "деплой " + str(ev, "version") + " не прошёл: " + str(ev, "error"), Href: bot + "/deploy"}, true
		}
	case "deploy_worse":
		return Note{Kind: "deploy_worse", Severity: "warn", Key: "worse:" + id + ":" + str(ev, "version"), Text: "после деплоя " + str(ev, "version") + " стало хуже", Href: bot + "/deploy"}, true
	case "canary_cancelled":
		return Note{Kind: "canary", Severity: "error", Key: "canary:" + id, Text: "канарейка " + str(ev, "version") + " отменена: " + str(ev, "reason"), Href: bot + "/deploy"}, true
	case "canary":
		if str(ev, "action") == "promoted" {
			return Note{Kind: "canary", Severity: "info", Key: "canary:" + id + ":promoted", Text: "канарейка " + str(ev, "version") + " переведена на всех", Href: bot + "/deploy"}, true
		}
	case "service_down":
		h := str(ev, "host")
		return Note{Kind: "service", Severity: "warn", Key: "svc:" + id + ":" + h, Text: "недоступен " + h, Href: bot + "/network"}, true
	case "service_recovered":
		return Note{Resolves: []string{"svc:" + id + ":" + str(ev, "host")}, Href: bot + "/network"}, true
	case "bot_bypasses_proxy":
		return Note{Kind: "proxy", Severity: "warn", Key: "bypass:" + id, Text: "ходит в Telegram мимо прокси", Href: bot + "/network"}, true
	case "db_backup_failed":
		return Note{Kind: "backup", Severity: "error", Key: "backup:" + id, Text: "бэкап базы не прошёл: " + str(ev, "error"), Href: bot + "/db"}, true
	case "db_check_failed":
		return Note{Kind: "backup", Severity: "error", Key: "check:" + id, Text: "проверка базы не прошла", Href: bot + "/db"}, true
	case "full_backup_failed":
		return Note{Kind: "backup", Severity: "error", Key: "full-backup:failed", Text: "Полный бэкап не создан: " + str(ev, "error"), Href: "/server"}, true
	case "full_backup_verification_failed":
		return Note{Kind: "backup", Severity: "error", Key: "full-backup:verify:" + str(ev, "id"), Text: "Архив " + str(ev, "name") + " не прошёл проверку: " + str(ev, "error"), Href: "/server"}, true
	case "full_backup_verification_skipped":
		return Note{Kind: "backup", Severity: "warn", Key: "full-backup:verify:" + str(ev, "id"), Text: "Архив " + str(ev, "name") + ": проверка пропущена — " + str(ev, "warning"), Href: "/server"}, true
	case "full_backup_download_reminder":
		return Note{Kind: "backup", Severity: "warn", Key: "full-backup:download", Text: str(ev, "message"), Href: "/server"}, true
	case "telegram_429":
		return Note{Kind: "telegram_429", Severity: "warn", Key: "429:" + id, Text: "Telegram ограничил частоту (" + str(ev, "method") + ", ждать " + num(ev, "retry_after") + " с)", Href: bot}, true
	case "limit_reached":
		return Note{Kind: "limit", Severity: "error", Key: "limit:" + id + ":" + str(ev, "limit"), Text: "упёрся в лимит " + str(ev, "limit"), Href: bot + "/settings"}, true
	case "limit_warning":
		return Note{Kind: "limit", Severity: "warn", Key: "limitw:" + id + ":" + str(ev, "limit"), Text: "близок к лимиту " + str(ev, "limit"), Href: bot + "/settings"}, true
	case "maintenance_started":
		return Note{Kind: "maintenance", Severity: "info", Key: "maint:" + id, Text: "техработы начались", Href: bot + "/deploy"}, true
	case "maintenance_ended":
		return Note{Kind: "maintenance", Severity: "info", Key: "maintend:" + id, Text: "техработы закончились", Href: bot + "/deploy",
			Resolves: []string{"maintlate:" + id, "maintdeploy:" + id}}, true
	case "maintenance_overdue":
		return Note{Kind: "maintenance", Severity: "warn", Key: "maintlate:" + id, Text: "техработы затянулись: продлите или закончите", Href: bot + "/deploy"}, true
	case "maintenance_deploy_failed":
		return Note{Kind: "maintenance", Severity: "error", Key: "maintdeploy:" + id, Text: "деплой на техработах не прошёл, техработы продолжаются", Href: bot + "/deploy"}, true
	case "broadcast_finished":
		return Note{Kind: "broadcast", Severity: "info", Key: "bc:" + id + ":" + num(ev, "id"),
			Text: "рассылка завершена: доставлено " + num(ev, "sent") + ", заблокировали " + num(ev, "blocked") + ", ошибок " + num(ev, "failed"), Href: bot + "/broadcast"}, true
	case "github_release":
		return Note{Kind: "release", Severity: "info", Key: "gh:" + id + ":" + str(ev, "version"), Text: "новый релиз " + str(ev, "version"), Href: bot + "/deploy"}, true
	case "github_release_failed":
		return Note{Kind: "release", Severity: "warn", Key: "ghfail:" + id + ":" + str(ev, "tag"), Text: "релиз " + str(ev, "tag") + " не загружен: " + str(ev, "error"), Href: bot + "/deploy"}, true
	case "panic":
		return Note{Kind: "panic", Severity: "error", Key: "panic:" + id + ":" + str(ev, "signature"), Text: "паника: " + str(ev, "kind"), Href: bot + "/panics"}, true
	case "alert", "alert_ok", "disk_low", "new_device", "mcp_confirm", "mcp_secret":
		// Panel-side events carry their text.
		n := Note{Kind: ev.Type, Severity: ev.Severity, Key: str(ev, "key"), Text: str(ev, "text"), Href: str(ev, "href")}
		if ev.Type == "alert_ok" {
			n = Note{Resolves: []string{str(ev, "key")}, Href: str(ev, "href")}
		}
		return n, true
	}
	return Note{}, false
}
