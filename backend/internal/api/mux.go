package api

import "net/http"

// Mux is http.ServeMux that remembers its patterns, so a test can check
// that every route is described in openapi.yaml.
type Mux struct {
	*http.ServeMux
	Patterns []string
}

// NewMux returns an empty router.
func NewMux() *Mux { return &Mux{ServeMux: http.NewServeMux()} }

// HandleFunc registers a handler and records its pattern.
func (m *Mux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.Patterns = append(m.Patterns, pattern)
	m.ServeMux.HandleFunc(pattern, h)
}
