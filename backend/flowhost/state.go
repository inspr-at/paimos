// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package flowhost

import (
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/delivery"
)

type BuildInput struct {
	Principal        auth.Principal
	PermissionsEpoch int64
	ProjectID        int64
	ProjectName      string
	AppVersion       string
	InstanceLabel    string
	Workflow         baselinebatch.Workflow
	Now              time.Time
}

type Gate struct {
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	GateKind    string  `json:"gateKind"`
	EvidenceRef *string `json:"evidenceRef"`
	ObservedAt  *string `json:"observedAt"`
	FreshUntil  *string `json:"freshUntil"`
	TargetRef   *string `json:"targetRef"`
	Digest      *string `json:"digest,omitempty"`
	Readiness   string  `json:"readiness,omitempty"`
}

type ForecastView struct {
	PercentComplete float64        `json:"percent_complete"`
	EstimatedFinish string         `json:"estimated_finish"`
	Kind            string         `json:"kind"`
	AsOf            string         `json:"as_of"`
	Basis           map[string]any `json:"basis"`
	ConditionalOn   *string        `json:"conditional_on"`
	PreviousTarget  *string        `json:"previous_target"`
}

type ProgressSnapshot struct {
	Progress map[string]any `json:"progress"`
	Forecast ForecastView   `json:"forecast"`
}

type ShellState struct {
	EvaluatedAt           string           `json:"evaluatedAt"`
	Header                map[string]any   `json:"header"`
	Health                map[string]any   `json:"health"`
	Delivery              map[string]any   `json:"delivery"`
	Prerequisites         map[string]Gate  `json:"prerequisites"`
	Progress              map[string]any   `json:"progress"`
	ExecutionModes        []string         `json:"executionModes"`
	SelectedExecutionMode string           `json:"selectedExecutionMode"`
	SelectedAction        string           `json:"selectedAction"`
	IdentityContext       *IdentityContext `json:"identityContext"`
}

