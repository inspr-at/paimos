// SPDX-License-Identifier: AGPL-3.0-only

// Package codex implements the qualified native app-server codec. Process
// ownership, admission, grant issuance and exit observation belong to agentd.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	n "github.com/inspr-at/paimos/internal/nativecontract"
)

// ProtocolPin identifies this codec, not a claim of live binary qualification.
// I2 must check the official binary digest/version before opening its wire.
const ProtocolPin = "codex-app-server/0.157.1"

var (
	ErrQualification = errors.New("Codex native qualification missing")
	ErrIdentity      = errors.New("Codex native account identity mismatch")
	ErrBinding       = errors.New("Codex native binding mismatch")
	ErrPolicy        = errors.New("Codex native policy mismatch")
	ErrProtocol      = errors.New("Codex native protocol evidence invalid")
	ErrUncertain     = errors.New("Codex native outcome unconfirmed")
	ErrBusy          = errors.New("Codex native turn still active")
	ErrTurn          = errors.New("Codex native expected turn mismatch")
	ErrExpired       = errors.New("Codex native authority expired")
	ErrBuffer        = errors.New("Codex native event buffer full")
)

// WorkerGrant is a local qualification from the existing actor
// grant. It is not a browser option or a new grant issuer. The resolver must
// qualify inherited hooks/MCP/settings as well as the filesystem sandbox.
// Routine, confined and verification profiles cannot use this codec.
type WorkerGrant struct {
	Binding                n.Binding
	Policy                 n.Policy
	Binary                 n.BinaryPin
	AccountID              string
	ProfileBindingRevision int64
	EnrolledEmail          string
	Profile                string
	ApprovalPolicy         string
	Sandbox                string
	QualificationDigest    string
}

// GrantSource revalidates current authority and profile identity. I2 implements
// it using existing bounded grants, including revocation and lifetime/budget
// fences. The adapter calls it under its serialized native-I/O lock, including
// after account/read and immediately before each write/final publication.
type GrantSource interface {
	Authorize(context.Context, n.LaunchSpec) (WorkerGrant, error)
}

// Plan describes one child; it never launches, supervises or reads credentials.
// The existing launchWire adapter verifies physical paths and the binary pin,
// enforces policy/owned-writer exclusion and returns the exact owned process.
type Plan struct {
	Binary      n.BinaryPin
	Args        []string
	Environment []string
	Cwd         string
	OwnedWriter string
	Binding     n.Binding
	Lifetime    context.Context
}

// Wire is an adapter over agentd's existing launchWire. Call returns only a
// correlated RPC result; JSON-RPC errors must return error. Frames contains
// notifications/server requests from THIS connection, each <=1 MiB. Its
// bounded reader must not wait for frame consumption to dispatch RPC replies;
// overflow/EOF loses ownership evidence. Close/Wait return observed root/group
// exit, never infer exit from a completed turn or a closed pipe.
type Wire interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
	Notify(context.Context, string, json.RawMessage) error
	Reply(context.Context, json.RawMessage, json.RawMessage) error
	Frames() <-chan json.RawMessage
	Identity() n.NativeIdentity
	Close(context.Context) (n.ExitEvidence, error)
	Wait(context.Context) (n.ExitEvidence, error)
}

type WireOpener interface {
	Open(context.Context, Plan) (Wire, error)
}

// Launcher has no default process/authority implementation. Unwired and
// unqualified instances refuse before possible fork.
type Launcher struct {
	Wires  WireOpener
	Grants GrantSource
	Clock  n.Clock
}

var _ n.Launcher = (*Launcher)(nil)

func sameBinding(a, b n.Binding) bool { return reflect.DeepEqual(a, b) }
func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func (l *Launcher) Open(ctx context.Context, spec n.LaunchSpec) (n.NativeTransport, error) {
	if l.Wires == nil || l.Grants == nil || l.Clock == nil {
		return nil, ErrQualification
	}
	ctx, cancelOpen := context.WithTimeout(ctx, 20*time.Second)
	defer cancelOpen()
	if ctx.Err() != nil || spec.Lifetime != nil && spec.Lifetime.Err() != nil {
		return nil, ErrExpired
	}
	spec = cloneSpec(spec)
	grant, err := l.Grants.Authorize(ctx, spec)
	if err != nil {
		return nil, ErrQualification
	}
	if grant.Binding.RunID != nil {
		id := *grant.Binding.RunID
		grant.Binding.RunID = &id
	}
	plan, err := launchPlan(spec, grant, l.Clock.Now())
	if err != nil {
		return nil, err
	}
	validator, err := n.NewValidator()
	if err != nil {
		return nil, ErrProtocol
	}
	wire, err := l.Wires.Open(ctx, plan)
	if err != nil {
		return nil, ErrUncertain
	}
	s := &transport{wire: wire, grants: l.Grants, clock: l.Clock, spec: spec, grant: grant, validator: validator,
		events: make(chan n.ProviderEvent, 32), readerDone: make(chan struct{}), approvals: make(map[string]pendingApproval)}
	s.identity = wire.Identity()
	if s.identity.Provider != "codex" || s.identity.RuntimeEpoch != spec.Binding.RuntimeEpoch ||
		s.identity.PID <= 0 || !digest(s.identity.ProcessIdentityDigest) || s.identity.StartedAt.IsZero() {
		err = ErrBinding
	} else {
		err = s.initialize(ctx)
	}
	if err != nil {
		// Failure never abandons a possibly live writer. The wire/supervisor keeps
		// its occupancy until this close has real exit evidence.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		exit, closeErr := wire.Close(closeCtx)
		if closeErr != nil || !s.validExit(exit) {
			return nil, ErrUncertain
		}
		return nil, err
	}
	go s.readFrames()
	if spec.InitialInput != nil {
		if _, err := s.Send(ctx, *spec.InitialInput); err != nil {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			if _, closeErr := s.Close(closeCtx); closeErr != nil {
				return nil, ErrUncertain
			}
			return nil, err
		}
	}
	return s, nil
}

