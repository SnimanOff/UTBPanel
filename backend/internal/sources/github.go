package sources

import (
	"encoding/json"
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

// registerGitHub adds the GitHub Releases setup of a bot.
func (u *Uploads) registerGitHub(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/github", func(w http.ResponseWriter, r *http.Request) {
		var g *core.GitHubSource
		if err := u.Core.Call(r.Context(), "github.get", botID(r), &g); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, map[string]any{"github": g})
	})
	mux.HandleFunc("PUT /api/bots/{id}/github", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Repo       string  `json:"repo"`
			AssetMask  string  `json:"asset_mask"`
			AutoDeploy bool    `json:"auto_deploy"`
			Token      *string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		req := core.GitHubSet{BotID: botID(r), Repo: in.Repo, AssetMask: in.AssetMask, AutoDeploy: in.AutoDeploy, Token: in.Token, Actor: api.Actor(r.Context())}
		var out core.GitHubSaved
		if err := u.Core.Call(r.Context(), "github.set", req, &out); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		audit.Annotate(r, "github.set", in.Repo, map[string]any{"auto_deploy": in.AutoDeploy, "token": in.Token != nil})
		api.WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /api/bots/{id}/github/check", func(w http.ResponseWriter, r *http.Request) {
		var g *core.GitHubSource
		if err := u.Core.Call(r.Context(), "github.check", botID(r), &g); err != nil {
			bots.WriteCoreError(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, map[string]any{"github": g})
	})
}