func Build(input BuildInput) (*ShellState, error) {
	if !input.Workflow.INSPRStreamEnabled {
		return nil, ErrNotEnabled
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	freshUntil := NextTenMinuteBoundary(now)
	identity := issueIdentity(input, now, freshUntil)
	workflow := input.Workflow
	draft := workflow.Draft
	batch := workflow.ActiveBatch
	asOf := iso(now)
	fresh := iso(freshUntil)

	requirements := mapRequirements(draft, batch, asOf, fresh)
	artifact := mapArtifact(batch, asOf, fresh)
	pharos := mapPharos(batch, asOf, fresh)
	janus := mapJanus(batch, asOf, fresh)

	stageEvidence := []string{
		stageFromGate(requirements, draft != nil || batch != nil),
		stageFromGate(artifact, batch != nil),
		stageFromGate(pharos, batch != nil),
		"unknown",
	}
	if janus.Status == "pass" {
		stageEvidence[3] = "performed"
	}

	scope := scopeItems(draft, batch)
	deliveryStatus, activeStage, statusLabel, summary, mapDetail := deliveryProjection(draft, batch, requirements)
	taskLabel, taskSnap, overallSnap, freshnessLabel := mapProgress(draft, batch, now, freshUntil)

	batchRef := (*string)(nil)
	baselineRef := (*string)(nil)
	var baselineDigest *string
	if batch != nil {
		ref := OpaqueRef("batch", fmt.Sprintf("%d", batch.ID), batch.BatchKey)
		batchRef = &ref
		if batch.Baseline.BaselineRef != "" {
			b := OpaqueRef("base", batch.Baseline.BaselineRef, fmt.Sprintf("%d", batch.ID))
			baselineRef = &b
		}
		if batch.Baseline.ContentDigest != "" {
			d := batch.Baseline.ContentDigest
			baselineDigest = &d
		}
	} else if draft != nil && draft.Baseline.BaselineRef != "" {
		b := OpaqueRef("base", draft.Baseline.BaselineRef, fmt.Sprintf("%d", draft.ID))
		baselineRef = &b
		if draft.Baseline.ContentDigest != "" {
			d := draft.Baseline.ContentDigest
			baselineDigest = &d
		}
	}

	mode := "manual"
	if draft != nil && draft.ExecutionMode != "" {
		mode = draft.ExecutionMode
	}
	if batch != nil && batch.ExecutionMode != "" {
		mode = batch.ExecutionMode
	}

	version := strings.TrimSpace(input.AppVersion)
	instance := strings.TrimSpace(input.InstanceLabel)
	if instance == "" {
		instance = "Paimos"
	}

	return &ShellState{
		EvaluatedAt: asOf,
		Header: map[string]any{
			"appName":         "Paimos",
			"instanceLabel":   instance,
			"version":         version,
			"userInitials":    identity.Display.UserInitials,
			"userLabel":       identity.Display.UserLabel,
			"projectName":     identity.Display.ProjectLabel,
			"projectSubtitle": "Host-issued project context · not live identity",
		},
		Health: map[string]any{
			"status":       "available",
			"label":        "Paimos available",
			"checkedLabel": "Check service health",
			"url":          "/api/health",
		},
		Delivery: map[string]any{
			"liveReleaseLabel": "Paimos delivery stream",
			"batchTitle":       identity.Display.ProjectLabel,
			"batchSummary":     summary,
			"draftCount":       draftCount(draft),
			"mapDetail":        mapDetail,
			"batchRef":         batchRef,
			"baselineRef":      baselineRef,
			"baselineDigest":   baselineDigest,
			"status":           deliveryStatus,
			"activeStage":      activeStage,
			"stageEvidence":    stageEvidence,
			"batchStatusLabel": statusLabel,
			"scopeItems":       scope,
		},
		Prerequisites: map[string]Gate{
			"requirementsBaseline": requirements,
			"deployArtifact":       artifact,
			"pharosTarget":         pharos,
			"janusGate":            janus,
		},
		Progress: map[string]any{
			"taskLabel":      taskLabel,
			"overallLabel":   "Overall",
			"task":           taskSnap,
			"overall":        overallSnap,
			"freshnessLabel": freshnessLabel,
		},
		ExecutionModes:        []string{"manual", "assisted", "automatic"},
		SelectedExecutionMode: mode,
		SelectedAction:        "build",
		IdentityContext:       identity,
	}, nil
}

func issueIdentity(input BuildInput, now, freshUntil time.Time) *IdentityContext {
	kind := actorKindOf(input.Principal)
	label := "Host-verified human"
	if kind == ActorAgent {
		label = "Host-verified agent"
	}
	material := principalMaterial(input.Principal)
	principalRef := OpaqueRef("prin", material)
	projectRef := OpaqueRef("proj", fmt.Sprintf("%d", input.ProjectID))
	bindingRef := OpaqueRef("bind", material, fmt.Sprintf("%d", input.ProjectID), string(kind))
	revParts := []string{
		material,
		fmt.Sprintf("%d", input.ProjectID),
		string(kind),
		fmt.Sprintf("%d", input.PermissionsEpoch),
		fmt.Sprintf("%t", input.Principal.Impersonated()),
	}
	if input.Workflow.Draft != nil {
		revParts = append(revParts,
			fmt.Sprintf("d:%d:%d", input.Workflow.Draft.ID, input.Workflow.Draft.Revision),
			input.Workflow.Draft.Baseline.ContentDigest,
			fmt.Sprintf("rv:%t", input.Workflow.Draft.ReviewValid),
		)
	}
	if input.Workflow.ActiveBatch != nil {
		b := input.Workflow.ActiveBatch
		revParts = append(revParts, fmt.Sprintf("b:%d:%s:%s", b.ID, b.Status, b.ControlState))
	}
	contextRevision := OpaqueRef("ctxrev", revParts...)
	return &IdentityContext{
		ContractVersion:     IdentityContractVersion,
		EvaluatedAt:         iso(now),
		HostID:              HostID,
		PrincipalKind:       "local_host",
		PrincipalRef:        principalRef,
		BindingRef:          bindingRef,
		OrganizationRef:     nil,
		ProjectRef:          projectRef,
		ActorKind:           string(kind),
		IssuedAt:            iso(now),
		ExpiresAt:           iso(now.Add(ContextTTL)),
		FreshUntil:          iso(freshUntil),
		ContextRevision:     contextRevision,
		AuthorityDisclaimer: AuthorityDisclaimer,
		Display: IdentityDisplay{
			UserLabel:    label,
			UserInitials: initialsFromRef(principalRef),
			ProjectLabel: projectLabel(input.ProjectName),
			FixtureLabel: "Host-issued Paimos session context. Not live identity.",
		},
	}
}

func currentHumanReview(draft *baselinebatch.Draft, batch *baselinebatch.Batch) bool {
	if batch != nil && batch.ReviewID > 0 {
		return true
	}
	return draft != nil && draft.ReviewValid && draft.ReviewID != nil
}

func mapRequirements(draft *baselinebatch.Draft, batch *baselinebatch.Batch, asOf, fresh string) Gate {
	if currentHumanReview(draft, batch) {
		src := "review"
		if draft != nil && draft.ReviewID != nil {
			src = fmt.Sprintf("review:%d", *draft.ReviewID)
		} else if batch != nil {
			src = fmt.Sprintf("batch-review:%d", batch.ReviewID)
		}
		ref := OpaqueRef("ev", src)
		digest := (*string)(nil)
		if batch != nil && batch.Baseline.ContentDigest != "" {
			d := batch.Baseline.ContentDigest
			digest = &d
		} else if draft != nil && draft.Baseline.ContentDigest != "" {
			d := draft.Baseline.ContentDigest
			digest = &d
		}
		return Gate{
			Status:      "pass",
			Message:     "Current Paimos human review is bound for this project baseline.",
			GateKind:    "requirements_baseline",
			EvidenceRef: &ref,
			ObservedAt:  &asOf,
			FreshUntil:  &fresh,
			TargetRef:   nil,
			Digest:      digest,
		}
	}
	if draft != nil && draft.Baseline.Authenticity == baselinebatch.ImportedClaimAuthenticity {
		return unknownGate(
			"Imported Aithema approval is untrusted until a current Paimos human review is bound. This is not a completed requirements gate.",
			"requirements_baseline",
		)
	}
	return unknownGate(
		"No current Paimos human review is bound. Confirm the baseline on the project overview before delivery work.",
		"requirements_baseline",
	)
}

func mapArtifact(batch *baselinebatch.Batch, asOf, fresh string) Gate {
	if batch == nil {
		return unknownGate(
			"Implementation and QA evidence is not yet recorded. Stage exploration never starts work.",
			"artifact",
		)
	}
	impl := findStage(batch.Progress.Stages, delivery.StageImplementation)
	qa := findStage(batch.Progress.Stages, delivery.StageQA)
	if stagePerformed(impl) && (qa == nil || stagePerformed(qa) || !stageApplicable(qa)) {
		ref := OpaqueRef("ev", "artifact", fmt.Sprintf("%d", batch.ID))
		observed := asOf
		if impl != nil && impl.LastSignalAt != "" {
			observed = impl.LastSignalAt
		}
		return Gate{
			Status:      "pass",
			Message:     "Owned implementation evidence is on record for this batch.",
			GateKind:    "artifact",
			EvidenceRef: &ref,
			ObservedAt:  &observed,
			FreshUntil:  &fresh,
			TargetRef:   nil,
		}
	}
	if batch.Progress.SetupRequired == baselinebatch.SetupRequiredBuiltArtifact {
		return unknownGate(
			"Built artifact identity is still required. This is not build success from ticket status.",
			"artifact",
		)
	}
	return unknownGate(
		"Implementation and QA evidence is not yet recorded on this batch. Ticket status is not build success.",
		"artifact",
	)
}

func mapPharos(batch *baselinebatch.Batch, asOf, fresh string) Gate {
	if batch == nil {
		return unknownGate(
			"Pharos target readiness is not reported yet. Delivery stays gated until current external-stage evidence exists.",
			"target_readiness",
		)
	}
	switch batch.Progress.SetupRequired {
	case baselinebatch.SetupRequiredPharosRegistration,
		baselinebatch.SetupRequiredHandoffSecretMint,
		baselinebatch.SetupRequiredHandoffConfig,
		baselinebatch.SetupRequiredHandoffRevoked:
		return unknownGate(setupMessage(batch.Progress.SetupRequired), "target_readiness")
	}
	dep := findStage(batch.Progress.Stages, delivery.StageDeployment)
	if stagePerformed(dep) {
		ref := OpaqueRef("ev", "pharos", fmt.Sprintf("%d", batch.ID))
		observed := asOf
		if dep != nil && dep.LastSignalAt != "" {
			observed = dep.LastSignalAt
		}
		return Gate{
			Status:      "pass",
			Message:     "Current Pharos deployment evidence is on record for this batch.",
			GateKind:    "target_readiness",
			EvidenceRef: &ref,
			ObservedAt:  &observed,
			FreshUntil:  &fresh,
			TargetRef:   nil,
			Readiness:   "ready",
		}
	}
	if batch.Progress.SetupRequired != "" {
		return unknownGate(setupMessage(batch.Progress.SetupRequired), "target_readiness")
	}
	return unknownGate(
		"Pharos target readiness is not reported for this batch. No target is invented.",
		"target_readiness",
	)
}

func mapJanus(batch *baselinebatch.Batch, _, _ string) Gate {
	if batch != nil && batch.Progress.SetupRequired != "" {
		return unknownGate(setupMessage(batch.Progress.SetupRequired), "access")
	}
	return unknownGate(
		"Janus access evidence is not reported on this batch. Access stays gated until current evidence exists.",
		"access",
	)
}

func setupMessage(code string) string {
	switch code {
	case "":
		return ""
	case baselinebatch.SetupRequiredPharosRegistration:
		return "Pharos owner registration is still required. Delivery stays gated."
	case baselinebatch.SetupRequiredHandoffSecretMint:
		return "Handoff secret minting is still required. Delivery stays gated."
	case baselinebatch.SetupRequiredHandoffConfig:
		return "Handoff configuration is still required. Delivery stays gated."
	case baselinebatch.SetupRequiredHandoffRevoked:
		return "The current handoff was revoked. Rotate it before delivery continues."
	case baselinebatch.SetupRequiredBuiltArtifact:
		return "Built artifact identity is still required."
	case baselinebatch.SetupRequiredPrerequisiteSeal, baselinebatch.SetupRequiredPrerequisiteReview:
		return "A current prerequisite review is still required. Imported approval is not enough."
	default:
		return "External-stage setup is still required (" + code + "). Delivery stays gated."
	}
}

func unknownGate(message, kind string) Gate {
	return Gate{
		Status:      "unknown",
		Message:     message,
		GateKind:    kind,
		EvidenceRef: nil,
		ObservedAt:  nil,
		FreshUntil:  nil,
		TargetRef:   nil,
	}
}

func stageFromGate(gate Gate, inScope bool) string {
	if !inScope {
		return "unknown"
	}
	if gate.Status == "pass" {
		return "performed"
	}
	return "unknown"
}

func findStage(stages []baselinebatch.StageView, key string) *baselinebatch.StageView {
	for i := range stages {
		if stages[i].StageKey == key {
			return &stages[i]
		}
	}
	return nil
}

func stageApplicable(stage *baselinebatch.StageView) bool {
	if stage == nil {
		return false
	}
	return stage.Applicability != "not_applicable"
}

func stagePerformed(stage *baselinebatch.StageView) bool {
	return stage != nil && stage.Performed && !stage.Stale
}

func scopeItems(draft *baselinebatch.Draft, batch *baselinebatch.Batch) []string {
	items := make([]string, 0, 12)
	seen := map[string]bool{}
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || seen[text] || len(items) >= 12 {
			return
		}
		seen[text] = true
		items = append(items, text)
	}
	if draft != nil {
		selected := map[string]bool{}
		for _, ref := range draft.Selected.RequirementRefs {
			selected[ref] = true
		}
		for _, req := range draft.Requirements {
			if len(selected) == 0 || selected[req.Ref] {
				add(req.Statement)
			}
		}
	}
	if batch != nil && len(items) == 0 {
		add(batch.BatchKey)
	}
	return items
}

