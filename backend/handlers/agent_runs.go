// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// PAI-606 (epic PAI-605): agent-run lifecycle API for the "Implement this"
// feature. The UI button creates a queued run on an issue; the developer's
// local runner transitions it and posts the structured report. SSE delivery
// to online runners is PAI-607; this slice is the persistence + REST surface.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/ai"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/sse"
)

const agentRunTestsSummaryMaxBytes = 4096

// AgentRun is the lifecycle record for one "Implement this" run.
type AgentRun struct {
	ID                             int64   `json:"id"`
	IssueID                        int64   `json:"issue_id"`
	ProjectID                      *int64  `json:"project_id"`
	DeviceID                       string  `json:"device_id"`
	RequestedBy                    *int64  `json:"requested_by"`
	ClaimedBy                      *int64  `json:"claimed_by"`
	ActionKey                      string  `json:"action_key"`
	ProviderKind                   string  `json:"provider_kind"`
	ProviderID                     string  `json:"provider_id"`
	ProviderLabel                  string  `json:"provider_label"`
	Model                          string  `json:"model"`
	RunMode                        string  `json:"run_mode"`
	ProfileID                      string  `json:"profile_id"`
	Effort                         string  `json:"effort"`
	PromptPresetRef                string  `json:"prompt_preset_ref"`
	ContextPack                    string  `json:"context_pack"`
	ContextTruncated               bool    `json:"context_truncated,omitempty"`
	ContextSourcesJSON             string  `json:"context_sources_json,omitempty"`
	PromptTokens                   int     `json:"prompt_tokens,omitempty"`
	CompletionTokens               int     `json:"completion_tokens,omitempty"`
	FinishReason                   string  `json:"finish_reason,omitempty"`
	AgentName                      string  `json:"agent_name"`
	SessionID                      string  `json:"session_id"`
	Status                         string  `json:"status"`
	Version                        string  `json:"version"`
	TestsSummary                   *string `json:"tests_summary"`
	DeployTarget                   string  `json:"deploy_target"`
	RepoURL                        string  `json:"repo_url"`
	BranchName                     string  `json:"branch_name"`
	CommitBaseSHA                  string  `json:"commit_base_sha"`
	CommitSHA                      string  `json:"commit_sha"`
	LogAttachmentID                *int64  `json:"log_attachment_id"`
	Error                          string  `json:"error"`
	CreatedAt                      string  `json:"created_at"`
	StartedAt                      *string `json:"started_at"`
	FinishedAt                     *string `json:"finished_at"`
	SourceDraftRunID               *int64  `json:"source_draft_run_id,omitempty"`
	FollowupRunID                  *int64  `json:"followup_run_id,omitempty"`
	ExpectsSupervisorTelemetry     bool    `json:"expects_supervisor_telemetry"`
	DeliveryInstrumentationVersion int     `json:"delivery_instrumentation_version"`
}

// agentRunStatuses is the allowed lifecycle set (mirrors the DB CHECK).
var agentRunStatuses = map[string]bool{
	"queued": true, "running": true, "completed": true, "tests_passed": true, "tests_failed": true,
	"deployed": true, "failed": true, "cancelled": true, "drafted": true,
}

// agentRunIsTerminal means lifecycle-immutable. Test result statuses retain
// explicit deploy/fail edges, even though they close the separate telemetry
// stream (see agentRunTelemetryIsTerminal).
func agentRunIsTerminal(s string) bool {
	return s == "completed" || s == "deployed" || s == "failed" || s == "cancelled" || s == "drafted"
}

// legalRunTransitions is the run lifecycle as a directed graph. Terminal states
// (deployed/failed/cancelled) have no outgoing edges and are handled by the
// terminal-immutability guard. A same-status PATCH is a no-op (not a transition).
var legalRunTransitions = map[string]map[string]bool{
	"queued":       {"running": true, "cancelled": true},
	"running":      {"completed": true, "tests_passed": true, "tests_failed": true, "deployed": true, "failed": true, "cancelled": true, "drafted": true},
	"tests_passed": {"deployed": true, "failed": true},
	"tests_failed": {"failed": true},
}

func isLegalRunTransition(from, to string) bool {
	return legalRunTransitions[from][to]
}

// attachmentBelongsToIssue reports whether attachment attID is linked to issueID.
func attachmentBelongsToIssue(attID, issueID int64) bool {
	var aIssue sql.NullInt64
	if err := db.DB.QueryRow(`SELECT issue_id FROM attachments WHERE id=?`, attID).Scan(&aIssue); err != nil {
		return false
	}
	return aIssue.Valid && aIssue.Int64 == issueID
}

const agentRunCols = `id, issue_id, project_id, device_id, requested_by, claimed_by, ` +
	`action_key, provider_kind, provider_id, provider_label, model, run_mode, ` +
	`profile_id, effort, prompt_preset_ref, context_pack, context_truncated, context_sources_json, prompt_tokens, completion_tokens, finish_reason, ` +
	`agent_name, session_id, ` +
	`status, version, tests_summary, deploy_target, repo_url, branch_name, commit_base_sha, commit_sha, log_attachment_id, error, created_at, started_at, finished_at, ` +
	`source_draft_run_id, followup_run_id, expects_supervisor_telemetry, delivery_instrumentation_version`

func scanAgentRun(row interface{ Scan(...any) error }) (*AgentRun, error) {
	var ar AgentRun
	var projectID, requestedBy, claimedBy, logAtt, sourceDraftRunID, followupRunID sql.NullInt64
	var tests, startedAt, finishedAt sql.NullString
	var contextTruncated, expectsSupervisorTelemetry int
	if err := row.Scan(&ar.ID, &ar.IssueID, &projectID, &ar.DeviceID, &requestedBy,
		&claimedBy, &ar.ActionKey, &ar.ProviderKind, &ar.ProviderID, &ar.ProviderLabel,
		&ar.Model, &ar.RunMode, &ar.ProfileID, &ar.Effort, &ar.PromptPresetRef,
		&ar.ContextPack, &contextTruncated, &ar.ContextSourcesJSON, &ar.PromptTokens,
		&ar.CompletionTokens, &ar.FinishReason, &ar.AgentName, &ar.SessionID, &ar.Status, &ar.Version, &tests,
		&ar.DeployTarget, &ar.RepoURL, &ar.BranchName, &ar.CommitBaseSHA, &ar.CommitSHA,
		&logAtt, &ar.Error, &ar.CreatedAt, &startedAt, &finishedAt,
		&sourceDraftRunID, &followupRunID, &expectsSupervisorTelemetry, &ar.DeliveryInstrumentationVersion); err != nil {
		return nil, err
	}
	ar.ContextTruncated = contextTruncated == 1
	ar.ExpectsSupervisorTelemetry = expectsSupervisorTelemetry == 1
	if projectID.Valid {
		ar.ProjectID = &projectID.Int64
	}
	if requestedBy.Valid {
		ar.RequestedBy = &requestedBy.Int64
	}
	if claimedBy.Valid {
		ar.ClaimedBy = &claimedBy.Int64
	}
	if logAtt.Valid {
		ar.LogAttachmentID = &logAtt.Int64
	}
	if tests.Valid {
		ar.TestsSummary = &tests.String
	}
	if startedAt.Valid {
		ar.StartedAt = &startedAt.String
	}
	if finishedAt.Valid {
		ar.FinishedAt = &finishedAt.String
	}
	if sourceDraftRunID.Valid {
		ar.SourceDraftRunID = &sourceDraftRunID.Int64
	}
	if followupRunID.Valid {
		ar.FollowupRunID = &followupRunID.Int64
	}
	return &ar, nil
}

func getAgentRunByID(id int64) (*AgentRun, error) {
	return scanAgentRun(db.DB.QueryRow(`SELECT `+agentRunCols+` FROM agent_runs WHERE id=?`, id))
}

