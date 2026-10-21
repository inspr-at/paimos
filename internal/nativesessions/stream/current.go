// SPDX-License-Identifier: AGPL-3.0-only

package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CurrentSessions rechecks the persisted original actor and immutable consent
// pins in the caller's transaction. Production supplies consent.Authority.
type CurrentSessions interface {
	SessionTx(context.Context, pgx.Tx, nativecontract.Binding) (consent.Session, error)
}

// ExportConsent checks the existing, explicit owner watch decision for this
// exact export. Native control consent is never an implementation of this port.
type ExportConsent interface {
	RequireViewTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Binding, string, string) error
}

type rememberedViewer struct {
	actor, view string
	binding     nativecontract.Binding
	deadline    time.Time
}

// CurrentReadAuthority binds SQL viewers to this process's successful attaches.
// Neither a public viewer UUID nor a viewer left by a previous server boot is
// authority. Elapsed lease budgets also prevent a backwards wall clock from
// extending an existing stream.
type CurrentReadAuthority struct {
	pool    *pgxpool.Pool
	current CurrentSessions
	watch   ExportConsent
	digest  string
	now     func() time.Time
	mu      sync.Mutex
	viewers map[string]rememberedViewer
}

func NewCurrentReadAuthority(pool *pgxpool.Pool, current CurrentSessions, digest string, watch ExportConsent) (*CurrentReadAuthority, error) {
	if pool == nil || current == nil || len(digest) != 64 {
		return nil, nativestream.Refuse(nativecontract.ReasonQualificationMissing)
	}
	return &CurrentReadAuthority{pool: pool, current: current, watch: watch, digest: digest, now: time.Now, viewers: map[string]rememberedViewer{}}, nil
}

// Remember is called only after the interaction store commits an authenticated
// attach/renew. The server response supplies the bounded remaining lease budget.
func (a *CurrentReadAuthority) Remember(actor nativecontract.Actor, view string, result nativecontract.AttachResult) error {
	if !workorders.UUID(result.ViewerID) || result.Binding.TenantID != actor.TenantID || result.ExpiresInMS <= 0 || result.ExpiresInMS > 30000 {
		return nativestream.Refuse(nativecontract.ReasonControllerExpired)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for id, v := range a.viewers {
		if !now.Before(v.deadline) {
			delete(a.viewers, id)
		}
	}
	key := actor.TenantID + ":" + result.ViewerID
	if _, exists := a.viewers[key]; !exists && len(a.viewers) >= nativecontract.ViewersGlobal {
		return nativestream.Refuse(nativecontract.ReasonBufferFull)
	}
	a.viewers[key] = rememberedViewer{actor: actor.PrincipalID, view: view, binding: result.Binding, deadline: now.Add(time.Duration(result.ExpiresInMS) * time.Millisecond)}
	return nil
}

func (a *CurrentReadAuthority) Forget(tenantID, viewerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.viewers, tenantID+":"+viewerID)
}

// VisibilityTx enforces GUI-35 #39 independently of ticket visibility. Taking
// control has its own project-admin rule; it does not grant stream visibility.
func VisibilityTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, project, view string) error {
	if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: project}); err != nil {
		return nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
	}
	if view == "structured" {
		return nil
	}
	if view != "terminal" && view != "expert" {
		return nativestream.Refuse(nativecontract.ReasonInvalidInput)
	}
	if p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" || p.KeyCreatorID != "" {
		return nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
	}
	person, err := modelprefs.CanonicalPerson(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if person != nil && *person == owner {
		return nil
	}
	effective, err := authz.EffectiveTx(ctx, tx, p, project)
	if err != nil {
		return err
	}
	if role := effective.Workspace.Role; role != nil && (role.Key == "owner" || role.Key == "admin") {
		return nil
	}
	return nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
}

// TakeAuthorityTx implements GUI-35 #35. An agent or API key cannot inherit a
// person's right through an owner/account association or an administrator role.
func TakeAuthorityTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner, project string) error {
	if p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" || p.KeyCreatorID != "" {
		return nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.control", authz.Scope{ProjectID: project}); err != nil {
		return nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
	}
	person, err := modelprefs.CanonicalPerson(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if person != nil && *person == owner {
		return nil
	}
	if err := authz.RequireProjectAdminTx(ctx, tx, p, project); err != nil {
		return nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
	}
	return nil
}

