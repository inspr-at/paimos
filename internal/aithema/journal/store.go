// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fault is a value-free transport error. Underlying storage diagnostics never
// enter HTTP replies, where they could reveal credentials or foreign scope.
type Fault struct {
	Status   int
	Code     string
	Document json.RawMessage
}

func (e *Fault) Error() string             { return e.Code }
func fault(status int, code string) *Fault { return &Fault{Status: status, Code: code} }
func publicError(err error) *Fault {
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	return fault(503, "unavailable")
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random UUID unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func uuid(s string) bool {
	return regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(s)
}

// Caps are trusted host policy in currency micro-units. Daily scopes are UTC
// admission days; a reservation retains that day through settle and recovery.
type Caps struct{ Session, PrincipalDay, TenantDay int64 }
type SessionConfig struct {
	TenantID        string
	Authorization   json.RawMessage
	PluginPrincipal string
	Generation      int64
	HostMode        string
	Currency        string
	Evidence        bool
	Caps            Caps
	// Only host-qualified loopback lanes may reserve zero. A caller-supplied
	// lane_kind is never enough to establish local execution.
	LocalLanes []string
}
type Store struct {
	Pool      *pgxpool.Pool
	validator *Validator
	clock     func() time.Time
}

func NewStore(pool *pgxpool.Pool) (*Store, error) {
	v, err := NewValidator()
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool, validator: v, clock: time.Now}, nil
}
func (s *Store) now() time.Time { return s.clock().UTC().Truncate(time.Microsecond) }

type session struct {
	Tenant, ID, Project, Plugin, HostMode, Currency string
	Authorization                                   []byte
	Generation, Epoch, Seq, WorkingRev, ConsumedSeq int64
	SnapshotSeq                                     *int64
	Tombstone, Suspended, Evidence                  bool
	SessionCap                                      int64
	LocalLanes                                      []string
}

func load(ctx context.Context, tx pgx.Tx, tenant, sid string) (*session, error) {
	st := &session{Tenant: tenant, ID: sid}
	err := tx.QueryRow(ctx, `SELECT project_id,plugin_principal,authorization_bytes,worker_generation,auth_epoch,tombstone,suspended,host_mode,currency,evidence,session_cap,seq,working_rev,consumed_seq,snapshot_seq,local_lanes FROM aithema_sessions WHERE tenant_id=$1 AND sid=$2 FOR UPDATE`, tenant, sid).Scan(&st.Project, &st.Plugin, &st.Authorization, &st.Generation, &st.Epoch, &st.Tombstone, &st.Suspended, &st.HostMode, &st.Currency, &st.Evidence, &st.SessionCap, &st.Seq, &st.WorkingRev, &st.ConsumedSeq, &st.SnapshotSeq, &st.LocalLanes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fault(404, "not_found")
	}
	return st, err
}
func (s *Store) transaction(ctx context.Context, tenant, sid string, ledger bool, fn func(pgx.Tx, *session) error) error {
	if s == nil || s.Pool == nil {
		return fault(503, "unavailable")
	}
	if !uuid(tenant) || !uuid(sid) {
		return fault(400, "invalid_request")
	}
	return db.InTenant(ctx, s.Pool, tenant, func(tx pgx.Tx) error {
		// Always acquire the shared currency-policy lock before the session lock.
		// This serializes aggregate admission across sessions and replicas, while
		// journal-only writes and trusted lifecycle operations use the session lock.
		if ledger {
			var currency string
			err := tx.QueryRow(ctx, `SELECT p.currency FROM aithema_budget_policy p WHERE p.tenant_id=$1 AND p.currency=(SELECT currency FROM aithema_sessions WHERE tenant_id=$1 AND sid=$2) FOR UPDATE`, tenant, sid).Scan(&currency)
			if errors.Is(err, pgx.ErrNoRows) {
				return fault(404, "not_found")
			}
			if err != nil {
				return err
			}
		}
		st, err := load(ctx, tx, tenant, sid)
		if err != nil {
			return err
		}
		return fn(tx, st)
	})
}
func authorize(st *session, c tokens.Claims, capability string) error {
	if c.TenantID != st.Tenant || c.SessionID != st.ID || c.Subject != st.Plugin || c.Actor == nil {
		return fault(403, "forbidden")
	}
	permitted := false
	for _, cap := range c.Capabilities {
		if cap == capability {
			permitted = true
		}
	}
	auth, err := decode(st.Authorization)
	if err != nil {
		return fault(503, "unavailable")
	}
	actor := false
	for _, p := range array(auth["participants"]) {
		if object(p)["participant_ref"] == c.Actor.Subject {
			actor = true
		}
	}
	if !permitted || !actor || capability != "aithema.authority.read" && c.ProjectID != st.Project {
		return fault(403, "forbidden")
	}
	return nil
}

