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
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM recurrences r
 JOIN recurrence_definitions d ON d.tenant_id=r.tenant_id AND d.recurrence_id=r.id
 JOIN nodes project ON project.tenant_id=r.tenant_id AND project.id=r.project_id
 WHERE d.owner_principal_id=$1 AND d.assignment IS NOT NULL AND r.retired_at IS NULL
 AND project.deleted_at IS NULL AND project.state<>'archived'
 AND (NOT d.execute_consent OR d.consent_revision<>r.revision)
 ORDER BY r.id LIMIT 1001`, person)
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
		policy, err := routinePolicyTx(ctx, tx, p.TenantID, item, reader, false)
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
