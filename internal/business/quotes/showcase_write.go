// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotedocument"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ShowcaseQuote is the quote row the showcase command reports.
type ShowcaseQuote struct {
	ID            string
	OfferNo       string
	Title         string
	State         string
	Version       int
	Revision      int64
	Archived      bool
	CustomerOrgID string
}

// ShowcaseSettings is the tenant numbering and sender the showcase command needs.
type ShowcaseSettings struct {
	Revision         int64
	TimeZone         string
	Currency         string
	Sender           json.RawMessage
	Layout           json.RawMessage
	DefaultProfileID string
}

// ShowcaseMark is a bold or italic span. Offsets are UTF-16 indexes, matching the editor.
type ShowcaseMark struct {
	Start  int  `json:"start"`
	End    int  `json:"end"`
	Bold   bool `json:"bold,omitempty"`
	Italic bool `json:"italic,omitempty"`
}

// ShowcaseText is one paragraph or list item.
type ShowcaseText struct {
	Kind         string         `json:"kind"`
	Text         string         `json:"text"`
	Depth        int            `json:"depth,omitempty"`
	Marker       string         `json:"marker,omitempty"`
	Numbering    string         `json:"numbering,omitempty"`
	ListStart    int            `json:"list_start,omitempty"`
	ListContinue bool           `json:"list_continue,omitempty"`
	SectionBound bool           `json:"section_bound,omitempty"`
	Glyph        string         `json:"glyph,omitempty"`
	MarkerXMM    string         `json:"marker_x_mm,omitempty"`
	MarkerYMM    string         `json:"marker_y_mm,omitempty"`
	TextStartMM  string         `json:"text_start_mm,omitempty"`
	Marks        []ShowcaseMark `json:"marks,omitempty"`
}

// ShowcaseSection is one numbered block of the quote.
type ShowcaseSection struct {
	Heading         string         `json:"heading"`
	NumberingStyle  string         `json:"numbering_style,omitempty"`
	PageBreakBefore *bool          `json:"page_break_before,omitempty"`
	KeepTogether    *bool          `json:"keep_together,omitempty"`
	SpacingBeforeMM string         `json:"spacing_before_mm,omitempty"`
	SpacingAfterMM  string         `json:"spacing_after_mm,omitempty"`
	Nodes           []ShowcaseText `json:"nodes"`
}

// ShowcasePosition is a manually priced row. The document model has no optional or alternative flag.
type ShowcasePosition struct {
	ShortText      string `json:"short_text"`
	LongText       string `json:"long_text"`
	Quantity       string `json:"quantity"`
	UnitLabel      string `json:"unit_label"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

// ShowcaseLegal is the cover, acceptance and commercial text stored on the document.
type ShowcaseLegal struct {
	Intro        string `json:"intro"`
	AcceptText   string `json:"accept_text"`
	VATNote      string `json:"vat_note"`
	DiscountNote string `json:"discount_note"`
	PaymentTerms string `json:"payment_terms"`
}

// ShowcaseDraft is one version of a showcase quote. Dates are filled when the command runs.
type ShowcaseDraft struct {
	Title      string             `json:"title"`
	Subtitle   string             `json:"subtitle"`
	ProjectRef string             `json:"project_ref"`
	Currency   string             `json:"currency"`
	Legal      ShowcaseLegal      `json:"legal"`
	Sections   []ShowcaseSection  `json:"sections"`
	Positions  []ShowcasePosition `json:"positions"`
}

// ShowcaseRecipient replaces the contact on a draft while keeping the customer number.
type ShowcaseRecipient struct {
	Name  string
	Email string
	ID    string
}

var showcaseKeyRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func showcaseQuote(q quote, title string) ShowcaseQuote {
	return ShowcaseQuote{ID: q.QuoteNodeID, OfferNo: q.OfferNo, Title: title, State: q.State, Version: q.CurrentVersion, Revision: q.Revision, Archived: q.Archived, CustomerOrgID: q.CustomerOrgNodeID}
}

func explainDocument(err error) error {
	var f failure
	if !errors.As(err, &f) || len(f.fields) == 0 {
		return err
	}
	keys := make([]string, 0, len(f.fields))
	for key := range f.fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key + ": " + f.fields[key]
	}
	return fmt.Errorf("%s (%s)", f.message, strings.Join(parts, "; "))
}

func nodeTitle(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var title string
	err := tx.QueryRow(ctx, `SELECT title FROM nodes WHERE id=$1::uuid`, id).Scan(&title)
	return title, err
}

// LoadShowcaseSettings reads the tenant quote settings. A revision of zero means they are not configured.
func LoadShowcaseSettings(ctx context.Context, tx pgx.Tx) (ShowcaseSettings, error) {
	s, err := readSettings(ctx, tx)
	if err != nil {
		return ShowcaseSettings{}, err
	}
	return ShowcaseSettings{Revision: s.Revision, TimeZone: s.NumberingTimeZone, Currency: s.DefaultCurrency, Sender: s.Sender, Layout: s.Layout, DefaultProfileID: s.DefaultProfileID}, nil
}

// ShowcaseDay is the numbering calendar day used for offer dates and validity.
func ShowcaseDay(s ShowcaseSettings, now time.Time) (time.Time, error) {
	return quoteDay(quoteSettings{Revision: s.Revision, NumberingTimeZone: s.TimeZone}, now)
}

// FindShowcaseKey returns the live node stored under key, or an empty id when none exists.
func FindShowcaseKey(ctx context.Context, tx pgx.Tx, kind, key string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug=$1 AND n.deleted_at IS NULL AND n.fields->>'showcase_key'=$2 ORDER BY n.id`, kind, key)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("duplicate showcase key %q", key)
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

