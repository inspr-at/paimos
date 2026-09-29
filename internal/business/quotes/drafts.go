// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins/fence"
)

var draftETagRe = regexp.MustCompile(`^"qd-([1-9][0-9]*)"$`)

type draftRow struct {
	Document             json.RawMessage `json:"document"`
	DocumentSHA256       string          `json:"document_sha256"`
	DraftRevision        int64           `json:"draft_revision"`
	QuoteRevision        int64           `json:"quote_revision"`
	SchemaVersion        int             `json:"schema_version"`
	MinimumWriterVersion int             `json:"minimum_writer_version"`
	BaseVersion          int             `json:"base_version"`
	UpdatedAt            time.Time       `json:"updated_at"`
	UpdatedByPrincipalID string          `json:"updated_by_principal_id"`
}

func readDraft(ctx context.Context, tx pgx.Tx, id string, lock bool) (draftRow, error) {
	var d draftRow
	q := `SELECT d.document,d.draft_revision,q.revision,d.schema_version,d.minimum_writer_version,d.base_version,d.updated_at,d.updated_by_principal_id::text FROM quote_drafts d JOIN business_quotes q ON q.tenant_id=d.tenant_id AND q.quote_node_id=d.quote_node_id WHERE d.quote_node_id=$1::uuid AND q.deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE OF d`
	}
	err := tx.QueryRow(ctx, q, id).Scan(&d.Document, &d.DraftRevision, &d.QuoteRevision, &d.SchemaVersion, &d.MinimumWriterVersion, &d.BaseVersion, &d.UpdatedAt, &d.UpdatedByPrincipalID)
	if err == nil {
		sum := sha256.Sum256(d.Document)
		d.DocumentSHA256 = hex.EncodeToString(sum[:])
		// Documents saved before writer 2 may contain marks while their row
		// still advertises writer 1. Enforce the effective floor on reads too.
		if doc, decodeErr := decodeDocument(d.Document); decodeErr == nil {
			d.MinimumWriterVersion = max(d.MinimumWriterVersion, documentMinimumWriterVersion(doc))
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return d, missing()
	}
	return d, err
}
func (m *Module) draftGet(w http.ResponseWriter, r *http.Request) {
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
	var out draftRow
	e = m.tx(r.Context(), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		if _, err := readQuote(r.Context(), tx, id, false); err != nil {
			return err
		}
		var err error
		out, err = readDraft(r.Context(), tx, id, false)
		return err
	})
	if e == nil {
		w.Header().Set("ETag", fmt.Sprintf(`"qd-%d"`, out.DraftRevision))
	}
	respond(w, 200, out, e)
}

type draftWrite struct {
	ClientSessionID string          `json:"client_session_id"`
	MutationID      string          `json:"mutation_id"`
	WriterVersion   int             `json:"writer_version"`
	Document        json.RawMessage `json:"document"`
}

