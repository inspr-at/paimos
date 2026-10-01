// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/aithema/journal"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type sessionTokens struct {
	Session      string `json:"sid"`
	Generation   int64  `json:"worker_generation"`
	Epoch        int64  `json:"auth_epoch"`
	Callback     string `json:"callback_id,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	// Only server-to-server delivery may use this bearer. It must never be
	// serialized into a browser/person response, including future call sites.
	DelegatedToken string `json:"-"`
}

func (m *Module) sessionToken(ctx context.Context, tid string, state journal.AuthorityState) (sessionTokens, error) {
	var a authorization
	if json.Unmarshal(state.Authorization, &a) != nil || m.Keys == nil {
		return sessionTokens{}, fail(503, "unavailable")
	}
	var mode string
	err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT host_mode FROM aithema_sessions WHERE tenant_id=$1 AND sid=$2`, tid, a.Session).Scan(&mode)
	})
	if err != nil {
		return sessionTokens{}, err
	}
	now := m.clock().Unix()
	c := tokens.Claims{Issuer: m.Issuer, Audience: "aithema", Subject: owner(a), TenantID: tid, ProjectID: a.Project, SessionID: a.Session, ActorKind: "person", HostMode: mode, Scope: []string{"session.converse", "session.upload", "session.confirm", "session.export"}, AuthEpoch: state.Epoch, IssuedAt: now, ExpiresAt: now + 900}
	st, err := m.Keys.MintSession(ctx, c)
	if err != nil {
		return sessionTokens{}, fail(503, "unavailable")
	}
	return sessionTokens{Session: a.Session, Generation: state.Generation, Epoch: state.Epoch, SessionToken: st}, nil
}

func (m *Module) tokenPair(ctx context.Context, tid string, state journal.AuthorityState) (sessionTokens, error) {
	pair, err := m.sessionToken(ctx, tid, state)
	if err != nil {
		return sessionTokens{}, err
	}
	var a authorization
	if json.Unmarshal(state.Authorization, &a) != nil {
		return sessionTokens{}, fail(503, "unavailable")
	}
	now := m.clock().Unix()
	c := tokens.Claims{Issuer: m.Issuer, Audience: m.Issuer, Subject: state.PluginPrincipal, TenantID: tid, ProjectID: a.Project, SessionID: a.Session, Actor: &tokens.Actor{Subject: owner(a)}, AuthEpoch: state.Epoch, Generation: state.Generation, IssuedAt: now, ExpiresAt: now + 900, Capabilities: []string{"intake.read", "intake.write", "aithema.journal.read", "aithema.journal.write", "aithema.authority.read", "aithema.ledger"}}
	dt, err := m.Keys.MintDelegated(ctx, c)
	if err != nil {
		return sessionTokens{}, fail(503, "unavailable")
	}
	pair.DelegatedToken = dt
	return pair, nil
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("projectId")
	p, err := m.person(r, "intake.write", project)
	if err != nil {
		writeError(w, err)
		return
	}
	if !uuidRE.MatchString(project) || !m.sameOrigin(r) {
		writeError(w, fail(403, "forbidden"))
		return
	}
	var in struct {
		Authorization json.RawMessage `json:"authorization"`
		Mode          string          `json:"host_mode"`
	}
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	var auth map[string]any
	d := json.NewDecoder(bytes.NewReader(in.Authorization))
	d.UseNumber()
	if d.Decode(&auth) != nil || auth == nil {
		writeError(w, fail(400, "invalid_request"))
		return
	}
	sid := newID()
	auth["tid"] = p.TenantID
	auth["pid"] = project
	auth["sid"] = sid
	auth["epoch"] = 1
	auth["withdrawn_at"] = nil
	// The pinned contract permits at most six fractional digits, including
	// when the host clock has nanosecond precision.
	auth["created_at"] = m.clock().UTC().Format("2006-01-02T15:04:05.000000Z")
	var out sessionTokens
	err = db.InTransaction(r.Context(), m.Pool, func(ctx context.Context) error {
		var s Settings
		if err := db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			var credential []byte
			var err error
			s, credential, err = loadSettings(ctx, tx, p.TenantID, true)
			if err != nil {
				return err
			}
			if len(credential) == 0 {
				return fail(503, "unconfigured")
			}
			var offboarded bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aithema_deprovisioned_subjects WHERE tenant_id=$1 AND issuer=$2 AND subject=$3)`, p.TenantID, m.Issuer, p.ID).Scan(&offboarded); err != nil {
				return err
			}
			if offboarded {
				return fail(409, "revoked")
			}
			canonical, _ := json.Marshal(s)
			canonical, err = tokens.CanonicalJSON(canonical)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(canonical)
			auth["settings_sha256"] = hex.EncodeToString(sum[:])
			return nil
		}); err != nil {
			return err
		}
		raw, _ := json.Marshal(auth)
		var a authorization
		if json.Unmarshal(raw, &a) != nil || owner(a) != p.ID {
			return fail(403, "forbidden")
		}
		if err := m.Journal.CreateSession(ctx, journal.SessionConfig{TenantID: p.TenantID, Authorization: raw, PluginPrincipal: s.PluginPrincipal, Generation: 1, HostMode: in.Mode, Currency: s.Currency, Evidence: s.EvidenceVerified, Caps: journal.Caps{Session: s.SessionCap, PrincipalDay: s.PrincipalDayCap, TenantDay: s.TenantDayCap}}); err != nil {
			return err
		}
		return db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			state, err := m.Journal.LockAuthority(ctx, tx, p.TenantID, sid)
			if err != nil {
				return err
			}
			if _, _, err := m.live(ctx, tx, p.TenantID, state, "intake.write"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO aithema_host_sessions(tenant_id,sid,issuer,requester,event_cursor) VALUES($1,$2,$3,$4,(SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1))`, p.TenantID, sid, m.Issuer, p.ID); err != nil {
				return err
			}
			callback, err := enqueue(ctx, tx, p.TenantID, sid, "create", "create:"+sid, map[string]any{"sid": sid, "authorization": json.RawMessage(raw), "host_mode": in.Mode})
			if err != nil {
				return err
			}
			_, err = events.Append(ctx, tx, p, events.Change{Type: "aithema.session_created", NodeID: &project, After: map[string]any{"sid": sid, "callback_id": callback}})
			out = sessionTokens{Session: sid, Generation: 1, Epoch: 1, Callback: callback}
			return err
		})
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 202, out)
}

