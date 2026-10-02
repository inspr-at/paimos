// SPDX-License-Identifier: AGPL-3.0-only

// Package collaboration exposes quote-scoped presence and revision notices.
// Mount New alongside quotes.New; the coordinator owns server registration.
// Presence is leased, advisory state. Draft writes remain exclusively in the
// P1 quote module's If-Match/mutation-receipt endpoint.
package collaboration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

const leaseSeconds = 45

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var errDenied = errors.New("quote collaboration is unavailable")
var errMissing = errors.New("quote or session not found")
var errRate = errors.New("presence updates are too frequent")
var errInvalid = errors.New("invalid presence request")

type Module struct {
	pool     *pgxpool.Pool
	registry *plugins.Registry
	now      func() time.Time
}

var _ httpapi.Module = (*Module)(nil)

// ManifestPlugin exposes the existing business_quotes manifest for coordinator
// wiring. Register this or quotes.ManifestPlugin once, then mount both modules;
// collaboration has no separate tenant-installable plugin or digest.
func ManifestPlugin() (plugins.Plugin, error) { return quotes.ManifestPlugin() }

func New(pool *pgxpool.Pool, registry *plugins.Registry) (httpapi.Module, error) {
	if pool == nil || registry == nil {
		return nil, errors.New("collaboration: pool and registry required")
	}
	if _, ok := registry.Lookup("business_quotes"); !ok {
		return nil, errors.New("collaboration: quote manifest is not registered")
	}
	return &Module{pool: pool, registry: registry, now: time.Now}, nil
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/quotes/{quoteId}/presence", m.list)
	mux.HandleFunc("POST /api/quotes/{quoteId}/presence", m.join)
	mux.HandleFunc("PATCH /api/quotes/{quoteId}/presence/{sessionId}", m.heartbeat)
	mux.HandleFunc("DELETE /api/quotes/{quoteId}/presence/{sessionId}", m.leave)
	mux.HandleFunc("GET /api/quotes/{quoteId}/collaboration/stream", m.stream)
}