func draftCount(draft *baselinebatch.Draft) int {
	if draft == nil {
		return 0
	}
	n := len(draft.Unresolved)
	if n == 0 && draft.Status == baselinebatch.DraftOpen {
		return 1
	}
	return n
}

func deliveryProjection(draft *baselinebatch.Draft, batch *baselinebatch.Batch, requirements Gate) (status string, stage int, label, summary, detail string) {
	status = "draft"
	stage = 0
	label = "Requirements in progress"
	summary = "No current batch. Confirm review and start on the project overview."
	detail = "Flow maps the current Paimos baseline workflow. Imported Aithema approval is untrusted until current human review. Stage exploration never starts work."
	if requirements.Status == "pass" {
		label = "Requirements recorded · delivery gated"
		summary = "Current human review is bound. Start stays on the existing overview controls."
	}
	if batch == nil {
		if draft != nil && draft.Baseline.Authenticity == baselinebatch.ImportedClaimAuthenticity && requirements.Status != "pass" {
			summary = "Imported baseline is on record as an untrusted claim. Bind a current Paimos human review before start."
		}
		return status, stage, label, summary, detail
	}
	switch batch.Status {
	case baselinebatch.BatchActive, baselinebatch.BatchQueued:
		status = "in_progress"
		label = "Batch " + batch.Status
	case baselinebatch.BatchPaused, baselinebatch.BatchBlocked, baselinebatch.BatchCancelled:
		status = "blocked"
		label = "Batch " + batch.Status
	case baselinebatch.BatchCompleted:
		status = "completed"
		label = "Batch completed"
	}
	if stagePerformed(findStage(batch.Progress.Stages, delivery.StageDeployment)) {
		stage = 2
	} else if stagePerformed(findStage(batch.Progress.Stages, delivery.StageImplementation)) ||
		stagePerformed(findStage(batch.Progress.Stages, delivery.StageQA)) {
		stage = 1
	}
	summary = "Current batch " + batch.Status + ". Controls stay on the project overview."
	if batch.ControlState == baselinebatch.ControlPaused || batch.ControlState == baselinebatch.ControlCancelled {
		summary = "Batch is " + batch.ControlState + ". Stopped activity is not treated as live progress."
	}
	if batch.Progress.SetupRequired != "" {
		detail = setupMessage(batch.Progress.SetupRequired) + " Stage exploration never starts work."
	}
	return status, stage, label, summary, detail
}

