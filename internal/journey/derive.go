// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	stageInspire      = "inspire"
	stageShape        = "shape"
	stageRequirements = "requirements"
	stagePlan         = "plan"
	stageBuild        = "build"
	stageDeploy       = "deploy"
	stageAccess       = "access"
	stageLive         = "live"

	actionContinueIntake      = "continue_intake"
	actionConfirmBrief        = "confirm_brief"
	actionDecide              = "decide"
	actionReopen              = "reopen"
	actionApproveRequirements = "approve_requirements"
	actionStartBuild          = "start_build"
	actionWaitForBuild        = "wait_for_build"
	actionOpenFirstRelease    = "open_first_release"
	actionMarkCandidate       = "mark_candidate"
	actionApproveCandidate    = "approve_candidate"
	actionApproveDeploy       = "approve_deploy"
	actionRetryDeploy         = "retry_deploy"
	actionApprovePermit       = "approve_permit"
	actionPlanNext            = "plan_next_release"

	outcomePending   = "pending"
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"

	reasonShapeGate       = "Shape needs an approved gate before go, reduce scope, park or drop."
	reasonReopenGate      = "Reopen needs an approved gate."
	reasonReqNone         = "Add a requirement before agreeing."
	reasonReqGate         = "Requirements need an approved gate."
	reasonScope           = "A manual ticket changes agreed scope. Build start needs a fresh requirements agreement."
	reasonNoRelease       = "No release is open."
	reasonNoTickets       = "Select at least one ticket for this release."
	reasonEstimate        = "A selected ticket has no estimate."
	reasonNoCap           = "The approved cap is missing."
	reasonOverCap         = "The selected plan exceeds the approved cap."
	reasonBuildGate       = "Build start needs an approved gate."
	reasonCompletionGate  = "Marking a candidate needs an approved build gate."
	reasonBuilding        = "The build is still in progress."
	reasonOpenTickets     = "Finish the release's tickets before marking a candidate."
	reasonEmptyRelease    = "A release needs at least one ticket before it can become a candidate."
	reasonCandidateGate   = "Candidate review needs an approved gate."
	reasonReviewer        = "The candidate reviewer must be a different person from the builder and the author."
	reasonReviewerUnknown = "Candidate review needs a recorded builder and author."
	reasonDrafts          = "Enterprise compliance still has draft requirements."
	reasonDeployGate      = "Deployment needs an approved gate."
	reasonDeployEvidence  = "Deployment evidence is not terminal."
	reasonRetryGate       = "Deployment retry needs an approved gate."
	reasonPermitGate      = "The permit needs an approved gate."
	reasonAccessEvidence  = "Access apply has not succeeded."
	reasonAccessFailed    = "Access evidence failed."
)

var stageOrder = []string{
	stageInspire, stageShape, stageRequirements, stagePlan,
	stageBuild, stageDeploy, stageAccess, stageLive,
}

// gateOffer is the newest unconsumed approval for one gate, if any.
type gateOffer struct {
	ID        string
	DecidedBy string
	Live      bool
	State     string
	ExpiresAt string
}

// releaseFacts is the current journey release, not a client-supplied stage.
type releaseFacts struct {
	ID             string
	Number         int
	State          string
	AccessRequired bool
	Revision       int64
}

// facts are the durable inputs stage derivation is allowed to see.
// Heartbeats, timers, forecasts and client stage strings are not inputs.
type facts struct {
	ProjectID                  string
	NodeKey                    string
	ProjectKey                 string
	TenantSlug                 string
	Profile                    string
	Revision                   int64
	BriefConfirmed             bool
	AcceptedBrief              bool
	Decision                   string
	RequirementsRevision       int64
	AgreedRequirementsRevision int64
	RequirementCount           int
	DraftCount                 int
	Release                    *releaseFacts
	ImportedStage              string
	Imported                   bool
	Disposable                 bool
	CurrentReleaseRecorded     bool
	RequirementsDigest         string
	OpenReleaseTickets         int
	NextReleaseNumber          int
	PriorReleased              bool
	IncludedTickets            int
	ScopeRevision              bool
	AccessChange               bool
	MissingEstimate            bool
	PlanCents                  int64
	SpentCents                 int64
	HasCap                     bool
	CapCents                   int64
	BriefAuthor                string
	BuildStarter               string
	RequirementsDecider        string
	Shape                      gateOffer
	Requirements               gateOffer
	Build                      gateOffer
	Candidate                  gateOffer
	Deploy                     gateOffer
	Access                     gateOffer
	ShapeGateID                string
	RequirementsGateID         string
	BuildGateID                string
	CandidateGateID            string
	DeployGateID               string
	AccessGateID               string
	GateLiveByID               map[string]bool
	DeployHandoff              handoffIdentity
	AccessHandoff              handoffIdentity
	DeployOutcome              string
	VerifyOutcome              string
	AccessOutcome              string
}

