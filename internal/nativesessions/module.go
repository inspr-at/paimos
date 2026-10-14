// SPDX-License-Identifier: AGPL-3.0-only

// Package nativesessions mounts the additive owned native session surface.
// Providers remain disabled until I1 supplies all qualified services.
package nativesessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/tenant"
)

// A disabled module retains no native or database services.
type Module struct {
	services  *nativecontract.Services
	validator *nativecontract.Validator
	live      *stream.HTTPHandler
}

func NewDisabled() *Module { return &Module{} }

// New requires all qualified services. Each Store/FinalSink implementation must
// revalidate the current target under its final mutation fence; HTTP admission
// and a qualified label never substitute for that check.
func New(services nativecontract.Services) (*Module, error) {
	if !services.Qualified || services.Store == nil || services.Frames == nil || services.Finals == nil {
		return nil, &nativecontract.ValidationError{Reason: nativecontract.ReasonFeatureDisabled, Path: "$"}
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	m := &Module{services: &services, validator: v}
	if relay, ok := services.Frames.(*stream.Relay); ok {
		if authority, ok := services.Store.(stream.ReadAuthority); ok {
			m.live, err = stream.NewHTTPHandler(relay, authority)
			if err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

func (m *Module) Mount(mux *http.ServeMux) {
	for _, route := range nativecontract.PublicRoutes() {
		handler := m.disabled
		if m.services != nil {
			handler = func(w http.ResponseWriter, r *http.Request) { m.serve(w, r, route) }
		}
		mux.HandleFunc(route.Method+" "+route.Path, reportercontract.WithHeader(reportercontract.NativeSession, handler))
	}
}

// DaemonRequests resolves CURRENT paired identity and private proof from the
// authenticated request. Wire generation fields are selectors, never proof.
// A qualified Store without this seam cannot receive daemon mutations.
type DaemonRequests interface {
	DaemonRequest(*http.Request) (context.Context, nativecontract.DaemonActor, error)
}

type views interface {
	View(context.Context, nativecontract.Actor, string, string) (nativecontract.SessionView, error)
	ByHarness(context.Context, nativecontract.Actor, string, string) (nativecontract.SessionView, error)
}

type attaches interface {
	Attach(context.Context, nativecontract.Actor, nativecontract.AttachIntent) (nativecontract.AttachResult, error)
	Renew(context.Context, nativecontract.Actor, nativecontract.AttachRenew) (nativecontract.AttachResult, error)
}

// LiveRequests binds publication to current paired-worker authority. P2a's
// ReadAuthority separately authenticates each viewer and frame-delivery fence.
type LiveRequests interface {
	PublishRequest(context.Context, nativecontract.DaemonActor, nativecontract.Publish) (nativecontract.OperationResult, error)
}

type projectKey struct{}

// RouteProject supplies the transport's project selector to the integrating
// Store adapter. It confers no rights and keeps auth's module independent of
// the concrete start store/account/inbox dependency graph.
func RouteProject(ctx context.Context) string {
	project, _ := ctx.Value(projectKey{}).(string)
	return project
}

func (m *Module) serve(w http.ResponseWriter, r *http.Request, route nativecontract.Route) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" || p.Kind != tenant.Person && p.Kind != tenant.Agent {
		writeReason(w, http.StatusUnauthorized, nativecontract.ReasonGrantMissing)
		return
	}
	if len(r.URL.RawQuery) > 2048 {
		writeReason(w, http.StatusRequestEntityTooLarge, nativecontract.ReasonBufferFull)
		return
	}
	// The same person-only boundaries apply before either enabled or disabled
	// handlers inspect bodies or look up targets.
	if p.Kind == tenant.Agent && personOnly(r) {
		writeReason(w, http.StatusForbidden, nativecontract.ReasonWrongPrincipal)
		return
	}
	if route.OperationID == "streamNativeSession" || route.OperationID == "listNativeSessionEvents" {
		if m.live == nil {
			nativeError(w, invalid(nativecontract.ReasonQualificationMissing))
			return
		}
		if route.OperationID == "streamNativeSession" {
			m.live.Stream(w, r)
		} else {
			m.live.LivePage(w, r)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	actor := nativecontract.Actor{TenantID: p.TenantID, PrincipalID: p.ID, PrincipalKind: string(p.Kind), KeyCeiling: p.Scopes}
	ctx = context.WithValue(ctx, projectKey{}, r.PathValue("projectId"))
	r = r.WithContext(ctx)
	var daemon nativecontract.DaemonActor
	if r.PathValue("projectId") == "" && route.OperationID != "endNativeSessionControl" {
		requests, ok := m.services.Store.(DaemonRequests)
		if !ok || p.Kind != tenant.Agent {
			writeReason(w, http.StatusForbidden, nativecontract.ReasonGrantMissing)
			return
		}
		var err error
		ctx, daemon, err = requests.DaemonRequest(r)
		if err != nil || ctx == nil || daemon.PrincipalID != p.ID || daemon.TenantID != p.TenantID || daemon.PrincipalKind != string(p.Kind) {
			writeReason(w, http.StatusForbidden, nativecontract.ReasonGrantMissing)
			return
		}
		r = r.WithContext(ctx)
	}
	var result any
	var err error
	if route.Method == http.MethodGet {
		result, err = m.read(w, r, route, actor, daemon)
	} else {
		limit := int64(nativecontract.RequestBodyBytes)
		if route.RequestSchema == "NativeReceipt" {
			limit = nativecontract.ReceiptBytes
		}
		if route.RequestSchema == "NativePublish" {
			limit = nativecontract.PublishBatchBytes
		}
		var raw []byte
		raw, err = httpapi.ReadRequestBody(w, r, limit, nativecontract.BodyWorkDeadlineSeconds*time.Second)
		if err == nil {
			err = m.validator.ValidateJSON(route.RequestSchema, raw)
		}
		if err == nil {
			result, err = m.mutate(r, route, actor, daemon, raw)
		}
	}
	if err != nil {
		nativeError(w, err)
		return
	}
	// Reapply the public projection at transport egress, including replies from
	// mutating stores and native-context lookups. Agent keys never receive URLs.
	switch value := result.(type) {
	case nativecontract.SessionView:
		result = nativecontract.PersonProjection(value, actor.PrincipalKind, true)
	case nativecontract.OperationResult:
		if value.Session != nil {
			view := nativecontract.PersonProjection(*value.Session, actor.PrincipalKind, true)
			value.Session = &view
		}
		result = value
	case nativecontract.AttachResult:
		value.Snapshot = nativecontract.PersonProjection(value.Snapshot, actor.PrincipalKind, true)
		result = value
	}
	if err = m.validateResponse(route.ResponseSchema, result); err != nil {
		nativeError(w, err)
		return
	}
	status := http.StatusOK
	if operation, ok := result.(nativecontract.OperationResult); ok && (operation.Receipt.State == "accepted" || operation.Receipt.State == "queued") {
		status = http.StatusAccepted
	}
	httpapi.WriteJSON(w, status, result)
}

func personOnly(r *http.Request) bool {
	view := r.URL.Query().Get("view")
	if view == "terminal" || view == "expert" {
		return true
	}
	switch r.Pattern {
	case "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach", "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach/renew", "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/detach", "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/take", "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/return", "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/approvals/{approvalId}/decision", "POST /api/sessions/{id}/control/end":
		return true
	}
	return false
}

func invalid(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (m *Module) mutate(r *http.Request, route nativecontract.Route, actor nativecontract.Actor, daemon nativecontract.DaemonActor, raw []byte) (any, error) {
	ctx := r.Context()
	command := nativecontract.Command{}
	var binding *nativecontract.Binding
	switch route.OperationID {
	case "startNativeSession":
		var in nativecontract.StartIntent
		_ = json.Unmarshal(raw, &in)
		return m.services.Store.Start(ctx, actor, in)
	case "claimNativeSession":
		var in nativecontract.Claim
		_ = json.Unmarshal(raw, &in)
		return m.services.Store.Claim(ctx, daemon, in)
	case "reportNativeSessionReceipt":
		var in nativecontract.Receipt
		_ = json.Unmarshal(raw, &in)
		return m.services.Store.Report(ctx, daemon, in)
	case "persistNativeExplicitFinal":
		var in nativecontract.FinalPublish
		_ = json.Unmarshal(raw, &in)
		return m.services.Finals.Persist(ctx, in)
	case "publishNativeSessionEvents":
		var in nativecontract.Publish
		_ = json.Unmarshal(raw, &in)
		live, ok := m.services.Store.(LiveRequests)
		if !ok {
			return nil, invalid(nativecontract.ReasonQualificationMissing)
		}
		return live.PublishRequest(ctx, daemon, in)
	case "nativeAttach", "nativeAttachRenew":
		attach, ok := m.services.Store.(attaches)
		if !ok {
			return nil, invalid(nativecontract.ReasonQualificationMissing)
		}
		if route.OperationID == "nativeAttach" {
			var in nativecontract.AttachIntent
			_ = json.Unmarshal(raw, &in)
			if !routeBinding(r, actor, in.ExpectedBinding) {
				return nil, invalid(nativecontract.ReasonIdentityMismatch)
			}
			return attach.Attach(ctx, actor, in)
		}
		var in nativecontract.AttachRenew
		_ = json.Unmarshal(raw, &in)
		if !routeBinding(r, actor, in.ExpectedBinding) {
			return nil, invalid(nativecontract.ReasonIdentityMismatch)
		}
		return attach.Renew(ctx, actor, in)
	case "nativeInput":
		command.Kind, command.Input = "input", new(nativecontract.Input)
		_ = json.Unmarshal(raw, command.Input)
		binding = &command.Input.ExpectedBinding
	case "nativeInterrupt":
		command.Kind, command.Interrupt = "interrupt", new(nativecontract.Interrupt)
		_ = json.Unmarshal(raw, command.Interrupt)
		binding = &command.Interrupt.ExpectedBinding
	case "nativeStop":
		command.Kind, command.Stop = "stop", new(nativecontract.Stop)
		_ = json.Unmarshal(raw, command.Stop)
		binding = &command.Stop.ExpectedBinding
	case "nativeDetach":
		command.Kind, command.Detach = "detach", new(nativecontract.Detach)
		_ = json.Unmarshal(raw, command.Detach)
		binding = &command.Detach.ExpectedBinding
	case "resumeNativeSession":
		command.Kind, command.Resume = "resume", new(nativecontract.ResumeIntent)
		_ = json.Unmarshal(raw, command.Resume)
		binding = &command.Resume.ExpectedBinding
	case "nativeTake":
		command.Kind, command.Take = "take", new(nativecontract.TakeIntent)
		_ = json.Unmarshal(raw, command.Take)
		binding = &command.Take.ExpectedBinding
	case "nativeReturn":
		command.Kind, command.Return = "return", new(nativecontract.ReturnIntent)
		_ = json.Unmarshal(raw, command.Return)
		binding = &command.Return.ExpectedBinding
	case "nativeApprovalsApprovalDecision":
		command.Kind, command.Approval = "approval", new(nativecontract.ApprovalDecision)
		_ = json.Unmarshal(raw, command.Approval)
		binding = &command.Approval.ExpectedBinding
		if command.Approval.ApprovalID != r.PathValue("approvalId") {
			return nil, invalid(nativecontract.ReasonIdentityMismatch)
		}
	case "endNativeSessionControl":
		command.Kind, command.EndControl = "control_end", new(nativecontract.EndControl)
		_ = json.Unmarshal(raw, command.EndControl)
		binding = &command.EndControl.ExpectedBinding
	default:
		return nil, invalid(nativecontract.ReasonUnsupported)
	}
	if binding == nil || !routeBinding(r, actor, *binding) {
		return nil, invalid(nativecontract.ReasonIdentityMismatch)
	}
	return m.services.Store.Mutate(ctx, actor, command)
}

func routeBinding(r *http.Request, actor nativecontract.Actor, binding nativecontract.Binding) bool {
	if actor.TenantID != binding.TenantID {
		return false
	}
	if r.Pattern == "POST /api/sessions/{id}/control/end" {
		return r.PathValue("id") == binding.OwnedSessionID
	}
	return r.PathValue("projectId") == binding.ProjectID && r.PathValue("ownedSessionId") == binding.OwnedSessionID
}

func (m *Module) read(w http.ResponseWriter, r *http.Request, route nativecontract.Route, actor nativecontract.Actor, daemon nativecontract.DaemonActor) (any, error) {
	query := r.URL.Query()
	limit := 0
	if value := query.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > nativecontract.ReceiptPageItems {
			return nil, invalid(nativecontract.ReasonBufferFull)
		}
	}
	if len(query.Get("after")) > 512 {
		return nil, invalid(nativecontract.ReasonBufferFull)
	}
	if route.OperationID == "listNativePendingCommands" {
		if limit > 50 {
			return nil, invalid(nativecontract.ReasonBufferFull)
		}
		return m.services.Store.Commands(r.Context(), daemon, nativecontract.PageQuery{After: query.Get("after"), Limit: limit})
	}
	store, ok := m.services.Store.(views)
	if !ok {
		return nil, invalid(nativecontract.ReasonQualificationMissing)
	}
	var view nativecontract.SessionView
	var err error
	if route.OperationID == "getHarnessNativeContext" {
		return store.ByHarness(r.Context(), actor, r.PathValue("projectId"), r.PathValue("sessionId"))
	}
	view, err = store.View(r.Context(), actor, r.PathValue("projectId"), r.PathValue("ownedSessionId"))
	if err != nil {
		return nil, err
	}
	if route.OperationID == "getNativeSession" {
		return nativecontract.PersonProjection(view, actor.PrincipalKind, true), nil
	}
	if view.CurrentBinding == nil {
		return nil, invalid(nativecontract.ReasonWrongGeneration)
	}
	if route.OperationID == "listNativeReceipts" {
		return m.services.Store.Receipts(r.Context(), actor, *view.CurrentBinding, nativecontract.PageQuery{After: query.Get("after"), Limit: limit})
	}
	return nil, invalid(nativecontract.ReasonQualificationMissing)
}

func (m *Module) validateResponse(schema string, result any) error {
	if schema == "NativeStreamEvent SSE" {
		return nil
	}
	_, err := nativestream.Encode(m.validator, schema, result)
	return err
}

func nativeError(w http.ResponseWriter, err error) {
	status, reason := http.StatusServiceUnavailable, nativecontract.ReasonNativeFailure
	var invalid *nativecontract.ValidationError
	var large *http.MaxBytesError
	var failure interface{ HTTPStatus() int }
	if errors.As(err, &invalid) {
		reason = invalid.Reason
		switch reason {
		case nativecontract.ReasonInvalidInput, nativecontract.ReasonUnsupported:
			status = http.StatusBadRequest
		case nativecontract.ReasonIdentityMismatch, nativecontract.ReasonWrongGeneration, nativecontract.ReasonStaleRevision:
			status = http.StatusConflict
		case nativecontract.ReasonWrongPrincipal, nativecontract.ReasonGrantMissing, nativecontract.ReasonPermissionRevoked, nativecontract.ReasonGrantExpired:
			status = http.StatusForbidden
		case nativecontract.ReasonBufferFull:
			status = http.StatusRequestEntityTooLarge
		}
	} else if errors.As(err, &large) {
		status, reason = http.StatusRequestEntityTooLarge, nativecontract.ReasonBufferFull
	} else if errors.Is(err, httpapi.ErrBodyDeadlineUnavailable) {
		reason = nativecontract.ReasonQualificationMissing
	} else if errors.As(err, &failure) {
		status = failure.HTTPStatus()
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusRequestTimeout
	}
	writeReason(w, status, reason)
}

// AuthenticationMiddleware keeps the native envelope around authentication,
// after Server has resolved the exact route pattern. It changes only rejected
// response bodies; authentication, pairing and permission checks stay intact.
func AuthenticationMiddleware(authenticate func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		ordinary := authenticate(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			native := false
			for _, route := range nativecontract.PublicRoutes() {
				if r.Pattern == route.Method+" "+route.Path {
					native = true
					break
				}
			}
			if !native {
				ordinary.ServeHTTP(w, r)
				return
			}
			w.Header().Set(reportercontract.Header, reportercontract.NativeSession)
			w.Header().Set("Cache-Control", "no-store")
			response := &authenticationResponse{ResponseWriter: w}
			authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response.authorized = true
				next.ServeHTTP(w, r)
			})).ServeHTTP(response, r)
		})
	}
}