func mapProgress(draft *baselinebatch.Draft, batch *baselinebatch.Batch, now time.Time, freshUntil time.Time) (string, ProgressSnapshot, ProgressSnapshot, string) {
	asOf := iso(now)
	fresh := iso(freshUntil)
	taskLabel := "Baseline review"
	freshnessLabel := "Educated guess · observations missing"
	overallForecast := fallbackForecast(5, now, asOf)
	taskForecast := fallbackForecast(0, now, asOf)
	taskProgress := pendingProgress(asOf, fresh, nil)
	overallProgress := pendingProgress(asOf, fresh, nil)

	var forecasts []baselinebatch.Forecast
	if batch != nil {
		forecasts = batch.Forecasts
		taskLabel = "Current batch"
		if batch.ControlState == baselinebatch.ControlPaused || batch.ControlState == baselinebatch.ControlCancelled {
			freshnessLabel = "Paused or stopped activity · not live work"
		}
	} else if draft != nil {
		forecasts = []baselinebatch.Forecast{draft.Impact.Forecast}
		taskLabel = "Baseline draft"
	}
	if currentHumanReview(draft, batch) && batch == nil {
		overallForecast = fallbackForecast(25, now, asOf)
	}
	for _, item := range forecasts {
		view := forecastFrom(item, now, asOf)
		progress := progressFrom(item, asOf, fresh)
		switch item.Subject {
		case "overall", "":
			overallForecast = view
			overallProgress = progress
			if item.Observed && item.Fresh {
				freshnessLabel = "Updated from current Paimos batch evidence"
			} else if item.Observed {
				freshnessLabel = "Observed evidence is stale"
			} else {
				freshnessLabel = "Educated guess · " + strings.TrimSpace(item.Label+" "+item.Basis)
			}
		default:
			taskLabel = item.Subject
			taskForecast = view
			taskProgress = progress
		}
	}
	if batch != nil && (batch.ControlState == baselinebatch.ControlPaused || batch.Status == baselinebatch.BatchPaused) {
		taskProgress.Progress["status"] = "pending"
		overallProgress.Progress["status"] = "pending"
		freshnessLabel = "Paused activity · not live work"
	}
	if batch != nil && (batch.ControlState == baselinebatch.ControlCancelled || batch.Status == baselinebatch.BatchCancelled) {
		taskProgress.Progress["status"] = "pending"
		overallProgress.Progress["status"] = "pending"
		freshnessLabel = "Stopped activity · not live work"
	}
	return taskLabel, ProgressSnapshot{Progress: taskProgress.Progress, Forecast: taskForecast},
		ProgressSnapshot{Progress: overallProgress.Progress, Forecast: overallForecast}, freshnessLabel
}

