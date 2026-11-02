package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreInstallRollbackAndCommit(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "restore", "stage")
	os.MkdirAll(filepath.Join(stage, "bots"), 0o700)
	os.WriteFile(filepath.Join(stage, "core.db"), []byte("new"), 0o600)
	os.WriteFile(filepath.Join(dir, "core.db"), []byte("old"), 0o600)
	items := []InstallItem{{Source: filepath.Join(stage, "core.db"), Target: filepath.Join(dir, "core.db")}, {Source: filepath.Join(stage, "bots"), Target: filepath.Join(dir, "bots")}}
	if err := InstallThen(dir, items, func() error { return errors.New("panel refused") }); err == nil {
		t.Fatal("refusal ignored")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "core.db"))
	if string(raw) != "old" {
		t.Fatal("original database not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "bots")); !os.IsNotExist(err) {
		t.Fatal("new directory not rolled back")
	}
	os.MkdirAll(filepath.Join(stage, "bots"), 0o700)
	os.WriteFile(filepath.Join(stage, "core.db"), []byte("new"), 0o600)
	if err := Install(dir, items); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "core.db"))
	if string(raw) != "new" {
		t.Fatal("new database not installed")
	}
	if err := RecoverInstall(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreInstallRecoversInterruptedRenames(t *testing.T) {
	dir := t.TempDir()
	items := []InstallItem{{Source: filepath.Join(dir, "stage", "core.db"), Target: filepath.Join(dir, "core.db"), Original: true}, {Source: filepath.Join(dir, "stage", "bots"), Target: filepath.Join(dir, "bots")}}
	if err := os.WriteFile(items[0].Target+".restore-old", []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(items[0].Target, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(items[1].Target, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(items)
	if err := os.WriteFile(filepath.Join(dir, "restore-install.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInstall(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(items[0].Target)
	if err != nil || string(raw) != "original" {
		t.Fatal(string(raw), err)
	}
	if _, err := os.Stat(items[1].Target); !os.IsNotExist(err) {
		t.Fatal("new bots directory survived rollback", err)
	}
}

func TestRestoreInstallDiscardsCommittedLeftoversBeforeRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "core.db")
	if err := os.WriteFile(target+".restore-old", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, []InstallItem{{Source: filepath.Join(dir, "missing.db"), Target: target}}); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("stale original became the live database", err)
	}
}
