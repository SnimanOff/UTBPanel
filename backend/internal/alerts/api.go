package alerts

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
)

// Register adds the feed routes.
func (s *Service) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/notifications", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		ns, err := s.List(r.Context(), limit)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, ns)
	})
	mux.HandleFunc("POST /api/notifications/read", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []int64 `json:"ids"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
				api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
		}
		if err := s.MarkRead(r.Context(), in.IDs); err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/notifications/problems", func(w http.ResponseWriter, r *http.Request) {
		ids, err := s.Problems(r.Context())
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, ids)
	})
	// The stream for the interface and gate.exe (Windows notifications).
	mux.HandleFunc("GET /api/notifications/stream", func(w http.ResponseWriter, r *http.Request) {
		ch, cancel := s.Subscribe()
		defer cancel()
		sse := api.NewSSE(w)
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case n := <-ch:
				if sse.Send(strconv.FormatInt(n.ID, 10), "notification", n) != nil {
					return
				}
			case <-ping.C:
				if sse.Ping() != nil {
					return
				}
			}
		}
	})
}
