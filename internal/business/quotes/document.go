// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/quotedocument"
)

type textMark = quotedocument.TextMark
type textNode = quotedocument.TextNode
type documentSection = quotedocument.Section
type documentPosition = quotedocument.Position
type quoteDocument = quotedocument.Document

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func decodeDocument(raw []byte) (quoteDocument, error) {
	var d quoteDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			return d, badField(documentFieldPath(raw, typeError.Field, typeError.Value), "must be "+typeError.Type.String())
		}
		if field, ok := strings.CutPrefix(err.Error(), `json: unknown field "`); ok {
			return d, badField("document."+strings.TrimSuffix(field, `"`), "unsupported field")
		}
		return d, badField("document", "invalid JSON schema")
	}
	if d.SchemaVersion != 1 || d.MinimumWriterVersion < 1 || d.MinimumWriterVersion > 2 {
		return d, conflict("unsupported document writer")
	}
	if !jsonObject(d.Sender) || !jsonObject(d.Recipient) || !jsonObject(d.Legal) || !jsonObject(d.Layout) {
		return d, badField("document.sender/recipient/legal/layout", "must be objects")
	}
	if err := validateSnapshotFields(d.Sender, senderFields); err != nil {
		return d, badField("document.sender", err.Error())
	}
	if err := validateSnapshotFields(d.Recipient, recipientFields); err != nil {
		return d, badField("document.recipient", err.Error())
	}
	if err := validateSnapshotFields(d.Legal, legalFields); err != nil {
		return d, badField("document.legal", err.Error())
	}
	if err := validateSnapshotFields(d.Layout, layoutFields); err != nil {
		return d, badField("document.layout", err.Error())
	}
	if d.Profile != nil && (!uuidRe.MatchString(d.Profile.ID) || d.Profile.Revision < 1 || d.Profile.Definition.Schema != "inspr.document-profile.v1") {
		return d, badField("document.profile", "invalid profile snapshot")
	}
	return d, nil
}

// encoding/json omits array indices from UnmarshalTypeError.Field. Restore
// them so the editor can take a refused save to the affected section or row.
func documentFieldPath(raw []byte, field, valueKind string) string {
	fallback := "document." + field
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fallback
	}
	parts := strings.Split(field, ".")
	var find func(any, int, string) string
	find = func(value any, depth int, path string) string {
		if depth == len(parts) {
			switch valueKind {
			case "number":
				if _, ok := value.(float64); ok {
					return path
				}
			case "string":
				if _, ok := value.(string); ok {
					return path
				}
			case "bool":
				if _, ok := value.(bool); ok {
					return path
				}
			case "object":
				if _, ok := value.(map[string]any); ok {
					return path
				}
			case "array":
				if _, ok := value.([]any); ok {
					return path
				}
			case "null":
				if value == nil {
					return path
				}
			}
			return ""
		}
		if list, ok := value.([]any); ok {
			for index, item := range list {
				if found := find(item, depth, fmt.Sprintf("%s[%d]", path, index)); found != "" {
					return found
				}
			}
			return ""
		}
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		child, ok := object[parts[depth]]
		if !ok {
			return ""
		}
		return find(child, depth+1, path+"."+parts[depth])
	}
	if found := find(root, 0, "document"); found != "" {
		return found
	}
	return fallback
}

// Inline marks are part of the full-document write contract introduced by
// writer 2. Keep the requirement after all marks are removed so a stale older
// editor cannot later replace a marked document with an unformatted copy.
func documentMinimumWriterVersion(d quoteDocument) int {
	if d.MinimumWriterVersion > 1 {
		return d.MinimumWriterVersion
	}
	for _, section := range d.Sections {
		for _, node := range section.Nodes {
			if len(node.Marks) > 0 {
				return 2
			}
		}
	}
	return 1
}