func fallbackForecast(percent float64, now time.Time, asOf string) ForecastView {
	return ForecastView{
		PercentComplete: percent,
		EstimatedFinish: iso(now.Add(45 * time.Minute)),
		Kind:            "educated_guess",
		AsOf:            asOf,
		Basis:           map[string]any{"kind": "manual_estimate", "evidence_ref": nil},
		ConditionalOn:   nil,
		PreviousTarget:  nil,
	}
}

func forecastFrom(item baselinebatch.Forecast, now time.Time, asOf string) ForecastView {
	kind := "educated_guess"
	basisKind := "manual_estimate"
	if item.Kind == baselinebatch.ForecastMeasured && item.Observed {
		kind = "observed"
		basisKind = "observed_work"
	} else if item.Kind == baselinebatch.ForecastWorker {
		kind = "educated_guess"
		basisKind = "manual_estimate"
	}
	finish := now.Add(45 * time.Minute)
	seconds := item.ETASeconds
	if seconds == nil {
		seconds = item.EducatedETASeconds
	}
	if seconds != nil {
		finish = now.Add(time.Duration(*seconds) * time.Second)
	}
	reported := asOf
	if item.AsOf != "" {
		reported = item.AsOf
	}
	return ForecastView{
		PercentComplete: item.Percent,
		EstimatedFinish: iso(finish),
		Kind:            kind,
		AsOf:            reported,
		Basis:           map[string]any{"kind": basisKind, "evidence_ref": nil},
		ConditionalOn:   nil,
		PreviousTarget:  nil,
	}
}

