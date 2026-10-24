// SPDX-License-Identifier: AGPL-3.0-only

// Package assistants adds hosted persona metadata and policy to existing agent
// principals. A policy never grants keys, roles or project visibility.
package assistants

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("assistant not found")
var ErrConflict = errors.New("assistant revision or name conflict")
var errInput = errors.New("invalid assistant input")

var uuid = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var decimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)

type Budget struct {
	Amount string `json:"amount"`
	Unit   string `json:"unit"`
}

type Policy struct {
	AllowedProjects                       []string `json:"allowed_projects"`
	AllowedScopes                         []string `json:"allowed_scopes"`
	WeeklyBudget                          Budget   `json:"weekly_budget"`
	MessageCost                           string   `json:"message_cost,omitempty"`
	OutwardActionRule                     string   `json:"outward_action_rule"`
	NeverAskPersonForSecretsOrManualSteps bool     `json:"never_ask_person_for_secrets_or_manual_steps"`
	ApproverPersonIDs                     []string `json:"approver_person_ids,omitempty"`
	DelegatorPersonIDs                    []string `json:"delegator_person_ids,omitempty"`
	WeeklyResetDay                        *int     `json:"weekly_reset_day,omitempty"`
}

type Snapshot struct {
	PrincipalID   string `json:"principal_id"`
	OwnerPersonID string `json:"owner_person_id"`
	Status        string `json:"status"`
	Revision      int64  `json:"revision"`
	Policy        Policy `json:"policy"`
}

type Assistant struct {
	Snapshot
	Kind          string `json:"kind"`
	PrincipalKind string `json:"principal_kind"`
	DisplayName   string `json:"display_name"`
	Vendor        string `json:"vendor"`
	MessagingOnly bool   `json:"messaging_only"`
}

// PolicyReader is the tenant-explicit in-process seam for adapter, approval and
// routine wiring. Mutation callers hold the tenant fence in the same transaction
// so a concurrent policy edit cannot invalidate an earlier authorization read.
// HTTP authorization is separate; callers retain live key/role and status checks.
// Policy applies independently of host reachability, including sleeping hosts
// that pull work between runs; principal status is not an online/offline signal.
type PolicyReader interface {
	ReadPolicyTx(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (Snapshot, error)
}

func DefaultPolicy() Policy {
	scopes := authz.MessagingAssistantScopes()
	slices.Sort(scopes)
	return Policy{AllowedProjects: []string{}, AllowedScopes: scopes, WeeklyBudget: Budget{Amount: "0", Unit: "USD"}, MessageCost: "0.01", OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true}
}

func cleanPolicy(p Policy) (Policy, error) {
	if p.AllowedProjects == nil || p.AllowedScopes == nil || len(p.AllowedProjects) > 100 || len(p.AllowedScopes) > 200 || !decimal.MatchString(p.WeeklyBudget.Amount) || !slices.Contains([]string{"USD", "EUR", "tokens", "credits"}, p.WeeklyBudget.Unit) || p.OutwardActionRule != "approval_required" || !p.NeverAskPersonForSecretsOrManualSteps {
		return Policy{}, errInput
	}
	if p.MessageCost == "" {
		p.MessageCost = "0.01"
		if p.WeeklyBudget.Unit == "tokens" || p.WeeklyBudget.Unit == "credits" {
			p.MessageCost = "1"
		}
	}
	if amount, err := micro(p.MessageCost); err != nil || amount <= 0 {
		return Policy{}, errInput
	}
	p.AllowedProjects = slices.Clone(p.AllowedProjects)
	p.AllowedScopes = slices.Clone(p.AllowedScopes)
	if len(p.ApproverPersonIDs) > 32 || len(p.DelegatorPersonIDs) > 32 || p.WeeklyResetDay != nil && (*p.WeeklyResetDay < 0 || *p.WeeklyResetDay > 6) {
		return Policy{}, errInput
	}
	for _, ids := range []*[]string{&p.ApproverPersonIDs, &p.DelegatorPersonIDs} {
		*ids = slices.Clone(*ids)
		seen := map[string]bool{}
		for i, id := range *ids {
			id = strings.ToLower(id)
			if !uuid.MatchString(id) || seen[id] {
				return Policy{}, errInput
			}
			seen[id], (*ids)[i] = true, id
		}
		slices.Sort(*ids)
	}
	seen := map[string]bool{}
	for i, id := range p.AllowedProjects {
		id = strings.ToLower(id)
		if !uuid.MatchString(id) || seen[id] {
			return Policy{}, errInput
		}
		seen[id], p.AllowedProjects[i] = true, id
	}
	seen = map[string]bool{}
	for _, scope := range p.AllowedScopes {
		if len(scope) > 100 || !authz.KeyGrantable(scope, false) || seen[scope] {
			return Policy{}, errInput
		}
		seen[scope] = true
	}
	slices.Sort(p.AllowedProjects)
	slices.Sort(p.AllowedScopes)
	return p, nil
}

func validName(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 200 && !strings.ContainsAny(name, "\x00\r\n")
}
func validVendor(vendor string) bool { return vendor == "grok" || vendor == "unknown" }
