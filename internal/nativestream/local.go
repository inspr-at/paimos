// SPDX-License-Identifier: AGPL-3.0-only

package nativestream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// LocalRequest carries untrusted selectors only, never authority. The authority
// resolves the current binding independently and checks any supplied binding.
type LocalRequest struct {
	Method, OwnedSessionID, ApprovalID, ViewerID, View, ControlCapability string
	ExpectedBinding                                                       *nativecontract.Binding
	Envelope                                                              *nativecontract.LocalEnvelope
	Query                                                                 url.Values
}

// LocalAuthority is installed by the existing owner-socket server. Authenticate
// MUST verify its protected socket bearer AND kernel peer provenance. WithAccess
// checks the current server-issued intent/view/controller grant, peer/actor,
// expiry, exact binding and operation-specific step-up; socket access alone does
// not authorize native work. No inbound network listener is created here.
type LocalAuthority interface {
	Authenticate(*http.Request) (nativecontract.Actor, error)
	WithAccess(context.Context, nativecontract.Actor, LocalRequest, func(nativecontract.ValidationContext) error) error
}

type LocalAdapter struct {
	host      nativecontract.Host
	authority LocalAuthority
	projector *Projector
	clock     nativecontract.Clock
	validator *nativecontract.Validator
}

func NewLocalAdapter(host nativecontract.Host, authority LocalAuthority, projector *Projector, clock nativecontract.Clock) (*LocalAdapter, error) {
	if host == nil || authority == nil || projector == nil {
		return nil, Refuse(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	return &LocalAdapter{host: host, authority: authority, projector: projector, clock: clockOrDefault(clock), validator: v}, nil
}

// Mount is additive on the existing protected Unix-socket mux. It deliberately
// does not touch agentd's socket/token lifecycle or any legacy attachment route.
func (a *LocalAdapter) Mount(mux *http.ServeMux) {
	for _, route := range nativecontract.LocalRoutes() {
		mux.HandleFunc(route.Method+" "+route.Path, func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, route) })
	}
}

