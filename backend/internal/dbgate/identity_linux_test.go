//go:build linux

package dbgate

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFilesystemIdentityCannotReadCoreFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o711)
	os.Chmod(filepath.Dir(dir), 0o711)
	protected := filepath.Join(dir, "core-secret")
	if err := os.WriteFile(protected, []byte("secret"), 0o640); err != nil {
		t.Fatal(err)
	}
	botDir := filepath.Join(dir, "bot")
	os.Mkdir(botDir, 0o750)
	const uid = 12345
	if err := os.Chown(botDir, uid, uid); err != nil {
		t.Fatal(err)
	}
	botFile := filepath.Join(botDir, "own-file")
	if err := withIdentity(uid, func() error {
		if _, err := os.ReadFile(protected); !os.IsPermission(err) {
			t.Fatalf("bot read core file: %v", err)
		}
		return os.WriteFile(botFile, []byte("own"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(botFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sys().(*syscall.Stat_t).Uid != uid {
		t.Fatal("file created under core identity")
	}
	if _, err := os.ReadFile(protected); err != nil {
		t.Fatalf("core identity was not restored: %v", err)
	}
}
