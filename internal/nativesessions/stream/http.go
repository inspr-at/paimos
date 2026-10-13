// SPDX-License-Identifier: AGPL-3.0-only

package stream

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

// ReadAuthority authenticates the person/agent AND nonsecret attach viewer ID,
// independently resolves the current target, and supplies a request-scoped
// fence tied to that actor, exact view, consent and monotonic viewer expiry.
type ReadAuthority interface {
	AuthenticateViewer(*http.Request) (nativecontract.Binding, Viewer, error)
}

type HTTPHandler struct {
	relay     *Relay
	authority ReadAuthority
}

func NewHTTPHandler(relay *Relay, authority ReadAuthority) (*HTTPHandler, error) {
	if relay == nil || authority == nil {
		return nil, nativestream.Refuse(nativecontract.ReasonQualificationMissing)
	}
	return &HTTPHandler{relay: relay, authority: authority}, nil
}

// Stream is mounted by the I1/I2 module in place of C0's disabled handler. It
// never follows a replacement runtime on the client's behalf.
func (h *HTTPHandler) Stream(w http.ResponseWriter, r *http.Request) {
	nativestream.Headers(w)
	binding, viewer, err := h.authority.AuthenticateViewer(r)
	if err != nil {
		nativestream.WriteError(w, err)
		return
	}
	viewer.Fence = webFence(viewer.Fence)
	query, err := h.query(r, binding, viewer, "stream")
	if err != nil {
		nativestream.WriteError(w, err)
		return
	}
	after := query.Get("after")
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		if after != "" && last != after {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonInvalidInput))
			return
		}
		after = last
	}
	viewer.Query.After = after
	sub, err := h.relay.SubscribeView(r.Context(), binding, viewer)
	if err != nil {
		nativestream.WriteError(w, err)
		return
	}
	nativestream.ServeEvents(w, r, sub, viewer.Fence, h.relay.projector, h.relay.clock)
}

// LivePage exposes only bounded transient replay to the owning module. The
// durable events endpoint must additionally use its authorized history store;
// these pages never claim that token/Expert/PTY data is durable or complete.
func (h *HTTPHandler) LivePage(w http.ResponseWriter, r *http.Request) {
	nativestream.Headers(w)
	binding, viewer, err := h.authority.AuthenticateViewer(r)
	if err != nil {
		nativestream.WriteError(w, err)
		return
	}
	viewer.Fence = webFence(viewer.Fence)
	query, err := h.query(r, binding, viewer, "events")
	if err != nil {
		nativestream.WriteError(w, err)
		return
	}
	// Keep the delivery fence through serialization and Write. Page itself also
	// rechecks, but returns before this final response and cannot authorize it.
	var page nativecontract.EventPage
	limit, _ := strconv.Atoi(query.Get("limit"))
	page, err = h.relay.Page(r.Context(), binding, nativecontract.EventQuery{Before: query.Get("before"), Limit: limit, Generation: viewer.Query.Generation}, viewer)
	if err != nil {
		nativestream.WriteError(w, err)
		return
	}
	err = viewer.Fence(r.Context(), func(access nativecontract.ValidationContext) error {
		data, err := nativestream.Encode(h.relay.validator, "NativeEventPage", page)
		if err != nil {
			return err
		}
		if err := h.relay.validator.ValidateBoundJSON("NativeEventPage", data, access); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write(append(data, '\n'))
		return err
	})
	if err != nil {
		nativestream.WriteError(w, err)
	}
}

func webFence(fence nativestream.Fence) nativestream.Fence {
	if fence == nil {
		return nil
	}
	return func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
		return fence(ctx, func(access nativecontract.ValidationContext) error {
			if access.Private {
				return nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
			}
			access.Web = true
			return deliver(access)
		})
	}
}

func (h *HTTPHandler) query(r *http.Request, binding nativecontract.Binding, viewer Viewer, method string) (url.Values, error) {
	if r.Method != http.MethodGet || len(r.URL.RawQuery) > 4096 || len(r.Header.Get("Last-Event-ID")) > 64 {
		return nil, nativestream.Refuse(nativecontract.ReasonInvalidInput)
	}
	if len(r.Header.Values(nativecontract.ViewerHeader)) != 1 || len(r.Header.Get(nativecontract.ViewerHeader)) != 36 || viewer.Fence == nil {
		return nil, nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	if r.PathValue("ownedSessionId") != binding.OwnedSessionID || r.PathValue("projectId") != binding.ProjectID {
		return nil, nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, nativestream.Refuse(nativecontract.ReasonInvalidInput)
	}
	view := query.Get("view")
	if view == "" {
		view = "structured"
	}
	if view != viewer.Query.View {
		return nil, nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
	}
	if err := nativestream.ValidateReadQuery(nativestream.LocalRequest{Query: query}, method); err != nil {
		return nil, err
	}
	err = viewer.Fence(r.Context(), func(access nativecontract.ValidationContext) error {
		if access.Private || access.Binding == nil || !access.Binding.Equal(binding) || access.Generation != viewer.Query.Generation || access.View != view {
			return nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
		}
		if !nativestream.QueryMatches(query, access) {
			return nativestream.Refuse(nativecontract.ReasonWrongGeneration)
		}
		return nil
	})
	return query, err
}
