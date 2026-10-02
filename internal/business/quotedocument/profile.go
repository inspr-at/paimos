// SPDX-License-Identifier: AGPL-3.0-only
package quotedocument

type Font struct {
	Role    string `json:"role"`
	Family  string `json:"family"`
	Weight  int    `json:"weight"`
	Style   string `json:"style"`
	AssetID string `json:"asset_id"`
}
type Page struct {
	WidthMM  string `json:"width_mm"`
	HeightMM string `json:"height_mm"`
	TopMM    string `json:"top_mm"`
	RightMM  string `json:"right_mm"`
	BottomMM string `json:"bottom_mm"`
	LeftMM   string `json:"left_mm"`
}
type Column struct {
	Key     string `json:"key"`
	WidthMM string `json:"width_mm"`
}
type Table struct {
	Columns      []Column `json:"columns"`
	Separator    string   `json:"separator"`
	RepeatHeader bool     `json:"repeat_header"`
}
type Totals struct {
	VAT      string `json:"vat"`
	Discount string `json:"discount"`
	NetLabel string `json:"net_label"`
}
type Payment struct {
	Position string `json:"position"`
	Heading  string `json:"heading"`
}
type Acceptance struct {
	SignatureColumns int    `json:"signature_columns"`
	GapMM            string `json:"gap_mm"`
	LeadMM           string `json:"lead_mm"`
}
type Footer struct {
	AssetID          string `json:"asset_id,omitempty"`
	DotsAssetID      string `json:"dots_asset_id,omitempty"`
	WidthMM          string `json:"width_mm"`
	OffsetMM         string `json:"offset_mm"`
	PageNumberFormat string `json:"page_number_format"`
}
type ProfileDefinition struct {
	Schema         string            `json:"schema"`
	LayoutVariant  string            `json:"layout_variant"`
	Locale         string            `json:"locale"`
	Fonts          []Font            `json:"fonts"`
	Colors         map[string]string `json:"colors"`
	Typography     map[string]string `json:"typography"`
	Page           Page              `json:"page"`
	Cover          map[string]string `json:"cover"`
	Sections       map[string]string `json:"sections"`
	PositionsTable Table             `json:"positions_table"`
	Totals         Totals            `json:"totals"`
	PaymentTerms   Payment           `json:"payment_terms"`
	Acceptance     Acceptance        `json:"acceptance"`
	Footer         Footer            `json:"footer"`
	Labels         map[string]string `json:"labels"`
}
type ProfileSnapshot struct {
	ID         string            `json:"id"`
	Revision   int               `json:"revision"`
	Definition ProfileDefinition `json:"definition"`
}

// normalized returns the definition with empty lists and maps instead of nil.
func (d ProfileDefinition) Normalized() ProfileDefinition {
	if d.Fonts == nil {
		d.Fonts = []Font{}
	}
	if d.PositionsTable.Columns == nil {
		d.PositionsTable.Columns = []Column{}
	}
	for _, m := range []*map[string]string{&d.Colors, &d.Typography, &d.Cover, &d.Sections, &d.Labels} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	return d
}
