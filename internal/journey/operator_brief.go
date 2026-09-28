// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/intake"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/tenant"
)

type briefContextKey struct{}

type disposableBrief struct{ title, body string }

// These strings are the fixed AEON disposable-intake-briefs knowledge entries.
var disposableBriefs = map[string]disposableBrief{
	"1": {"Host status page", "A read-only status page for a small fleet of hosts: current state per host, last deploy, open incidents. Success: one page, loads under a second, no write actions."},
	"2": {"Release notes digest", "A weekly digest of released tickets per project: groups by feature and fix, links each ticket, sent nowhere (rendered page only). Success: the digest for last week matches the release history."},
	"3": {"Maintenance window planner", "Plan maintenance windows for hosts: propose a window, check it against the release calendar, record the decision. Success: a window can be proposed, checked and recorded; conflicts are shown."},
}

// checkBriefSeed runs before creating the operator principal or any intake row.
func checkBriefSeed(ctx context.Context, tx pgx.Tx, projectID, brief string) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases WHERE project_node_id=$1::uuid AND state IN ('released','superseded','deploying','access'))`, projectID).Scan(&live); err != nil {
		return err
	}
	if live {
		return errors.New("cannot seed a deployed or released project")
	}
	var accepted, matching int
	err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE d.idempotency_key=$2 AND d.title=$3 AND d.body=$4) FROM intake_drafts d
		JOIN intake_draft_acceptances a ON a.tenant_id=d.tenant_id AND a.draft_id=d.id
		WHERE d.project_node_id=$1::uuid AND d.kind='brief'`, projectID, "operator-brief:"+brief, disposableBriefs[brief].title, disposableBriefs[brief].body).Scan(&accepted, &matching)
	if err != nil {
		return err
	}
	if accepted > 0 {
		if accepted != 1 || matching != 1 {
			return errors.New("intake already uses a different brief; disposable seed refuses to replace it")
		}
		return nil
	}
	var confirmed bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT brief_confirmed_at IS NOT NULL FROM journey_projects WHERE project_node_id=$1::uuid),false)`, projectID).Scan(&confirmed); err != nil {
		return err
	}
	if confirmed {
		return errors.New("intake is already confirmed without this disposable brief")
	}
	return nil
}

func prepareBriefSeed(ctx context.Context, pool *pgxpool.Pool, m *Module, p tenant.Principal, projectID, brief, target string) (bool, error) {
	selected := disposableBriefs[brief]
	key := "operator-brief:" + brief
	var present bool
	err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		if err := ensureJourney(ctx, tx, p, projectID); err != nil {
			return err
		}
		if err := lockJourney(ctx, tx, projectID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM intake_drafts d JOIN intake_draft_acceptances a ON a.tenant_id=d.tenant_id AND a.draft_id=d.id WHERE d.project_node_id=$1::uuid AND d.idempotency_key=$2)`, projectID, key).Scan(&present); err != nil {
			return err
		}
		if present {
			return nil
		}
		return intake.ApplyDisposableIntake(ctx, tx, p, projectID, intake.DisposableBrief{
			Title: selected.title, Body: selected.body, IdempotencyKey: key,
		})
	})
	if err != nil || present {
		return false, err
	}
	view, err := m.read(ctx, p, projectID)
	if err != nil {
		return false, err
	}
	if view.NextAction.Key != "confirm_brief" {
		return false, fmt.Errorf("disposable brief cannot be confirmed: %s", view.NextAction.Key)
	}
	if _, err := m.actWithMode(ctx, p, projectID, actionWrite{Action: "confirm_brief", ExpectedRevision: view.Revision, IdempotencyKey: key + ":confirm"}, true); err != nil {
		return false, fmt.Errorf("confirm disposable brief: %w", err)
	}
	view, err = m.read(ctx, p, projectID)
	if err != nil {
		return false, err
	}
	if err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		return requirements.AgreeDisposable(ctx, tx, p, projectID, view.Revision, key+":agreement")
	}); err != nil {
		return false, fmt.Errorf("agree disposable specification: %w", err)
	}
	view, err = m.read(ctx, p, projectID)
	if err != nil {
		return false, err
	}
	switch view.NextAction.Key {
	case "open_first_release":
		view, err = m.actWithMode(ctx, p, projectID, actionWrite{Action: "open_first_release", ExpectedRevision: view.Revision, IdempotencyKey: key + ":release"}, true)
		if err != nil {
			return false, err
		}
	case "mark_candidate":
		// An existing building release can already be past Plan.
	case "approve_candidate":
		if target != "candidate" && target != "deploy" {
			return false, fmt.Errorf("disposable specification reached approve_candidate, which is not a pending gate for --to-stage %s", target)
		}
	case "approve_deploy":
		if target != "deploy" {
			return false, fmt.Errorf("disposable specification reached approve_deploy, which is not a pending gate for --to-stage %s", target)
		}
	default:
		return false, fmt.Errorf("disposable specification cannot continue to %s: next action %s (expected open_first_release, mark_candidate, or a pending human gate)", target, view.NextAction.Key)
	}
	if view.CurrentReleaseID == nil {
		return false, fmt.Errorf("disposable specification reached %s without a release", view.NextAction.Key)
	}
	if err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		return requirements.MaterializeDisposableWork(ctx, tx, p, projectID, *view.CurrentReleaseID, key+":requirement")
	}); err != nil {
		return false, fmt.Errorf("seed disposable release ticket: %w", err)
	}
	return true, nil
}
