package dbgate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestWeeklyChecks(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runs := 0
	bad := 0
	c := &Checks{DB: db, Table: "kv",
		Run: func(ctx context.Context) []Check {
			runs++
			return []Check{CheckFile(ctx, "core.db", db.Path, 0), {Name: "bot.db x", BotID: 2, At: time.Now(), Error: "page 3 broken"}}
		},
		OnBad: func(Check) { bad++ },
	}
	c.RunNow(ctx)
	last := c.Last(ctx)
	if len(last) != 2 || !last[0].OK || last[1].OK || bad != 1 || runs != 1 {
		t.Fatalf("%+v runs=%d bad=%d", last, runs, bad)
	}
}
