// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const managedControlCapability = "managed_control_v1"

// Both timestamps belong to the database. Never interpret expires_at with the
// API host or daemon wall clock. Cap the budget at the authorization window.
func controlTTL(expires *time.Time, now time.Time) time.Duration {
	if expires == nil {
		return 0
	}
	return max(0, min(expires.Sub(now), ownershipWindow))
}

// Text never reaches SQL or events. Missing text after restart is a rejection,
// not an invitation to reconstruct or replay input. The relay is bounded even
// when no worker polls. Tenant and session are part of every lookup.
type controlRelay struct {
	mu    sync.Mutex
	items map[string]controlText
}
type controlText struct {
	text    string
	expires time.Time
}

func relayKey(tenantID, sessionID, id string) string { return tenantID + "/" + sessionID + "/" + id }
func (q *controlRelay) put(key, text string, started time.Time, ttl time.Duration) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.items == nil {
		q.items = make(map[string]controlText)
	}
	for k, v := range q.items {
		if !v.expires.After(time.Now()) {
			delete(q.items, k)
		}
	}
	if len(q.items) >= 256 {
		return false
	}
	// started was sampled locally before fetching the DB budget and carries a
	// monotonic reading. DB round-trip time cannot extend retention.
	expires := started.Add(ttl)
	q.items[key] = controlText{text, expires}
	// Remove expired content even when the relay receives no further traffic.
	time.AfterFunc(time.Until(expires), func() { q.take(key) })
	return true
}
func (q *controlRelay) take(key string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	v := q.items[key]
	delete(q.items, key)
	if !v.expires.After(time.Now()) {
		return ""
	}
	return v.text
}