// ProfileIDByName returns the live profile with this name.
func ProfileIDByName(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM quote_document_profiles WHERE name=$1 AND archived_at IS NULL ORDER BY id`, name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("duplicate quote profile %q", name)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("quote profile %q is not in this tenant", name)
	}
	return ids[0], nil
}

// ShowcaseHasPublicLink reports an unrevoked public link on the quote.
func ShowcaseHasPublicLink(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var linked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quote_public_links WHERE quote_node_id=$1::uuid AND revoked_at IS NULL)`, id).Scan(&linked)
	return linked, err
}

// ReadShowcaseQuote loads a quote for the showcase report.
func ReadShowcaseQuote(ctx context.Context, tx pgx.Tx, id string) (ShowcaseQuote, error) {
	q, err := readQuote(ctx, tx, id, false)
	if err != nil {
		return ShowcaseQuote{}, err
	}
	title, err := nodeTitle(ctx, tx, id)
	if err != nil {
		return ShowcaseQuote{}, err
	}
	return showcaseQuote(q, title), nil
}

func createCustomerQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, title, projectID, orgID, profileID, showcaseKey string) (quote, error) {
	var out quote
	if showcaseKey != "" && !showcaseKeyRe.MatchString(showcaseKey) {
		return out, bad("invalid showcase key")
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes org JOIN node_kinds ok ON ok.tenant_id=org.tenant_id AND ok.id=org.kind_id WHERE org.id=$1::uuid AND org.deleted_at IS NULL AND ok.slug='organisation' AND ($2::text='' OR EXISTS (SELECT 1 FROM node_relations rel JOIN nodes prj ON prj.tenant_id=rel.tenant_id AND prj.id=rel.target_node_id JOIN node_kinds pk ON pk.tenant_id=prj.tenant_id AND pk.id=prj.kind_id WHERE rel.type='customer_of' AND rel.source_node_id=org.id AND rel.target_node_id=NULLIF($2,'')::uuid AND prj.deleted_at IS NULL AND pk.slug='project')))`, orgID, projectID).Scan(&exists)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, bad("project must belong to a live customer organisation")
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return out, err
	}
	if settings.Revision == 0 && projectID == "" {
		return out, bad("quote settings must be configured for a customer-only draft")
	}
	var offerNo, customerNo string
	var day time.Time
	if settings.Revision > 0 {
		day, err = quoteDay(settings, time.Now())
		if err != nil {
			return out, err
		}
		customerNo, err = ensureCustomerNumber(ctx, tx, p, orgID, day)
		if err != nil {
			return out, err
		}
		offerNo, err = allocateOfferNumber(ctx, tx, p, day)
		if err != nil {
			return out, err
		}
	}
	fields := []byte(`{}`)
	if showcaseKey != "" {
		fields, err = json.Marshal(map[string]string{"showcase_key": showcaseKey})
		if err != nil {
			return out, err
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,fields) SELECT $1::uuid,aeon_next_node_key($1::uuid,k.short_prefix),k.id,$2,$3::jsonb FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug='quote' RETURNING id::text`, p.TenantID, title, fields).Scan(&out.QuoteNodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, conflict("quote kind is not configured")
	}
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO business_quotes(tenant_id,quote_node_id,project_node_id,customer_org_node_id,offer_no) VALUES($1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4::uuid,NULLIF($5,''))`, p.TenantID, out.QuoteNodeID, projectID, orgID, offerNo); err != nil {
		return out, err
	}
	if settings.Revision > 0 {
		doc, err := makeDocument(ctx, tx, settings, orgID, title, customerNo, day)
		if err != nil {
			return out, err
		}
		if profileID == "" {
			profileID = settings.DefaultProfileID
		}
		doc.Profile, err = readProfileSnapshot(ctx, tx, profileID)
		if err != nil {
			return out, err
		}
		raw, err := marshalDraft(doc)
		if err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO quote_drafts(tenant_id,quote_node_id,document,schema_version,minimum_writer_version,updated_by_principal_id) VALUES($1::uuid,$2::uuid,$3::jsonb,1,$4,$5::uuid)`, p.TenantID, out.QuoteNodeID, string(raw), doc.MinimumWriterVersion, p.ID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'customer_of')`, p.TenantID, orgID, out.QuoteNodeID); err != nil {
		return out, err
	}
	out, err = readQuote(ctx, tx, out.QuoteNodeID, false)
	if err != nil {
		return out, err
	}
	return out, appendEvent(ctx, tx, p, out.QuoteNodeID, "quote.created", nil, out)
}

// CreateShowcaseQuote opens a customer quote through the same insert the API uses.
func CreateShowcaseQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, title, orgID, profileID, key string) (ShowcaseQuote, error) {
	q, err := createCustomerQuote(ctx, tx, p, title, "", orgID, profileID, key)
	if err != nil {
		return ShowcaseQuote{}, explainDocument(err)
	}
	return showcaseQuote(q, title), nil
}

func buildShowcaseDocument(base quoteDocument, draft ShowcaseDraft, offerDate, validUntil, currency string, recipient *ShowcaseRecipient) (quoteDocument, error) {
	doc := base
	doc.SchemaVersion = 1
	if len(doc.Sender) == 0 {
		doc.Sender = json.RawMessage(`{}`)
	}
	if len(doc.Layout) == 0 {
		doc.Layout = json.RawMessage(`{}`)
	}
	if currency != "" {
		doc.Currency = currency
	}
	doc.Title = draft.Title
	doc.Subtitle = draft.Subtitle
	doc.ProjectRef = draft.ProjectRef
	doc.OfferDate = offerDate
	doc.ValidUntil = validUntil
	if recipient != nil {
		var rec map[string]string
		if err := json.Unmarshal(doc.Recipient, &rec); err != nil || rec == nil {
			return doc, bad("invalid recipient")
		}
		rec["contact"] = recipient.Name
		rec["email"] = recipient.Email
		rec["contact_node_id"] = recipient.ID
		raw, err := json.Marshal(rec)
		if err != nil {
			return doc, err
		}
		doc.Recipient = raw
	}
	legal, err := json.Marshal(map[string]string{
		"intro":         draft.Legal.Intro,
		"accept_text":   draft.Legal.AcceptText,
		"vat_note":      draft.Legal.VATNote,
		"discount_note": draft.Legal.DiscountNote,
		"payment_terms": draft.Legal.PaymentTerms,
	})
	if err != nil {
		return doc, err
	}
	doc.Legal = legal
	doc.Sections = make([]documentSection, 0, len(draft.Sections))
	for _, section := range draft.Sections {
		id, err := newID()
		if err != nil {
			return doc, err
		}
		nodes := make([]textNode, 0, len(section.Nodes))
		var body []string
		for _, node := range section.Nodes {
			nodeID, err := newID()
			if err != nil {
				return doc, err
			}
			marks := make([]textMark, len(node.Marks))
			for i, mark := range node.Marks {
				marks[i] = textMark{Start: mark.Start, End: mark.End, Bold: mark.Bold, Italic: mark.Italic}
			}
			nodes = append(nodes, textNode{ID: nodeID, Kind: node.Kind, Text: node.Text, Depth: node.Depth, Marker: node.Marker, Numbering: node.Numbering, ListStart: node.ListStart, ListContinue: node.ListContinue, SectionBound: node.SectionBound, Glyph: node.Glyph, MarkerXMM: node.MarkerXMM, MarkerYMM: node.MarkerYMM, TextStartMM: node.TextStartMM, Marks: marks})
			body = append(body, node.Text)
		}
		doc.Sections = append(doc.Sections, documentSection{ID: id, Heading: section.Heading, Body: strings.Join(body, "\n"), Nodes: nodes, NumberingStyle: section.NumberingStyle, PageBreakBefore: section.PageBreakBefore, KeepTogether: section.KeepTogether, SpacingBeforeMM: section.SpacingBeforeMM, SpacingAfterMM: section.SpacingAfterMM})
	}
	doc.Positions = make([]documentPosition, 0, len(draft.Positions))
	for _, position := range draft.Positions {
		id, err := newID()
		if err != nil {
			return doc, err
		}
		doc.Positions = append(doc.Positions, documentPosition{ID: id, PricingSource: "manual", ShortText: position.ShortText, LongText: position.LongText, Quantity: position.Quantity, UnitLabel: position.UnitLabel, UnitPriceCents: position.UnitPriceCents, Currency: doc.Currency})
	}
	doc.MinimumWriterVersion = documentMinimumWriterVersion(doc)
	if err := validateDocument(&doc, false); err != nil {
		return doc, explainDocument(err)
	}
	return doc, nil
}

// ValidateShowcaseDraft checks a bundle version against the tenant sender without writing.
func ValidateShowcaseDraft(sender, layout json.RawMessage, currency string, draft ShowcaseDraft, recipient map[string]string, offerDate, validUntil string, final bool) error {
	raw, err := json.Marshal(recipient)
	if err != nil {
		return err
	}
	doc, err := buildShowcaseDocument(quoteDocument{Sender: sender, Recipient: raw, Layout: layout, Currency: currency, Legal: json.RawMessage(`{}`)}, draft, offerDate, validUntil, currency, nil)
	if err != nil {
		return err
	}
	if final {
		return explainDocument(validateDocument(&doc, true))
	}
	return nil
}

// WriteShowcaseDraft replaces the open draft through the same checks as a document save.
func WriteShowcaseDraft(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, draft ShowcaseDraft, offerDate, validUntil, currency string, recipient *ShowcaseRecipient) error {
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if q.State != "draft" || q.Archived {
		return conflict("quote is not editable")
	}
	current, err := readDraft(ctx, tx, id, true)
	if err != nil {
		return err
	}
	base, err := decodeDocument(current.Document)
	if err != nil {
		return explainDocument(err)
	}
	doc, err := buildShowcaseDocument(base, draft, offerDate, validUntil, currency, recipient)
	if err != nil {
		return err
	}
	var customerNo string
	if err = tx.QueryRow(ctx, `SELECT customer_no FROM crm_customer_numbers WHERE organisation_node_id=$1::uuid`, q.CustomerOrgNodeID).Scan(&customerNo); err != nil {
		return err
	}
	var stored struct {
		CustomerNo string `json:"customer_no"`
	}
	if json.Unmarshal(doc.Recipient, &stored) != nil || stored.CustomerNo != customerNo {
		return bad("customer number is immutable")
	}
	minimumWriter := max(current.MinimumWriterVersion, doc.MinimumWriterVersion)
	doc.MinimumWriterVersion = minimumWriter
	raw, err := marshalDocument(doc)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE quote_drafts SET document=$1::jsonb,minimum_writer_version=$2,draft_revision=draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$3::uuid WHERE quote_node_id=$4::uuid`, string(raw), minimumWriter, p.ID, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE business_quotes SET project_ref=$1,revision=revision+1 WHERE quote_node_id=$2::uuid`, doc.ProjectRef, id); err != nil {
		return err
	}
	if doc.Title != "" {
		if _, err = tx.Exec(ctx, `UPDATE nodes SET title=$1,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$2::uuid AND title IS DISTINCT FROM $1`, doc.Title, id); err != nil {
			return err
		}
	}
	return appendEvent(ctx, tx, p, id, "quote.draft_updated", map[string]any{"draft_revision": current.DraftRevision, "quote_revision": q.Revision}, map[string]any{"draft_revision": current.DraftRevision + 1, "quote_revision": q.Revision + 1, "schema_version": doc.SchemaVersion, "source": "quote-showcase"})
}

func setQuoteVisibility(ctx context.Context, tx pgx.Tx, p tenant.Principal, q quote, archived bool) (quote, error) {
	if q.Archived == archived {
		return q, nil
	}
	var at, by any
	if archived {
		at = time.Now()
		by = p.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE business_quotes SET archived_at=$1::timestamptz,archived_by_principal_id=$2::uuid,revision=revision+1 WHERE quote_node_id=$3::uuid`, at, by, q.QuoteNodeID); err != nil {
		return quote{}, err
	}
	out, err := readQuote(ctx, tx, q.QuoteNodeID, false)
	if err != nil {
		return quote{}, err
	}
	if err = appendEvent(ctx, tx, p, q.QuoteNodeID, "quote.visibility_changed", map[string]any{"archived": q.Archived, "revision": q.Revision}, map[string]any{"archived": out.Archived, "revision": out.Revision}); err != nil {
		return quote{}, err
	}
	return out, nil
}

