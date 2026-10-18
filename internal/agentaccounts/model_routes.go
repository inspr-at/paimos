// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

// ModelPausesSQL projects route pauses onto immutable pins, across native levels.
// Every source enforces tenant RLS. A nil expiry is an indefinite model pause.
const ModelPausesSQL = `SELECT p.tenant_id,p.id AS profile_id,
 CASE WHEN hold.model IS NULL THEN 'available' ELSE 'unavailable' END AS state,
 coalesce(hold.reason,'') AS reason,hold.until_at AS valid_until
 FROM model_profiles p LEFT JOIN model_pauses hold
 ON hold.tenant_id=p.tenant_id AND hold.harness=p.harness AND hold.model=p.model`

// SecurityExcludedFamily is the mandatory security review exclusion.
const SecurityExcludedFamily = "anthropic"
