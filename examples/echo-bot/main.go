// Command echo-bot is the reference bot without any panel dependency: a
// plain webhook bot that takes its settings from the environment and runs
// anywhere — in the panel, locally, or at another host.
//
//	BOT_TOKEN       token from BotFather
//	PORT            port on 127.0.0.1 for the webhook
//	WEBHOOK_SECRET  compared with X-Telegram-Bot-Api-Secret-Token
//	DATABASE_URL    file:… — SQLite file (modernc.org/sqlite, no cgo)
//
// It answers with the method in the webhook response body, so it needs no
// HTTP client at all for replies.
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From *struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Text string `json:"text"`
	} `json:"message"`
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	port := os.Getenv("PORT")
	secret := os.Getenv("WEBHOOK_SECRET")
	if port == "" {
		return errors.New("PORT is not set")
	}
	dsn := strings.TrimPrefix(os.Getenv("DATABASE_URL"), "file:")
	if dsn == "" {
		dsn = "bot.db"
	}
	db, err := sql.Open("sqlite", "file:"+dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	// Migrations must stay backward compatible: add, never rename or drop.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS counts (chat_id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if secret != "" && subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var u update
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&u); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if u.Message == nil || u.Message.Text == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		chat := u.Message.Chat.ID
		var n int
		err := db.QueryRowContext(r.Context(), `INSERT INTO counts (chat_id, n) VALUES (?, 1)
			ON CONFLICT (chat_id) DO UPDATE SET n = n + 1 RETURNING n`, chat).Scan(&n)
		if err != nil {
			log.Error("db", "err", err)
		}
		text := u.Message.Text
		if text == "/start" || strings.HasPrefix(text, "/start ") {
			text = "Привет! Я повторяю сообщения."
		} else {
			text = fmt.Sprintf("%s (#%d)", text, n)
		}
		log.Info("message", "chat", chat, "n", n)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"method": "sendMessage", "chat_id": chat, "text": text})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		srv.Shutdown(sctx) // finish updates in flight, then exit
	}()
	log.Info("listening", "port", port)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