// ArchiveShowcaseQuote archives one quote through the visibility path. An archived quote is left unchanged.
func ArchiveShowcaseQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (ShowcaseQuote, bool, error) {
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		var f failure
		if errors.As(err, &f) && f.status == 404 {
			return ShowcaseQuote{}, false, fmt.Errorf("archive quote %s: quote not found", id)
		}
		return ShowcaseQuote{}, false, err
	}
	changed := !q.Archived
	out, err := setQuoteVisibility(ctx, tx, p, q, true)
	if err != nil {
		return ShowcaseQuote{}, false, err
	}
	title, err := nodeTitle(ctx, tx, out.QuoteNodeID)
	if err != nil {
		return ShowcaseQuote{}, false, err
	}
	return showcaseQuote(out, title), changed, nil
}

func issueQuoteDraft(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, linkKey []byte, pre *finalizeWrite) (quote, string, error) {
	var out quote
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		return out, "", err
	}
	draft, err := readDraft(ctx, tx, id, true)
	if err != nil {
		return out, "", err
	}
	if pre != nil && (q.Revision != pre.ExpectedQuoteRevision || draft.DraftRevision != pre.ExpectedDraftRevision || draft.DocumentSHA256 != pre.ExpectedDocumentSHA256) {
		return out, "", conflict("quote or draft revision is stale")
	}
	if q.State != "draft" || q.Archived || q.OfferNo == "" {
		return out, "", conflict("quote cannot be finalized")
	}
	doc, err := decodeDocument(draft.Document)
	if err != nil {
		return out, "", err
	}
	if err = validateDocument(&doc, true); err != nil {
		return out, "", err
	}
	// Frozen as the editor reads it: a draft stored with null lists (before
	// AEON-274) is issued with empty ones; the digest covers this value.
	doc = normalizedDraft(doc)
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return out, "", err
	}
	day, err := quoteDay(settings, time.Now())
	if err != nil {
		return out, "", err
	}
	if doc.ValidUntil < day.Format("2006-01-02") {
		return out, "", bad("quote validity has expired")
	}
	var customerNo string
	if err = tx.QueryRow(ctx, `SELECT customer_no FROM crm_customer_numbers WHERE organisation_node_id=$1::uuid`, q.CustomerOrgNodeID).Scan(&customerNo); err != nil {
		return out, "", err
	}
	var recipient struct {
		CustomerNo    string `json:"customer_no"`
		ContactNodeID string `json:"contact_node_id"`
	}
	if json.Unmarshal(doc.Recipient, &recipient) != nil || recipient.CustomerNo != customerNo {
		return out, "", conflict("customer number changed")
	}
	if recipient.ContactNodeID != "" {
		if !uuidRe.MatchString(recipient.ContactNodeID) {
			return out, "", bad("invalid recipient contact")
		}
		var linked bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_relations rel JOIN nodes n ON n.tenant_id=rel.tenant_id AND n.id=rel.source_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE rel.type='contact_for' AND rel.source_node_id=$1::uuid AND rel.target_node_id=$2::uuid AND n.deleted_at IS NULL AND k.slug='contact')`, recipient.ContactNodeID, q.CustomerOrgNodeID).Scan(&linked)
		if err != nil {
			return out, "", err
		}
		if !linked {
			return out, "", bad("recipient contact is not linked to customer")
		}
	}
	rateSnapshots := make([]string, len(doc.Positions))
	var subtotal int64
	for i := range doc.Positions {
		line := &doc.Positions[i]
		if line.PricingSource == "cost_unit" {
			var rateText string
			err = tx.QueryRow(ctx, `SELECT r.bill_amount::text FROM cost_unit_rates r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.cost_unit_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.cost_unit_node_id=$1::uuid AND r.currency=$2 AND r.unit=$3 AND r.effective_from<=$4::date AND (r.effective_until IS NULL OR r.effective_until>$4::date) AND n.deleted_at IS NULL AND k.slug='cost_unit' ORDER BY r.effective_from DESC LIMIT 1 FOR SHARE OF r`, line.CostUnitNodeID, doc.Currency, line.RateUnit, doc.OfferDate).Scan(&rateText)
			if errors.Is(err, pgx.ErrNoRows) {
				return out, "", bad("no effective cost unit rate")
			}
			if err != nil {
				return out, "", err
			}
			rateSnapshots[i] = rateText
			rate, err := parseDecimal(rateText, false)
			if err != nil {
				return out, "", err
			}
			qty, err := parseQuantity(line.Quantity)
			if err != nil {
				return out, "", err
			}
			line.TotalCents, err = rateCents(rate, qty)
			if err != nil {
				return out, "", err
			}
			line.UnitPriceCents, err = rateCents(rate, 100)
			if err != nil {
				return out, "", err
			}
		}
		if line.TotalCents > 1_000_000_000_000-subtotal {
			return out, "", bad("quote total too large")
		}
		subtotal += line.TotalCents
	}
	doc.NetTotalCents = subtotal
	versionNo := q.CurrentVersion + 1
	digest, err := documentDigest(id, versionNo, q.OfferNo, doc)
	if err != nil {
		return out, "", err
	}
	raw, err := marshalDocument(doc)
	if err != nil {
		return out, "", err
	}
	var contact any
	if recipient.ContactNodeID != "" {
		contact = recipient.ContactNodeID
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_versions(tenant_id,quote_node_id,version,recipient_contact_node_id,currency,title,subtotal,tax_total,total,content_sha256,created_by_principal_id,digest_mode,pricing_mode) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7::numeric,0,$7::numeric,$8,$9::uuid,'document-v2','cent-half-up-v1')`, p.TenantID, id, versionNo, contact, doc.Currency, doc.Title, decimalCents(subtotal), digest, p.ID); err != nil {
		return out, "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_version_snapshots(tenant_id,quote_node_id,version,document,sender,recipient,legal,layout,offer_no,customer_no,project_ref,offer_date,valid_until,validity_time_zone,document_schema_version,renderer_version) VALUES($1::uuid,$2::uuid,$3,$4::jsonb,$5::jsonb,$6::jsonb,$7::jsonb,$8::jsonb,$9,$10,$11,$12::date,$13::date,$14,1,'document-v1')`, p.TenantID, id, versionNo, string(raw), string(doc.Sender), string(doc.Recipient), string(doc.Legal), string(doc.Layout), q.OfferNo, customerNo, doc.ProjectRef, doc.OfferDate, doc.ValidUntil, settings.NumberingTimeZone); err != nil {
		return out, "", err
	}
	for i, line := range doc.Positions {
		var costID, rateText any
		if line.PricingSource == "cost_unit" {
			costID = line.CostUnitNodeID
			rateText = rateSnapshots[i]
		}
		if _, err = tx.Exec(ctx, `INSERT INTO quote_document_lines(tenant_id,quote_node_id,version,position,line_id,pricing_source,short_text,long_text,unit_label,cost_unit_node_id,currency,quantity,unit_price_cents,total_cents,rate_amount,net_amount) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid,$6,$7,$8,$9,$10::uuid,$11,$12::numeric,$13,$14,$15::numeric,$16::numeric)`, p.TenantID, id, versionNo, i, line.ID, line.PricingSource, line.ShortText, line.LongText, line.UnitLabel, costID, doc.Currency, line.Quantity, line.UnitPriceCents, line.TotalCents, rateText, decimalCents(line.TotalCents)); err != nil {
			return out, "", err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE business_quotes SET current_version=$1,revision=revision+1,project_ref=$2 WHERE quote_node_id=$3::uuid`, versionNo, doc.ProjectRef, id); err != nil {
		return out, "", err
	}
	if err = appendEvent(ctx, tx, p, id, "quote.version_created", map[string]any{"version": q.CurrentVersion}, map[string]any{"version": versionNo, "content_sha256": digest}); err != nil {
		return out, "", err
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.issued", Before: map[string]any{"state": "draft"}, After: map[string]any{"version": versionNo, "content_sha256": digest}})
	if err != nil {
		return out, "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_issues(tenant_id,quote_node_id,version,issued_by_principal_id,event_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, p.TenantID, id, versionNo, p.ID, ev.ID); err != nil {
		return out, "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE business_quotes SET state='issued',revision=revision+1 WHERE quote_node_id=$1::uuid`, id); err != nil {
		return out, "", err
	}
	if err = createIssuedLink(ctx, tx, p, id, versionNo, digest, linkKey); err != nil {
		return out, "", err
	}
	out, err = readQuote(ctx, tx, id, false)
	return out, digest, err
}

