// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A capability is deliberately not an agent key or a session. Its only consumer
// is ownerSummary, which never installs it in the API authentication context.
const maxConnections = 100

var connectState = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var errConnectionEnded = errors.New("connection ended")
var errConnectConflict = errors.New("connect state already used or connection limit reached")

type Connection struct {
	ID         string     `json:"id"`
	Origin     string     `json:"origin"`
	Instance   string     `json:"instance"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (m *Module) siblingOrigin(origin string) (config.Sibling, bool) {
	for _, sibling := range m.instances.Siblings {
		if sibling.Origin == origin {
			return sibling, true
		}
	}
	return config.Sibling{}, false
}

// CORS is an exact deployment allow-list, never reflected arbitrary input.
// There are no cookies on this path, even if a caller sends one deliberately.
func (m *Module) summaryOrigin(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Origin")
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		httpapi.WriteError(w, 403, "configured sibling origin required")
		return false
	}
	if _, ok := m.siblingOrigin(origins[0]); !ok {
		httpapi.WriteError(w, 403, "configured sibling origin required")
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origins[0])
	return true
}

func (m *Module) summaryPreflight(w http.ResponseWriter, r *http.Request) {
	m.connectionPreflight(w, r, false)
}
func (m *Module) exchangePreflight(w http.ResponseWriter, r *http.Request) {
	m.connectionPreflight(w, r, true)
}
func (m *Module) connectionPreflight(w http.ResponseWriter, r *http.Request, exchange bool) {
	if !m.summaryOrigin(w, r) {
		return
	}
	w.Header().Add("Vary", "Access-Control-Request-Method")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
	method := r.Header.Get("Access-Control-Request-Method")
	headers := strings.Split(strings.ToLower(r.Header.Get("Access-Control-Request-Headers")), ",")
	required, allowed, methods := "authorization", []string{"authorization", "accept"}, "GET, DELETE"
	if exchange {
		required, allowed, methods = "content-type", []string{"content-type"}, "POST"
	}
	if (exchange && method != "POST") || (!exchange && method != "GET" && method != "DELETE") || len(headers) > len(allowed) || !slices.ContainsFunc(headers, func(h string) bool { return strings.TrimSpace(h) == required }) {
		httpapi.WriteError(w, 403, "method or headers not allowed")
		return
	}
	for _, h := range headers {
		if !slices.Contains(allowed, strings.TrimSpace(h)) {
			httpapi.WriteError(w, 403, "header not allowed")
			return
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", methods)
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(allowed, ", "))
	w.Header().Set("Access-Control-Max-Age", "300")
	w.WriteHeader(http.StatusNoContent)
}

// All personal connection mutations share the access-change fence and check
// live authority inside the same transaction, before connection rows.
func (m *Module) connectionWrite(ctx context.Context, p tenant.Principal, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := requireDeskAccessTx(ctx, tx, p); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Match the home Desk's entry rule, including custom person roles. Native
// source permissions in ReadTx continue to narrow the actual summary.
func requireDeskAccessTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	err := authz.RequireTx(ctx, tx, p, "profile.read", authz.Scope{AnyProject: true})
	if err == nil || !errors.Is(err, authz.ErrForbidden) {
		return err
	}
	return authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
}

func connectionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authz.ErrForbidden):
		httpapi.WriteError(w, 403, "active person required")
	case errors.Is(err, errConnectConflict):
		httpapi.WriteError(w, 409, errConnectConflict.Error())
	default:
		httpapi.WriteInternalError(w, r, "decision_desk", err, "connection could not be saved")
	}
}

func (m *Module) connectionPerson(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := browserPerson(w, r)
	if !ok {
		return p, false
	}
	origin := m.publicOrigin
	if origin == "" {
		origin = "http://" + r.Host
		if r.TLS != nil {
			origin = "https://" + r.Host
		}
	}
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin {
		httpapi.WriteError(w, 403, "same-origin confirmation required")
		return p, false
	}
	return p, true
}

func (m *Module) connectInstance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	p, ok := m.connectionPerson(w, r)
	if !ok {
		return
	}
	var in struct {
		Origin    string `json:"origin"`
		State     string `json:"state"`
		Challenge string `json:"code_challenge"`
	}
	decoder := httpapi.NewRequestDecoder(w, r, 2048)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || !connectState.MatchString(in.State) || !connectState.MatchString(in.Challenge) {
		httpapi.WriteError(w, 400, "invalid connect request")
		return
	}
	sibling, allowed := m.siblingOrigin(in.Origin)
	if !allowed {
		httpapi.WriteError(w, 403, "configured sibling origin required")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		connectionError(w, r, err)
		return
	}
	token := "desk_" + p.TenantID + "_" + p.ID + "_" + base64.RawURLEncoding.EncodeToString(secret)
	// The provisional token is never disclosed. Exchange installs a fresh bearer.
	code, err := newConnectionSecret("code_", p)
	if err != nil {
		connectionError(w, r, err)
		return
	}
	hash, stateHash, codeHash := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(in.State)), sha256.Sum256([]byte(code))
	var grant Connection
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err = m.connectionWrite(ctx, p, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM desk_instance_connections WHERE tenant_id=$1 AND principal_id=$2 AND revoked_at IS NULL AND expires_at>now() AND (exchange_code_hash IS NULL OR exchange_used_at IS NOT NULL OR exchange_expires_at>now()) LIMIT 100) c`, p.TenantID, p.ID).Scan(&count); err != nil {
			return err
		}
		if count >= maxConnections {
			return errConnectConflict
		}
		err := tx.QueryRow(ctx, `INSERT INTO desk_instance_connections(tenant_id,principal_id,origin,instance_tag,token_hash,state_hash,exchange_code_hash,pkce_challenge,exchange_expires_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp()+interval '60 seconds') ON CONFLICT (tenant_id,state_hash) DO NOTHING RETURNING id::text,created_at,expires_at`, p.TenantID, p.ID, sibling.Origin, sibling.Tag, hash[:], stateHash[:], codeHash[:], in.Challenge).Scan(&grant.ID, &grant.CreatedAt, &grant.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConnectConflict
		}
		return err
	})
	if err != nil {
		connectionError(w, r, err)
		return
	}
	fragment := url.Values{"state": {in.State}, "code": {code}, "instance": {m.instances.Here}}
	httpapi.WriteJSON(w, 201, map[string]string{"redirect_url": sibling.Origin + "/instances/connected#" + fragment.Encode()})
}

