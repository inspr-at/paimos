// SPDX-License-Identifier: AGPL-3.0-only

// Package httpapi wires the HTTP server. Shared contract between P0.2 (owns the
// server) and P0.3 (mounts auth routes and middleware); extend, do not rename.
package httpapi

import (
	"io/fs"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/brand"
)

// Module is anything that adds routes under /api (auth, nodes, ...).
type Module interface {
	Mount(mux *http.ServeMux)
}

// Server holds what every module needs.
type Server struct {
	Mux        *http.ServeMux
	Pool       *pgxpool.Pool
	Middleware []func(http.Handler) http.Handler // applied outermost-first to /api
	Modules    []Module
	// Web is the SPA filesystem (AEON_WEB_DIR or the webembed dist).
	// Nil serves a placeholder page.
	Web fs.FS
	// Brand is the product's names from AEON_BRAND_FILE (brand.Load at startup);
	// nil serves the embedded brand.json.
	Brand *brand.Brand

	// serving is set when the process is in http.Server.Serve.
	// draining is set on SIGTERM before Shutdown. Readiness is serving and
	// not draining; liveness (GET /api/health) ignores both.
	serving  atomic.Bool
	draining atomic.Bool

	once    sync.Once
	handler http.Handler
}

// SetServing reports whether the process is inside Serve.
// The zero value is not serving, so a handler built in tests is not ready
// until the test opts in.
func (s *Server) SetServing(on bool) {
	s.serving.Store(on)
}

// Drain makes readiness fail while the listener still accepts. In-flight
// handlers, including SSE, keep running until Shutdown.
func (s *Server) Drain() {
	s.draining.Store(true)
}

func (s *Server) accepting() bool {
	return s.serving.Load() && !s.draining.Load()
}

// Handler composes routes. Mux is the /api mux; modules register full paths
// such as "GET /api/auth/login". Middleware wraps /api only, outermost first.
// Recover, request id and security headers wrap every response.
// Call Handler once, after Modules, Middleware and Web are set.
func (s *Server) Handler() http.Handler {
	s.once.Do(s.build)
	return s.handler
}

func (s *Server) build() {
	if s.Mux == nil {
		s.Mux = http.NewServeMux()
	}
	s.Mux.HandleFunc("GET /api/health", s.handleHealth)
	s.Mux.HandleFunc("GET /api/ready", s.handleReady)
	s.Mux.HandleFunc("GET /api/version", s.handleVersion)
	for _, m := range s.Modules {
		m.Mount(s.Mux)
	}
	s.Mux.HandleFunc("/api/", handleNotFound)
	s.Mux.HandleFunc("/api", handleNotFound)

	var api http.Handler = s.Mux
	for i := len(s.Middleware) - 1; i >= 0; i-- {
		api = s.Middleware[i](api)
	}
	api = readStatementTimeoutMiddleware(api)
	api = routePatternMiddleware(s.Mux, api)
	api = commonMiddleware(api)

	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/api", api)
	// Cutover step 5 proxies classic API paths from pm.barta.cm and
	// flow.inspr.at/paimos here (AEON-175). They moved for good: answer every
	// method with 410 and the new location, without auth and without data.
	root.Handle("/from-classic/api/", commonMiddleware(http.HandlerFunc(handleClassicAPIGone)))
	root.Handle("/", commonMiddleware(spaHandler(s.Web, s.brand())))
	s.handler = root
}

func handleClassicAPIGone(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = w.Write([]byte(`{"error":"moved","location":"https://aeon.barta.cm"}` + "\n"))
}