func (m *Module) managedControl(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person may control a managed session")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.control", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return nil, workorders.Fail(403, "harness.control permission required")
		}
		return nil, err
	}
	var in struct {
		RequestID string                `json:"request_id"`
		Kind      string                `json:"kind"`
		Value     string                `json:"value,omitempty"`
		Text      string                `json:"text"`
		Ownership ownedprocess.Identity `json:"expected_ownership"`
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 16384)
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || (in.Kind != "steer" && in.Kind != "interrupt" && in.Kind != "stop" && !settingKind(in.Kind)) || !validSetting(in.Kind, in.Value) || len(in.Text) > 8192 || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) || (in.Kind == "steer" && strings.TrimSpace(in.Text) == "") || (in.Kind != "steer" && in.Text != "") {
		return nil, workorders.Fail(400, "request id, typed operation and bounded text or setting value required")
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if s.Management != "managed" {
		return nil, workorders.Fail(409, "Aeon did not launch this session; unmanaged sessions cannot be controlled")
	}
	raw, _ := json.Marshal(in)
	dg := digest("managed-control:"+p.ID, string(raw))
	prior, err := scanControl(tx.QueryRow(r.Context(), `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1 AND session_id=$2`, in.RequestID, s.ID))
	if err == nil {
		if prior.Kind == in.Kind && subtle.ConstantTimeCompare(prior.requestDigest, dg) == 1 {
			return prior, nil
		}
		return nil, workorders.Fail(409, "request id already used for different control")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// process_observed_at is stamped from the database clock. A container clock
	// ahead of the API host makes that fresh stamp look future-dated to time.Now(),
	// and forceAvailable then refuses a live session (AEON-337).
	now, err := m.ownershipNow(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if s.Harness != "claude" || !forceAvailable(s, now) || !has(s, managedControlCapability) || !has(s, in.Kind) {
		return nil, workorders.Fail(409, "live sandboxed managed control unavailable for this adapter")
	}
	if *s.ProcessOwnership != in.Ownership {
		return nil, workorders.Fail(409, "process generation changed; refresh this session")
	}
	var liveRun bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND agent_principal_id=$2 AND status='running' AND daemon_id=$3 AND daemon_generation=$4)`, *s.RunID, s.AgentPrincipalID, in.Ownership.DaemonID, in.Ownership.Generation).Scan(&liveRun); err != nil {
		return nil, err
	}
	if !liveRun {
		return nil, workorders.Fail(409, "run is not owned by this live daemon generation")
	}
	if err = validateSettingCatalog(r.Context(), tx, s, in.Kind, in.Value); err != nil {
		return nil, err
	}
	if settingKind(in.Kind) {
		if err = m.expireControls(r, tx, p, s); err != nil {
			return nil, err
		}
		var pending bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_controls WHERE session_id=$1 AND kind IN ('rename','model','effort') AND state<>'completed')`, s.ID).Scan(&pending); err != nil {
			return nil, err
		}
		if pending {
			return nil, workorders.Fail(409, "a session setting is still pending")
		}
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND (state<>'completed' OR created_at>clock_timestamp()-interval '1 minute')`, s.ID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 16 {
		return nil, workorders.Fail(429, "session control quota reached")
	}
	identity, _ := json.Marshal(in.Ownership)
	c, err := scanControl(tx.QueryRow(r.Context(), `INSERT INTO harness_controls(tenant_id,id,session_id,kind,sequence,requested_by_principal_id,expected_ownership,request_digest,expires_at,value) SELECT $1,$2,$3,$4,coalesce(max(sequence),0)+1,$5,$6::jsonb,$7,clock_timestamp()+interval '45 seconds',nullif($8,'') FROM harness_controls WHERE session_id=$3 RETURNING `+controlColumns, p.TenantID, in.RequestID, s.ID, in.Kind, p.ID, string(identity), dg, in.Value))
	if err != nil {
		return nil, err
	}
	if err = record(r.Context(), tx, p, s, "control_requested", nil, c); err != nil {
		return nil, err
	}
	if in.Kind == "steer" {
		started := time.Now()
		now, err := m.ownershipNow(r.Context(), tx)
		if err != nil {
			return nil, err
		}
		if !m.controlText.put(relayKey(p.TenantID, s.ID, c.ID), in.Text, started, controlTTL(c.ExpiresAt, now)) {
			return nil, workorders.Fail(429, "transient control queue full")
		}
	}
	return c, nil
}

func (m *Module) expireControls(r *http.Request, tx pgx.Tx, p tenant.Principal, s Session) error {
	rows, err := tx.Query(r.Context(), `UPDATE harness_controls SET state='completed',outcome='rejected',reason=CASE WHEN state='claimed' THEN 'outcome_unconfirmed' ELSE 'authorization_expired' END,claimed_at=coalesce(claimed_at,clock_timestamp()),completed_at=clock_timestamp() WHERE session_id=$1 AND state<>'completed' AND expires_at<=clock_timestamp() RETURNING `+controlColumns, s.ID)
	if err != nil {
		return err
	}
	var expired []Control
	for rows.Next() {
		c, e := scanControl(rows)
		if e != nil {
			rows.Close()
			return e
		}
		expired = append(expired, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range expired {
		m.controlText.take(relayKey(p.TenantID, s.ID, c.ID))
		if err = record(r.Context(), tx, p, s, "control_completed", nil, c); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) managedContext(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct{}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	s, err := worker(r.Context(), tx, r, p)
	if err != nil {
		return nil, err
	}
	if s.Management != "managed" || s.RunID == nil || s.WorkOrderID == nil {
		return nil, workorders.Fail(409, "managed run required")
	}
	h := s.Harness
	if h == "claude" {
		h = "claude-code"
	}
	return rules.ForManagedSession(r.Context(), tx, p, s.ProjectID, *s.WorkOrderID, h)
}

func controlRequesterAuthorized(r *http.Request, tx pgx.Tx, p tenant.Principal, s Session, c Control) (bool, error) {
	if c.ExpectedOwnership == nil || c.Kind == "force_stop" {
		return true, nil
	}
	var id string
	err := tx.QueryRow(r.Context(), `SELECT requested_by_principal_id::text FROM harness_controls WHERE session_id=$1 AND id=$2`, s.ID, c.ID).Scan(&id)
	if err != nil {
		return false, err
	}
	actor := tenant.Principal{TenantID: p.TenantID, ID: id, Kind: tenant.Person}
	err = authz.RequireTx(r.Context(), tx, actor, "harness.control", authz.Scope{ProjectID: s.ProjectID})
	if errors.Is(err, authz.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}