type anchor struct {
	SectionID        string `json:"section_id"`
	NodeID           string `json:"node_id,omitempty"`
	ObservedRevision int64  `json:"observed_revision"`
	TextSHA256       string `json:"text_sha256,omitempty"`
	Anchor           int    `json:"anchor,omitempty"`
	Focus            int    `json:"focus,omitempty"`
	Fidelity         string `json:"fidelity"`
}
type presence struct {
	SessionID        string    `json:"session_id"`
	PrincipalID      string    `json:"principal_id"`
	Name             string    `json:"name"`
	HasAvatar        bool      `json:"has_avatar"`
	Mode             string    `json:"mode"`
	Anchor           *anchor   `json:"anchor,omitempty"`
	ObservedRevision int64     `json:"observed_revision"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type snapshot struct {
	Sessions      []presence `json:"sessions"`
	DraftRevision int64      `json:"draft_revision"`
	QuoteRevision int64      `json:"quote_revision"`
	State         string     `json:"state"`
}
type presenceWrite struct {
	ResumeSessionID  string  `json:"resume_session_id,omitempty"`
	Mode             string  `json:"mode"`
	Anchor           *anchor `json:"anchor,omitempty"`
	ObservedRevision int64   `json:"observed_revision"`
	Interacted       bool    `json:"interacted,omitempty"`
}

func request(r *http.Request) (tenant.Principal, string, error) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidPattern.MatchString(p.ID) || !uuidPattern.MatchString(p.TenantID) {
		return p, "", errDenied
	}
	id := r.PathValue("quoteId")
	if !uuidPattern.MatchString(id) {
		return p, "", errInvalid
	}
	return p, id, nil
}
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDenied):
		httpapi.WriteError(w, 403, errDenied.Error())
	case errors.Is(err, errMissing):
		httpapi.WriteError(w, 404, errMissing.Error())
	case errors.Is(err, errRate):
		httpapi.WriteError(w, 429, errRate.Error())
	case errors.Is(err, errInvalid):
		httpapi.WriteError(w, 400, errInvalid.Error())
	default:
		httpapi.WriteError(w, 500, "quote collaboration failed")
	}
}
func decode(r *http.Request, value any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 8193))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return errInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errInvalid
	}
	return nil
}
func has(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// authorize reloads current grants and installation digests in
// every transaction, including each stream poll. Public/customer readers never
// learn staff identities through this route.
func (m *Module) authorize(ctx context.Context, tx pgx.Tx, p tenant.Principal, quoteID string, write bool) error {
	if p.Kind != tenant.Person {
		return errDenied
	}
	permission := "quotes.read"
	if write {
		permission = "quotes.write"
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
		return errDenied
	}
	for _, id := range []string{"business_quotes", "business_crm", "business_costs"} {
		plug, ok := m.registry.Lookup(id)
		if !ok {
			return errDenied
		}
		var enabled bool
		var digest string
		var perms []string
		err := tx.QueryRow(ctx, `SELECT enabled,manifest_digest_sha256,permissions FROM plugin_installations WHERE tenant_id=$1::uuid AND plugin_id=$2`, p.TenantID, id).Scan(&enabled, &digest, &perms)
		if errors.Is(err, pgx.ErrNoRows) {
			return errDenied
		}
		if err != nil {
			return err
		}
		if !enabled || digest != plug.Manifest.DigestSHA256 {
			return errDenied
		}
		if id == "business_quotes" && (!has(perms, fence.PermViewsProvide) || write && !has(perms, fence.PermNodesContribute)) {
			return errDenied
		}
	}
	var found bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_quotes q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.quote_node_id WHERE q.tenant_id=$1::uuid AND q.quote_node_id=$2::uuid AND n.deleted_at IS NULL)`, p.TenantID, quoteID).Scan(&found); err != nil {
		return err
	}
	if !found {
		return errMissing
	}
	return nil
}
func (m *Module) inQuote(ctx context.Context, p tenant.Principal, id string, write bool, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorize(ctx, tx, p, id, write); err != nil {
			return err
		}
		return fn(tx)
	})
}
func readSnapshot(ctx context.Context, tx pgx.Tx, id string) (snapshot, error) {
	s := snapshot{Sessions: []presence{}}
	if err := tx.QueryRow(ctx, `SELECT d.draft_revision,q.revision,q.state FROM quote_drafts d JOIN business_quotes q ON q.tenant_id=d.tenant_id AND q.quote_node_id=d.quote_node_id WHERE d.quote_node_id=$1::uuid AND q.deleted_at IS NULL`, id).Scan(&s.DraftRevision, &s.QuoteRevision, &s.State); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s, errMissing
		}
		return s, err
	}
	rows, err := tx.Query(ctx, `SELECT s.session_id::text,s.principal_id::text,p.name,EXISTS (SELECT 1 FROM personal_profiles avatar WHERE avatar.tenant_id=s.tenant_id AND avatar.principal_id=s.principal_id AND avatar.avatar_hashes <> '{}'::jsonb),s.mode,s.anchor,s.observed_revision,s.expires_at FROM quote_presence s JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=s.principal_id WHERE s.quote_node_id=$1::uuid AND s.expires_at>clock_timestamp() ORDER BY s.last_seen DESC,s.session_id LIMIT 50`, id)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var x presence
		var raw []byte
		if err := rows.Scan(&x.SessionID, &x.PrincipalID, &x.Name, &x.HasAvatar, &x.Mode, &raw, &x.ObservedRevision, &x.ExpiresAt); err != nil {
			return s, err
		}
		if len(raw) > 0 {
			var a anchor
			if json.Unmarshal(raw, &a) == nil {
				x.Anchor = &a
			}
		}
		s.Sessions = append(s.Sessions, x)
	}
	return s, rows.Err()
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, id, err := request(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var out snapshot
	err = m.inQuote(r.Context(), p, id, false, func(tx pgx.Tx) error { var e error; out, e = readSnapshot(r.Context(), tx, id); return e })
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:]), nil
}
func validMode(mode string) bool { return mode == "viewing" || mode == "editing" || mode == "idle" }