func derive(f facts) Journey {
	stage, nextKey, available, reason, approvalID, blocked := project(f)
	stageSource := "journey"
	// Imported release registrations remain in planning until a Journey action
	// records real progress. An explicitly selected current release also comes
	// from Journey history, even when the project itself was imported.
	if f.ImportedStage != "" && !f.CurrentReleaseRecorded && (f.Release == nil || f.Release.State == "planning") {
		stageSource = "derived"
	}
	relID := ""
	if f.Release != nil {
		relID = f.Release.ID
	}
	return Journey{
		ProjectNodeID:        f.ProjectID,
		NodeKey:              f.NodeKey,
		ProjectKey:           f.ProjectKey,
		TenantSlug:           f.TenantSlug,
		Profile:              f.Profile,
		Revision:             f.Revision,
		Stage:                stage,
		StageSource:          stageSource,
		Imported:             f.Imported,
		Disposable:           f.Disposable,
		Stages:               stageRail(f, stage, blocked),
		NextAction:           nextAction(f, stage, nextKey, available, reason, approvalID),
		RequirementsRevision: f.RequirementsRevision,
		RequirementsDigest:   f.RequirementsDigest,
		RequirementsScope:    requirementsScope(f.Revision, f.RequirementsDigest),
		LaunchReadiness:      launchReadiness(f),
		CurrentReleaseID:     strPtr(relID),
	}
}

func requirementsScope(revision int64, digest string) string {
	return fmt.Sprintf("journey.requirements.r%d.d%s", revision, digest)
}

func launchReadiness(f facts) LaunchReadiness {
	if f.Release == nil {
		return LaunchReadiness{Reason: "No release is open."}
	}
	if f.Release.State != "deploying" {
		return LaunchReadiness{Reason: "The release is not ready for deployment."}
	}
	if f.CandidateGateID == "" {
		return LaunchReadiness{Reason: "Candidate review has not been approved."}
	}
	return LaunchReadiness{Reason: "Pharos launch checks are unavailable."}
}

func project(f facts) (stage, key string, available bool, reason, approvalID string, blocked bool) {
	if f.ImportedStage != "" {
		switch {
		case f.Release != nil && f.Release.State == "candidate":
			return candidateProjection(f)
		case f.Release != nil && (f.Release.State == "deploying" || f.Release.State == "refused"):
			return deployProjection(f)
		case f.Release != nil && f.Release.State == "access":
			return accessProjection(f)
		case f.Release != nil && (f.Release.State == "released" || f.Release.State == "superseded"):
			return stageLive, actionPlanNext, true, "", "", false
		case f.Release != nil && f.Release.State == "building":
			return buildProjection(f)
		case f.ImportedStage == stageLive && f.Release != nil && f.Release.State == "planning":
			return stageLive, actionPlanNext, true, "", "", false
		case f.ImportedStage == stageBuild && f.Release != nil:
			return buildProjection(f)
		case f.ImportedStage == stagePlan && f.Release == nil:
			return stagePlan, actionOpenFirstRelease, true, "", "", false
		case f.ImportedStage == stagePlan && f.Release != nil:
			return planProjection(f)
		}
	}
	if !f.BriefConfirmed {
		if !f.AcceptedBrief {
			return stageInspire, actionContinueIntake, true, "", "", false
		}
		return stageInspire, actionConfirmBrief, true, "", "", false
	}
	if shapeBlocks(f) {
		if f.Decision == "park" || f.Decision == "drop" {
			ok, id := offer(f.Shape)
			reason = ""
			if !ok {
				reason = gateReason(f.Shape, reasonReopenGate)
			}
			return stageShape, actionReopen, ok, reason, id, false
		}
		ok, id := offer(f.Shape)
		reason = ""
		if !ok {
			reason = gateReason(f.Shape, reasonShapeGate)
		}
		return stageShape, actionDecide, ok, reason, id, false
	}
	if requirementsStale(f) {
		return requirementsProjection(f)
	}
	if f.Release == nil || f.Release.State == "planning" {
		return planProjection(f)
	}
	switch f.Release.State {
	case "building":
		return buildProjection(f)
	case "candidate":
		return candidateProjection(f)
	case "deploying", "refused":
		return deployProjection(f)
	case "access":
		return accessProjection(f)
	case "released", "superseded":
		return stageLive, actionPlanNext, true, "", "", false
	default:
		return planProjection(f)
	}
}

