package backup

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestFullRetentionDownloadAndSchedule(t *testing.T) {
	files := FullFiles(filepath.Join(t.TempDir(), "full"))
	now := time.Now()
	files.Now = func() time.Time { return now }
	for range 3 {
		if _, err := files.Create(context.Background(), 0, "test.backup", func(w io.Writer) error { _, err := w.Write([]byte("encrypted")); return err }); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	if err := Retain(files, 2); err != nil {
		t.Fatal(err)
	}
	list, err := files.List()
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	ref := FileRef{ID: list[0].ID}
	if err := files.MarkDownloaded(ref); err != nil {
		t.Fatal(err)
	}
	info, err := files.Info(ref)
	if err != nil || info.DownloadedAt.IsZero() {
		t.Fatal(info, err)
	}
	cfg := DefaultFullSettings()
	cfg.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	next := cfg.Next(time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC))
	if next.Day() != 3 || next.Hour() != 3 || next.Location() != time.UTC {
		t.Fatal(next)
	}
	cfg.Cron = "0 0 31 2 *"
	if err := cfg.Validate(); err == nil {
		t.Fatal("impossible schedule accepted")
	}
	cfg = DefaultFullSettings()
	cfg.Keep = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero retention accepted")
	}
}
