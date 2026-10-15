// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func writeReviewRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reviewgate.ErrRunCredential):
		writeError(w, 401, err.Error())
	case errors.Is(err, reviewgate.ErrRunBinding):
		writeError(w, 409, err.Error())
	case errors.Is(err, db.ErrKeyAuthorityChanged):
		writeError(w, 403, "reviewer key authority changed")
	default:
		writeErr(w, err)
	}
}

func (m *Module) handleCreateReviewRun(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	var in reviewgate.RunCreate
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	if !workorders.UUID(in.AuthorSession) || !workorders.UUID(in.ReviewerSession) {
		writeError(w, 400, "author and reviewer session IDs are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out reviewgate.Run
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = reviewgate.CreateRunTx(ctx, tx, p, id, in, time.Now())
		if err != nil {
			return err
		}
		return m.record(ctx, tx, p.ID, &id, "review_run.created", nil, out)
	})
	if err != nil {
		writeReviewRunError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, out)
}

func (m *Module) handleClaimReviewRun(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("runId"), "invalid run id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out struct {
		Credential string    `json:"credential"`
		ExpiresAt  time.Time `json:"expires_at"`
	}
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		var err error
		out.Credential, out.ExpiresAt, err = reviewgate.ClaimRunTx(ctx, tx, p, id, time.Now())
		return err
	})
	if err != nil {
		writeReviewRunError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (m *Module) handleSubmitReviewRun(w http.ResponseWriter, r *http.Request) {
	// Ordinary coordinator/worker credentials can never substitute for this
	// capability, nor be mixed with it to choose an actor.
	if _, ok := tenant.PrincipalFrom(r.Context()); ok || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		writeError(w, 403, "only the run credential may submit a verdict")
		return
	}
	id, ok := pathUUID(w, r.PathValue("runId"), "invalid run id")
	if !ok {
		return
	}
	token := r.Header.Get(reviewgate.CredentialHeader)
	tenantID := reviewgate.CredentialTenant(token)
	if tenantID == "" {
		writeReviewRunError(w, reviewgate.ErrRunCredential)
		return
	}
	var in reviewgate.RunSubmit
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	if !in.Valid() || !workorders.UUID(in.Binding.Ticket) {
		writeError(w, 400, "invalid bound verdict")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var p tenant.Principal
	err := db.InTenantReadSnapshot(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		p, err = reviewgate.CredentialPrincipalTx(ctx, tx, id, token, time.Now())
		return err
	})
	if err != nil {
		writeReviewRunError(w, err)
		return
	}
	ctx = tenant.WithPrincipal(ctx, p)
	var out reviewgate.Run
	err = m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = reviewgate.SubmitRunTx(ctx, tx, p, id, token, in, time.Now())
		if err != nil {
			return err
		}
		kind := "coordinator_review.recorded"
		check := "review_ok_cross_family"
		if out.Kind == "visual" {
			kind = "entry_check.evidence"
			check = "visual_ok"
		}
		return m.record(ctx, tx, p.ID, &out.Ticket, kind, nil, map[string]any{"run_id": out.ID, "check_id": check, "ok": out.Verdict == "ok", "head_sha": out.Head, "base_sha": out.Base, "scope": out.Scope, "repository": out.Repository, "pull_request": out.PR, "target_branch": out.Target, "author_family": out.AuthorFamily, "reviewer_family": out.ReviewerFamily, "harness": out.Harness, "model": out.Model, "reasoning_effort": out.Effort, "visual_impact": out.VisualImpact, "reason": out.Reason})
	})
	if err != nil {
		writeReviewRunError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (m *Module) handleReviewEvidenceProvenance(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out reviewgate.EvidenceProvenance
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		n, err := workorders.EntryTicketTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "nodes.read", entryScope(n.ProjectID)); err != nil {
			return err
		}
		out, err = reviewgate.EvidenceProvenanceTx(ctx, tx, id)
		return err
	})
	if err != nil {
		writeReviewRunError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