func cloneSpec(s n.LaunchSpec) n.LaunchSpec {
	s.Profile.Environment = append([]string(nil), s.Profile.Environment...)
	s.Tools.Token = append([]byte(nil), s.Tools.Token...)
	if s.Binding.RunID != nil {
		v := *s.Binding.RunID
		s.Binding.RunID = &v
	}
	if s.Options.Effort != nil {
		v := *s.Options.Effort
		s.Options.Effort = &v
	}
	if s.InitialInput != nil {
		v := *s.InitialInput
		if v.ExpectedBinding.RunID != nil {
			id := *v.ExpectedBinding.RunID
			v.ExpectedBinding.RunID = &id
		}
		s.InitialInput = &v
	}
	return s
}

func launchPlan(s n.LaunchSpec, g WorkerGrant, now time.Time) (Plan, error) {
	v, err := n.NewValidator()
	if err != nil {
		return Plan{}, ErrProtocol
	}
	for _, field := range []struct {
		schema string
		value  any
	}{{"NativeBinding", s.Binding}, {"NativePolicy", s.Policy}, {"NativeLaunchConfig", s.Options}} {
		if v.ValidateJSON(field.schema, encode(field.value)) != nil {
			return Plan{}, ErrPolicy
		}
	}
	if g.Profile != "native_worker" || !digest(g.QualificationDigest) {
		return Plan{}, ErrQualification
	}
	if !sameBinding(s.Binding, g.Binding) || s.Binding.RuntimeEpoch == "" || s.OwnedWriterID == "" ||
		s.Profile.AccountID != g.AccountID || s.Profile.ProfileBindingRevision != g.ProfileBindingRevision {
		return Plan{}, ErrBinding
	}
	if s.Policy != g.Policy || s.Options.ToolPolicyDigest != s.Policy.ToolPolicyDigest ||
		s.Options.SettingsPolicyDigest != s.Policy.SettingsPolicyDigest || !digest(s.Policy.ToolPolicyDigest) ||
		!digest(s.Policy.SettingsPolicyDigest) || s.Policy.GrantID == "" || s.Policy.GrantRevision < 0 ||
		s.Policy.MaximumRuntimeSeconds < 1 || s.Policy.MaximumTurns < 1 || s.Policy.MaximumTokens < 1 ||
		s.Tools.URL != "" || len(s.Tools.Token) != 0 || s.Mode != "structured" {
		return Plan{}, ErrPolicy
	}
	if !s.Policy.ExpiresAt.After(now) {
		return Plan{}, ErrExpired
	}
	// Only supervised, already-qualified native workers. A label/sandbox does
	// not qualify a bounded-preapproved tool or routine execution ceiling.
	if s.Policy.PermissionPolicy != "supervised" || g.ApprovalPolicy != "onRequest" ||
		(g.Sandbox != "readOnly" && g.Sandbox != "workspaceWrite") ||
		(s.Policy.LocalHistory != "vendor_native" && s.Policy.LocalHistory != "disabled") {
		return Plan{}, ErrQualification
	}
	if s.ResumeRef != "" && s.Policy.LocalHistory != "vendor_native" {
		return Plan{}, ErrPolicy
	}
	if strings.TrimSpace(g.EnrolledEmail) == "" {
		return Plan{}, ErrIdentity
	}
	if s.Profile.Binary != g.Binary || s.Profile.Binary.Protocol != ProtocolPin ||
		s.Profile.Binary.Version != "0.157.1" || !digest(s.Profile.Binary.SHA256) || !filepath.IsAbs(s.Profile.Binary.Path) {
		return Plan{}, ErrQualification
	}
	if !filepath.IsAbs(s.Cwd) || !filepath.IsAbs(s.Profile.WorkspaceRoot) ||
		!filepath.IsAbs(s.Profile.AccountHome) || !filepath.IsAbs(s.Profile.UserHome) || s.Lifetime == nil {
		return Plan{}, ErrPolicy
	}
	rel, err := filepath.Rel(s.Profile.WorkspaceRoot, s.Cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Plan{}, ErrPolicy
	}
	if s.Options.Model == "" || len(s.Options.Model) > 128 || s.Options.ServiceTier != "default" ||
		(s.Options.Effort != nil && !strings.Contains("|low|medium|high|xhigh|max|", "|"+*s.Options.Effort+"|")) {
		return Plan{}, ErrQualification
	}
	if s.ResumeRef != "" && !validRef(s.ResumeRef) {
		return Plan{}, ErrProtocol
	}
	// Never inherit ambient credentials or alternate providers. Profile setup
	// owns the login; no auth file, token or OAuth lifecycle is read here.
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + s.Profile.UserHome, "CODEX_HOME=" + s.Profile.AccountHome}
	for _, entry := range s.Profile.Environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || (key != "LANG" && key != "LC_ALL" && key != "TMPDIR") || len(entry) > 4096 || strings.ContainsRune(entry, 0) {
			return Plan{}, ErrPolicy
		}
		env = append(env, entry)
	}
	return Plan{Binary: s.Profile.Binary, Args: []string{"app-server", "--listen", "stdio://"}, Environment: env,
		Cwd: s.Cwd, OwnedWriter: s.OwnedWriterID, Binding: s.Binding, Lifetime: s.Lifetime}, nil
}
