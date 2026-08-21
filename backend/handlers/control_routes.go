// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/httpcontract"
	"github.com/inspr-at/paimos/backend/supervision"
)

const (
	controlSmallBodyLimit   = int64(1 << 10)
	controlCommandBodyLimit = int64(4 << 10)
)

type controlGrantDTO struct {
	GrantID     string                  `json:"grant_id"`
	Revision    int64                   `json:"revision"`
	DeliveryKey string                  `json:"delivery_key"`
	IssueKey    string                  `json:"issue_key"`
	Actions     []supervision.Action    `json:"actions"`
	Targets     []controlGrantTargetDTO `json:"targets"`
	ExpiresAt   time.Time               `json:"expires_at"`
}

type controlGrantTargetDTO struct {
	Action               supervision.Action       `json:"action"`
	RunID                int64                    `json:"run_id,omitempty"`
	RuntimeState         supervision.RuntimeState `json:"runtime_state,omitempty"`
	RuntimeRevision      int64                    `json:"runtime_revision,omitempty"`
	InputRequestID       string                   `json:"input_request_id,omitempty"`
	InputRequestRevision int64                    `json:"input_request_revision,omitempty"`
	InputKind            supervision.InputKind    `json:"input_kind,omitempty"`
	OptionCodes          []string                 `json:"option_codes,omitempty"`
}

type controlDisplayDTO struct {
	IssueKey        string                   `json:"issue_key"`
	DeliveryKey     string                   `json:"delivery_key"`
	Priority        string                   `json:"priority,omitempty"`
	RunID           int64                    `json:"run_id,omitempty"`
	InputKind       supervision.InputKind    `json:"input_kind,omitempty"`
	ChoiceOrdinal   int                      `json:"choice_ordinal,omitempty"`
	ChoiceCode      string                   `json:"choice_code,omitempty"`
	RuntimeState    supervision.RuntimeState `json:"runtime_state,omitempty"`
	RuntimeRevision int64                    `json:"runtime_revision,omitempty"`
}

type controlCommandDTO struct {
	CommandID         string                        `json:"command_id"`
	StatusRevision    int64                         `json:"status_revision"`
	Action            supervision.Action            `json:"action"`
	Status            supervision.CommandStatus     `json:"status"`
	Outcome           supervision.Outcome           `json:"outcome,omitempty"`
	Reason            supervision.SafeReason        `json:"reason,omitempty"`
	ChallengeTemplate supervision.ChallengeTemplate `json:"challenge_template"`
	Display           controlDisplayDTO             `json:"display"`
	ExpiresAt         time.Time                     `json:"expires_at"`
}

type controlTargetDTO struct {
	DeliveryID                 int64  `json:"delivery_id"`
	DeliveryKey                string `json:"delivery_key"`
	DeliveryRevision           int64  `json:"delivery_revision"`
	RootIssueID                int64  `json:"root_issue_id"`
	IssueRevision              int64  `json:"issue_revision"`
	AttemptID                  int64  `json:"attempt_id"`
	AttemptNumber              int64  `json:"attempt_number"`
	PlanRevision               int64  `json:"plan_revision"`
	StageKey                   string `json:"stage_key"`
	ExecutionNumber            int64  `json:"execution_number"`
	ExecutionStartStageEventID int64  `json:"execution_start_stage_event_id"`
	AuthorityEpoch             int64  `json:"authority_epoch"`
	AuthorityStageEventID      int64  `json:"authority_stage_event_id"`
	ReporterID                 int64  `json:"reporter_id"`
	RunID                      int64  `json:"run_id"`
}

type controlLeaseDTO struct {
	LeaseID     string               `json:"lease_id"`
	Revision    int64                `json:"revision"`
	DeliveryKey string               `json:"delivery_key"`
	IssueKey    string               `json:"issue_key"`
	Actions     []supervision.Action `json:"actions"`
	ExpiresAt   time.Time            `json:"expires_at"`
	Target      controlTargetDTO     `json:"target"`
}

type controlInputDTO struct {
	RequestID      string                          `json:"request_id"`
	Revision       int64                           `json:"revision"`
	Kind           supervision.InputKind           `json:"kind"`
	PromptTemplate supervision.InputPromptTemplate `json:"prompt_template"`
	OptionCodes    []string                        `json:"option_codes"`
	ExpiresAt      time.Time                       `json:"expires_at"`
	Target         controlTargetDTO                `json:"target"`
}

