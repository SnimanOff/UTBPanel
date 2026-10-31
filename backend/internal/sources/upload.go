// Package sources brings bot binaries into the panel: uploads and, later,
// GitHub Releases. The binary goes to the core as a stream of chunks and
// is never held in memory whole.
package sources

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

const chunkSize = 256 << 10

// Uploads serves release uploads and version switches.
type Uploads struct {
	Core *wire.Client
}

// Register adds the routes.
func (u *Uploads) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/bots/{id}/releases", u.upload)
	mux.HandleFunc("GET /api/bots/{id}/releases", u.list)
	mux.HandleFunc("POST /api/bots/{id}/releases/{version}/switch", u.switchVersion)
}

func botID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// Send streams r to the core as a new release.
func Send(ctx context.Context, c *wire.Client, botID int64, version, source string, actor journal.Actor, r io.Reader) (deploy.Release, error) {
	var id string
	if err := c.Call(ctx, "releases.begin", core.UploadBegin{BotID: botID, Version: version, Source: source, Actor: actor}, &id); err != nil {
		return deploy.Release{}, err
	}
	buf := make([]byte, chunkSize)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if cerr := c.Call(ctx, "releases.chunk", core.UploadChunk{ID: id, Data: buf[:n]}, nil); cerr != nil {
				c.Call(context.WithoutCancel(ctx), "releases.abort", id, nil)
				return deploy.Release{}, cerr
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			c.Call(context.WithoutCancel(ctx), "releases.abort", id, nil)
			return deploy.Release{}, err
		}
	}
	var rel deploy.Release
	err := c.Call(ctx, "releases.finish", core.UploadFinish{ID: id, BotID: botID, Actor: actor}, &rel)
	return rel, err
}

func (u *Uploads) upload(w http.ResponseWriter, r *http.Request) {
	id := botID(r)
	mr, err := r.MultipartReader()
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "send the binary as multipart/form-data, field \"file\"")
		return
	}
	version := ""
	var part *multipart.Part
	for {
		p, err := mr.NextPart()
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", "no \"file\" field")
			return
		}
		if p.FormName() == "version" {
			b, _ := io.ReadAll(io.LimitReader(p, 100))
			version = string(b)
			continue
		}
		if p.FormName() == "file" {
			part = p
			break
		}
	}
	rel, err := Send(r.Context(), u.Core, id, version, "upload", api.Actor(r.Context()), part)
	if err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "release.upload", rel.Version, map[string]any{"sha256": rel.SHA256, "size": rel.Size})
	api.WriteJSON(w, http.StatusCreated, rel)
}

func (u *Uploads) list(w http.ResponseWriter, r *http.Request) {
	var rs []deploy.Release
	if err := u.Core.Call(r.Context(), "releases.list", botID(r), &rs); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, rs)
}

func (u *Uploads) switchVersion(w http.ResponseWriter, r *http.Request) {
	in := core.SwitchVersion{BotID: botID(r), Version: r.PathValue("version"), Actor: api.Actor(r.Context())}
	if err := u.Core.Call(r.Context(), "releases.switch", in, nil); err != nil {
		bots.WriteCoreError(w, err)
		return
	}
	audit.Annotate(r, "release.switch", in.Version, nil)
	api.WriteJSON(w, http.StatusOK, map[string]string{"version": in.Version})
}
