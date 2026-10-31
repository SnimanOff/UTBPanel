package logs

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDetectLevel(t *testing.T) {
	cases := map[string]string{
		`{"level":"ERROR","msg":"x"}`: LevelError,
		`{"level":"warn"}`:            LevelWarn,
		`panic: runtime error`:        LevelError,
		`2026/10/31 error: db down`:   LevelError,
		`WARNING disk`:                LevelWarn,
		`terrors are fine`:            LevelInfo,
		`started`:                     LevelInfo,
	}
	for in, want := range cases {
		if got := DetectLevel(in, "stdout"); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestRotationAndSearch(t *testing.T) {
	oldSize, oldKeep := FileSize, KeepFiles
	FileSize, KeepFiles = 2000, 3
	t.Cleanup(func() { FileSize, KeepFiles = oldSize, oldKeep })
	dir := t.TempDir()
	s := New(func(int64) string { return dir }, nil)
	defer s.Close()
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for i := range 200 {
		text := fmt.Sprintf("line %d", i)
		if i%50 == 0 {
			text = "error at " + fmt.Sprint(i)
		}
		s.Add(1, "stdout", text, base.Add(time.Duration(i)*time.Second))
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) > KeepFiles {
		t.Fatalf("%d files kept", len(ents))
	}
	got, err := s.Search(1, Filter{MinLevel: LevelError}, time.Time{}, time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Old files were dropped; the last error lines remain, oldest first.
	if len(got) == 0 || got[len(got)-1].Text != "error at 150" {
		t.Fatalf("search = %+v", got)
	}
	tail := s.Tail(1, 5, Filter{})
	if len(tail) != 5 || tail[4].Text != "line 199" {
		t.Fatalf("tail = %+v", tail)
	}
	if q := s.Tail(1, 100, Filter{Query: "LINE 19"}); len(q) != 11 {
		t.Fatalf("query tail = %d", len(q))
	}
}

func TestSubscribe(t *testing.T) {
	s := New(func(int64) string { return t.TempDir() }, nil)
	defer s.Close()
	sub := s.Subscribe(1, Filter{MinLevel: LevelWarn})
	s.Add(1, "stdout", "hello", time.Now())
	s.Add(1, "stderr", "warning: disk", time.Now())
	select {
	case l := <-sub.C:
		if l.Text != "warning: disk" {
			t.Fatalf("got %+v", l)
		}
	case <-time.After(time.Second):
		t.Fatal("no line")
	}
	s.Unsubscribe(1, sub)
}

func TestZip(t *testing.T) {
	dir := t.TempDir()
	s := New(func(int64) string { return dir }, nil)
	s.Add(1, "core", "starting version v1", time.Now())
	s.Add(1, "stdout", "hi", time.Now())
	var buf bytes.Buffer
	if err := s.Zip(1, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("%v %d", err, len(zr.File))
	}
	f, _ := zr.File[0].Open()
	var out bytes.Buffer
	out.ReadFrom(f)
	if !strings.Contains(out.String(), "[core] I starting version v1") {
		t.Fatalf("zip = %s", out.String())
	}
	s.Close()
}