type controlEffectDTO struct {
	OutboxID        int64                         `json:"outbox_id"`
	CommandID       string                        `json:"command_id"`
	Action          supervision.Action            `json:"action"`
	EffectSequence  int64                         `json:"effect_sequence"`
	LeaseID         string                        `json:"lease_id"`
	LeaseRevision   int64                         `json:"lease_revision"`
	Target          controlTargetDTO              `json:"target"`
	InputRequestID  string                        `json:"input_request_id,omitempty"`
	InputRevision   int64                         `json:"input_request_revision,omitempty"`
	InputResponse   supervision.InputResponseKind `json:"input_response,omitempty"`
	ChoiceOrdinal   int                           `json:"choice_ordinal,omitempty"`
	ChoiceCode      string                        `json:"choice_code,omitempty"`
	RuntimeRevision int64                         `json:"runtime_revision,omitempty"`
}

type controlPullDTO struct {
	SnapshotHighWater int64              `json:"snapshot_high_water"`
	NextCursor        int64              `json:"next_cursor"`
	HasMore           bool               `json:"has_more"`
	Effects           []controlEffectDTO `json:"effects"`
}

func grantDTO(value supervision.GrantProjection) controlGrantDTO {
	actions := append([]supervision.Action(nil), value.Actions...)
	if actions == nil {
		actions = []supervision.Action{}
	}
	targets := make([]controlGrantTargetDTO, len(value.Targets))
	for index, target := range value.Targets {
		targets[index] = controlGrantTargetDTO{Action: target.Action, RunID: target.RunID,
			RuntimeState: target.RuntimeState, RuntimeRevision: target.RuntimeRevision,
			InputRequestID: target.InputRequestID, InputRequestRevision: target.InputRequestRevision,
			InputKind: target.InputKind, OptionCodes: append([]string(nil), target.OptionCodes...)}
	}
	return controlGrantDTO{GrantID: value.GrantID, Revision: value.Revision, DeliveryKey: value.DeliveryKey,
		IssueKey: value.IssueKey, Actions: actions, Targets: targets, ExpiresAt: value.ExpiresAt}
}

func commandDTO(value supervision.CommandProjection) controlCommandDTO {
	return controlCommandDTO{CommandID: value.CommandID, StatusRevision: value.StatusRevision, Action: value.Action,
		Status: value.Status, Outcome: value.Outcome, Reason: value.Reason, ChallengeTemplate: value.ChallengeTemplate,
		ExpiresAt: value.ExpiresAt, Display: controlDisplayDTO{IssueKey: value.Display.IssueKey,
			DeliveryKey: value.Display.DeliveryKey, Priority: value.Display.Priority,
			RunID: value.Display.RunID, InputKind: value.Display.InputKind, ChoiceOrdinal: value.Display.ChoiceOrdinal,
			ChoiceCode: value.Display.ChoiceCode, RuntimeState: value.Display.RuntimeState,
			RuntimeRevision: value.Display.RuntimeRevision}}
}

func targetDTO(value supervision.RunnerTarget) controlTargetDTO {
	return controlTargetDTO{DeliveryID: value.DeliveryID, DeliveryKey: value.DeliveryKey,
		DeliveryRevision: value.DeliveryRevision, RootIssueID: value.RootIssueID, IssueRevision: value.IssueRevision,
		AttemptID: value.AttemptID, AttemptNumber: value.AttemptNumber, PlanRevision: value.PlanRevision,
		StageKey: value.StageKey, ExecutionNumber: value.ExecutionNumber,
		ExecutionStartStageEventID: value.ExecutionStartStageEventID, AuthorityEpoch: value.AuthorityEpoch,
		AuthorityStageEventID: value.AuthorityStageEventID, ReporterID: value.ReporterID, RunID: value.RunID}
}

func leaseDTO(value supervision.LeaseProjection) controlLeaseDTO {
	actions := append([]supervision.Action(nil), value.Actions...)
	if actions == nil {
		actions = []supervision.Action{}
	}
	return controlLeaseDTO{LeaseID: value.LeaseID, Revision: value.Revision, DeliveryKey: value.DeliveryKey,
		IssueKey: value.IssueKey, Actions: actions, ExpiresAt: value.ExpiresAt, Target: targetDTO(value.Target)}
}

func inputDTO(value supervision.InputRequestProjection) controlInputDTO {
	options := append([]string(nil), value.OptionCodes...)
	if options == nil {
		options = []string{}
	}
	return controlInputDTO{RequestID: value.RequestID, Revision: value.Revision, Kind: value.Kind,
		PromptTemplate: value.PromptTemplate, OptionCodes: options, ExpiresAt: value.ExpiresAt,
		Target: targetDTO(value.Target)}
}

