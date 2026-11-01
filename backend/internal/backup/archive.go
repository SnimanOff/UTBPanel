// Package backup builds portable bot archives and encrypted system backups.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/age"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/sethvargo/go-diceware/diceware"
)

const FormatVersion = 1
const ChunkSize = 256 << 10

type Entry struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Format         string           `json:"format"`
	Version        int              `json:"version"`
	Created        time.Time        `json:"created"`
	Name           string           `json:"name"`
	Username       string           `json:"username"`
	TelegramID     int64            `json:"telegram_id"`
	Group          *botconfig.Group `json:"group,omitempty"`
	CurrentVersion string           `json:"current_version"`
	Releases       []deploy.Release `json:"releases"`
	Entries        map[string]Entry `json:"entries"`
}

func Password() (string, error) {
	words, err := diceware.Generate(6)
	return strings.Join(words, " "), err
}
func ValidatePassword(password string) error {
	if n := utf8.RuneCountInString(password); n < 10 || n > 1024 {
		return fmt.Errorf("пароль: от 10 до 1024 символов")
	}
	return nil
}

type Archive struct {
	ctx      context.Context
	tar      *tar.Writer
	manifest *Manifest
}
type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// Encrypt writes tar.gz directly into age. A manifest comes last so its hashes
// describe exactly the bytes streamed, including files of a running bot.
func Encrypt(ctx context.Context, out io.Writer, password string, manifest Manifest, fill func(*Archive) error) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return err
	}
	encrypted, err := age.Encrypt(out, recipient)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(encrypted)
	tw := tar.NewWriter(gzipWriter)
	manifest.Format = "utbpanel.bot"
	manifest.Version = FormatVersion
	manifest.Created = time.Now().UTC()
	manifest.Entries = map[string]Entry{}
	if manifest.Releases == nil {
		manifest.Releases = []deploy.Release{}
	}
	a := &Archive{ctx: ctx, tar: tw, manifest: &manifest}
	if err := fill(a); err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := a.add("manifest.json", int64(len(raw)), 0o600, strings.NewReader(string(raw)), false); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	return encrypted.Close()
}

func (a *Archive) add(name string, size int64, mode int64, r io.Reader, record bool) error {
	if !fs.ValidPath(name) || name == "." || strings.Contains(name, "\\") || size < 0 {
		return fmt.Errorf("invalid archive entry %q", name)
	}
	if _, exists := a.manifest.Entries[name]; exists || (record && name == "manifest.json") {
		return fmt.Errorf("duplicate archive entry %q", name)
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if err := a.tar.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: size}); err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(a.tar, hash), contextReader{a.ctx, r}, size)
	if err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	if record {
		a.manifest.Entries[name] = Entry{Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	return nil
}
func (a *Archive) Add(name string, size int64, r io.Reader) error {
	return a.add(name, size, 0o600, r, true)
}
func (a *Archive) Bytes(name string, raw []byte) error {
	return a.Add(name, int64(len(raw)), strings.NewReader(string(raw)))
}

// RootFile confines reads even if the bot swaps a directory for a symlink.
// Links and special files are never included as archive entries.
func (a *Archive) RootFile(root *os.Root, relative, name string, executable bool) error {
	info, err := root.Lstat(relative)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive %s: expected regular file", name)
	}
	f, err := openFile(root, relative)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive %s: expected regular file", name)
	}
	mode := int64(0o600)
	if executable {
		mode = 0o500
	}
	return a.add(name, info.Size(), mode, f, true)
}
func (a *Archive) Tree(root *os.Root, directory, prefix string) error {
	return fs.WalkDir(root.FS(), directory, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := a.ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative := strings.TrimPrefix(name, directory+"/")
		return a.RootFile(root, name, path.Join(prefix, relative), false)
	})
}
