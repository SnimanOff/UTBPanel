//go:build linux

package botdir

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

func owner(t *testing.T, p string) (uid, gid uint32, mode os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return st.Uid, st.Gid, fi.Mode()
}

// A bot owned its directory and planted links to the core's files there.
// Preparing the directory must neither follow them nor give anything
// outside the directory to the bot.
func TestBotDirLinksAreNotFollowed(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	base := t.TempDir()
	os.Chmod(base, 0o711)
	secret := filepath.Join(base, "master.key")
	os.WriteFile(secret, []byte("key"), 0o600)
	keys := filepath.Join(base, "keys")
	os.Mkdir(keys, 0o700)
	uid := 10007
	dir := filepath.Join(base, "bots", "7")
	os.MkdirAll(filepath.Join(dir, "releases", "v1"), 0o750)
	for _, p := range []string{dir, filepath.Join(dir, "releases"), filepath.Join(dir, "releases", "v1")} {
		os.Chown(p, uid, uid)
	}
	os.WriteFile(filepath.Join(dir, "releases", "v1", "release.json"), []byte("{}"), 0o640)
	links := map[string]string{
		"bot.db":                 secret,
		"bot.db-wal":             secret,
		"bot.db.restore":         secret,
		"files":                  keys,
		"secrets":                keys,
		"logs":                   keys,
		"releases/v2":            keys,
		"releases/v1/bot":        secret,
		"releases/v1/notes.json": secret,
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	if err := Secure(dir, uid); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{secret, keys} {
		if u, _, m := owner(t, p); u != 0 || m.Perm() != map[string]os.FileMode{secret: 0o600, keys: 0o700}[p] {
			t.Errorf("%s changed: uid %d mode %v", p, u, m)
		}
	}
	if u, g, m := owner(t, dir); u != 0 || int(g) != uid || m&os.ModeSticky == 0 || m.Perm() != 0o770 {
		t.Errorf("bot dir: %d:%d %v", u, g, m)
	}
	for name, want := range map[string]os.FileMode{"files": 0o770, "releases": 0o750, "secrets": 0o750, "logs": 0o700, "releases/v1": 0o750} {
		u, _, m := owner(t, filepath.Join(dir, name))
		if u != 0 || !m.IsDir() || m.Perm() != want {
			t.Errorf("%s: uid %d %v", name, u, m)
		}
	}
	for _, name := range []string{"bot.db", "bot.db-wal", "bot.db.restore", "releases/v2", "releases/v1/bot", "releases/v1/notes.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s left: %v", name, err)
		}
	}
	if u, g, m := owner(t, filepath.Join(dir, "releases", "v1", "release.json")); u != 0 || int(g) != uid || m.Perm() != 0o640 {
		t.Errorf("release.json: %d:%d %v", u, g, m)
	}

	// The database the bot made stays the bot's.
	db := filepath.Join(dir, "bot.db")
	os.WriteFile(db, nil, 0o600)
	if err := Secure(dir, uid); err != nil {
		t.Fatal(err)
	}
	if u, _, _ := owner(t, db); int(u) != uid {
		t.Errorf("bot.db owner %d", u)
	}

	// A hard link to a file of the core is refused, not given away.
	os.Remove(db)
	if err := os.Link(secret, db); err != nil {
		t.Fatal(err)
	}
	if err := Secure(dir, uid); err == nil {
		t.Error("hard link accepted")
	}
	if u, _, _ := owner(t, secret); u != 0 {
		t.Error("hard-linked file given to the bot")
	}
	os.Remove(db)

	// File secrets: readable by the bot's group, owned by root.
	files, err := secrets.WriteFiles(filepath.Join(dir, "secrets"), uid, []secrets.Secret{{Name: "CERT", Kind: secrets.KindFile, Value: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if u, g, m := owner(t, files["CERT"]); u != 0 || int(g) != uid || m.Perm() != 0o440 {
		t.Errorf("secret file: %d:%d %v", u, g, m)
	}
}