func (m *Module) personState(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, sid string, active bool) (journal.AuthorityState, error) {
	state, err := m.Journal.LockAuthority(ctx, tx, p.TenantID, sid)
	if err != nil {
		return state, err
	}
	var a authorization
	if json.Unmarshal(state.Authorization, &a) != nil || a.Project != project || owner(a) != p.ID {
		return state, fail(403, "forbidden")
	}
	if active {
		if _, _, err := m.live(ctx, tx, p.TenantID, state, "intake.write"); err != nil {
			return state, err
		}
		if state.Tombstone || a.Withdrawn != nil {
			return state, fail(409, "revoked")
		}
		if state.Suspended {
			return state, fail(409, "suspended")
		}
	}
	return state, nil
}
func (m *Module) refresh(w http.ResponseWriter, r *http.Request) {
	project, sid := r.PathValue("projectId"), r.PathValue("sid")
	p, err := m.person(r, "intake.write", project)
	if err != nil {
		writeError(w, err)
		return
	}
	if !m.sameOrigin(r) {
		writeError(w, fail(403, "forbidden"))
		return
	}
	var out sessionTokens
	err = db.InTransaction(r.Context(), m.Pool, func(ctx context.Context) error {
		var state journal.AuthorityState
		if err := db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			state, err = m.personState(ctx, tx, p, project, sid, true)
			if err != nil {
				return err
			}
			var delivered bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aithema_callbacks WHERE tenant_id=$1 AND sid=$2 AND operation='create' AND state='delivered')`, p.TenantID, sid).Scan(&delivered); err != nil {
				return err
			}
			if !delivered {
				return fail(409, "creation_pending")
			}
			return nil
		}); err != nil {
			return err
		}
		var err error
		out, err = m.sessionToken(ctx, p.TenantID, state)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, out)
}

// enqueue records only value-free host payloads or the public authorization
// record. Service and session credentials are supplied at delivery, never stored.
func enqueue(ctx context.Context, tx pgx.Tx, tid, sid, operation, key string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := newID()
	var prior string
	err = tx.QueryRow(ctx, `INSERT INTO aithema_callbacks(tenant_id,id,idempotency_key,sid,operation,payload) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key WHERE aithema_callbacks.operation=EXCLUDED.operation AND aithema_callbacks.sid IS NOT DISTINCT FROM EXCLUDED.sid AND aithema_callbacks.payload=EXCLUDED.payload RETURNING id::text`, tid, id, key, nullable(sid), operation, raw).Scan(&prior)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(409, "idempotency_conflict")
	}
	return prior, err
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func priorCallback(ctx context.Context, tx pgx.Tx, tid, key, sid, operation string) (string, error) {
	var id, oldSID, oldOperation string
	err := tx.QueryRow(ctx, `SELECT id::text,coalesce(sid::text,''),operation FROM aithema_callbacks WHERE tenant_id=$1 AND idempotency_key=$2 FOR UPDATE`, tid, key).Scan(&id, &oldSID, &oldOperation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if oldSID != sid || oldOperation != operation {
		return "", fail(409, "idempotency_conflict")
	}
	return id, nil
}

func (m *Module) control(w http.ResponseWriter, r *http.Request) {
	project, sid := r.PathValue("projectId"), r.PathValue("sid")
	p, err := m.person(r, "intake.write", project)
	if err != nil {
		writeError(w, err)
		return
	}
	if !m.sameOrigin(r) {
		writeError(w, fail(403, "forbidden"))
		return
	}
	var in struct {
		Action string `json:"action"`
		Key    string `json:"idempotency_key"`
	}
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !uuidRE.MatchString(in.Key) || !contains([]string{"suspend", "resume", "purge"}, in.Action) {
		writeError(w, fail(400, "invalid_request"))
		return
	}
	callback := ""
	err = db.InTransaction(r.Context(), m.Pool, func(ctx context.Context) error {
		var state journal.AuthorityState
		if err := db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			state, err = m.personState(ctx, tx, p, project, sid, false)
			if err != nil {
				return err
			}
			callback, err = priorCallback(ctx, tx, p.TenantID, in.Key, sid, in.Action)
			if err != nil || callback != "" {
				return err
			}
			if in.Action != "purge" {
				var delivered bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aithema_callbacks WHERE tenant_id=$1 AND sid=$2 AND operation='create' AND state='delivered')`, p.TenantID, sid).Scan(&delivered); err != nil {
					return err
				}
				if !delivered {
					return fail(409, "creation_pending")
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if callback != "" {
			return nil
		}
		if in.Action == "resume" {
			if err := db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error { _, _, err := m.live(ctx, tx, p.TenantID, state, "intake.write"); return err }); err != nil {
				return err
			}
		}
		if err := m.Journal.Revoke(ctx, p.TenantID, project, sid, in.Action); err != nil {
			return err
		}
		if in.Action == "resume" {
			if _, err := m.Journal.Takeover(ctx, p.TenantID, project, sid, state.Generation); err != nil {
				return err
			}
		}
		return db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			callback, err = enqueue(ctx, tx, p.TenantID, sid, in.Action, in.Key, map[string]any{"sid": sid, "action": in.Action})
			if err != nil {
				return err
			}
			_, err = events.Append(ctx, tx, p, events.Change{Type: "aithema.session_control", NodeID: &project, After: map[string]any{"sid": sid, "action": in.Action, "callback_id": callback}})
			return err
		})
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 202, map[string]string{"callback_id": callback})
}

