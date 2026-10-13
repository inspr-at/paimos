// SPDX-License-Identifier: AGPL-3.0-only

// Package store persists native starts. Production mounting is owned by I1;
// construction alone does not enable a provider or automatic routine start.
package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Authority must read the existing immutable actor/tool/settings/workspace
// grant in THIS transaction, including revocation, limits and owner consent.
// It must also validate model activation, policy pins and delegated worker
// ancestry. It performs no I/O, appends no events and never launches a process.
// There is deliberately no permissive/default implementation.
type Authority interface {
	AuthorizeTx(context.Context, pgx.Tx, nativecontract.Actor, string, nativecontract.StartIntent) (Authorization, error)
}

type Authorization struct {
	ProjectID, OwnerPersonID string
	PersonInvoked            bool
	Policy                   nativecontract.Policy
	Launch                   nativecontract.LaunchConfig
	Capabilities             nativecontract.Capabilities
	RunID                    *string // existing leaf run; always nil for a coordinator
}

// PendingStart is content-free local preflight context. Identifiers grant no
// authority. Observer pins the owner-only local ledger reservation and fresh
// private harness proofs before final server admission, without spawning.
type PendingStart struct {
	OperationID, OwnedSessionID, ProjectID, OwnerPersonID string
	Intent                                                nativecontract.StartIntent
}

type Observer interface {
	Observe(context.Context, nativecontract.DaemonActor, PendingStart) (Observation, error)
}

// Observation is trusted transport output, never decoded from a browser body.
// I1 binds it to paired private proof headers and the local shared ledger.
// Only digests enter harness SQL; homes/cwd/credentials never enter this type.
type Observation struct {
	OperationID, PrincipalID, ComputerID, DaemonID, DaemonGeneration string
	LedgerGeneration, CapabilityDigest, AccountID, WorkspaceID       string
	LedgerReservationID                                              string
	LedgerClear                                                      bool
	SessionRefDigest, WorkerLeaseDigest                              []byte
	ObservedAt, ExpiresAt                                            time.Time
	TerminalViewerURL                                                *string // publishing daemon only; Zellij login stays local
}

type Options struct {
	Authority           Authority
	Observer            Observer
	Now                 func() time.Time
	TerminalViewerHosts []string // nil defaults to 100.64.0.0/10 and *.ts.net
	TerminalViewerPorts []int    // nil defaults to 443 and 8082; omitted URL port means 443
}

type Store struct {
	pool        *pgxpool.Pool
	authority   Authority
	observer    Observer
	now         func() time.Time
	validator   *nativecontract.Validator
	viewerHosts ViewerHosts
}

func New(pool *pgxpool.Pool, opts Options) (*Store, error) {
	if pool == nil || opts.Authority == nil || opts.Observer == nil {
		return nil, fail(503, nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	hosts, err := newViewerHosts(opts.TerminalViewerHosts, opts.TerminalViewerPorts)
	if err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{pool: pool, authority: opts.Authority, observer: opts.Observer, now: opts.Now, validator: v, viewerHosts: hosts}, nil
}

// Failure is safe for the native Error envelope: only the closed C0 reason.
type Failure struct {
	Status int
	Reason nativecontract.Reason
}

func (e *Failure) Error() string                          { return string(e.Reason) }
func (e *Failure) HTTPStatus() int                        { return e.Status }
func fail(status int, reason nativecontract.Reason) error { return &Failure{status, reason} }

type projectKey struct{}

// WithProject binds the transport's validated route project, never a body ID.
func WithProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, projectKey{}, project)
}
func projectFrom(ctx context.Context) string { p, _ := ctx.Value(projectKey{}).(string); return p }

type actorRecord struct {
	Principal     tenant.Principal `json:"principal"`
	KeyID         string           `json:"key_id"`
	AuthKeyID     string           `json:"auth_key_id"`
	ActorOwnerID  string           `json:"actor_owner_id"`
	GrantID       string           `json:"grant_id"`
	GrantRevision int64            `json:"grant_revision"`
}

func (r actorRecord) actor() nativecontract.Actor {
	return nativecontract.Actor{TenantID: r.Principal.TenantID, PrincipalID: r.Principal.ID, OwnerPersonID: r.ActorOwnerID, PrincipalKind: string(r.Principal.Kind), KeyCeiling: r.Principal.Scopes, GrantID: r.GrantID, GrantRevision: r.GrantRevision}
}
func (r actorRecord) principal() tenant.Principal {
	p := r.Principal
	p.KeyID = r.KeyID
	p.AuthKeyID = r.AuthKeyID
	return p
}

func (s *Store) validate(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fail(400, nativecontract.ReasonInvalidInput)
	}
	return s.validator.ValidateJSON(name, b)
}
