package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/github"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// GitHubSource is the GitHub Releases setup of a bot.
type GitHubSource struct {
	BotID      int64     `cbor:"1,keyasint" json:"bot_id"`
	Repo       string    `cbor:"2,keyasint" json:"repo"`
	AssetMask  string    `cbor:"3,keyasint" json:"asset_mask"`
	AutoDeploy bool      `cbor:"4,keyasint" json:"auto_deploy"`
	HasToken   bool      `cbor:"5,keyasint" json:"has_token"`
	HookURL    string    `cbor:"6,keyasint" json:"hook_url"`
	LastTag    string    `cbor:"7,keyasint" json:"last_tag,omitempty"`
	CheckedAt  time.Time `cbor:"8,keyasint,omitzero" json:"checked_at,omitzero"`
	LastError  string    `cbor:"9,keyasint,omitempty" json:"last_error,omitempty"`
}

// GitHubSet changes the setup. Token: nil keeps it, "" removes it.
type GitHubSet struct {
	BotID      int64         `cbor:"1,keyasint"`
	Repo       string        `cbor:"2,keyasint"`
	AssetMask  string        `cbor:"3,keyasint,omitempty"`
	AutoDeploy bool          `cbor:"4,keyasint"`
	Token      *string       `cbor:"5,keyasint,omitempty"`
	Actor      journal.Actor `cbor:"9,keyasint"`
}

// GitHubSaved returns the webhook secret once, when it is created.
type GitHubSaved struct {
	Source     GitHubSource `cbor:"1,keyasint" json:"source"`
	HookSecret string       `cbor:"2,keyasint,omitempty" json:"hook_secret,omitempty"`
}

var githubActor = journal.Actor{Source: "github", Name: "github"}

func (c *Core) githubHookURL(id int64) string {
	host := net.JoinHostPort(c.Cfg.PublicAddr, strconv.Itoa(c.Cfg.WebhookPort))
	return "https://" + host + "/gh/" + strconv.FormatInt(id, 10)
}

// GitHub returns the setup of a bot, or nil.
func (c *Core) GitHub(ctx context.Context, botID int64) (*GitHubSource, error) {
	var g GitHubSource
	var checked sql.NullString
	err := c.DB.R.QueryRowContext(ctx, `SELECT bot_id, repo, asset_mask, auto_deploy, last_tag, checked_at, last_error FROM github_sources WHERE bot_id = ?`, botID).
		Scan(&g.BotID, &g.Repo, &g.AssetMask, &g.AutoDeploy, &g.LastTag, &checked, &g.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.CheckedAt, _ = store.ParseTime(checked.String)
	_, terr := c.Sec.Decrypt(ctx, botID, secrets.GitHubTokenName)
	g.HasToken = terr == nil
	g.HookURL = c.githubHookURL(botID)
	return &g, nil
}

// SetGitHub stores the setup; the first time it creates the webhook secret.
func (c *Core) SetGitHub(ctx context.Context, in GitHubSet) (GitHubSaved, error) {
	var out GitHubSaved
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return out, err
	}
	defer unlock()
	if err := c.botExists(ctx, in.BotID); err != nil {
		return out, err
	}
	in.Repo = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(in.Repo), "https://github.com/"), "/")
	if in.Repo == "" {
		// An empty repository turns GitHub off.
		c.DB.W.ExecContext(ctx, `DELETE FROM github_sources WHERE bot_id = ?`, in.BotID)
		c.Sec.Delete(ctx, in.BotID, secrets.GitHubTokenName)
		c.Sec.Delete(ctx, in.BotID, secrets.GitHubHookName)
		return out, nil
	}
	if !github.ValidRepo(in.Repo) {
		return out, wire.Errorf(wire.CodeBadRequest, "репозиторий в виде owner/repo")
	}
	if in.AssetMask == "" {
		in.AssetMask = "*linux_amd64*"
	}
	if _, err := filepath.Match(in.AssetMask, "x"); err != nil {
		return out, wire.Errorf(wire.CodeBadRequest, "маска файла: %v", err)
	}
	if _, err := c.DB.W.ExecContext(ctx, `INSERT INTO github_sources (bot_id, repo, asset_mask, auto_deploy) VALUES (?, ?, ?, ?)
		ON CONFLICT (bot_id) DO UPDATE SET repo = excluded.repo, asset_mask = excluded.asset_mask, auto_deploy = excluded.auto_deploy,
		etag = CASE WHEN github_sources.repo = excluded.repo THEN github_sources.etag ELSE '' END`,
		in.BotID, in.Repo, in.AssetMask, in.AutoDeploy); err != nil {
		return out, err
	}
	if in.Token != nil {
		if *in.Token == "" {
			c.Sec.Delete(ctx, in.BotID, secrets.GitHubTokenName)
		} else if err := c.Sec.Set(ctx, in.BotID, secrets.GitHubTokenName, secrets.KindPanel, []byte(strings.TrimSpace(*in.Token))); err != nil {
			return out, err
		}
	}
	if _, err := c.Sec.Decrypt(ctx, in.BotID, secrets.GitHubHookName); err != nil {
		b := make([]byte, 24)
		rand.Read(b)
		out.HookSecret = hex.EncodeToString(b)
		if err := c.Sec.Set(ctx, in.BotID, secrets.GitHubHookName, secrets.KindPanel, []byte(out.HookSecret)); err != nil {
			return out, err
		}
	}
	g, err := c.GitHub(ctx, in.BotID)
	if g != nil {
		out.Source = *g
	}
	return out, err
}

