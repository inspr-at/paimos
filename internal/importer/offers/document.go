// SPDX-License-Identifier: AGPL-3.0-only
package offers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/inspr-at/paimos/internal/business/quotedocument"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var offerNumber = regexp.MustCompile(`^A([0-9]{6})-([0-9]{2,})$`)
var exactMillimetres = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9])?$`)

type legacyDocument struct {
	Title      string                 `json:"title"`
	Subtitle   string                 `json:"subtitle"`
	ProjectRef string                 `json:"project_ref"`
	OfferDate  string                 `json:"offer_date"`
	ValidUntil string                 `json:"valid_until"`
	Sender     json.RawMessage        `json:"sender"`
	Customer   json.RawMessage        `json:"customer"`
	Intro      string                 `json:"intro"`
	AcceptText string                 `json:"accept_text"`
	VATNote    string                 `json:"vat_note"`
	Footer     map[string]json.Number `json:"footer"`
	Blocks     []struct {
		Heading string           `json:"heading"`
		Body    string           `json:"body"`
		Nodes   []map[string]any `json:"nodes"`
	} `json:"blocks"`
	Positions []struct {
		ShortText      string      `json:"short_text"`
		LongText       string      `json:"long_text"`
		Quantity       json.Number `json:"quantity"`
		Unit           string      `json:"unit"`
		UnitPriceCents int64       `json:"unit_price_cents"`
		TotalCents     int64       `json:"total_cents"`
	} `json:"positions"`
	NetTotalCents int64 `json:"net_total_cents"`
}

// Imports use the same model as native quote writes and verification.
type Document = quotedocument.Document
type ProfileSnapshot = quotedocument.ProfileSnapshot
type Section = quotedocument.Section
type Position = quotedocument.Position

func convertDocument(instance string, o Offer, contactID string) (Document, error) {
	var src legacyDocument
	if err := decode(o.Document, &src); err != nil {
		return Document{}, fmt.Errorf("offer %d document: %w", o.ID, err)
	}
	if src.Title == "" || len(src.Title) > 512 || len(src.Blocks) > 20 || len(src.Positions) > 100 {
		return Document{}, errors.New("invalid offer document bounds")
	}
	day, err := time.Parse("2006-01-02", src.OfferDate)
	if err != nil {
		return Document{}, errors.New("invalid offer date")
	}
	valid, err := time.Parse("2006-01-02", src.ValidUntil)
	if err != nil || valid.Before(day) {
		return Document{}, errors.New("invalid validity date")
	}
	if !json.Valid(src.Sender) || !json.Valid(src.Customer) {
		return Document{}, errors.New("missing document party snapshot")
	}
	var recipient map[string]any
	if err := json.Unmarshal(src.Customer, &recipient); err != nil || recipient == nil {
		return Document{}, errors.New("invalid recipient")
	}
	if contactID != "" {
		recipient["contact_node_id"] = contactID
	}
	recipientJSON, _ := json.Marshal(recipient)
	d := Document{SchemaVersion: 1, MinimumWriterVersion: 1, Title: src.Title, Subtitle: src.Subtitle, ProjectRef: src.ProjectRef, OfferDate: src.OfferDate, ValidUntil: src.ValidUntil, Currency: "EUR", Sender: src.Sender, Recipient: recipientJSON, Legal: nil, Layout: nil, Sections: []Section{}, Positions: []Position{}}
	d.Legal, err = json.Marshal(map[string]string{"intro": src.Intro, "accept_text": src.AcceptText, "vat_note": src.VATNote})
	if err != nil {
		return Document{}, err
	}
	layout := map[string]string{}
	for _, key := range []string{"logo_width_mm", "logo_offset_mm"} {
		if v, ok := src.Footer[key]; ok {
			layout[key] = v.String()
		}
	}
	d.Layout, err = json.Marshal(layout)
	if err != nil {
		return Document{}, err
	}
	for i, s := range src.Blocks {
		if len(s.Nodes) > 100 {
			return Document{}, errors.New("too many prose nodes")
		}
		section := Section{ID: stableID(instance, "offer", o.ID, "section", i), Heading: s.Heading, Body: s.Body, Nodes: []quotedocument.TextNode{}}
		for j, n := range s.Nodes {
			copyNode := map[string]any{}
			for k, v := range n {
				copyNode[k] = v
			}
			if _, err := normalizeNodeDimensions(copyNode); err != nil {
				return Document{}, fmt.Errorf("offer %d section %d node %d: %w", o.ID, i, j, err)
			}
			copyNode["id"] = stableID(instance, "offer", o.ID, fmt.Sprintf("section-%d-node", i), j)
			raw, err := json.Marshal(copyNode)
			if err != nil {
				return Document{}, err
			}
			var node quotedocument.TextNode
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&node); err != nil {
				return Document{}, fmt.Errorf("offer %d prose node: %w", o.ID, err)
			}
			if len(node.Marks) > 0 {
				d.MinimumWriterVersion = 2
			}
			section.Nodes = append(section.Nodes, node)
		}
		d.Sections = append(d.Sections, section)
	}
	for i, p := range src.Positions {
		qty := p.Quantity.String()
		if qty == "" {
			return Document{}, errors.New("position quantity missing")
		}
		computed, err := lineTotal(p.UnitPriceCents, qty)
		if err != nil || computed != p.TotalCents {
			return Document{}, fmt.Errorf("offer %d position %d total mismatch", o.ID, i)
		}
		if strings.TrimSpace(p.ShortText) == "" || p.Unit == "" {
			return Document{}, errors.New("invalid position")
		}
		d.Positions = append(d.Positions, Position{ID: stableID(instance, "offer", o.ID, "position", i), PricingSource: "manual", ShortText: p.ShortText, LongText: p.LongText, Quantity: qty, UnitLabel: p.Unit, UnitPriceCents: p.UnitPriceCents, TotalCents: p.TotalCents, Currency: "EUR"})
		if d.NetTotalCents > 1_000_000_000_000-p.TotalCents {
			return Document{}, errors.New("offer total too large")
		}
		d.NetTotalCents += p.TotalCents
	}
	if d.NetTotalCents != src.NetTotalCents {
		return Document{}, errors.New("offer net total mismatch")
	}
	if o.Status != "draft" && len(d.Positions) == 0 {
		return Document{}, errors.New("issued offer needs a position")
	}
	return d, nil
}

// Classic emits layout measurements as JSON numbers; Aeon's quote document
// uses exact decimal strings. This is also used by the operator repair so an
// import and a repair produce the same draft shape without floating point.
func normalizeNodeDimensions(node map[string]any) (bool, error) {
	changed := false
	for _, dimension := range []struct {
		field    string
		min, max int64
	}{
		{"marker_x_mm", -300, 300}, {"marker_y_mm", -200, 200}, {"text_start_mm", -200, 400},
	} {
		field := dimension.field
		value, ok := node[field]
		if !ok {
			continue
		}
		number, ok := value.(json.Number)
		if !ok {
			continue
		}
		text := number.String()
		if !exactMillimetres.MatchString(text) {
			return false, fmt.Errorf("%s has unsupported precision", field)
		}
		signed := strings.TrimPrefix(text, "-")
		parts := strings.Split(signed, ".")
		whole, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || whole > 1000 {
			return false, fmt.Errorf("%s is out of range", field)
		}
		tenths := whole * 10
		if len(parts) == 2 {
			tenths += int64(parts[1][0] - '0')
		}
		if strings.HasPrefix(text, "-") {
			tenths = -tenths
		}
		if tenths < dimension.min || tenths > dimension.max {
			return false, fmt.Errorf("%s is out of range", field)
		}
		node[field] = text
		changed = true
	}
	return changed, nil
}
func lineTotal(price int64, quantity string) (int64, error) {
	if price < 0 {
		return 0, errors.New("negative price")
	}
	parts := strings.Split(quantity, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 15 {
		return 0, errors.New("invalid quantity")
	}
	for _, p := range parts {
		if p == "" {
			return 0, errors.New("invalid quantity")
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return 0, errors.New("invalid quantity")
			}
		}
	}
	if len(parts) == 2 && len(parts[1]) > 2 {
		return 0, errors.New("quantity has too many decimals")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	qty := whole * 100
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) == 1 {
			frac += "0"
		}
		n, _ := strconv.ParseInt(frac, 10, 64)
		qty += n
	}
	if qty <= 0 {
		return 0, errors.New("quantity must be positive")
	}
	n := new(big.Int).Mul(big.NewInt(price), big.NewInt(qty))
	n.Add(n, big.NewInt(50))
	n.Div(n, big.NewInt(100))
	if !n.IsInt64() || n.Int64() > 1_000_000_000_000 {
		return 0, errors.New("line total too large")
	}
	return n.Int64(), nil
}
func majorMoney(value json.Number) (int64, error) {
	s := value.String()
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 12 {
		return 0, errors.New("invalid source money")
	}
	for _, part := range parts {
		if part == "" {
			return 0, errors.New("invalid source money")
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, errors.New("invalid source money")
			}
		}
	}
	if len(parts) == 2 && len(parts[1]) > 2 {
		return 0, errors.New("source money has sub-cent precision")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	minor := whole * 100
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) == 1 {
			frac += "0"
		}
		n, _ := strconv.ParseInt(frac, 10, 64)
		minor += n
	}
	return minor, nil
}
func stableID(instance, kind string, id int64, part string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("paimos:%s:%s:%d:%s:%d", instance, kind, id, part, index)))
	sum[6] = (sum[6] & 15) | 80
	sum[8] = (sum[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
func sha(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
