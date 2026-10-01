// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/journal"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

func (m *Module) proxy(w http.ResponseWriter, r *http.Request) {
	p, err := m.person(r, "intake.write", r.PathValue("projectId"))
	if err != nil {
		writeError(w, err)
		return
	}
	sid, operation := r.PathValue("sid"), r.PathValue("operation")
	if !m.sameOrigin(r) || !uuidRE.MatchString(sid) || r.Method == "POST" && !contains([]string{"input", "ws-ticket"}, operation) || r.Method != "POST" && operation != "inline" {
		writeError(w, fail(403, "forbidden"))
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || operation != "inline" && len(q) != 0 || operation == "inline" && (len(q) != 1 || len(q["ticket"]) != 1 || q.Get("ticket") == "" || len(q.Get("ticket")) > 4096 || strings.ContainsAny(q.Get("ticket"), "\r\n")) {
		writeError(w, fail(400, "invalid_request"))
		return
	}
	if operation == "inline" && (!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerToken(r.Header.Get("Connection"), "upgrade")) {
		writeError(w, fail(400, "invalid_request"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	var state journal.AuthorityState
	err = db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(ctx, `SELECT project_id FROM aithema_sessions WHERE tenant_id=$1 AND sid=$2`, p.TenantID, sid).Scan(&project); err != nil {
			return fail(404, "not_found")
		}
		if project != r.PathValue("projectId") {
			return fail(403, "forbidden")
		}
		var err error
		state, err = m.personState(ctx, tx, p, project, sid, true)
		return err
	})
	if err != nil {
		cancel()
		writeError(w, err)
		return
	}
	s, credential, err := m.settings(ctx, p.TenantID)
	if err != nil || credential == "" {
		cancel()
		writeError(w, fail(503, "unavailable"))
		return
	}
	pair, err := m.sessionToken(ctx, p.TenantID, state)
	cancel()
	if err != nil {
		writeError(w, err)
		return
	}
	target, _ := url.Parse(s.ServiceURL)
	if operation != "inline" {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeError(w, fail(413, "too_large"))
			return
		}
		if _, err := tokens.CanonicalJSON(raw); err != nil {
			writeError(w, fail(400, "invalid_request"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	}
	proxy := &httputil.ReverseProxy{Transport: serviceTransport(s, m.servicePolicy), ErrorLog: log.New(io.Discard, "", 0), Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.URL.Path = "/v1/sessions/" + sid + "/" + operation
		pr.Out.URL.RawPath = ""
		pr.Out.URL.RawQuery = q.Encode()
		// Do not relay browser cookies, agent keys, forged forward headers or
		// arbitrary credential-bearing fields to the service.
		pr.Out.Header = make(http.Header)
		pr.Out.Header.Set("Authorization", "Bearer "+string(credential))
		pr.Out.Header.Set("X-Aithema-Session-Token", pair.SessionToken)
		pr.Out.Header.Set("Origin", m.Issuer)
		if operation == "inline" {
			for _, h := range []string{"Sec-WebSocket-Key", "Sec-WebSocket-Version", "Upgrade", "Connection"} {
				pr.Out.Header.Set(h, pr.In.Header.Get(h))
			}
		} else {
			pr.Out.Header.Set("Content-Type", "application/json")
		}
	}, ModifyResponse: func(resp *http.Response) error {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return fail(502, "unavailable")
		}
		if operation == "inline" && resp.StatusCode != 101 {
			return fail(502, "unavailable")
		}
		if operation == "inline" {
			// No extensions were offered upstream; reject compressed/extended
			// streams and scan complete messages before releasing any frame.
			stream, ok := resp.Body.(io.ReadWriteCloser)
			if !ok || resp.Header.Get("Sec-WebSocket-Extensions") != "" {
				return errors.New("unsafe WebSocket output")
			}
			resp.Body = &guardedWebSocket{ReadWriteCloser: stream, credential: []byte(string(credential))}
		}
		if operation != "inline" && (!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || resp.StatusCode < 200 || resp.StatusCode >= 300) {
			return fail(502, "unavailable")
		}
		// Service-controlled cookie, redirect and browser policy headers never
		// escape through the host boundary. JSON responses are size bounded.
		if operation != "inline" {
			raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
			resp.Body.Close()
			if err != nil || len(raw) > 1<<20 || bytes.Contains(raw, []byte(string(credential))) {
				return fail(502, "unavailable")
			}
			if _, err := tokens.CanonicalJSON(raw); err != nil {
				return fail(502, "unavailable")
			}
			resp.Body = io.NopCloser(strings.NewReader(string(raw)))
			resp.ContentLength = int64(len(raw))
		}
		headers := make(http.Header)
		if operation == "inline" {
			for _, h := range []string{"Connection", "Upgrade", "Sec-WebSocket-Accept"} {
				headers.Set(h, resp.Header.Get(h))
			}
		} else {
			headers.Set("Content-Type", "application/json; charset=utf-8")
		}
		headers.Set("Cache-Control", "no-store")
		resp.Header = headers
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) { writeError(w, fail(502, "unavailable")) }}
	// Inline sockets live beyond the handshake; authority polling and revoke
	// callbacks stop the service. HTTP input retains a ten-second deadline.
	if operation != "inline" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
	}
	proxy.ServeHTTP(w, r)
}
func headerToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