func (m *Module) listConnections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := browserPerson(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out := []Connection{}
	err := db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requireDeskAccessTx(ctx, tx, p); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,origin,instance_tag,created_at,expires_at,last_used_at FROM desk_instance_connections WHERE tenant_id=$1 AND principal_id=$2 AND revoked_at IS NULL AND expires_at>now() AND (exchange_code_hash IS NULL OR exchange_used_at IS NOT NULL) ORDER BY created_at DESC,id DESC LIMIT 100`, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Connection
			if err := rows.Scan(&c.ID, &c.Origin, &c.Instance, &c.CreatedAt, &c.ExpiresAt, &c.LastUsedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		connectionError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"connections": out})
}

func (m *Module) revokeConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := m.connectionPerson(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		httpapi.WriteError(w, 400, "invalid connection")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := m.connectionWrite(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE desk_instance_connections SET revoked_at=coalesce(revoked_at,now()) WHERE tenant_id=$1 AND principal_id=$2 AND id=$3`, p.TenantID, p.ID, id)
		return err
	})
	if err != nil {
		connectionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newConnectionSecret(prefix string, p tenant.Principal) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return prefix + p.TenantID + "_" + p.ID + "_" + base64.RawURLEncoding.EncodeToString(secret), nil
}

func connectionCredential(value, prefix string) (tenant.Principal, []byte, bool) {
	raw, ok := strings.CutPrefix(value, prefix)
	if !ok || len(raw) != 36+1+36+1+43 || !validID(raw[:36]) || raw[36] != '_' || !validID(raw[37:73]) || raw[73] != '_' || !connectState.MatchString(raw[74:]) {
		return tenant.Principal{}, nil, false
	}
	hash := sha256.Sum256([]byte(strings.TrimPrefix(value, "Bearer ")))
	return tenant.Principal{TenantID: raw[:36], ID: raw[37:73], Kind: tenant.Person, BrowserSession: false}, hash[:], true
}
func summaryCredential(r *http.Request) (tenant.Principal, []byte, bool) {
	if len(r.Header.Values("Authorization")) != 1 {
		return tenant.Principal{}, nil, false
	}
	return connectionCredential(r.Header.Get("Authorization"), "Bearer desk_")
}