func shapeBlocks(f facts) bool {
	if f.Decision == "park" || f.Decision == "drop" {
		return true
	}
	if f.Profile == "personal" {
		return false
	}
	return f.Decision != "go" && f.Decision != "reduce_scope"
}

func requirementsStale(f facts) bool {
	if f.AgreedRequirementsRevision == 0 || f.RequirementsRevision > f.AgreedRequirementsRevision {
		return true
	}
	return f.Release != nil && f.Release.State == "planning" && f.ScopeRevision
}

func requirementsProjection(f facts) (string, string, bool, string, string, bool) {
	if f.ScopeRevision && f.AgreedRequirementsRevision > 0 && f.RequirementsRevision == f.AgreedRequirementsRevision {
		ok, id := offer(f.Requirements)
		reason := reasonScope
		if ok {
			reason = ""
		}
		return stageRequirements, actionApproveRequirements, ok, reason, id, false
	}
	if f.RequirementCount == 0 {
		return stageRequirements, actionApproveRequirements, false, reasonReqNone, "", false
	}
	ok, id := offer(f.Requirements)
	reason := ""
	if !ok {
		reason = gateReason(f.Requirements, reasonReqGate)
	}
	return stageRequirements, actionApproveRequirements, ok, reason, id, false
}

func planProjection(f facts) (string, string, bool, string, string, bool) {
	if f.Release == nil {
		return stagePlan, actionOpenFirstRelease, true, "", "", false
	}
	if f.IncludedTickets == 0 {
		return stagePlan, actionStartBuild, false, reasonNoTickets, "", false
	}
	if needsCap(f.Profile) {
		switch {
		case f.MissingEstimate:
			return stagePlan, actionStartBuild, false, reasonEstimate, "", false
		case !f.HasCap:
			return stagePlan, actionStartBuild, false, reasonNoCap, "", false
		case f.PlanCents+f.SpentCents > f.CapCents:
			return stagePlan, actionStartBuild, false, reasonOverCap, "", false
		}
	}
	ok, id := offer(f.Build)
	reason := ""
	if !ok {
		reason = gateReason(f.Build, reasonBuildGate)
	}
	return stagePlan, actionStartBuild, ok, reason, id, false
}

func buildProjection(f facts) (string, string, bool, string, string, bool) {
	if f.IncludedTickets == 0 {
		return stageBuild, actionMarkCandidate, false, reasonEmptyRelease, "", false
	}
	if f.OpenReleaseTickets > 0 {
		return stageBuild, actionWaitForBuild, false, reasonBuilding, "", false
	}
	ok, id := offer(f.Build)
	if !ok {
		return stageBuild, actionMarkCandidate, false, gateReason(f.Build, reasonCompletionGate), id, false
	}
	return stageBuild, actionMarkCandidate, true, "", id, false
}

func candidateProjection(f facts) (string, string, bool, string, string, bool) {
	if f.Profile == "enterprise" {
		if strings.TrimSpace(f.BuildStarter) == "" || strings.TrimSpace(f.BriefAuthor) == "" {
			return stageBuild, actionApproveCandidate, false, reasonReviewerUnknown, f.Candidate.ID, false
		}
		if f.DraftCount > 0 {
			return stageBuild, actionApproveCandidate, false, reasonDrafts, f.Candidate.ID, false
		}
		if f.Candidate.Live && samePerson(f.Candidate.DecidedBy, f.BuildStarter, f.BriefAuthor, f.RequirementsDecider) {
			return stageBuild, actionApproveCandidate, false, reasonReviewer, f.Candidate.ID, false
		}
	}
	ok, id := offer(f.Candidate)
	reason := ""
	if !ok {
		reason = gateReason(f.Candidate, reasonCandidateGate)
	}
	return stageBuild, actionApproveCandidate, ok, reason, id, false
}

