// SPDX-License-Identifier: AGPL-3.0-only
// Package quotedocument owns the shared native and import document contract.
package quotedocument

import "encoding/json"

type TextMark struct {
	Start  int  `json:"start"`
	End    int  `json:"end"`
	Bold   bool `json:"bold,omitempty"`
	Italic bool `json:"italic,omitempty"`
}
type TextNode struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Text         string     `json:"text"`
	Depth        int        `json:"depth,omitempty"`
	Marker       string     `json:"marker,omitempty"`
	Numbering    string     `json:"numbering,omitempty"`
	ListStart    int        `json:"list_start,omitempty"`
	ListContinue bool       `json:"list_continue,omitempty"`
	SectionBound bool       `json:"section_bound,omitempty"`
	Glyph        string     `json:"glyph,omitempty"`
	MarkerXMM    string     `json:"marker_x_mm,omitempty"`
	MarkerYMM    string     `json:"marker_y_mm,omitempty"`
	TextStartMM  string     `json:"text_start_mm,omitempty"`
	Marks        []TextMark `json:"marks,omitempty"`
}
type Section struct {
	ID              string     `json:"id"`
	Heading         string     `json:"heading"`
	Body            string     `json:"body"`
	Nodes           []TextNode `json:"nodes"`
	NumberingStyle  string     `json:"numbering_style,omitempty"`
	PageBreakBefore *bool      `json:"page_break_before,omitempty"`
	KeepTogether    *bool      `json:"keep_together,omitempty"`
	SpacingBeforeMM string     `json:"spacing_before_mm,omitempty"`
	SpacingAfterMM  string     `json:"spacing_after_mm,omitempty"`
}
type Position struct {
	ID             string `json:"id"`
	PricingSource  string `json:"pricing_source"`
	ShortText      string `json:"short_text"`
	LongText       string `json:"long_text"`
	Quantity       string `json:"quantity"`
	UnitLabel      string `json:"unit_label"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	TotalCents     int64  `json:"total_cents"`
	CostUnitNodeID string `json:"cost_unit_node_id,omitempty"`
	RateUnit       string `json:"rate_unit,omitempty"`
	Currency       string `json:"currency"`
}
type Document struct {
	SchemaVersion        int              `json:"schema_version"`
	MinimumWriterVersion int              `json:"minimum_writer_version"`
	Title                string           `json:"title"`
	Subtitle             string           `json:"subtitle"`
	ProjectRef           string           `json:"project_ref"`
	OfferDate            string           `json:"offer_date"`
	ValidUntil           string           `json:"valid_until"`
	Currency             string           `json:"currency"`
	Sender               json.RawMessage  `json:"sender"`
	Recipient            json.RawMessage  `json:"recipient"`
	Legal                json.RawMessage  `json:"legal"`
	Layout               json.RawMessage  `json:"layout"`
	Profile              *ProfileSnapshot `json:"profile,omitempty"`
	Sections             []Section        `json:"sections"`
	Positions            []Position       `json:"positions"`
	NetTotalCents        int64            `json:"net_total_cents"`
}
