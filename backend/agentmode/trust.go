// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/deliverytrust"
)

func buildDeliveryRows(entries []catalogEntry, snapshots []delivery.Snapshot, facts trustFacts,
	history durationHistory, calculatedAt time.Time) ([]DeliveryRow, error) {
	if len(entries) != len(snapshots) {
		return nil, fmt.Errorf("%w: catalog and delivery snapshot cardinality differ", ErrInvariant)
	}
	rows := make([]DeliveryRow, 0, len(entries))
	for index, entry := range entries {
		snapshot := snapshots[index]
		if entry.IssueID != snapshot.IssueID || entry.DeliveryKey != snapshot.DeliveryKey {
			return nil, fmt.Errorf("%w: catalog and delivery snapshot identity differ", ErrInvariant)
		}
		row, err := buildDeliveryRow(entry, snapshot, facts[entry.IssueID], history, calculatedAt)
		if err != nil {
			return nil, fmt.Errorf("issue %d: %w", entry.IssueID, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func buildDeliveryRow(entry catalogEntry, snapshot delivery.Snapshot, facts map[string]*trustStageFact,
	history durationHistory, calculatedAt time.Time) (DeliveryRow, error) {
	if err := validateCatalogText(entry); err != nil {
		return DeliveryRow{}, err
	}
	deliveryIdentity := "delivery:" + snapshot.DeliveryKey
	if snapshot.DeliveryID != nil {
		deliveryIdentity = fmt.Sprintf("delivery:%d", *snapshot.DeliveryID)
	}
	trustInput, err := buildTrustInput(entry, snapshot, facts, history, calculatedAt, deliveryIdentity)
	if err != nil {
		return DeliveryRow{}, err
	}
	trustOutput, err := deliverytrust.Evaluate(trustInput)
	if err != nil {
		return DeliveryRow{}, fmt.Errorf("%w: delivery trust input: %v", ErrInvariant, err)
	}
	trust := safeTrustProjection(trustOutput)

	row := DeliveryRow{
		DeliveryID: snapshot.DeliveryKey, IssueID: entry.IssueID, IssueKey: entry.IssueKey, Title: entry.Title,
		ProjectID: entry.ProjectID, ProjectKey: entry.ProjectKey, ProjectName: entry.ProjectName,
		EpicID: entry.EpicID, EpicKey: entry.EpicKey, EpicTitle: entry.EpicTitle,
		LaneKey: laneKey(entry), AttemptStatus: snapshot.State,
		DeliveryRevision: deliveryRevision(snapshot), TrustRevision: trustOutput.TrustRevision,
		Tags: append([]string{}, entry.Tags...), Capabilities: capabilities(entry, snapshot),
		Trust: trust, state: snapshot.State, active: snapshot.State != "completed" && snapshot.State != "cancelled",
		changeSequence: snapshot.ChangeSequence, UpdatedAt: entry.UpdatedAt,
	}
	if snapshot.AttemptID != nil {
		attempt := fmt.Sprintf("attempt:%d", *snapshot.AttemptID)
		plan := fmt.Sprintf("plan:%s:%d", deliveryPlanIdentity(snapshot), snapshot.PlanRevision)
		row.AttemptID, row.AttemptNumber, row.PlanRevision = &attempt, &snapshot.AttemptNumber, &plan
	}
	row.Stages, row.Evidence = publicStages(snapshot, facts)
	row.Stage = currentStageSummary(snapshot)
	row.Actor = currentActor(snapshot)
	row.Activity = currentActivity(snapshot, row.Stage.Key)
	row.Blockers = currentBlockers(snapshot)
	row.Freshness = currentFreshness(snapshot)
	row.Progress = publicProgress(trustOutput)
	row.ETA = publicETA(trustOutput, calculatedAt)
	row.attentionFlags = attentionFlags(snapshot)
	row.Attention, row.attentionSince = publicAttention(row.attentionFlags, snapshot, entry.UpdatedAt)
	row.Health = publicHealth(row.Attention, row.attentionFlags)
	row.StatusText = statusText(row, snapshot)
	row.landingAt = cloneTime(trustOutput.LandingAt)
	row.nextRefresh = nextRefreshAt(snapshot, trustOutput, calculatedAt)
	row.structuralIdentity = strings.Join([]string{
		row.DeliveryID, deliveryIdentity, optionalIntIdentity(snapshot.AttemptID), strconv.FormatInt(snapshot.PlanRevision, 10),
		strconv.FormatInt(snapshot.DeliveryRevision, 10), strconv.FormatInt(snapshot.ChangeSequence, 10),
		strconv.FormatInt(snapshot.SpecRevision, 10), row.LaneKey,
	}, "\x00")
	return row, nil
}

func buildTrustInput(entry catalogEntry, snapshot delivery.Snapshot, facts map[string]*trustStageFact,
	history durationHistory, calculatedAt time.Time, deliveryIdentity string) (deliverytrust.Input, error) {
	input := deliverytrust.Input{DeliveryIdentity: deliveryIdentity, ProjectIdentity: fmt.Sprintf("project:%d", entry.ProjectID),
		CalculatedAt: calculatedAt, PolicyVersion: deliverytrust.EstimatorPolicyVersion}
	if !snapshot.Instrumented || snapshot.AttemptID == nil {
		return input, nil
	}
	if len(snapshot.Stages) != len(delivery.CanonicalStages) || len(facts) != len(delivery.CanonicalStages) {
		return deliverytrust.Input{}, fmt.Errorf("%w: incomplete delivery trust stage set", ErrInvariant)
	}
	input.Instrumented = true
	input.Policy = make([]deliverytrust.StagePolicy, 0, len(delivery.CanonicalStages))
	input.Stages = make([]deliverytrust.StageInput, 0, len(delivery.CanonicalStages))
	attemptIdentity := fmt.Sprintf("attempt:%d", *snapshot.AttemptID)
	planIdentity := fmt.Sprintf("plan:%s:%d", deliveryPlanIdentity(snapshot), snapshot.PlanRevision)
	for index, stageKey := range delivery.CanonicalStages {
		stage := snapshot.Stages[index]
		fact := facts[stageKey]
		if fact == nil || fact.Stage != stageKey || fact.SortOrder != index+1 || stage.StageKey != stageKey ||
			fact.AttemptID != *snapshot.AttemptID || fact.AttemptNumber != snapshot.AttemptNumber ||
			fact.PlanRevision != snapshot.PlanRevision || fact.Applicability != stage.Applicability ||
			fact.Weight != stage.Weight || fact.Weight != deliverytrust.DefaultWeight(deliverytrust.StageKey(stageKey)) {
			return deliverytrust.Input{}, fmt.Errorf("%w: policy/attempt trust fact mismatch", ErrInvariant)
		}
		required := stage.Applicability == "required"
		if !required && stage.Applicability != "not_applicable" {
			return deliverytrust.Input{}, fmt.Errorf("%w: unsupported stage applicability", ErrInvariant)
		}
		input.Policy = append(input.Policy, deliverytrust.StagePolicy{Stage: deliverytrust.StageKey(stageKey),
			Required: required, Weight: stage.Weight,
			Identity: fmt.Sprintf("policy:%d:%d:%s:%s", fact.AttemptID, fact.PlanRevision, stageKey, fact.Applicability)})
		stageInput, err := buildTrustStage(entry, snapshot, stage, fact, history, calculatedAt, attemptIdentity, planIdentity)
		if err != nil {
			return deliverytrust.Input{}, err
		}
		input.Stages = append(input.Stages, stageInput)
	}
	return input, nil
}

func buildTrustStage(entry catalogEntry, snapshot delivery.Snapshot, stage delivery.StageSnapshot, fact *trustStageFact,
	history durationHistory, calculatedAt time.Time, attemptIdentity, planIdentity string) (deliverytrust.StageInput, error) {
	stageKey := deliverytrust.StageKey(stage.StageKey)
	input := deliverytrust.StageInput{Stage: stageKey, Reporter: deliverytrust.ReporterUnknown,
		Scope:      deliverytrust.Scope{AttemptID: attemptIdentity, PlanID: planIdentity},
		Completion: deliverytrust.CompletionInput{Status: completionStatus(stage.SemanticState), Eligible: stage.EligibleSuccess}}
	if stage.Applicability == "not_applicable" {
		return input, nil
	}
	if (stage.ExecutionStartEventID == nil) != (fact.ExecutionStartID == nil) ||
		(stage.ExecutionStartEventID != nil && *stage.ExecutionStartEventID != *fact.ExecutionStartID) {
		return deliverytrust.StageInput{}, fmt.Errorf("%w: execution identity mismatch", ErrInvariant)
	}
	if fact.ExecutionStartID != nil {
		if fact.ExecutionStartedAt == nil || fact.AuthorityAnchorID == nil || fact.ReporterID == nil || fact.ExecutionNumber == nil {
			return deliverytrust.StageInput{}, fmt.Errorf("%w: incomplete current execution authority", ErrInvariant)
		}
		input.ExecutionStartedAt = cloneTime(fact.ExecutionStartedAt)
		input.Reporter = reporterKind(fact.ReporterType)
		input.Scope.ExecutionID = fmt.Sprintf("execution:%d", *fact.ExecutionStartID)
		input.Scope.AuthorityID = fmt.Sprintf("authority:%d", *fact.AuthorityAnchorID)
		if fact.ResetID == nil {
			input.Scope.ResetID = fmt.Sprintf("reset:authority:%d:0", *fact.AuthorityAnchorID)
		} else {
			input.Scope.ResetID = fmt.Sprintf("reset:%d", *fact.ResetID)
		}
		input.Scope.ReporterID = fmt.Sprintf("reporter:%d", *fact.ReporterID)
		if input.Reporter == deliverytrust.ReporterAgentRun {
			if fact.RunLinkID == nil {
				return deliverytrust.StageInput{}, fmt.Errorf("%w: agent authority lacks exact run link", ErrInvariant)
			}
			input.Scope.RunLinkID = fmt.Sprintf("run-link:%d", *fact.RunLinkID)
		} else if fact.RunLinkID != nil {
			return deliverytrust.StageInput{}, fmt.Errorf("%w: non-agent authority carries run link", ErrInvariant)
		}
	} else if fact.AuthorityAnchorID != nil || fact.ReporterID != nil || fact.RunLinkID != nil || fact.ResetID != nil ||
		fact.ExecutionStartedAt != nil {
		return deliverytrust.StageInput{}, fmt.Errorf("%w: unstarted stage carries authority", ErrInvariant)
	}
	if stage.EligibleSuccess {
		if fact.SemanticID == nil || fact.SemanticKind != "stage_event" || len(fact.EvidenceIDs) == 0 {
			return deliverytrust.StageInput{}, fmt.Errorf("%w: successful stage lacks immutable completion evidence", ErrInvariant)
		}
		input.Completion.SemanticIdentity = fmt.Sprintf("stage-event:%d", *fact.SemanticID)
		input.Completion.EvidenceIdentities = evidenceIdentities(fact.EvidenceIDs)
	}
	if fact.SemanticID != nil {
		input.Signals.SemanticIdentity = fmt.Sprintf("%s:%d", fact.SemanticKind, *fact.SemanticID)
	}
	input.Signals.WaitingOnHuman = stage.NeedsInput || stage.SemanticState == "waiting" || hasHumanWait(stage.CurrentBlockers)
	input.Signals.Blocked = hasNonHumanBlocker(stage.CurrentBlockers)
	input.Signals.Stale = stage.Stale || stage.EstimateStale
	input.Signals.UnknownReporter = stage.ExecutionStartEventID != nil &&
		(input.Reporter != deliverytrust.ReporterAgentRun && input.Reporter != deliverytrust.ReporterExternal)
	input.Signals.NoSignal = stage.NeverSignaled
	if stage.NextFreshnessTransitionAt != nil {
		transition, err := parseDBTime(*stage.NextFreshnessTransitionAt)
		if err != nil || !transition.After(calculatedAt) {
			return deliverytrust.StageInput{}, fmt.Errorf("%w: invalid freshness transition", ErrInvariant)
		}
		input.Signals.TransitionAt = []time.Time{transition}
	}
	for _, estimate := range fact.Estimates {
		copy := estimate
		copy.Scope = input.Scope
		if copy.Reporter != input.Reporter {
			return deliverytrust.StageInput{}, fmt.Errorf("%w: estimate reporter differs from current owner", ErrInvariant)
		}
		input.Estimates = append(input.Estimates, copy)
	}
	input.History = append([]deliverytrust.DurationSample(nil), history[durationKey{ProjectID: entry.ProjectID, Stage: stage.StageKey}]...)
	return input, nil
}

func hasNonHumanBlocker(blockers []delivery.Blocker) bool {
	for _, blocker := range blockers {
		if !blocker.HumanWait {
			return true
		}
	}
	return false
}

func safeTrustProjection(output deliverytrust.Output) SafeTrust {
	reporter, source, basis := string(deliverytrust.ReporterUnknown), "stage_evidence", ""
	if output.ProgressSource != nil {
		reporter, source, basis = string(output.ProgressSource.ReporterKind), "owner_estimate", output.ProgressSource.Basis
	} else if output.OwnerSource != nil {
		reporter, source, basis = string(output.OwnerSource.ReporterKind), "owner_estimate", output.OwnerSource.Basis
	} else if output.OptimisticLandingAt != nil || output.PessimisticLandingAt != nil {
		source = "history"
		basis = "historical stage durations"
	}
	flags := make([]string, len(output.Flags))
	for index := range output.Flags {
		flags[index] = string(output.Flags[index])
	}
	trust := SafeTrust{SchemaVersion: deliverytrust.SchemaVersion, TrustRevision: output.TrustRevision,
		ProgressKnown: output.ProgressKnown, ProgressPercent: output.ProgressPercent,
		ConfidenceLabel: string(output.ConfidenceLabel), ReporterKind: reporter, SourceKind: source, Basis: basis,
		OptimisticLandingAt: cloneTime(output.OptimisticLandingAt), PessimisticLandingAt: cloneTime(output.PessimisticLandingAt),
		LandingAt: cloneTime(output.LandingAt), RangeOnly: output.RangeOnly, Suppression: string(output.Suppression), Flags: flags}
	if output.Scope != nil {
		trust.Scope = &PublicScope{AttemptID: output.Scope.AttemptID, PlanID: output.Scope.PlanID,
			ExecutionID: output.Scope.ExecutionID, AuthorityID: output.Scope.AuthorityID, ResetID: output.Scope.ResetID}
	}
	return trust
}

func publicStages(snapshot delivery.Snapshot, facts map[string]*trustStageFact) ([]Stage, []Evidence) {
	stages := make([]Stage, 0, len(snapshot.Stages))
	allEvidence := []Evidence{}
	for _, stage := range snapshot.Stages {
		fact := facts[stage.StageKey]
		public := Stage{Key: stage.StageKey, Label: stageLabel(stage.StageKey), Status: publicStageStatus(stage),
			Required: stage.Applicability == "required", Owner: actorForReporter(stage.ReporterType),
			Activity: safeDisplayText(stage.Activity), Blockers: safeBlockers(stage.CurrentBlockers), Evidence: safeEvidence(stage),
			StartedAt: formatTimePointer(factExecutionStarted(fact)), CompletedAt: completedAt(stage)}
		stages = append(stages, public)
		allEvidence = append(allEvidence, public.Evidence...)
	}
	return stages, allEvidence
}

func currentStageSummary(snapshot delivery.Snapshot) StageSummary {
	if len(snapshot.Stages) == 0 {
		return StageSummary{Key: "unknown", Label: "Unknown"}
	}
	selected := len(snapshot.Stages) - 1
	for index, stage := range snapshot.Stages {
		if stage.Applicability == "required" && !stage.EligibleSuccess {
			selected = index
			break
		}
	}
	index, total := selected+1, len(snapshot.Stages)
	return StageSummary{Key: snapshot.Stages[selected].StageKey, Label: stageLabel(snapshot.Stages[selected].StageKey),
		Index: &index, Total: &total}
}

func currentDeliveryStage(snapshot delivery.Snapshot) *delivery.StageSnapshot {
	if len(snapshot.Stages) == 0 {
		return nil
	}
	for index := range snapshot.Stages {
		if snapshot.Stages[index].Applicability == "required" && !snapshot.Stages[index].EligibleSuccess {
			return &snapshot.Stages[index]
		}
	}
	return &snapshot.Stages[len(snapshot.Stages)-1]
}

func currentActor(snapshot delivery.Snapshot) *Actor {
	stage := currentDeliveryStage(snapshot)
	if stage == nil {
		return nil
	}
	return actorForReporter(stage.ReporterType)
}

func currentActivity(snapshot delivery.Snapshot, stageKey string) Activity {
	stage := currentDeliveryStage(snapshot)
	if stage == nil {
		return Activity{Kind: "unknown"}
	}
	kind := "working"
	switch {
	case len(stage.CurrentBlockers) > 0:
		kind = "blocked"
	case stage.NeedsInput || stage.SemanticState == "waiting":
		kind = "waiting"
	case stage.ExecutionStartEventID == nil:
		kind = "idle"
	case stageKey == delivery.StageQA:
		kind = "testing"
	case stageKey == delivery.StageDeployment:
		kind = "deploying"
	case stageKey == delivery.StageVerification:
		kind = "verifying"
	}
	return Activity{Kind: kind, Text: safeDisplayText(stage.Activity), Since: latestTimeString(stage.LastSemanticAt, stage.AuthorityActivatedAt)}
}

func safeDisplayText(value string) string {
	if delivery.ContainsSecretLike(value) {
		return ""
	}
	return value
}

func currentBlockers(snapshot delivery.Snapshot) []SafeBlocker {
	stage := currentDeliveryStage(snapshot)
	if stage == nil {
		return []SafeBlocker{}
	}
	return safeBlockers(stage.CurrentBlockers)
}

func safeBlockers(blockers []delivery.Blocker) []SafeBlocker {
	out := make([]SafeBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		kind := blocker.Class
		if kind == "" {
			kind = "blocked"
		}
		out = append(out, SafeBlocker{Kind: kind, Text: safeDisplayText(blocker.Summary)})
	}
	return out
}

func safeEvidence(stage delivery.StageSnapshot) []Evidence {
	out := make([]Evidence, 0, len(stage.Evidence))
	for rangeIndex, item := range stage.Evidence {
		_ = rangeIndex
		out = append(out, Evidence{Kind: item.Type, Status: item.Outcome, ReportedAt: latestTimeString(stage.LastSemanticAt)})
	}
	return out
}

func currentFreshness(snapshot delivery.Snapshot) Freshness {
	stage := currentDeliveryStage(snapshot)
	if stage == nil {
		return Freshness{State: "unknown"}
	}
	state := "unknown"
	switch stage.SignalState {
	case "live":
		state = "fresh"
	case "awaiting_first_signal", "queued":
		state = "aging"
	case "stale", "no_signal":
		state = "stale"
	}
	if stage.Stale || stage.EstimateStale {
		state = "stale"
	}
	return Freshness{State: state, LastReportAt: latestTimeString(stage.LastHeartbeatAt, stage.LastSemanticAt, stage.LatestEstimateAt)}
}

func publicProgress(output deliverytrust.Output) *Progress {
	if !output.ProgressKnown {
		return nil
	}
	source, basis := "stage_evidence", ""
	if output.ProgressSource != nil {
		source, basis = "owner_estimate", output.ProgressSource.Basis
	}
	var basisPointer *string
	if basis != "" {
		basisPointer = &basis
	}
	return &Progress{Percent: output.ProgressPercent, Trusted: true, Confidence: string(output.ConfidenceLabel),
		Source: &source, Basis: basisPointer, Revision: output.TrustRevision}
}

func publicETA(output deliverytrust.Output, calculatedAt time.Time) *ETA {
	if output.LandingAt == nil && output.OptimisticLandingAt == nil && output.PessimisticLandingAt == nil {
		return nil
	}
	basis := "historical stage durations"
	if output.OwnerSource != nil && output.OwnerSource.Basis != "" {
		basis = output.OwnerSource.Basis
	}
	return &ETA{LandingAt: cloneTime(output.LandingAt), OptimisticAt: cloneTime(output.OptimisticLandingAt),
		PessimisticAt: cloneTime(output.PessimisticLandingAt), Trusted: output.Suppression == "",
		Confidence: string(output.ConfidenceLabel), Basis: &basis, CalculatedAt: calculatedAt}
}

func attentionFlags(snapshot delivery.Snapshot) CountFlags {
	flags := CountFlags{}
	for _, flag := range snapshot.AttentionFlags {
		switch flag {
		case "blocked":
			flags.Blocked = 1
		case "waiting_on_human":
			flags.WaitingNeedsInput = 1
		case "stale":
			flags.StaleNoSignal = 1
		case "unknown_reporter":
			flags.UnknownReporter = 1
		case "unverified":
			flags.Unverified = 1
		}
	}
	if snapshot.FailedNeedsRetry {
		flags.FailedNeedsRetry = 1
	}
	if snapshot.Deployed && snapshot.Unverified {
		flags.DeployedUnverified = 1
		flags.Unverified = 1
	}
	return flags
}

func publicAttention(flags CountFlags, snapshot delivery.Snapshot, fallback string) (RowAttention, *time.Time) {
	level, reason := 0, ""
	switch {
	case flags.Blocked > 0:
		level, reason = 3, "blocked"
	case flags.WaitingNeedsInput > 0:
		level, reason = 2, "waiting_needs_input"
	case flags.FailedNeedsRetry > 0:
		level, reason = 2, "failed_needs_retry"
	case flags.StaleNoSignal > 0:
		level, reason = 1, "stale_no_signal"
	case flags.UnknownReporter > 0:
		level, reason = 1, "unknown_reporter"
	case flags.DeployedUnverified > 0:
		level, reason = 1, "deployed_unverified"
	}
	if level == 0 {
		return RowAttention{}, nil
	}
	stage := currentDeliveryStage(snapshot)
	var raw *string
	switch reason {
	case "blocked":
		if stage != nil {
			raw = stage.BlockedSince
		}
	case "waiting_needs_input":
		if stage != nil {
			raw = stage.HumanWaitSince
			if raw == nil {
				raw = stage.LastSemanticAt
			}
		}
	case "failed_needs_retry":
		if stage != nil {
			raw = stage.LastSemanticAt
		}
	case "stale_no_signal":
		if stage != nil {
			raw = stage.StaleSince
		}
	case "unknown_reporter":
		if stage != nil {
			raw = stage.AuthorityActivatedAt
		}
		if raw == nil && fallback != "" {
			raw = &fallback
		}
	case "deployed_unverified":
		for index := range snapshot.Stages {
			if snapshot.Stages[index].StageKey == delivery.StageDeployment {
				raw = snapshot.Stages[index].LastSemanticAt
				break
			}
		}
	}
	var since *time.Time
	if raw != nil {
		if parsed, err := parseDBTime(*raw); err == nil {
			since = &parsed
		}
	}
	return RowAttention{Level: level, Reason: reason, Since: formatTimePointer(since)}, since
}

func publicHealth(attention RowAttention, flags CountFlags) Health {
	switch {
	case flags.Blocked > 0:
		return HealthBlocked
	case flags.StaleNoSignal > 0:
		return HealthStale
	case flags.UnknownReporter > 0:
		return HealthUnknown
	case attention.Level > 0:
		return HealthAttention
	default:
		return HealthHealthy
	}
}

func nextRefreshAt(snapshot delivery.Snapshot, trust deliverytrust.Output, calculatedAt time.Time) *time.Time {
	candidates := []time.Time{}
	if trust.NextTrustTransitionAt != nil {
		candidates = append(candidates, trust.NextTrustTransitionAt.UTC())
	}
	for _, stage := range snapshot.Stages {
		if stage.NextFreshnessTransitionAt == nil {
			continue
		}
		if parsed, err := parseDBTime(*stage.NextFreshnessTransitionAt); err == nil {
			candidates = append(candidates, parsed)
		}
	}
	if trust.LandingAt != nil {
		for _, duration := range []time.Duration{72 * time.Hour, 24 * time.Hour, 4 * time.Hour} {
			candidates = append(candidates, trust.LandingAt.Add(-duration))
		}
	}
	var earliest *time.Time
	for _, candidate := range candidates {
		candidate = candidate.UTC()
		if candidate.After(calculatedAt) && (earliest == nil || candidate.Before(*earliest)) {
			copy := candidate
			earliest = &copy
		}
	}
	return earliest
}

func validateCatalogText(entry catalogEntry) error {
	fields := []struct {
		value string
		limit int
	}{{entry.IssueKey, 128}, {entry.Title, 1024}, {entry.ProjectKey, 128}, {entry.ProjectName, 512}, {entry.UpdatedAt, 64}}
	if entry.EpicKey != nil {
		fields = append(fields, struct {
			value string
			limit int
		}{*entry.EpicKey, 128})
	}
	if entry.EpicTitle != nil {
		fields = append(fields, struct {
			value string
			limit int
		}{*entry.EpicTitle, 1024})
	}
	for _, tag := range entry.Tags {
		fields = append(fields, struct {
			value string
			limit int
		}{tag, 128})
	}
	for _, field := range fields {
		if !safeSingleLine(field.value, field.limit) {
			return fmt.Errorf("%w: unsafe or unbounded display text", ErrInvariant)
		}
	}
	return nil
}

func safeSingleLine(value string, limit int) bool {
	if !utf8.ValidString(value) || len([]byte(value)) > limit || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, runeValue := range value {
		if unicode.IsControl(runeValue) {
			return false
		}
	}
	return true
}

func reporterKind(value string) deliverytrust.ReporterKind {
	switch value {
	case "agent_run":
		return deliverytrust.ReporterAgentRun
	case "external":
		return deliverytrust.ReporterExternal
	case "user":
		return deliverytrust.ReporterUser
	case "system":
		return deliverytrust.ReporterSystem
	default:
		return deliverytrust.ReporterUnknown
	}
}

func actorForReporter(value string) *Actor {
	switch value {
	case "agent_run":
		return &Actor{Name: "agent", Label: "Agent runner", Kind: "agent"}
	case "external":
		return &Actor{Name: "external", Label: "External reporter", Kind: "system"}
	case "user":
		return &Actor{Name: "user", Label: "Human", Kind: "human"}
	case "system":
		return &Actor{Name: "system", Label: "System", Kind: "system"}
	default:
		return nil
	}
}

func completionStatus(value string) deliverytrust.CompletionStatus {
	switch value {
	case "succeeded":
		return deliverytrust.CompletionSucceeded
	case "failed":
		return deliverytrust.CompletionFailed
	case "cancelled":
		return deliverytrust.CompletionCancelled
	case "conflict":
		return deliverytrust.CompletionConflict
	default:
		return deliverytrust.CompletionPending
	}
}

func publicStageStatus(stage delivery.StageSnapshot) string {
	switch {
	case stage.Applicability == "not_applicable":
		return "not_applicable"
	case stage.EligibleSuccess:
		return "succeeded"
	case stage.SemanticState == "failed" || stage.SemanticState == "cancelled":
		return "failed"
	case len(stage.CurrentBlockers) > 0:
		return "blocked"
	case stage.NeedsInput || stage.SemanticState == "waiting":
		return "waiting"
	case stage.ExecutionStartEventID != nil:
		return "active"
	default:
		return "pending"
	}
}

func stageLabel(stage string) string {
	switch stage {
	case delivery.StageQA:
		return "QA"
	case delivery.StageSpecification:
		return "Specification"
	case delivery.StageImplementation:
		return "Implementation"
	case delivery.StageDeployment:
		return "Deployment"
	case delivery.StageVerification:
		return "Verification"
	default:
		return "Unknown"
	}
}

func capabilities(entry catalogEntry, snapshot delivery.Snapshot) Capabilities {
	editor := entry.AccessLevel == "editor"
	activeRun := len(snapshot.LegacyActiveRuns) > 0
	for _, stage := range snapshot.Stages {
		activeRun = activeRun || (stage.OwnerRunID != nil && !stage.EligibleSuccess)
	}
	return Capabilities{ViewIssue: true, EditIssue: editor, Comment: editor, Attach: editor,
		LiveNote: false, OneShotRunActive: activeRun}
}

func statusText(row DeliveryRow, snapshot delivery.Snapshot) string {
	switch row.Attention.Reason {
	case "blocked":
		return "Blocked"
	case "waiting_needs_input":
		return "Waiting for input"
	case "failed_needs_retry":
		return "Retry required"
	case "stale_no_signal":
		return "No recent signal"
	case "unknown_reporter":
		return "Reporter unavailable"
	case "deployed_unverified":
		return "Deployed; verification pending"
	}
	if snapshot.State == "completed" {
		return "Completed"
	}
	return ""
}

func deliveryPlanIdentity(snapshot delivery.Snapshot) string {
	if snapshot.DeliveryID != nil {
		return strconv.FormatInt(*snapshot.DeliveryID, 10)
	}
	return snapshot.DeliveryKey
}

func deliveryRevision(snapshot delivery.Snapshot) string {
	return fmt.Sprintf("delivery:%s:%d:%d", deliveryPlanIdentity(snapshot), snapshot.DeliveryRevision, snapshot.ChangeSequence)
}

func laneKey(entry catalogEntry) string {
	if entry.EpicID == nil {
		return fmt.Sprintf("project:%d/ungrouped", entry.ProjectID)
	}
	return fmt.Sprintf("project:%d/epic:%d", entry.ProjectID, *entry.EpicID)
}

func evidenceIdentities(ids []string) []string {
	copyIDs := append([]string(nil), ids...)
	sort.Strings(copyIDs)
	out := make([]string, 0, len(copyIDs))
	for _, id := range copyIDs {
		out = append(out, "evidence:"+id)
	}
	return out
}

func hasHumanWait(blockers []delivery.Blocker) bool {
	for _, blocker := range blockers {
		if blocker.HumanWait {
			return true
		}
	}
	return false
}

func factExecutionStarted(fact *trustStageFact) *time.Time {
	if fact == nil {
		return nil
	}
	return fact.ExecutionStartedAt
}

func completedAt(stage delivery.StageSnapshot) *string {
	if !stage.EligibleSuccess {
		return nil
	}
	return latestTimeString(stage.LastSemanticAt)
}

func latestTimeString(values ...*string) *string {
	var latest *time.Time
	for _, value := range values {
		if value == nil {
			continue
		}
		parsed, err := parseDBTime(*value)
		if err != nil {
			continue
		}
		if latest == nil || parsed.After(*latest) {
			copy := parsed
			latest = &copy
		}
	}
	return formatTimePointer(latest)
}

func formatTimePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func optionalIntIdentity(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}
