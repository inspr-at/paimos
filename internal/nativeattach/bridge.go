// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

// PersonBridge supplies independent person authentication to the existing
// protected socket. Its outer wrapper must still verify bearer and kernel peer.
// Person writes go through the server's final mutation transaction before the
// owning host claims them. The core remains the only mode-change audit writer.
type PersonBridge struct {
	nativecontract.Host
	origin, tenant string
	http           *http.Client
	validator      *nativecontract.Validator
}

type personRequest struct {
	bridge          *PersonBridge
	token           string
	actor           nativecontract.Actor
	control, viewer string
}
type personRequestKey struct{}

func NewPersonBridge(origin, tenant string, host nativecontract.Host) (*PersonBridge, error) {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || !validID(tenant) || host == nil {
		return nil, errors.New("invalid native person authority configuration")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, errors.New("native person authority requires a secure instance")
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	hc := client.BoundedHTTP()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &PersonBridge{Host: host, origin: strings.TrimRight(origin, "/"), tenant: tenant, http: hc, validator: v}, nil
}

func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
func bridgeRefuse(reason nativecontract.Reason) error { return nativestream.Refuse(reason) }

func (b *PersonBridge) Authenticate(r *http.Request) (nativecontract.Actor, error) {
	var empty nativecontract.Actor
	if len(r.Header.Get("Cookie")) > 256 || len(r.Header.Values("Cookie")) != 1 || len(r.Header.Get(nativecontract.ControlHeader)) > 4096 || len(r.Header.Get(nativecontract.ViewerHeader)) > 36 {
		return empty, bridgeRefuse(nativecontract.ReasonGrantMissing)
	}
	cookies := r.CookiesNamed("aeon_session")
	if len(cookies) != 1 {
		return empty, bridgeRefuse(nativecontract.ReasonWrongPrincipal)
	}
	raw, err := hex.DecodeString(cookies[0].Value)
	defer clear(raw)
	if err != nil || len(raw) != 32 {
		return empty, bridgeRefuse(nativecontract.ReasonWrongPrincipal)
	}
	p := &personRequest{bridge: b, token: cookies[0].Value, control: r.Header.Get(nativecontract.ControlHeader), viewer: r.Header.Get(nativecontract.ViewerHeader)}
	var me client.Me
	if err = b.call(r.Context(), p, http.MethodGet, "/api/me", nil, "", &me); err != nil {
		return empty, err
	}
	if me.OwnerWorkstation || me.Principal.Kind != "person" || !validID(me.Principal.ID) || me.Principal.TenantID != b.tenant || me.Tenant.ID != b.tenant {
		return empty, bridgeRefuse(nativecontract.ReasonWrongPrincipal)
	}
	p.actor = nativecontract.Actor{TenantID: b.tenant, PrincipalID: me.Principal.ID, PrincipalKind: "person"}
	*r = *r.WithContext(context.WithValue(r.Context(), personRequestKey{}, p))
	return p.actor, nil
}

func (b *PersonBridge) person(ctx context.Context) (*personRequest, error) {
	p, ok := ctx.Value(personRequestKey{}).(*personRequest)
	if !ok || p.bridge != b {
		return nil, bridgeRefuse(nativecontract.ReasonWrongPrincipal)
	}
	return p, nil
}

// WithAccess checks the current binding before dispatch. Each supported Host
// mutation below independently obtains the final server write authorization.
// The production host has no qualified local read/PTY-write authority yet;
// authentication and a readable snapshot cannot substitute for those grants.
func (b *PersonBridge) WithAccess(ctx context.Context, actor nativecontract.Actor, in nativestream.LocalRequest, deliver func(nativecontract.ValidationContext) error) error {
	p, err := b.person(ctx)
	if err != nil {
		return err
	}
	if actor.PrincipalKind != "person" || actor.PrincipalID != p.actor.PrincipalID || actor.TenantID != b.tenant || len(actor.KeyCeiling) != 0 {
		return bridgeRefuse(nativecontract.ReasonWrongPrincipal)
	}
	switch in.Method {
	case "attach", "renew", "detach", "take", "return", "input", "interrupt", "stop":
	default:
		return bridgeRefuse(nativecontract.ReasonQualificationMissing)
	}
	binding := in.ExpectedBinding
	if binding == nil || binding.TenantID != b.tenant || binding.OwnedSessionID != in.OwnedSessionID {
		return bridgeRefuse(nativecontract.ReasonIdentityMismatch)
	}
	view, err := b.Snapshot(ctx, *binding)
	if err != nil {
		return err
	}
	return deliver(nativecontract.ValidationContext{Binding: view.CurrentBinding, OwnedSessionID: view.OwnedSessionID, Generation: view.Generation, PrincipalKind: "person", Capabilities: &view.Capabilities, Mode: view.Mode, State: view.State, View: in.View})
}

func publicPath(binding nativecontract.Binding, method string) string {
	path := "/api/projects/" + binding.ProjectID + "/native-sessions/" + binding.OwnedSessionID
	if method != "" {
		path += "/" + method
	}
	return path
}

func (b *PersonBridge) call(ctx context.Context, p *personRequest, method, path string, in any, schema string, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil || len(body) > nativecontract.RequestBodyBytes {
			return bridgeRefuse(nativecontract.ReasonBufferFull)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, b.origin+path, bytes.NewReader(body))
	if err != nil {
		return bridgeRefuse(nativecontract.ReasonInvalidInput)
	}
	r.AddCookie(&http.Cookie{Name: "aeon_session", Value: p.token})
	r.Header.Set(LocalPersonHeader, LocalPersonVersion)
	r.Header.Set("Origin", b.origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(nativecontract.ControlHeader, p.control)
	r.Header.Set(nativecontract.ViewerHeader, p.viewer)
	res, err := b.http.Do(r)
	if err != nil {
		return bridgeRefuse(nativecontract.ReasonGrantMissing)
	}
	defer res.Body.Close()
	limit := nativecontract.RequestBodyBytes
	if path == "/api/me" {
		limit = 16 << 10
	}
	raw, err := readBounded(res.Body, limit)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		if b.validator.ValidateJSON("NativeError", raw) == nil {
			var refusal nativecontract.Error
			_ = json.Unmarshal(raw, &refusal)
			return bridgeRefuse(refusal.Reason)
		}
		return bridgeRefuse(nativecontract.ReasonGrantMissing)
	}
	if schema != "" {
		if err = b.validator.ValidateJSON(schema, raw); err != nil {
			return safeValidation(err)
		}
	}
	if json.Unmarshal(raw, out) != nil {
		return bridgeRefuse(nativecontract.ReasonInvalidInput)
	}
	return nil
}

func (b *PersonBridge) Snapshot(ctx context.Context, binding nativecontract.Binding) (nativecontract.SessionView, error) {
	var out nativecontract.SessionView
	p, err := b.person(ctx)
	if err != nil {
		return out, err
	}
	if binding.TenantID != b.tenant || !validID(binding.ProjectID) || !validID(binding.OwnedSessionID) {
		return out, bridgeRefuse(nativecontract.ReasonIdentityMismatch)
	}
	err = b.call(ctx, p, http.MethodGet, publicPath(binding, ""), nil, "NativeSessionView", &out)
	if err == nil && (out.OwnedSessionID != binding.OwnedSessionID || out.CurrentBinding == nil || !out.CurrentBinding.Equal(binding)) {
		err = bridgeRefuse(nativecontract.ReasonWrongGeneration)
	}
	return out, err
}

func (b *PersonBridge) attach(ctx context.Context, binding nativecontract.Binding, method string, in any) (nativecontract.AttachResult, error) {
	var out nativecontract.AttachResult
	p, err := b.person(ctx)
	if err != nil {
		return out, err
	}
	err = b.call(ctx, p, http.MethodPost, publicPath(binding, method), in, "NativeAttachResult", &out)
	if err != nil {
		return out, err
	}
	if !out.Binding.Equal(binding) || out.Snapshot.CurrentBinding == nil || !out.Snapshot.CurrentBinding.Equal(binding) || out.StreamPath != publicPath(binding, "stream") {
		return nativecontract.AttachResult{}, bridgeRefuse(nativecontract.ReasonIdentityMismatch)
	}
	out.StreamPath = "/v1/native-sessions/" + binding.OwnedSessionID + "/stream"
	return out, nil
}
func (b *PersonBridge) Attach(ctx context.Context, in nativecontract.AttachIntent) (nativecontract.AttachResult, error) {
	return b.attach(ctx, in.ExpectedBinding, "attach", in)
}
func (b *PersonBridge) Renew(ctx context.Context, in nativecontract.AttachRenew) (nativecontract.AttachResult, error) {
	return b.attach(ctx, in.ExpectedBinding, "attach/renew", in)
}
func (b *PersonBridge) operation(ctx context.Context, binding nativecontract.Binding, method string, in any) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	p, err := b.person(ctx)
	if err != nil {
		return out, err
	}
	err = b.call(ctx, p, http.MethodPost, publicPath(binding, method), in, "NativeOperationResult", &out)
	return out, err
}
func (b *PersonBridge) Detach(ctx context.Context, in nativecontract.Detach) (nativecontract.OperationResult, error) {
	return b.operation(ctx, in.ExpectedBinding, "detach", in)
}
func (b *PersonBridge) Take(ctx context.Context, in nativecontract.TakeIntent) (nativecontract.OperationResult, error) {
	out, err := b.operation(ctx, in.ExpectedBinding, "take", in)
	if err != nil {
		return out, err
	}
	return b.Host.Take(ctx, in)
}
func (b *PersonBridge) Return(ctx context.Context, in nativecontract.ReturnIntent) (nativecontract.OperationResult, error) {
	out, err := b.operation(ctx, in.ExpectedBinding, "return", in)
	if err != nil {
		return out, err
	}
	return b.Host.Return(ctx, in)
}
func (b *PersonBridge) Input(ctx context.Context, in nativecontract.Input) (nativecontract.OperationResult, error) {
	out, err := b.operation(ctx, in.ExpectedBinding, "input", in)
	if err != nil {
		return out, err
	}
	return b.Host.Input(ctx, in)
}
func (b *PersonBridge) Interrupt(ctx context.Context, in nativecontract.Interrupt) (nativecontract.OperationResult, error) {
	out, err := b.operation(ctx, in.ExpectedBinding, "interrupt", in)
	if err != nil {
		return out, err
	}
	return b.Host.Interrupt(ctx, in)
}
func (b *PersonBridge) Stop(ctx context.Context, in nativecontract.Stop) (nativecontract.OperationResult, error) {
	out, err := b.operation(ctx, in.ExpectedBinding, "stop", in)
	if err != nil {
		return out, err
	}
	return b.Host.Stop(ctx, in)
}

var _ nativestream.LocalAuthority = (*PersonBridge)(nil)
var _ nativecontract.Host = (*PersonBridge)(nil)