func (a *LocalAdapter) serve(w http.ResponseWriter, r *http.Request, route nativecontract.Route) {
	Headers(w)
	actor, err := a.authority.Authenticate(r)
	if err != nil || actor.TenantID == "" || actor.PrincipalID == "" || (actor.PrincipalKind != "person" && actor.PrincipalKind != "agent") {
		WriteError(w, &Failure{http.StatusUnauthorized, nativecontract.ReasonGrantMissing})
		return
	}
	if r.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
		WriteError(w, Refuse(nativecontract.ReasonUnsupported))
		return
	}
	if len(r.URL.RawQuery) > 4096 || len(r.Header.Get(nativecontract.ControlHeader)) > 4096 || len(r.Header.Get(nativecontract.ViewerHeader)) > 36 || len(r.Header.Get("Last-Event-ID")) > 64 {
		WriteError(w, Refuse(nativecontract.ReasonBufferFull))
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		WriteError(w, Refuse(nativecontract.ReasonInvalidInput))
		return
	}
	request := LocalRequest{Method: route.OperationID, OwnedSessionID: r.PathValue("ownedSessionId"), ApprovalID: r.PathValue("approvalId"), ViewerID: r.Header.Get(nativecontract.ViewerHeader), ControlCapability: r.Header.Get(nativecontract.ControlHeader), Query: query, View: query.Get("view")}
	if request.View == "" {
		request.View = "structured"
	}
	if request.View != "structured" && request.View != "expert" && request.View != "terminal" {
		WriteError(w, Refuse(nativecontract.ReasonUnsupported))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	var body []byte
	if route.Method == http.MethodPost {
		if len(query) > 0 {
			WriteError(w, Refuse(nativecontract.ReasonInvalidInput))
			return
		}
		body, err = readBody(w, r, nativecontract.RequestBodyBytes)
		if err != nil {
			WriteError(w, err)
			return
		}
		if err := a.validator.DecodeJSON("NativeLocalEnvelope", body, new(nativecontract.LocalEnvelope)); err != nil {
			WriteError(w, err)
			return
		}
		var envelope nativecontract.LocalEnvelope
		_ = json.Unmarshal(body, &envelope)
		request.ExpectedBinding = envelope.ExpectedBinding
		request.Envelope = &envelope
		if envelope.Method != route.OperationID || envelope.Protocol != nativecontract.LocalProtocol {
			WriteError(w, Refuse(nativecontract.ReasonUnsupported))
			return
		}
		if err := a.validator.ValidateJSON(route.RequestSchema, envelope.Params); err != nil {
			WriteError(w, err)
			return
		}
		if envelope.Method == "attach" {
			var attach nativecontract.AttachIntent
			_ = json.Unmarshal(envelope.Params, &attach)
			request.View = attach.View
		}
		// DecodeLocal later checks method/header/nested request-id agreement.
	} else if err := ValidateReadQuery(request, route.OperationID); err != nil {
		WriteError(w, err)
		return
	}
	if route.OperationID == "stream" && request.ViewerID == "" {
		WriteError(w, Refuse(nativecontract.ReasonGrantMissing))
		return
	}
	if controlMethod(route.OperationID) && request.ControlCapability == "" {
		WriteError(w, Refuse(nativecontract.ReasonGrantMissing))
		return
	}
	fence := func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
		return a.authority.WithAccess(ctx, actor, request, func(access nativecontract.ValidationContext) error {
			if access.PrincipalKind != actor.PrincipalKind || access.Binding != nil && access.Binding.TenantID != actor.TenantID || actor.PrincipalKind != "person" && request.View != "structured" {
				return Refuse(nativecontract.ReasonWrongPrincipal)
			}
			if route.OperationID != "start" && (access.Binding == nil || access.Binding.OwnedSessionID != request.OwnedSessionID) {
				return Refuse(nativecontract.ReasonIdentityMismatch)
			}
			if route.Method == http.MethodGet && !QueryMatches(request.Query, access) {
				return Refuse(nativecontract.ReasonWrongGeneration)
			}
			access.View = request.View
			return deliver(access)
		})
	}
	if route.OperationID == "stream" {
		var sub nativecontract.Subscription
		err = fence(ctx, func(access nativecontract.ValidationContext) error {
			after := query.Get("after")
			if header := r.Header.Get("Last-Event-ID"); header != "" {
				if after != "" && after != header {
					return Refuse(nativecontract.ReasonInvalidInput)
				}
				after = header
			}
			if after != "" {
				generation, _, err := nativecontract.ParseEventID(after)
				if err != nil {
					return err
				}
				if generation != access.Generation {
					return Refuse(nativecontract.ReasonWrongGeneration)
				}
			}
			var err error
			sub, err = a.host.Subscribe(r.Context(), *access.Binding, nativecontract.StreamQuery{After: after, View: request.View, Generation: access.Generation})
			return err
		})
		if err != nil {
			WriteError(w, err)
			return
		}
		if sub == nil {
			WriteError(w, Refuse(nativecontract.ReasonNativeFailure))
			return
		}
		ServeEvents(w, r, sub, fence, a.projector, a.clock)
		return
	}
	err = fence(ctx, func(access nativecontract.ValidationContext) error {
		var result any
		if route.Method == http.MethodPost {
			envelope, err := a.validator.DecodeLocal(body, r.Header.Get(nativecontract.LocalProtocolHeader), route.OperationID, access)
			if err != nil {
				return err
			}
			if request.ApprovalID != "" {
				var approval nativecontract.ApprovalDecision
				_ = json.Unmarshal(envelope.Params, &approval)
				if approval.ApprovalID != request.ApprovalID {
					return Refuse(nativecontract.ReasonIdentityMismatch)
				}
			}
			result, err = a.dispatch(ctx, envelope)
			if err != nil {
				return err
			}
		} else {
			switch route.OperationID {
			case "receipts":
				limit, _ := strconv.Atoi(query.Get("limit"))
				result, err = a.host.Receipts(ctx, *access.Binding, nativecontract.PageQuery{After: query.Get("after"), Limit: limit})
			case "events":
				limit, _ := strconv.Atoi(query.Get("limit"))
				result, err = a.host.EventsPage(ctx, *access.Binding, nativecontract.EventQuery{Before: query.Get("before"), Limit: limit, Generation: access.Generation})
			default:
				return Refuse(nativecontract.ReasonUnsupported)
			}
			if err != nil {
				return err
			}
		}
		result = projectResponse(result, actor.PrincipalKind)
		data, err := Encode(a.validator, route.ResponseSchema, result)
		if err != nil {
			return err
		}
		if route.OperationID != "start" {
			if err := a.validator.ValidateBoundJSON(route.ResponseSchema, data, access); err != nil {
				return err
			}
		}
		if page, ok := result.(nativecontract.EventPage); ok {
			for i, event := range page.Items {
				if !Allows(request.View, event.Kind) && event.Kind != "assistant.final" {
					return Refuse(nativecontract.ReasonUnsupported)
				}
				clean, err := a.projector.SanitizeHistory(ctx, event, access)
				if err != nil {
					return err
				}
				page.Items[i] = clean
			}
			data, err = Encode(a.validator, route.ResponseSchema, page)
			if err != nil {
				return err
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write(append(data, '\n'))
		return err
	})
	if err != nil {
		WriteError(w, err)
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int) ([]byte, error) {
	rc := http.NewResponseController(w)
	// The surrounding existing server also owns header/idle timeouts. This
	// read deadline bounds a slow body before any JSON work or Host invocation.
	if err := rc.SetReadDeadline(time.Now().Add(nativecontract.BodyWorkDeadlineSeconds * time.Second)); err != nil {
		return nil, Refuse(nativecontract.ReasonQualificationMissing)
	}
	defer rc.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, Refuse(nativecontract.ReasonBufferFull)
		}
		return nil, Refuse(nativecontract.ReasonInvalidInput)
	}
	return data, nil
}

