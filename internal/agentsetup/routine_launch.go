// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"encoding/hex"
	"errors"
	"regexp"
	"time"
)

const RoutineExecutionCapability = "routine_execution_v1"
const RoutinePickupHeader = "X-Aeon-Routine-Pickup"

// RoutineGrant is immutable server intent, not a credential. The final claim
// independently checks current consent, qualification, admission and budget.
// Paths and account credentials must never be added to this projection.
type RoutineGrant struct {
	LedgerGeneration     string    `json:"ledger_generation"`
	LaunchID             string    `json:"launch_id"`
	TenantID             string    `json:"tenant_id"`
	OwnerID              string    `json:"owner_id"`
	ProjectID            string    `json:"project_id"`
	RoutineRunID         string    `json:"routine_run_id"`
	AgentRunID           string    `json:"agent_run_id"`
	ExecutionPrincipalID string    `json:"execution_principal_id"`
	PrincipalID          string    `json:"principal_id"`
	AccountID            string    `json:"account_id"`
	ComputerID           string    `json:"computer_id"`
	DaemonID             string    `json:"daemon_id"`
	DaemonGeneration     string    `json:"daemon_generation"`
	AssignmentDigest     string    `json:"assignment_digest"`
	DefinitionRevision   int64     `json:"definition_revision"`
	ConsentRevision      int64     `json:"consent_revision"`
	ExecutionRevision    int64     `json:"execution_revision"`
	QualificationID      string    `json:"qualification_id"`
	DaemonDigest         string    `json:"daemon_digest"`
	CapabilityDigest     string    `json:"capability_digest"`
	HostMappingDigest    string    `json:"host_mapping_digest"`
	Capability           string    `json:"capability"`
	RuntimeCapability    string    `json:"runtime_capability"`
	BudgetMode           string    `json:"budget_mode"`
	BudgetGrantID        string    `json:"budget_grant_id"`
	RemainingTokens      *int64    `json:"remaining_tokens,omitempty"`
	RemainingCostMicros  *int64    `json:"remaining_cost_micros,omitempty"`
	ExpiresAt            time.Time `json:"expires_at"`
}

var routineIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

var routineUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func RoutineDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (g RoutineGrant) Validate() error {
	for _, id := range []string{g.LedgerGeneration, g.LaunchID, g.TenantID, g.OwnerID, g.ProjectID, g.RoutineRunID, g.AgentRunID, g.PrincipalID, g.ExecutionPrincipalID, g.AccountID, g.ComputerID, g.QualificationID, g.BudgetGrantID} {
		if !routineUUID.MatchString(id) {
			return errors.New("routine binding identity unavailable")
		}
	}
	for _, pin := range []string{g.AssignmentDigest, g.DaemonDigest, g.CapabilityDigest, g.HostMappingDigest} {
		if !RoutineDigest(pin) {
			return errors.New("routine binding pin unavailable")
		}
	}
	if !routineIdentifier.MatchString(g.DaemonID) || !routineIdentifier.MatchString(g.DaemonGeneration) || g.Capability != RoutineExecutionCapability ||
		(g.RuntimeCapability != "routine_native_coding_v1" && g.RuntimeCapability != "routine_native_browser_v1") || g.DefinitionRevision < 1 || g.ConsentRevision < 1 || g.ExecutionRevision < 1 || g.ExpiresAt.IsZero() {
		return errors.New("routine runtime qualification unavailable")
	}
	positive := func(n *int64) bool { return n != nil && *n > 0 && *n <= 1000000000000 }
	switch g.BudgetMode {
	case "off":
		if g.RemainingTokens != nil || g.RemainingCostMicros != nil {
			return errors.New("invalid off budget grant")
		}
	case "tokens":
		if !positive(g.RemainingTokens) || g.RemainingCostMicros != nil {
			return errors.New("routine token grant unavailable")
		}
	case "money":
		if !positive(g.RemainingCostMicros) || g.RemainingTokens != nil {
			return errors.New("routine money grant unavailable")
		}
	case "both":
		if !positive(g.RemainingTokens) || !positive(g.RemainingCostMicros) {
			return errors.New("routine budget grant unavailable")
		}
	default:
		return errors.New("routine budget mode unavailable")
	}
	return nil
}

// RoutinePickup binds the claimed local checkout without publishing its path.
type RoutinePickup struct {
	LaunchID         string `json:"launch_id"`
	WorktreeID       string `json:"worktree_id"`
	Capability       string `json:"capability"`
	DaemonDigest     string `json:"daemon_digest"`
	CapabilityDigest string `json:"capability_digest"`
	ComputerID       string `json:"computer_id"`
}
