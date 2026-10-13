// SPDX-License-Identifier: AGPL-3.0-only

package interaction

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeinteraction"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type lease struct {
	view                nativecontract.ControllerView
	viewer, boot        string
	binding, capability []byte
}

func (s *Store) controller(ctx context.Context, tx pgx.Tx, l *locked) (*lease, error) {
	var c lease
	err := tx.QueryRow(ctx, `SELECT actor_person_id::text,client_id::text,epoch::text,viewer_id::text,boot_id::text,binding_digest,capability_digest,expires_at
 FROM native_session_controllers WHERE owned_session_id=$1`, l.view.OwnedSessionID).Scan(&c.view.ActorID, &c.view.ClientID, &c.view.Epoch, &c.viewer, &c.boot, &c.binding, &c.capability, &c.view.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.boot != s.bootID || !sameDigest(c.binding, digest(l.view.CurrentBinding)) || !l.now.Before(c.view.ExpiresAt) {
		return nil, nil
	}
	c.view.ControllerID = c.viewer
	return &c, nil
}

func (s *Store) Attach(ctx context.Context, a nativecontract.Actor, in nativecontract.AttachIntent) (nativecontract.AttachResult, error) {
	var out nativecontract.AttachResult
	if err := nativeinteraction.Validate(s.validator, "NativeAttachIntent", in); err != nil {
		return out, err
	}
	err := s.transaction(ctx, a, in.ExpectedBinding, "attach:"+in.View, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked) error {
		if err := human(p); err != nil {
			return err
		}
		if in.View != "structured" {
			if err := require(ctx, tx, p, in.ExpectedBinding, "harness.watch"); err != nil {
				return err
			}
			if in.ConsentID == nil {
				return fault(403, nativecontract.ReasonGrantMissing)
			}
			// The mandatory authority adapter verifies this exact consent ID too.
			if err := s.authority.AuthorizeTx(ctx, tx, p, in.ExpectedBinding, "consent:"+*in.ConsentID+":"+in.View); err != nil {
				return err
			}
		}
		if in.Mode == "control" {
			if err := require(ctx, tx, p, in.ExpectedBinding, "harness.control"); err != nil {
				return err
			}
		}
		if err := s.refresh(ctx, tx, l); err != nil {
			return err
		}
		c, err := s.controller(ctx, tx, l)
		if err != nil {
			return err
		}
		l.view.Controller = nil
		if c != nil {
			l.view.Controller = &c.view
		}
		var viewer, oldMode, oldBoot string
		var oldDigest []byte
		var expires time.Time
		var detached *time.Time
		err = tx.QueryRow(ctx, `SELECT id::text,request_digest,mode,boot_id::text,expires_at,detached_at FROM native_session_viewers WHERE request_id=$1`, in.RequestID).Scan(&viewer, &oldDigest, &oldMode, &oldBoot, &expires, &detached)
		if err == nil {
			if !sameDigest(oldDigest, digest(struct {
				Actor  string
				Intent nativecontract.AttachIntent
			}{p.ID, in})) {
				return fault(409, nativecontract.ReasonIdentityMismatch)
			}
			if oldBoot != s.bootID || detached != nil || !l.now.Before(expires) {
				return fault(410, nativecontract.ReasonControllerExpired)
			}
			if oldMode == "control" && (c == nil || c.viewer != viewer) {
				return fault(410, nativecontract.ReasonControllerExpired)
			}
			out = s.attached(l, viewer, oldMode, expires, c)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// An explicit control request cannot displace even the same person's
		// other tab. Reconnect never grants or renews someone else's epoch.
		if in.Mode == "control" && c != nil {
			return fault(409, nativecontract.ReasonControllerConflict)
		}
		var sessionCount, tenantCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE owned_session_id=$1),count(*) FROM native_session_viewers WHERE boot_id=$2 AND expires_at>$3 AND detached_at IS NULL`, in.ExpectedBinding.OwnedSessionID, s.bootID, l.now).Scan(&sessionCount, &tenantCount); err != nil {
			return err
		}
		if sessionCount >= 8 || tenantCount >= 128 {
			return fault(429, nativecontract.ReasonBufferFull)
		}
		viewer, err = uuid()
		if err != nil {
			return err
		}
		expires = minTime(l.now.Add(LeaseTTL), l.policy.ExpiresAt)
		var epoch *string
		if in.Mode == "control" {
			id, err := uuid()
			if err != nil {
				return err
			}
			epoch = &id
			c = &lease{view: nativecontract.ControllerView{ControllerID: viewer, ActorID: p.ID, ClientID: in.ClientID, Epoch: id, ExpiresAt: expires}, viewer: viewer, boot: s.bootID, binding: digest(in.ExpectedBinding), capability: tokenDigest(s.token(p.ID, viewer, id))}
			_, err = tx.Exec(ctx, `INSERT INTO native_session_controllers(tenant_id,owned_session_id,actor_person_id,client_id,viewer_id,epoch,boot_id,binding_digest,capability_digest,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,owned_session_id) DO UPDATE SET
 actor_person_id=excluded.actor_person_id,client_id=excluded.client_id,viewer_id=excluded.viewer_id,epoch=excluded.epoch,boot_id=excluded.boot_id,binding_digest=excluded.binding_digest,capability_digest=excluded.capability_digest,expires_at=excluded.expires_at`, p.TenantID, in.ExpectedBinding.OwnedSessionID, p.ID, in.ClientID, viewer, id, s.bootID, c.binding, c.capability, expires)
			if err != nil {
				return err
			}
			l.view.Controller = &c.view
		}
		_, err = tx.Exec(ctx, `INSERT INTO native_session_viewers(tenant_id,id,owned_session_id,actor_person_id,client_id,request_id,request_digest,binding_digest,boot_id,mode,view_kind,consent_id,controller_epoch,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, p.TenantID, viewer, in.ExpectedBinding.OwnedSessionID, p.ID, in.ClientID, in.RequestID, digest(struct {
			Actor  string
			Intent nativecontract.AttachIntent
		}{p.ID, in}), digest(in.ExpectedBinding), s.bootID, in.Mode, in.View, in.ConsentID, epoch, expires)
		if err != nil {
			return err
		}
		if in.Mode == "control" {
			if err = s.saveView(ctx, tx, l); err != nil {
				return err
			}
		}
		out = s.attached(l, viewer, in.Mode, expires, c)
		if in.Mode == "control" {
			return s.event(ctx, tx, p, l, in.RequestID, "native_session.controller_changed", "held")
		}
		return nil
	})
	if err != nil {
		return nativecontract.AttachResult{}, err
	}
	return out, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (s *Store) attached(l *locked, viewer, mode string, expires time.Time, c *lease) nativecontract.AttachResult {
	var token *string
	if mode == "control" && c != nil && c.viewer == viewer {
		value := s.token(c.view.ActorID, viewer, c.view.Epoch)
		token = &value
	}
	return nativecontract.AttachResult{ViewerID: viewer, Binding: *l.view.CurrentBinding, Mode: mode, ExpiresAt: expires, Controller: l.view.Controller, ControlCapability: token,
		StreamPath: "/api/projects/" + l.view.CurrentBinding.ProjectID + "/native-sessions/" + l.view.OwnedSessionID + "/stream", Snapshot: l.view, ExpiresInMS: max(0, expires.Sub(l.now).Milliseconds())}
}

type viewerRecord struct {
	actor, client, mode, view, boot string
	consent, epoch                  *string
	binding                         []byte
	expires                         time.Time
	detached                        *time.Time
}

func (s *Store) viewer(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked, id string) (viewerRecord, error) {
	var v viewerRecord
	err := tx.QueryRow(ctx, `SELECT actor_person_id::text,client_id::text,mode,view_kind,boot_id::text,consent_id::text,controller_epoch::text,binding_digest,expires_at,detached_at
 FROM native_session_viewers WHERE id=$1 AND owned_session_id=$2 FOR NO KEY UPDATE`, id, l.view.OwnedSessionID).Scan(&v.actor, &v.client, &v.mode, &v.view, &v.boot, &v.consent, &v.epoch, &v.binding, &v.expires, &v.detached)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, fault(410, nativecontract.ReasonControllerExpired)
	}
	if err != nil {
		return v, err
	}
	if v.actor != p.ID {
		return v, fault(403, nativecontract.ReasonWrongPrincipal)
	}
	if v.boot != s.bootID || !sameDigest(v.binding, digest(l.view.CurrentBinding)) || v.detached != nil || !l.now.Before(v.expires) {
		return v, fault(410, nativecontract.ReasonControllerExpired)
	}
	return v, nil
}

