// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func (m *Module) handleEntryEvidence(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	var in struct {
		Check           string    `json:"check_id"`
		OK              bool      `json:"ok"`
		Revision        time.Time `json:"expected_revision"`
		Head            string    `json:"head_sha"`
		Base            string    `json:"base_sha"`
		AuthorSession   string    `json:"author_session_id"`
		ReviewerSession string    `json:"reviewer_session_id"`
		Attachment      string    `json:"attachment_id"`
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Revision.IsZero() || in.Check != "review_ok_cross_family" && in.Check != "visual_ok" && in.Check != "design_attached" {
		writeError(w, 400, "invalid entry evidence")
		return
	}
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		n, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !n.UpdatedAt.Equal(in.Revision) {
			return &httpError{status: 412, msg: "node has changed"}
		}
		t, err := workqueue.EntryTicketTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Kind != "work" && t.Kind != "ticket" && t.Kind != "task" {
			return badRequest("entry evidence is for work leaves")
		}
		if err = authz.RequireTx(ctx, tx, p, "nodes.write", entryScope(t.ProjectID)); err != nil {
			return err
		}
		if p.Kind == tenant.Person {
			if err = authz.RequireProjectAdminTx(ctx, tx, p, deref(t.ProjectID)); err != nil {
				return err
			}
		} else {
			if in.Check != "review_ok_cross_family" {
				return authz.ErrForbidden
			}
			if err = authz.RequireQueueCoordinatorTx(ctx, tx, p, deref(t.ProjectID)); err != nil {
				return err
			}
		}
		if in.Check == "design_attached" {
			if !workorders.UUID(in.Attachment) {
				return badRequest("attachment_id is required")
			}
			var present bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE id=$1 AND node_id=$2 AND deleted_at IS NULL)`, in.Attachment, id).Scan(&present); err != nil {
				return err
			}
			if !present {
				return badRequest("design attachment is not on this ticket")
			}
		} else {
			if !reviewgate.ValidSHA(in.Head) || !reviewgate.ValidSHA(in.Base) || in.Head == in.Base {
				return badRequest("exact build head and base are required")
			}
			var current bool
			if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT d.head_sha=$2 AND d.observation->>'base_sha'=$3 FROM delivery_items d JOIN delivery_metric_sources s ON s.tenant_id=d.tenant_id AND s.project_id=d.project_id AND s.repository=d.repository WHERE d.ticket_node_id=$1 ORDER BY d.updated_at DESC,d.id DESC LIMIT 1),false)`, id, in.Head, in.Base).Scan(&current); err != nil {
				return err
			}
			if !current {
				return conflict("delivery head or base has changed")
			}
		}
		if in.Check == "review_ok_cross_family" {
			if !workorders.UUID(in.AuthorSession) || !workorders.UUID(in.ReviewerSession) {
				return badRequest("author and reviewer sessions are required")
			}
			v := reviewgate.CoordinatorReview{Head: in.Head, Base: in.Base, AuthorSession: in.AuthorSession, ReviewerSession: in.ReviewerSession, OK: in.OK}
			valid, err := reviewgate.CoordinatorFamiliesTx(ctx, tx, id, deref(t.ProjectID), &v)
			if err != nil {
				return err
			}
			if !valid {
				return conflict("recorded sessions must prove different known model families on this ticket")
			}
			return m.record(ctx, tx, p.ID, &id, "coordinator_review.recorded", nil, v)
		}
		return m.record(ctx, tx, p.ID, &id, "entry_check.evidence", nil, map[string]any{"check_id": in.Check, "ok": in.OK, "head_sha": in.Head, "base_sha": in.Base, "attachment_id": in.Attachment})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}
