package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

var ErrExpired = errors.New("архив не найден или срок хранения истёк")

type File struct {
	ID      string    `cbor:"1,keyasint" json:"id"`
	BotID   int64     `cbor:"2,keyasint" json:"bot_id"`
	Name    string    `cbor:"3,keyasint" json:"name"`
	Size    int64     `cbor:"4,keyasint" json:"size"`
	Expires time.Time `cbor:"5,keyasint" json:"expires"`
}
type FileRef struct {
	ID     string `cbor:"1,keyasint"`
	BotID  int64  `cbor:"2,keyasint"`
	Offset int64  `cbor:"3,keyasint"`
}
type Files struct {
	Dir string
	TTL time.Duration
	Now func() time.Time
	mu  sync.Mutex
}

func (f *Files) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		f.Prune()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (f *Files) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}
func Filename(name string, id int64) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	base := strings.Trim(b.String(), "._")
	if base == "" {
		base = fmt.Sprintf("bot-%d", id)
	}
	return base + ".tgbot"
}

func (f *Files) Create(ctx context.Context, botID int64, name string, fill func(io.Writer) error) (File, error) {
	f.Prune()
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return File{}, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return File{}, err
	}
	id := hex.EncodeToString(random)
	file, err := os.CreateTemp(f.Dir, ".export-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := fill(file); err != nil {
		return File{}, err
	}
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	if err := file.Sync(); err != nil {
		return File{}, err
	}
	stat, err := file.Stat()
	if err != nil {
		return File{}, err
	}
	if err := file.Close(); err != nil {
		return File{}, err
	}
	ttl := f.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	out := File{ID: id, BotID: botID, Name: name, Size: stat.Size(), Expires: f.now().Add(ttl).UTC()}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := filepath.Join(f.Dir, id)
	if err := os.Rename(file.Name(), path); err != nil {
		return File{}, err
	}
	raw, _ := json.Marshal(out)
	if err := os.WriteFile(path+".json", raw, 0o600); err != nil {
		os.Remove(path)
		return File{}, err
	}
	return out, nil
}
func (f *Files) Info(ref FileRef) (File, error) {
	if !validID(ref.ID) {
		return File{}, ErrExpired
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info(ref)
}
func (f *Files) info(ref FileRef) (File, error) {
	raw, err := os.ReadFile(filepath.Join(f.Dir, ref.ID+".json"))
	if err != nil {
		return File{}, ErrExpired
	}
	var out File
	if json.Unmarshal(raw, &out) != nil || out.ID != ref.ID || out.BotID != ref.BotID || !f.now().Before(out.Expires) {
		return File{}, ErrExpired
	}
	return out, nil
}
func (f *Files) Read(ref FileRef) ([]byte, error) {
	if !validID(ref.ID) {
		return nil, ErrExpired
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	info, err := f.info(ref)
	if err != nil {
		return nil, err
	}
	if ref.Offset < 0 || ref.Offset > info.Size {
		return nil, fmt.Errorf("invalid file offset")
	}
	file, err := os.Open(filepath.Join(f.Dir, ref.ID))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	chunk := make([]byte, min(int64(ChunkSize), info.Size-ref.Offset))
	_, err = io.ReadFull(io.NewSectionReader(file, ref.Offset, int64(len(chunk))), chunk)
	return chunk, err
}
func (f *Files) Delete(ref FileRef) error {
	if !validID(ref.ID) {
		return ErrExpired
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.info(ref); err != nil {
		return err
	}
	os.Remove(filepath.Join(f.Dir, ref.ID+".json"))
	return os.Remove(filepath.Join(f.Dir, ref.ID))
}
func (f *Files) Prune() {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, _ := os.ReadDir(f.Dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		if validID(base) {
			raw, err := os.ReadFile(filepath.Join(f.Dir, base+".json"))
			var meta File
			expired := err != nil || json.Unmarshal(raw, &meta) != nil || !f.now().Before(meta.Expires)
			if expired {
				os.Remove(filepath.Join(f.Dir, base))
				os.Remove(filepath.Join(f.Dir, base+".json"))
			}
		} else if strings.HasPrefix(e.Name(), ".export-") && f.now().Sub(info.ModTime()) > 24*time.Hour {
			os.Remove(filepath.Join(f.Dir, e.Name()))
		}
	}
}

type RemoteReader struct {
	Ctx       context.Context
	Ref       FileRef
	Size      int64
	ReadChunk func(context.Context, FileRef) ([]byte, error)
	pending   []byte
}

func (r *RemoteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.Ctx.Err(); err != nil {
		return 0, err
	}
	if len(r.pending) == 0 {
		if r.Ref.Offset >= r.Size {
			return 0, io.EOF
		}
		chunk, err := r.ReadChunk(r.Ctx, r.Ref)
		if err != nil {
			return 0, err
		}
		if len(chunk) == 0 || len(chunk) > ChunkSize || int64(len(chunk)) > r.Size-r.Ref.Offset {
			return 0, io.ErrUnexpectedEOF
		}
		r.pending = chunk
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	r.Ref.Offset += int64(n)
	return n, nil
}