// Renew's capability is supplied by the authenticated transport header, never
// a URL. Read-only renewal cannot acquire or silently replace a controller.
func (s *Store) Renew(ctx context.Context, a nativecontract.Actor, in nativecontract.AttachRenew, capability string) (nativecontract.AttachResult, error) {
	var out nativecontract.AttachResult
	if len(capability) > 4096 {
		return out, fault(413, nativecontract.ReasonBufferFull)
	}
	if err := nativeinteraction.Validate(s.validator, "NativeAttachRenew", in); err != nil {
		return out, err
	}
	err := s.transaction(ctx, a, in.ExpectedBinding, "renew", func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked) error {
		if err := human(p); err != nil {
			return err
		}
		v, err := s.viewer(ctx, tx, p, l, in.ViewerID)
		if err != nil {
			return err
		}
		if v.view != "structured" {
			if err = require(ctx, tx, p, in.ExpectedBinding, "harness.watch"); err != nil {
				return err
			}
			if v.consent == nil {
				return fault(403, nativecontract.ReasonGrantMissing)
			}
			if err = s.authority.AuthorizeTx(ctx, tx, p, in.ExpectedBinding, "consent:"+*v.consent+":"+v.view); err != nil {
				return err
			}
		}
		if err = s.refresh(ctx, tx, l); err != nil {
			return err
		}
		if !l.now.Before(v.expires) {
			return fault(410, nativecontract.ReasonControllerExpired)
		}
		c, err := s.controller(ctx, tx, l)
		if err != nil {
			return err
		}
		l.view.Controller = nil
		if c != nil {
			l.view.Controller = &c.view
		}
		if v.mode == "control" {
			if err = require(ctx, tx, p, in.ExpectedBinding, "harness.control"); err != nil {
				return err
			}
			if c == nil || c.viewer != in.ViewerID || in.ControllerEpoch == nil || *in.ControllerEpoch != c.view.Epoch || v.epoch == nil || *v.epoch != c.view.Epoch {
				return fault(410, nativecontract.ReasonControllerExpired)
			}
			if subtle.ConstantTimeCompare(c.capability, tokenDigest(capability)) != 1 {
				return fault(403, nativecontract.ReasonWrongPrincipal)
			}
		} else if in.ControllerEpoch != nil || capability != "" {
			return fault(403, nativecontract.ReasonControllerConflict)
		}
		expires := minTime(l.now.Add(LeaseTTL), l.policy.ExpiresAt)
		if _, err = tx.Exec(ctx, `UPDATE native_session_viewers SET expires_at=$2 WHERE id=$1`, in.ViewerID, expires); err != nil {
			return err
		}
		if v.mode == "control" {
			c.view.ExpiresAt = expires
			if _, err = tx.Exec(ctx, `UPDATE native_session_controllers SET expires_at=$2 WHERE owned_session_id=$1`, l.view.OwnedSessionID, expires); err != nil {
				return err
			}
			if err = s.saveView(ctx, tx, l); err != nil {
				return err
			}
		}
		out = s.attached(l, in.ViewerID, v.mode, expires, c)
		if v.mode == "control" {
			return s.event(ctx, tx, p, l, in.RequestID, "native_session.controller_changed", "renewed")
		}
		return nil
	})
	if err != nil {
		return nativecontract.AttachResult{}, err
	}
	return out, nil
}

