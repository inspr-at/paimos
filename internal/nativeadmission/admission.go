// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeadmission guards local admission; I2 owns production wiring.
// It never launches vendor binaries, resolves credentials or rotates accounts.
package nativeadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

const maxRecords = 4096
const operationTimeout = 3 * time.Second

// Request is trusted owning-supervisor context, never decoded from an HTTP
// body. Login comes from the existing enrollment-bound identity projection.
// Binding.RunID distinguishes workers from roots; both consume ONE local slot.
type Request struct {
	Binding              nativecontract.Binding
	Profile              nativecontract.ProfileRequest
	Launch               nativecontract.LaunchConfig
	Login                agentsetup.LedgerLogin
	AccountBindingDigest string // NativeAccountBinding.Digest(), supplied by current server authority
}

func (Request) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native admission request is local authority")
}
func (*Request) UnmarshalJSON([]byte) error {
	return errors.New("native admission request cannot be decoded from JSON")
}

// Authority must re-read current identity, account/context/model/capacity and
// immutable actor grant in the final server transaction under access fences.
// It excludes only this request's already-held reservation. A cached route or
// a final-drain grant is insufficient. Profile changes require current explicit
// person/OPS authority; admission itself never authorizes logout or login.
type Authority interface {
	Revalidate(context.Context, Request) error
	AuthorizeProfileChange(context.Context, nativecontract.ProfileRequest) error
}

type Options struct {
	Ledger               *agentsetup.SharedLedger
	State                *agentsetup.Store // dedicated private admission directory
	Profiles             nativecontract.ProfileResolver
	Authority            Authority
	MemberID, Generation string
	Now                  func() time.Time
}

type Gate struct {
	opts      Options
	validator *nativecontract.Validator
	mutex     chan struct{}
}

// Permit carries no renewable launch authority and is never a public DTO.
type Permit struct {
	request Request
	group   string
	digest  string
}

func (*Permit) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native permit is local authority")
}

// record is a bounded content-free PRIVATE journal, not another session log.
// The released tombstone keeps a conversation pinned to its original account.
// Process identity is evidence only; reading it never adopts or signals a PID.
type record struct {
	Version              int                    `json:"version"`
	Binding              nativecontract.Binding `json:"binding"`
	AccountID            string                 `json:"account_id"`
	AccountKey           string                 `json:"account_key"`
	OwnerPersonID        string                 `json:"owner_person_id"`
	WorkspaceID          string                 `json:"workspace_id"`
	Harness              string                 `json:"harness"`
	ProfileRevision      int64                  `json:"profile_revision"`
	ProfileDigest        string                 `json:"profile_digest"`
	RequestDigest        string                 `json:"request_digest"`
	AccountBindingDigest string                 `json:"account_binding_digest"`
	Login                agentsetup.LedgerLogin `json:"login"`
	MemberID             string                 `json:"member_id"`
	Generation           string                 `json:"ledger_generation"`
	Group                string                 `json:"group"`
	State                string                 `json:"state"`
	Attempted            bool                   `json:"attempted"`
	IdentityDigest       string                 `json:"process_identity_digest"`
	PID                  int                    `json:"pid"`
	StartedAt            time.Time              `json:"started_at"`
}

