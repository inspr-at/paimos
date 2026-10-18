// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeroles describes and checks separately approved coordinator
// cutovers. Recipes grant no authority and never launch a vendor binary.
package nativeroles

import "github.com/inspr-at/paimos/internal/nativecontract"

// Recipe is safe to display: it contains requirements, never account paths,
// conversation references, approvals or executable operator commands.
type Recipe struct {
	Role             string   `json:"role"`
	Workspace        string   `json:"workspace"`
	Account          string   `json:"account"`
	Tools            string   `json:"tools"`
	RequiredGrants   []string `json:"required_grants"`
	RequiredEvidence []string `json:"required_evidence"`
	Pairing          string   `json:"pairing"`
	RemoteAttach     []string `json:"remote_attach"`
	TerminalViewer   string   `json:"terminal_viewer"`
	Cutover          []string `json:"cutover"`
	Rollback         []string `json:"rollback"`
	OperatorBoundary string   `json:"operator_boundary"`
}

// Lookup deliberately accepts three exact role identifiers. LEAD and arbitrary
// display names cannot select one of these grants or an automatic routine flip.
func Lookup(role string) (Recipe, error) {
	r := Recipe{Role: role,
		Account:          "Workspace owner's own locally enrolled vendor account, pinned to this instance, role and host; no copying or fallback.",
		RequiredGrants:   []string{"instance/tenant/project", "principal/owner", "role", "computer/account/workspace/model profile", "tool/settings policy and grant revision", "separate vendor-native history consent", "continuation/deadlines/admission"},
		RequiredEvidence: []string{"G_P1_LIVE", "G_ROLES", "actual host/platform and provider pins", "restricted and native capability runs", "exact conversation and approval history", "owned root/group exit and rollback"},
		Pairing:          "Pair on each owning host locally; keep the owner-only Unix socket local and use outbound authenticated server relay.",
		RemoteAttach:     []string{"approved session API with current viewer/control authority", "approved SSH invoking aeon session attach OWNED_ID on the owning host"},
		TerminalViewer:   "Owner-applied Zellij coord-session.sh ROLE with web sharing; publish only the approved https host:8082/ROLE path to people. Browser tokens stay in the browser.",
		Cutover:          []string{"Record this role's exact immutable grant, history consent, actual-host restricted/native evidence and rollback.", "Owner prepares this role inside Zellij using its existing authorized tooling; freeze the owning daemon upgrades.", "Submit owned native start with the same local account/workspace/conversation and the pinned predecessor after-exit barrier.", "Finish the predecessor handover and exit; the owning daemon proves root/group exit, revalidates grants and admission, then opens one fresh generation.", "Verify correlated inputs/finals, approval history, controls, detach and occupancy; accepted/queued alone is incomplete cutover evidence."},
		Rollback:         []string{"Disable only this role's native route; preserve legacy serving paths and authorized receipts/history.", "Stop through current owned authority and prove exact root/group exit before official CLI resume on the same account/workspace/conversation.", "Unknown exit or input acknowledgement retains occupancy and needs recovery; no successor, reinjection, account hopping or history deletion."},
	}
	switch role {
	case "Tick":
		r.Workspace = "Separately approved design/UI worktree and resource leases; no LEAD delivery workspace by name."
		r.Tools = "Explicit brokered design/read/edit/test tool list under Tick's own grant; design acceptance and release authority remain separate."
		r.OperatorBoundary = "The role owner applies Tick's launcher change and records Tick's cutover; this recipe performs no design or release approval."
	case "OPS":
		r.Workspace = "Separately approved OPS tooling worktree and exact operator resource leases; workstation qualification does not qualify production hosts."
		r.Tools = "Explicit brokered OPS tool list; production effects and external tooling require their own current operator authorization."
		r.OperatorBoundary = "Only an authorized operator changes production hosts, launchd, Zellij or OPS tooling; an OPS label grants none of those effects."
	case "AIT":
		r.Workspace = "Separately approved AIT worktree and resource leases; no Tick, OPS or LEAD workspace inheritance."
		r.Tools = "Explicit brokered AIT tool list under AIT's own actor and policy; no borrowed coordinator or operator grant."
		r.OperatorBoundary = "The role owner applies AIT's launcher change and separately records AIT's actual-host cutover and rollback."
	default:
		return Recipe{}, refuse(nativecontract.ReasonUnsupported)
	}
	return r, nil
}