// SessionTx verifies the advertised provider protocol after the integrating
// CurrentSessions authority checks consent and the exact enrolled runtime.
func (a *CurrentReadAuthority) SessionTx(ctx context.Context, tx pgx.Tx, b nativecontract.Binding) (consent.Session, error) {
	s, err := a.current.SessionTx(ctx, tx, b)
	if err != nil {
		return s, err
	}
	if s.View.CurrentBinding == nil || !s.View.CurrentBinding.Equal(b) || s.View.Capabilities.QualificationDigest != a.digest {
		return s, nativestream.Refuse(nativecontract.ReasonQualificationMissing)
	}
	return s, nil
}

func (a *CurrentReadAuthority) AuthenticateViewer(r *http.Request) (nativecontract.Binding, Viewer, error) {
	var empty nativecontract.Binding
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.TenantID == "" || !workorders.UUID(p.ID) || p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" || p.KeyCreatorID != "" {
		return empty, Viewer{}, nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
	}
	ids := r.Header.Values(nativecontract.ViewerHeader)
	if len(ids) != 1 || !workorders.UUID(ids[0]) || len(r.URL.RawQuery) > 2048 {
		return empty, Viewer{}, nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	query := r.URL.Query()
	view := query.Get("view")
	if view == "" {
		view = "structured"
	}
	generation, err := strconv.ParseInt(query.Get("generation"), 10, 64)
	if err != nil || generation < 1 {
		return empty, Viewer{}, nativestream.Refuse(nativecontract.ReasonWrongGeneration)
	}
	a.mu.Lock()
	lease, exists := a.viewers[p.TenantID+":"+ids[0]]
	a.mu.Unlock()
	if !exists || !a.now().Before(lease.deadline) || lease.actor != p.ID || lease.view != view {
		return empty, Viewer{}, nativestream.Refuse(nativecontract.ReasonControllerExpired)
	}
	b := lease.binding
	if r.PathValue("projectId") != b.ProjectID || r.PathValue("ownedSessionId") != b.OwnedSessionID {
		return empty, Viewer{}, nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
	}
	fence := func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
		if !a.now().Before(lease.deadline) {
			return nativestream.Refuse(nativecontract.ReasonControllerExpired)
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return db.InTenant(ctx, a.pool, p.TenantID, func(tx pgx.Tx) error {
			if err := agentpairing.Lock(ctx, tx); err != nil {
				return err
			}
			s, err := a.SessionTx(ctx, tx, b)
			if err != nil {
				return err
			}
			if err = VisibilityTx(ctx, tx, p, s.OwnerPersonID, b.ProjectID, view); err != nil {
				return err
			}
			var actor, selected string
			var bindingDigest []byte
			var active bool
			var consentID *string
			err = tx.QueryRow(ctx, `SELECT actor_person_id::text,view_kind,binding_digest,consent_id::text,detached_at IS NULL AND expires_at>clock_timestamp()
 FROM native_session_viewers WHERE tenant_id=$1 AND id=$2 AND owned_session_id=$3`, p.TenantID, ids[0], b.OwnedSessionID).Scan(&actor, &selected, &bindingDigest, &consentID, &active)
			if errors.Is(err, pgx.ErrNoRows) {
				return nativestream.Refuse(nativecontract.ReasonControllerExpired)
			}
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(b)
			digest := sha256.Sum256(raw)
			if !active || actor != p.ID || selected != view || !bytes.Equal(bindingDigest, digest[:]) {
				return nativestream.Refuse(nativecontract.ReasonControllerExpired)
			}
			if view != "structured" {
				// Native control consent never substitutes for a separately qualified
				// watch export. That capability remains closed in the current pilot.
				if consentID == nil || a.watch == nil {
					return nativestream.Refuse(nativecontract.ReasonGrantMissing)
				}
				if err = a.watch.RequireViewTx(ctx, tx, p, b, *consentID, view); err != nil {
					return err
				}
				if err = authz.RequireTx(ctx, tx, p, "harness.watch", authz.Scope{ProjectID: b.ProjectID}); err != nil {
					return nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
				}
			}
			if s.View.Generation != generation || !a.now().Before(lease.deadline) {
				return nativestream.Refuse(nativecontract.ReasonWrongGeneration)
			}
			return deliver(nativecontract.ValidationContext{Binding: &b, OwnedSessionID: b.OwnedSessionID, PrincipalKind: "person", Generation: generation, View: view, Mode: s.View.Mode, State: s.View.State, Capabilities: &s.View.Capabilities, Web: true})
		})
	}
	if err = fence(r.Context(), func(nativecontract.ValidationContext) error { return nil }); err != nil {
		return empty, Viewer{}, err
	}
	return b, Viewer{Query: nativecontract.StreamQuery{View: view, Generation: generation}, Fence: fence}, nil
}

var _ ReadAuthority = (*CurrentReadAuthority)(nil)