func effectDTO(value supervision.EffectProjection) controlEffectDTO {
	return controlEffectDTO{OutboxID: value.OutboxID, CommandID: value.CommandID, Action: value.Action,
		EffectSequence: value.EffectSequence, LeaseID: value.LeaseID, LeaseRevision: value.LeaseRevision,
		Target: targetDTO(value.Target), InputRequestID: value.InputRequestID, InputRevision: value.InputRevision,
		InputResponse: value.InputResponse, ChoiceOrdinal: value.ChoiceOrdinal, ChoiceCode: value.ChoiceCode,
		RuntimeRevision: value.RuntimeRevision}
}

func controlJSON(w http.ResponseWriter, value any) {
	SetControlCachePolicy(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeControlDomainError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, supervision.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, supervision.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, supervision.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, supervision.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, supervision.ErrUnavailable):
		status = http.StatusServiceUnavailable
	}
	code := string(supervision.ErrorCode(err))
	if code == "" || code == string(supervision.CodeInvariant) {
		switch {
		case errors.Is(err, supervision.ErrInvalid):
			code = string(supervision.CodeInvalidRequest)
		case errors.Is(err, supervision.ErrForbidden):
			code = string(supervision.CodeForbidden)
		case errors.Is(err, supervision.ErrNotFound):
			code = string(supervision.CodeTargetNotFound)
		case errors.Is(err, supervision.ErrConflict):
			code = string(supervision.CodeSemanticConflict)
		case errors.Is(err, supervision.ErrUnavailable):
			code = string(supervision.CodeDependencyUnavailable)
		default:
			code = string(supervision.CodeInvariant)
		}
	}
	detail := map[int]string{
		http.StatusBadRequest: "control request is invalid", http.StatusForbidden: "control operation is forbidden",
		http.StatusNotFound: "control target was not found", http.StatusConflict: "control target changed",
		http.StatusServiceUnavailable:  "control dependency is unavailable",
		http.StatusInternalServerError: "control invariant failed",
	}[status]
	requestID := trustedControlResponseRequestID(r)
	w.Header().Set(RequestIDHeader, requestID)
	SetControlCachePolicy(w)
	writeProblem(w, nil, ProblemDetails{Status: status, Code: code, Detail: detail, RequestID: requestID})
}

func writeControlNotFound(w http.ResponseWriter, r *http.Request) {
	writeControlDomainError(w, r, supervision.ErrNotFound)
}

func controlPrincipal(r *http.Request) (auth.Principal, bool) { return auth.GetPrincipal(r) }

func requireControlAuthority(scope string, apiKeyOnly bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := controlPrincipal(r)
			if !ok || !principal.HasScope(scope) || (apiKeyOnly && principal.Kind() != auth.PrincipalAPIKey) {
				writeControlDomainError(w, r, supervision.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MountAgentModeControlRoutes mounts only the four frozen operator families.
// The caller owns Agent Mode authentication, CSRF, must-change, and external
// concealment composition; this group adds the control scope boundary.
func MountAgentModeControlRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireControlAuthority(auth.ScopeAgentControlsWrite, false))
		r.Post("/deliveries/{deliveryKey}/control-capability-grants", IssueControlGrant)
		r.Get("/control-capability-grants/{controlID}", GetControlGrant)
		r.Post("/control-capability-grants/{controlID}", TransitionControlGrant)
		r.Post("/deliveries/{deliveryKey}/control-commands", CreateControlCommand)
		r.Get("/control-commands/{controlID}", GetControlCommand)
		r.Post("/control-commands/{controlID}", TransitionControlCommand)
	})
}

// MountRunnerControlRoutes mounts only the five frozen runner families. It
// must be called inside auth/CSRF/must-change/internal concealment middleware.
func MountRunnerControlRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireControlAuthority(auth.ScopeAgentControlsRunner, true))
		r.Post("/runs/{id}/control-capability-leases", IssueControlLease)
		r.Post("/control-capability-leases/{controlID}", TransitionControlLease)
		r.Post("/runs/{id}/input-requests", CreateControlInputRequest)
		r.Post("/runs/{id}/control-commands", PullControlCommands)
		r.Post("/control-commands/{controlID}", TransitionRunnerControlCommand)
	})
}

func IssueControlGrant(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	key, ok := decodeControlMutation(w, r, controlSmallBodyLimit, &body)
	if !ok {
		return
	}
	deliveryID, ok := resolveControlDelivery(r, chi.URLParam(r, "deliveryKey"))
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	if _, err := service.Reconcile(r.Context(), principal, supervision.ReconcileRequest{Mode: supervision.ReconcileActor, Limit: 32}); err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	projection, err := service.IssueActorGrant(r.Context(), principal, supervision.GrantIssueRequest{
		DeliveryID: deliveryID, OperationKeyDigest: key})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, grantDTO(projection))
}

