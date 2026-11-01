// Package github reads releases of a repository: the list with ETag (a 304
// does not use the rate limit), asset downloads, checksums.txt, tar.gz
// archives, webhook signatures and GitHub's hook addresses.
package github

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"strings"
	"time"
)

// DefaultBase is GitHub's API.
const DefaultBase = "https://api.github.com"

// Client calls the GitHub API.
type Client struct {
	Base string
	HTTP *http.Client
}

// New returns a client for api.github.com.
func New() *Client {
	return &Client{Base: DefaultBase, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Asset is a file of a release.
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"` // API address; with Accept: application/octet-stream it is the file
}

// Release is a published release.
type Release struct {
	ID          int64     `json:"id"`
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Error is an unsuccessful answer of GitHub.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("github: %d %s", e.Status, e.Message) }

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidRepo checks "owner/repo".
func ValidRepo(s string) bool { return repoRe.MatchString(s) }

func (c *Client) request(ctx context.Context, method, url, token, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func readError(resp *http.Response) error {
	var body struct {
		Message string `json:"message"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	if body.Message == "" {
		body.Message = resp.Status
	}
	return &Error{Status: resp.StatusCode, Message: body.Message}
}

// Releases lists the newest releases. With the previous ETag an unchanged
// list comes back as notModified and costs nothing.
func (c *Client) Releases(ctx context.Context, repo, token, etag string) (rels []Release, newETag string, notModified bool, err error) {
	req, err := c.request(ctx, http.MethodGet, c.Base+"/repos/"+repo+"/releases?per_page=5", token, "application/vnd.github+json")
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
	default:
		return nil, "", false, readError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, "", false, err
	}
	return rels, resp.Header.Get("ETag"), false, nil
}

// Download writes an asset to w.
func (c *Client) Download(ctx context.Context, a Asset, token string, w io.Writer) error {
	req, err := c.request(ctx, http.MethodGet, a.URL, token, "application/octet-stream")
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Pick returns the asset matching mask ("*linux_amd64*") and checksums.txt.
func Pick(r Release, mask string) (bin, sums *Asset) {
	for i := range r.Assets {
		a := &r.Assets[i]
		low := strings.ToLower(a.Name)
		if low == "checksums.txt" || strings.HasSuffix(low, "_checksums.txt") || low == "sha256sums" || low == "sha256sums.txt" {
			sums = a
			continue
		}
		if ok, _ := path.Match(mask, a.Name); ok && bin == nil && !strings.HasSuffix(low, ".sig") && !strings.HasSuffix(low, ".sbom.json") {
			bin = a
		}
	}
	return bin, sums
}

// Checksums parses "sha256  name" lines (the format of sha256sum and
// GoReleaser).
func Checksums(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && len(f[0]) == 64 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out
}

// ErrNoBinary means the archive has no executable file.
var ErrNoBinary = errors.New("github: no executable file in the archive")

// Extract reads a tar.gz: tgpanel.yaml is kept, the first executable
// regular file is passed to use (it is streamed, not held in memory).
func Extract(r io.Reader, use func(name string, bin io.Reader) error) (config []byte, err error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		base := path.Base(h.Name)
		if base == "tgpanel.yaml" || base == "tgpanel.yml" {
			if config, err = io.ReadAll(io.LimitReader(tr, 1<<20)); err != nil {
				return nil, err
			}
			continue
		}
		if !found && h.Mode&0o111 != 0 {
			found = true
			if err := use(base, tr); err != nil {
				return nil, err
			}
		}
	}
	if !found {
		return config, ErrNoBinary
	}
	return config, nil
}

// IsTarGz tells archives from plain binaries by the asset name.
func IsTarGz(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz")
}

// ValidSignature checks X-Hub-Signature-256 ("sha256=<hex>").
func ValidSignature(secret, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || len(secret) == 0 {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// Sign makes the header value (tests and the fake).
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// HookNets reads the addresses GitHub sends webhooks from.
func (c *Client) HookNets(ctx context.Context) ([]netip.Prefix, error) {
	req, err := c.request(ctx, http.MethodGet, c.Base+"/meta", "", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var meta struct {
		Hooks []string `json:"hooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, s := range meta.Hooks {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("github: meta has no hook addresses")
	}
	return out, nil
}

// Version turns a tag into a release version ("v1.2.0" stays, "/" → "-").
func Version(tag string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(strings.TrimSpace(tag))
}