func denial(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func New(opts Options) (*Gate, error) {
	if opts.Ledger == nil || opts.State == nil || opts.Profiles == nil || opts.Authority == nil ||
		!hexID(opts.MemberID, 32) || !uuid(opts.Generation) {
		return nil, denial(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	g := &Gate{opts: opts, validator: v, mutex: make(chan struct{}, 1)}
	g.mutex <- struct{}{}
	return g, nil
}

func hexID(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func uuid(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' && hexID(strings.ReplaceAll(s, "-", ""), 32)
}

func (g *Gate) validate(r Request) error {
	// Bound local input before marshaling the C0 schemas.
	fields := []string{r.Binding.TenantID, r.Binding.ProjectID, r.Binding.OwnedSessionID,
		r.Binding.HarnessSessionID, r.Binding.RuntimeEpoch, r.Binding.DaemonID, r.Binding.DaemonGeneration,
		r.Profile.Policy.GrantID, r.Profile.Policy.ToolPolicyDigest, r.Profile.Policy.SettingsPolicyDigest,
		r.Profile.Policy.PermissionPolicy, r.Profile.Policy.LocalHistory, r.Profile.Policy.Continuation,
		r.Launch.ModelProfileID, r.Launch.Model, r.Launch.ServiceTier, r.Launch.ToolPolicyDigest, r.Launch.SettingsPolicyDigest}
	if r.Binding.RunID != nil {
		fields = append(fields, *r.Binding.RunID)
	}
	if r.Launch.Effort != nil {
		fields = append(fields, *r.Launch.Effort)
	}
	for _, value := range fields {
		if len(value) > 256 {
			return denial(nativecontract.ReasonInvalidInput)
		}
	}
	for _, id := range []string{r.Profile.TenantID, r.Profile.OwnerPersonID, r.Profile.AccountID, r.Profile.WorkspaceID} {
		if !uuid(id) {
			return denial(nativecontract.ReasonInvalidInput)
		}
	}
	if r.Binding.TenantID != r.Profile.TenantID || r.Profile.BindingRevision < 1 ||
		len(r.Profile.AccountKey) < 1 || len(r.Profile.AccountKey) > 128 ||
		strings.ContainsAny(r.Profile.AccountKey, "\x00\r\n") ||
		r.Login.Harness != r.Profile.Harness || len(r.Profile.Harness) < 1 || len(r.Profile.Harness) > 32 ||
		!(r.Login.Identity == "unknown" || hexID(r.Login.Identity, 64)) ||
		!hexID(r.AccountBindingDigest, 64) ||
		r.Launch.ToolPolicyDigest != r.Profile.Policy.ToolPolicyDigest ||
		r.Launch.SettingsPolicyDigest != r.Profile.Policy.SettingsPolicyDigest {
		return denial(nativecontract.ReasonIdentityMismatch)
	}
	for _, item := range []struct {
		name  string
		value any
	}{
		{"NativeBinding", r.Binding}, {"NativePolicy", r.Profile.Policy}, {"NativeLaunchConfig", r.Launch},
	} {
		data, err := json.Marshal(item.value)
		if err != nil {
			return err
		}
		if err = g.validator.ValidateJSON(item.name, data); err != nil {
			return err
		}
	}
	if !g.opts.Now().Before(r.Profile.Policy.ExpiresAt) {
		return denial(nativecontract.ReasonGrantExpired)
	}
	return nil
}

// Hash the resolver's local-only fields without serializing them into a public
// DTO or retaining homes/environment in the journal. Resolver owns filesystem,
// identity and binary-pin qualification; this package does not inspect auth.
func profileDigest(p nativecontract.LocalProfile) string {
	data, _ := json.Marshal([]any{p.AccountID, p.AccountHome, p.UserHome, p.WorkspaceRoot,
		p.ConversationRoot, p.Environment, p.Binary.Path, p.Binary.SHA256, p.Binary.Version,
		p.Binary.Protocol, p.SDK.Path, p.SDK.SHA256, p.SDK.Version, p.SDK.Protocol, p.ProfileBindingRevision})
	return agentsetup.Hash(data)
}

func requestDigest(r Request) string {
	data, _ := json.Marshal([]any{r.Binding, r.Profile.TenantID, r.Profile.OwnerPersonID,
		r.Profile.AccountID, r.Profile.AccountKey, r.Profile.WorkspaceID, r.Profile.Harness,
		r.Profile.Policy, r.Profile.BindingRevision, r.Launch, r.Login, r.AccountBindingDigest})
	return agentsetup.Hash(data)
}

func cloneRequest(r Request) Request {
	if r.Binding.RunID != nil {
		value := *r.Binding.RunID
		r.Binding.RunID = &value
	}
	if r.Launch.Effort != nil {
		value := *r.Launch.Effort
		r.Launch.Effort = &value
	}
	return r
}

func (g *Gate) locked(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.mutex:
	}
	defer func() { g.mutex <- struct{}{} }()
	lock, err := g.opts.State.LockNamed("admission.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn(ctx)
}

func recordName(session string) string { return "session-" + session + ".json" }

func (g *Gate) read(session string) (record, error) {
	var r record
	if !uuid(session) {
		return r, denial(nativecontract.ReasonInvalidInput)
	}
	data, err := g.opts.State.Read(recordName(session), 8192)
	if err != nil {
		return r, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF || r.Version != 1 ||
		r.Binding.OwnedSessionID != session || !hexID(r.Group, 32) || !hexID(r.RequestDigest, 64) || !hexID(r.ProfileDigest, 64) || !hexID(r.AccountBindingDigest, 64) {
		return r, denial(nativecontract.ReasonOwnershipUnconfirmed)
	}
	switch r.State {
	case "reserved", "possible_fork", "running", "idle", "taken", "exit_unconfirmed", "released":
	default:
		return r, denial(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return r, nil
}

func (g *Gate) write(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data) > 8192 {
		return denial(nativecontract.ReasonBufferFull)
	}
	return g.opts.State.Write(recordName(r.Binding.OwnedSessionID), data, false)
}

func (g *Gate) resolve(ctx context.Context, r Request) (nativecontract.LocalProfile, error) {
	p, err := g.opts.Profiles.Resolve(ctx, r.Profile)
	if err != nil {
		return p, err
	}
	if p.AccountID != r.Profile.AccountID || p.ProfileBindingRevision != r.Profile.BindingRevision {
		return p, denial(nativecontract.ReasonIdentityMismatch)
	}
	if err = boundProfile(p); err != nil {
		return p, err
	}
	return p, nil
}

func boundProfile(p nativecontract.LocalProfile) error {
	if len(p.Environment) > 256 {
		return denial(nativecontract.ReasonBufferFull)
	}
	size := len(p.AccountID) + len(p.AccountHome) + len(p.UserHome) + len(p.WorkspaceRoot) + len(p.ConversationRoot)
	for _, value := range []string{p.Binary.Path, p.Binary.SHA256, p.Binary.Version, p.Binary.Protocol,
		p.SDK.Path, p.SDK.SHA256, p.SDK.Version, p.SDK.Protocol} {
		size += len(value)
	}
	for _, value := range p.Environment {
		size += len(value)
	}
	if size > 64<<10 {
		return denial(nativecontract.ReasonBufferFull)
	}
	return nil
}

// Reserve takes the existing SHARED machine/login ledger slot before final
// server admission. It never widens the one pinned account to a candidate pool.
// Lost writes/replies retain occupancy; no optimistic release on an error.
func (g *Gate) Reserve(ctx context.Context, request Request) (*Permit, error) {
	r := cloneRequest(request)
	if err := g.validate(r); err != nil {
		return nil, err
	}
	var permit *Permit
	err := g.locked(ctx, func(ctx context.Context) error {
		if err := g.profileClear(r.Profile.AccountID); err != nil {
			return err
		}
		old, err := g.read(r.Binding.OwnedSessionID)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if exists {
			// Alternate-account history portability is unqualified, including a
			// stopped conversation. A new account requires a NEW conversation.
			if old.AccountID != r.Profile.AccountID || old.AccountKey != r.Profile.AccountKey ||
				old.OwnerPersonID != r.Profile.OwnerPersonID || old.WorkspaceID != r.Profile.WorkspaceID ||
				old.Harness != r.Profile.Harness || old.Binding.TenantID != r.Binding.TenantID || old.Binding.ProjectID != r.Binding.ProjectID ||
				old.AccountBindingDigest != r.AccountBindingDigest || !sameRun(old.Binding.RunID, r.Binding.RunID) {
				return denial(nativecontract.ReasonUnsupported)
			}
			if old.State != "released" {
				if old.State != "reserved" || old.RequestDigest != requestDigest(r) {
					return denial(nativecontract.ReasonOwnershipUnconfirmed)
				}
				permit = &Permit{r, old.Group, old.RequestDigest}
				_, _, err = g.current(ctx, permit)
				return err
			}
			if old.Binding.RuntimeEpoch == r.Binding.RuntimeEpoch || old.Binding.HarnessSessionID == r.Binding.HarnessSessionID || r.Binding.BindingRevision <= old.Binding.BindingRevision {
				return denial(nativecontract.ReasonWrongGeneration)
			}
		}
		names, err := g.opts.State.Names(maxRecords)
		if err != nil {
			return err
		}
		if !exists && len(names) >= maxRecords {
			return denial(nativecontract.ReasonBufferFull)
		}
		profile, err := g.resolve(ctx, r)
		if err != nil {
			return err
		}
		group, err := agentsetup.LedgerID()
		if err != nil {
			return err
		}
		selected, err := g.opts.Ledger.Acquire(g.opts.MemberID, g.opts.Generation, group, []agentsetup.LedgerCandidate{{AccountID: r.Profile.AccountID, Login: r.Login}})
		if err != nil {
			return err
		}
		if len(selected) != 1 || selected[0].AccountID != r.Profile.AccountID {
			return denial(nativecontract.ReasonIdentityMismatch)
		}
		digest := requestDigest(r)
		saved := record{Version: 1, Binding: r.Binding, AccountID: r.Profile.AccountID, AccountKey: r.Profile.AccountKey,
			OwnerPersonID: r.Profile.OwnerPersonID, WorkspaceID: r.Profile.WorkspaceID, Harness: r.Profile.Harness,
			ProfileRevision: r.Profile.BindingRevision, ProfileDigest: profileDigest(profile), RequestDigest: digest, AccountBindingDigest: r.AccountBindingDigest, Login: r.Login,
			MemberID: g.opts.MemberID, Generation: g.opts.Generation, Group: group, State: "reserved"}
		if err = g.write(saved); err != nil {
			return err
		}
		permit = &Permit{r, group, digest}
		return g.opts.Authority.Revalidate(ctx, cloneRequest(r))
	})
	// The permit lets the owner prove a pre-fork abandonment even if the final
	// server check refused. Callers must check err; it never authorizes a start.
	return permit, err
}

func (g *Gate) current(ctx context.Context, p *Permit) (record, nativecontract.LocalProfile, error) {
	var profile nativecontract.LocalProfile
	if p == nil || p.digest != requestDigest(p.request) {
		return record{}, profile, denial(nativecontract.ReasonIdentityMismatch)
	}
	r, err := g.read(p.request.Binding.OwnedSessionID)
	if err != nil {
		return r, profile, err
	}
	if r.RequestDigest != p.digest || r.Group != p.group || r.MemberID != g.opts.MemberID || r.Generation != g.opts.Generation || r.State == "released" || !sameBinding(r.Binding, p.request.Binding) {
		return r, profile, denial(nativecontract.ReasonWrongGeneration)
	}
	if err = g.opts.Ledger.CheckGroup(r.MemberID, r.Generation, r.Group); err != nil {
		return r, profile, err
	}
	data, _, err := g.opts.Ledger.Snapshot()
	if err != nil {
		return r, profile, err
	}
	group := data.Groups[r.Group]
	if data.Generation != r.Generation || group.Instance != r.MemberID || group.Generation != r.Generation ||
		len(group.Holds) != 1 || group.Holds[0] != p.request.Login || r.Login != p.request.Login {
		return r, profile, denial(nativecontract.ReasonIdentityMismatch)
	}
	if r.State == "reserved" && group.State != "pending" && group.State != "claimed" ||
		r.State == "possible_fork" && group.State != "launching" ||
		(r.State == "running" || r.State == "idle" || r.State == "taken") &&
			(group.State != "running" || group.PID != r.PID || !group.StartedAt.Equal(r.StartedAt)) {
		return r, profile, denial(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err = g.validate(p.request); err != nil {
		return r, profile, err
	}
	profile, err = g.resolve(ctx, p.request)
	if err != nil {
		return r, profile, err
	}
	if profileDigest(profile) != r.ProfileDigest {
		return r, profile, denial(nativecontract.ReasonIdentityMismatch)
	}
	if err = g.opts.Authority.Revalidate(ctx, cloneRequest(p.request)); err != nil {
		return r, profile, err
	}
	return r, profile, nil
}

// Input runs the owning transport write only after current profile/authority
// checks. It cannot write a structured stream while a person owns the PTY.
func (g *Gate) Input(ctx context.Context, p *Permit, send func(context.Context) error) error {
	if send == nil {
		return denial(nativecontract.ReasonInvalidInput)
	}
	return g.locked(ctx, func(ctx context.Context) error {
		r, _, err := g.current(ctx, p)
		if err != nil {
			return err
		}
		if r.State != "running" && r.State != "idle" {
			return denial(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return send(ctx)
	})
}

// State changes display/lifecycle state, never occupancy. Turn completion uses
// idle, takeover uses taken; unknown fork/exit remains held even after expiry.
func (g *Gate) State(ctx context.Context, p *Permit, state string) error {
	switch state {
	case "running", "idle", "taken", "exit_unconfirmed":
	default:
		return denial(nativecontract.ReasonInvalidInput)
	}
	return g.locked(ctx, func(ctx context.Context) error {
		r, err := g.owned(p)
		if err != nil {
			return err
		}
		if !r.Attempted || r.State == "released" || state != "exit_unconfirmed" && r.IdentityDigest == "" {
			return denial(nativecontract.ReasonOwnershipUnconfirmed)
		}
		if r.State == "exit_unconfirmed" && state != "exit_unconfirmed" || r.State == "taken" && state != "taken" && state != "exit_unconfirmed" {
			return denial(nativecontract.ReasonOwnershipUnconfirmed)
		}
		r.State = state
		return g.write(r)
	})
}

func sameRun(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func sameBinding(a, b nativecontract.Binding) bool {
	if !sameRun(a.RunID, b.RunID) {
		return false
	}
	a.RunID, b.RunID = nil, nil
	return a == b
}

func (g *Gate) owned(p *Permit) (record, error) {
	if p == nil || p.digest != requestDigest(p.request) {
		return record{}, denial(nativecontract.ReasonIdentityMismatch)
	}
	r, err := g.read(p.request.Binding.OwnedSessionID)
	if err == nil && (r.RequestDigest != p.digest || r.Group != p.group || r.MemberID != g.opts.MemberID || r.Generation != g.opts.Generation || !sameBinding(r.Binding, p.request.Binding)) {
		err = denial(nativecontract.ReasonWrongGeneration)
	}
	return r, err
}
