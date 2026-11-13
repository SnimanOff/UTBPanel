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
	Config   bool      `cbor:"9,keyasint" json:"config"` // the release carries tgpanel.yaml
}

// ConfigName is the settings file a release may carry next to the binary.
const ConfigName = "tgpanel.yaml"

// Store keeps releases under /data/core/bots/<id>/releases.
type Store struct {
	// Dir returns the releases directory of a bot.
	Dir func(botID int64) string
	// Chown gives a stored file or directory to root with the bot's group
	// (the bot may run its binaries but not change them); nil when not
	// running as root.
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

// ValidVersion reports whether a version can be used as a release directory.
func ValidVersion(version string) bool { return versionRe.MatchString(version) }

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
	if err := os.Chmod(bin, 0o550); err != nil { //nolint:gosec // executable by the bot's group, see Chown
		return Release{}, err
	}
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
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	_, err = os.Stat(filepath.Join(s.Dir(botID), version, ConfigName))
	r.Config = err == nil
	return r, nil
}

// Config returns tgpanel.yaml of a release, nil when it has none.
func (s *Store) Config(botID int64, version string) ([]byte, error) {
	if !versionRe.MatchString(version) {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(botID), version, ConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// SetConfig stores tgpanel.yaml next to a release.
func (s *Store) SetConfig(botID int64, version string, raw []byte) error {
	return os.WriteFile(filepath.Join(s.Dir(botID), version, ConfigName), raw, 0o640)
}

// CopyTo stores release version of bot from as a release of bot to (a test
// run of one bot's build under another bot). The copy keeps the version name;
// when the target already has that name with another file, the name gets the
// first 8 hex digits of the SHA-256. The same file again returns the stored
// release.
func (s *Store) CopyTo(from int64, version string, to int64) (Release, error) {
	if !versionRe.MatchString(version) {
		return Release{}, os.ErrNotExist
	}
	rel, err := s.read(from, version)
	if err != nil {
		return Release{}, err
	}
	name := version
	if old, err := s.read(to, name); err == nil {
		if old.SHA256 == rel.SHA256 {
			return old, nil
		}
		name = version + "-" + rel.SHA256[:8]
		if old, err := s.read(to, name); err == nil && old.SHA256 == rel.SHA256 {
			return old, nil
		}
	}
	if err := os.MkdirAll(s.Dir(to), 0o750); err != nil {
		return Release{}, err
	}
	src, err := os.Open(filepath.Join(s.Dir(from), version, "bot"))
	if err != nil {
		return Release{}, err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(s.Dir(to), ".copy-*")
	if err != nil {
		return Release{}, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), src); err != nil {
		tmp.Close()
		return Release{}, err
	}
	if err := tmp.Close(); err != nil {
		return Release{}, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.SHA256 {
		return Release{}, fmt.Errorf("deploy: %s of bot %d changed on disk (SHA-256 %s, want %s)", version, from, got[:12], rel.SHA256[:12])
	}
	dir := filepath.Join(s.Dir(to), name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Release{}, err
	}
	bin := filepath.Join(dir, "bot")
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return Release{}, err
	}
	if err := os.Chmod(bin, 0o550); err != nil { //nolint:gosec // executable by the bot's group, see Chown
		return Release{}, err
	}
	rel.Version, rel.Uploaded, rel.Current, rel.Previous, rel.Config = name, time.Now().UTC(), false, false, false
	meta, _ := json.MarshalIndent(rel, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "release.json"), meta, 0o640); err != nil {
		return Release{}, err
	}
	if s.Chown != nil {
		for _, p := range []string{dir, bin} {
			if err := s.Chown(to, p); err != nil {
				return Release{}, err
			}
		}
	}
	return rel, nil
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
	return s.PruneKeep(botID, Keep, current, previous)
}

// PruneKeep keeps the newest keep releases (Keep when keep < 1); current
// and previous always stay.
func (s *Store) PruneKeep(botID int64, keep int, current, previous string) error {
	if keep < 1 {
		keep = Keep
	}
	rs, err := s.List(botID, current, previous)
	if err != nil {
		return err
	}
	// Current and previous always stay; the newest others fill the rest.
	room := keep
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
