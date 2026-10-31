package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// InImageEnv is set in the Docker image; dev mode refuses to run there.
const InImageEnv = "TGPANEL_IN_IMAGE"

// ErrDevInImage is returned when dev mode is asked for inside the image.
var ErrDevInImage = errors.New("dev mode is not allowed in the Docker image")

// DevHandler serves the API without mTLS for the vite dev server: every
// request acts as the owner key "dev". Only on 127.0.0.1, only with
// TGPANEL_DEV=1, never in the image.
func (s *Server) DevHandler() http.Handler {
	var h http.Handler = s.Mux
	for i := len(s.middleware) - 1; i >= 0; i-- {
		h = s.middleware[i](h)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		c := Caller{Identity: pki.Identity{Name: "dev", Kind: pki.Owner, Serial: "dev"}, IP: ip, Source: SourceUI}
		h.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), c)))
	})
}

// ListenDev serves the dev listener until ctx ends.
func (s *Server) ListenDev(ctx context.Context, addr string) error {
	if os.Getenv(InImageEnv) != "" {
		return ErrDevInImage
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		return errors.New("dev listener must be on 127.0.0.1")
	}
	srv := &http.Server{Addr: addr, Handler: s.DevHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	s.Log.Warn("DEV listener without mTLS", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