func draftPrecondition(r *http.Request) (int64, error) {
	h := r.Header.Get("If-Match")
	if h == "" {
		return 0, failure{status: 428, message: "If-Match is required"}
	}
	parts := draftETagRe.FindStringSubmatch(h)
	if parts == nil {
		return 0, bad("invalid draft precondition")
	}
	n, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil {
		return 0, bad("invalid draft precondition")
	}
	return n, nil
}
func (m *Module) draftPatch(w http.ResponseWriter, r *http.Request) {
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
	expected, e := draftPrecondition(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	var in draftWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !uuidRe.MatchString(in.ClientSessionID) || !uuidRe.MatchString(in.MutationID) || len(in.Document) == 0 || in.WriterVersion < 1 || in.WriterVersion > 2 {
		respond(w, 0, nil, bad("invalid draft mutation"))
		return
	}
	doc, e := decodeDocument(in.Document)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if e = validateDocument(&doc, false); e != nil {
		respond(w, 0, nil, e)
		return
	}
	payload, e := json.Marshal(in)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	hash := sha256.Sum256(payload)
	inputDigest := hex.EncodeToString(hash[:])
	var out map[string]any
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		q, err := readQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		current, err := readDraft(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		var priorDigest string
		var priorRevision, priorQuoteRevision int64
		err = tx.QueryRow(r.Context(), `SELECT input_sha256,result_revision,result_quote_revision FROM quote_draft_mutations WHERE quote_node_id=$1::uuid AND client_session_id=$2::uuid AND mutation_id=$3::uuid`, id, in.ClientSessionID, in.MutationID).Scan(&priorDigest, &priorRevision, &priorQuoteRevision)
		if err == nil {
			if priorDigest != inputDigest {
				return conflict("mutation id was reused with different content")
			}
			out = map[string]any{"mutation_id": in.MutationID, "acknowledged_revision": priorRevision, "acknowledged_quote_revision": priorQuoteRevision, "current_revision": current.DraftRevision, "current_quote_revision": current.QuoteRevision, "replayed": true}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if q.State != "draft" || q.Archived {
			return conflict("quote is not editable")
		}
		if current.DraftRevision != expected {
			return failure{status: 412, message: "draft revision is stale"}
		}
		// Profile changes have their own server-side selection endpoint. A full
		// document save may neither forge a definition nor silently drop it.
		oldDocument, err := decodeDocument(current.Document)
		if err != nil {
			return err
		}
		// Null and empty lists are the same profile (AEON-274).
		if !sameProfile(oldDocument.Profile, doc.Profile) {
			return conflict("select the quote profile through its endpoint")
		}
		minimumWriter := max(current.MinimumWriterVersion, documentMinimumWriterVersion(doc))
		if in.WriterVersion < minimumWriter {
			return conflict("document requires a newer writer")
		}
		doc.MinimumWriterVersion = minimumWriter
		var customerNo string
		err = tx.QueryRow(r.Context(), `SELECT customer_no FROM crm_customer_numbers WHERE organisation_node_id=$1::uuid`, q.CustomerOrgNodeID).Scan(&customerNo)
		if err != nil {
			return err
		}
		var recipient struct {
			CustomerNo string `json:"customer_no"`
		}
		if json.Unmarshal(doc.Recipient, &recipient) != nil || recipient.CustomerNo != customerNo {
			return bad("customer number is immutable")
		}
		// Validate rate references at save; effective amount is selected again at issue.
		for _, line := range doc.Positions {
			if line.PricingSource == "cost_unit" {
				var ok bool
				err = tx.QueryRow(r.Context(), `SELECT aeon_business_node_kind($1::uuid,$2::uuid,'cost_unit')`, p.TenantID, line.CostUnitNodeID).Scan(&ok)
				if err != nil {
					return err
				}
				if !ok {
					return bad("cost unit is not live")
				}
			}
		}
		raw, err := marshalDraft(doc)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE quote_drafts SET document=$1::jsonb,minimum_writer_version=$2,draft_revision=draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$3::uuid WHERE quote_node_id=$4::uuid`, string(raw), minimumWriter, p.ID, id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE business_quotes SET project_ref=$1,revision=revision+1 WHERE quote_node_id=$2::uuid`, doc.ProjectRef, id)
		if err != nil {
			return err
		}
		if doc.Title != "" {
			_, err = tx.Exec(r.Context(), `UPDATE nodes SET title=$1,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$2::uuid AND title IS DISTINCT FROM $1`, doc.Title, id)
			if err != nil {
				return err
			}
		}
		ev, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &id, Type: "quote.draft_updated", Before: map[string]any{"draft_revision": current.DraftRevision, "quote_revision": q.Revision}, After: map[string]any{"draft_revision": current.DraftRevision + 1, "quote_revision": q.Revision + 1, "client_session_id": in.ClientSessionID, "mutation_id": in.MutationID, "schema_version": doc.SchemaVersion}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO quote_draft_mutations(tenant_id,quote_node_id,client_session_id,mutation_id,input_sha256,result_revision,result_quote_revision,event_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8)`, p.TenantID, id, in.ClientSessionID, in.MutationID, inputDigest, current.DraftRevision+1, q.Revision+1, ev.ID)
		if err != nil {
			return err
		}
		updated, err := readDraft(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		out = map[string]any{"mutation_id": in.MutationID, "acknowledged_revision": updated.DraftRevision, "acknowledged_quote_revision": updated.QuoteRevision, "current_revision": updated.DraftRevision, "current_quote_revision": updated.QuoteRevision, "document": updated.Document, "document_sha256": updated.DocumentSHA256, "updated_at": updated.UpdatedAt, "updated_by_principal_id": updated.UpdatedByPrincipalID, "replayed": false}
		return nil
	})
	if e == nil {
		w.Header().Set("ETag", fmt.Sprintf(`"qd-%d"`, out["current_revision"]))
	}
	respond(w, 200, out, e)
}
