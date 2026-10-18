// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func (s *Store) Read(ctx context.Context, actor nativecontract.Actor, binding nativecontract.Binding) (nativecontract.SessionView, error) {
	if !workorders.UUID(binding.OwnedSessionID) || !workorders.UUID(binding.ProjectID) {
		return nativecontract.SessionView{}, fail(400, nativecontract.ReasonInvalidInput)
	}
	return s.read(ctx, actor, binding.ProjectID, binding.OwnedSessionID, "", &binding)
}

// View reads an accepted logical session before it has a runtime binding.
func (s *Store) View(ctx context.Context, actor nativecontract.Actor, project, session string) (nativecontract.SessionView, error) {
	if !workorders.UUID(project) || !workorders.UUID(session) {
		return nativecontract.SessionView{}, fail(400, nativecontract.ReasonInvalidInput)
	}
	return s.read(ctx, actor, project, session, "", nil)
}

// ByHarness is the additive native-context lookup; HarnessSession wire shape
// and legacy session ownership semantics remain unchanged.
func (s *Store) ByHarness(ctx context.Context, actor nativecontract.Actor, project, harness string) (nativecontract.SessionView, error) {
	if !workorders.UUID(project) || !workorders.UUID(harness) {
		return nativecontract.SessionView{}, fail(400, nativecontract.ReasonInvalidInput)
	}
	return s.read(ctx, actor, project, "", harness, nil)
}

func (s *Store) read(ctx context.Context, actor nativecontract.Actor, project, session, harness string, expected *nativecontract.Binding) (nativecontract.SessionView, error) {
	var out nativecontract.SessionView
	p, err := authenticated(ctx, actor)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: project}); err != nil {
			return fail(404, nativecontract.ReasonInvalidInput)
		}
		var raw []byte
		var viewer *string
		err := tx.QueryRow(ctx, `SELECT b.view,CASE WHEN $4='person' THEN b.terminal_viewer_url ELSE NULL END FROM native_session_bindings b
 JOIN nodes n ON n.tenant_id=b.tenant_id AND n.id=b.project_id
 WHERE b.project_id=$1 AND n.deleted_at IS NULL AND (b.id::text=$2 OR b.current_harness_session_id::text=$3)`, project, session, harness, string(p.Kind)).Scan(&raw, &viewer)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, nativecontract.ReasonInvalidInput)
		}
		if err != nil {
			return err
		}
		if len(raw) > 131072 {
			return fail(413, nativecontract.ReasonBufferFull)
		}
		if err = json.Unmarshal(raw, &out); err != nil {
			return err
		}
		if expected != nil && !reflect.DeepEqual(out.CurrentBinding, expected) {
			return fail(409, nativecontract.ReasonWrongGeneration)
		}
		// Agent reads never even fetch the stored URL from SQL. Strip again at
		// projection, including owner-workstation agent principals.
		if viewer != nil {
			if err = s.viewerHosts.Validate(*viewer); err != nil {
				return err
			}
		}
		out.TerminalViewerURL = viewer
		out = nativecontract.PersonProjection(out, string(p.Kind), true)
		return s.validate("NativeSessionView", out)
	})
	if err != nil {
		return nativecontract.SessionView{}, err
	}
	return out, nil
}
