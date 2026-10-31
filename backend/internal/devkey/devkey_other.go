//go:build !windows

package devkey

import (
	"fmt"
	"path/filepath"
)

func create(dir, _ string, _ bool) (Key, string, error) {
	k, err := createFileKey(filepath.Join(dir, "key.pem"))
	return k, "file:key.pem", err
}

func openRef(dir, kind, arg string) (Key, error) {
	if kind != "file" {
		return nil, fmt.Errorf("devkey: %s keys are not supported on this system", kind)
	}
	return openFileKey(filepath.Join(dir, arg))
}
