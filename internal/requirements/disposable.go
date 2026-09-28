// SPDX-License-Identifier: AGPL-3.0-only

package requirements

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AgreeDisposable pins an agreed requirements digest with the same revision,
// feature, event and receipt as a person agreement. It does not require or
// record an approval, and it does not generate tickets; the release may not
// exist yet. The caller must be the host operator on a disposable project.
func AgreeDisposable(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, expected int64, key string) error {
	if err := requireDisposableOperator(ctx, tx, p, project); err != nil {
		return err
	}
	if err := refuseLiveRelease(ctx, tx, project); err != nil {
		return err
	}
	rev, err := lockProject(ctx, tx, project, true)
	if err != nil {
		return err
	}
	requestDigest := requestHash("requirements.agree", p, agreeInput{Revision: expected, Key: key})
	repeated, err := replay(ctx, tx, project, key, requestDigest)
	if err != nil {
		return err
	}
	if repeated {
		return nil
	}
	if rev != expected {
		return fail(409, "project revision changed")
	}
	if err = lockCurrentRelease(ctx, tx, project); err != nil {
		return err
	}
	contentDigest, err := Digest(ctx, tx, project)
	if err != nil {
		return err
	}
	var confirmed bool
	if err = tx.QueryRow(ctx, `SELECT brief_confirmed_at IS NOT NULL FROM journey_projects WHERE project_node_id=$1`, project).Scan(&confirmed); err != nil {
		return err
	}
	if !confirmed {
		return fail(409, "brief is not confirmed")
	}
	return commitAgreement(ctx, tx, p, project, rev, contentDigest, key, requestDigest, "", false)
}

// MaterializeDisposableWork generates the accepted requirement's ticket into
// an existing planning or building release and marks that ticket done. It uses
// the same generation mutation as agreement. Replaying it does not add another
// ticket or completion event.
func MaterializeDisposableWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, releaseID, creationKey string) error {
	if err := requireDisposableOperator(ctx, tx, p, project); err != nil {
		return err
	}
	if err := refuseLiveRelease(ctx, tx, project); err != nil {
		return err
	}
	if _, err := lockProject(ctx, tx, project, true); err != nil {
		return err
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE project_node_id=$1 AND release_node_id=$2::uuid FOR UPDATE`, project, releaseID).Scan(&state); err != nil {
		return err
	}
	if state != "planning" && state != "building" {
		return fail(409, "release cannot take disposable work")
	}
	var item Requirement
	err := tx.QueryRow(ctx, `SELECT r.requirement_node_id::text, r.project_node_id::text, r.kind, r.revision, r.status, n.title, f.feature_node_id::text
		FROM journey_requirements r
		JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.requirement_node_id
		LEFT JOIN journey_features f ON f.tenant_id=r.tenant_id AND f.requirement_node_id=r.requirement_node_id
		WHERE r.project_node_id=$1 AND r.creation_key=$2 AND n.deleted_at IS NULL`, project, creationKey).Scan(
		&item.NodeID, &item.ProjectID, &item.Kind, &item.Revision, &item.Status, &item.Title, &item.FeatureID)
	if err != nil {
		return err
	}
	item.TicketIDs = []string{}
	if err = generateWork(ctx, tx, p, project, item, releaseID, true); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT t.ticket_node_id::text FROM journey_tickets t
		JOIN journey_features f ON f.tenant_id=t.tenant_id AND f.feature_node_id=t.feature_node_id
		JOIN journey_requirements r ON r.tenant_id=f.tenant_id AND r.requirement_node_id=f.requirement_node_id
		WHERE r.project_node_id=$1 AND r.creation_key=$2 AND t.source='requirements' AND t.release_node_id=$3::uuid`, project, creationKey, releaseID)
	if err != nil {
		return err
	}
	var tickets []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		tickets = append(tickets, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(tickets) == 0 {
		return fail(409, "disposable specification did not generate a ticket")
	}
	for _, id := range tickets {
		if err = markDone(ctx, tx, p, id); err != nil {
			return err
		}
	}
	return nil
}

func requireDisposableOperator(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM principals WHERE id=$1::uuid AND kind='agent' AND 'operator'=ANY(roles))
		AND EXISTS(SELECT 1 FROM journey_disposable_projects WHERE project_node_id=$2::uuid)`, p.ID, project).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return fail(403, "disposable operator required")
	}
	return nil
}

func refuseLiveRelease(ctx context.Context, tx pgx.Tx, project string) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases WHERE project_node_id=$1::uuid AND state IN ('released','superseded','deploying','access'))`, project).Scan(&live); err != nil {
		return err
	}
	if live {
		return fail(409, "cannot seed a deployed or released project")
	}
	return nil
}

func lockCurrentRelease(ctx context.Context, tx pgx.Tx, project string) error {
	rows, err := tx.Query(ctx, `SELECT r.release_node_id FROM journey_releases r
		JOIN journey_projects j ON j.tenant_id=r.tenant_id AND j.current_release_node_id=r.release_node_id
		WHERE j.project_node_id=$1 FOR UPDATE OF r`, project)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	return err
}

func markDone(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) error {
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM nodes WHERE id=$1::uuid FOR UPDATE`, id).Scan(&state); err != nil {
		return err
	}
	if state == "done" {
		return nil
	}
	before, err := nodeSnapshot(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE nodes SET state='done', updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
		return err
	}
	after, err := nodeSnapshot(ctx, tx, id)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "node.updated", Before: before, After: after})
	return err
}
