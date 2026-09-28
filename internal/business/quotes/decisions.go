// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins/fence"
)

type acceptanceWrite struct {
	ExpectedContentSHA256 string `json:"expected_content_sha256"`
}
type acceptance struct {
	QuoteNodeID            string    `json:"quote_node_id"`
	Version                int       `json:"version"`
	CustomerPrincipalID    string    `json:"customer_principal_id"`
	RecipientContactNodeID string    `json:"recipient_contact_node_id"`
	AcceptedContentSHA256  string    `json:"accepted_content_sha256"`
	AcceptedAt             time.Time `json:"accepted_at"`
	EventID                int64     `json:"event_id"`
}

func (m *Module) issue(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	id, e := pathID(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	n, e := pathVersion(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	var out quote
	e = m.tx(r.Context(), p, fence.PermStepsApply, true, func(tx pgx.Tx) error {
		q, err := readQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if err := m.enabled(r.Context(), tx, p.TenantID, fence.PermStepsApply, true); err != nil {
			return err
		}
		if q.CurrentVersion != n || q.State != "draft" {
			return conflict("only the current draft version can be issued")
		}
		v, err := readVersion(r.Context(), tx, id, n)
		if err != nil {
			return err
		}
		sum, err := digestVersion(v)
		if err != nil {
			return err
		}
		if sum != v.ContentSHA256 {
			return conflict("version digest mismatch")
		}
		var linked bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM node_relations rel JOIN nodes c ON c.tenant_id=rel.tenant_id AND c.id=rel.source_node_id JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id WHERE rel.type='contact_for' AND rel.source_node_id=$1::uuid AND rel.target_node_id=$2::uuid AND c.deleted_at IS NULL AND k.slug='contact')`, v.RecipientContactNodeID, q.CustomerOrgNodeID).Scan(&linked)
		if err != nil {
			return err
		}
		if !linked {
			return conflict("recipient is no longer linked to customer")
		}
		ev, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &id, Type: "quote.issued", Before: q, After: map[string]any{"version": n, "content_sha256": v.ContentSHA256, "recipient_contact_node_id": v.RecipientContactNodeID}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO quote_issues(tenant_id,quote_node_id,version,issued_by_principal_id,event_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, p.TenantID, id, n, p.ID, ev.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE business_quotes SET state='issued',revision=revision+1 WHERE quote_node_id=$1::uuid`, id)
		if err != nil {
			return err
		}
		out, err = readQuote(r.Context(), tx, id, false)
		return err
	})
	respond(w, 200, out, e)
}

func (m *Module) accept(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !person(p) {
		respond(w, 0, nil, denied())
		return
	}
	id, e := pathID(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	n, e := pathVersion(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	var in acceptanceWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !shaRe.MatchString(in.ExpectedContentSHA256) {
		respond(w, 0, nil, bad("invalid content digest"))
		return
	}
	var out acceptance
	e = m.tx(portalContext(r), p, fence.PermStepsApply, true, func(tx pgx.Tx) error {
		if err := m.enabled(r.Context(), tx, p.TenantID, fence.PermStepsApply, true); err != nil {
			return err
		}
		var err error
		out, err = acceptQuoteVersion(r.Context(), tx, p, id, n, in.ExpectedContentSHA256)
		return err
	})
	respond(w, 201, out, e)
}
