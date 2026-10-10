// SPDX-License-Identifier: AGPL-3.0-only

package journey

import "github.com/inspr-at/paimos/internal/deploytarget"

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
	Key                   string               `json:"key"`
	State                 string               `json:"state"`
	GateScope             string               `json:"gate_scope"`
	GateApprovalID        *string              `json:"gate_approval_id"`
	GateLive              bool                 `json:"gate_live"`
	GateOfferID           *string              `json:"gate_offer_id,omitempty"`
	GateOfferState        string               `json:"gate_offer_state,omitempty"`
	GateOfferExpiresAt    string               `json:"gate_offer_expires_at,omitempty"`
	HandoffID             *string              `json:"handoff_id"`
	HandoffAttempt        *int                 `json:"handoff_attempt"`
	HandoffAuthorityEpoch *int64               `json:"handoff_authority_epoch"`
	Target                *deploytarget.Target `json:"target,omitempty"`
	TargetDigestSHA256    string               `json:"target_digest_sha256,omitempty"`
}

// JourneyNextAction is the single call to action for the project.
type JourneyNextAction struct {
	Key                 string  `json:"key"`
	RenewalAction       string  `json:"renewal_action,omitempty"`
	AccessRenewalAction string  `json:"access_renewal_action,omitempty"`
	Label               string  `json:"label"`
	Stage               string  `json:"stage"`
	Available           bool    `json:"available"`
	Reason              string  `json:"reason,omitempty"`
	ApprovalRequestID   *string `json:"approval_request_id"`
}
