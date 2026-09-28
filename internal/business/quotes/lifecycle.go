// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

type finalizeWrite struct {
	ExpectedQuoteRevision  int64  `json:"expected_quote_revision"`
	ExpectedDraftRevision  int64  `json:"expected_draft_revision"`
	ExpectedDocumentSHA256 string `json:"expected_document_sha256"`
}

func documentDigest(id string, version int, offerNo string, doc quoteDocument) (string, error) {
	payload := struct {
		Mode        string        `json:"mode"`
		QuoteNodeID string        `json:"quote_node_id"`
		Version     int           `json:"version"`
		OfferNo     string        `json:"offer_no"`
		Document    quoteDocument `json:"document"`
	}{"document-v1", id, version, offerNo, doc}
	b, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func decimalCents(cents int64) string { return fmt.Sprintf("%d.%02d00", cents/100, cents%100) }
func rateCents(rate decimal, quantity int64) (int64, error) {
	n := new(big.Int).Mul(big.NewInt(int64(rate)), big.NewInt(quantity))
	n.Add(n, big.NewInt(5000))
	n.Div(n, big.NewInt(10000))
	if !n.IsInt64() || n.Int64() > 1_000_000_000_000 {
		return 0, bad("rate total too large")
	}
	return n.Int64(), nil
}
func publicLinkPart(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// createIssuedLink runs only with a persistent key: automatic issuance has no
// response channel for showing a one-time token. Without a key, the admin
// creates the link explicitly after finalization and receives it once.
func createIssuedLink(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, version int, digest string, key []byte) error {
	if key == nil {
		return nil
	}
	token, err := publicLinkPart(32)
	if err != nil {
		return err
	}
	selector, err := publicLinkPart(24)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_public_tenant_selectors(tenant_id,selector) VALUES($1::uuid,$2) ON CONFLICT (tenant_id) DO NOTHING`, p.TenantID, selector); err != nil {
		return err
	}
	linkID, err := newID()
	if err != nil {
		return err
	}
	event, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.public_link_created", After: map[string]any{"link_id": linkID, "version": version, "target_content_sha256": digest}})
	if err != nil {
		return err
	}
	verifier := sha256.Sum256([]byte(token))
	_, err = tx.Exec(ctx, `INSERT INTO quote_public_links(tenant_id,id,quote_node_id,version,token_sha256,target_content_sha256,issued_by_principal_id,issued_event_id,expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7::uuid,$8,$9)`, p.TenantID, linkID, id, version, hex.EncodeToString(verifier[:]), digest, p.ID, event.ID, time.Now().Add(30*24*time.Hour))
	if err != nil {
		return err
	}
	ciphertext, err := linkvault.Encrypt(key, p.TenantID, linkID, token)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO quote_public_link_tokens(tenant_id,link_id,ciphertext) VALUES($1::uuid,$2::uuid,$3)`, p.TenantID, linkID, ciphertext)
	return err
}
func (m *Module) finalize(w http.ResponseWriter, r *http.Request) {
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
	var in finalizeWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	if in.ExpectedQuoteRevision < 1 || in.ExpectedDraftRevision < 1 || !shaRe.MatchString(in.ExpectedDocumentSHA256) {
		respond(w, 0, nil, bad("invalid finalization precondition"))
		return
	}
	var out quote
	e = m.tx(r.Context(), p, fence.PermStepsApply, true, func(tx pgx.Tx) error {
		var err error
		out, _, err = issueQuoteDraft(r.Context(), tx, p, id, m.linkKey, &in)
		return err
	})
	respond(w, 200, out, e)
}

type visibilityWrite struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Archived         bool  `json:"archived"`
}