func deployProjection(f facts) (string, string, bool, string, string, bool) {
	phase := deployPhase(f)
	if f.Release != nil && f.Release.State == "refused" && phase != outcomeSucceeded {
		phase = outcomeFailed
	}
	switch phase {
	case outcomeSucceeded:
		if f.DeployGateID == "" {
			ok, id := offer(f.Deploy)
			reason := ""
			if !ok {
				reason = gateReason(f.Deploy, reasonDeployGate)
			}
			return stageDeploy, actionApproveDeploy, ok, reason, id, false
		}
		if accessNeeded(f) {
			return accessProjection(f)
		}
		return stageLive, actionPlanNext, true, "", "", false
	case outcomeFailed:
		ok, id := offer(f.Deploy)
		reason := ""
		if !ok {
			reason = gateReason(f.Deploy, reasonRetryGate)
		}
		return stageDeploy, actionRetryDeploy, ok, reason, id, true
	default:
		if f.DeployGateID != "" {
			return stageDeploy, actionApproveDeploy, false, reasonDeployEvidence, f.DeployGateID, true
		}
		ok, id := offer(f.Deploy)
		reason := ""
		if !ok {
			reason = gateReason(f.Deploy, reasonDeployGate)
		}
		return stageDeploy, actionApproveDeploy, ok, reason, id, false
	}
}

func accessProjection(f facts) (string, string, bool, string, string, bool) {
	switch f.AccessOutcome {
	case outcomeSucceeded:
		if f.AccessGateID == "" {
			ok, id := offer(f.Access)
			reason := ""
			if !ok {
				reason = gateReason(f.Access, reasonPermitGate)
			}
			return stageAccess, actionApprovePermit, ok, reason, id, false
		}
		return stageLive, actionPlanNext, true, "", "", false
	case outcomeFailed:
		ok, id := offer(f.Access)
		reason := reasonAccessFailed
		if ok {
			reason = ""
		}
		return stageAccess, actionApprovePermit, ok, reason, id, true
	default:
		if f.AccessGateID != "" {
			return stageAccess, actionApprovePermit, false, reasonAccessEvidence, f.AccessGateID, true
		}
		ok, id := offer(f.Access)
		reason := ""
		if !ok {
			reason = gateReason(f.Access, reasonPermitGate)
		}
		return stageAccess, actionApprovePermit, ok, reason, id, false
	}
}

func deployPhase(f facts) string {
	if f.DeployOutcome == outcomeFailed || f.VerifyOutcome == outcomeFailed {
		return outcomeFailed
	}
	if f.DeployOutcome == outcomeSucceeded && f.VerifyOutcome == outcomeSucceeded {
		return outcomeSucceeded
	}
	return outcomePending
}

func accessNeeded(f facts) bool {
	if f.Release == nil {
		return false
	}
	return f.Release.AccessRequired || f.AccessChange
}

func needsCap(profile string) bool {
	return profile == "professional" || profile == "enterprise"
}

func offer(g gateOffer) (bool, string) {
	if g.ID == "" {
		return false, ""
	}
	if !g.Live {
		return false, g.ID
	}
	return true, g.ID
}

func gateReason(g gateOffer, fallback string) string {
	if g.State == "expired" {
		if g.DecidedBy == "" {
			return "Gate request expired. The agent asks again for a fresh one."
		}
		return "Gate approval expired. The agent asks again for a fresh one."
	}
	if g.State == "revoked" {
		return "Gate approval was revoked. The agent asks again for a fresh one."
	}
	if g.State == "rejected" {
		return "Gate request was rejected. The agent asks again for a fresh one."
	}
	if g.State == "grant_missing" {
		return "Gate approval has no grant. The agent asks again for a fresh one."
	}
	return fallback
}

func samePerson(decider string, ids ...string) bool {
	if decider == "" {
		return false
	}
	for _, id := range ids {
		if id != "" && id == decider {
			return true
		}
	}
	return false
}

func nextAction(f facts, stage, key string, available bool, reason, approvalID string) JourneyNextAction {
	return JourneyNextAction{
		Key:               key,
		Label:             actionLabel(f, key),
		Stage:             stage,
		Available:         available,
		Reason:            reason,
		ApprovalRequestID: strPtr(approvalID),
	}
}

func actionLabel(f facts, key string) string {
	switch key {
	case actionContinueIntake:
		return "Continue intake"
	case actionConfirmBrief:
		return "Confirm brief"
	case actionDecide:
		return "Decide"
	case actionReopen:
		return "Reopen"
	case actionApproveRequirements:
		return "Approve requirements"
	case actionStartBuild:
		return "Start build"
	case actionOpenFirstRelease:
		return "Open release 1"
	case actionMarkCandidate:
		return "Mark candidate"
	case actionWaitForBuild:
		return "Building"
	case actionApproveCandidate:
		return "Approve candidate"
	case actionApproveDeploy:
		return "Approve deployment"
	case actionRetryDeploy:
		return "Retry deployment"
	case actionApprovePermit:
		return "Approve permit"
	case actionPlanNext:
		n := f.NextReleaseNumber
		if n == 0 && f.Release != nil {
			n = f.Release.Number + 1
		}
		if n == 0 {
			n = 1
		}
		return fmt.Sprintf("Plan release %d", n)
	default:
		return key
	}
}

