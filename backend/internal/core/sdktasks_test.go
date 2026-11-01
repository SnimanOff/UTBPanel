package core

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
	"github.com/SnimanOff/UTBPanel/backend/sdk"
)

func taskKit(t *testing.T, c *Core, instance string) *sdk.Kit {
	t.Helper()
	uid := -1
	if runtime.GOOS == "linux" {
		uid = os.Geteuid()
	}
	if err := c.SDK.Start(1, instance, uid); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TGPANEL_SDK", "unix://"+c.SDK.Path(1, instance))
	if instance == "canary" {
		t.Setenv("TGPANEL_CANARY", "1")
	} else {
		t.Setenv("TGPANEL_CANARY", "")
	}
	k := sdk.New()
	t.Cleanup(func() { k.Close() })
	return k
}

func taskCore(t *testing.T) *Core {
	t.Helper()
	c := newCore(t)
	if _, err := c.DB.W.Exec(`INSERT INTO bots(id,name,port,state,created_at) VALUES(1,'tasks',20001,'running','x')`); err != nil {
		t.Fatal(err)
	}
	c.SDKTasks.Runnable = func(id int64) bool {
		var state string
		c.DB.R.QueryRow(`SELECT state FROM bots WHERE id=?`, id).Scan(&state)
		return state == "running"
	}
	t.Cleanup(func() { c.SDKTasks.Close(); c.SDK.Close() })
	return c
}

func waitTask(t *testing.T, c *Core, id int64) sdkhost.TaskRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := c.SDKTasks.List(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range view.Runs {
			if run.ID == id && run.Finished != "" {
				return run
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task history not finished")
	return sdkhost.TaskRun{}
}

func TestSDKTasksManualPauseAndCanary(t *testing.T) {
	c := taskCore(t)
	k := taskKit(t, c, "")
	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := k.Task("digest", "0 9 * * *", func(ctx context.Context) error { close(started); <-ctx.Done(); close(canceled); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	if err := k.Ready(); err != nil {
		t.Fatal(err)
	}
	canary := taskKit(t, c, "canary")
	if err := canary.Task("digest", "* * * * *", func(context.Context) error { t.Error("canary task ran"); return nil }); err != nil {
		t.Fatal(err)
	}
	view, err := c.SDKTasks.List(context.Background(), 1)
	if err != nil || len(view.Tasks) != 1 || view.Tasks[0].Cron != "0 9 * * *" {
		t.Fatalf("canary replaced main schedule: %+v %v", view, err)
	}
	runID, err := c.SDKTasks.Start(context.Background(), 1, "digest", "manual")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not run")
	}
	if _, err := c.SDKTasks.Start(context.Background(), 1, "digest", "manual"); err == nil {
		t.Fatal("overlapping task started")
	}
	if err := c.Maint.PauseBot(context.Background(), 1, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("pause did not cancel task")
	}
	if run := waitTask(t, c, runID); run.Success || run.Error == "" {
		t.Fatalf("%+v", run)
	}
	if _, err := c.SDKTasks.Start(context.Background(), 1, "digest", "manual"); err == nil {
		t.Fatal("task ran while paused")
	}
}

func TestSDKTaskErrorsAreStoredAndRedacted(t *testing.T) {
	c := taskCore(t)
	k := taskKit(t, c, "")
	c.Sec.Redact.Add("secret-123")
	if err := k.Task("fail", "0 9 * * *", func(context.Context) error { return fmt.Errorf("failure secret-123") }); err != nil {
		t.Fatal(err)
	}
	id, err := c.SDKTasks.Start(context.Background(), 1, "fail", "manual")
	if err != nil {
		t.Fatal(err)
	}
	run := waitTask(t, c, id)
	if run.Success || !strings.Contains(run.Error, "***") || strings.Contains(run.Error, "secret-123") {
		t.Fatalf("%+v", run)
	}
}

func TestSDKTaskCoreSchedule(t *testing.T) {
	c := taskCore(t)
	k := taskKit(t, c, "")
	var calls atomic.Int64
	called := make(chan struct{}, 4)
	if err := k.Task("tick", "@every 1s", func(context.Context) error { calls.Add(1); called <- struct{}{}; return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.SDKTasks.Run(ctx) }()
	select {
	case <-called:
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("core schedule did not run")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate schedule execution: %d", calls.Load())
	}
}