type authenticationResponse struct {
	http.ResponseWriter
	authorized, written, denied bool
}

func (w *authenticationResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *authenticationResponse) WriteHeader(status int) {
	if w.written {
		return
	}
	w.written = true
	if !w.authorized && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		w.denied = true
		w.Header().Del("Content-Length")
		writeReason(w.ResponseWriter, status, nativecontract.ReasonGrantMissing)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *authenticationResponse) Write(body []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if w.denied {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

func (w *authenticationResponse) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if !w.denied {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
	}
}

func (*Module) disabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" || p.Kind != tenant.Person && p.Kind != tenant.Agent {
		writeReason(w, http.StatusUnauthorized, nativecontract.ReasonGrantMissing)
		return
	}
	// These denials remain server-side, even for an owner-workstation agent.
	// The disabled response never looks up the requested session or project.
	if len(r.URL.RawQuery) > 2048 {
		writeReason(w, http.StatusRequestEntityTooLarge, nativecontract.ReasonBufferFull)
		return
	}
	view := r.URL.Query().Get("view")
	personOnly := false
	switch r.Pattern {
	case "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach/renew",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/detach",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/take",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/return",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/approvals/{approvalId}/decision",
		"POST /api/sessions/{id}/control/end":
		personOnly = true
	}
	if p.Kind == tenant.Agent && (view == "terminal" || view == "expert" || personOnly) {
		writeReason(w, http.StatusForbidden, nativecontract.ReasonWrongPrincipal)
		return
	}
	writeReason(w, http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled)
}

func writeReason(w http.ResponseWriter, status int, reason nativecontract.Reason) {
	httpapi.WriteJSON(w, status, nativecontract.Error{Reason: reason, RequestID: nil, Retryable: false})
}