func (m *Module) hostEvent(w http.ResponseWriter, r *http.Request) {
	project, sid := r.PathValue("projectId"), r.PathValue("sid")
	p, err := m.person(r, "intake.write", project)
	if err != nil {
		writeError(w, err)
		return
	}
	if !m.sameOrigin(r) {
		writeError(w, fail(403, "forbidden"))
		return
	}
	var in struct {
		Event int64  `json:"event_id"`
		Key   string `json:"idempotency_key"`
	}
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !uuidRE.MatchString(in.Key) || in.Event < 1 || in.Event > tokens.MaxSafeInteger {
		writeError(w, fail(400, "invalid_request"))
		return
	}
	callback := ""
	err = db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := m.personState(r.Context(), tx, p, project, sid, true); err != nil {
			return err
		}
		// Project RLS and the person context prevent foreign event disclosure.
		var eventType string
		if err := tx.QueryRow(r.Context(), `SELECT e.type FROM events e LEFT JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id WHERE e.tenant_id=$1 AND e.id=$2 AND (e.node_id=$3::uuid OR n.project_id=$3::uuid OR e.after->>'project_node_id'=$3::text)`, p.TenantID, in.Event, project).Scan(&eventType); err != nil {
			return fail(404, "not_found")
		}
		var err error
		callback, err = enqueue(r.Context(), tx, p.TenantID, sid, "host-event", in.Key, map[string]any{"sid": sid, "event_id": in.Event, "type": eventType})
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 202, map[string]string{"callback_id": callback})
}

