// Package server serves the daemon's status page and liveness endpoint.
package server

import (
	"embed"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/state"
)

//go:embed templates/*.html
var templateFS embed.FS

// Vendored asset, pinned version and hash in the Makefile (make fetch-htmx).
//go:embed assets/htmx.min.js
var assetsFS embed.FS

// Server renders the daemon's status pages.
type Server struct {
	cfg *config.Config
	db  *state.DB
}

// New returns a Server reading accounts from cfg and cycle results from db.
func New(cfg *config.Config, db *state.DB) *Server {
	return &Server{cfg: cfg, db: db}
}

// Handler returns the fully routed handler with middleware applied. It does
// not bind ports or run an http.Server.
func (s *Server) Handler() http.Handler {
	return withMiddleware(s.routes())
}

// routes returns the ServeMux served by Handler.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleStatus)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /assets/htmx.min.js", handleHTMX)
	return mux
}

// withMiddleware wraps h with panic recovery and request logging.
func withMiddleware(h http.Handler) http.Handler {
	return recoverPanics(logRequests(h))
}

// recoverPanics converts a panicking handler into a 500 response.
func recoverPanics(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Logger.Error().
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Interface("panic", rec).
					Msg("handler panic")
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	})
}

// logRequests writes one line per request: method, path, status, duration.
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		h.ServeHTTP(rec, r)
		log.Logger.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.code).
			Str("duration", time.Since(start).Round(time.Microsecond).String()).
			Msg("request")
	})
}

// statusWriter records the response status code.
type statusWriter struct {
	http.ResponseWriter
	code    int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.code = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}
