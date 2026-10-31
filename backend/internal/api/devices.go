package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// Devices serves enrollment of new gates and their approval.
type Devices struct {
	CA *pki.CA
	// OnNew is called for every new request (event "new device").
	OnNew func(pki.DeviceRequest)
	Audit func(r *http.Request, action, object string, details map[string]any)
}

// Register adds the routes.
func (d *Devices) Register(mux *http.ServeMux) {
	// Called by gate.exe with its enrollment certificate.
	mux.HandleFunc("POST /enroll", d.enroll)
	mux.HandleFunc("GET /enroll/{id}", d.status)
	// Called from the panel UI.
	mux.HandleFunc("GET /api/devices", d.list)
	mux.HandleFunc("POST /api/devices/{id}/approve", d.approve)
	mux.HandleFunc("POST /api/devices/{id}/reject", d.reject)
}

// EnrollRequest is sent by gate.exe.
type EnrollRequest struct {
	CSR []byte `json:"csr"` // DER
}

// EnrollStatus is returned to gate.exe.
type EnrollStatus struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Code  string `json:"code"`
	Cert  []byte `json:"cert,omitempty"` // DER, when approved
	CA    []byte `json:"ca,omitempty"`   // DER of the panel CA
}

func (d *Devices) enroll(w http.ResponseWriter, r *http.Request) {
	c := CallerFrom(r.Context())
	var in EnrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req, err := d.CA.Enroll(r.Context(), c.Identity, c.IP, in.CSR)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if d.OnNew != nil {
		d.OnNew(req)
	}
	WriteJSON(w, http.StatusAccepted, EnrollStatus{ID: req.ID, State: req.State, Code: req.Code})
}

func (d *Devices) status(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := d.CA.Device(r.Context(), id, CallerFrom(r.Context()).Serial)
	if err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "no such request")
		return
	}
	out := EnrollStatus{ID: req.ID, State: req.State, Code: req.Code}
	if req.State == pki.DeviceApproved {
		out.Cert, out.CA = req.Cert(), d.CA.Cert().Raw
	}
	WriteJSON(w, http.StatusOK, out)
}

func (d *Devices) list(w http.ResponseWriter, r *http.Request) {
	ds, err := d.CA.PendingDevices(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if ds == nil {
		ds = []pki.DeviceRequest{}
	}
	WriteJSON(w, http.StatusOK, ds)
}

func (d *Devices) approve(w http.ResponseWriter, r *http.Request) {
	if !CallerFrom(r.Context()).IsOwner() {
		WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can approve devices")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := d.CA.Approve(r.Context(), id)
	switch {
	case errors.Is(err, pki.ErrUnknown):
		WriteError(w, http.StatusNotFound, "not_found", "no such request")
		return
	case errors.Is(err, pki.ErrNotPending), errors.Is(err, pki.ErrUsed):
		WriteError(w, http.StatusConflict, "not_pending", "the request was already decided")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if d.Audit != nil {
		d.Audit(r, "device.approve", req.Name, map[string]any{"kind": req.Kind, "ip": req.IP})
	}
	WriteJSON(w, http.StatusOK, req)
}

func (d *Devices) reject(w http.ResponseWriter, r *http.Request) {
	if !CallerFrom(r.Context()).IsOwner() {
		WriteError(w, http.StatusForbidden, "owner_only", "only the owner key can reject devices")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := d.CA.Reject(r.Context(), id); err != nil {
		WriteError(w, http.StatusConflict, "not_pending", err.Error())
		return
	}
	if d.Audit != nil {
		d.Audit(r, "device.reject", strconv.FormatInt(id, 10), nil)
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