func GetControlGrant(w http.ResponseWriter, r *http.Request) {
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	if _, err := service.Reconcile(r.Context(), principal, supervision.ReconcileRequest{Mode: supervision.ReconcileActor, Limit: 32}); err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	projection, err := service.GetActorGrant(r.Context(), principal,
		supervision.GrantGetRequest{GrantID: chi.URLParam(r, "controlID")})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, grantDTO(projection))
}

func TransitionControlGrant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operation string      `json:"operation"`
		Revision  json.Number `json:"revision"`
	}
	key, ok := decodeControlMutation(w, r, controlSmallBodyLimit, &body)
	if !ok {
		return
	}
	if _, err := StrictControlEnum(body.Operation, []string{"revoke"}); err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	revision, err := positiveControlInteger(body.Revision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	projection, err := service.RevokeActorGrant(r.Context(), principal, supervision.GrantRevokeRequest{
		GrantID: chi.URLParam(r, "controlID"), Revision: revision, OperationKeyDigest: key})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, grantDTO(projection))
}

type commandCreateBody struct {
	GrantID              string      `json:"grant_id"`
	GrantRevision        json.Number `json:"grant_revision"`
	Action               string      `json:"action"`
	Priority             string      `json:"priority,omitempty"`
	RunID                json.Number `json:"run_id,omitempty"`
	InputRequestID       string      `json:"input_request_id,omitempty"`
	InputRequestRevision json.Number `json:"input_request_revision,omitempty"`
	InputResponse        string      `json:"input_response,omitempty"`
	ChoiceOrdinal        json.Number `json:"choice_ordinal,omitempty"`
	RuntimeRevision      json.Number `json:"runtime_revision,omitempty"`
}

func CreateControlCommand(w http.ResponseWriter, r *http.Request) {
	var body commandCreateBody
	key, ok := decodeControlMutation(w, r, controlCommandBodyLimit, &body)
	if !ok {
		return
	}
	action, err := StrictControlEnum(body.Action, actionStrings())
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	if err := validateCommandCreateBody(body, supervision.Action(action)); err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	if body.Priority != "" {
		if _, err := StrictControlEnum(body.Priority, []string{"low", "medium", "high"}); err != nil {
			WriteControlRequestError(w, r, err)
			return
		}
	}
	if body.InputResponse != "" {
		if _, err := StrictControlEnum(body.InputResponse, []string{"approve", "reject", "choice"}); err != nil {
			WriteControlRequestError(w, r, err)
			return
		}
	}
	grantRevision, err := positiveControlInteger(body.GrantRevision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	deliveryID, ok := resolveControlDelivery(r, chi.URLParam(r, "deliveryKey"))
	if !ok || !controlGrantNamesDelivery(r, body.GrantID, grantRevision, deliveryID) {
		writeControlNotFound(w, r)
		return
	}
	request := supervision.CommandCreateRequest{GrantID: body.GrantID, GrantRevision: grantRevision,
		Action: supervision.Action(action), Priority: body.Priority, InputRequestID: body.InputRequestID,
		InputResponse: supervision.InputResponseKind(body.InputResponse), OperationKeyDigest: key}
	if body.RunID != "" {
		request.RunID, err = positiveControlInteger(body.RunID)
	}
	if err == nil && body.InputRequestRevision != "" {
		request.InputRequestRevision, err = positiveControlInteger(body.InputRequestRevision)
	}
	if err == nil && body.ChoiceOrdinal != "" {
		var ordinal int64
		ordinal, err = positiveControlInteger(body.ChoiceOrdinal)
		request.ChoiceOrdinal = int(ordinal)
	}
	if err == nil && body.RuntimeRevision != "" {
		request.RuntimeRevision, err = positiveControlInteger(body.RuntimeRevision)
	}
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	projection, err := service.CreateCommand(r.Context(), principal, request)
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, commandDTO(projection))
}

func GetControlCommand(w http.ResponseWriter, r *http.Request) {
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	if _, err := service.Reconcile(r.Context(), principal, supervision.ReconcileRequest{Mode: supervision.ReconcileActor, Limit: 32}); err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	projection, err := service.GetCommand(r.Context(), principal,
		supervision.CommandGetRequest{CommandID: chi.URLParam(r, "controlID")})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, commandDTO(projection))
}

func TransitionControlCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operation      string      `json:"operation"`
		StatusRevision json.Number `json:"status_revision"`
	}
	key, ok := decodeControlMutation(w, r, controlSmallBodyLimit, &body)
	if !ok {
		return
	}
	operation, err := StrictControlEnum(body.Operation, []string{"confirm", "withdraw"})
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	revision, err := positiveControlInteger(body.StatusRevision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	var projection supervision.CommandProjection
	if operation == "confirm" {
		projection, err = service.ConfirmCommand(r.Context(), principal, supervision.CommandConfirmRequest{
			CommandID: chi.URLParam(r, "controlID"), StatusRevision: revision, OperationKeyDigest: key})
	} else {
		projection, err = service.WithdrawCommand(r.Context(), principal, supervision.CommandWithdrawRequest{
			CommandID: chi.URLParam(r, "controlID"), StatusRevision: revision, OperationKeyDigest: key})
	}
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, commandDTO(projection))
}

func IssueControlLease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID         string   `json:"device_id"`
		SupportedActions []string `json:"supported_actions"`
	}
	key, ok := decodeControlMutation(w, r, controlCommandBodyLimit, &body)
	if !ok {
		return
	}
	runID, ok := positiveControlPathInteger(chi.URLParam(r, "id"))
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	actions, err := strictActions(body.SupportedActions)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	projection, err := service.IssueRunnerLease(r.Context(), principal, supervision.LeaseIssueRequest{
		RunID: runID, DeviceID: body.DeviceID, SupportedActions: actions, OperationKeyDigest: key})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	if _, err := service.Reconcile(r.Context(), principal, supervision.ReconcileRequest{Mode: supervision.ReconcileRunner,
		Limit: 32, LeaseID: projection.LeaseID, LeaseRevision: projection.Revision, DeviceID: body.DeviceID}); err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, leaseDTO(projection))
}

func TransitionControlLease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operation        string      `json:"operation"`
		Revision         json.Number `json:"revision"`
		DeviceID         string      `json:"device_id,omitempty"`
		SupportedActions []string    `json:"supported_actions,omitempty"`
	}
	key, ok := decodeControlMutation(w, r, controlCommandBodyLimit, &body)
	if !ok {
		return
	}
	operation, err := StrictControlEnum(body.Operation, []string{"renew", "revoke"})
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	revision, err := positiveControlInteger(body.Revision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	var projection supervision.LeaseProjection
	if operation == "renew" {
		actions, actionErr := strictActions(body.SupportedActions)
		if actionErr != nil || body.DeviceID == "" {
			if actionErr == nil {
				actionErr = ErrControlBodyMalformed
			}
			WriteControlRequestError(w, r, actionErr)
			return
		}
		if _, reconcileErr := service.Reconcile(r.Context(), principal, supervision.ReconcileRequest{Mode: supervision.ReconcileRunner,
			Limit: 32, LeaseID: chi.URLParam(r, "controlID"), LeaseRevision: revision, DeviceID: body.DeviceID}); reconcileErr != nil {
			writeControlDomainError(w, r, reconcileErr)
			return
		}
		projection, err = service.RenewRunnerLease(r.Context(), principal, supervision.LeaseRenewRequest{
			LeaseID: chi.URLParam(r, "controlID"), Revision: revision, DeviceID: body.DeviceID,
			SupportedActions: actions, OperationKeyDigest: key})
	} else {
		if strings.TrimSpace(body.DeviceID) == "" || body.SupportedActions != nil {
			WriteControlRequestError(w, r, ErrControlBodyMalformed)
			return
		}
		projection, err = service.RevokeRunnerLease(r.Context(), principal, supervision.LeaseRevokeRequest{
			LeaseID: chi.URLParam(r, "controlID"), Revision: revision, DeviceID: body.DeviceID,
			OperationKeyDigest: key})
	}
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, leaseDTO(projection))
}

func CreateControlInputRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseID       string      `json:"lease_id"`
		LeaseRevision json.Number `json:"lease_revision"`
		RequestID     string      `json:"request_id,omitempty"`
		Kind          string      `json:"kind"`
		Prompt        string      `json:"prompt_template"`
		OptionCodes   []string    `json:"option_codes"`
	}
	key, ok := decodeControlMutation(w, r, controlCommandBodyLimit, &body)
	if !ok {
		return
	}
	runID, ok := positiveControlPathInteger(chi.URLParam(r, "id"))
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	leaseRevision, err := positiveControlInteger(body.LeaseRevision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	kind, err := StrictControlEnum(body.Kind, []string{"approval", "choice"})
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	prompt, err := StrictControlEnum(body.Prompt, []string{"approval_required", "choice_required"})
	if err != nil || (kind == "approval" && (prompt != "approval_required" || len(body.OptionCodes) != 0)) ||
		(kind == "choice" && (prompt != "choice_required" || len(body.OptionCodes) == 0)) {
		if err == nil {
			err = ErrControlBodyMalformed
		}
		WriteControlRequestError(w, r, err)
		return
	}
	for index, code := range body.OptionCodes {
		want := "choice_" + strconv.Itoa(index+1)
		if parsed, optionErr := StrictControlEnum(code, []string{"choice_1", "choice_2", "choice_3", "choice_4",
			"choice_5", "choice_6", "choice_7", "choice_8"}); optionErr != nil || parsed != want {
			if optionErr == nil {
				optionErr = ErrControlEnumInvalid
			}
			WriteControlRequestError(w, r, optionErr)
			return
		}
	}
	if !controlLeaseNamesRun(r, body.LeaseID, leaseRevision, runID) {
		writeControlNotFound(w, r)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	projection, err := service.CreateInputRequest(r.Context(), principal, supervision.InputCreateRequest{
		LeaseID: body.LeaseID, LeaseRevision: leaseRevision, RequestID: body.RequestID,
		Kind: supervision.InputKind(kind), PromptTemplate: supervision.InputPromptTemplate(prompt),
		OptionCodes: append([]string(nil), body.OptionCodes...), OperationKeyDigest: key})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	controlJSON(w, inputDTO(projection))
}

func PullControlCommands(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseID       string      `json:"lease_id"`
		LeaseRevision json.Number `json:"lease_revision"`
		DeviceID      string      `json:"device_id"`
		Cursor        json.Number `json:"cursor"`
	}
	if err := DecodeControlJSON(w, r, controlSmallBodyLimit, &body); err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	// Pull is read-like in the domain but still requires one stable retry key on
	// the runner wire contract. Only its digest survives validation.
	if _, err := ControlIdempotencyKeyDigest(r); err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	runID, ok := positiveControlPathInteger(chi.URLParam(r, "id"))
	if !ok {
		writeControlNotFound(w, r)
		return
	}
	leaseRevision, err := positiveControlInteger(body.LeaseRevision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	cursor, err := nonNegativeControlInteger(body.Cursor)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	if strings.TrimSpace(body.DeviceID) == "" {
		WriteControlRequestError(w, r, ErrControlBodyMalformed)
		return
	}
	if !controlLeaseDeviceNamesRun(r, body.LeaseID, leaseRevision, runID, body.DeviceID) {
		writeControlNotFound(w, r)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	if _, err := service.Reconcile(r.Context(), principal, supervision.ReconcileRequest{Mode: supervision.ReconcileRunner,
		Limit: 32, LeaseID: body.LeaseID, LeaseRevision: leaseRevision, DeviceID: body.DeviceID}); err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	projection, err := service.Pull(r.Context(), principal, supervision.PullRequest{
		LeaseID: body.LeaseID, LeaseRevision: leaseRevision, DeviceID: body.DeviceID, Cursor: cursor})
	if err != nil {
		writeControlDomainError(w, r, err)
		return
	}
	effects := make([]controlEffectDTO, len(projection.Effects))
	for index := range projection.Effects {
		effects[index] = effectDTO(projection.Effects[index])
	}
	controlJSON(w, controlPullDTO{SnapshotHighWater: projection.SnapshotHighWater,
		NextCursor: projection.NextCursor, HasMore: projection.HasMore, Effects: effects})
}

func TransitionRunnerControlCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operation      string      `json:"operation"`
		LeaseID        string      `json:"lease_id"`
		LeaseRevision  json.Number `json:"lease_revision"`
		EffectSequence json.Number `json:"effect_sequence"`
		ClaimSequence  json.Number `json:"claim_sequence,omitempty"`
		ResultSequence json.Number `json:"result_sequence,omitempty"`
		DeviceID       string      `json:"device_id"`
		Outcome        string      `json:"outcome,omitempty"`
		Reason         string      `json:"reason,omitempty"`
	}
	key, ok := decodeControlMutation(w, r, controlCommandBodyLimit, &body)
	if !ok {
		return
	}
	operation, err := StrictControlEnum(body.Operation, []string{"claim", "result"})
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	leaseRevision, err := positiveControlInteger(body.LeaseRevision)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	effectSequence, err := positiveControlInteger(body.EffectSequence)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return
	}
	principal, _ := controlPrincipal(r)
	service := newControlService(db.DB)
	if operation == "claim" {
		if body.ClaimSequence != "" || body.ResultSequence != "" || body.Outcome != "" || body.Reason != "" || body.DeviceID == "" {
			WriteControlRequestError(w, r, ErrControlBodyMalformed)
			return
		}
		projection, claimErr := service.Claim(r.Context(), principal, supervision.ClaimRequest{
			CommandID: chi.URLParam(r, "controlID"), LeaseID: body.LeaseID, LeaseRevision: leaseRevision,
			EffectSequence: effectSequence, DeviceID: body.DeviceID, OperationKeyDigest: key})
		if claimErr != nil {
			writeControlDomainError(w, r, claimErr)
			return
		}
		controlJSON(w, effectDTO(projection))
		return
	}
	if body.DeviceID == "" {
		WriteControlRequestError(w, r, ErrControlBodyMalformed)
		return
	}
	outcome, enumErr := StrictControlEnum(body.Outcome, []string{"applied", "rejected"})
	if enumErr != nil || (outcome == "applied" && body.Reason != "") || (outcome == "rejected" && body.Reason == "") {
		if enumErr == nil {
			enumErr = ErrControlBodyMalformed
		}
		WriteControlRequestError(w, r, enumErr)
		return
	}
	if body.Reason != "" {
		if _, enumErr = StrictControlEnum(body.Reason, []string{"effect_rejected", "unsupported_platform", "process_termination_failed", "natural_exit"}); enumErr != nil {
			WriteControlRequestError(w, r, enumErr)
			return
		}
	}
	claimSequence, err := positiveControlInteger(body.ClaimSequence)
	if err == nil {
		var resultSequence int64
		resultSequence, err = positiveControlInteger(body.ResultSequence)
		if err == nil {
			projection, resultErr := service.RecordResult(r.Context(), principal, supervision.ResultRequest{
				CommandID: chi.URLParam(r, "controlID"), LeaseID: body.LeaseID, LeaseRevision: leaseRevision,
				EffectSequence: effectSequence, ClaimSequence: claimSequence, ResultSequence: resultSequence,
				DeviceID: body.DeviceID, Outcome: supervision.Outcome(outcome),
				Reason: supervision.SafeReason(body.Reason), OperationKeyDigest: key})
			if resultErr != nil {
				writeControlDomainError(w, r, resultErr)
				return
			}
			controlJSON(w, commandDTO(projection))
			return
		}
	}
	WriteControlRequestError(w, r, err)
}

