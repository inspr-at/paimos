// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var ErrConsentCoverage = errors.New("routine consent coverage exceeds 1000 candidates")

// consentCandidatesSQL walks the owner's definitions in key order and probes
// each live recurrence and project through a LIMIT 1 lateral lookup. Postgres
// cannot flatten that lookup, so a pooled connection's plan cached while the
// tables were small still does constant work per candidate instead of
// rescanning every owned definition for every recurrence.
const consentCandidatesSQL = `SELECT d.recurrence_id::text FROM recurrence_definitions d
 CROSS JOIN LATERAL (SELECT 1 FROM recurrences r
  JOIN nodes project ON project.tenant_id=r.tenant_id AND project.id=r.project_id
  WHERE r.tenant_id=d.tenant_id AND r.id=d.recurrence_id AND r.retired_at IS NULL
  AND project.deleted_at IS NULL AND project.state<>'archived' LIMIT 1) live
 WHERE d.owner_principal_id=$1 AND d.assignment IS NOT NULL
 ORDER BY d.recurrence_id LIMIT 1001`

type ConsentRequest struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Revision  int64     `json:"revision"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Href      string    `json:"href"`
}

// ConsentRequestsTx is a quiet Desk source, never a grant. The owner and live
// policy checks are the same ones used by PUT execution-consent. Candidates
// are bounded before loading assignments or resolving policy; exceeding the
// bound fails explicitly so Desk counts cannot silently omit requests.
func ConsentRequestsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, reader ExecutionRuntimeReader) ([]ConsentRequest, error) {
	out := []ConsentRequest{}
	if !modelregistry.InteractivePerson(p) || reader == nil {
		return out, nil
	}
	person, err := canonicalPerson(ctx, tx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if person != p.ID {
		return out, nil
	}
	rows, err := tx.Query(ctx, consentCandidatesSQL, person)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 1000 {
		return nil, ErrConsentCoverage
	}
	for _, id := range ids {
		item, err := load(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		if err = manageDefinition(ctx, tx, p, item.Input); errors.Is(err, authz.ErrForbidden) {
			continue
		} else if err != nil {
			return nil, err
		}
		// Stored consent can expire without a routine edit when the project
		// revision, qualification or owner policy changes. Use the same live
		// fingerprint check as the recurrence read before projecting a request.
		policy, err := routinePolicyTx(ctx, tx, p.TenantID, item, reader, true)
		if err != nil {
			return nil, err
		}
		if policy.WaitReason != "execution_consent_required" {
			continue
		}
		href := "/settings/routines"
		switch item.Definition.Scope.Kind {
		case "personal":
			href = "/settings/personal"
		case "project":
			var key string
			if err := tx.QueryRow(ctx, `SELECT key FROM nodes WHERE id=$1 AND deleted_at IS NULL`, item.Definition.Scope.ProjectID).Scan(&key); err != nil {
				return nil, err
			}
			href = "/p/" + url.PathEscape(key) + "/settings"
		}
		href += "?recurrence=" + item.ID + "&consent=1"
		name := item.Template.Name
		if name == "" {
			name = item.Template.Title
		}
		out = append(out, ConsentRequest{ID: item.ID, ProjectID: item.ProjectID, Revision: item.Revision, Title: "Let agents run “" + name + "”?", CreatedAt: item.CreatedAt.UTC(), Href: href})
	}
	return out, nil
}