func stageRail(f facts, current string, blocked bool) []JourneyStage {
	cur := stageIndex(current)
	shapeSkip := f.Profile == "personal" && f.BriefConfirmed && f.Decision != "park" && f.Decision != "drop" && current != stageShape
	accessSkip := f.Release != nil && cur >= stageIndex(stagePlan) && !accessNeeded(f)
	priorLive := f.PriorReleased && current == stagePlan
	out := make([]JourneyStage, 0, len(stageOrder))
	for i, key := range stageOrder {
		gateID := gateIDFor(f, key)
		handoff := handoffFor(f, key)
		st := JourneyStage{
			Key:            key,
			GateScope:      gateScopeFor(f, key),
			GateApprovalID: strPtr(gateID),
			GateLive:       f.GateLiveByID[gateID],
			HandoffID:      strPtr(handoff.ID),
		}
		if offer := gateOfferForStage(f, key); offer.ID != "" {
			st.GateOfferID = strPtr(offer.ID)
			st.GateOfferState = offer.State
			st.GateOfferExpiresAt = offer.ExpiresAt
		}
		if handoff.ID != "" {
			st.HandoffAttempt = &handoff.Attempt
			st.HandoffAuthorityEpoch = &handoff.Epoch
		}
		switch {
		case key == stageShape && shapeSkip:
			st.State = "skipped"
		case key == stageAccess && accessSkip:
			st.State = "skipped"
		case key == stageLive && priorLive:
			st.State = "done"
		case key == current && blocked:
			st.State = "blocked"
		case key == current:
			st.State = "current"
		case i < cur:
			st.State = "done"
		default:
			st.State = "later"
		}
		out = append(out, st)
	}
	return out
}

func gateScopeFor(f facts, stage string) string {
	switch stage {
	case stageShape:
		return ScopeShape
	case stageRequirements:
		return requirementsScope(f.Revision, f.RequirementsDigest)
	case stagePlan:
		return ScopeBuild
	case stageBuild:
		return ScopeCandidate
	case stageDeploy:
		return ScopeDeploy
	case stageAccess:
		return ScopeAccess
	default:
		return ""
	}
}

func gateOfferForStage(f facts, stage string) gateOffer {
	switch stage {
	case stageShape:
		return f.Shape
	case stageRequirements:
		return f.Requirements
	case stagePlan:
		return f.Build
	case stageBuild:
		return f.Candidate
	case stageDeploy:
		return f.Deploy
	case stageAccess:
		return f.Access
	default:
		return gateOffer{}
	}
}

func gateIDFor(f facts, stage string) string {
	switch stage {
	case stageShape:
		return f.ShapeGateID
	case stageRequirements:
		return f.RequirementsGateID
	case stagePlan:
		return f.BuildGateID
	case stageBuild:
		return f.CandidateGateID
	case stageDeploy:
		return f.DeployGateID
	case stageAccess:
		return f.AccessGateID
	default:
		return ""
	}
}

func handoffFor(f facts, stage string) handoffIdentity {
	switch stage {
	case stageDeploy:
		return f.DeployHandoff
	case stageAccess:
		return f.AccessHandoff
	default:
		return handoffIdentity{}
	}
}

func stageIndex(stage string) int {
	for i, key := range stageOrder {
		if key == stage {
			return i
		}
	}
	return 0
}

func actionMatches(next, action string) bool {
	switch next {
	case actionDecide:
		return action == "go" || action == "reduce_scope" || action == "park" || action == "drop"
	case actionApproveCandidate:
		return action == actionApproveCandidate || action == "reject_candidate"
	default:
		return next == action
	}
}

func parseCents(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	whole, fractional, hasPoint := strings.Cut(s, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" {
		return 0, false
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	if hasPoint {
		if fractional == "" || strings.Trim(fractional, "0123456789") != "" {
			return 0, false
		}
		if len(fractional) > 2 {
			if strings.Trim(fractional[2:], "0") != "" {
				return 0, false
			}
			fractional = fractional[:2]
		}
	}
	for len(fractional) < 2 {
		fractional += "0"
	}
	minor, err := strconv.ParseInt(fractional, 10, 64)
	if err != nil || units > (int64(^uint64(0)>>1)-minor)/100 {
		return 0, false
	}
	return units*100 + minor, true
}

func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
