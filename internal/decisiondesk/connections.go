// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
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
	if !m.summaryOrigin(w, r) {
		return
	}
	w.Header().Add("Vary", "Access-Control-Request-Method")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
	headers := strings.Split(strings.ToLower(r.Header.Get("Access-Control-Request-Headers")), ",")
	if r.Header.Get("Access-Control-Request-Method") != "GET" || len(headers) > 2 || !slices.ContainsFunc(headers, func(h string) bool { return strings.TrimSpace(h) == "authorization" }) {
		httpapi.WriteError(w, 403, "only summary reads are allowed")
		return
	}
	for _, h := range headers {
		if h = strings.TrimSpace(h); h != "authorization" && h != "accept" {
			httpapi.WriteError(w, 403, "header not allowed")
			return
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Accept")
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
		if err := authz.RequireTx(ctx, tx, p, "profile.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		return fn(tx)
	})
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
		Origin string `json:"origin"`
		State  string `json:"state"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || !connectState.MatchString(in.State) {
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
	hash, stateHash := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(in.State))
	var grant Connection
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := m.connectionWrite(ctx, p, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM desk_instance_connections WHERE tenant_id=$1 AND principal_id=$2 AND revoked_at IS NULL AND expires_at>now() LIMIT 100) c`, p.TenantID, p.ID).Scan(&count); err != nil {
			return err
		}
		if count >= maxConnections {
			return errConnectConflict
		}
		err := tx.QueryRow(ctx, `INSERT INTO desk_instance_connections(tenant_id,principal_id,origin,instance_tag,token_hash,state_hash)
		 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,state_hash) DO NOTHING RETURNING id::text,created_at,expires_at`, p.TenantID, p.ID, sibling.Origin, sibling.Tag, hash[:], stateHash[:]).Scan(&grant.ID, &grant.CreatedAt, &grant.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConnectConflict
		}
		return err
	})
	if err != nil {
		connectionError(w, r, err)
		return
	}
	fragment := url.Values{"state": {in.State}, "token": {token}, "instance": {m.instances.Here}, "account": {clip(p.Name, 120)}, "expires_at": {grant.ExpiresAt.UTC().Format(time.RFC3339)}, "created_at": {grant.CreatedAt.UTC().Format(time.RFC3339)}}
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
		if err := authz.RequireTx(ctx, tx, p, "profile.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,origin,instance_tag,created_at,expires_at,last_used_at FROM desk_instance_connections WHERE tenant_id=$1 AND principal_id=$2 AND revoked_at IS NULL AND expires_at>now() ORDER BY created_at DESC,id DESC LIMIT 100`, p.TenantID, p.ID)
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

func summaryCredential(r *http.Request) (tenant.Principal, []byte, bool) {
	if len(r.Header.Values("Authorization")) != 1 {
		return tenant.Principal{}, nil, false
	}
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer desk_")
	parts := strings.Split(raw, "_")
	// The final base64url secret can itself contain underscores.
	if !ok || len(raw) != 36+1+36+1+43 || len(parts) < 3 || !validID(raw[:36]) || raw[36] != '_' || !validID(raw[37:73]) || raw[73] != '_' || !connectState.MatchString(raw[74:]) {
		return tenant.Principal{}, nil, false
	}
	hash := sha256.Sum256([]byte("desk_" + raw))
	return tenant.Principal{TenantID: raw[:36], ID: raw[37:73], Kind: tenant.Person, BrowserSession: true}, hash[:], true
}