// CheckControl is called again during a command's final claim, under that
// transaction's existing tenant/tree/binding fence. It grants no native I/O.
func (s *Store) CheckControlTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b nativecontract.Binding, epoch, viewer, capability string) error {
	if len(capability) > 4096 {
		return fault(413, nativecontract.ReasonBufferFull)
	}
	if err := human(p); err != nil {
		return err
	}
	if err := require(ctx, tx, p, b, "harness.control"); err != nil {
		return err
	}
	var now time.Time
	var err error
	if s.clock == nil {
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	} else {
		now, err = s.clock(ctx, tx)
	}
	if err != nil {
		return err
	}
	l := &locked{view: nativecontract.SessionView{OwnedSessionID: b.OwnedSessionID, CurrentBinding: &b}, now: now}
	c, err := s.controller(ctx, tx, l)
	if err != nil {
		return err
	}
	if c == nil || c.view.Epoch != epoch || c.viewer != viewer {
		return fault(410, nativecontract.ReasonControllerExpired)
	}
	if c.view.ActorID != p.ID || subtle.ConstantTimeCompare(c.capability, tokenDigest(capability)) != 1 {
		return fault(403, nativecontract.ReasonWrongPrincipal)
	}
	return nil
}

func (s *Store) Detach(ctx context.Context, a nativecontract.Actor, in nativecontract.Detach) error {
	if err := nativeinteraction.Validate(s.validator, "NativeDetach", in); err != nil {
		return err
	}
	return s.transaction(ctx, a, in.ExpectedBinding, "detach", func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked) error {
		v, err := s.viewer(ctx, tx, p, l, in.ViewerID)
		if err != nil {
			return err
		}
		c, err := s.controller(ctx, tx, l)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE native_session_viewers SET detached_at=$2 WHERE id=$1`, in.ViewerID, l.now); err != nil {
			return err
		}
		if v.mode == "control" && c != nil && c.viewer == in.ViewerID {
			return s.release(ctx, tx, p, l, in.RequestID)
		}
		return nil // no stop/resume/process/occupancy mutation
	})
}

func (s *Store) EndControl(ctx context.Context, a nativecontract.Actor, in nativecontract.EndControl) error {
	if len(in.Reason) > 512 {
		return fault(413, nativecontract.ReasonBufferFull)
	}
	if err := nativeinteraction.Validate(s.validator, "NativeEndControl", in); err != nil {
		return err
	}
	return s.transaction(ctx, a, in.ExpectedBinding, "control_end", func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked) error {
		if err := human(p); err != nil {
			return err
		}
		if err := require(ctx, tx, p, in.ExpectedBinding, "harness.control"); err != nil {
			return err
		}
		owner, err := modelprefs.CanonicalPerson(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if owner == nil || *owner != l.view.OwnerPersonID {
			if err = authz.RequireProjectAdminTx(ctx, tx, p, in.ExpectedBinding.ProjectID); err != nil {
				return fault(403, nativecontract.ReasonPermissionRevoked)
			}
		}
		if l.view.Revision != in.ExpectedRevision {
			return fault(409, nativecontract.ReasonStaleRevision)
		}
		c, err := s.controller(ctx, tx, l)
		if err != nil {
			return err
		}
		if c == nil || c.view.Epoch != in.ControllerEpoch {
			return fault(410, nativecontract.ReasonControllerExpired)
		}
		return s.release(ctx, tx, p, l, in.RequestID)
	})
}

func (s *Store) release(ctx context.Context, tx pgx.Tx, p tenant.Principal, l *locked, request string) error {
	if _, err := tx.Exec(ctx, `UPDATE native_session_controllers SET expires_at=$2,capability_digest=NULL WHERE owned_session_id=$1`, l.view.OwnedSessionID, l.now); err != nil {
		return err
	}
	l.view.Controller = nil
	if err := s.saveView(ctx, tx, l); err != nil {
		return err
	}
	return s.event(ctx, tx, p, l, request, "native_session.controller_changed", "released")
}
