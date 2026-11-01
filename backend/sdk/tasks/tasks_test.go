package tasks

import (
	"context"
	"testing"
	"time"
)

type client struct{}

func (client) Connected() bool                              { return false }
func (client) Use(string) error                             { return nil }
func (client) Call(context.Context, string, any, any) error { return nil }

func TestAutonomousScheduleAndCanary(t *testing.T) {
	for _, canary := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "canary"}[canary], func(t *testing.T) {
			r := New(client{}, nil, canary)
			defer r.Close()
			called := make(chan struct{}, 2)
			if err := r.Add("digest", "@every 1s", func(context.Context) error { called <- struct{}{}; return nil }); err != nil {
				t.Fatal(err)
			}
			select {
			case <-called:
				if canary {
					t.Fatal("canary owns a schedule")
				}
			case <-time.After(1200 * time.Millisecond):
				if !canary {
					t.Fatal("autonomous schedule did not run")
				}
			}
		})
	}
}

func TestCoreTakeoverCancelsLocalTask(t *testing.T) {
	r := New(client{}, nil, false)
	defer r.Close()
	started := make(chan struct{})
	done := make(chan error, 1)
	if err := r.Add("digest", "0 9 * * *", func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	go func() { done <- r.execute(context.Background(), "digest", true) }()
	<-started
	if err := r.Connected(context.Background(), client{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("local task survived core takeover")
	}
	if err := r.execute(context.Background(), "digest", true); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleValidationAndPanic(t *testing.T) {
	if err := Validate("digest", "CRON_TZ=Europe/Moscow 0 9 * * *"); err == nil {
		t.Fatal("non-UTC cron accepted")
	}
	r := New(client{}, nil, false)
	defer r.Close()
	if err := r.Add("panic", "0 9 * * *", func(context.Context) error { panic("failure") }); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), "panic"); err == nil {
		t.Fatal("panic was not reported")
	}
}
