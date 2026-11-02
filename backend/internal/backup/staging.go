package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Staging keeps privileged restore uploads on the destination volume. The
// opaque ID is the only path component accepted from a peer.
type Staging struct {
	Dir   string
	mu    sync.Mutex
	items map[string]stageItem
}
type stageItem struct {
	size    int64
	expires time.Time
}

const MaxRestoreBytes int64 = 1 << 40

func (s *Staging) Begin() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random)
	dir := filepath.Join(s.Dir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(dir, "upload.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		os.Remove(dir)
		return "", err
	}
	file.Close()
	if s.items == nil {
		s.items = map[string]stageItem{}
	}
	s.items[id] = stageItem{expires: time.Now().Add(time.Hour)}
	return id, nil
}
func (s *Staging) Append(id string, offset int64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !validID(id) || !ok || time.Now().After(item.expires) {
		return ErrExpired
	}
	if offset != item.size || len(data) > ChunkSize || int64(len(data)) > MaxRestoreBytes-item.size {
		return fmt.Errorf("invalid restore upload chunk")
	}
	file, err := os.OpenFile(filepath.Join(s.Dir, id, "upload.bin"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	item.size += int64(len(data))
	item.expires = time.Now().Add(time.Hour)
	s.items[id] = item
	return nil
}
func (s *Staging) With(id string, fn func(string, *os.Root) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !validID(id) || !ok || time.Now().After(item.expires) {
		return ErrExpired
	}
	dir := filepath.Join(s.Dir, id)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	item.expires = time.Now().Add(time.Hour)
	s.items[id] = item
	err = fn(dir, root)
	item.expires = time.Now().Add(time.Hour)
	s.items[id] = item
	return err
}
func (s *Staging) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(id) {
		return
	}
	delete(s.items, id)
	os.RemoveAll(filepath.Join(s.Dir, id))
}

func (s *Staging) Run(ctx context.Context) {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.Prune()
		}
	}
}
func (s *Staging) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := os.ReadDir(s.Dir)
	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		item, ok := s.items[entry.Name()]
		expired := ok && now.After(item.expires)
		if !ok {
			info, err := entry.Info()
			expired = err == nil && now.Sub(info.ModTime()) > 24*time.Hour
		}
		if expired {
			delete(s.items, entry.Name())
			os.RemoveAll(filepath.Join(s.Dir, entry.Name()))
		}
	}
}

func Copy(ctxReader io.Reader, root *os.Root, name string, mode os.FileMode) error {
	parent := filepath.Dir(name)
	if err := root.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, ctxReader); err != nil {
		return err
	}
	return file.Close()
}
