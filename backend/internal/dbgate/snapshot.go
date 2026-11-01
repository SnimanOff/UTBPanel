package dbgate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Snapshot copies a live database without opening it with root's filesystem
// permissions. The caller gives dst's directory to uid before calling this.
func Snapshot(ctx context.Context, src, dst string, uid int) error {
	return withIdentity(uid, func() error {
		if _, err := os.Stat(src); os.IsNotExist(err) {
			return ErrNoDB
		} else if err != nil {
			return err
		}
		q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(10000)"}}
		db, err := sql.Open("sqlite", sqliteURI(src, q))
		if err != nil {
			return err
		}
		db.SetMaxOpenConns(1)
		defer db.Close()
		if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
			return fmt.Errorf("database snapshot: %w", err)
		}
		return Integrity(ctx, dst, false)
	})
}

func sqliteURI(path string, q url.Values) string {
	path = filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
}