// deliveryStoreForRequest binds every delivery-side write to the request's
// current project edit authority. The delivery package resolves the root
// issue's project inside the caller's transaction, so this deliberately does
// not trust the run's requester/claimer or its historical project_id.
// Projectless issues retain the existing RequireIssueEdit orphan semantics.
func deliveryStoreForRequest(r *http.Request) *delivery.Store {
	return delivery.NewStore(db.DB, delivery.Options{Freshness: deliveryFreshnessPolicy(), Authorizer: delivery.AuthorizerFunc(
		func(_ context.Context, req delivery.AuthorizationRequest) error {
			user := auth.GetUser(r)
			if user == nil || user.Status != "active" {
				return errors.New("active authenticated user required")
			}
			if req.ProjectID != nil && !auth.CanEditProject(r, *req.ProjectID) {
				return errors.New("current project edit access required")
			}
			return nil
		},
	)})
}

func deliveryFreshnessPolicy() delivery.FreshnessPolicy {
	cfg := AgentRunReconcilerConfigFromEnv()
	return delivery.FreshnessPolicy{
		FirstSignalTimeout: cfg.FirstHeartbeatTimeout,
		HeartbeatTimeout:   cfg.HeartbeatTimeout,
		EstimateTimeout:    cfg.HeartbeatTimeout,
	}
}

func deliveryUserActor(r *http.Request) delivery.Actor {
	if u := auth.GetUser(r); u != nil {
		return delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", u.ID)}
	}
	return delivery.Actor{Type: "system", OpaqueKey: "paimos"}
}

// canReadAgentRun: an admin, requester, or project editor may read a run. This
// keeps the UI/watch surfaces usable for project editors without granting every
// editor write access to another user's queued/running job.
func canReadAgentRun(r *http.Request, run *AgentRun) bool {
	u := auth.GetUser(r)
	if u == nil {
		return false
	}
	if u.Role == auth.RoleAdmin || u.Role == auth.RoleSuperAdmin {
		return true
	}
	if run.RequestedBy != nil && *run.RequestedBy == u.ID {
		return true
	}
	if run.ClaimedBy != nil && *run.ClaimedBy == u.ID {
		return true
	}
	return run.ProjectID != nil && auth.CanEditProject(r, *run.ProjectID)
}

