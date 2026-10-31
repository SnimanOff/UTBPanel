// Package migrations embeds the schema of core.db and panel.db.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed core/*.sql panel/*.sql
var files embed.FS

func sub(dir string) fs.FS {
	f, err := fs.Sub(files, dir)
	if err != nil {
		panic(err)
	}
	return f
}

// Core returns migrations of core.db.
func Core() fs.FS { return sub("core") }

// Panel returns migrations of panel.db.
func Panel() fs.FS { return sub("panel") }