// IssueShowcaseQuote finalizes the open draft. linkKey nil leaves the quote without a public link.
func IssueShowcaseQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, linkKey []byte) (ShowcaseQuote, string, error) {
	q, digest, err := issueQuoteDraft(ctx, tx, p, id, linkKey, nil)
	if err != nil {
		return ShowcaseQuote{}, "", explainDocument(err)
	}
	title, err := nodeTitle(ctx, tx, q.QuoteNodeID)
	if err != nil {
		return ShowcaseQuote{}, "", err
	}
	return showcaseQuote(q, title), digest, nil
}

func branchQuoteDraft(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, expectedRevision int64, expectedVersion int, expectedDigest string) (draftRow, error) {
	var out draftRow
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		return out, err
	}
	if q.Revision != expectedRevision || q.CurrentVersion != expectedVersion || q.Archived || (q.State != "issued" && q.State != "accepted") {
		return out, conflict("quote cannot branch from this version")
	}
	v, err := readVersion(ctx, tx, id, expectedVersion)
	if err != nil {
		return out, err
	}
	if !quotedocument.IsDocument(v.DigestMode) || v.ContentSHA256 != expectedDigest {
		return out, conflict("source document digest is stale")
	}
	doc, err := decodeDocument(v.Document)
	if err != nil {
		return out, err
	}
	verified, err := quotedocument.Verify(v.DigestMode, id, v.Version, v.OfferNo, v.ContentSHA256, doc, v.Document)
	if err != nil {
		return out, err
	}
	if !verified {
		return out, conflict("source document digest mismatch")
	}
	doc.MinimumWriterVersion = documentMinimumWriterVersion(doc)
	if _, err = tx.Exec(ctx, `UPDATE business_quotes SET state='draft',revision=revision+1 WHERE quote_node_id=$1::uuid`, id); err != nil {
		return out, err
	}
	raw, err := marshalDraft(doc)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quote_drafts(tenant_id,quote_node_id,document,schema_version,minimum_writer_version,base_version,updated_by_principal_id) VALUES($1::uuid,$2::uuid,$3::jsonb,1,$4,$5,$6::uuid) ON CONFLICT(tenant_id,quote_node_id) DO UPDATE SET document=excluded.document,schema_version=excluded.schema_version,minimum_writer_version=excluded.minimum_writer_version,base_version=excluded.base_version,draft_revision=quote_drafts.draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=excluded.updated_by_principal_id`, p.TenantID, id, string(raw), doc.MinimumWriterVersion, v.Version, p.ID); err != nil {
		return out, err
	}
	out, err = readDraft(ctx, tx, id, false)
	if err != nil {
		return out, err
	}
	return out, appendEvent(ctx, tx, p, id, "quote.draft_branched", map[string]any{"quote_revision": q.Revision, "version": v.Version}, map[string]any{"quote_revision": out.QuoteRevision, "draft_revision": out.DraftRevision, "base_version": v.Version})
}

// BranchShowcaseQuote opens a new draft from an issued document version.
func BranchShowcaseQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, revision int64, version int, digest string) error {
	_, err := branchQuoteDraft(ctx, tx, p, id, revision, version, digest)
	return err
}

func acceptQuoteVersion(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, n int, digest string) (acceptance, error) {
	var out acceptance
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		return out, err
	}
	if q.CurrentVersion != n {
		return out, conflict("quote version is stale")
	}
	if q.State == "accepted" {
		err = tx.QueryRow(ctx, `SELECT customer_principal_id::text,recipient_contact_node_id::text,accepted_content_sha256,accepted_at,event_id FROM quote_acceptances WHERE quote_node_id=$1::uuid AND version=$2`, id, n).Scan(&out.CustomerPrincipalID, &out.RecipientContactNodeID, &out.AcceptedContentSHA256, &out.AcceptedAt, &out.EventID)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, conflict("quote is already accepted")
		}
		if err != nil {
			return out, err
		}
		if out.CustomerPrincipalID != p.ID || out.AcceptedContentSHA256 != digest {
			return out, conflict("quote is already accepted")
		}
		out.QuoteNodeID = id
		out.Version = n
		return out, nil
	}
	if q.State != "issued" {
		return out, conflict("quote is not issued")
	}
	if q.ClassicStatus == "expired" {
		return out, conflict("quote validity has expired")
	}
	if p.Kind != tenant.Person {
		return out, denied()
	}
	v, err := readVersion(ctx, tx, id, n)
	if err != nil {
		return out, err
	}
	if v.ContentSHA256 != digest {
		return out, conflict("quote content digest is stale")
	}
	var verified bool
	if quotedocument.IsDocument(v.DigestMode) {
		doc, err := decodeDocument(v.Document)
		if err != nil {
			return out, err
		}
		verified, err = quotedocument.Verify(v.DigestMode, id, n, v.OfferNo, v.ContentSHA256, doc, v.Document)
		if err != nil {
			return out, err
		}
	} else {
		sum, err := digestVersion(v)
		if err != nil {
			return out, err
		}
		verified = sum == v.ContentSHA256
	}
	if !verified {
		return out, conflict("version digest mismatch")
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crm_contact_principals cp JOIN principals pr ON pr.tenant_id=cp.tenant_id AND pr.id=cp.principal_id JOIN nodes c ON c.tenant_id=cp.tenant_id AND c.id=cp.contact_node_id JOIN node_relations rel ON rel.tenant_id=cp.tenant_id AND rel.source_node_id=cp.contact_node_id AND rel.target_node_id=$3::uuid AND rel.type='contact_for' WHERE cp.contact_node_id=$1::uuid AND cp.principal_id=$2::uuid AND pr.kind='person' AND c.deleted_at IS NULL)`, v.RecipientContactNodeID, p.ID, q.CustomerOrgNodeID).Scan(&allowed)
	if err != nil {
		return out, err
	}
	if !allowed {
		return out, denied()
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "quote.accepted", After: map[string]any{"version": n, "content_sha256": v.ContentSHA256, "recipient_contact_node_id": v.RecipientContactNodeID, "customer_principal_id": p.ID}})
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO quote_acceptances(tenant_id,quote_node_id,version,customer_principal_id,recipient_contact_node_id,accepted_content_sha256,event_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7) RETURNING accepted_at`, p.TenantID, id, n, p.ID, v.RecipientContactNodeID, v.ContentSHA256, ev.ID).Scan(&out.AcceptedAt); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE business_quotes SET state='accepted',revision=revision+1 WHERE quote_node_id=$1::uuid`, id); err != nil {
		return out, err
	}
	out.QuoteNodeID = id
	out.Version = n
	out.CustomerPrincipalID = p.ID
	out.RecipientContactNodeID = v.RecipientContactNodeID
	out.AcceptedContentSHA256 = v.ContentSHA256
	out.EventID = ev.ID
	return out, nil
}

// AcceptShowcaseQuote records the bound person's acceptance of the current issued version.
func AcceptShowcaseQuote(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, version int, digest string) error {
	_, err := acceptQuoteVersion(ctx, tx, p, id, version, digest)
	return err
}