func (m *Module) exchangeConnection(w http.ResponseWriter, r *http.Request) {
	if !m.summaryOrigin(w, r) {
		return
	}
	var in struct {
		Code     string `json:"code"`
		State    string `json:"state"`
		Verifier string `json:"code_verifier"`
	}
	decoder := httpapi.NewRequestDecoder(w, r, 2048)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || !connectState.MatchString(in.State) || !connectState.MatchString(in.Verifier) {
		httpapi.WriteError(w, 400, "invalid exchange request")
		return
	}
	p, hash, ok := connectionCredential(in.Code, "code_")
	if !ok {
		httpapi.WriteError(w, 401, "connection code refused")
		return
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(r.Context(), p), 8*time.Second)
	defer cancel()
	stateHash := sha256.Sum256([]byte(in.State))
	challengeHash := sha256.Sum256([]byte(in.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])
	var id, expected string
	// Unknown codes never take the tenant fence. No raw code or verifier is stored.
	lookup := func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT c.id::text,c.pkce_challenge,p.name FROM desk_instance_connections c JOIN principals p ON p.tenant_id=c.tenant_id AND p.id=c.principal_id AND p.kind='person' AND p.status='active'
   WHERE c.tenant_id=$1 AND c.principal_id=$2 AND c.exchange_code_hash=$3 AND c.state_hash=$4 AND c.origin=$5 AND c.exchange_used_at IS NULL AND c.exchange_expires_at>clock_timestamp() AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp()`, p.TenantID, p.ID, hash, stateHash[:], r.Header.Get("Origin")).Scan(&id, &expected, &p.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConnectionEnded
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) != 1 {
			return errConnectionEnded
		}
		return nil
	}
	err := db.InTenantReadSnapshot(db.NoProjects(ctx, "decision desk: validate exchange code"), m.pool, p.TenantID, lookup)
	var token string
	var grant Connection
	if err == nil {
		token, err = newConnectionSecret("desk_", p)
		if err == nil {
			err = m.connectionWrite(ctx, p, func(tx pgx.Tx) error {
				if err := lookup(tx); err != nil {
					return err
				}
				tokenHash := sha256.Sum256([]byte(token))
				return tx.QueryRow(ctx, `UPDATE desk_instance_connections SET token_hash=$3,exchange_used_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND exchange_used_at IS NULL RETURNING created_at,expires_at`, p.TenantID, id, tokenHash[:]).Scan(&grant.CreatedAt, &grant.ExpiresAt)
			})
		}
	}
	if errors.Is(err, errConnectionEnded) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, 401, "connection code refused")
		return
	}
	if err != nil {
		connectionError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"token": token, "instance": m.instances.Here, "account": clip(p.Name, 120), "created_at": grant.CreatedAt, "expires_at": grant.ExpiresAt})
}

func (m *Module) disconnectSummary(w http.ResponseWriter, r *http.Request) {
	if !m.summaryOrigin(w, r) {
		return
	}
	p, hash, ok := summaryCredential(r)
	if !ok {
		httpapi.WriteError(w, 401, "summary connection required")
		return
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(r.Context(), p), 8*time.Second)
	defer cancel()
	// Verify before taking the fence; final authorization and update are inside it.
	var id string
	var revoked bool
	lookup := func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text,revoked_at IS NOT NULL FROM desk_instance_connections WHERE tenant_id=$1 AND principal_id=$2 AND token_hash=$3 AND origin=$4`, p.TenantID, p.ID, hash, r.Header.Get("Origin")).Scan(&id, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConnectionEnded
		}
		return err
	}
	err := db.InTenantReadSnapshot(db.NoProjects(ctx, "decision desk: validate self-revocation"), m.pool, p.TenantID, lookup)
	if err == nil && revoked {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err == nil {
		retry, _ := m.summaryAllowance(hash, false)
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			httpapi.WriteError(w, 429, "connection request limit reached")
			return
		}
		err = m.connectionWrite(ctx, p, func(tx pgx.Tx) error {
			if err := lookup(tx); err != nil {
				return err
			}
			if revoked {
				return nil
			}
			_, err := tx.Exec(ctx, `UPDATE desk_instance_connections SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE tenant_id=$1 AND id=$2`, p.TenantID, id)
			return err
		})
	}
	if errors.Is(err, errConnectionEnded) || errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, 401, "connection ended")
		return
	}
	if err != nil {
		connectionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