func decodeControlMutation(w http.ResponseWriter, r *http.Request, limit int64, dst any) ([32]byte, bool) {
	var zero [32]byte
	if err := DecodeControlJSON(w, r, limit, dst); err != nil {
		WriteControlRequestError(w, r, err)
		return zero, false
	}
	key, err := ControlIdempotencyKeyDigest(r)
	if err != nil {
		WriteControlRequestError(w, r, err)
		return zero, false
	}
	return key, true
}

func positiveControlInteger(value json.Number) (int64, error) {
	parsed, err := StrictControlInt64(value)
	if err != nil || parsed <= 0 {
		return 0, ErrControlIntegerInvalid
	}
	return parsed, nil
}

func nonNegativeControlInteger(value json.Number) (int64, error) {
	parsed, err := StrictControlInt64(value)
	if err != nil || parsed < 0 {
		return 0, ErrControlIntegerInvalid
	}
	return parsed, nil
}

func positiveControlPathInteger(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func actionStrings() []string {
	actions := supervision.Actions()
	out := make([]string, len(actions))
	for index := range actions {
		out[index] = string(actions[index])
	}
	return out
}

func validateCommandCreateBody(body commandCreateBody, action supervision.Action) error {
	hasRun := body.RunID != ""
	hasInput := body.InputRequestID != "" || body.InputRequestRevision != "" || body.InputResponse != "" || body.ChoiceOrdinal != ""
	hasRuntime := body.RuntimeRevision != ""
	switch action {
	case "issue.priority.set":
		if body.Priority == "" || hasRun || hasInput || hasRuntime {
			return ErrControlBodyMalformed
		}
	case "run.cancel.queued", "run.cancel.running":
		if !hasRun || body.Priority != "" || hasInput || hasRuntime {
			return ErrControlBodyMalformed
		}
	case "input.respond":
		if hasRun || body.Priority != "" || body.InputRequestID == "" || body.InputRequestRevision == "" ||
			body.InputResponse == "" || hasRuntime {
			return ErrControlBodyMalformed
		}
		if body.InputResponse == "choice" {
			if body.ChoiceOrdinal == "" {
				return ErrControlBodyMalformed
			}
		} else if body.ChoiceOrdinal != "" {
			return ErrControlBodyMalformed
		}
	case "run.pause", "run.resume":
		if !hasRun || !hasRuntime || body.Priority != "" || hasInput {
			return ErrControlBodyMalformed
		}
	default:
		return ErrControlEnumInvalid
	}
	return nil
}

func strictActions(values []string) ([]supervision.Action, error) {
	if len(values) == 0 || len(values) > len(supervision.Actions()) {
		return nil, ErrControlEnumInvalid
	}
	seen := map[string]bool{}
	out := make([]supervision.Action, len(values))
	for index, value := range values {
		parsed, err := StrictControlEnum(value, actionStrings())
		if err != nil || seen[parsed] {
			return nil, ErrControlEnumInvalid
		}
		seen[parsed] = true
		out[index] = supervision.Action(parsed)
	}
	return out, nil
}

func resolveControlDelivery(r *http.Request, key string) (int64, bool) {
	if strings.TrimSpace(key) != key || key == "" || len(key) > 80 {
		return 0, false
	}
	var id int64
	err := db.DB.QueryRowContext(r.Context(), `SELECT delivery.id FROM deliveries delivery
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id AND project.status<>'deleted'
		WHERE delivery.delivery_key=?`, key).Scan(&id)
	return id, err == nil && id > 0
}

func controlGrantNamesDelivery(r *http.Request, grantID string, revision, deliveryID int64) bool {
	principal, ok := controlPrincipal(r)
	if !ok || grantID == "" || revision <= 0 || deliveryID <= 0 {
		return false
	}
	var exists int
	err := db.DB.QueryRowContext(r.Context(), `SELECT 1 FROM control_capability_grants
		WHERE grant_id=? AND revision=? AND delivery_id=? AND user_id=?`, grantID, revision, deliveryID,
		principal.UserID()).Scan(&exists)
	return err == nil && exists == 1
}

func controlLeaseNamesRun(r *http.Request, leaseID string, revision, runID int64) bool {
	principal, ok := controlPrincipal(r)
	if !ok || principal.Kind() != auth.PrincipalAPIKey || leaseID == "" || revision <= 0 || runID <= 0 {
		return false
	}
	var exists int
	err := db.DB.QueryRowContext(r.Context(), `SELECT 1 FROM control_capability_leases
		WHERE lease_id=? AND revision=? AND agent_run_id=? AND user_id=? AND actor_api_key_id=?`,
		leaseID, revision, runID, principal.UserID(), principal.APIKeyID()).Scan(&exists)
	return err == nil && exists == 1
}

func controlLeaseDeviceNamesRun(r *http.Request, leaseID string, revision, runID int64, deviceID string) bool {
	principal, ok := controlPrincipal(r)
	if !ok || principal.Kind() != auth.PrincipalAPIKey || leaseID == "" || revision <= 0 || runID <= 0 ||
		strings.TrimSpace(deviceID) == "" {
		return false
	}
	var exists int
	err := db.DB.QueryRowContext(r.Context(), `SELECT 1 FROM control_capability_leases
		WHERE lease_id=? AND revision=? AND agent_run_id=? AND user_id=? AND actor_api_key_id=? AND device_id=?`,
		leaseID, revision, runID, principal.UserID(), principal.APIKeyID(), deviceID).Scan(&exists)
	return err == nil && exists == 1
}

// ControlAwareNotFound/MethodNotAllowed retain ordinary chi behavior for
// ordinary endpoints while giving every classified control outcome the fixed
// privacy envelope.
func ControlAwareNotFound(w http.ResponseWriter, r *http.Request) {
	if IsClassifiedControlRequest(r) {
		writeControlNotFound(w, r)
		return
	}
	http.NotFound(w, r)
}

func ControlAwareMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	// External-stage adapter routes are API-key-only concealed capabilities.
	// chi reaches this global handler before the route group's authentication
	// middleware, so a method distinction here would disclose the route.
	if strings.HasPrefix(r.URL.Path, "/api/external-stage/") {
		w.Header().Del("Allow")
		w.Header().Del("X-Permissions-Epoch")
		writeControlNotFound(w, r)
		return
	}
	if IsClassifiedControlRequest(r) {
		requestID := trustedControlResponseRequestID(r)
		w.Header().Set(RequestIDHeader, requestID)
		SetControlCachePolicy(w)
		writeProblem(w, nil, ProblemDetails{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed",
			Detail: "control method is not allowed", RequestID: requestID})
		return
	}
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
}

// AgentModeControlAwareNotFound preserves Agent Mode's established private
// concealment for unrelated paths while retaining the closed control problem
// envelope for the exact classified families.
func AgentModeControlAwareNotFound(w http.ResponseWriter, r *http.Request) {
	if IsClassifiedControlRequest(r) {
		writeControlNotFound(w, r)
		return
	}
	httpcontract.WriteAgentModeNotFound(w, r)
}

func AgentModeControlAwareMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if IsClassifiedControlRequest(r) {
		ControlAwareMethodNotAllowed(w, r)
		return
	}
	httpcontract.WriteAgentModeNotFound(w, r)
}

func IsClassifiedControlRequest(r *http.Request) bool {
	return httpcontract.IsControlRequest(r)
}
