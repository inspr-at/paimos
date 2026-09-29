// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// AEON-291. The product must not depend on every launcher marking its run
// stopped. An unmanaged generation whose last sign of life (heartbeat, else
// registration) is older than the tenant's heartbeat_lost_minutes (default 15)
// is closed by the server with stop_reason heartbeat_lost, through the same
// closeGeneration path and harness.stopped event as a worker stop. Closing
// also fires the session-message trigger (session_ended).
//
// Managed generations are left to their daemon, which owns the process and
// reports ownership_lost itself. Nothing here signals a process.
//
// No false kills: a later heartbeat with the same worker proof revives the
// generation (harness.revived), unless it was archived or a new generation
// already holds the same session reference.

const (
	StopReasonHeartbeatLost      = "heartbeat_lost"
	defaultHeartbeatLostMinutes  = 15
	minHeartbeatLostMinutes      = 5
	maxHeartbeatLostMinutes      = 1440
	lostContactBatch             = 200
	LostContactSweepInterval     = time.Minute
	lostContactAdvisoryNamespace = 91
)

func heartbeatLostMinutes(ctx context.Context, tx pgx.Tx) (int, error) {
	var mins int
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT heartbeat_lost_minutes FROM harness_settings), $1)`, defaultHeartbeatLostMinutes).Scan(&mins)
	return mins, err
}

func (m *Module) getHeartbeatLost(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	mins, err := heartbeatLostMinutes(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	return map[string]int{"heartbeat_lost_minutes": mins}, nil
}

func (m *Module) putHeartbeatLost(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Minutes int `json:"heartbeat_lost_minutes"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Minutes < minHeartbeatLostMinutes || in.Minutes > maxHeartbeatLostMinutes {
		return nil, workorders.Fail(400, "heartbeat_lost_minutes must be from 5 to 1440")
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO harness_settings(tenant_id,heartbeat_lost_minutes) VALUES($1,$2)
		ON CONFLICT (tenant_id) DO UPDATE SET heartbeat_lost_minutes=EXCLUDED.heartbeat_lost_minutes,updated_at=now()`, p.TenantID, in.Minutes); err != nil {
		return nil, err
	}
	return map[string]int{"heartbeat_lost_minutes": in.Minutes}, nil
}

// lostContact reports a generation the sweeper closed and nobody archived.
func lostContact(s Session) bool {
	return s.ID != "" && s.ArchivedAt == nil && s.StoppedAt != nil && s.StopReason != nil && *s.StopReason == StopReasonHeartbeatLost
}

// revive reopens a lost-contact generation for its own worker. The caller has
// verified the worker proof and holds the row lock. Once any newer generation
// registered the same session reference, whatever its state now, it replaced
// this one for good: the old generation stays closed.
func revive(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, phase string) (Session, error) {
	if s.HandedOverToID != nil {
		return s, workorders.Fail(403, "harness generation handed over")
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE project_id=$1 AND ref_digest=$2 AND id<>$3 AND (created_at,id)>($4,$3::uuid))`, s.ProjectID, s.refDigest, s.ID, s.CreatedAt).Scan(&taken); err != nil {
		return s, err
	}
	if taken {
		return s, workorders.Fail(403, "harness worker proof rejected")
	}
	before := s
	s, err := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET phase=$2,stopped_at=NULL,stop_reason=NULL,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, phase))
	if err != nil {
		return s, err
	}
	return s, record(ctx, tx, p, s, "revived", before, s)
}

// SweepLostContact closes one tenant's silent unmanaged generations and
// returns how many it closed. One runner per tenant at a time: a replica that
// cannot take the transaction advisory lock skips the tenant this round.
// Rows a heartbeat holds are skipped; the heartbeat wins.
func SweepLostContact(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	closed := 0
	ctx = db.AllProjects(ctx, "harness lost-contact sweeper")
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,$2))`, "harness-lost-contact:"+tenantID, lostContactAdvisoryNamespace).Scan(&mine); err != nil || !mine {
			return err
		}
		mins, err := heartbeatLostMinutes(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM harness_sessions
			WHERE management='unmanaged' AND stopped_at IS NULL AND archived_at IS NULL
			AND coalesce(heartbeat_at,created_at)<clock_timestamp()-make_interval(mins=>$1)
			ORDER BY coalesce(heartbeat_at,created_at),id LIMIT $2 FOR UPDATE SKIP LOCKED`, mins, lostContactBatch)
		if err != nil {
			return err
		}
		silent := []Session{}
		for rows.Next() {
			s, e := scanSession(rows)
			if e != nil {
				rows.Close()
				return e
			}
			silent = append(silent, s)
		}
		rows.Close()
		if err = rows.Err(); err != nil || len(silent) == 0 {
			return err
		}
		system := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Name: "System", Roles: []string{"system"}}
		if err = tx.QueryRow(ctx, `SELECT aeon_authz_system_actor($1::uuid)::text`, tenantID).Scan(&system.ID); err != nil {
			return err
		}
		for _, s := range silent {
			if _, err = closeGeneration(ctx, tx, system, s, StopReasonHeartbeatLost); err != nil {
				return err
			}
		}
		closed = len(silent)
		return nil
	})
	return closed, err
}

// RunLostContactSweeper sweeps every tenant once a minute until ctx ends.
// A tenant created later is picked up on the next round.
func RunLostContactSweeper(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(LostContactSweepInterval)
	defer ticker.Stop()
	for {
		sweepAllTenants(ctx, pool)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sweepAllTenants(ctx context.Context, pool *pgxpool.Pool) {
	var tenants []string
	// tenants is the one global table; the read still runs in a tenant-scoped
	// transaction so no query in the sweeper bypasses db.InTenant.
	err := db.InTenant(db.NoProjects(ctx, "harness lost-contact tenant scan"), pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			tenants = append(tenants, id)
		}
		return rows.Err()
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("harness lost-contact tenant scan failed", "error", err)
		}
		return
	}
	for _, id := range tenants {
		if ctx.Err() != nil {
			return
		}
		n, err := SweepLostContact(ctx, pool, id)
		if err != nil && ctx.Err() == nil {
			slog.Error("harness lost-contact sweep failed", "tenant_id", id, "error", err)
		}
		if n > 0 {
			slog.Info("harness sessions lost contact", "tenant_id", id, "closed", n)
		}
	}
}