func validateAnchor(ctx context.Context, tx pgx.Tx, id string, in *anchor) (*anchor, error) {
	if in == nil {
		return nil, nil
	}
	if !uuidPattern.MatchString(in.SectionID) || in.ObservedRevision < 1 || in.Anchor < 0 || in.Focus < 0 || in.Anchor > 65535 || in.Focus > 65535 {
		return nil, errInvalid
	}
	var raw []byte
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT document,draft_revision FROM quote_drafts WHERE quote_node_id=$1::uuid`, id).Scan(&raw, &revision); err != nil {
		return nil, err
	}
	var doc struct {
		Sections []struct {
			ID    string `json:"id"`
			Nodes []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"nodes"`
		} `json:"sections"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, errInvalid
	}
	for _, section := range doc.Sections {
		if section.ID != in.SectionID {
			continue
		}
		block := &anchor{SectionID: in.SectionID, ObservedRevision: in.ObservedRevision, Fidelity: "section"}
		if in.NodeID == "" {
			return block, nil
		}
		for _, node := range section.Nodes {
			if node.ID != in.NodeID {
				continue
			}
			if in.ObservedRevision != revision {
				return block, nil
			}
			sum := shaText(node.Text)
			if !strings.EqualFold(sum, in.TextSHA256) {
				return block, nil
			}
			units := utf16.Encode([]rune(node.Text))
			if in.Anchor > len(units) || in.Focus > len(units) || !boundary(units, in.Anchor) || !boundary(units, in.Focus) {
				return block, nil
			}
			return &anchor{SectionID: in.SectionID, NodeID: in.NodeID, ObservedRevision: revision, TextSHA256: sum, Anchor: in.Anchor, Focus: in.Focus, Fidelity: "precise"}, nil
		}
		return block, nil
	}
	return nil, errInvalid
}
func shaText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func boundary(units []uint16, n int) bool {
	return n == 0 || n == len(units) || !(units[n-1] >= 0xd800 && units[n-1] <= 0xdbff && units[n] >= 0xdc00 && units[n] <= 0xdfff)
}

func (m *Module) join(w http.ResponseWriter, r *http.Request) {
	p, id, err := request(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in presenceWrite
	if err = decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !validMode(in.Mode) || in.ObservedRevision < 1 || in.ResumeSessionID != "" && !uuidPattern.MatchString(in.ResumeSessionID) {
		writeError(w, errInvalid)
		return
	}
	var session string
	var out snapshot
	err = m.inQuote(r.Context(), p, id, in.Mode == "editing", func(tx pgx.Tx) error {
		a, e := validateAnchor(r.Context(), tx, id, in.Anchor)
		if e != nil {
			return e
		}
		if in.ResumeSessionID != "" {
			var owner string
			e = tx.QueryRow(r.Context(), `SELECT principal_id::text FROM quote_presence WHERE quote_node_id=$1::uuid AND session_id=$2::uuid AND expires_at>clock_timestamp()`, id, in.ResumeSessionID).Scan(&owner)
			if e == nil {
				if owner != p.ID {
					return errDenied
				}
				session = in.ResumeSessionID
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		if session == "" {
			// Serialize per-principal membership allocation across app instances.
			if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, p.TenantID+":"+id+":"+p.ID); e != nil {
				return e
			}
			session, e = newUUID()
			if e != nil {
				return e
			}
			var count int
			if e = tx.QueryRow(r.Context(), `SELECT count(*) FROM quote_presence WHERE quote_node_id=$1::uuid AND principal_id=$2::uuid AND expires_at>clock_timestamp()`, id, p.ID).Scan(&count); e != nil {
				return e
			}
			if count >= 8 {
				return errRate
			}
			if e = tx.QueryRow(r.Context(), `SELECT count(*) FROM quote_presence WHERE quote_node_id=$1::uuid AND expires_at>clock_timestamp()`, id).Scan(&count); e != nil {
				return e
			}
			if count >= 50 {
				return errRate
			}
		}
		payload, e := json.Marshal(a)
		if e != nil {
			return e
		}
		if a == nil {
			payload = nil
		}
		_, e = tx.Exec(r.Context(), `INSERT INTO quote_presence(tenant_id,quote_node_id,session_id,principal_id,mode,anchor,observed_revision) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6::jsonb,$7) ON CONFLICT(tenant_id,quote_node_id,session_id) DO UPDATE SET mode=excluded.mode,anchor=excluded.anchor,observed_revision=excluded.observed_revision,last_seen=clock_timestamp(),last_interaction=clock_timestamp(),expires_at=clock_timestamp()+interval '45 seconds'`, p.TenantID, id, session, p.ID, in.Mode, payload, in.ObservedRevision)
		if e != nil {
			return e
		}
		out, e = readSnapshot(r.Context(), tx, id)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 201, struct {
		SessionID string   `json:"session_id"`
		Snapshot  snapshot `json:"snapshot"`
	}{session, out})
}
func (m *Module) heartbeat(w http.ResponseWriter, r *http.Request) {
	p, id, err := request(r)
	if err != nil {
		writeError(w, err)
		return
	}
	session := r.PathValue("sessionId")
	if !uuidPattern.MatchString(session) {
		writeError(w, errInvalid)
		return
	}
	var in presenceWrite
	if err = decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !validMode(in.Mode) || in.ObservedRevision < 1 || in.ResumeSessionID != "" {
		writeError(w, errInvalid)
		return
	}
	var out snapshot
	err = m.inQuote(r.Context(), p, id, in.Mode == "editing", func(tx pgx.Tx) error {
		var owner string
		var last time.Time
		var lastInteraction time.Time
		var priorMode string
		var priorAnchor []byte
		e := tx.QueryRow(r.Context(), `SELECT principal_id::text,last_seen,last_interaction,mode,anchor FROM quote_presence WHERE quote_node_id=$1::uuid AND session_id=$2::uuid AND expires_at>clock_timestamp() FOR UPDATE`, id, session).Scan(&owner, &last, &lastInteraction, &priorMode, &priorAnchor)
		if errors.Is(e, pgx.ErrNoRows) {
			return errMissing
		}
		if e != nil {
			return e
		}
		if owner != p.ID {
			return errDenied
		}
		now := m.now()
		if now.Sub(last) < 200*time.Millisecond {
			return errRate
		}
		a, e := validateAnchor(r.Context(), tx, id, in.Anchor)
		if e != nil {
			return e
		}
		payload, e := json.Marshal(a)
		if e != nil {
			return e
		}
		if a == nil {
			payload = nil
		}
		interaction := in.Mode == "editing" && (in.Interacted || !jsonEqual(priorAnchor, payload))
		mode := in.Mode
		if mode == "editing" && !interaction && now.Sub(lastInteraction) >= 60*time.Second {
			mode = "idle"
		}
		_, e = tx.Exec(r.Context(), `UPDATE quote_presence SET mode=$1,anchor=$2::jsonb,observed_revision=$3,last_seen=clock_timestamp(),last_interaction=CASE WHEN $4 THEN clock_timestamp() ELSE last_interaction END,expires_at=clock_timestamp()+interval '45 seconds' WHERE quote_node_id=$5::uuid AND session_id=$6::uuid`, mode, payload, in.ObservedRevision, interaction, id, session)
		if e != nil {
			return e
		}
		out, e = readSnapshot(r.Context(), tx, id)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}
func jsonEqual(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}
func (m *Module) leave(w http.ResponseWriter, r *http.Request) {
	p, id, err := request(r)
	if err != nil {
		writeError(w, err)
		return
	}
	session := r.PathValue("sessionId")
	if !uuidPattern.MatchString(session) {
		writeError(w, errInvalid)
		return
	}
	err = m.inQuote(r.Context(), p, id, false, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `DELETE FROM quote_presence WHERE quote_node_id=$1::uuid AND session_id=$2::uuid AND principal_id=$3::uuid`, id, session, p.ID)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}

// durableNotice contains no document, event before/after, capability or cursor.
type durableNotice struct {
	ID               int64  `json:"id"`
	QuoteNodeID      string `json:"quote_node_id"`
	Type             string `json:"type"`
	ActorPrincipalID string `json:"actor_principal_id"`
	DraftRevision    int64  `json:"draft_revision"`
	QuoteRevision    int64  `json:"quote_revision"`
	State            string `json:"state"`
	ClientSessionID  string `json:"client_session_id,omitempty"`
	MutationID       string `json:"mutation_id,omitempty"`
}

func readNotices(ctx context.Context, tx pgx.Tx, id string, after int64, s snapshot) ([]durableNotice, error) {
	rows, err := tx.Query(ctx, `SELECT id,type,actor_principal_id::text,coalesce(after->>'client_session_id',''),coalesce(after->>'mutation_id',''),CASE WHEN type='quote.draft_updated' THEN (after->>'draft_revision')::bigint END,CASE WHEN type='quote.draft_updated' THEN (after->>'quote_revision')::bigint END FROM events WHERE node_id=$1::uuid AND id>$2 AND type LIKE 'quote.%' ORDER BY id LIMIT 100`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []durableNotice{}
	for rows.Next() {
		n := durableNotice{QuoteNodeID: id, DraftRevision: s.DraftRevision, QuoteRevision: s.QuoteRevision, State: s.State}
		var draft, quote *int64
		if err := rows.Scan(&n.ID, &n.Type, &n.ActorPrincipalID, &n.ClientSessionID, &n.MutationID, &draft, &quote); err != nil {
			return nil, err
		}
		if draft != nil {
			n.DraftRevision = *draft
		}
		if quote != nil {
			n.QuoteRevision = *quote
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (m *Module) stream(w http.ResponseWriter, r *http.Request) {
	p, id, err := request(r)
	if err != nil {
		writeError(w, err)
		return
	}
	after := int64(-1)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			writeError(w, errInvalid)
			return
		}
	}
	var first snapshot
	err = m.inQuote(r.Context(), p, id, false, func(tx pgx.Tx) error {
		if after < 0 {
			if e := tx.QueryRow(r.Context(), `SELECT coalesce(max(id),0) FROM events`).Scan(&after); e != nil {
				return e
			}
		}
		var e error
		first, e = readSnapshot(r.Context(), tx, id)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	send := func(kind string, id int64, value any) error {
		raw, e := json.Marshal(value)
		if e != nil {
			return e
		}
		if e = rc.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil && !errors.Is(e, http.ErrNotSupported) {
			return e
		}
		prefix := ""
		if id > 0 {
			prefix = fmt.Sprintf("id: %d\n", id)
		}
		if _, e = fmt.Fprintf(w, "%sevent: %s\ndata: %s\n\n", prefix, kind, raw); e != nil {
			return e
		}
		return rc.Flush()
	}
	if send("presence", 0, first) != nil {
		return
	}
	lastPresence, _ := json.Marshal(first)
	lastKeepalive := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			var s snapshot
			var notices []durableNotice
			err = m.inQuote(r.Context(), p, id, false, func(tx pgx.Tx) error {
				var e error
				s, e = readSnapshot(r.Context(), tx, id)
				if e != nil {
					return e
				}
				notices, e = readNotices(r.Context(), tx, id, after, s)
				return e
			})
			if err != nil {
				_ = send("access_revoked", 0, map[string]string{"reason": "access unavailable"})
				return
			}
			for _, n := range notices {
				if send("quote_change", n.ID, n) != nil {
					return
				}
				after = n.ID
			}
			current, _ := json.Marshal(s)
			if !bytes.Equal(current, lastPresence) {
				if send("presence", 0, s) != nil {
					return
				}
				lastPresence = current
				lastKeepalive = time.Now()
			} else if time.Since(lastKeepalive) >= 15*time.Second {
				if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
					return
				}
				lastKeepalive = time.Now()
			}
		}
	}
}
