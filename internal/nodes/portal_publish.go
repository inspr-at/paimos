// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Portal catalog status is a workspace decision. Generic node create, patch,
// and bulk can still change other work with nodes.write, but state and the
// public feature fields on portal kinds need a person with settings.manage.
// An agent is refused even when its scopes name that permission.

var portalPublicationKeys = []string{"live_since", "legal_basis", "decline_reason"}

func portalKind(slug string) bool {
	switch slug {
	case "portal_product", "portal_feature", "portal_wish":
		return true
	default:
		return false
	}
}

func portalModerator(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
}

func denyUnlessPortalModerator(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	err := portalModerator(ctx, tx, p)
	if err == nil {
		return nil
	}
	if errors.Is(err, authz.ErrForbidden) {
		return &httpError{status: http.StatusForbidden, msg: "permission denied"}
	}
	return err
}

func portalPublicationCreate(slug, state string, fields json.RawMessage) bool {
	if !portalKind(slug) {
		return false
	}
	if state != "open" {
		return true
	}
	return publicationKeysTouched(nil, fields)
}

func portalStatusWrite(slug string, current json.RawMessage, raw map[string]json.RawMessage) bool {
	if !portalKind(slug) {
		return false
	}
	if _, ok := raw["state"]; ok {
		return true
	}
	next, ok := raw["fields"]
	if !ok {
		return false
	}
	return publicationKeysTouched(current, next)
}

func publicationChanged(current, next json.RawMessage) bool {
	return publicationKeysTouched(current, next)
}

func publicationKeysTouched(current, next json.RawMessage) bool {
	before, beforeOK := publicationFieldMap(current)
	after, afterOK := publicationFieldMap(next)
	if !beforeOK || !afterOK {
		return true
	}
	if len(before) != len(after) {
		return true
	}
	for key, value := range before {
		if after[key] != value {
			return true
		}
	}
	return false
}

func publicationFieldMap(raw json.RawMessage) (map[string]string, bool) {
	out := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, true
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	for _, key := range portalPublicationKeys {
		value, ok := obj[key]
		if !ok || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func guardBulkPortalPublication(ctx context.Context, tx pgx.Tx, p tenant.Principal, plan bulkPlan, targets []bulkTarget) error {
	for _, target := range targets {
		if !portalKind(target.kindSlug) {
			continue
		}
		stateChange := plan.state != nil && target.node.State != *plan.state
		fieldChange := false
		if plan.changesFields() {
			next, err := nextFields(target.node.Fields, plan)
			if err != nil || publicationChanged(target.node.Fields, next) {
				fieldChange = true
			}
		}
		if stateChange || fieldChange {
			return denyUnlessPortalModerator(ctx, tx, p)
		}
	}
	return nil
}
