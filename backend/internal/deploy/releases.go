package deploy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Keep is how many releases are kept per bot.
var Keep = 5

// Release is one stored version.
type Release struct {
	Version  string    `cbor:"1,keyasint" json:"version"`
	SHA256   string    `cbor:"2,keyasint" json:"sha256"`
	Size     int64     `cbor:"3,keyasint" json:"size"`
	Uploaded time.Time `cbor:"4,keyasint" json:"uploaded"`
	Source   string    `cbor:"5,keyasint" json:"source"` // upload | github
	Build    BuildInfo `cbor:"6,keyasint" json:"build"`
	Current  bool      `cbor:"7,keyasint" json:"current"`
	Previous bool      `cbor:"8,keyasint" json:"previous"`
}

// Store keeps releases under /data/core/bots/<id>/releases.
type Store struct {
	// Dir returns the releases directory of a bot.
	Dir func(botID int64) string
	// Chown gives files to the bot's uid; nil when not running as root.
	Chown func(botID int64, path string) error

	mu      sync.Mutex
	uploads map[string]*upload
}

type upload struct {
	botID   int64
	version string
	source  string
	f       *os.File
	h       interface {
		io.Writer
		Sum([]byte) []byte
	}
	size    int64
	started time.Time
}

var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// ErrUnknownUpload is returned for chunks of an upload that does not exist.
var ErrUnknownUpload = errors.New("deploy: unknown upload")

// Begin starts an upload; the version may be empty (taken from the binary).
func (s *Store) Begin(botID int64, version, source string) (string, error) {
	if version != "" && !versionRe.MatchString(version) {
		return "", refuse("version %q: use letters, digits, '.', '-', '+' and '_'", version)
	}
	dir := s.Dir(botID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", err
	}
	idb := make([]byte, 12)
	rand.Read(idb)
	id := hex.EncodeToString(idb)
	s.mu.Lock()
	if s.uploads == nil {
		s.uploads = map[string]*upload{}
	}
	// Drop abandoned uploads.
	for k, u := range s.uploads {
		if time.Since(u.started) > time.Hour {
			u.f.Close()
			os.Remove(u.f.Name())
			delete(s.uploads, k)
		}
	}
	s.uploads[id] = &upload{botID: botID, version: version, source: source, f: f, h: sha256.New(), started: time.Now()}
	s.mu.Unlock()
	return id, nil
}

// Write appends a chunk.
func (s *Store) Write(id string, data []byte) error {
	s.mu.Lock()
	u := s.uploads[id]
	s.mu.Unlock()
	if u == nil {
		return ErrUnknownUpload
	}
	u.size += int64(len(data))
	if u.size > MaxBinary {
		s.Abort(id)
		return refuse("file is over %d MB", MaxBinary>>20)
	}
	if _, err := u.f.Write(data); err != nil {
		return err
	}
	u.h.Write(data)
	return nil
}

// Abort drops an upload.
func (s *Store) Abort(id string) {
	s.mu.Lock()
	u := s.uploads[id]
	delete(s.uploads, id)
	s.mu.Unlock()
	if u != nil {
		u.f.Close()
		os.Remove(u.f.Name())
	}
}

// Finish checks the binary and stores it as releases/<version>/bot.
func (s *Store) Finish(id string) (Release, error) {
	s.mu.Lock()
	u := s.uploads[id]
	delete(s.uploads, id)
	s.mu.Unlock()
	if u == nil {
		return Release{}, ErrUnknownUpload
	}
	defer os.Remove(u.f.Name())
	defer u.f.Close()
	bi, err := Check(u.f, u.size)
	u.f.Close()
	if err != nil {
		return Release{}, err
	}
	rel := Release{SHA256: hex.EncodeToString(u.h.Sum(nil)), Size: u.size, Uploaded: time.Now().UTC(), Source: u.source, Build: bi}
	rel.Version = u.version
	if rel.Version == "" {
		rel.Version = s.autoVersion(u.botID, bi, rel.SHA256)
	}
	dir := filepath.Join(s.Dir(u.botID), rel.Version)
	if _, err := os.Stat(dir); err == nil {
		old, _ := s.read(u.botID, rel.Version)
		if old.SHA256 == rel.SHA256 {
			return old, nil // the same file again
		}
		return Release{}, refuse("version %s already exists with another file", rel.Version)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Release{}, err
	}
	bin := filepath.Join(dir, "bot")
	if err := os.Rename(u.f.Name(), bin); err != nil {
		return Release{}, err
	}
	os.Chmod(bin, 0o500)
	meta, _ := json.MarshalIndent(rel, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "release.json"), meta, 0o640); err != nil {
		return Release{}, err
	}
	if s.Chown != nil {
		for _, p := range []string{dir, bin} {
			if err := s.Chown(u.botID, p); err != nil {
				return Release{}, err
			}
		}
	}
	return rel, nil
}

// autoVersion names a release by its build: module version, VCS revision,
// or upload time.
func (s *Store) autoVersion(botID int64, bi BuildInfo, sum string) string {
	v := bi.Version
	if v == "" && bi.Revision != "" {
		v = bi.Revision[:min(12, len(bi.Revision))]
		if bi.Modified {
			v += "-dirty"
		}
	}
	if v == "" {
		v = time.Now().UTC().Format("20060102-150405")
	}
	v = strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' {
			return '-'
		}
		return r
	}, v)
	if _, err := os.Stat(filepath.Join(s.Dir(botID), v)); err == nil {
		if old, _ := s.read(botID, v); old.SHA256 != sum {
			v += "-" + sum[:8]
		}
	}
	return v
}

// Get returns a stored release.
func (s *Store) Get(botID int64, version string) (Release, error) { return s.read(botID, version) }

func (s *Store) read(botID int64, version string) (Release, error) {
	var r Release
	b, err := os.ReadFile(filepath.Join(s.Dir(botID), version, "release.json"))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// Exists reports whether a version is stored.
func (s *Store) Exists(botID int64, version string) bool {
	_, err := os.Stat(filepath.Join(s.Dir(botID), version, "bot"))
	return err == nil
}

// List returns releases, newest first, marking current and previous.
func (s *Store) List(botID int64, current, previous string) ([]Release, error) {
	ents, err := os.ReadDir(s.Dir(botID))
	if errors.Is(err, os.ErrNotExist) {
		return []Release{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Release{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r, err := s.read(botID, e.Name())
		if err != nil {
			continue
		}
		r.Current, r.Previous = r.Version == current, r.Version == previous
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Uploaded.After(out[j].Uploaded) })
	return out, nil
}

// Prune keeps the newest Keep releases; current and previous always stay.
func (s *Store) Prune(botID int64, current, previous string) error {
	rs, err := s.List(botID, current, previous)
	if err != nil {
		return err
	}
	// Current and previous always stay; the newest others fill the rest.
	room := Keep
	for _, r := range rs {
		if r.Current || r.Previous {
			room--
		}
	}
	for _, r := range rs {
		if r.Current || r.Previous {
			continue
		}
		if room > 0 {
			room--
			continue
		}
		bin := filepath.Join(s.Dir(botID), r.Version, "bot")
		os.Chmod(bin, 0o600) // read-only files cannot be removed on some systems
		if err := os.RemoveAll(filepath.Join(s.Dir(botID), r.Version)); err != nil {
			return fmt.Errorf("deploy: removing %s: %w", r.Version, err)
		}
	}
	return nil
}