func ValidateReadQuery(request LocalRequest, method string) error {
	allowed := map[string]bool{"expected_runtime_epoch": true, "expected_binding_revision": true, "expected_harness_session_id": true, "generation": true, "view": true}
	switch method {
	case "stream":
		allowed["after"] = true
	case "receipts":
		allowed["after"] = true
		allowed["limit"] = true
	case "events":
		allowed["before"] = true
		allowed["limit"] = true
	default:
		return Refuse(nativecontract.ReasonUnsupported)
	}
	for key, values := range request.Query {
		if !allowed[key] || len(values) != 1 || len(values[0]) > 512 {
			return Refuse(nativecontract.ReasonInvalidInput)
		}
	}
	for _, key := range []string{"expected_runtime_epoch", "expected_binding_revision", "expected_harness_session_id", "generation"} {
		if request.Query.Get(key) == "" {
			return Refuse(nativecontract.ReasonInvalidInput)
		}
	}
	if raw := request.Query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		max := nativecontract.ReceiptPageItems
		if method == "events" {
			max = nativecontract.EventPageItems
		}
		if err != nil || strconv.Itoa(limit) != raw || limit < 1 || limit > max {
			return Refuse(nativecontract.ReasonBufferFull)
		}
	}
	return nil
}

func QueryMatches(query url.Values, access nativecontract.ValidationContext) bool {
	return access.Binding != nil && query.Get("expected_runtime_epoch") == access.Binding.RuntimeEpoch && query.Get("expected_harness_session_id") == access.Binding.HarnessSessionID && query.Get("expected_binding_revision") == strconv.FormatInt(access.Binding.BindingRevision, 10) && query.Get("generation") == strconv.FormatInt(access.Generation, 10)
}

func controlMethod(method string) bool {
	return strings.Contains(" input interrupt stop take return pty_input resize approval control_end ", " "+method+" ")
}

func projectResponse(result any, kind string) any {
	switch value := result.(type) {
	case nativecontract.OperationResult:
		if value.Session != nil {
			view := nativecontract.PersonProjection(*value.Session, kind, false)
			value.Session = &view
		}
		return value
	case nativecontract.AttachResult:
		value.Snapshot = nativecontract.PersonProjection(value.Snapshot, kind, false)
		return value
	}
	return result
}

func (a *LocalAdapter) dispatch(ctx context.Context, envelope nativecontract.LocalEnvelope) (any, error) {
	switch envelope.Method {
	case "start":
		var value nativecontract.LocalStart
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Start(ctx, value)
	case "resume":
		var value nativecontract.ResumeIntent
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Resume(ctx, value)
	case "input":
		var value nativecontract.Input
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Input(ctx, value)
	case "interrupt":
		var value nativecontract.Interrupt
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Interrupt(ctx, value)
	case "stop":
		var value nativecontract.Stop
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Stop(ctx, value)
	case "attach":
		var value nativecontract.AttachIntent
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Attach(ctx, value)
	case "renew":
		var value nativecontract.AttachRenew
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Renew(ctx, value)
	case "detach":
		var value nativecontract.Detach
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Detach(ctx, value)
	case "take":
		var value nativecontract.TakeIntent
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Take(ctx, value)
	case "return":
		var value nativecontract.ReturnIntent
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Return(ctx, value)
	case "pty_input":
		var value nativecontract.PTYInput
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.PTYInput(ctx, value)
	case "resize":
		var value nativecontract.Resize
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Resize(ctx, value)
	case "approval":
		var value nativecontract.ApprovalDecision
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.Decide(ctx, value)
	case "control_end":
		var value nativecontract.EndControl
		_ = json.Unmarshal(envelope.Params, &value)
		return a.host.EndControl(ctx, value)
	}
	return nil, Refuse(nativecontract.ReasonUnsupported)
}
