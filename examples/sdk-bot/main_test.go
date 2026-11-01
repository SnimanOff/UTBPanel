package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	sdkdb "github.com/SnimanOff/UTBPanel/backend/sdk/db"
)

func TestAutonomousOrdersAreIdempotent(t *testing.T) {
	t.Setenv("TGPANEL_SDK", "")
	t.Setenv("TGPANEL_FLAG_NEW_CHECKOUT", "true")
	t.Setenv("DATABASE_URL", "file:"+filepath.Join(t.TempDir(), "bot.db"))
	k := sdk.New()
	defer k.Close()
	db, err := sdkdb.Open(k)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	b := &bot{db: db, secret: "test", checkout: k.Flag("new_checkout", false, ""), orders: k.Counter("orders_created"), total: k.Value("orders_total"), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"update_id":1,"message":{"chat":{"id":7},"text":"/buy"}}`))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test")
		w := httptest.NewRecorder()
		b.webhook(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "#1") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%d %v", count, err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	b.webhook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

func TestRunAutonomousHTTPAndShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	t.Setenv("PORT", fmt.Sprint(port))
	t.Setenv("TGPANEL_SDK", "")
	t.Setenv("DATABASE_URL", "file:"+filepath.Join(t.TempDir(), "bot.db"))
	t.Setenv("WEBHOOK_SECRET", "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	client := &http.Client{Timeout: time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP server did not start")
		}
		time.Sleep(time.Millisecond)
	}
	r, err := http.NewRequest(http.MethodPost, url+"/", strings.NewReader(`{"update_id":1,"message":{"chat":{"id":7},"text":"   "}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP server did not stop")
	}
}