func (m *Module) visibility(w http.ResponseWriter, r *http.Request) {
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
	var in visibilityWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	var out quote
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		q, err := readQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if q.Revision != in.ExpectedRevision {
			return conflict("quote revision is stale")
		}
		out, err = setQuoteVisibility(r.Context(), tx, p, q, in.Archived)
		return err
	})
	respond(w, 200, out, e)
}
func cloneDocumentIDs(doc *quoteDocument) error {
	for i := range doc.Sections {
		id, e := newID()
		if e != nil {
			return e
		}
		doc.Sections[i].ID = id
		for j := range doc.Sections[i].Nodes {
			id, e := newID()
			if e != nil {
				return e
			}
			doc.Sections[i].Nodes[j].ID = id
		}
	}
	for i := range doc.Positions {
		id, e := newID()
		if e != nil {
			return e
		}
		doc.Positions[i].ID = id
	}
	return nil
}
func (m *Module) duplicate(w http.ResponseWriter, r *http.Request) {
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
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	var out quote
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		source, err := readQuote(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		settings, err := readSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		day, err := quoteDay(settings, time.Now())
		if err != nil {
			return err
		}
		customerNo, err := ensureCustomerNumber(r.Context(), tx, p, source.CustomerOrgNodeID, day)
		if err != nil {
			return err
		}
		source, err = readQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if source.Revision != in.ExpectedRevision {
			return conflict("quote revision is stale")
		}
		var raw []byte
		if source.State != "draft" && source.CurrentVersion > 0 {
			err = tx.QueryRow(r.Context(), `SELECT document FROM quote_version_snapshots WHERE quote_node_id=$1::uuid AND version=$2`, id, source.CurrentVersion).Scan(&raw)
		} else {
			err = tx.QueryRow(r.Context(), `SELECT document FROM quote_drafts WHERE quote_node_id=$1::uuid`, id).Scan(&raw)
		}
		if errors.Is(err, pgx.ErrNoRows) && source.CurrentVersion > 0 {
			err = tx.QueryRow(r.Context(), `SELECT document FROM quote_version_snapshots WHERE quote_node_id=$1::uuid AND version=$2`, id, source.CurrentVersion).Scan(&raw)
		}
		if err != nil {
			return err
		}
		doc, err := decodeDocument(raw)
		if err != nil {
			return err
		}
		if err = cloneDocumentIDs(&doc); err != nil {
			return err
		}
		doc.MinimumWriterVersion = documentMinimumWriterVersion(doc)
		doc.OfferDate = day.Format("2006-01-02")
		doc.ValidUntil = day.AddDate(0, 0, 30).Format("2006-01-02")
		var recipient map[string]any
		if json.Unmarshal(doc.Recipient, &recipient) != nil {
			return bad("invalid recipient")
		}
		recipient["customer_no"] = customerNo
		currentRecipient, err := customerRecipient(r.Context(), tx, source.CustomerOrgNodeID, customerNo)
		if err != nil {
			return err
		}
		for _, field := range []string{"contact", "email", "contact_node_id"} {
			recipient[field] = currentRecipient[field]
		}
		doc.Recipient, err = json.Marshal(recipient)
		if err != nil {
			return err
		}
		offerNo, err := allocateOfferNumber(r.Context(), tx, p, day)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(r.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1::uuid,aeon_next_node_key($1::uuid,k.short_prefix),k.id,$2 FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug='quote' RETURNING id::text`, p.TenantID, doc.Title).Scan(&out.QuoteNodeID); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO business_quotes(tenant_id,quote_node_id,project_node_id,customer_org_node_id,offer_no,project_ref) VALUES($1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4::uuid,$5,$6)`, p.TenantID, out.QuoteNodeID, source.ProjectNodeID, source.CustomerOrgNodeID, offerNo, doc.ProjectRef)
		if err != nil {
			return err
		}
		if err := validateDocument(&doc, false); err != nil {
			return err
		}
		raw, err = json.Marshal(doc)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO quote_drafts(tenant_id,quote_node_id,document,schema_version,minimum_writer_version,updated_by_principal_id) VALUES($1::uuid,$2::uuid,$3::jsonb,1,$4,$5::uuid)`, p.TenantID, out.QuoteNodeID, string(raw), doc.MinimumWriterVersion, p.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'customer_of')`, p.TenantID, source.CustomerOrgNodeID, out.QuoteNodeID)
		if err != nil {
			return err
		}
		out, err = readQuote(r.Context(), tx, out.QuoteNodeID, false)
		if err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, out.QuoteNodeID, "quote.duplicated", nil, map[string]any{"quote": out, "source_quote_node_id": id})
	})
	respond(w, 201, out, e)
}
func classicStatus(ctx context.Context, tx pgx.Tx, q quote) (string, error) {
	switch q.State {
	case "draft":
		return "draft", nil
	case "accepted":
		return "accepted", nil
	case "void":
		return "void", nil
	case "issued":
		var valid, zone string
		err := tx.QueryRow(ctx, `SELECT valid_until::text,validity_time_zone FROM quote_version_snapshots WHERE quote_node_id=$1::uuid AND version=$2`, q.QuoteNodeID, q.CurrentVersion).Scan(&valid, &zone)
		if errors.Is(err, pgx.ErrNoRows) {
			return "sent", nil
		}
		if err != nil {
			return "", err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return "", err
		}
		day := time.Now().In(loc)
		if valid < day.Format("2006-01-02") {
			return "expired", nil
		}
		return "sent", nil
	}
	return strings.ToLower(q.State), nil
}
