// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// PrepareWrite opens the rules write path inside a transaction the caller
// already began. It takes the tenant advisory lock and then the tenant row,
// the same order as a rules HTTP write, and it does not publish.
func PrepareWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$2,true)`, lockTimeout, statementTimeout); err != nil {
		return err
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return err
	}
	agent := ""
	if p.Kind == tenant.Agent {
		agent = p.ID
	}
	if err = enterRules(ctx, tx, owner, agent); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0)),set_config('aeon.rules_write','on',true)`); err != nil {
		return err
	}
	if err = lockAccess(ctx, tx, p.TenantID); err != nil {
		return err
	}
	var creator any
	if workorders.UUID(p.KeyCreatorID) {
		creator = p.KeyCreatorID
	}
	if _, err = tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, p.TenantID, p.ID, creator); err != nil {
		return err
	}
	if owner, err = actorOwner(ctx, tx, p); err != nil {
		return err
	}
	if err = enterRules(ctx, tx, owner, agent); err != nil {
		return err
	}
	return ensureKinds(ctx, tx, p)
}

// AppendLearningRule adds rule to the draft and leaves the published snapshot
// untouched. The set must belong to layerID. A project set must be the learning's
// project. An identity that is already in the draft is already_in_set.
func AppendLearningRule(ctx context.Context, tx pgx.Tx, p tenant.Principal, layerID, setID, projectID string, rule Rule) (Set, error) {
	s, err := loadWritableSet(ctx, tx, p, layerID, setID, projectID)
	if err != nil {
		return Set{}, err
	}
	for _, existing := range s.Rules {
		if existing.Identity == rule.Identity {
			return Set{}, fail(409, "already_in_set", "This learning is already a rule in that set.")
		}
	}
	next := append(slices.Clone(s.Rules), rule)
	return replaceDraft(ctx, tx, p, s, draftInput{ExpectedRevision: s.Revision, Name: s.Name, Rules: next})
}

// RemoveLearningRule drops rule only when the draft still holds that identity
// unchanged. A later edit, or a missing rule, is a revision conflict. An empty
// draft is an explicit empty list. Published snapshots are not touched.
func RemoveLearningRule(ctx context.Context, tx pgx.Tx, p tenant.Principal, layerID, setID, projectID string, rule Rule) error {
	s, err := loadWritableSet(ctx, tx, p, layerID, setID, projectID)
	if err != nil {
		return err
	}
	found := false
	rest := make([]Rule, 0, len(s.Rules))
	for _, existing := range s.Rules {
		if existing.Identity != rule.Identity {
			rest = append(rest, existing)
			continue
		}
		if !sameLearningRule(existing, rule) {
			return fail(409, "revision_conflict", "That rule set changed. Open it and try again.")
		}
		found = true
	}
	if !found {
		return fail(409, "revision_conflict", "That rule set changed. Open it and try again.")
	}
	_, err = replaceDraft(ctx, tx, p, s, draftInput{ExpectedRevision: s.Revision, Name: s.Name, Rules: rest})
	return err
}

func loadWritableSet(ctx context.Context, tx pgx.Tx, p tenant.Principal, layerID, setID, projectID string) (Set, error) {
	s, err := loadSet(ctx, tx, setID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Set{}, fail(404, "rule_unavailable", "That rule set is not available.")
		}
		return Set{}, err
	}
	if s.LayerID != layerID || (s.Scope.Layer == "project" && s.Scope.ProjectID != projectID) {
		return Set{}, fail(404, "rule_unavailable", "That rule set is not available.")
	}
	if err = permission(ctx, tx, p, s.Scope, "rules.write"); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return Set{}, fail(403, "rule_forbidden", "You cannot draft rules in that set.")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return Set{}, fail(404, "rule_unavailable", "That rule set is not available.")
		}
		return Set{}, err
	}
	return s, nil
}

func sameLearningRule(got, want Rule) bool {
	if len(got.Roles) != 0 || len(got.Harnesses) != 0 || got.ExpiresAt != nil || want.ExpiresAt != nil {
		return false
	}
	return got.Identity == want.Identity && got.Text == want.Text && got.Why == want.Why && got.Details == want.Details &&
		got.Strength == want.Strength && got.Enabled == want.Enabled &&
		got.Source.Reference == want.Source.Reference && got.Source.Revision == want.Source.Revision &&
		got.Source.Identity == want.Source.Identity && got.Source.EditedHere == want.Source.EditedHere
}
