// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"
	"database/sql"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
)

type RuntimeHealthPage struct {
	SchemaVersion int                   `json:"schema_version"`
	ObservedAt    string                `json:"observed_at"`
	Runtimes      []RuntimeHealthStatus `json:"runtimes"`
}
type RuntimeHealthStatus struct {
	RuntimeID         string               `json:"runtime_id"`
	RuntimeGeneration string               `json:"runtime_generation"`
	MachineID         string               `json:"machine_id"`
	ExpiresAt         string               `json:"expires_at"`
	Status            string               `json:"status"`
	Layers            []RuntimeLayerHealth `json:"layers"`
}
type RuntimeLayerHealth struct {
	Layer        string `json:"layer"`
	State        string `json:"state"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	FailureCount int    `json:"failure_count"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// RuntimeHealth reads the existing typed producer rows. The latest generation
// per machine remains visible when offline; private owner identities and
// unauthorized records never contribute public counts or pagination signals.
func (s *Service) RuntimeHealth(ctx context.Context, p auth.Principal, project int64) (RuntimeHealthPage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeHealthPage{}, ErrStorage
	}
	defer tx.Rollback()
	_, current, err := auth.ReauthorizePrincipalTx(ctx, tx, p, s.now())
	if err != nil || current.Kind() != auth.PrincipalSession || current.Impersonated() {
		return RuntimeHealthPage{}, ErrUnavailable
	}
	var allowed int
	if tx.QueryRowContext(ctx, auth.AgentModeAuthorizationCTE+`SELECT 1 FROM agent_mode_projects WHERE project_id=?`, current.UserID(), project).Scan(&allowed) != nil {
		return RuntimeHealthPage{}, ErrUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.generation,r.machine_id,r.expires_at,r.user_id,r.api_key_id FROM lifecycle_runtimes r WHERE r.project_id=? AND NOT EXISTS(SELECT 1 FROM lifecycle_runtimes newer WHERE newer.project_id=r.project_id AND newer.machine_id=r.machine_id AND (newer.created_at>r.created_at OR (newer.created_at=r.created_at AND newer.id>r.id))) ORDER BY r.created_at DESC,r.id DESC LIMIT 1024`, project)
	if err != nil {
		return RuntimeHealthPage{}, ErrStorage
	}
	type candidate struct {
		public    RuntimeHealthStatus
		user, key int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.public.RuntimeID, &c.public.RuntimeGeneration, &c.public.MachineID, &c.public.ExpiresAt, &c.user, &c.key) != nil {
			rows.Close()
			return RuntimeHealthPage{}, ErrStorage
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RuntimeHealthPage{}, ErrStorage
	}
	out := RuntimeHealthPage{SchemaVersion: 1, ObservedAt: stamp(s.now()), Runtimes: []RuntimeHealthStatus{}}
	for _, c := range candidates {
		reporter, e := auth.NewAPIKeyPrincipal(c.key, c.user, auth.ParseScopes("*"))
		if e != nil || s.authorize(ctx, tx, reporter, project, auth.PrincipalAPIKey) != nil {
			continue
		}
		r := c.public
		r.Status = "fresh"
		if expired(r.ExpiresAt, s.now()) {
			r.Status = "offline"
		}
		r.Layers = []RuntimeLayerHealth{}
		for _, name := range []string{"reporter", "primary", "fallback", "attention"} {
			layer := RuntimeLayerHealth{Layer: name, State: "unknown", Status: "unknown", Reason: "not_reported"}
			e = tx.QueryRowContext(ctx, `SELECT state,reason,failure_count,updated_at FROM agent_runtime_health WHERE runtime_id=? AND project_id=? AND layer=?`, r.RuntimeID, project, name).Scan(&layer.State, &layer.Reason, &layer.FailureCount, &layer.UpdatedAt)
			if e != nil && e != sql.ErrNoRows {
				return RuntimeHealthPage{}, ErrStorage
			}
			if e == nil {
				reported, parseErr := time.Parse(time.RFC3339Nano, layer.UpdatedAt)
				layer.Status = "fresh"
				if parseErr != nil || reported.After(s.now()) {
					layer = RuntimeLayerHealth{Layer: name, State: "unknown", Status: "unknown", Reason: "not_reported"}
				} else if s.now().Sub(reported) > time.Minute {
					layer.Status = "stale"
				}
			}
			if r.Status == "offline" {
				layer.Status = "offline"
			}
			r.Layers = append(r.Layers, layer)
		}
		out.Runtimes = append(out.Runtimes, r)
		if len(out.Runtimes) == 32 {
			break
		}
	}
	if tx.Commit() != nil {
		return RuntimeHealthPage{}, ErrStorage
	}
	return out, nil
}
