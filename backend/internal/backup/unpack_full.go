package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
)

func fullPath(name string) bool {
	if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00") || len(name) > 4096 {
		return false
	}
	switch name {
	case "core.db", "panel.db", "manifest.json":
		return true
	}
	parts := strings.Split(name, "/")
	if len(parts) > 1 && parts[0] == "pki" {
		return true
	}
	if len(parts) < 3 || parts[0] != "bots" {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 || id > math.MaxInt32 || strconv.FormatInt(id, 10) != parts[1] {
		return false
	}
	if len(parts) == 3 && parts[2] == "bot.db" {
		return true
	}
	if len(parts) > 3 && parts[2] == "files" {
		return true
	}
	return len(parts) == 5 && parts[2] == "releases" && deploy.ValidVersion(parts[3]) && (parts[4] == "bot" || parts[4] == "release.json")
}

// ExtractFull authenticates the final encryption frame before exposing a
// manifest. All writes stay in a newly created private staging directory.
func ExtractFull(ctx context.Context, input io.Reader, key []byte, directory string) (FullManifest, error) {
	var manifest FullManifest
	encrypted, err := NewChunkReader(contextReader{ctx, input}, key)
	if err != nil {
		return manifest, err
	}
	buffered := bufio.NewReader(encrypted)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return manifest, err
	}
	defer gz.Close()
	gz.Multistream(false)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	reader := tar.NewReader(gz)
	actual := map[string]Entry{}
	total := int64(0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return manifest, err
		}
		if len(actual) >= 1_000_000 || header.Typeflag != tar.TypeReg || !fullPath(header.Name) || header.Size < 0 || header.Size > MaxRestoreBytes-total {
			return manifest, fmt.Errorf("invalid full backup entry %q", header.Name)
		}
		if _, exists := actual[header.Name]; exists {
			return manifest, fmt.Errorf("duplicate backup entry")
		}
		if header.Name == "manifest.json" && header.Size > 128<<20 {
			return manifest, fmt.Errorf("manifest too large")
		}
		total += header.Size
		hash := sha256.New()
		if err := root.MkdirAll(path.Dir(header.Name), 0o700); err != nil {
			return manifest, err
		}
		file, err := root.OpenFile(header.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return manifest, err
		}
		n, copyErr := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx, reader}, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return manifest, copyErr
		}
		if closeErr != nil {
			return manifest, closeErr
		}
		actual[header.Name] = Entry{Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	if n, err := io.Copy(io.Discard, contextReader{ctx, gz}); err != nil || n != 0 {
		if err != nil {
			return manifest, err
		}
		return manifest, fmt.Errorf("data after tar archive")
	}
	if n, err := io.Copy(io.Discard, contextReader{ctx, buffered}); err != nil || n != 0 {
		if err != nil {
			return manifest, err
		}
		return manifest, fmt.Errorf("data after gzip stream")
	}
	raw, err := root.ReadFile("manifest.json")
	if err != nil {
		return manifest, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return manifest, err
	}
	if manifest.Format != "utbpanel.full" || manifest.Version != FormatVersion {
		return manifest, fmt.Errorf("unsupported full backup version")
	}
	delete(actual, "manifest.json")
	if len(actual) != len(manifest.Entries) {
		return manifest, fmt.Errorf("backup contents differ from manifest")
	}
	for name, entry := range actual {
		if manifest.Entries[name] != entry {
			return manifest, fmt.Errorf("checksum mismatch: %s", name)
		}
	}
	for _, name := range []string{"core.db", "panel.db", "pki/ca.crt", "pki/ca.key"} {
		if _, exists := actual[name]; !exists {
			return manifest, fmt.Errorf("missing %s", name)
		}
	}
	for name, entry := range actual {
		if name == "core.db" || name == "panel.db" || strings.HasSuffix(name, "/bot.db") {
			if err := dbgate.Integrity(ctx, filepath.Join(directory, filepath.FromSlash(name)), false); err != nil {
				return manifest, fmt.Errorf("%s: %w", name, err)
			}
		}
		if strings.HasPrefix(name, "bots/") && strings.Contains(name, "/releases/") && path.Base(name) == "bot" {
			file, err := root.Open(name)
			if err != nil {
				return manifest, err
			}
			_, err = deploy.Check(file, entry.Size)
			file.Close()
			if err != nil {
				return manifest, err
			}
		}
	}
	cert, err := root.ReadFile("pki/ca.crt")
	if err != nil {
		return manifest, err
	}
	private, err := root.ReadFile("pki/ca.key")
	if err != nil {
		return manifest, err
	}
	if _, err := tls.X509KeyPair(cert, private); err != nil {
		return manifest, fmt.Errorf("CA key and certificate: %w", err)
	}
	seen := map[int64]FullBot{}
	for _, bot := range manifest.Bots {
		if bot.ID <= 0 || bot.ID > math.MaxInt32 {
			return manifest, fmt.Errorf("invalid restored bot id")
		}
		if _, ok := seen[bot.ID]; ok {
			return manifest, fmt.Errorf("duplicate bot id")
		}
		seen[bot.ID] = bot
		if bot.Version != "" {
			if !deploy.ValidVersion(bot.Version) {
				return manifest, fmt.Errorf("invalid bot version")
			}
			for _, name := range []string{"bot", "release.json"} {
				if _, ok := actual[path.Join("bots", strconv.FormatInt(bot.ID, 10), "releases", bot.Version, name)]; !ok {
					return manifest, fmt.Errorf("missing current release")
				}
			}
		}
	}
	db, err := dbgate.OpenReadOnly(filepath.Join(directory, "core.db"))
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id,name,current_version,state FROM bots`)
	if err != nil {
		return manifest, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var b FullBot
		if err := rows.Scan(&b.ID, &b.Name, &b.Version, &b.State); err != nil {
			return manifest, err
		}
		if expected, ok := seen[b.ID]; !ok || expected != b {
			return manifest, fmt.Errorf("bot list differs from core.db")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return manifest, err
	}
	if count != len(seen) {
		return manifest, fmt.Errorf("bot list differs from core.db")
	}
	for name := range actual {
		if strings.HasPrefix(name, "bots/") {
			id, _ := strconv.ParseInt(strings.Split(name, "/")[1], 10, 64)
			if _, ok := seen[id]; !ok {
				return manifest, fmt.Errorf("unknown bot directory")
			}
		}
	}
	return manifest, nil
}

// ExtractPanel installs only the already validated panel contribution over the
// private socket. CA files are checked again on the receiving side.
func ExtractPanel(ctx context.Context, input io.Reader, directory string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	reader := tar.NewReader(contextReader{ctx, input})
	seen := map[string]bool{}
	total := int64(0)
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(seen) >= 100_000 || h.Typeflag != tar.TypeReg || (h.Name != "panel.db" && !strings.HasPrefix(h.Name, "pki/")) || !fullPath(h.Name) || seen[h.Name] || h.Size < 0 || h.Size > MaxRestoreBytes-total {
			return fmt.Errorf("invalid panel restore entry")
		}
		seen[h.Name] = true
		total += h.Size
		if err := Copy(io.LimitReader(reader, h.Size), root, h.Name, 0o600); err != nil {
			return err
		}
	}
	for _, name := range []string{"panel.db", "pki/ca.crt", "pki/ca.key"} {
		if !seen[name] {
			return fmt.Errorf("missing %s", name)
		}
	}
	if err := dbgate.Integrity(ctx, filepath.Join(directory, "panel.db"), false); err != nil {
		return err
	}
	cert, err := root.ReadFile("pki/ca.crt")
	if err != nil {
		return err
	}
	key, err := root.ReadFile("pki/ca.key")
	if err != nil {
		return err
	}
	_, err = tls.X509KeyPair(cert, key)
	return err
}
