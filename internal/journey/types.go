// SPDX-License-Identifier: AGPL-3.0-only

package journey

// Gate and approval scope names are the R2 contract for journey decisions.
// An agent proposes the scope on the project or release node; a person decides
// it through the approvals API. This package never writes that decision.
const (
	GateShape        = "shape"
	GateRequirements = "requirements"
	GateBuild        = "build"
	GateCandidate    = "candidate"
	GateDeploy       = "deploy"
	GateAccess       = "access"

	ScopeShape        = "journey.shape"
	ScopeRequirements = "journey.requirements"
	ScopeBuild        = "journey.build"
	ScopeCandidate    = "journey.candidate"
	ScopeDeploy       = "journey.deploy"
	ScopeAccess       = "journey.access"
)

// Journey is the derived projection returned by the journey routes. Imported is
// true for a project that came from classic Paimos with its history: its stages
// before Plan were never recorded here, so the face shows what came with it
// (description, epics, tickets) instead of intake and gates.
type Journey struct {
	ProjectNodeID string `json:"project_node_id"`
	// ProjectKey is the route key used in /p/{project_key} links (e.g. PHAROS);
	// NodeKey is the project node's immutable key (e.g. PRJ-17).
	NodeKey              string            `json:"node_key"`
	ProjectKey           string            `json:"project_key"`
	TenantSlug           string            `json:"tenant_slug"`
	Profile              string            `json:"profile"`
	Revision             int64             `json:"revision"`
	Stage                string            `json:"stage"`
	StageSource          string            `json:"stage_source"`
	Imported             bool              `json:"imported"`
	Disposable           bool              `json:"disposable"`
	Stages               []JourneyStage    `json:"stages"`
	NextAction           JourneyNextAction `json:"next_action"`
	RequirementsRevision int64             `json:"requirements_revision"`
	RequirementsDigest   string            `json:"requirements_digest_sha256"`
	RequirementsScope    string            `json:"requirements_approval_scope"`
	LaunchReadiness      LaunchReadiness   `json:"launch_readiness"`
	CurrentReleaseID     *string           `json:"current_release_id"`
}

type LaunchReadiness struct {
	CanAdmit bool   `json:"can_admit"`
	Reason   string `json:"reason"`
}

// JourneyStage is one step on the eight-stage rail.
type JourneyStage struct {
	Key                   string  `json:"key"`
	State                 string  `json:"state"`
	GateScope             string  `json:"gate_scope"`
	GateApprovalID        *string `json:"gate_approval_id"`
	GateLive              bool    `json:"gate_live"`
	GateOfferID           *string `json:"gate_offer_id,omitempty"`
	GateOfferState        string  `json:"gate_offer_state,omitempty"`
	GateOfferExpiresAt    string  `json:"gate_offer_expires_at,omitempty"`
	HandoffID             *string `json:"handoff_id"`
	HandoffAttempt        *int    `json:"handoff_attempt"`
	HandoffAuthorityEpoch *int64  `json:"handoff_authority_epoch"`
}

// JourneyNextAction is the single call to action for the project.
type JourneyNextAction struct {
	Key               string  `json:"key"`
	Label             string  `json:"label"`
	Stage             string  `json:"stage"`
	Available         bool    `json:"available"`
	Reason            string  `json:"reason,omitempty"`
	ApprovalRequestID *string `json:"approval_request_id"`
}

type profileWrite struct {
	Profile          string `json:"profile"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type actionWrite struct {
	Action            string   `json:"action"`
	ExpectedRevision  int64    `json:"expected_revision"`
	IdempotencyKey    string   `json:"idempotency_key"`
	ApprovalRequestID *string  `json:"approval_request_id"`
	ReleaseID         *string  `json:"release_id"`
	Reason            *string  `json:"reason"`
	TicketNodeIDs     []string `json:"ticket_node_ids,omitempty"`
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, msg string) error {
	return &httpError{status: status, msg: msg}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func ptrVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
