package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
)

func testImportArchive(t *testing.T, change func(map[string][]byte, *Manifest)) []byte {
	t.Helper()
	d := botconfig.Document{}
	botconfig.Normalize(&d)
	yaml, err := botconfig.YAML(d)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"config.yaml": []byte(yaml), "users.json": []byte(`{"version":1,"users":[],"allow":[]}`), "payments.json": []byte(`{"version":1,"payments":[]}`)}
	m := Manifest{Format: "utbpanel.bot", Version: 1, Name: "test", Entries: map[string]Entry{}}
	for name, raw := range files {
		hash := sha256.Sum256(raw)
		m.Entries[name] = Entry{Size: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])}
	}
	if change != nil {
		change(files, &m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = raw
	return testEncryptedTar(t, func(tw *tar.Writer) {
		for name, raw := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(raw); err != nil {
				t.Fatal(err)
			}
		}
	})
}
func testEncryptedTar(t *testing.T, fill func(*tar.Writer)) []byte {
	t.Helper()
	var out bytes.Buffer
	r, _ := age.NewScryptRecipient("import test password")
	r.SetWorkFactor(8)
	encrypted, err := age.Encrypt(&out, r)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(encrypted)
	tw := tar.NewWriter(gz)
	fill(tw)
	tw.Close()
	gz.Close()
	encrypted.Close()
	return out.Bytes()
}

func TestImportAuthenticationHashesAndFormat(t *testing.T) {
	valid := testImportArchive(t, nil)
	out, err := Decrypt(context.Background(), bytes.NewReader(valid), "import test password", t.TempDir())
	if err != nil || out.Manifest.Name != "test" {
		t.Fatal(out, err)
	}
	for name, raw := range map[string][]byte{"version": testImportArchive(t, func(_ map[string][]byte, m *Manifest) { m.Version = 2 }), "digest": testImportArchive(t, func(f map[string][]byte, _ *Manifest) { f["users.json"] = []byte("tampered") }), "truncated": valid[:len(valid)-10]} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decrypt(context.Background(), bytes.NewReader(raw), "import test password", t.TempDir()); err == nil {
				t.Fatal("corrupt archive accepted")
			}
		})
	}
	if _, err := Decrypt(context.Background(), bytes.NewReader(valid), "wrong password", t.TempDir()); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestImportChecksBinaryAndDatabase(t *testing.T) {
	for name, change := range map[string]func(map[string][]byte, *Manifest){
		"binary": func(files map[string][]byte, m *Manifest) {
			raw := []byte("not ELF")
			key := "bin/v1/bot"
			files[key] = raw
			sum := sha256.Sum256(raw)
			m.Entries[key] = Entry{Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
			m.CurrentVersion = "v1"
			m.Releases = []deploy.Release{{Version: "v1"}}
		},
		"database": func(files map[string][]byte, m *Manifest) {
			raw := []byte("not SQLite")
			files["bot.db"] = raw
			sum := sha256.Sum256(raw)
			m.Entries["bot.db"] = Entry{Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := testImportArchive(t, change)
			if _, err := Decrypt(context.Background(), bytes.NewReader(raw), "import test password", t.TempDir()); err == nil {
				t.Fatal("invalid data accepted")
			}
		})
	}
}

func TestImportRejectsTraversalLinksAndOversize(t *testing.T) {
	for _, h := range []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}, {Name: "files/link", Typeflag: tar.TypeSymlink, Linkname: "/root/key"}, {Name: "files/large", Typeflag: tar.TypeReg, Size: MaxImportBytes + 1}, {Name: "files/x:stream", Typeflag: tar.TypeReg}} {
		raw := testEncryptedTar(t, func(tw *tar.Writer) { tw.WriteHeader(h) })
		if _, err := Decrypt(context.Background(), bytes.NewReader(raw), "import test password", t.TempDir()); err == nil {
			t.Errorf("accepted %+v", h)
		}
	}
}

func TestImportSessionAndBoundedRows(t *testing.T) {
	store := &Imports{Dir: filepath.Join(t.TempDir(), "imports")}
	id, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	raw := testImportArchive(t, nil)
	if err := store.Append(id, 0, raw); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(id, 0, raw); err == nil {
		t.Fatal("duplicate upload accepted")
	}
	if _, err := store.Inspect(context.Background(), id, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	preview, err := store.Inspect(context.Background(), id, "import test password")
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID != id {
		t.Fatal(preview)
	}
	if err := store.With(id, func(p *Preview, _ *os.Root) error { p.BotID = 7; return nil }); err != nil {
		t.Fatal(err)
	}
	preview, _ = store.Preview(id)
	if preview.BotID != 7 {
		t.Fatal("commit state lost")
	}
	store.Abort(id)
	if _, err := store.Preview(id); err == nil {
		t.Fatal("aborted session accessible")
	}
	for _, input := range []string{`{"version":2,"rows":[]}`, `{"version":1,"rows":[null]}`, `{"version":1,"rows":[],"rows":[]}`, `{"version":1,"rows":[{"huge":"` + strings.Repeat("a", 2<<20) + `"}]}`} {
		if err := ReadRowsDocument(strings.NewReader(input), map[string]func(map[string]any) error{"rows": func(map[string]any) error { return nil }}); err == nil {
			t.Fatal("invalid rows accepted")
		}
	}
	count := 0
	input := `{"version":1,"rows":[{"n":9223372036854775807},{"n":2}]}`
	if err := ReadRowsDocument(strings.NewReader(input), map[string]func(map[string]any) error{"rows": func(row map[string]any) error {
		if _, ok := row["n"].(json.Number); !ok {
			t.Fatal("precision lost")
		}
		count++
		return nil
	}}); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if _, err := io.ReadAll(&RemoteReader{Ctx: context.Background(), Ref: FileRef{}, Size: 10, ReadChunk: func(context.Context, FileRef) ([]byte, error) { return nil, nil }}); err == nil {
		t.Fatal("truncated remote file accepted")
	}
}