func (c *Core) githubToken(ctx context.Context, botID int64) string {
	t, _ := c.Sec.Decrypt(ctx, botID, secrets.GitHubTokenName)
	return string(t)
}

// CheckGitHub looks for new releases of one bot and imports the newest.
func (c *Core) CheckGitHub(ctx context.Context, botID int64) error {
	g, err := c.GitHub(ctx, botID)
	if err != nil || g == nil {
		return err
	}
	var etag string
	c.DB.R.QueryRowContext(ctx, `SELECT etag FROM github_sources WHERE bot_id = ?`, botID).Scan(&etag)
	rels, newETag, same, err := c.GH.Releases(ctx, g.Repo, c.githubToken(ctx, botID), etag)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	c.DB.W.ExecContext(ctx, `UPDATE github_sources SET checked_at = ?, last_error = ?, etag = CASE WHEN ? = '' THEN etag ELSE ? END WHERE bot_id = ?`,
		store.Now(), msg, newETag, newETag, botID)
	if err != nil || same {
		return err
	}
	for _, r := range rels {
		if r.Draft || r.Prerelease {
			continue
		}
		// The newest published release; older ones are not imported.
		if r.Tag == g.LastTag || c.Releases.Exists(botID, github.Version(r.Tag)) {
			return nil
		}
		return c.importRelease(ctx, *g, r)
	}
	return nil
}

// importRelease downloads, verifies and stores a release as a version.
func (c *Core) importRelease(ctx context.Context, g GitHubSource, r github.Release) (err error) {
	version := github.Version(r.Tag)
	defer func() {
		if err != nil {
			c.DB.W.ExecContext(context.WithoutCancel(ctx), `UPDATE github_sources SET last_error = ? WHERE bot_id = ?`, r.Tag+": "+err.Error(), g.BotID)
			c.Ev.Emit("github_release_failed", g.BotID, events.Warn, map[string]any{"tag": r.Tag, "error": err.Error()})
		}
	}()
	bin, sums := github.Pick(r, g.AssetMask)
	if bin == nil {
		return fmt.Errorf("в релизе нет файла по маске %s", g.AssetMask)
	}
	token := c.githubToken(ctx, g.BotID)
	if err := os.MkdirAll(c.Releases.Dir(g.BotID), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Releases.Dir(g.BotID), ".gh-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if err := c.GH.Download(ctx, *bin, token, io.MultiWriter(tmp, h)); err != nil {
		return err
	}
	if sums != nil {
		var sb strings.Builder
		if err := c.GH.Download(ctx, *sums, token, &sb); err != nil {
			return err
		}
		want, ok := github.Checksums(strings.NewReader(sb.String()))[bin.Name]
		if !ok {
			return fmt.Errorf("в %s нет строки для %s", sums.Name, bin.Name)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return fmt.Errorf("SHA-256 %s не совпадает с %s", bin.Name, sums.Name)
		}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var rel deploy.Release
	var config []byte
	err = c.Journal.Do(ctx, githubActor, "releases.upload", g.BotID, map[string]string{"version": version, "repo": g.Repo}, func() error {
		save := func(src io.Reader) error {
			id, err := c.Releases.Begin(g.BotID, version, "github")
			if err != nil {
				return err
			}
			buf := make([]byte, 256<<10)
			for {
				n, rerr := src.Read(buf)
				if n > 0 {
					if err := c.Releases.Write(id, buf[:n]); err != nil {
						c.Releases.Abort(id)
						return err
					}
				}
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					c.Releases.Abort(id)
					return rerr
				}
			}
			rel, err = c.Releases.Finish(id)
			return err
		}
		if github.IsTarGz(bin.Name) {
			var err error
			config, err = github.Extract(tmp, func(_ string, b io.Reader) error { return save(b) })
			return checkErr(err)
		}
		return checkErr(save(tmp))
	})
	if err != nil {
		return err
	}
	if len(config) > 0 {
		os.WriteFile(filepath.Join(c.Releases.Dir(g.BotID), rel.Version, "tgpanel.yaml"), config, 0o640)
	}
	cur, prev, _ := c.versions(ctx, g.BotID)
	c.Releases.Prune(g.BotID, cur, prev)
	c.DB.W.ExecContext(ctx, `UPDATE github_sources SET last_tag = ?, last_error = '' WHERE bot_id = ?`, r.Tag, g.BotID)
	c.Logs.Add(g.BotID, "core", "new release "+rel.Version+" from GitHub", nowUTC())
	c.Ev.Emit("github_release", g.BotID, events.Info, map[string]any{"version": rel.Version, "repo": g.Repo, "auto_deploy": g.AutoDeploy})
	if g.AutoDeploy {
		return c.Journal.Do(ctx, githubActor, "releases.switch", g.BotID, map[string]string{"version": rel.Version}, func() error {
			return c.SwitchVersion(ctx, SwitchVersion{BotID: g.BotID, Version: rel.Version, Actor: githubActor})
		})
	}
	return nil
}