func pendingProgress(asOf, fresh string, evidence *string) ProgressSnapshot {
	freshness := "unknown"
	return ProgressSnapshot{
		Progress: map[string]any{
			"status":           "pending",
			"percent_complete": nil,
			"eta":              nil,
			"basis":            map[string]any{"kind": "unknown", "evidence_ref": evidence},
			"reporter":         map[string]any{"kind": "system", "reporter_ref": OpaqueRef("rep", "paimos")},
			"reported_at":      asOf,
			"fresh_until":      fresh,
			"freshness":        freshness,
		},
	}
}

func progressFrom(item baselinebatch.Forecast, asOf, fresh string) ProgressSnapshot {
	status := "pending"
	basisKind := "unknown"
	freshness := "unknown"
	var percent any
	if item.Observed {
		basisKind = "observed_work"
		percent = item.Percent
		if item.Percent >= 100 {
			status = "done"
		}
		if item.Fresh {
			freshness = "fresh"
		} else {
			freshness = "stale"
		}
	} else {
		basisKind = "manual_estimate"
		percent = nil
	}
	reported := asOf
	if item.AsOf != "" {
		reported = item.AsOf
	}
	return ProgressSnapshot{
		Progress: map[string]any{
			"status":           status,
			"percent_complete": percent,
			"eta":              nil,
			"basis":            map[string]any{"kind": basisKind, "evidence_ref": nil},
			"reporter":         map[string]any{"kind": "system", "reporter_ref": OpaqueRef("rep", "paimos")},
			"reported_at":      reported,
			"fresh_until":      fresh,
			"freshness":        freshness,
		},
	}
}
