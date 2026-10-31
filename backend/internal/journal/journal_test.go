package journal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestJournal(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := New(db, nil, nil)
	j.Do(ctx, Actor{Source: SourceUI, Name: "home-pc", IP: "1.2.3.4"}, "bots.start", 1, nil, func() error { return nil })
	j.Do(ctx, Actor{Source: SourceCLI, Name: "ssh"}, "bots.stop", 1, map[string]string{"name": "echo"}, func() error { return errors.New("boom") })
	es, err := j.Since(ctx, 0, 10)
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	if es[0].Actor.Name != "home-pc" || es[0].Result != "ok" || es[1].Result != "error: boom" || string(es[1].Args) != `{"name":"echo"}` {
		t.Fatalf("entries = %+v", es)
	}
	if es, _ := j.Since(ctx, es[1].ID, 10); len(es) != 0 {
		t.Fatal("since after last")
	}
}
