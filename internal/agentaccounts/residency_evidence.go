// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ResidencyEvidence is the host's bounded attestation, never inferred from a
// vendor name, workstation label or the location of the daemon process.
type ResidencyEvidence struct {
	ProfileIDs         []string  `json:"model_profile_ids"`
	InferenceCountries []string  `json:"inference_countries"`
	StorageCountries   []string  `json:"storage_countries"`
	LogCountries       []string  `json:"log_countries"`
	LocalExecution     *bool     `json:"local_execution"`
	RetentionDays      *int      `json:"retention_days,omitempty"`
	TrainingOptOut     *bool     `json:"training_opt_out,omitempty"`
	VerifiedAt         time.Time `json:"verified_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	ProofRef           string    `json:"proof_ref"`
}

type residencyEvidenceRecord struct {
	AccountID      string            `json:"account_id"`
	Evidence       ResidencyEvidence `json:"evidence"`
	RecordedBy     string            `json:"recorded_by"`
	RecordedAt     time.Time         `json:"recorded_at"`
	BindingCurrent bool              `json:"binding_current"`
}

// The SQL alias a is always agent_accounts. Display labels, capacity and model
// grants are deliberately absent: changing them cannot expand evidence scope.
const residencyBindingSQL = `jsonb_build_object('daemon_id',a.daemon_id,
 'registered_by',a.registered_by_principal_id,'owner_person_id',a.owner_person_id,
 'link_revision',a.link_revision,'harness',a.harness,'provider',a.provider,'model',a.model)`

func (e ResidencyEvidence) validate(now time.Time) error {
	if len(e.ProfileIDs) == 0 || len(e.ProfileIDs) > 256 || e.LocalExecution == nil ||
		e.VerifiedAt.IsZero() || e.ExpiresAt.IsZero() || e.VerifiedAt.After(now) || !e.ExpiresAt.After(e.VerifiedAt) ||
		strings.TrimSpace(e.ProofRef) == "" || !utf8.ValidString(e.ProofRef) || utf8.RuneCountInString(e.ProofRef) > 1024 ||
		strings.ContainsFunc(e.ProofRef, unicode.IsControl) || looksLikeCredential(e.ProofRef) ||
		(e.RetentionDays != nil && (*e.RetentionDays < 0 || *e.RetentionDays > 36500)) {
		return fail(400, "invalid residency evidence")
	}
	seen := make(map[string]bool, len(e.ProfileIDs))
	for _, id := range e.ProfileIDs {
		if !uuidRE.MatchString(id) || seen[strings.ToLower(id)] {
			return fail(400, "invalid evidence model profiles")
		}
		seen[strings.ToLower(id)] = true
	}
	for _, countries := range [][]string{e.InferenceCountries, e.StorageCountries, e.LogCountries} {
		if countries == nil || len(countries) > 249 {
			return fail(400, "country sets required and limited to 249 entries")
		}
		seen := make(map[string]bool, len(countries))
		for _, c := range countries {
			if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' || seen[c] {
				return fail(400, "country sets require unique uppercase alpha-2 codes")
			}
			seen[c] = true
		}
	}
	return nil
}

func (e ResidencyEvidence) class(profileID string, now time.Time) string {
	if e.validate(now) != nil || !e.ExpiresAt.After(now) || !slices.Contains(e.ProfileIDs, strings.ToLower(profileID)) {
		return "any"
	}
	if *e.LocalExecution {
		return "local"
	}
	for _, countries := range [][]string{e.InferenceCountries, e.StorageCountries, e.LogCountries} {
		if len(countries) == 0 {
			return "any"
		}
		for _, c := range countries {
			// EU member states, ISO alpha-2 (GR, not the VAT-specific EL).
			switch c {
			case "AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE":
			default:
				return "any"
			}
		}
	}
	return "eu"
}

type storedResidencyClassifier struct{ now time.Time }

func (c storedResidencyClassifier) ResidencyClass(_ context.Context, _ pgx.Tx, a Account, profile string) (string, string, *time.Time, error) {
	if a.residencyEvidence == nil {
		return "any", "", nil, nil
	}
	e := a.residencyEvidence
	return e.class(profile, c.now), e.ProofRef, &e.ExpiresAt, nil
}

func loadResidencyEvidence(ctx context.Context, tx pgx.Tx, id string) (residencyEvidenceRecord, error) {
	var out residencyEvidenceRecord
	err := tx.QueryRow(ctx, `SELECT e.account_id::text,e.evidence,e.recorded_by::text,e.recorded_at,
 e.binding=`+residencyBindingSQL+` FROM agent_account_residency_evidence e
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id WHERE e.account_id=$1`, id).
		Scan(&out.AccountID, &out.Evidence, &out.RecordedBy, &out.RecordedAt, &out.BindingCurrent)
	return out, err
}

// requireEvidenceHost accepts only the bound runtime key of a connected paired
// computer. Merely registering a legacy account or naming its daemon is no proof
// of host identity, nor is being the key creator proof of person ownership.
func requireEvidenceHost(r *http.Request, tx pgx.Tx, p tenant.Principal, a Account) error {
	prefix, _, _ := presentedSecret(r)
	var owns bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id
 WHERE e.account_id=$1 AND c.principal_id=$2 AND c.daemon_id=$3
 AND k.prefix=$4 AND e.state='connected' AND c.state='connected')`, a.ID, p.ID, a.DaemonID, prefix).Scan(&owns)
	if err != nil {
		return err
	}
	if !owns || a.RegisteredBy != p.ID {
		return fail(403, "owning paired host identity required")
	}
	return nil
}

