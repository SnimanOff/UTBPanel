package migrations

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

func TestDatabasesCreate(t *testing.T) {
	for name, fsys := range map[string]fs.FS{"core": Core(), "panel": Panel()} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), name+".db")
			for range 2 { // the second open must change nothing
				db, err := store.Open(context.Background(), file, fsys)
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
		})
	}
}
