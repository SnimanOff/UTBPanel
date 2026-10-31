package bots

import (
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
)

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "bad bot id")
		return 0, false
	}
	return id, true
}
