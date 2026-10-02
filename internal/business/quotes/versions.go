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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotedocument"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

type lineWrite struct {
	Description    string      `json:"description"`
	CostUnitNodeID string      `json:"cost_unit_node_id"`
	Unit           string      `json:"unit"`
	Quantity       decimal     `json:"quantity"`
	TaxRate        json.Number `json:"tax_rate"`
}
type versionWrite struct {
	ExpectedRevision       int64       `json:"expected_revision"`
	RecipientContactNodeID string      `json:"recipient_contact_node_id"`
	Currency               string      `json:"currency"`
	Title                  string      `json:"title"`
	TermsMarkdown          string      `json:"terms_markdown"`
	Lines                  []lineWrite `json:"lines"`
}
type line struct {
	Description    string      `json:"description"`
	CostUnitNodeID string      `json:"cost_unit_node_id"`
	Unit           string      `json:"unit"`
	Quantity       decimal     `json:"quantity"`
	TaxRate        json.Number `json:"tax_rate"`
	Position       int         `json:"position"`
	RateAmount     decimal     `json:"rate_amount"`
	NetAmount      decimal     `json:"net_amount"`
}
type version struct {
	QuoteNodeID            string          `json:"quote_node_id"`
	Version                int             `json:"version"`
	RecipientContactNodeID string          `json:"recipient_contact_node_id"`
	Currency               string          `json:"currency"`
	Title                  string          `json:"title"`
	TermsMarkdown          string          `json:"terms_markdown"`
	Lines                  []line          `json:"lines"`
	Subtotal               decimal         `json:"subtotal"`
	TaxTotal               decimal         `json:"tax_total"`
	Total                  decimal         `json:"total"`
	ContentSHA256          string          `json:"content_sha256"`
	CreatedByPrincipalID   string          `json:"created_by_principal_id"`
	CreatedAt              time.Time       `json:"created_at"`
	DigestMode             string          `json:"digest_mode"`
	PricingMode            string          `json:"pricing_mode"`
	Document               json.RawMessage `json:"document,omitempty"`
	OfferNo                string          `json:"offer_no,omitempty"`
	ValidityTimeZone       string          `json:"validity_time_zone,omitempty"`
}

func appendEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, kind string, before, after any) error {
	var nodeID *string
	if id != "" {
		nodeID = &id
	}
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: nodeID, Type: kind, Before: before, After: after})
	return err
}
func validateVersion(in versionWrite) error {
	if in.ExpectedRevision < 1 || !uuidRe.MatchString(in.RecipientContactNodeID) || !currencyRe.MatchString(in.Currency) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 512 || len(in.TermsMarkdown) > 100000 || len(in.Lines) == 0 || len(in.Lines) > 100 {
		return bad("invalid quote version")
	}
	for _, l := range in.Lines {
		if strings.TrimSpace(l.Description) == "" || len(l.Description) > 2000 || !uuidRe.MatchString(l.CostUnitNodeID) || (l.Unit != "hour" && l.Unit != "day" && l.Unit != "item") || l.Quantity <= 0 {
			return bad("invalid quote line")
		}
		if _, err := parseDecimal(l.TaxRate.String(), true); err != nil {
			return bad("invalid tax rate")
		}
	}
	return nil
}
func (m *Module) versions(w http.ResponseWriter, r *http.Request) {
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
	out := []version{}
	e = m.tx(r.Context(), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		if _, err := readQuote(r.Context(), tx, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT version FROM quote_versions WHERE quote_node_id=$1::uuid ORDER BY version`, id)
		if err != nil {
			return err
		}
		var nums []int
		for rows.Next() {
			var n int
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			nums = append(nums, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, n := range nums {
			v, err := readVersion(r.Context(), tx, id, n)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	respond(w, 200, out, e)
}
func (m *Module) version(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
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
	var out version
	e = m.tx(portalContext(r), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		q, err := readQuote(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		if err = canReadQuote(r.Context(), tx, p, q, n); err != nil {
			return err
		}
		out, err = readVersion(r.Context(), tx, id, n)
		return err
	})
	respond(w, 200, out, e)
}
func readVersion(ctx context.Context, tx pgx.Tx, id string, n int) (version, error) {
	var v version
	var subtotal, tax, total string
	err := tx.QueryRow(ctx, `SELECT quote_node_id::text,version,coalesce(recipient_contact_node_id::text,''),currency,title,terms_markdown,subtotal::text,tax_total::text,total::text,content_sha256,created_by_principal_id::text,created_at,digest_mode,pricing_mode FROM quote_versions WHERE quote_node_id=$1::uuid AND version=$2`, id, n).Scan(&v.QuoteNodeID, &v.Version, &v.RecipientContactNodeID, &v.Currency, &v.Title, &v.TermsMarkdown, &subtotal, &tax, &total, &v.ContentSHA256, &v.CreatedByPrincipalID, &v.CreatedAt, &v.DigestMode, &v.PricingMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, missing()
	}
	if err != nil {
		return v, err
	}
	for _, pair := range []struct {
		s string
		d *decimal
	}{{subtotal, &v.Subtotal}, {tax, &v.TaxTotal}, {total, &v.Total}} {
		*pair.d, err = parseDecimal(pair.s, false)
		if err != nil {
			return v, err
		}
	}
	v.Lines = []line{}
	if quotedocument.IsDocument(v.DigestMode) {
		err = tx.QueryRow(ctx, `SELECT document,offer_no,validity_time_zone FROM quote_version_snapshots WHERE quote_node_id=$1::uuid AND version=$2`, id, n).Scan(&v.Document, &v.OfferNo, &v.ValidityTimeZone)
		if err != nil {
			return v, err
		}
		return v, nil
	}
	rows, err := tx.Query(ctx, `SELECT position,description,cost_unit_node_id::text,unit,quantity::text,rate_amount::text,net_amount::text,tax_rate::text FROM quote_line_items WHERE quote_node_id=$1::uuid AND version=$2 ORDER BY position`, id, n)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var l line
		var qty, rate, net, tr string
		if err := rows.Scan(&l.Position, &l.Description, &l.CostUnitNodeID, &l.Unit, &qty, &rate, &net, &tr); err != nil {
			return v, err
		}
		l.Quantity, err = parseDecimal(qty, false)
		if err != nil {
			return v, err
		}
		l.RateAmount, err = parseDecimal(rate, false)
		if err != nil {
			return v, err
		}
		l.NetAmount, err = parseDecimal(net, false)
		if err != nil {
			return v, err
		}
		l.TaxRate = number(tr)
		v.Lines = append(v.Lines, l)
	}
	return v, rows.Err()
}
func digestVersion(v version) (string, error) {
	canonical := struct {
		QuoteNodeID string  `json:"quote_node_id"`
		Version     int     `json:"version"`
		Recipient   string  `json:"recipient_contact_node_id"`
		Currency    string  `json:"currency"`
		Title       string  `json:"title"`
		Terms       string  `json:"terms_markdown"`
		Lines       []line  `json:"lines"`
		Subtotal    decimal `json:"subtotal"`
		TaxTotal    decimal `json:"tax_total"`
		Total       decimal `json:"total"`
	}{v.QuoteNodeID, v.Version, v.RecipientContactNodeID, v.Currency, v.Title, v.TermsMarkdown, v.Lines, v.Subtotal, v.TaxTotal, v.Total}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func (m *Module) freeze(w http.ResponseWriter, r *http.Request) {
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
	var in versionWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	if e = validateVersion(in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	var out version
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		q, err := readQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if q.Revision != in.ExpectedRevision {
			return conflict("quote revision is stale")
		}
		// The legacy rate-backed freeze endpoint shares the document edit fence.
		if q.Archived {
			return conflict("quote is archived")
		}
		if q.State == "void" {
			return conflict("quote is void")
		}
		var linked bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM node_relations rel JOIN nodes c ON c.tenant_id=rel.tenant_id AND c.id=rel.source_node_id JOIN node_kinds k ON k.tenant_id=c.tenant_id AND k.id=c.kind_id WHERE rel.type='contact_for' AND rel.source_node_id=$1::uuid AND rel.target_node_id=$2::uuid AND c.deleted_at IS NULL AND k.slug='contact')`, in.RecipientContactNodeID, q.CustomerOrgNodeID).Scan(&linked)
		if err != nil {
			return err
		}
		if !linked {
			return bad("recipient must be a contact for the customer")
		}
		out = version{QuoteNodeID: id, Version: q.CurrentVersion + 1, RecipientContactNodeID: in.RecipientContactNodeID, Currency: in.Currency, Title: strings.TrimSpace(in.Title), TermsMarkdown: in.TermsMarkdown, Lines: []line{}, CreatedByPrincipalID: p.ID}
		var today time.Time
		err = tx.QueryRow(r.Context(), `SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date`).Scan(&today)
		if err != nil {
			return err
		}
		for i, src := range in.Lines {
			var rateText string
			err = tx.QueryRow(r.Context(), `SELECT r.bill_amount::text FROM cost_unit_rates r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.cost_unit_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.cost_unit_node_id=$1::uuid AND r.unit=$2 AND r.currency=$3 AND r.effective_from<=$4::date AND (r.effective_until IS NULL OR r.effective_until>$4::date) AND n.deleted_at IS NULL AND k.slug='cost_unit' ORDER BY r.effective_from DESC LIMIT 1`, src.CostUnitNodeID, src.Unit, in.Currency, today).Scan(&rateText)
			if errors.Is(err, pgx.ErrNoRows) {
				return bad("no effective cost unit rate")
			}
			if err != nil {
				return err
			}
			rate, err := parseDecimal(rateText, false)
			if err != nil {
				return err
			}
			net, err := multiply(rate, src.Quantity, 10000)
			if err != nil {
				return bad("line amount is too large")
			}
			taxRate, err := parseDecimal(src.TaxRate.String(), true)
			if err != nil {
				return bad("invalid tax rate")
			}
			lineTax, err := multiply(net, taxRate, 100000)
			if err != nil {
				return bad("tax amount is too large")
			}
			out.Subtotal, err = add(out.Subtotal, net)
			if err != nil {
				return bad("subtotal is too large")
			}
			out.TaxTotal, err = add(out.TaxTotal, lineTax)
			if err != nil {
				return bad("tax total is too large")
			}
			out.Lines = append(out.Lines, line{Description: strings.TrimSpace(src.Description), CostUnitNodeID: src.CostUnitNodeID, Unit: src.Unit, Quantity: src.Quantity, TaxRate: number(fmt.Sprintf("%d.%05d", int64(taxRate)/100000, int64(taxRate)%100000)), Position: i, RateAmount: rate, NetAmount: net})
		}
		out.Total, err = add(out.Subtotal, out.TaxTotal)
		if err != nil {
			return bad("total is too large")
		}
		out.ContentSHA256, err = digestVersion(out)
		if err != nil {
			return err
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO quote_versions(tenant_id,quote_node_id,version,recipient_contact_node_id,currency,title,terms_markdown,subtotal,tax_total,total,content_sha256,created_by_principal_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11,$12::uuid) RETURNING created_at`, p.TenantID, id, out.Version, out.RecipientContactNodeID, out.Currency, out.Title, out.TermsMarkdown, out.Subtotal.String(), out.TaxTotal.String(), out.Total.String(), out.ContentSHA256, p.ID).Scan(&out.CreatedAt)
		if err != nil {
			return err
		}
		for _, l := range out.Lines {
			_, err = tx.Exec(r.Context(), `INSERT INTO quote_line_items(tenant_id,quote_node_id,version,position,description,cost_unit_node_id,unit,quantity,rate_amount,net_amount,tax_rate) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7,$8::numeric,$9::numeric,$10::numeric,$11::numeric)`, p.TenantID, id, out.Version, l.Position, l.Description, l.CostUnitNodeID, l.Unit, l.Quantity.String(), l.RateAmount.String(), l.NetAmount.String(), l.TaxRate.String())
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(r.Context(), `UPDATE business_quotes SET current_version=$2,state='draft',revision=revision+1 WHERE quote_node_id=$1::uuid`, id, out.Version)
		if err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, id, "quote.version_created", q, struct {
			Quote   quote   `json:"quote"`
			Version version `json:"version"`
		}{quote{QuoteNodeID: id, ProjectNodeID: q.ProjectNodeID, CustomerOrgNodeID: q.CustomerOrgNodeID, CurrentVersion: out.Version, State: "draft", Revision: q.Revision + 1}, out})
	})
	respond(w, 201, out, e)
}
