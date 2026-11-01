package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
)

const MaxImportBytes int64 = 64 << 30
const maxImportEntries = 100_000

type Preview struct {
	BotID    int64              `cbor:"6,keyasint,omitempty" json:"bot_id,omitempty"`
	ID       string             `cbor:"1,keyasint" json:"id"`
	Manifest Manifest           `cbor:"2,keyasint" json:"manifest"`
	Document botconfig.Document `cbor:"3,keyasint" json:"document"`
	Expires  time.Time          `cbor:"4,keyasint" json:"expires"`
	Metrics  bool               `cbor:"5,keyasint" json:"metrics"`
}
type importSession struct {
	expires time.Time
	preview *Preview
	size    int64
}
type Imports struct {
	Dir      string
	TTL      time.Duration
	mu       sync.Mutex
	sessions map[string]*importSession
}

func (i *Imports) directory(id string) string { return filepath.Join(i.Dir, id) }
func (i *Imports) Begin() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := os.MkdirAll(i.Dir, 0o700); err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	if err := os.Mkdir(i.directory(id), 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(i.directory(id), "archive.age"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		os.Remove(i.directory(id))
		return "", err
	}
	file.Close()
	if i.sessions == nil {
		i.sessions = map[string]*importSession{}
	}
	i.sessions[id] = &importSession{expires: time.Now().Add(i.ttl())}
	return id, nil
}
func (i *Imports) ttl() time.Duration {
	if i.TTL > 0 {
		return i.TTL
	}
	return time.Hour
}
func (i *Imports) session(id string) (*importSession, error) {
	s := i.sessions[id]
	if !validID(id) || s == nil || time.Now().After(s.expires) {
		return nil, ErrExpired
	}
	return s, nil
}
func (i *Imports) Append(id string, offset int64, data []byte) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, err := i.session(id)
	if err != nil {
		return err
	}
	if s.preview != nil || offset != s.size || len(data) > ChunkSize || int64(len(data)) > MaxImportBytes-s.size {
		return fmt.Errorf("invalid upload chunk")
	}
	f, err := os.OpenFile(filepath.Join(i.directory(id), "archive.age"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	s.size += int64(len(data))
	s.expires = time.Now().Add(i.ttl())
	return nil
}
func (i *Imports) Inspect(ctx context.Context, id, password string) (Preview, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, err := i.session(id)
	if err != nil {
		return Preview{}, err
	}
	if s.preview != nil {
		return *s.preview, nil
	}
	dir := filepath.Join(i.directory(id), "data")
	os.RemoveAll(dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Preview{}, err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(dir)
		}
	}()
	f, err := os.Open(filepath.Join(i.directory(id), "archive.age"))
	if err != nil {
		return Preview{}, err
	}
	defer f.Close()
	preview, err := Decrypt(ctx, f, password, dir)
	if err != nil {
		return Preview{}, err
	}
	preview.ID = id
	s.expires = time.Now().Add(i.ttl())
	preview.Expires = s.expires
	s.preview = &preview
	success = true
	return preview, nil
}

// With keeps a validated session alive during commit and prevents two commits
// from consuming the same upload concurrently.
func (i *Imports) With(id string, fn func(*Preview, *os.Root) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, err := i.session(id)
	if err != nil {
		return err
	}
	if s.preview == nil {
		return fmt.Errorf("сначала проверьте архив")
	}
	root, err := os.OpenRoot(filepath.Join(i.directory(id), "data"))
	if err != nil {
		return err
	}
	defer root.Close()
	s.expires = time.Now().Add(i.ttl())
	s.preview.Expires = s.expires
	err = fn(s.preview, root)
	s.expires = time.Now().Add(i.ttl())
	s.preview.Expires = s.expires
	return err
}

func (i *Imports) Preview(id string) (Preview, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, err := i.session(id)
	if err != nil {
		return Preview{}, err
	}
	if s.preview == nil {
		return Preview{}, fmt.Errorf("сначала проверьте архив")
	}
	return *s.preview, nil
}
func (i *Imports) Read(ref FileRef) ([]byte, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	s, err := i.session(ref.ID)
	if err != nil {
		return nil, err
	}
	if s.preview == nil || !s.preview.Metrics || ref.BotID != 0 || ref.Offset < 0 {
		return nil, ErrExpired
	}
	entry := s.preview.Manifest.Entries["metrics/history.json"]
	if ref.Offset > entry.Size {
		return nil, fmt.Errorf("invalid offset")
	}
	f, err := os.Open(filepath.Join(i.directory(ref.ID), "data", "metrics", "history.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([]byte, min(int64(ChunkSize), entry.Size-ref.Offset))
	_, err = io.ReadFull(io.NewSectionReader(f, ref.Offset, int64(len(raw))), raw)
	return raw, err
}
func (i *Imports) Abort(id string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !validID(id) {
		return
	}
	delete(i.sessions, id)
	os.RemoveAll(i.directory(id))
}
func (i *Imports) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		i.Prune()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (i *Imports) Prune() {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := time.Now()
	entries, _ := os.ReadDir(i.Dir)
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		s := i.sessions[entry.Name()]
		expired := s != nil && now.After(s.expires)
		if s == nil {
			info, err := entry.Info()
			expired = err == nil && now.Sub(info.ModTime()) > i.ttl()
		}
		if expired {
			delete(i.sessions, entry.Name())
			os.RemoveAll(i.directory(entry.Name()))
		}
	}
}

func allowedEntry(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") || len(name) > 4096 {
		return false
	}
	switch name {
	case "manifest.json", "config.yaml", "users.json", "payments.json", "bot.db", "metrics/history.json":
		return true
	}
	parts := strings.Split(name, "/")
	if len(parts) == 3 && parts[0] == "bin" && deploy.ValidVersion(parts[1]) && parts[2] == "bot" {
		return true
	}
	return len(parts) > 1 && parts[0] == "files"
}

