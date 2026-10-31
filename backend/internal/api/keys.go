package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// Keys serves the key management API (plan 2.3). Only the owner issues and
// revokes keys; child keys see the list.
type Keys struct {
	CA      *pki.CA
	GateExe string
	Servers func() []string
	// Audit is called after every change (the audit log arrives in a later
	// step; until then this is only the call site).
	Audit func(r *http.Request, action, object string, details map[string]any)
}

// Register adds the key routes.
func (k *Keys) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/keys", k.list)
	mux.HandleFunc("POST /api/keys", k.issue)
	mux.HandleFunc("POST /api/keys/{serial}/revoke", k.revoke)
}

func (k *Keys) audit(r *http.Request, action, object string, details map[string]any) {
	if k.Audit != nil {
		k.Audit(r, action, object, details)
	}
}

func (k *Keys) list(w http.ResponseWriter, r *http.Request) {
	keys, err := k.CA.List(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if keys == nil {
		keys = []pki.Key{}
	}
	WriteJSON(w, http.StatusOK, keys)
}

func (k *Keys) issue(w http.ResponseWriter, r *http.Request) {
	if !CallerFrom(r.Context()).IsOwner() {
		WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can issue keys")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	b, err := k.CA.NewGate(r.Context(), pki.Child, in.Name, k.Servers())
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	k.audit(r, "key.issue", in.Name, map[string]any{"kind": "child"})
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gate-%s.exe"`, in.Name))
	if err := pki.WriteGate(w, k.GateExe, b); err != nil {
		// Headers are already sent; the download will be cut short.
		CallerLog(r).Error("writing gate.exe failed", "err", err)
	}
}

func (k *Keys) revoke(w http.ResponseWriter, r *http.Request) {
	if !CallerFrom(r.Context()).IsOwner() {
		WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can revoke keys")
		return
	}
	key, err := k.CA.RevokeKey(r.Context(), r.PathValue("serial"), false)
	switch {
	case errors.Is(err, pki.ErrUnknown):
		WriteError(w, http.StatusNotFound, "not_found", "no such key")
		return
	case err != nil:
		WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	k.audit(r, "key.revoke", key.Name, map[string]any{"serial": key.Serial})
	WriteJSON(w, http.StatusOK, key)
}
