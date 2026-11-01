// Command sdk-bot demonstrates database migrations, explicit readiness,
// custom metrics, UTC tasks and a runtime flag. It also runs without a panel.
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	sdkdb "github.com/SnimanOff/UTBPanel/backend/sdk/db"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
	"github.com/SnimanOff/UTBPanel/backend/sdk/metrics"
)

type bot struct {
	db       *sql.DB
	secret   string
	checkout *flags.Bool
	orders   *metrics.Counter
	total    *metrics.Value
	log      *slog.Logger
}

type update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than this bot", version)
	}
	if version == 1 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE orders(id INTEGER PRIMARY KEY,update_id INTEGER NOT NULL UNIQUE,chat_id INTEGER NOT NULL,created_at TEXT NOT NULL);INSERT INTO schema_migrations(version) VALUES(1)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *bot) placeOrder(ctx context.Context, u update) (string, error) {
	var id int64
	err := b.db.QueryRowContext(ctx, `SELECT id FROM orders WHERE update_id=?`, u.ID).Scan(&id)
	if err == nil {
		return fmt.Sprintf("Заказ #%d уже принят.", id), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !b.checkout.On() {
		return "Оформление заказов пока выключено.", nil
	}
	res, err := b.db.ExecContext(ctx, `INSERT INTO orders(update_id,chat_id,created_at) VALUES(?,?,?) ON CONFLICT(update_id) DO NOTHING`, u.ID, u.Message.Chat.ID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	if affected, _ := res.RowsAffected(); affected > 0 {
		b.orders.Add(1)
	}
	if err := b.db.QueryRowContext(ctx, `SELECT id FROM orders WHERE update_id=?`, u.ID).Scan(&id); err != nil {
		return "", err
	}
	var total int64
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&total); err == nil {
		b.total.Set(float64(total))
	}
	return fmt.Sprintf("Заказ #%d принят.", id), nil
}

func (b *bot) webhook(w http.ResponseWriter, r *http.Request) {
	if b.secret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(b.secret)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var u update
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&u); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if u.Message == nil || u.Message.Chat.ID == 0 || u.Message.Text == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	words := strings.Fields(u.Message.Text)
	if len(words) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	command := words[0]
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	text := "Команды: /start, /buy"
	if command == "/start" {
		text = "Привет! /buy создаёт заказ, когда включён переключатель new_checkout."
	}
	if command == "/buy" {
		var err error
		text, err = b.placeOrder(r.Context(), u)
		if err != nil {
			b.log.Error("order failed", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"method": "sendMessage", "chat_id": u.Message.Chat.ID, "text": text})
}

func run(ctx context.Context, log *slog.Logger) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	if os.Getenv("DATABASE_URL") == "" {
		os.Setenv("DATABASE_URL", "file:bot.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	}
	kit := sdk.New(sdk.WithLogger(log))
	defer kit.Close()
	db, err := sdkdb.Open(kit)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	if err := migrate(ctx, db); err != nil {
		return err
	}
	b := &bot{db: db, secret: os.Getenv("WEBHOOK_SECRET"), checkout: kit.Flag("new_checkout", false, "Разрешить создание заказов"), orders: kit.Counter("orders_created"), total: kit.Value("orders_total"), log: log}
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&total); err != nil {
		return err
	}
	b.total.Set(float64(total))
	if err := kit.Task("daily_digest", "0 9 * * *", func(ctx context.Context) error {
		var count int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&count); err != nil {
			return err
		}
		log.Info("daily digest", "orders", count)
		return nil
	}); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", b.webhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := kit.Ready(); err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Info("ready", "port", port, "panel", kit.Connected())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if err := run(ctx, log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
