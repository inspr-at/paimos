// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// FamilyPolicy controls family eligibility only; it grants no execution or
// approval authority and never relaxes the run/model/verdict requirements.
type FamilyPolicy struct {
	Mode            string   `json:"mode"`
	AllowedFamilies []string `json:"allowed_families"`
}

func DefaultFamilyPolicy() FamilyPolicy {
	return FamilyPolicy{Mode: "other_family", AllowedFamilies: []string{}}
}

func (p FamilyPolicy) Valid() bool {
	if p.Mode != "off" && p.Mode != "other_family" && p.Mode != "allowlist" {
		return false
	}
	if len(p.AllowedFamilies) > 6 || (p.Mode == "allowlist") != (len(p.AllowedFamilies) > 0) {
		return false
	}
	seen := map[string]bool{}
	for _, family := range p.AllowedFamilies {
		if !ValidFamily(family) || seen[family] {
			return false
		}
		seen[family] = true
	}
	return true
}

func (p FamilyPolicy) Decision(author, reviewer string) (bool, string) {
	if !p.Valid() || !ValidFamily(author) || !ValidFamily(reviewer) {
		return false, "Review family policy or provider identity is invalid."
	}
	if p.Mode == "off" {
		return true, "cross-family not required (policy off)"
	}
	if author == reviewer {
		return false, "not cross-family: author and reviewer are both " + author
	}
	if p.Mode == "allowlist" && !slices.Contains(p.AllowedFamilies, reviewer) {
		return false, "reviewer family " + reviewer + " not allowed for this project"
	}
	return true, "cross-family ok (author " + author + ", reviewer " + reviewer + ")"
}

type FamilyPolicySettings struct {
	ValidFamilies []string      `json:"valid_families"`
	Workspace     FamilyPolicy  `json:"workspace"`
	ProjectID     *string       `json:"project_id"`
	Policy        *FamilyPolicy `json:"policy"`
	Effective     FamilyPolicy  `json:"effective"`
	Source        string        `json:"source"`
	UpdatedBy     *string       `json:"updated_by"`
	UpdatedAt     *time.Time    `json:"updated_at"`
}

func localFamilyPolicy(ctx context.Context, tx pgx.Tx, project *string) (*FamilyPolicy, *string, *time.Time, error) {
	var p FamilyPolicy
	var editor string
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT mode,allowed_families,updated_by::text,updated_at FROM cross_family_policies
 WHERE project_id IS NOT DISTINCT FROM $1::uuid`, project).Scan(&p.Mode, &p.AllowedFamilies, &editor, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if !p.Valid() {
		return nil, nil, nil, errors.New("invalid stored review family policy")
	}
	return &p, &editor, &at, nil
}

// LoadFamilyPolicyTx resolves the current project's override over the tenant
// default under the caller's tenant/project RLS. Callers own authorization and
// write fences; this read never grants authority or caches a policy snapshot.
func LoadFamilyPolicyTx(ctx context.Context, tx pgx.Tx, project *string) (FamilyPolicySettings, error) {
	out := FamilyPolicySettings{ProjectID: project, Effective: DefaultFamilyPolicy(), Source: "default", ValidFamilies: ValidFamilies()}
	workspace, _, _, err := localFamilyPolicy(ctx, tx, nil)
	if err != nil {
		return out, err
	}
	if workspace != nil {
		out.Effective, out.Source = *workspace, "tenant"
	}
	out.Workspace = out.Effective
	out.Policy, out.UpdatedBy, out.UpdatedAt, err = localFamilyPolicy(ctx, tx, project)
	if err != nil {
		return out, err
	}
	if out.Policy != nil {
		out.Effective = *out.Policy
		if project != nil {
			out.Source = "project"
		}
	}
	return out, nil
}