// Decrypt extracts only regular, format-owned files beneath a newly created
// root. Every byte is authenticated before the preview becomes available.
func Decrypt(ctx context.Context, input io.Reader, password, directory string) (Preview, error) {
	var out Preview
	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return out, err
	}
	identity.SetMaxWorkFactor(18)
	plain, err := age.Decrypt(contextReader{ctx, input}, identity)
	if err != nil {
		return out, fmt.Errorf("не удалось открыть архив: неверный пароль или повреждённый файл: %w", err)
	}
	buffered := bufio.NewReader(plain)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return out, err
	}
	defer gz.Close()
	gz.Multistream(false)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return out, err
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	actual := map[string]Entry{}
	count := 0
	total := int64(0)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		count++
		if count > maxImportEntries || h.Typeflag != tar.TypeReg || !allowedEntry(h.Name) || h.Size < 0 || h.Size > MaxImportBytes-total {
			return out, fmt.Errorf("недопустимый файл архива %q", h.Name)
		}
		if _, ok := actual[h.Name]; ok {
			return out, fmt.Errorf("повторный файл %q", h.Name)
		}
		total += h.Size
		if (h.Name == "config.yaml" && h.Size > 1<<20) || (h.Name == "manifest.json" && h.Size > 32<<20) {
			return out, fmt.Errorf("metadata file is too large")
		}
		if err := root.MkdirAll(path.Dir(h.Name), 0o700); err != nil {
			return out, err
		}
		file, err := root.OpenFile(h.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return out, err
		}
		hash := sha256.New()
		n, copyErr := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx, tr}, h.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return out, copyErr
		}
		if closeErr != nil {
			return out, closeErr
		}
		actual[h.Name] = Entry{Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	if n, err := io.Copy(io.Discard, contextReader{ctx, gz}); err != nil || n != 0 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("лишние данные после tar")
	}
	if n, err := io.Copy(io.Discard, contextReader{ctx, buffered}); err != nil || n != 0 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("лишние данные после gzip")
	}
	raw, err := root.ReadFile("manifest.json")
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out.Manifest); err != nil {
		return out, err
	}
	if out.Manifest.Format != "utbpanel.bot" || out.Manifest.Version != FormatVersion {
		return out, fmt.Errorf("неподдерживаемая версия формата")
	}
	delete(actual, "manifest.json")
	if len(actual) != len(out.Manifest.Entries) {
		return out, fmt.Errorf("список файлов не совпадает с манифестом")
	}
	for name, entry := range actual {
		if out.Manifest.Entries[name] != entry {
			return out, fmt.Errorf("контрольная сумма %s не совпадает", name)
		}
	}
	for _, required := range []string{"config.yaml", "users.json", "payments.json"} {
		if _, ok := actual[required]; !ok {
			return out, fmt.Errorf("отсутствует %s", required)
		}
	}
	raw, err = root.ReadFile("config.yaml")
	if err != nil {
		return out, err
	}
	out.Document, err = botconfig.Decode(raw)
	if err != nil {
		return out, err
	}
	out.Document.Version = ""
	versions := map[string]bool{}
	for _, release := range out.Manifest.Releases {
		if !deploy.ValidVersion(release.Version) || versions[release.Version] {
			return out, fmt.Errorf("invalid release list")
		}
		versions[release.Version] = true
		name := path.Join("bin", release.Version, "bot")
		entry, ok := actual[name]
		if !ok {
			return out, fmt.Errorf("нет бинарника %s", release.Version)
		}
		f, err := root.Open(name)
		if err != nil {
			return out, err
		}
		_, err = deploy.Check(f, entry.Size)
		f.Close()
		if err != nil {
			return out, err
		}
	}
	for name := range actual {
		if strings.HasPrefix(name, "bin/") && !versions[strings.Split(name, "/")[1]] {
			return out, fmt.Errorf("binary is missing from release list")
		}
	}
	if out.Manifest.CurrentVersion != "" && !versions[out.Manifest.CurrentVersion] {
		return out, fmt.Errorf("нет текущей версии")
	}
	if _, ok := actual["bot.db"]; ok {
		if err := dbgate.Integrity(ctx, filepath.Join(directory, "bot.db"), false); err != nil {
			return out, err
		}
	}
	_, out.Metrics = actual["metrics/history.json"]
	return out, nil
}
