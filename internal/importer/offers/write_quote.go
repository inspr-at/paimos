// SPDX-License-Identifier: AGPL-3.0-only
package offers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotedocument"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func writeOffer(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, orgID string, o Offer, d Document, action string) error {
	if action == "create" {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_quotes WHERE tenant_id=$1::uuid AND offer_no=$2)`, p.TenantID, o.OfferNo).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("offer number %s already belongs to another quote", o.OfferNo)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO business_quotes(tenant_id,quote_node_id,customer_org_node_id,offer_no,project_ref) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5)`, p.TenantID, id, orgID, o.OfferNo, d.ProjectRef); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'customer_of') ON CONFLICT DO NOTHING`, p.TenantID, orgID, id); err != nil {
			return err
		}
	} else {
		var currentOrg, number string
		if err := tx.QueryRow(ctx, `SELECT customer_org_node_id::text,offer_no FROM business_quotes WHERE quote_node_id=$1::uuid FOR UPDATE`, id).Scan(&currentOrg, &number); err != nil {
			return err
		}
		if currentOrg != orgID {
			return errors.New("imported offer customer changed")
		}
		if number != o.OfferNo {
			return errors.New("imported offer number changed")
		}
		if _, err := tx.Exec(ctx, `UPDATE business_quotes SET project_ref=$1,revision=revision+1 WHERE quote_node_id=$2::uuid`, d.ProjectRef, id); err != nil {
			return err
		}
	}
	match := offerNumber.FindStringSubmatch(o.OfferNo)
	n, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil || n < 1 {
		return errors.New("invalid offer sequence")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_number_sequences(tenant_id,kind,period,value) VALUES($1::uuid,'offer',$2,$3) ON CONFLICT (tenant_id,kind,period) DO UPDATE SET value=greatest(quote_number_sequences.value,EXCLUDED.value)`, p.TenantID, match[1], n); err != nil {
		return err
	}
	// Native draft edits are guarded. A newer classic revision branches from an
	// issued snapshot; prior versions and their issue evidence remain immutable.
	if action == "update" {
		var activeLinks bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quote_public_links WHERE quote_node_id=$1::uuid AND revoked_at IS NULL)`, id).Scan(&activeLinks); err != nil {
			return err
		}
		if activeLinks {
			return errors.New("cannot update imported quote with an active Aeon public link")
		}
		if _, err = tx.Exec(ctx, `UPDATE business_quotes SET state='draft' WHERE quote_node_id=$1::uuid`, id); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_drafts(tenant_id,quote_node_id,document,schema_version,minimum_writer_version,updated_by_principal_id) VALUES($1::uuid,$2::uuid,$3::jsonb,1,$5,$4::uuid) ON CONFLICT (tenant_id,quote_node_id) DO UPDATE SET document=EXCLUDED.document,minimum_writer_version=EXCLUDED.minimum_writer_version,draft_revision=quote_drafts.draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=EXCLUDED.updated_by_principal_id`, p.TenantID, id, string(raw), p.ID, d.MinimumWriterVersion); err != nil {
		return err
	}
	if o.Status == "draft" {
		if _, err = tx.Exec(ctx, `UPDATE business_quotes SET state='draft' WHERE quote_node_id=$1::uuid`, id); err != nil {
			return err
		}
		return event(ctx, tx, p, id, "quote.draft_imported", nil, map[string]any{"offer_no": o.OfferNo, "source_revision": o.Revision})
	}
	var priorVersion int
	if err = tx.QueryRow(ctx, `SELECT current_version FROM business_quotes WHERE quote_node_id=$1::uuid FOR UPDATE`, id).Scan(&priorVersion); err != nil {
		return err
	}
	version := priorVersion + 1
	var recipient struct {
		CustomerNo    string `json:"customer_no"`
		ContactNodeID string `json:"contact_node_id"`
	}
	if err = json.Unmarshal(d.Recipient, &recipient); err != nil {
		return err
	}
	var contact any
	if recipient.ContactNodeID != "" {
		contact = recipient.ContactNodeID
	}
	// Classic offer days and expiration were interpreted in Vienna regardless
	// of the target tenant's current numbering preference.
	zone := "Europe/Vienna"
	digest, err := quotedocument.Digest(quotedocument.DigestMode, id, version, o.OfferNo, d)
	if err != nil {
		return err
	}
	amount := fmt.Sprintf("%d.%02d", d.NetTotalCents/100, d.NetTotalCents%100)
	if _, err = tx.Exec(ctx, `INSERT INTO quote_versions(tenant_id,quote_node_id,version,recipient_contact_node_id,currency,title,subtotal,tax_total,total,content_sha256,created_by_principal_id,digest_mode,pricing_mode) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7::numeric,0,$7::numeric,$8,$9::uuid,'document-v2','cent-half-up-v1')`, p.TenantID, id, version, contact, d.Currency, d.Title, amount, digest, p.ID); err != nil {
		return err
	}
	sender, _ := json.Marshal(d.Sender)
	recipientJSON, _ := json.Marshal(d.Recipient)
	legal, _ := json.Marshal(d.Legal)
	layout, _ := json.Marshal(d.Layout)
	if _, err = tx.Exec(ctx, `INSERT INTO quote_version_snapshots(tenant_id,quote_node_id,version,document,sender,recipient,legal,layout,offer_no,customer_no,project_ref,offer_date,valid_until,validity_time_zone,document_schema_version,renderer_version) VALUES($1::uuid,$2::uuid,$3,$4::jsonb,$5::jsonb,$6::jsonb,$7::jsonb,$8::jsonb,$9,$10,$11,$12::date,$13::date,$14,1,'document-v1')`, p.TenantID, id, version, string(raw), string(sender), string(recipientJSON), string(legal), string(layout), o.OfferNo, recipient.CustomerNo, d.ProjectRef, d.OfferDate, d.ValidUntil, zone); err != nil {
		return err
	}
	for i, line := range d.Positions {
		lineAmount := fmt.Sprintf("%d.%02d", line.TotalCents/100, line.TotalCents%100)
		if _, err = tx.Exec(ctx, `INSERT INTO quote_document_lines(tenant_id,quote_node_id,version,position,line_id,pricing_source,short_text,long_text,unit_label,currency,quantity,unit_price_cents,total_cents,net_amount) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid,'manual',$6,$7,$8,$9,$10::numeric,$11,$12,$13::numeric)`, p.TenantID, id, version, i, line.ID, line.ShortText, line.LongText, line.UnitLabel, d.Currency, line.Quantity, line.UnitPriceCents, line.TotalCents, lineAmount); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE business_quotes SET current_version=$1,state='draft',revision=revision+1 WHERE quote_node_id=$2::uuid`, version, id); err != nil {
		return err
	}
	if err = event(ctx, tx, p, id, "quote.version_created", map[string]any{"version": priorVersion}, map[string]any{"version": version, "content_sha256": digest}); err != nil {
		return err
	}
	issuedAt := time.Now().UTC()
	if o.SentAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, o.SentAt)
		if parseErr != nil {
			return fmt.Errorf("invalid sent_at for offer %d", o.ID)
		}
		issuedAt = parsed
	}
	return queueImportEvent(ctx, func() error {
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.issued", Before: map[string]any{"state": "draft"}, After: map[string]any{"version": version, "content_sha256": digest, "source_status": o.Status}, At: &issuedAt})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO quote_issues(tenant_id,quote_node_id,version,issued_by_principal_id,issued_at,event_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6)`, p.TenantID, id, version, p.ID, issuedAt, ev.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE business_quotes SET state='issued',revision=revision+1 WHERE quote_node_id=$1::uuid`, id); err != nil {
			return err
		}
		return nil
	})
}