var senderFields = map[string]bool{"company": true, "street": true, "postal_code": true, "city": true, "country": true, "register_no": true, "register_court": true, "email": true, "phone": true, "website": true, "uid": true, "bank_name": true, "iban": true, "bic": true, "contact_person": true, "logo_file_id": true, "logo_sha256": true}
var recipientFields = map[string]bool{"name": true, "address": true, "contact": true, "country": true, "customer_no": true, "email": true, "contact_node_id": true}
var legalFields = map[string]bool{"intro": true, "accept_text": true, "vat_note": true, "discount_note": true, "payment_terms": true}
var layoutFields = map[string]bool{"logo_width_mm": true, "logo_offset_mm": true, "logo_file_id": true, "logo_sha256": true, "page_style": true}

func validateSnapshotFields(raw json.RawMessage, allowed map[string]bool) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return bad("invalid document snapshot")
	}
	for name, value := range fields {
		if !allowed[name] {
			return bad("unsupported document snapshot field")
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil || len(s) > 100000 {
			return bad("invalid document snapshot field")
		}
		if allowed["logo_width_mm"] {
			switch name {
			case "logo_width_mm":
				if _, err := parseMM(s, 180, 960); err != nil {
					return err
				}
			case "logo_offset_mm":
				if _, err := parseMM(s, -60, 100); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func parseMM(s string, min, max int64) (int64, error) {
	if s == "" {
		return 0, nil
	}
	sign := int64(1)
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 3 {
		return 0, bad("invalid millimetres")
	}
	for _, part := range parts {
		if part == "" {
			return 0, bad("invalid millimetres")
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, bad("invalid millimetres")
			}
		}
	}
	if len(parts) == 2 && len(parts[1]) != 1 {
		return 0, bad("millimetres require one decimal place")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, bad("invalid millimetres")
	}
	value := whole * 10
	if len(parts) == 2 {
		frac, _ := strconv.ParseInt(parts[1], 10, 64)
		value += frac
	}
	value *= sign
	if value < min || value > max {
		return 0, bad("millimetres out of range")
	}
	return value, nil
}
func validateDocument(d *quoteDocument, final bool) error {
	if len(d.Title) > 512 || len(d.Subtitle) > 512 || len(d.ProjectRef) > 512 || !currencyRe.MatchString(d.Currency) || len(d.Sections) > 20 || len(d.Positions) > 100 {
		return badField("document", "title, currency, sections or positions exceed document limits")
	}
	day, e := time.Parse("2006-01-02", d.OfferDate)
	if e != nil {
		return badField("document.offer_date", "must be a calendar date")
	}
	until, e := time.Parse("2006-01-02", d.ValidUntil)
	if e != nil || until.Before(day) {
		return badField("document.valid_until", "must be a date on or after offer date")
	}
	if final && (strings.TrimSpace(d.Title) == "" || len(d.Positions) == 0) {
		return badField("document", "needs a title and position")
	}
	ids := map[string]bool{}
	for si, s := range d.Sections {
		sectionPath := fmt.Sprintf("document.sections[%d]", si)
		if !uuidRe.MatchString(s.ID) || ids[s.ID] || len(s.Heading) > 500 || len(s.Body) > 100000 || len(s.Nodes) > 100 {
			return badField(sectionPath, "invalid ID or content bounds")
		}
		if s.NumberingStyle != "" && s.NumberingStyle != "decimal" && s.NumberingStyle != "upper-roman" && s.NumberingStyle != "lower-roman" && s.NumberingStyle != "upper-alpha" && s.NumberingStyle != "lower-alpha" && s.NumberingStyle != "none" {
			return badField(sectionPath+".numbering_style", "unsupported numbering style")
		}
		if _, err := parseMM(s.SpacingBeforeMM, 0, 400); err != nil {
			return badField(sectionPath+".spacing_before_mm", err.Error())
		}
		if _, err := parseMM(s.SpacingAfterMM, 0, 400); err != nil {
			return badField(sectionPath+".spacing_after_mm", err.Error())
		}
		ids[s.ID] = true
		for ni, n := range s.Nodes {
			nodePath := fmt.Sprintf("%s.nodes[%d]", sectionPath, ni)
			if !uuidRe.MatchString(n.ID) || ids[n.ID] || utf8.RuneCountInString(n.Text) > 2000 || n.Depth < 0 || n.Depth > 5 || utf8.RuneCountInString(n.Glyph) > 4 {
				return badField(nodePath, "invalid ID or content bounds")
			}
			ids[n.ID] = true
			if n.Kind != "paragraph" && n.Kind != "item" {
				return badField(nodePath+".kind", "must be paragraph or item")
			}
			if n.Kind == "paragraph" && (n.Marker != "" || n.Numbering != "" || n.ListStart != 0 || n.ListContinue || n.SectionBound) {
				return badField(nodePath, "paragraph cannot have list markers")
			}
			if (n.Kind != "item" || n.Marker != "decimal") && (n.Numbering != "" || n.ListStart != 0 || n.ListContinue || n.SectionBound) {
				return badField(nodePath+".numbering", "requires a decimal item")
			}
			if n.Kind == "paragraph" && (n.Depth != 0 || n.Glyph != "" || n.MarkerXMM != "" || n.MarkerYMM != "" || n.TextStartMM != "") {
				return badField(nodePath, "paragraph cannot have item layout")
			}
			if n.Glyph != "" && (n.Marker == "decimal" || strings.TrimSpace(n.Glyph) != n.Glyph || strings.ContainsAny(n.Glyph, "<>&")) {
				return badField(nodePath+".glyph", "invalid bullet glyph")
			}
			if _, err := parseMM(n.MarkerXMM, -300, 300); err != nil {
				return badField(nodePath+".marker_x_mm", err.Error())
			}
			if _, err := parseMM(n.MarkerYMM, -200, 200); err != nil {
				return badField(nodePath+".marker_y_mm", err.Error())
			}
			if _, err := parseMM(n.TextStartMM, -200, 400); err != nil {
				return badField(nodePath+".text_start_mm", err.Error())
			}
			if n.Marker != "" && n.Marker != "disc" && n.Marker != "circle" && n.Marker != "square" && n.Marker != "dash" && n.Marker != "decimal" {
				return badField(nodePath+".marker", "unsupported list marker")
			}
			if n.Numbering != "" && n.Numbering != "outline" {
				return badField(nodePath+".numbering", "unsupported numbering")
			}
			if n.ListStart < 0 || n.ListStart > 9999 || n.ListStart > 0 && n.ListContinue || n.SectionBound && n.Numbering != "outline" {
				return badField(nodePath+".list_start", "invalid numbering start")
			}
			units := utf16.Encode([]rune(n.Text))
			end := 0
			for mi, m := range n.Marks {
				if m.Start < end || m.Start < 0 || m.End <= m.Start || m.End > len(units) || (!m.Bold && !m.Italic) || !utf16Boundary(units, m.Start) || !utf16Boundary(units, m.End) {
					return badField(fmt.Sprintf("%s.marks[%d]", nodePath, mi), "invalid inline mark range")
				}
				end = m.End
			}
		}
	}
	var total int64
	for i := range d.Positions {
		p := &d.Positions[i]
		positionPath := fmt.Sprintf("document.positions[%d]", i)
		if !uuidRe.MatchString(p.ID) || ids[p.ID] || len(p.ShortText) > 2000 || len(p.LongText) > 10000 || len(p.UnitLabel) > 80 || strings.TrimSpace(p.UnitLabel) == "" || p.Currency != d.Currency || p.UnitPriceCents < 0 || p.UnitPriceCents > 1_000_000_000 {
			return badField(positionPath, "invalid ID, currency, unit or content bounds")
		}
		ids[p.ID] = true
		if p.PricingSource != "manual" && p.PricingSource != "cost_unit" {
			return badField(positionPath+".pricing_source", "unsupported pricing source")
		}
		if p.PricingSource == "manual" && p.CostUnitNodeID != "" || p.PricingSource == "cost_unit" && !uuidRe.MatchString(p.CostUnitNodeID) {
			return badField(positionPath+".cost_unit_node_id", "invalid cost unit")
		}
		if (p.PricingSource == "manual" && p.RateUnit != "") || (p.PricingSource == "cost_unit" && p.RateUnit != "hour" && p.RateUnit != "day" && p.RateUnit != "item") {
			return badField(positionPath+".rate_unit", "invalid rate unit")
		}
		qty, e := parseQuantity(p.Quantity)
		if e != nil {
			return badField(positionPath+".quantity", "invalid quantity")
		}
		if final && (strings.TrimSpace(p.ShortText) == "" || qty == 0) {
			return badField(positionPath, "position needs text and a positive quantity")
		}
		amount := new(big.Int).Mul(big.NewInt(qty), big.NewInt(p.UnitPriceCents))
		amount.Add(amount, big.NewInt(50))
		amount.Div(amount, big.NewInt(100))
		if !amount.IsInt64() || amount.Int64() > 1_000_000_000_000-total {
			return badField(positionPath+".unit_price_cents", "quote total too large")
		}
		p.TotalCents = amount.Int64()
		total += p.TotalCents
	}
	d.NetTotalCents = total
	if final {
		var sender struct {
			Company    string `json:"company"`
			Street     string `json:"street"`
			PostalCode string `json:"postal_code"`
			City       string `json:"city"`
			Country    string `json:"country"`
			Email      string `json:"email"`
		}
		var recipient struct {
			Name    string `json:"name"`
			Address string `json:"address"`
			Email   string `json:"email"`
		}
		if json.Unmarshal(d.Sender, &sender) != nil || json.Unmarshal(d.Recipient, &recipient) != nil || strings.TrimSpace(sender.Company) == "" || strings.TrimSpace(sender.Street) == "" || strings.TrimSpace(sender.PostalCode) == "" || strings.TrimSpace(sender.City) == "" || strings.TrimSpace(sender.Country) == "" || strings.TrimSpace(recipient.Name) == "" || strings.TrimSpace(recipient.Address) == "" || !validEmail(sender.Email) || !validEmail(recipient.Email) {
			return bad("sender or recipient is incomplete")
		}
	}
	return nil
}
func validEmail(s string) bool { a, e := mail.ParseAddress(s); return e == nil && a.Address == s }
func utf16Boundary(units []uint16, at int) bool {
	return at <= 0 || at >= len(units) || !(units[at-1] >= 0xD800 && units[at-1] <= 0xDBFF && units[at] >= 0xDC00 && units[at] <= 0xDFFF)
}
func parseQuantity(s string) (int64, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 7 {
		return 0, bad("invalid quantity")
	}
	for _, part := range parts {
		if part == "" {
			return 0, bad("invalid quantity")
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, bad("invalid quantity")
			}
		}
	}
	if len(parts[0]) > 1 && parts[0][0] == '0' {
		return 0, bad("invalid quantity")
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole > 1_000_000 {
		return 0, bad("invalid quantity")
	}
	var frac int64
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, bad("invalid quantity")
		}
		frac, e = strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			return 0, bad("invalid quantity")
		}
		if len(parts[1]) == 1 {
			frac *= 10
		}
	}
	return whole*100 + frac, nil
}
func makeDocument(ctx context.Context, tx pgx.Tx, settings quoteSettings, org, title, customerNo string, day time.Time) (quoteDocument, error) {
	var d quoteDocument
	recipient, err := customerRecipient(ctx, tx, org, customerNo)
	if err != nil {
		return d, err
	}
	var defaults struct {
		Intro  string `json:"intro"`
		Blocks []struct {
			Heading string     `json:"heading"`
			Body    string     `json:"body"`
			Nodes   []textNode `json:"nodes"`
		} `json:"blocks"`
		AcceptText string `json:"accept_text"`
		VATNote    string `json:"vat_note"`
	}
	if err := json.Unmarshal(settings.Defaults, &defaults); err != nil {
		return d, err
	}
	recipientRaw, _ := json.Marshal(recipient)
	legal, _ := json.Marshal(map[string]string{"intro": defaults.Intro, "accept_text": defaults.AcceptText, "vat_note": defaults.VATNote})
	positionID, err := newID()
	if err != nil {
		return d, err
	}
	d = quoteDocument{SchemaVersion: 1, MinimumWriterVersion: 1, Title: title, OfferDate: day.Format("2006-01-02"), ValidUntil: day.AddDate(0, 0, 30).Format("2006-01-02"), Currency: settings.DefaultCurrency, Sender: settings.Sender, Recipient: recipientRaw, Legal: legal, Layout: settings.Layout, Sections: []documentSection{}, Positions: []documentPosition{{ID: positionID, PricingSource: "manual", Quantity: "1", UnitLabel: "item", Currency: settings.DefaultCurrency}}}
	for _, b := range defaults.Blocks {
		id, e := newID()
		if e != nil {
			return d, e
		}
		section := documentSection{ID: id, Heading: b.Heading, Body: b.Body, Nodes: b.Nodes}
		if section.Nodes == nil {
			section.Nodes = []textNode{}
		}
		for i := range section.Nodes {
			nodeID, e := newID()
			if e != nil {
				return d, e
			}
			section.Nodes[i].ID = nodeID
		}
		d.Sections = append(d.Sections, section)
	}
	d.MinimumWriterVersion = documentMinimumWriterVersion(d)
	return d, nil
}

