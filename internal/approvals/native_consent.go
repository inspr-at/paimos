// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The ordinary decision transaction owns the tenant fence and request lock.
// Only the current person who owns the paired computer/account can approve
// native consent; workspace administration alone cannot consent for them.
func verifyNativeConsentDecision(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Approval) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT native_consent FROM approval_requests WHERE id=$1`, a.ID).Scan(&raw); err != nil {
		return err
	}
	var facts consent.Facts
	if len(raw) > 16384 || json.Unmarshal(raw, &facts) != nil || facts.Validate() != nil || a.ResourceKind != "node" || a.ResourceID == nil || a.RunID != nil {
		return fail(http.StatusBadRequest, "invalid native consent facts")
	}
	var own bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers c
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id
 JOIN agent_accounts account ON account.tenant_id=e.tenant_id AND account.id=e.account_id
 JOIN principals person ON person.tenant_id=c.tenant_id AND person.id=$4
 JOIN model_profiles model ON model.tenant_id=c.tenant_id AND model.id=$5
 WHERE c.tenant_id=$1 AND c.id=$2 AND e.account_id=$3 AND c.state='connected'
 AND q.state='redeemed' AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL
 AND person.kind='person' AND person.status='active' AND account.archived_at IS NULL
 AND account.harness='claude' AND model.harness='claude' AND model.enabled
 AND `+modelprefs.CanonicalPersonSQL("q.approved_by")+`=`+modelprefs.CanonicalPersonSQL("person.id")+`
 AND `+modelprefs.CanonicalPersonSQL("account.owner_person_id")+`=`+modelprefs.CanonicalPersonSQL("person.id")+`)`, p.TenantID, facts.ComputerID, facts.AccountID, p.ID, facts.ModelProfileID).Scan(&own)
	if err != nil {
		return err
	}
	if !own {
		return fail(http.StatusForbidden, "native consent requires the current computer and account owner")
	}
	return nil
}
