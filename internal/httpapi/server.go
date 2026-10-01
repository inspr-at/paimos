// SPDX-License-Identifier: AGPL-3.0-only

// Package httpapi wires the HTTP server. Shared contract between P0.2 (owns the
// server) and P0.3 (mounts auth routes and middleware); extend, do not rename.
package httpapi

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/brand"
)

// Module is anything that adds routes under /api (auth, nodes, ...).
type Module interface {
	Mount(mux *http.ServeMux)
}

// PublicModule mounts exact site-root files that must not fall through to the SPA.
// API modules keep using Module and Mount.
type PublicModule interface {
	MountPublic(mux *http.ServeMux)
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
	// PublicURL is this installation's origin, including the Classic API tombstone.
	// Empty uses a relative root instead of another operator's installation.
	PublicURL string
	// Brand is the product's names from AEON_BRAND_FILE (brand.Load at startup);
	// nil serves the embedded brand.json.
	Brand *brand.Brand
	// Codename names a release version (AEON-430); nil, or "", serves no name.
	Codename func(version string) string
	// AithemaOrigin enables browser microphone/WS policy on application
	// documents, including documents that later navigate to the journey in
	// Vue. Empty preserves the default policy everywhere.
	AithemaOrigin string

	// serving is set when the process is in http.Server.Serve.
	// draining is set on SIGTERM before Shutdown. Readiness is serving and
	// not draining; liveness (GET /api/health) ignores both.
	serving  atomic.Bool
	draining atomic.Bool
	// readyProbe overrides the readiness database ping. Nil uses Pool.Ping.
	// Tests inject it; production leaves it nil.
	readyProbe func(context.Context) error

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

	publicMux := http.NewServeMux()
	publicMounted := false
	for _, m := range s.Modules {
		if pub, ok := m.(PublicModule); ok {
			pub.MountPublic(publicMux)
			publicMounted = true
		}
	}

	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/api", api)
	// Old classic API paths that used to be proxied from pm.barta.cm and
	// flow.inspr.at/paimos land here (AEON-175). They moved for good: answer
	// every method with 410 and the new location, without auth and without data.
	root.Handle("/from-classic/api/", commonMiddleware(http.HandlerFunc(s.handleClassicAPIGone)))
	// Exact files only. A /portal/ subtree would take the Vue catalog page off the SPA.
	// The catalog and roadmap HTML patterns are the public declarations for those pages.
	spa := commonMiddleware(spaHandler(s.Web, s.brand()))
	if s.AithemaOrigin != "" {
		spa = commonMiddleware(aithemaViewPolicy(s.AithemaOrigin, spaHandler(s.Web, s.brand())))
	}
	if publicMounted {
		root.Handle("GET /aithema/preview/{design_rev}", commonMiddleware(publicMux))
		root.Handle("GET /portal/{tenantSlug}/llms.txt", commonMiddleware(publicMux))
		root.Handle("GET /portal/{tenantSlug}/catalog.json", commonMiddleware(publicMux))
		root.Handle("GET /portal/{tenantSlug}/roadmap.json", commonMiddleware(publicMux))
		root.Handle("GET /portal/{tenantSlug}/roadmap", spa)
		root.Handle("GET /portal/{tenantSlug}", spa)
	}
	root.Handle("/", spa)
	s.handler = root
}

func (s *Server) handleClassicAPIGone(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	location := strings.TrimRight(s.PublicURL, "/")
	if location == "" {
		location = "/"
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "moved", "location": location})
}