func customerRecipient(ctx context.Context, tx pgx.Tx, org, customerNo string) (map[string]string, error) {
	var name string
	var orgFields, contactFields []byte
	var contactName, contactID string
	err := tx.QueryRow(ctx, `SELECT o.title,o.fields,coalesce(c.title,''),coalesce(c.fields,'{}'::jsonb),coalesce(c.id::text,'')
		FROM nodes o LEFT JOIN crm_organisation_profiles p ON p.tenant_id=o.tenant_id AND p.organisation_node_id=o.id
		LEFT JOIN nodes c ON c.tenant_id=p.tenant_id AND c.id=p.primary_contact_node_id AND c.deleted_at IS NULL
		WHERE o.id=$1::uuid AND o.deleted_at IS NULL`, org).Scan(&name, &orgFields, &contactName, &contactFields, &contactID)
	if err != nil {
		return nil, err
	}
	var fields struct {
		BillingAddress  quoteAddress `json:"billing_address"`
		VisitingAddress quoteAddress `json:"visiting_address"`
	}
	if err := json.Unmarshal(orgFields, &fields); err != nil {
		return nil, err
	}
	var contact struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(contactFields, &contact); err != nil {
		return nil, err
	}
	address := fields.VisitingAddress
	if fields.BillingAddress.hasValue() {
		address = fields.BillingAddress
	}
	return map[string]string{"name": name, "address": address.display(), "contact": contactName, "country": address.Country, "customer_no": customerNo, "email": contact.Email, "contact_node_id": contactID}, nil
}

type quoteAddress struct {
	Street     string `json:"street"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	Country    string `json:"country"`
	Freeform   string `json:"freeform"`
}

func (a quoteAddress) hasValue() bool {
	return strings.TrimSpace(a.Street+a.PostalCode+a.City+a.Freeform) != ""
}

func (a quoteAddress) display() string {
	if strings.TrimSpace(a.Freeform) != "" {
		return strings.TrimSpace(a.Freeform)
	}
	return strings.TrimSpace(strings.Join([]string{strings.TrimSpace(a.Street), strings.TrimSpace(a.PostalCode + " " + a.City)}, "\n"))
}
