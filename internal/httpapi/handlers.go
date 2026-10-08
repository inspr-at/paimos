// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/brand"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/version"
)

// readyProbeTimeout caps the readiness database ping so an unbounded caller
// cannot hold the probe open. The cap is derived from the request context:
// a cancelled request and an earlier deadline still end the ping. It is
// shorter than the 1s load-balancer health-check timeout so a slow database
// returns 503 before that check gives up.
const readyProbeTimeout = 500 * time.Millisecond

type healthBody struct {
	Status string `json:"status"`
	DB     string `json:"db"`
}

type readyBody struct {
	Status string        `json:"status"`
	Reason string        `json:"reason,omitempty"`
	Detail string        `json:"detail,omitempty"`
	Pool   *db.PoolStats `json:"pool,omitempty"`
}

// versionBody carries Codename, the running release's marketing name
// (AEON-430), which the footer shows instead of the version; absent when the
// build has none.
type versionBody struct {
	Version  string      `json:"version"`
	Scheme   string      `json:"scheme"`
	Brand    brand.Brand `json:"brand"`
	Codename string      `json:"codename,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbState := "down"
	if s.Pool != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyProbeTimeout)
		defer cancel()
		if err := db.Probe(ctx, s.Pool); err != nil {
			slog.Warn("liveness database probe failed", "pool", db.Stats(s.Pool))
		} else {
			dbState = "ok"
		}
	}
	WriteJSON(w, http.StatusOK, healthBody{Status: "ok", DB: dbState})
}

// handleReady is the load-balancer probe. It is 200 only while this process
// should receive new requests. GET /api/health stays a liveness report and
// does not change during drain.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body := readyBody{Status: "unavailable"}
	if s.Pool != nil {
		stats := db.Stats(s.Pool)
		body.Pool = &stats
	}
	if !s.accepting() || !s.readyPingAvailable() {
		body.Reason = "not_accepting"
		if s.accepting() {
			body.Reason = "database_unavailable"
		}
		WriteJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	// WithTimeout bounds a caller that has no deadline. It keeps cancellation
	// and any earlier deadline from the request.
	ctx, cancel := context.WithTimeout(r.Context(), readyProbeTimeout)
	defer cancel()
	if err := s.pingReady(ctx); err != nil {
		body.Reason = "database_unavailable"
		if s.Pool != nil {
			stats := db.Stats(s.Pool)
			body.Pool = &stats
		}
		if errors.Is(err, db.ErrPoolExhausted) {
			body.Reason = "pool_exhausted"
			body.Detail = fmt.Sprintf("pool exhausted: %d acquired, %d waiting", body.Pool.Acquired, body.Pool.Waiting)
		}
		slog.Warn("readiness database probe failed", "reason", body.Reason, "pool", body.Pool)
		WriteJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	// Drain can start while the ping is in flight. The check above would
	// otherwise answer ready from a state that is already false.
	if !s.accepting() {
		body.Reason = "not_accepting"
		WriteJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	body.Status = "ready"
	if s.Pool != nil {
		stats := db.Stats(s.Pool)
		body.Pool = &stats
	}
	WriteJSON(w, http.StatusOK, body)
}

func (s *Server) readyPingAvailable() bool {
	return s.readyProbe != nil || s.Pool != nil
}

func (s *Server) pingReady(ctx context.Context) error {
	if s.readyProbe != nil {
		return s.readyProbe(ctx)
	}
	return db.Probe(ctx, s.Pool)
}

// handleVersion answers the build's calendar version and the product's names
// (brand.json, or the deployment's AEON_BRAND_FILE when the server was given one).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	body := versionBody{Version: version.Version, Scheme: version.Scheme, Brand: s.brand()}
	if s.Codename != nil {
		body.Codename = s.Codename(version.Version)
	}
	WriteJSON(w, http.StatusOK, body)
}

// brand is the deployment's brand, else the embedded brand.json.
func (s *Server) brand() brand.Brand {
	if s.Brand != nil {
		return *s.Brand
	}
	return brand.Default()
}