// fence governs new effects and recovery. Settlement instead checks the
// committed claim's generation/epoch after the ordinary scope checks above.
func fence(st *session, c tokens.Claims, write bool) error {
	auth, err := decode(st.Authorization)
	if err != nil {
		return fault(503, "unavailable")
	}
	if st.Tombstone || auth["withdrawn_at"] != nil || c.AuthEpoch != st.Epoch {
		return fault(409, "revoked")
	}
	if write && c.Generation != st.Generation {
		return fault(409, "fenced_generation")
	}
	return nil
}

// CreateSession is for the host lifecycle (P04), after project, plugin and
// person authorization. It is deliberately unavailable to delegated HTTP.
func (s *Store) CreateSession(ctx context.Context, c SessionConfig) error {
	auth, err := s.validator.Validate(c.Authorization, "aithema.authz")
	if err != nil {
		return err
	}
	if !uuid(c.TenantID) || auth["tid"] != c.TenantID || c.Generation < 1 || c.Generation > tokens.MaxSafeInteger || !in(c.HostMode, "review", "working_spec_only") || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(c.Currency) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(c.PluginPrincipal) {
		return fault(400, "invalid_request")
	}
	for _, n := range []int64{c.Caps.Session, c.Caps.PrincipalDay, c.Caps.TenantDay} {
		if n < 0 || n > tokens.MaxSafeInteger {
			return fault(400, "invalid_request")
		}
	}
	for _, lane := range c.LocalLanes {
		if !in(lane, "reaction", "spec", "design", "stt", "tts") {
			return fault(400, "invalid_request")
		}
	}
	if c.LocalLanes == nil {
		c.LocalLanes = []string{}
	}
	return db.InTenant(ctx, s.Pool, c.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_budget_policy(tenant_id,currency,principal_day_cap,tenant_day_cap) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.TenantID, c.Currency, c.Caps.PrincipalDay, c.Caps.TenantDay); err != nil {
			return err
		}
		var principal, tenant int64
		if err := tx.QueryRow(ctx, `SELECT principal_day_cap,tenant_day_cap FROM aithema_budget_policy WHERE tenant_id=$1 AND currency=$2 FOR UPDATE`, c.TenantID, c.Currency).Scan(&principal, &tenant); err != nil {
			return err
		}
		if principal != c.Caps.PrincipalDay || tenant != c.Caps.TenantDay {
			return fault(409, "policy_conflict")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO aithema_sessions(tenant_id,sid,project_id,plugin_principal,authorization_bytes,worker_generation,auth_epoch,tombstone,host_mode,currency,evidence,session_cap,local_lanes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, c.TenantID, text(auth["sid"]), text(auth["pid"]), c.PluginPrincipal, []byte(c.Authorization), c.Generation, number(auth["epoch"]), auth["withdrawn_at"] != nil, c.HostMode, c.Currency, c.Evidence, c.Caps.Session, c.LocalLanes)
		if err == nil && tag.RowsAffected() != 1 {
			return fault(409, "session_exists")
		}
		return err
	})
}

// AuthorityState is the shared P04 enforcement seam; it contains no signing
// material and never changes generation as a side effect of a read.
type AuthorityState struct {
	Generation      int64           `json:"worker_generation"`
	Epoch           int64           `json:"auth_epoch"`
	Tombstone       bool            `json:"tombstone"`
	Suspended       bool            `json:"suspended"`
	Authorization   json.RawMessage `json:"authorization"`
	IssuedAt        string          `json:"issued_at"`
	PluginPrincipal string          `json:"-"`
}
type GenerationStore interface {
	Current(context.Context, string, string, string) (AuthorityState, error)
	LockAuthority(context.Context, pgx.Tx, string, string) (AuthorityState, error)
}

// LockAuthority is the P04 adapter seam for P02's caller-owned transaction.
// The caller sets tenant RLS before calling and retains the session row lock
// through its intake effect. Current is an observation, not a write fence.
func (s *Store) LockAuthority(ctx context.Context, tx pgx.Tx, tenant, sid string) (AuthorityState, error) {
	if !uuid(tenant) || !uuid(sid) {
		return AuthorityState{}, fault(400, "invalid_request")
	}
	st, err := load(ctx, tx, tenant, sid)
	if err != nil {
		return AuthorityState{}, err
	}
	return state(st, s.now()), nil
}

