// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// DisposableBrief is one fixed host-operator intake. IdempotencyKey identifies
// the brief draft; the source and requirement draft use stable suffixes.
type DisposableBrief struct {
	Title          string
	Body           string
	IdempotencyKey string
}

// ApplyDisposableIntake proposes and accepts the brief and one functional
// requirement through the same source, draft and acceptance mutations as the
// intake routes. The caller must be the host operator on a disposable project.
// It does not confirm the journey or agree the requirement, and it does not
// invent a person principal. Accepting the brief may replace unconfirmed
// project text; the person route still preserves a person's edit.
func ApplyDisposableIntake(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, in DisposableBrief) error {
	if err := requireDisposableOperator(ctx, tx, p, projectID); err != nil {
		return err
	}
	if err := lockProject(ctx, tx, projectID); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(in.Body))
	source, err := insertSource(ctx, tx, p, projectID, sourceWrite{
		Kind:           "note",
		Label:          in.Title,
		ContentSHA256:  hex.EncodeToString(sum[:]),
		IdempotencyKey: in.IdempotencyKey + ":source",
	})
	if err != nil {
		return err
	}
	base, err := lastEventID(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if base == 0 {
		return fail(http.StatusConflict, "disposable brief needs an initialized journey event")
	}
	citation := citationWrite{SourceID: source.ID, Locator: "disposable-brief"}
	brief, err := insertDraft(ctx, tx, p, projectID, draftWrite{
		Kind:           "brief",
		TargetNodeID:   &projectID,
		Title:          in.Title,
		Body:           in.Body,
		BaseEventID:    &base,
		Citations:      []citationWrite{citation},
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return err
	}
	if _, err = acceptOneMode(ctx, tx, p, projectID, brief.ID, brief.BaseEventID, true); err != nil {
		return err
	}
	kind := "functional"
	zero := int64(0)
	later, access := false, false
	requirement, err := insertDraft(ctx, tx, p, projectID, draftWrite{
		Kind:            "requirement",
		RequirementKind: &kind,
		Title:           in.Title,
		Body:            in.Body,
		BaseEventID:     &zero,
		Citations:       []citationWrite{citation},
		Suggestions: []suggestionWrite{{
			Title:          in.Title,
			EstimatedHours: json.Number("0"),
			Later:          &later,
			AccessChange:   &access,
		}},
		IdempotencyKey: in.IdempotencyKey + ":requirement",
	})
	if err != nil {
		return err
	}
	_, err = acceptOneMode(ctx, tx, p, projectID, requirement.ID, requirement.BaseEventID, true)
	return err
}

func requireDisposableOperator(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM principals WHERE id=$1::uuid AND kind='agent' AND 'operator'=ANY(roles))
		AND EXISTS(SELECT 1 FROM journey_disposable_projects WHERE project_node_id=$2::uuid)`, p.ID, projectID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return fail(http.StatusForbidden, "disposable operator required")
	}
	return nil
}