// runGitHub polls every bot with GitHub every 5 minutes.
func (c *Core) runGitHub(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := c.DB.R.QueryContext(ctx, `SELECT bot_id FROM github_sources`)
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := c.CheckGitHub(ctx, id); err != nil && ctx.Err() == nil {
				c.Log.Warn("github check failed", "bot", id, "err", err)
			}
		}
	}
}

// onGitHubHook takes a "release" webhook: the signature is checked with
// the bot's secret, then the release is fetched like by the poller.
func (c *Core) onGitHubHook(ctx context.Context, in edge.GitHubHook) (int, error) {
	secret, err := c.Sec.Decrypt(ctx, in.BotID, secrets.GitHubHookName)
	if err != nil {
		return 404, nil
	}
	if !github.ValidSignature(secret, in.Body, in.Signature) {
		return 401, nil
	}
	switch in.Event {
	case "ping":
		return 200, nil
	case "release":
	default:
		return 204, nil
	}
	var ev struct {
		Action string `json:"action"`
	}
	json.Unmarshal(in.Body, &ev)
	if ev.Action != "published" && ev.Action != "released" {
		return 204, nil
	}
	// Downloading takes time: GitHub waits 10 s at most.
	go func() {
		ctx := context.WithoutCancel(ctx)
		if err := c.CheckGitHub(ctx, in.BotID); err != nil {
			c.Log.Warn("github check after webhook failed", "bot", in.BotID, "err", err)
		}
	}()
	return 202, nil
}

func (c *Core) registerGitHub() {
	c.Mux.Handle("github.get", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (*GitHubSource, error) {
		if err := c.botExists(ctx, id); err != nil {
			return nil, err
		}
		return c.GitHub(ctx, id)
	}))
	c.Mux.Handle("github.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in GitHubSet) (GitHubSaved, error) {
		var out GitHubSaved
		err := c.Journal.Do(ctx, in.Actor, "github.set", in.BotID, map[string]any{"repo": in.Repo, "auto_deploy": in.AutoDeploy}, func() error {
			var err error
			out, err = c.SetGitHub(ctx, in)
			return err
		})
		return out, err
	}))
	c.Mux.Handle("github.check", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (*GitHubSource, error) {
		if err := c.CheckGitHub(ctx, id); err != nil {
			var ge *github.Error
			if errors.As(err, &ge) {
				return nil, wire.Errorf("github", "%s", ge.Message)
			}
			return nil, wire.Errorf("github", "%v", err)
		}
		return c.GitHub(ctx, id)
	}))
	c.Mux.Handle(edge.MethodGitHubHook, wire.Func(func(ctx context.Context, _ *wire.Conn, in edge.GitHubHook) (int, error) {
		return c.onGitHubHook(ctx, in)
	}))
}