func (s *Store) Current(ctx context.Context, tenant, project, sid string) (AuthorityState, error) {
	var out AuthorityState
	err := s.transaction(ctx, tenant, sid, false, func(_ pgx.Tx, st *session) error {
		if st.Project != project {
			return fault(404, "not_found")
		}
		out = state(st, s.now())
		return nil
	})
	return out, err
}
func state(st *session, now time.Time) AuthorityState {
	return AuthorityState{Generation: st.Generation, Epoch: st.Epoch, Tombstone: st.Tombstone, Suspended: st.Suspended, Authorization: bytes.Clone(st.Authorization), IssuedAt: now.Format("2006-01-02T15:04:05.000000Z"), PluginPrincipal: st.Plugin}
}

// Takeover is explicit resume. CAS prevents two resumptions from owning the
// same generation. Suspend refuses takeover without consuming a generation;
// a tombstone or withdrawn authorization is terminal.
func (s *Store) Takeover(ctx context.Context, tenant, project, sid string, expected int64) (AuthorityState, error) {
	var out AuthorityState
	err := s.transaction(ctx, tenant, sid, false, func(tx pgx.Tx, st *session) error {
		if st.Project != project {
			return fault(404, "not_found")
		}
		auth, err := decode(st.Authorization)
		if err != nil {
			return err
		}
		if st.Tombstone || auth["withdrawn_at"] != nil {
			return fault(409, "revoked")
		}
		if st.Generation != expected {
			return fault(409, "fenced_generation")
		}
		if st.Suspended {
			return fault(409, "suspended")
		}
		if st.Generation == tokens.MaxSafeInteger {
			return fault(409, "generation_exhausted")
		}
		st.Generation++
		if _, err := tx.Exec(ctx, `UPDATE aithema_sessions SET worker_generation=$3 WHERE tenant_id=$1 AND sid=$2`, tenant, sid, st.Generation); err != nil {
			return err
		}
		out = state(st, s.now())
		return nil
	})
	return out, err
}

// Revoke applies host session controls, journaling before projection in one
// transaction. Suspend/resume pause new work without withdrawing authority;
// only purge advances the withdrawal epoch and creates a durable tombstone.
func (s *Store) Revoke(ctx context.Context, tenant, project, sid, action string) error {
	if !in(action, "suspend", "resume", "purge") {
		return fault(400, "invalid_request")
	}
	return s.transaction(ctx, tenant, sid, false, func(tx pgx.Tx, st *session) error {
		if st.Project != project {
			return fault(404, "not_found")
		}
		if st.Tombstone {
			if action == "purge" {
				return nil
			}
			return fault(409, "revoked")
		}
		if action != "purge" {
			suspended := action == "suspend"
			if st.Suspended == suspended {
				return nil
			}
			raw := marshal(envelope("aithema.journal.record", map[string]any{"sid": sid, "client_event_id": newID(), "writer": map[string]any{"kind": "host"}, "recorded_at": s.now().Format("2006-01-02T15:04:05.000000Z"), "kind": "session.control", "data": map[string]any{"action": action}}))
			doc, err := s.validator.Validate(raw, "aithema.journal.record")
			if err != nil {
				return err
			}
			if _, err := appendRecord(ctx, tx, st, raw, doc); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE aithema_sessions SET suspended=$3 WHERE tenant_id=$1 AND sid=$2`, tenant, sid, suspended)
			return err
		}
		if st.Epoch == tokens.MaxSafeInteger {
			return fault(409, "epoch_exhausted")
		}
		epoch := st.Epoch + 1
		for _, r := range []struct {
			kind string
			data map[string]any
		}{{"authz.epoch", map[string]any{"epoch": epoch, "reason": "withdrawal"}}, {"session.control", map[string]any{"action": action}}} {
			raw := marshal(envelope("aithema.journal.record", map[string]any{"sid": sid, "client_event_id": newID(), "writer": map[string]any{"kind": "host"}, "recorded_at": s.now().Format("2006-01-02T15:04:05.000000Z"), "kind": r.kind, "data": r.data}))
			doc, err := s.validator.Validate(raw, "aithema.journal.record")
			if err != nil {
				return err
			}
			if _, err := appendRecord(ctx, tx, st, raw, doc); err != nil {
				return err
			}
		}
		auth, err := decode(st.Authorization)
		if err != nil {
			return err
		}
		auth["epoch"] = epoch
		auth["withdrawn_at"] = s.now().Format("2006-01-02T15:04:05.000000Z")
		_, err = tx.Exec(ctx, `UPDATE aithema_sessions SET auth_epoch=$3,tombstone=true,authorization_bytes=$4 WHERE tenant_id=$1 AND sid=$2`, tenant, sid, epoch, marshal(auth))
		return err
	})
}