func userHasLiveImplementRunner(userID, projectID int64, deviceID, actionKey string) bool {
	if deviceID == "" {
		return false
	}
	var n int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM auto_watch_subscriptions
		 WHERE user_id=? AND device_id=? AND project_id=? AND enabled=1 AND can_implement=1`,
		userID, deviceID, projectID).Scan(&n); err != nil || n == 0 {
		return false
	}
	for _, p := range sse.GlobalBroker().ProjectSubscribers(projectID) {
		if p.UserID == userID && p.DeviceID == deviceID && p.CanImplement &&
			actionCapabilitiesContain(presenceActions(p), actionKey) {
			return true
		}
	}
	return false
}

// canPatchAgentRun: an admin or requester can always manage a non-terminal run.
// A project editor may make the initial queued->running CAS claim only from a
// matching live implement-capable runner; after that only the stamped claimer
// can report/cancel/update it.
func canPatchAgentRun(r *http.Request, run *AgentRun, claimAttempt bool, claimDeviceID string) bool {
	u := auth.GetUser(r)
	if u == nil {
		return false
	}
	if u.Role == auth.RoleAdmin || u.Role == auth.RoleSuperAdmin {
		return true
	}
	if run.RequestedBy != nil && *run.RequestedBy == u.ID {
		return true
	}
	if run.ClaimedBy != nil && *run.ClaimedBy == u.ID {
		return true
	}
	if claimAttempt && run.ProjectID != nil && auth.CanEditProject(r, *run.ProjectID) &&
		userHasLiveImplementRunner(u.ID, *run.ProjectID, claimDeviceID, run.ActionKey) {
		return true
	}
	// Compatibility for active rows created before M128: if a run was already
	// running but has no claimer, keep project-editor report-back working.
	return run.ClaimedBy == nil && run.Status == "running" && run.ProjectID != nil && auth.CanEditProject(r, *run.ProjectID)
}

// ImplementIssue — POST /api/issues/{id}/implement (RequireIssueEdit).
// Creates a queued run for the issue. SSE notification of online runners is
// wired in PAI-607.
func ImplementIssue(w http.ResponseWriter, r *http.Request) {
	issueID, ok := resolveIssueIDFromRequest(r)
	if !ok {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}
	var body struct {
		DeviceID         string          `json:"device_id"`
		DeployTarget     string          `json:"deploy_target"`
		ActionKey        string          `json:"action_key"`
		AgentName        string          `json:"agent_name"`
		Options          aiActionOptions `json:"options"`
		SourceDraftRunID *int64          `json:"source_draft_run_id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // body is optional
	}
	var projectID sql.NullInt64
	_ = db.DB.QueryRow(`SELECT project_id FROM issues WHERE id=?`, issueID).Scan(&projectID)
	agentName := strings.TrimSpace(body.AgentName)
	if agentName != "" {
		if len(agentName) > agentNameMaxLen || !agentNamePattern.MatchString(agentName) || reservedAgentNames[agentName] {
			jsonError(w, "invalid agent_name", http.StatusBadRequest)
			return
		}
		if !projectID.Valid {
			jsonError(w, "agent_name requires a project issue", http.StatusBadRequest)
			return
		}
		// project_agents has no disabled/archive state; a present project-scoped
		// row is the active agent declaration.
		if getProjectAgentByProjectAndName(projectID.Int64, agentName) == nil {
			jsonError(w, "agent not found", http.StatusNotFound)
			return
		}
	}
	explicitAction := strings.TrimSpace(body.ActionKey) != ""
	action, ok := resolveAgentRunAction(body.ActionKey)
	if !ok {
		jsonError(w, "invalid action_key", http.StatusBadRequest)
		return
	}
	if isDraftAgentRunAction(action) {
		if body.SourceDraftRunID != nil {
			jsonError(w, "draft handoff requires a trusted local runner action", http.StatusBadRequest)
			return
		}
		implementDraftIssue(w, r, issueID, projectID, action, agentName, body.DeviceID, body.DeployTarget, body.Options)
		return
	}
	var sourceDraftRunID *int64
	if body.SourceDraftRunID != nil {
		draft, err := getAgentRunByID(*body.SourceDraftRunID)
		if err != nil || draft.IssueID != issueID || draft.Status != "drafted" || draft.RunMode != "draft" {
			jsonError(w, "source draft run not found", http.StatusBadRequest)
			return
		}
		if draft.FollowupRunID != nil {
			jsonError(w, "source draft already has a follow-up run", http.StatusConflict)
			return
		}
		sourceDraftRunID = body.SourceDraftRunID
	}

	// The background reconciler is authoritative for stale runs. Running one
	// idempotent pass here preserves the historic immediate-retry behaviour, but
	// recovery no longer depends on a user clicking Implement again.
	_, _ = ReconcileStaleAgentRuns(r.Context(), time.Now().UTC(), AgentRunReconcilerConfigFromEnv())
	if active := activeRunForIssue(issueID); active != nil {
		jsonOK(w, active) // 200 (not 201): an existing active run is returned
		return
	}
	deviceID := strings.TrimSpace(body.DeviceID)
	if explicitAction && projectID.Valid && deviceID != "" && !deviceSupportsAgentAction(projectID.Int64, deviceID, action.ActionKey) {
		jsonError(w, "requested runner action is unavailable", http.StatusConflict)
		return
	}
	var requestedBy *int64
	if u := auth.GetUser(r); u != nil {
		requestedBy = &u.ID
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "could not create run", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO agent_runs(
			issue_id, project_id, device_id, requested_by, deploy_target,
			action_key, provider_kind, provider_id, provider_label, model, run_mode, agent_name, source_draft_run_id,
			status, delivery_instrumentation_version
		 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?, 'queued', 1)`,
		issueID, projectID, deviceID, requestedBy, strings.TrimSpace(body.DeployTarget),
		action.ActionKey, action.ProviderKind, action.ProviderID, action.ProviderLabel, action.Model, action.RunMode, agentName, sourceDraftRunID)
	if err != nil {
		_ = tx.Rollback()
		// Lost the unique-index race to a concurrent click — return its run (200).
		if existing := activeRunForIssue(issueID); existing != nil {
			jsonOK(w, existing)
			return
		}
		jsonError(w, "could not create run", http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()
	if sourceDraftRunID != nil {
		result, err := tx.ExecContext(r.Context(), `UPDATE agent_runs SET followup_run_id=? WHERE id=? AND followup_run_id IS NULL`, id, *sourceDraftRunID)
		if err != nil {
			jsonError(w, "could not link source draft", http.StatusInternalServerError)
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			jsonError(w, "source draft changed concurrently", http.StatusConflict)
			return
		}
	}
	store := deliveryStoreForRequest(r)
	effects := store.NewEffects()
	if _, err := store.BootstrapRunTx(r.Context(), tx, effects, delivery.RunBootstrap{IssueID: issueID,
		RunID: id, Mode: "implementation", Actor: deliveryUserActor(r), IdempotencyKey: fmt.Sprintf("run:%d", id)}); err != nil {
		log.Printf("agent run delivery bootstrap: issue=%d run=%d: %v", issueID, id, err)
		jsonError(w, "could not instrument run", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "could not create run", http.StatusInternalServerError)
		return
	}
	effects.Dispatch(r.Context())
	run, err := getAgentRunByID(id)
	if err != nil {
		jsonError(w, "run created but reload failed", http.StatusInternalServerError)
		return
	}
	// PAI-607: notify the project's online runners. The event carries the
	// run id (Rev) so a runner can GET /api/runs/{id} for the full detail;
	// Name is the issue key for human-readable logging.
	if projectID.Valid {
		sse.GlobalBroker().PublishProject(projectID.Int64, sse.Event{
			Type: "implement_requested",
			Name: agentRunIssueKey(issueID),
			Rev:  strconv.FormatInt(id, 10),
		})
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, run)
}

func implementDraftIssue(
	w http.ResponseWriter,
	r *http.Request,
	issueID int64,
	projectID sql.NullInt64,
	action agentRunAction,
	agentName string,
	deviceID string,
	deployTarget string,
	opts aiActionOptions,
) {
	if strings.TrimSpace(deviceID) != "" {
		jsonError(w, "draft mode does not use a local runner", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(deployTarget) != "" {
		jsonError(w, "draft mode cannot deploy", http.StatusBadRequest)
		return
	}
	if projectID.Valid && projectAIPolicyDisablesAction(loadProjectAIConfig(projectID.Int64).Policy, action) {
		jsonError(w, "draft provider is disabled by project AI policy", http.StatusConflict)
		return
	}

	// Keep the same active-run guard as shell-backed implement runs: a draft
	// should not run concurrently with a queued/running local implementation.
	_, _ = ReconcileStaleAgentRuns(r.Context(), time.Now().UTC(), AgentRunReconcilerConfigFromEnv())
	if active := activeRunForIssue(issueID); active != nil {
		jsonOK(w, active)
		return
	}

	requestID := newAIRequestID()
	w.Header().Set(AIRequestIDHeader, requestID)
	settings, err := LoadAISettings()
	if err != nil {
		log.Printf("agent_draft: settings load failed")
		recordAICall(r.Context(), aiCallArgs{
			RequestID:  requestID,
			ActionKey:  action.ActionKey,
			Surface:    "issue",
			IssueID:    nullableInt64(issueID),
			ProjectID:  nullableSQLInt64(projectID),
			Outcome:    outcomeCfgLoadFail,
			ErrorClass: "settings_load",
		})
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(settings.Provider) != action.ProviderID || !settings.AvailableForOptimize() {
		recordAICall(r.Context(), aiCallArgs{
			RequestID:  requestID,
			ActionKey:  action.ActionKey,
			Surface:    "issue",
			IssueID:    nullableInt64(issueID),
			ProjectID:  nullableSQLInt64(projectID),
			Provider:   settings.Provider,
			Model:      settings.Model,
			Outcome:    outcomeUnconfigured,
			ErrorClass: "settings_unavailable",
		})
		jsonError(w, "draft provider is not configured", http.StatusServiceUnavailable)
		return
	}

	var requestedBy *int64
	var userID int64
	if u := auth.GetUser(r); u != nil {
		userID = u.ID
		requestedBy = &u.ID
		if ok, _, _, bypass := CheckUsageCap(userID, auth.IsAdmin(u)); !ok {
			recordAICall(r.Context(), aiCallArgs{
				RequestID:  requestID,
				UserID:     requestedBy,
				ActionKey:  action.ActionKey,
				Surface:    "issue",
				IssueID:    nullableInt64(issueID),
				ProjectID:  nullableSQLInt64(projectID),
				Provider:   settings.Provider,
				Model:      settings.Model,
				Outcome:    outcomeBadRequest,
				ErrorClass: "usage_cap",
			})
			jsonError(w, "Daily AI limit reached. Ask an admin to raise the cap.", http.StatusTooManyRequests)
			return
		} else if bypass {
			w.Header().Set("X-AI-Over-Cap", "true")
		}
	}

	provider, err := ai.Get(action.ProviderID)
	if err != nil {
		log.Printf("agent_draft: provider %q not registered", action.ProviderID)
		recordAICall(r.Context(), aiCallArgs{
			RequestID:  requestID,
			UserID:     requestedBy,
			ActionKey:  action.ActionKey,
			Surface:    "issue",
			IssueID:    nullableInt64(issueID),
			ProjectID:  nullableSQLInt64(projectID),
			Provider:   action.ProviderID,
			Model:      settings.Model,
			Outcome:    outcomeProviderMissing,
			ErrorClass: "provider_missing",
		})
		jsonError(w, "AI provider unavailable", http.StatusServiceUnavailable)
		return
	}

	projectIDPtr := nullableSQLInt64(projectID)
	resolvedOptions, optionsErr := resolveAIActionOptions(settings, action.ActionKey, opts, projectIDPtr)
	if optionsErr != nil {
		callArgs := aiCallArgs{
			RequestID:  requestID,
			UserID:     requestedBy,
			ActionKey:  action.ActionKey,
			Surface:    "issue",
			IssueID:    nullableInt64(issueID),
			ProjectID:  projectIDPtr,
			Provider:   settings.Provider,
			Model:      settings.Model,
			Outcome:    outcomeBadRequest,
			ErrorClass: "options_invalid",
		}
		resolvedOptions.applyToAICallArgs(&callArgs)
		recordAICall(r.Context(), callArgs)
		jsonError(w, optionsErr.msg, optionsErr.status)
		return
	}

	issueData, ctxErr := loadOptimizeContext(r, issueID, "description")
	if ctxErr != nil {
		var ue *userError
		if errors.As(ctxErr, &ue) {
			recordAICall(r.Context(), aiCallArgs{
				RequestID:  requestID,
				UserID:     requestedBy,
				ActionKey:  action.ActionKey,
				Surface:    "issue",
				IssueID:    nullableInt64(issueID),
				ProjectID:  projectIDPtr,
				Provider:   settings.Provider,
				Model:      settings.Model,
				Outcome:    outcomeDenied,
				ErrorClass: "context_denied",
			})
			jsonError(w, ue.msg, ue.status)
			return
		}
		log.Printf("agent_draft: context load failed")
		recordAICall(r.Context(), aiCallArgs{
			RequestID:  requestID,
			UserID:     requestedBy,
			ActionKey:  action.ActionKey,
			Surface:    "issue",
			IssueID:    nullableInt64(issueID),
			ProjectID:  projectIDPtr,
			Provider:   settings.Provider,
			Model:      settings.Model,
			Outcome:    outcomeCtxFail,
			ErrorClass: "context_load",
		})
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	ax := &aiActionContext{
		Ctx:       r.Context(),
		Request:   r,
		UserID:    userID,
		IsAdmin:   auth.IsAdmin(auth.GetUser(r)),
		Provider:  provider,
		Settings:  settings,
		IssueData: issueData,
		IssueID:   issueID,
		Field:     "description",
		Text:      issueData.Description,
		Options:   resolvedOptions,
		DB:        db.DB,
	}
	if err := assembleAIContextPack(r.Context(), ax, projectIDPtr); err != nil {
		var ue *userError
		if errors.As(err, &ue) {
			recordAICall(r.Context(), aiCallArgs{
				RequestID:  requestID,
				UserID:     requestedBy,
				ActionKey:  action.ActionKey,
				Surface:    "issue",
				IssueID:    nullableInt64(issueID),
				ProjectID:  projectIDPtr,
				Provider:   settings.Provider,
				Model:      settings.Model,
				Outcome:    outcomeBadRequest,
				ErrorClass: "context_pack",
			})
			jsonError(w, ue.msg, ue.status)
			return
		}
		log.Printf("agent_draft: context pack failed")
		recordAICall(r.Context(), aiCallArgs{
			RequestID:  requestID,
			UserID:     requestedBy,
			ActionKey:  action.ActionKey,
			Surface:    "issue",
			IssueID:    nullableInt64(issueID),
			ProjectID:  projectIDPtr,
			Provider:   settings.Provider,
			Model:      settings.Model,
			Outcome:    outcomeCtxFail,
			ErrorClass: "context_pack",
		})
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	contextSourcesJSON := marshalAIContextSources(ax.Options.ContextSources)
	createTx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "could not create run", http.StatusInternalServerError)
		return
	}
	defer createTx.Rollback()
	res, err := createTx.ExecContext(r.Context(),
		`INSERT INTO agent_runs(
			issue_id, project_id, requested_by,
			action_key, provider_kind, provider_id, provider_label, model, run_mode,
			profile_id, effort, prompt_preset_ref, context_pack, context_truncated, context_sources_json,
			agent_name, status, started_at, delivery_instrumentation_version
		 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'running',datetime('now'),1)`,
		issueID, projectID, requestedBy,
		action.ActionKey, action.ProviderKind, action.ProviderID, action.ProviderLabel, ax.Options.Model, action.RunMode,
		ax.Options.ProfileID, ax.Options.Effort, ax.Options.PromptPresetRef, ax.Options.ContextPack, boolToSQLite(ax.Options.ContextTruncated), contextSourcesJSON,
		agentName)
	if err != nil {
		_ = createTx.Rollback()
		if existing := activeRunForIssue(issueID); existing != nil {
			jsonOK(w, existing)
			return
		}
		log.Printf("agent_draft: create run failed")
		jsonError(w, "could not create run", http.StatusInternalServerError)
		return
	}
	runID, _ := res.LastInsertId()
	store := deliveryStoreForRequest(r)
	createEffects := store.NewEffects()
	if _, err := store.BootstrapRunTx(r.Context(), createTx, createEffects, delivery.RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "draft", Actor: deliveryUserActor(r), IdempotencyKey: fmt.Sprintf("run:%d", runID)}); err != nil {
		log.Printf("agent_draft: delivery bootstrap failed: %v", err)
		jsonError(w, "could not instrument run", http.StatusInternalServerError)
		return
	}
	if err := createTx.Commit(); err != nil {
		jsonError(w, "could not create run", http.StatusInternalServerError)
		return
	}
	createEffects.Dispatch(r.Context())

	notes := loadIssueNotes(issueID)
	systemPrompt := applyAIPromptPreset(draftRunSystemPrompt, ax)
	userPrompt := buildDraftRunUserPrompt(issueData, notes, draftRunAgentContext(projectID, agentName))

	callCtx, cancel := context.WithTimeout(r.Context(), optimizeRequestTimeout)
	defer cancel()
	t0 := time.Now()
	resp, err := provider.Optimize(callCtx, ai.OptimizeRequest{
		Model:           ax.Options.Model,
		APIKey:          settings.APIKey,
		BaseURL:         settings.BaseURL,
		SystemPrompt:    systemPrompt,
		UserPrompt:      aiUserPromptWithContext(ax, userPrompt),
		MaxOutputTokens: 4000,
	})
	latency := time.Since(t0)

	model := strings.TrimSpace(resp.Model)
	if model == "" {
		model = ax.Options.Model
	}
	callArgs := aiCallArgs{
		RequestID:        requestID,
		UserID:           requestedBy,
		ActionKey:        action.ActionKey,
		Surface:          "issue",
		IssueID:          nullableInt64(issueID),
		ProjectID:        projectIDPtr,
		Provider:         action.ProviderID,
		Model:            model,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		LatencyMs:        latency.Milliseconds(),
	}
	ax.Options.applyToAICallArgs(&callArgs)
	if err != nil {
		outcome, errorClass := classifyDraftProviderError(err)
		callArgs.Outcome = outcome
		callArgs.ErrorClass = errorClass
		recordAICall(r.Context(), callArgs)
		if err := markAgentRunFailed(r, runID, safeDraftProviderError(err)); err != nil {
			jsonError(w, "run update failed", http.StatusInternalServerError)
			return
		}
		run, reloadErr := getAgentRunByID(runID)
		if reloadErr != nil {
			jsonError(w, "run created but reload failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		jsonOK(w, run)
		return
	}

	draft := strings.TrimSpace(ai.StripFenceEcho(resp.Text))
	if draft == "" {
		callArgs.Outcome = outcomeFailUpstream
		callArgs.ErrorClass = "empty_response"
		recordAICall(r.Context(), callArgs)
		if err := markAgentRunFailed(r, runID, "AI provider returned an empty draft"); err != nil {
			jsonError(w, "run update failed", http.StatusInternalServerError)
			return
		}
		run, reloadErr := getAgentRunByID(runID)
		if reloadErr != nil {
			jsonError(w, "run created but reload failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		jsonOK(w, run)
		return
	}

	testsSummary := "AI draft generated; no local tests were run and no deployment was attempted."
	resultTx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "run update failed", http.StatusInternalServerError)
		return
	}
	defer resultTx.Rollback()
	updateResult, err := resultTx.ExecContext(r.Context(),
		`UPDATE agent_runs
		    SET status='drafted',
		        tests_summary=?,
		        model=?,
		        prompt_tokens=?,
		        completion_tokens=?,
		        finish_reason=?,
		        context_truncated=?,
		        context_sources_json=?,
		        finished_at=datetime('now')
		  WHERE id=? AND status='running'`,
		testsSummary, model, resp.PromptTokens, resp.CompletionTokens, strings.ToLower(resp.FinishReason),
		boolToSQLite(ax.Options.ContextTruncated), contextSourcesJSON, runID)
	if err != nil {
		log.Printf("agent_draft: mark drafted failed")
		jsonError(w, "run update failed", http.StatusInternalServerError)
		return
	}
	if n, _ := updateResult.RowsAffected(); n != 1 {
		jsonError(w, "run changed concurrently", http.StatusConflict)
		return
	}
	resultStore := deliveryStoreForRequest(r)
	resultEffects := resultStore.NewEffects()
	if err := resultStore.NormalizeRunTx(r.Context(), resultTx, resultEffects, delivery.RunNormalization{RunID: runID,
		Status: "drafted", IdempotencyKey: fmt.Sprintf("run:%d:drafted", runID)}); err != nil {
		log.Printf("agent_draft: delivery normalization failed: %v", err)
		jsonError(w, "run update failed", http.StatusInternalServerError)
		return
	}
	if err := resultTx.Commit(); err != nil {
		jsonError(w, "run update failed", http.StatusInternalServerError)
		return
	}
	resultEffects.Dispatch(r.Context())
	postAgentRunDraftComment(issueID, requestedBy, runID, action.ProviderLabel, model, draft, ax.Options, agentName)
	callArgs.Outcome = outcomeOK
	recordAICall(r.Context(), callArgs)
	if userID > 0 {
		RecordUsage(userID, resp.PromptTokens, resp.CompletionTokens)
	}

	run, err := getAgentRunByID(runID)
	if err != nil {
		jsonError(w, "run created but reload failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, run)
}

const draftRunSystemPrompt = `You are an AI implementation draft provider inside PAIMOS.

Your job is to produce a careful markdown draft for a software issue: implementation approach, patch guidance, review notes, risks, and suggested test commands.

Hard rules:
- Draft only. Do not claim that you edited files, ran commands, ran tests, opened a PR, deployed, or changed the repository.
- Do not ask for, infer, print, or preserve secrets, API keys, tokens, passwords, cookies, private SSH material, or local environment values.
- Respect the selected execution profile, effort, prompt preset, context pack, and selected project-agent guidance.
- If important context is missing, say what is missing and provide the safest next step.
- Return markdown only, with no surrounding code fence.`

func nullableSQLInt64(v sql.NullInt64) *int64 {
	if !v.Valid || v.Int64 <= 0 {
		return nil
	}
	out := v.Int64
	return &out
}

func boolToSQLite(v bool) int {
	if v {
		return 1
	}
	return 0
}

func marshalAIContextSources(sources []aiContextSource) string {
	if len(sources) == 0 {
		return ""
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return ""
	}
	return string(raw)
}

func loadIssueNotes(issueID int64) string {
	var notes string
	_ = db.DB.QueryRow(`SELECT COALESCE(notes, '') FROM issues WHERE id=?`, issueID).Scan(&notes)
	return notes
}

func buildDraftRunUserPrompt(issue ai.Context, notes, agentContext string) string {
	var b strings.Builder
	b.WriteString("Create an implementation draft for this PAIMOS issue.\n\n")
	if strings.TrimSpace(issue.IssueKey) != "" {
		b.WriteString("Issue: ")
		b.WriteString(issue.IssueKey)
		b.WriteString("\n")
	}
	if strings.TrimSpace(issue.IssueType) != "" {
		b.WriteString("Type: ")
		b.WriteString(issue.IssueType)
		b.WriteString("\n")
	}
	if strings.TrimSpace(issue.IssueTitle) != "" {
		b.WriteString("Title: ")
		b.WriteString(issue.IssueTitle)
		b.WriteString("\n")
	}
	if strings.TrimSpace(issue.ProjectName) != "" {
		b.WriteString("Project: ")
		b.WriteString(issue.ProjectName)
		b.WriteString("\n")
	}
	if strings.TrimSpace(issue.ParentEpic) != "" {
		b.WriteString("Parent: ")
		b.WriteString(issue.ParentEpic)
		b.WriteString("\n")
	}
	if strings.TrimSpace(issue.Description) != "" {
		b.WriteString("\nDescription:\n")
		b.WriteString(issue.Description)
		b.WriteString("\n")
	}
	if strings.TrimSpace(issue.AcceptanceCriteria) != "" {
		b.WriteString("\nAcceptance criteria:\n")
		b.WriteString(issue.AcceptanceCriteria)
		b.WriteString("\n")
	}
	if strings.TrimSpace(notes) != "" {
		b.WriteString("\nNotes:\n")
		b.WriteString(notes)
		b.WriteString("\n")
	}
	if strings.TrimSpace(agentContext) != "" {
		b.WriteString("\nSelected project-agent guidance:\n")
		b.WriteString(agentContext)
		b.WriteString("\n")
	}
	b.WriteString("\nReturn:\n")
	b.WriteString("- a concise implementation plan\n")
	b.WriteString("- likely files/modules to inspect or change\n")
	b.WriteString("- edge cases and safety checks\n")
	b.WriteString("- suggested test commands\n")
	b.WriteString("- any open questions or assumptions\n")
	b.WriteString("\nDo not claim completion. This is a draft artifact only.")
	return b.String()
}

func draftRunAgentContext(projectID sql.NullInt64, agentName string) string {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" || !projectID.Valid {
		return ""
	}
	agent := getProjectAgentByProjectAndName(projectID.Int64, agentName)
	if agent == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Agent: ")
	b.WriteString(agent.Name)
	b.WriteString("\n")
	if strings.TrimSpace(agent.Description) != "" {
		b.WriteString("Description: ")
		b.WriteString(truncateBytes(agent.Description, 500))
		b.WriteString("\n")
	}
	if len(agent.LaneTags) > 0 {
		b.WriteString("Lane tags: ")
		b.WriteString(strings.Join(agent.LaneTags, ", "))
		b.WriteString("\n")
	}
	if strings.TrimSpace(agent.Body) != "" {
		b.WriteString("Body excerpt:\n")
		b.WriteString(truncateBytes(agent.Body, 1400))
		b.WriteString("\n")
	}
	if len(agent.BootstrapSteps) > 0 {
		b.WriteString("Bootstrap steps, commands intentionally omitted:\n")
		for _, step := range agent.BootstrapSteps {
			title := strings.TrimSpace(step.Title)
			if title == "" {
				title = "Untitled step"
			}
			b.WriteString("- ")
			b.WriteString(title)
			if strings.TrimSpace(step.Rationale) != "" {
				b.WriteString(": ")
				b.WriteString(truncateBytes(step.Rationale, 220))
			}
			b.WriteString("\n")
		}
	}
	if len(agent.NonNegotiableRules) > 0 {
		b.WriteString("Non-negotiable rules:\n")
		for _, rule := range agent.NonNegotiableRules {
			title := strings.TrimSpace(rule.Title)
			if title == "" {
				title = "Rule"
			}
			b.WriteString("- ")
			b.WriteString(title)
			if strings.TrimSpace(rule.Body) != "" {
				b.WriteString(": ")
				b.WriteString(truncateBytes(rule.Body, 320))
			}
			if strings.TrimSpace(rule.MemoryRef) != "" {
				b.WriteString(" (")
				b.WriteString(rule.MemoryRef)
				b.WriteString(")")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func classifyDraftProviderError(err error) (outcome string, errorClass string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return outcomeFailTimeout, "timeout"
	case errors.Is(err, ai.ErrProviderUnconfigured):
		return outcomeUnconfigured, "provider_unconfigured"
	case errors.Is(err, ai.ErrProviderUnavailable):
		return outcomeFailUpstream, "provider_unavailable"
	default:
		return outcomeFailUpstream, "upstream"
	}
}

func safeDraftProviderError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "AI provider timed out"
	case errors.Is(err, ai.ErrProviderUnconfigured):
		return "AI provider is not configured for draft mode"
	case errors.Is(err, ai.ErrProviderUnavailable):
		return "AI provider temporarily unavailable"
	default:
		return "AI provider returned an error"
	}
}

func markAgentRunFailed(r *http.Request, runID int64, msg string) error {
	ctx := r.Context()
	if strings.TrimSpace(msg) == "" {
		msg = "AI draft failed"
	}
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE agent_runs SET status='failed', error=?, finished_at=datetime('now') WHERE id=? AND status='running'`, msg, runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("draft run changed concurrently")
	}
	store := deliveryStoreForRequest(r)
	effects := store.NewEffects()
	if err := store.NormalizeRunTx(ctx, tx, effects, delivery.RunNormalization{RunID: runID, Status: "failed",
		IdempotencyKey: fmt.Sprintf("run:%d:failed", runID)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	effects.Dispatch(ctx)
	return nil
}

func postAgentRunDraftComment(
	issueID int64,
	authorID *int64,
	runID int64,
	providerLabel string,
	model string,
	draft string,
	opts resolvedAIActionOptions,
	agentName string,
) {
	body := agentRunDraftCommentBody(runID, providerLabel, model, draft, opts, agentName)
	if body == "" {
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO comments(issue_id, author_id, body, visibility) VALUES(?, ?, ?, ?)`,
		issueID, authorID, body, CommentVisibilityInternal); err != nil {
		log.Printf("agent draft comment: issue=%d run=%d: %v", issueID, runID, err)
	}
}

func agentRunDraftCommentBody(
	runID int64,
	providerLabel string,
	model string,
	draft string,
	opts resolvedAIActionOptions,
	agentName string,
) string {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return ""
	}
	providerLabel = firstNonEmptyOption(providerLabel, "AI Draft")
	model = firstNonEmptyOption(model, opts.Model)
	var b strings.Builder
	b.WriteString("## AI draft from ")
	b.WriteString(providerLabel)
	b.WriteString("\n\n")
	b.WriteString(draft)
	b.WriteString("\n\n---\n")
	b.WriteString("Provenance: run #")
	b.WriteString(strconv.FormatInt(runID, 10))
	b.WriteString("; provider ")
	b.WriteString(providerLabel)
	if model != "" {
		b.WriteString("; model `")
		b.WriteString(markdownInline(model))
		b.WriteString("`")
	}
	if opts.ProfileID != "" {
		b.WriteString("; profile `")
		b.WriteString(markdownInline(opts.ProfileID))
		b.WriteString("`")
	}
	if opts.Effort != "" {
		b.WriteString("; effort `")
		b.WriteString(markdownInline(opts.Effort))
		b.WriteString("`")
	}
	if opts.PromptPresetRef != "" {
		b.WriteString("; prompt `")
		b.WriteString(markdownInline(opts.PromptPresetRef))
		b.WriteString("`")
	}
	if opts.ContextPack != "" {
		b.WriteString("; context `")
		b.WriteString(markdownInline(opts.ContextPack))
		b.WriteString("`")
	}
	if strings.TrimSpace(agentName) != "" {
		b.WriteString("; agent `")
		b.WriteString(markdownInline(agentName))
		b.WriteString("`")
	}
	b.WriteString("; draft only, no repository changes, no local tests, no deploy.")
	return b.String()
}

func markdownInline(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

// activeRunForIssue returns the issue's current active (queued/running) run, or
// nil if there is none.
func activeRunForIssue(issueID int64) *AgentRun {
	var id int64
	if err := db.DB.QueryRow(
		`SELECT id FROM agent_runs WHERE issue_id=? AND status IN ('queued','running') ORDER BY id DESC LIMIT 1`,
		issueID).Scan(&id); err != nil {
		return nil
	}
	run, err := getAgentRunByID(id)
	if err != nil {
		return nil
	}
	return run
}

// agentRunIssueKey returns the "PAI-123" key for an issue id (best-effort;
// empty string if the lookup fails).
func agentRunIssueKey(issueID int64) string {
	var key string
	_ = db.DB.QueryRow(
		`SELECT p.key || '-' || i.issue_number FROM issues i JOIN projects p ON p.id = i.project_id WHERE i.id = ?`,
		issueID).Scan(&key)
	return key
}

// ProjectRunner is an online, implement-capable runner for a project.
type ProjectRunner struct {
	UserID   int64                  `json:"user_id"`
	DeviceID string                 `json:"device_id"`
	LastSeen string                 `json:"last_seen"`
	Actions  []sse.ActionCapability `json:"actions"`
}

// ListProjectRunners — GET /api/projects/{id}/runners (project-view gated).
// Intersects the broker's live subscribers for the project with the
// auto-watch rows that advertised implement-capability, so the UI can offer
// a device picker of runners that can actually take an "Implement this" job.
func ListProjectRunners(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(r)
	if !ok {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	out := make([]ProjectRunner, 0, 4)
	seen := map[string]bool{}
	for _, p := range sse.GlobalBroker().ProjectSubscribers(projectID) {
		// Capability is per live connection (PAI-605 M8): a runner connection
		// advertises it; a browser tab on the same device does not, and neither
		// masks the other. Dedup multiple connections of the same runner device,
		// merging action capabilities when one device advertises several actions.
		if !p.CanImplement {
			continue
		}
		key := fmt.Sprintf("%d/%s", p.UserID, p.DeviceID)
		actions := presenceActions(p)
		if seen[key] {
			for i := range out {
				if out[i].UserID == p.UserID && out[i].DeviceID == p.DeviceID {
					out[i].Actions = mergeActionCapabilities(out[i].Actions, actions)
					break
				}
			}
			continue
		}
		seen[key] = true
		var lastSeen string
		_ = db.DB.QueryRow(
			`SELECT updated_at FROM auto_watch_subscriptions WHERE user_id=? AND device_id=? AND project_id=?`,
			p.UserID, p.DeviceID, projectID).Scan(&lastSeen)
		out = append(out, ProjectRunner{UserID: p.UserID, DeviceID: p.DeviceID, LastSeen: lastSeen, Actions: actions})
	}
	jsonOK(w, map[string]any{"runners": out})
}

func mergeActionCapabilities(existing, next []sse.ActionCapability) []sse.ActionCapability {
	out := append([]sse.ActionCapability(nil), existing...)
	seen := map[string]bool{}
	for _, action := range out {
		seen[action.ActionKey] = true
	}
	for _, action := range next {
		if !seen[action.ActionKey] {
			out = append(out, action)
			seen[action.ActionKey] = true
		}
	}
	return out
}

func deviceSupportsAgentAction(projectID int64, deviceID, actionKey string) bool {
	for _, p := range sse.GlobalBroker().ProjectSubscribers(projectID) {
		if p.DeviceID != deviceID || !p.CanImplement {
			continue
		}
		if actionCapabilitiesContain(presenceActions(p), actionKey) {
			return true
		}
	}
	return false
}

// ListProjectRuns — GET /api/projects/{id}/runs?status=queued (project-view
// gated). The runner uses it on connect (and after each job) to drain runs it
// missed while offline or busy, or that a server restart orphaned (PAI-605 M1).
func ListProjectRuns(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromRequest(r)
	if !ok {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	query := `SELECT ` + agentRunCols + ` FROM agent_runs WHERE project_id=?`
	args := []any{projectID}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if !agentRunStatuses[status] {
			jsonError(w, "invalid status", http.StatusBadRequest)
			return
		}
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY id ASC LIMIT 200`
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := make([]*AgentRun, 0, 8)
	for rows.Next() {
		ar, err := scanAgentRun(rows)
		if err != nil {
			continue
		}
		out = append(out, ar)
	}
	jsonOK(w, map[string]any{"runs": out})
}

// ListIssueRuns — GET /api/issues/{id}/runs (RequireIssueAccess). Newest first.
func ListIssueRuns(w http.ResponseWriter, r *http.Request) {
	issueID, ok := resolveIssueIDFromRequest(r)
	if !ok {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}
	rows, err := db.DB.Query(`SELECT `+agentRunCols+` FROM agent_runs WHERE issue_id=? ORDER BY id DESC`, issueID)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := make([]*AgentRun, 0, 8)
	for rows.Next() {
		ar, err := scanAgentRun(rows)
		if err != nil {
			continue
		}
		out = append(out, ar)
	}
	jsonOK(w, map[string]any{"runs": out})
}

// GetAgentRun — GET /api/runs/{id}. Admin, requester, or project editor.
func GetAgentRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	run, err := getAgentRunByID(id)
	if err != nil {
		jsonError(w, "run not found", http.StatusNotFound)
		return
	}
	if !canReadAgentRun(r, run) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	jsonOK(w, run)
}

// PatchAgentRun — PATCH /api/runs/{id}. The local runner posts status
// transitions + the structured report. Requester or admin only. started_at is
// stamped on the first move to `running`; finished_at on any terminal status.
func PatchAgentRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	existing, err := getAgentRunByID(id)
	if err != nil {
		jsonError(w, "run not found", http.StatusNotFound)
		return
	}
	var body struct {
		Status                     *string `json:"status"`
		IfStatus                   *string `json:"if_status"` // optimistic claim guard (PAI-605 H3)
		DeviceID                   *string `json:"device_id"`
		ActionKey                  *string `json:"action_key"`
		Version                    *string `json:"version"`
		TestsSummary               *string `json:"tests_summary"`
		DeployTarget               *string `json:"deploy_target"`
		RepoURL                    *string `json:"repo_url"`
		BranchName                 *string `json:"branch_name"`
		CommitBaseSHA              *string `json:"commit_base_sha"`
		CommitSHA                  *string `json:"commit_sha"`
		LogAttachmentID            *int64  `json:"log_attachment_id"`
		Error                      *string `json:"error"`
		ExpectsSupervisorTelemetry *bool   `json:"expects_supervisor_telemetry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}

	// Validate the status value first (bad input is a 400 regardless of run state).
	if body.Status != nil && !agentRunStatuses[strings.TrimSpace(*body.Status)] {
		jsonError(w, "invalid status", http.StatusBadRequest)
		return
	}
	if body.IfStatus != nil && !agentRunStatuses[strings.TrimSpace(*body.IfStatus)] {
		jsonError(w, "invalid if_status", http.StatusBadRequest)
		return
	}
	if body.ActionKey != nil {
		actionKey := strings.TrimSpace(*body.ActionKey)
		if actionKey == "" || actionKey != existing.ActionKey {
			jsonError(w, "run action cannot be changed", http.StatusConflict)
			return
		}
	}
	claimAttempt := false
	if body.Status != nil && body.IfStatus != nil {
		claimAttempt = existing.Status == "queued" &&
			strings.TrimSpace(*body.Status) == "running" &&
			strings.TrimSpace(*body.IfStatus) == "queued"
	}
	claimDeviceID := ""
	if claimAttempt && body.DeviceID != nil {
		claimDeviceID = strings.TrimSpace(*body.DeviceID)
		if claimDeviceID == "" || len(claimDeviceID) > deviceIDMaxLen {
			jsonError(w, "invalid device_id", http.StatusBadRequest)
			return
		}
	}
	if !canPatchAgentRun(r, existing, claimAttempt, claimDeviceID) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	exactLifecycleReplay := body.Status != nil && strings.TrimSpace(*body.Status) == existing.Status
	exactLifecycleReplay = exactLifecycleReplay && (body.DeviceID == nil || strings.TrimSpace(*body.DeviceID) == existing.DeviceID)
	exactLifecycleReplay = exactLifecycleReplay && (body.ActionKey == nil || strings.TrimSpace(*body.ActionKey) == existing.ActionKey)
	exactLifecycleReplay = exactLifecycleReplay && (body.Version == nil || strings.TrimSpace(*body.Version) == existing.Version)
	exactLifecycleReplay = exactLifecycleReplay && (body.TestsSummary == nil || (existing.TestsSummary != nil && *body.TestsSummary == *existing.TestsSummary))
	exactLifecycleReplay = exactLifecycleReplay && (body.DeployTarget == nil || strings.TrimSpace(*body.DeployTarget) == existing.DeployTarget)
	exactLifecycleReplay = exactLifecycleReplay && (body.RepoURL == nil || strings.TrimSpace(*body.RepoURL) == existing.RepoURL)
	exactLifecycleReplay = exactLifecycleReplay && (body.BranchName == nil || strings.TrimSpace(*body.BranchName) == existing.BranchName)
	exactLifecycleReplay = exactLifecycleReplay && (body.CommitBaseSHA == nil || strings.ToLower(strings.TrimSpace(*body.CommitBaseSHA)) == existing.CommitBaseSHA)
	exactLifecycleReplay = exactLifecycleReplay && (body.CommitSHA == nil || strings.ToLower(strings.TrimSpace(*body.CommitSHA)) == existing.CommitSHA)
	exactLifecycleReplay = exactLifecycleReplay && (body.LogAttachmentID == nil || (existing.LogAttachmentID != nil && *body.LogAttachmentID == *existing.LogAttachmentID))
	exactLifecycleReplay = exactLifecycleReplay && (body.Error == nil || *body.Error == existing.Error)
	exactLifecycleReplay = exactLifecycleReplay && (body.ExpectsSupervisorTelemetry == nil || *body.ExpectsSupervisorTelemetry == existing.ExpectsSupervisorTelemetry)
	if agentRunTelemetryIsTerminal(existing.Status) && exactLifecycleReplay {
		jsonOK(w, existing)
		return
	}
	if body.ExpectsSupervisorTelemetry != nil && (!claimAttempt || !*body.ExpectsSupervisorTelemetry) {
		jsonError(w, "expects_supervisor_telemetry can only be enabled when claiming a queued run", http.StatusConflict)
		return
	}
	// Audit: a terminal run is immutable — reject ANY update (a status move out of
	// terminal AND non-status field edits), so the historical record can't be
	// rewritten after the fact. An exact lifecycle transport replay returns the
	// original row: its delivery normalization committed in the same transaction,
	// so this adds neither a domain event nor a change hint.
	if agentRunIsTerminal(existing.Status) {
		jsonError(w, "run is already in a terminal status", http.StatusConflict)
		return
	}

	sets := make([]string, 0, 8)
	args := make([]any, 0, 8)
	statusChanged := false
	newStatus := existing.Status
	if body.Status != nil {
		s := strings.TrimSpace(*body.Status)
		if !agentRunStatuses[s] {
			jsonError(w, "invalid status", http.StatusBadRequest)
			return
		}
		// Audit: enforce the lifecycle — a status change must follow a legal edge,
		// so a run can't jump (e.g.) queued→deployed with a fabricated version.
		// existing.Status is non-terminal here (checked above).
		if s != existing.Status && !isLegalRunTransition(existing.Status, s) {
			jsonError(w, "illegal status transition", http.StatusConflict)
			return
		}
		statusChanged = s != existing.Status
		newStatus = s
		sets = append(sets, "status=?")
		args = append(args, s)
		if s == "running" && existing.StartedAt == nil {
			sets = append(sets, "started_at=datetime('now')")
		}
		if s == "running" && existing.Status == "queued" && existing.ClaimedBy == nil {
			if u := auth.GetUser(r); u != nil {
				sets = append(sets, "claimed_by=?")
				args = append(args, u.ID)
			}
		}
		if agentRunTelemetryIsTerminal(s) {
			sets = append(sets, "finished_at=datetime('now')")
		}
	}
	if body.TestsSummary != nil && len(*body.TestsSummary) > agentRunTestsSummaryMaxBytes {
		jsonError(w, fmt.Sprintf("tests_summary must be at most %d bytes", agentRunTestsSummaryMaxBytes), http.StatusBadRequest)
		return
	}
	if newStatus == "tests_passed" || newStatus == "tests_failed" {
		evidence := existing.TestsSummary
		if body.TestsSummary != nil {
			evidence = body.TestsSummary
		}
		if evidence == nil || strings.TrimSpace(*evidence) == "" || len(*evidence) > agentRunTestsSummaryMaxBytes {
			jsonError(w, newStatus+" requires non-empty bounded tests_summary evidence", http.StatusConflict)
			return
		}
	}
	if newStatus == "completed" {
		evidence := existing.TestsSummary
		if body.TestsSummary != nil {
			evidence = body.TestsSummary
		}
		if evidence != nil && strings.TrimSpace(*evidence) != "" {
			jsonError(w, "completed means tests were not run and cannot carry tests_summary", http.StatusConflict)
			return
		}
	}
	if body.Version != nil {
		sets = append(sets, "version=?")
		args = append(args, strings.TrimSpace(*body.Version))
	}
	if body.TestsSummary != nil {
		sets = append(sets, "tests_summary=?")
		args = append(args, *body.TestsSummary)
	}
	if body.DeviceID != nil {
		d := strings.TrimSpace(*body.DeviceID)
		if d == "" || len(d) > deviceIDMaxLen {
			jsonError(w, "invalid device_id", http.StatusBadRequest)
			return
		}
		if existing.Status != "queued" || newStatus != "running" {
			jsonError(w, "device_id can only be stamped when claiming a queued run", http.StatusConflict)
			return
		}
		if existing.DeviceID != "" && existing.DeviceID != d {
			jsonError(w, "run is targeted to another device", http.StatusConflict)
			return
		}
		sets = append(sets, "device_id=?")
		args = append(args, d)
	}
	if body.DeployTarget != nil {
		sets = append(sets, "deploy_target=?")
		args = append(args, strings.TrimSpace(*body.DeployTarget))
	}
	if body.RepoURL != nil {
		v := strings.TrimSpace(*body.RepoURL)
		if v != "" && !validAgentRunRepoURL(v) {
			jsonError(w, "invalid repo_url", http.StatusBadRequest)
			return
		}
		sets = append(sets, "repo_url=?")
		args = append(args, v)
	}
	if body.BranchName != nil {
		v := strings.TrimSpace(*body.BranchName)
		if len(v) > 255 || strings.ContainsAny(v, "\r\n\x00") {
			jsonError(w, "invalid branch_name", http.StatusBadRequest)
			return
		}
		sets = append(sets, "branch_name=?")
		args = append(args, v)
	}
	for _, ref := range []struct {
		name  string
		value *string
	}{
		{"commit_base_sha", body.CommitBaseSHA},
		{"commit_sha", body.CommitSHA},
	} {
		if ref.value == nil {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(*ref.value))
		if v != "" && !agentRunCommitPattern.MatchString(v) {
			jsonError(w, "invalid "+ref.name, http.StatusBadRequest)
			return
		}
		sets = append(sets, ref.name+"=?")
		args = append(args, v)
	}
	if body.LogAttachmentID != nil {
		// Audit: only an attachment that belongs to this run's issue may be linked,
		// so a run can't carry a cross-issue attachment reference.
		if !attachmentBelongsToIssue(*body.LogAttachmentID, existing.IssueID) {
			jsonError(w, "log_attachment_id does not belong to this issue", http.StatusBadRequest)
			return
		}
		sets = append(sets, "log_attachment_id=?")
		args = append(args, *body.LogAttachmentID)
	}
	if body.Error != nil {
		sets = append(sets, "error=?")
		args = append(args, *body.Error)
	}
	if body.ExpectsSupervisorTelemetry != nil {
		sets = append(sets, "expects_supervisor_telemetry=1")
	}
	// Stamp the attributing agent/session if the runner forwarded them. A
	// selected project agent is already stored on creation and must not be
	// overwritten by the reporter's generic CLI attribution.
	if an := agentNameFromRequest(r); an != "" && strings.TrimSpace(existing.AgentName) == "" {
		sets = append(sets, "agent_name=?")
		args = append(args, an)
	}
	if sid := sessionIDFromRequest(r); sid != "" {
		sets = append(sets, "session_id=?")
		args = append(args, sid)
	}
	if len(sets) == 0 {
		jsonError(w, "no fields to update", http.StatusBadRequest)
		return
	}
	// PAI-605 H3/M4/L1: transition optimistically on the status we just read, so
	// two concurrent reporters — or a non-status edit racing a terminal transition
	// — cannot both win. RowsAffected==0 means someone changed it first; the loser
	// gets 409 and backs off.
	// #nosec G202 -- `sets` holds only hardcoded column-assignment fragments
	// (status=?, version=?, started_at=datetime('now'), …); every value is bound
	// via ? placeholders in args, so no user input enters the SQL string.
	// An explicit `if_status` (a runner claiming an open run sends
	// {status:running, if_status:queued}) is a compare-and-set: the update
	// applies only if the row is still in that status, so a second runner that
	// re-reads "running" still loses the claim. Otherwise the update is guarded on
	// the status we just read, including non-status-only updates.
	query := `UPDATE agent_runs SET ` + strings.Join(sets, ", ") + ` WHERE id=?`
	guardStatus := existing.Status
	if body.IfStatus != nil {
		guardStatus = strings.TrimSpace(*body.IfStatus)
	}
	query += ` AND status=?`
	args = append(args, id, guardStatus)
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(r.Context(), query, args...)
	if err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, "run changed concurrently (claim lost)", http.StatusConflict)
		return
	}
	store := deliveryStoreForRequest(r)
	effects := store.NewEffects()
	if existing.DeliveryInstrumentationVersion == 1 && statusChanged && agentRunTelemetryIsTerminal(newStatus) {
		if err := store.NormalizeRunTx(r.Context(), tx, effects, delivery.RunNormalization{RunID: id, Status: newStatus,
			IdempotencyKey: fmt.Sprintf("run:%d:%s", id, newStatus)}); err != nil {
			log.Printf("agent run delivery normalization: run=%d status=%s: %v", id, newStatus, err)
			if errors.Is(err, delivery.ErrUnauthorized) {
				jsonError(w, "forbidden", http.StatusForbidden)
				return
			}
			jsonError(w, "delivery normalization failed", http.StatusInternalServerError)
			return
		}
	} else if existing.DeliveryInstrumentationVersion == 1 {
		if err := store.RecordRunMutationChangeTx(r.Context(), tx, effects, id); err != nil {
			log.Printf("agent run delivery mutation hint: run=%d: %v", id, err)
			if errors.Is(err, delivery.ErrUnauthorized) {
				jsonError(w, "forbidden", http.StatusForbidden)
				return
			}
			jsonError(w, "delivery mutation failed", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	effects.Dispatch(r.Context())
	run, err := getAgentRunByID(id)
	if err != nil {
		jsonError(w, "reload failed", http.StatusInternalServerError)
		return
	}
	// PAI-609 + audit: post the report exactly once — gate on the status THIS
	// request set (newStatus), not the reloaded row a concurrent writer may have
	// advanced. Attribute the auto-comment to the ACTOR who reported, not the
	// run's requester, so a different user can't forge a comment as the requester.
	if statusChanged && agentRunIsReportable(newStatus) {
		var actor *int64
		if u := auth.GetUser(r); u != nil {
			actor = &u.ID
		}
		postAgentRunReport(run.IssueID, actor, run)
	}
	jsonOK(w, run)
}

// agentRunIsReportable reports whether a terminal status warrants a summary
// comment (cancelled/declined runs are intentionally silent).
func agentRunIsReportable(s string) bool {
	return s == "completed" || s == "deployed" || s == "tests_passed" || s == "tests_failed" || s == "failed"
}

// postAgentRunReport writes an internal issue comment summarizing a finished
// run (PAI-609). Attributed to the run's requester; best-effort.
func postAgentRunReport(issueID int64, authorID *int64, run *AgentRun) {
	body := agentRunReportBody(run)
	if body == "" {
		return
	}
	if _, err := db.DB.Exec(
		`INSERT INTO comments(issue_id, author_id, body, visibility) VALUES(?, ?, ?, ?)`,
		issueID, authorID, body, CommentVisibilityInternal); err != nil {
		log.Printf("agent run report comment: issue=%d run=%d: %v", issueID, run.ID, err)
	}
}

func agentRunReportBody(run *AgentRun) string {
	on := "an agent"
	if run.DeviceID != "" {
		on = "device " + run.DeviceID
	}
	at := ""
	if run.FinishedAt != nil {
		at = " at " + *run.FinishedAt
	}
	tests := ""
	if run.TestsSummary != nil && strings.TrimSpace(*run.TestsSummary) != "" {
		tests = " Tests: " + *run.TestsSummary + "."
	}
	code := agentRunCodeEvidenceSummary(run)
	switch run.Status {
	case "deployed":
		ver := ""
		if run.Version != "" {
			ver = " in v" + run.Version
		}
		target := ""
		if run.DeployTarget != "" {
			target = ", deployed to " + run.DeployTarget
		}
		return fmt.Sprintf("🤖 Implemented%s%s%s.%s%s (run #%d on %s)", ver, at, target, tests, code, run.ID, on)
	case "completed":
		ver := ""
		if run.Version != "" {
			ver = " (v" + run.Version + ")"
		}
		return fmt.Sprintf("🤖 Implemented%s%s. Tests were not run.%s (run #%d on %s)", at, ver, code, run.ID, on)
	case "tests_passed":
		ver := ""
		if run.Version != "" {
			ver = " (v" + run.Version + ")"
		}
		return fmt.Sprintf("🤖 Implemented%s%s.%s%s (run #%d on %s)", at, ver, tests, code, run.ID, on)
	case "tests_failed", "failed":
		reason := run.Error
		if strings.TrimSpace(reason) == "" {
			reason = "no detail provided"
		}
		return fmt.Sprintf("🤖 Run #%d %s%s: %s.%s (on %s)", run.ID, run.Status, at, reason, code, on)
	}
	return ""
}

var agentRunCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func validAgentRunRepoURL(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	u, err := url.ParseRequestURI(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func agentRunCodeEvidenceSummary(run *AgentRun) string {
	base := strings.TrimSpace(run.CommitBaseSHA)
	head := strings.TrimSpace(run.CommitSHA)
	if base == "" && head == "" {
		return " Commit evidence unavailable."
	}
	if base != "" && head == base {
		return " No commit produced."
	}
	short := func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	}
	if base != "" && head != "" {
		return fmt.Sprintf(" Code: `%s..%s`.", short(base), short(head))
	}
	return fmt.Sprintf(" Code: `%s`.", short(head))
}