func (m *Module) deprovision(w http.ResponseWriter, r *http.Request) {
	p, err := m.person(r, "plugins.manage", "")
	if err != nil {
		writeError(w, err)
		return
	}
	if !m.sameOrigin(r) {
		writeError(w, fail(403, "forbidden"))
		return
	}
	var in struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"sub"`
		Key     string `json:"idempotency_key"`
	}
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.Issuer != m.Issuer || !uuidRE.MatchString(in.Subject) || !uuidRE.MatchString(in.Key) {
		writeError(w, fail(400, "invalid_request"))
		return
	}
	callback := ""
	err = db.InTransaction(r.Context(), m.Pool, func(ctx context.Context) error {
		type target struct{ SID, Project string }
		var targets []target
		if err := db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			// Tenant settings lock serializes session creation and deprovision.
			if _, _, err := loadSettings(ctx, tx, p.TenantID, true); err != nil {
				return err
			}
			var oldID string
			var same bool
			payload, _ := json.Marshal(map[string]any{"issuer": in.Issuer, "tid": p.TenantID, "sub": in.Subject})
			err := tx.QueryRow(ctx, `SELECT id::text,operation='deprovision' AND payload=$3::jsonb FROM aithema_callbacks WHERE tenant_id=$1 AND idempotency_key=$2`, p.TenantID, in.Key, payload).Scan(&oldID, &same)
			if err == nil {
				if !same {
					return fail(409, "idempotency_conflict")
				}
				callback = oldID
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO aithema_deprovisioned_subjects(tenant_id,issuer,subject) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.TenantID, in.Issuer, in.Subject); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT h.sid::text,s.project_id FROM aithema_host_sessions h JOIN aithema_sessions s USING(tenant_id,sid) WHERE h.tenant_id=$1 AND h.issuer=$2 AND h.requester=$3 ORDER BY h.sid`, p.TenantID, in.Issuer, in.Subject)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t target
				if err := rows.Scan(&t.SID, &t.Project); err != nil {
					return err
				}
				targets = append(targets, t)
			}
			return rows.Err()
		}); err != nil {
			return err
		}
		if callback != "" {
			return nil
		}
		for _, t := range targets {
			if err := m.Journal.Revoke(ctx, p.TenantID, t.Project, t.SID, "purge"); err != nil {
				return err
			}
		}
		return db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			callback, err = enqueue(ctx, tx, p.TenantID, "", "deprovision", in.Key, map[string]any{"issuer": in.Issuer, "tid": p.TenantID, "sub": in.Subject})
			if err != nil {
				return err
			}
			_, err = events.Append(ctx, tx, p, events.Change{Type: "aithema.deprovisioned", After: map[string]any{"issuer": in.Issuer, "sub": in.Subject, "callback_id": callback}})
			return err
		})
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 202, map[string]string{"callback_id": callback})
}
