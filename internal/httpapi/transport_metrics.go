// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

type connectionIDKey struct{}

// ConnContext labels each accepted connection with an opaque server-generated
// ID. Never use a peer address, credential or database connection identifier.
func (s *Server) ConnContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connectionIDKey{}, newRequestID())
}
func ConnectionID(ctx context.Context) string {
	id, _ := ctx.Value(connectionIDKey{}).(string)
	return id
}

type authenticationKey struct{}

// MarkAuthentication identifies a valid credential lookup or an authenticated
// handler in the outer request counters, across WithContext request clones.
// Invalid/missing credentials never enter the authenticated SLO denominator.
func MarkAuthentication(r *http.Request) {
	if marker, ok := r.Context().Value(authenticationKey{}).(*atomic.Bool); ok {
		marker.Store(true)
	}
}

type requestCounterKey struct {
	Route, StatusClass string
	Authenticated      bool
}
type RequestCounter struct {
	Route         string `json:"route"`
	StatusClass   string `json:"status_class"`
	Authenticated bool   `json:"authenticated"`
	Total         uint64 `json:"total"`
}

type TransportMetrics struct {
	mu        sync.Mutex
	requests  map[requestCounterKey]uint64
	cancelled atomic.Uint64
}

func statusClass(status int) string {
	switch status / 100 {
	case 1:
		return "1xx"
	case 2:
		return "2xx"
	case 3:
		return "3xx"
	case 4:
		return "4xx"
	case 5:
		return "5xx"
	default:
		return "other"
	}
}
func metricRoute(r *http.Request) string {
	if r.Pattern == "" || r.Pattern == "/" || r.Pattern == "/api" || r.Pattern == "/api/" {
		return "unmatched"
	}
	return r.Pattern
}
func (m *TransportMetrics) record(r *http.Request, status int, authenticated, cancelled bool) uint64 {
	if cancelled {
		m.cancelled.Add(1)
	}
	key := requestCounterKey{metricRoute(r), statusClass(status), authenticated}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.requests == nil {
		m.requests = make(map[requestCounterKey]uint64)
	}
	// Production routes are fixed at startup; also bound unusual embedded muxes.
	if _, exists := m.requests[key]; !exists && len(m.requests) >= 8192 {
		key.Route = "other"
	}
	m.requests[key]++
	return m.requests[key]
}

// Snapshot provides process counters to trusted in-process exporters. The
// request log supplies the same dimensions as unit increments for PHAROS;
// no cross-tenant metrics endpoint is exposed to tenant API callers.
func (m *TransportMetrics) Snapshot() ([]RequestCounter, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := make([]RequestCounter, 0, len(m.requests))
	for key, total := range m.requests {
		rows = append(rows, RequestCounter{key.Route, key.StatusClass, key.Authenticated, total})
	}
	return rows, m.cancelled.Load()
}