func (m *Module) residencyEvidence(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	write := r.Method == http.MethodPut
	var in ResidencyEvidence
	if write {
		r.Body = http.MaxBytesReader(w, r.Body, 32768)
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	var out residencyEvidenceRecord
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		// Role mutation takes the same tenant row. Take it before the pairing
		// lock and account row; append the event only after all other writes.
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		permission := "account.read"
		if write {
			permission = "account.manage"
			if p.Kind == tenant.Agent {
				permission = "account.probe"
			}
		}
		if p.Kind == tenant.Agent {
			var err error
			p.Scopes, err = keyScopes(ctx, tx, r, p)
			if err != nil {
				return err
			}
		}
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
			return fail(403, "permission denied")
		}
		a, err := lockAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Kind == tenant.Agent {
			if err := requireEvidenceHost(r, tx, p, a); err != nil {
				return err
			}
		} else if write {
			var owns bool
			if err := tx.QueryRow(ctx, `SELECT COALESCE(`+modelprefs.CanonicalPersonSQL("$1::uuid")+` = `+
				modelprefs.CanonicalPersonSQL("$2::uuid")+`,false)`, p.ID, a.OwnerPersonID).Scan(&owns); err != nil {
				return err
			}
			if !owns {
				return fail(403, "owning person required")
			}
		}
		if !write {
			out, err = loadResidencyEvidence(ctx, tx, id)
			if isNoRows(err) {
				return fail(404, "residency evidence not found")
			}
			return err
		}
		if err := agentpairing.AccountFence(ctx, tx, id, false); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		if err := in.validate(now); err != nil {
			return err
		}
		for i := range in.ProfileIDs {
			in.ProfileIDs[i] = strings.ToLower(in.ProfileIDs[i])
		}
		var profilesOK bool
		if err := tx.QueryRow(ctx, `SELECT count(*)=cardinality($1::uuid[]) FROM model_profiles
 WHERE id=ANY($1::uuid[]) AND harness=$2 AND enabled`, in.ProfileIDs, a.Harness).Scan(&profilesOK); err != nil {
			return err
		}
		if !profilesOK {
			return fail(400, "evidence profiles must belong to this tenant and account harness")
		}
		var before *residencyEvidenceRecord
		prior, err := loadResidencyEvidence(ctx, tx, id)
		if err == nil {
			before = &prior
		} else if !isNoRows(err) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_account_residency_evidence(tenant_id,account_id,evidence,binding,recorded_by,recorded_at)
 SELECT a.tenant_id,a.id,$2,`+residencyBindingSQL+`,$3,$4 FROM agent_accounts a WHERE a.id=$1
 ON CONFLICT(tenant_id,account_id) DO UPDATE SET evidence=excluded.evidence,binding=excluded.binding,
 recorded_by=excluded.recorded_by,recorded_at=excluded.recorded_at`, id, in, p.ID, now)
		if err != nil {
			return err
		}
		out = residencyEvidenceRecord{AccountID: id, Evidence: in, RecordedBy: p.ID, RecordedAt: now, BindingCurrent: true}
		return writeEvent(ctx, tx, p, "account.residency_evidence_updated", before, out)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
