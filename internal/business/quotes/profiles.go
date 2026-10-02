// SPDX-License-Identifier: AGPL-3.0-only
package quotes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/plugins/fence"
)

// Profiles are part of the existing business_quotes module and compiled
// ManifestPlugin. The coordinator mounts quotes.New; this package does not
// register itself in cmd/aeon or the builtin plugin registry.
type profileFont struct {
	Role    string `json:"role"`
	Family  string `json:"family"`
	Weight  int    `json:"weight"`
	Style   string `json:"style"`
	AssetID string `json:"asset_id"`
}
type profilePage struct {
	WidthMM  string `json:"width_mm"`
	HeightMM string `json:"height_mm"`
	TopMM    string `json:"top_mm"`
	RightMM  string `json:"right_mm"`
	BottomMM string `json:"bottom_mm"`
	LeftMM   string `json:"left_mm"`
}
type profileColumn struct {
	Key     string `json:"key"`
	WidthMM string `json:"width_mm"`
}
type profileTable struct {
	Columns      []profileColumn `json:"columns"`
	Separator    string          `json:"separator"`
	RepeatHeader bool            `json:"repeat_header"`
}
type profileTotals struct {
	VAT      string `json:"vat"`
	Discount string `json:"discount"`
	NetLabel string `json:"net_label"`
}
type profilePayment struct {
	Position string `json:"position"`
	Heading  string `json:"heading"`
}
type profileAcceptance struct {
	SignatureColumns int    `json:"signature_columns"`
	GapMM            string `json:"gap_mm"`
	LeadMM           string `json:"lead_mm"`
}
type profileFooter struct {
	AssetID          string `json:"asset_id,omitempty"`
	DotsAssetID      string `json:"dots_asset_id,omitempty"`
	WidthMM          string `json:"width_mm"`
	OffsetMM         string `json:"offset_mm"`
	PageNumberFormat string `json:"page_number_format"`
}
type profileDefinition struct {
	Schema         string            `json:"schema"`
	LayoutVariant  string            `json:"layout_variant"`
	Locale         string            `json:"locale"`
	Fonts          []profileFont     `json:"fonts"`
	Colors         map[string]string `json:"colors"`
	Typography     map[string]string `json:"typography"`
	Page           profilePage       `json:"page"`
	Cover          map[string]string `json:"cover"`
	Sections       map[string]string `json:"sections"`
	PositionsTable profileTable      `json:"positions_table"`
	Totals         profileTotals     `json:"totals"`
	PaymentTerms   profilePayment    `json:"payment_terms"`
	Acceptance     profileAcceptance `json:"acceptance"`
	Footer         profileFooter     `json:"footer"`
	Labels         map[string]string `json:"labels"`
}
type documentProfileSnapshot struct {
	ID         string            `json:"id"`
	Revision   int               `json:"revision"`
	Definition profileDefinition `json:"definition"`
}
type profileRow struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Revision   int               `json:"revision"`
	Definition profileDefinition `json:"definition"`
	Archived   bool              `json:"archived"`
}
type profileWrite struct {
	ExpectedRevision int               `json:"expected_revision,omitempty"`
	Name             string            `json:"name"`
	Definition       profileDefinition `json:"definition"`
}
type profileAsset struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

var profileColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
var profileFamily = regexp.MustCompile(`^[\pL\pN][\pL\pN ._-]{0,79}$`)
var profileDecimal = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,2})(?:\.[0-9]{1,2})?$`)

func validProfileDecimal(v string, max int) bool {
	if !profileDecimal.MatchString(v) {
		return false
	}
	return profileHundredths(v) <= max*100
}
func validProfileSignedDecimal(v string, min, max int) bool {
	if strings.HasPrefix(v, "-") {
		value := strings.TrimPrefix(v, "-")
		return profileDecimal.MatchString(value) && -profileHundredths(value) >= min*100
	}
	return validProfileDecimal(v, max)
}
func profileHundredths(v string) int {
	parts := strings.SplitN(v, ".", 2)
	whole, _ := strconv.Atoi(parts[0])
	if len(parts) == 1 {
		return whole * 100
	}
	fraction, _ := strconv.Atoi(parts[1])
	if len(parts[1]) == 1 {
		fraction *= 10
	}
	return whole*100 + fraction
}
func validateProfile(ctx context.Context, tx pgx.Tx, d profileDefinition) error {
	return validateProfileWithAssets(d, func(id string) (string, error) {
		var kind string
		err := tx.QueryRow(ctx, `SELECT content_type FROM quote_document_profile_assets WHERE id=$1::uuid`, id).Scan(&kind)
		return kind, err
	})
}

func validateProfileWithAssets(d profileDefinition, assetType func(string) (string, error)) error {
	if d.Schema != "inspr.document-profile.v1" || (d.LayoutVariant != "standard" && d.LayoutVariant != "classic-v1") || (d.Locale != "de-AT" && d.Locale != "en") {
		return bad("invalid document profile schema, layout or locale")
	}
	if len(d.Fonts) > 12 || len(d.Colors) != 6 || len(d.Typography) == 0 || len(d.Typography) > 20 || len(d.Labels) > 40 {
		return bad("invalid document profile fields")
	}
	for _, key := range []string{"ink", "muted", "soft", "accent", "rule", "paper"} {
		if !profileColor.MatchString(d.Colors[key]) {
			return bad("invalid document profile color")
		}
	}
	for key, value := range d.Typography {
		if !map[string]bool{"body_pt": true, "title_pt": true, "section_pt": true, "table_pt": true, "footer_pt": true}[key] || !validProfileDecimal(value, 100) {
			return bad("invalid document profile typography")
		}
	}
	if d.Page.WidthMM != "210" || d.Page.HeightMM != "297" {
		return bad("document profile requires A4")
	}
	for _, margin := range []string{d.Page.TopMM, d.Page.RightMM, d.Page.BottomMM, d.Page.LeftMM} {
		if !validProfileDecimal(margin, 50) {
			return bad("invalid document profile margins")
		}
	}
	if len(d.Cover) > 20 || len(d.Sections) > 20 {
		return bad("invalid document profile layout")
	}
	for key, value := range d.Cover {
		if key == "brand_asset_id" {
			if err := validateProfileImageAsset(assetType, value, "cover brand"); err != nil {
				return err
			}
			continue
		}
		if !map[string]bool{"top_mm": true, "title_gap_mm": true, "columns_gap_mm": true, "columns_padding_mm": true}[key] || !validProfileDecimal(value, 100) {
			return bad("invalid cover geometry")
		}
	}
	for key, value := range d.Sections {
		if key == "numbering" && !map[string]bool{"decimal": true, "upper-roman": true, "lower-roman": true, "upper-alpha": true, "lower-alpha": true, "none": true}[value] || key == "heading_case" && value != "upper" && value != "as-is" || key != "numbering" && key != "heading_case" {
			return bad("invalid section style")
		}
	}
	if len(d.PositionsTable.Columns) != 6 || (d.PositionsTable.Separator != "rule" && d.PositionsTable.Separator != "none") {
		return bad("invalid positions table")
	}
	keys := []string{"position", "description", "quantity", "unit", "unit_price", "total"}
	columnWidth := 0
	for i, c := range d.PositionsTable.Columns {
		if c.Key != keys[i] || !validProfileDecimal(c.WidthMM, 180) || profileHundredths(c.WidthMM) == 0 {
			return bad("invalid positions column")
		}
		columnWidth += profileHundredths(c.WidthMM)
	}
	if columnWidth > 21000-profileHundredths(d.Page.LeftMM)-profileHundredths(d.Page.RightMM) {
		return bad("positions columns exceed page width")
	}
	if (d.Totals.VAT != "note" && d.Totals.VAT != "line" && d.Totals.VAT != "hidden") || (d.Totals.Discount != "line" && d.Totals.Discount != "hidden") || len(d.Totals.NetLabel) > 100 {
		return bad("invalid totals style")
	}
	if (d.PaymentTerms.Position != "sections" && d.PaymentTerms.Position != "after-totals") || len(d.PaymentTerms.Heading) > 100 {
		return bad("invalid payment terms")
	}
	if d.Acceptance.SignatureColumns < 1 || d.Acceptance.SignatureColumns > 2 || !validProfileDecimal(d.Acceptance.GapMM, 100) || !validProfileDecimal(d.Acceptance.LeadMM, 100) {
		return bad("invalid signature layout")
	}
	if !validProfileDecimal(d.Footer.WidthMM, 180) || !validProfileSignedDecimal(d.Footer.OffsetMM, -6, 10) || len(d.Footer.PageNumberFormat) > 100 || !strings.Contains(d.Footer.PageNumberFormat, "{page}") || !strings.Contains(d.Footer.PageNumberFormat, "{total}") {
		return bad("invalid footer")
	}
	for k, v := range d.Labels {
		if len(k) > 40 || len(v) > 100 || strings.ContainsAny(v, "<>") {
			return bad("invalid profile label")
		}
	}
	for _, f := range d.Fonts {
		if (f.Role != "body" && f.Role != "display") || !profileFamily.MatchString(f.Family) || f.Weight < 100 || f.Weight > 900 || f.Weight%100 != 0 || (f.Style != "normal" && f.Style != "italic") || !uuidRe.MatchString(f.AssetID) {
			return bad("invalid profile font")
		}
		var kind string
		kind, err := assetType(f.AssetID)
		if err != nil {
			return bad("profile font asset missing")
		}
		if kind != "font/ttf" && kind != "font/otf" && kind != "font/woff2" {
			return bad("profile font asset has wrong type")
		}
	}
	for _, asset := range []struct{ id, label string }{{d.Footer.AssetID, "footer"}, {d.Footer.DotsAssetID, "footer dots"}} {
		if asset.id != "" {
			if err := validateProfileImageAsset(assetType, asset.id, asset.label); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProfileImageAsset(assetType func(string) (string, error), id, label string) error {
	if !uuidRe.MatchString(id) {
		return bad("invalid " + label + " asset")
	}
	var kind string
	kind, err := assetType(id)
	if err != nil {
		return bad(label + " asset missing")
	}
	if kind != "image/png" && kind != "image/svg+xml" {
		return bad(label + " asset has wrong type")
	}
	return nil
}

func readProfile(ctx context.Context, tx pgx.Tx, id string, revision int) (profileRow, error) {
	var out profileRow
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT p.id::text,r.name,r.revision,r.definition,p.archived_at IS NOT NULL FROM quote_document_profiles p JOIN quote_document_profile_revisions r ON r.tenant_id=p.tenant_id AND r.profile_id=p.id AND r.revision=CASE WHEN $2=0 THEN p.current_revision ELSE $2 END WHERE p.id=$1::uuid`, id, revision).Scan(&out.ID, &out.Name, &out.Revision, &raw, &out.Archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, missing()
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out.Definition); err != nil {
		return out, err
	}
	out.Definition = out.Definition.normalized()
	return out, nil
}
func (m *Module) profileList(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	out := []profileRow{}
	e = m.tx(r.Context(), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT p.id::text,r.name,r.revision,r.definition,p.archived_at IS NOT NULL FROM quote_document_profiles p JOIN quote_document_profile_revisions r ON r.tenant_id=p.tenant_id AND r.profile_id=p.id AND r.revision=p.current_revision ORDER BY r.name,p.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item profileRow
			var raw []byte
			if err = rows.Scan(&item.ID, &item.Name, &item.Revision, &raw, &item.Archived); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &item.Definition); err != nil {
				return err
			}
			item.Definition = item.Definition.normalized()
			out = append(out, item)
		}
		return rows.Err()
	})
	respond(w, 200, out, e)
}
func (m *Module) profileGet(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	id := r.PathValue("profileId")
	if !uuidRe.MatchString(id) {
		respond(w, 0, nil, bad("invalid profile id"))
		return
	}
	rev := 0
	if v := r.URL.Query().Get("revision"); v != "" {
		rev, e = strconv.Atoi(v)
		if e != nil || rev < 1 {
			respond(w, 0, nil, bad("invalid profile revision"))
			return
		}
	}
	var out profileRow
	e = m.tx(r.Context(), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error { var err error; out, err = readProfile(r.Context(), tx, id, rev); return err })
	respond(w, 200, out, e)
}
func (m *Module) profileWrite(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	var in profileWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 100 {
		respond(w, 0, nil, bad("invalid profile name"))
		return
	}
	id := r.PathValue("profileId")
	creating := r.Method == http.MethodPost
	if !creating && !uuidRe.MatchString(id) {
		respond(w, 0, nil, bad("invalid profile id"))
		return
	}
	if creating && in.ExpectedRevision != 0 || !creating && in.ExpectedRevision < 1 {
		respond(w, 0, nil, bad("invalid expected revision"))
		return
	}
	var out profileRow
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		in.Definition = in.Definition.normalized()
		if err := validateProfile(r.Context(), tx, in.Definition); err != nil {
			return err
		}
		raw, err := marshalProfile(in.Definition)
		if err != nil {
			return err
		}
		revision := 1
		if creating {
			err = tx.QueryRow(r.Context(), `INSERT INTO quote_document_profiles(tenant_id,name,current_revision) VALUES($1::uuid,$2,1) RETURNING id::text`, p.TenantID, in.Name).Scan(&id)
		} else {
			var current int
			var archived bool
			err = tx.QueryRow(r.Context(), `SELECT current_revision,archived_at IS NOT NULL FROM quote_document_profiles WHERE id=$1::uuid FOR UPDATE`, id).Scan(&current, &archived)
			if errors.Is(err, pgx.ErrNoRows) {
				return missing()
			}
			if err != nil {
				return err
			}
			if archived || current != in.ExpectedRevision {
				return conflict("profile revision is stale or archived")
			}
			revision = current + 1
			_, err = tx.Exec(r.Context(), `UPDATE quote_document_profiles SET name=$2,current_revision=$3,updated_at=clock_timestamp() WHERE id=$1::uuid`, id, in.Name, revision)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO quote_document_profile_revisions(tenant_id,profile_id,revision,name,definition,created_by_principal_id) VALUES($1::uuid,$2::uuid,$3,$4,$5::jsonb,$6::uuid)`, p.TenantID, id, revision, in.Name, string(raw), p.ID)
		if err != nil {
			return err
		}
		out, err = readProfile(r.Context(), tx, id, revision)
		if err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, "", "quote.profile_saved", map[string]any{"profile_id": id, "revision": in.ExpectedRevision}, map[string]any{"profile_id": id, "revision": revision})
	})
	status := 200
	if creating {
		status = 201
	}
	respond(w, status, out, e)
}
func (m *Module) profileArchive(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	id := r.PathValue("profileId")
	if !uuidRe.MatchString(id) {
		respond(w, 0, nil, bad("invalid profile id"))
		return
	}
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		var revision int
		err := tx.QueryRow(r.Context(), `UPDATE quote_document_profiles SET archived_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid AND archived_at IS NULL RETURNING current_revision`, id).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		var settingsRevision int64
		err = tx.QueryRow(r.Context(), `UPDATE quote_settings SET default_profile_id=NULL,revision=revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$2::uuid WHERE default_profile_id=$1::uuid RETURNING revision`, id, p.ID).Scan(&settingsRevision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if err = appendEvent(r.Context(), tx, p, "", "quote.settings_updated", map[string]any{"default_profile_id": id, "revision": settingsRevision - 1}, map[string]any{"default_profile_id": "", "revision": settingsRevision}); err != nil {
				return err
			}
		}
		return appendEvent(r.Context(), tx, p, "", "quote.profile_archived", map[string]any{"profile_id": id, "revision": revision, "archived": false}, map[string]any{"profile_id": id, "revision": revision, "archived": true})
	})
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	w.WriteHeader(204)
}
func (m *Module) profileUndo(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	id := r.PathValue("profileId")
	if !uuidRe.MatchString(id) {
		respond(w, 0, nil, bad("invalid profile id"))
		return
	}
	var in struct {
		ExpectedRevision int `json:"expected_revision"`
	}
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	var out profileRow
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		var current int
		var archived bool
		err := tx.QueryRow(r.Context(), `SELECT current_revision,archived_at IS NOT NULL FROM quote_document_profiles WHERE id=$1::uuid FOR UPDATE`, id).Scan(&current, &archived)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		if current != in.ExpectedRevision {
			return conflict("profile revision is stale")
		}
		if archived {
			_, err = tx.Exec(r.Context(), `UPDATE quote_document_profiles SET archived_at=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
			if err != nil {
				return err
			}
			out, err = readProfile(r.Context(), tx, id, current)
			if err != nil {
				return err
			}
			return appendEvent(r.Context(), tx, p, "", "quote.profile_undone", map[string]any{"profile_id": id, "revision": current, "archived": true}, map[string]any{"profile_id": id, "revision": current, "archived": false})
		}
		if current == 1 {
			_, err = tx.Exec(r.Context(), `UPDATE quote_document_profiles SET archived_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
			if err != nil {
				return err
			}
			var settingsRevision int64
			err = tx.QueryRow(r.Context(), `UPDATE quote_settings SET default_profile_id=NULL,revision=revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$2::uuid WHERE default_profile_id=$1::uuid RETURNING revision`, id, p.ID).Scan(&settingsRevision)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				if err = appendEvent(r.Context(), tx, p, "", "quote.settings_updated", map[string]any{"default_profile_id": id, "revision": settingsRevision - 1}, map[string]any{"default_profile_id": "", "revision": settingsRevision}); err != nil {
					return err
				}
			}
			out, err = readProfile(r.Context(), tx, id, current)
			if err != nil {
				return err
			}
			return appendEvent(r.Context(), tx, p, "", "quote.profile_undone", map[string]any{"profile_id": id, "revision": current, "archived": false}, map[string]any{"profile_id": id, "revision": current, "archived": true})
		}
		previous, err := readProfile(r.Context(), tx, id, current-1)
		if err != nil {
			return err
		}
		raw, err := marshalProfile(previous.Definition)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE quote_document_profiles SET name=$2,current_revision=$3,archived_at=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid`, id, previous.Name, current+1)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO quote_document_profile_revisions(tenant_id,profile_id,revision,name,definition,created_by_principal_id) VALUES($1::uuid,$2::uuid,$3,$4,$5::jsonb,$6::uuid)`, p.TenantID, id, current+1, previous.Name, string(raw), p.ID)
		if err != nil {
			return err
		}
		out, err = readProfile(r.Context(), tx, id, current+1)
		if err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, "", "quote.profile_undone", map[string]any{"profile_id": id, "revision": current}, map[string]any{"profile_id": id, "revision": current + 1, "restored_from": current - 1})
	})
	respond(w, 200, out, e)
}

func readProfileSnapshot(ctx context.Context, tx pgx.Tx, id string) (*documentProfileSnapshot, error) {
	if id == "" {
		return nil, nil
	}
	// Hold the selected revision steady against concurrent saves and archives
	// until the containing quote or settings transaction commits.
	var lockedRevision int
	if err := tx.QueryRow(ctx, `SELECT current_revision FROM quote_document_profiles WHERE id=$1::uuid FOR SHARE`, id).Scan(&lockedRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, missing()
		}
		return nil, err
	}
	row, err := readProfile(ctx, tx, id, lockedRevision)
	if err != nil {
		return nil, err
	}
	if row.Archived {
		return nil, conflict("profile is archived")
	}
	return &documentProfileSnapshot{ID: row.ID, Revision: row.Revision, Definition: row.Definition}, nil
}
func profileAssetType(raw []byte) string {
	switch {
	case len(raw) >= 48 && string(raw[:4]) == "wOF2" && binary.BigEndian.Uint32(raw[8:12]) == uint32(len(raw)) && binary.BigEndian.Uint16(raw[12:14]) > 0:
		return "font/woff2"
	case sfntHeader(raw, "OTTO"):
		return "font/otf"
	case sfntHeader(raw, "\x00\x01\x00\x00"):
		return "font/ttf"
	case len(raw) > 8 && bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(raw) > 4 && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("<svg")) && safeProfileSVG(raw):
		return "image/svg+xml"
	default:
		return ""
	}
}
func sfntHeader(raw []byte, signature string) bool {
	if len(raw) < 12 || string(raw[:4]) != signature {
		return false
	}
	count := int(binary.BigEndian.Uint16(raw[4:6]))
	return count > 0 && count <= 256 && len(raw) >= 12+16*count
}
func safeProfileSVG(raw []byte) bool {
	if len(raw) > 1<<20 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("<svg")) {
		return false
	}
	tags := map[string]bool{"svg": true, "g": true, "path": true, "circle": true, "rect": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "text": true, "tspan": true}
	attrs := map[string]bool{"viewBox": true, "width": true, "height": true, "fill": true, "stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true, "d": true, "cx": true, "cy": true, "r": true, "x": true, "y": true, "x1": true, "x2": true, "y1": true, "y2": true, "rx": true, "ry": true, "points": true, "transform": true, "opacity": true, "fill-opacity": true, "stroke-opacity": true, "font-family": true, "font-size": true, "font-weight": true, "letter-spacing": true}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	depth := 0
	root := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return root && depth == 0
		}
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.StartElement:
			if !tags[value.Name.Local] || (value.Name.Space != "" && value.Name.Space != "http://www.w3.org/2000/svg") || depth > 32 {
				return false
			}
			if !root {
				if value.Name.Local != "svg" {
					return false
				}
				root = true
			}
			depth++
			for _, attr := range value.Attr {
				if attr.Name.Local == "xmlns" && attr.Value == "http://www.w3.org/2000/svg" {
					continue
				}
				if attr.Name.Space != "" || !attrs[attr.Name.Local] || len(attr.Value) > 10000 {
					return false
				}
				lower := strings.ToLower(attr.Value)
				if strings.ContainsAny(lower, ";<>\"'") || strings.Contains(lower, "url(") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") {
					return false
				}
			}
		case xml.EndElement:
			depth--
			if depth < 0 {
				return false
			}
		case xml.CharData:
		case xml.Comment:
			return false
		default:
			return false
		}
	}
}

// Profile fonts and safe SVGs have their own strict validator above. The
// general attachment upload deliberately does not accept those media types.
// Keep the same tenant/hash file layout so Store.Open can serve the asset.
func putProfileAsset(ctx context.Context, tenantID string, raw []byte, kind, filesDir string) (attachments.Prepared, error) {
	if kind == "image/png" {
		return (attachments.Store{FilesDir: filesDir}).Put(ctx, tenantID, bytes.NewReader(raw))
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	root := filesDir
	if root == "" {
		root = os.Getenv("AEON_FILES_DIR")
	}
	if root == "" {
		root = "./data/files"
	}
	directory := filepath.Join(root, tenantID, digest[:2], digest[2:4])
	if err := os.MkdirAll(directory, 0700); err != nil {
		return attachments.Prepared{}, err
	}
	if err := ctx.Err(); err != nil {
		return attachments.Prepared{}, err
	}
	tmp, err := os.CreateTemp(directory, "profile-*")
	if err != nil {
		return attachments.Prepared{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return attachments.Prepared{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return attachments.Prepared{}, err
	}
	if err := tmp.Close(); err != nil {
		return attachments.Prepared{}, err
	}
	if err := ctx.Err(); err != nil {
		return attachments.Prepared{}, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(directory, digest)); err != nil {
		return attachments.Prepared{}, err
	}
	return attachments.Prepared{SHA256: digest, ContentType: kind, Size: int64(len(raw))}, nil
}

func (m *Module) profileAssetUpload(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if e != nil {
		respond(w, 0, nil, bad("asset exceeds 10 MiB"))
		return
	}
	kind := profileAssetType(raw)
	if kind == "" {
		respond(w, 0, nil, bad("expected TTF, OTF, WOFF2, PNG or safe SVG"))
		return
	}
	prepared, e := putProfileAsset(r.Context(), p.TenantID, raw, kind, "")
	if e != nil {
		respond(w, 0, nil, bad("asset storage rejected file"))
		return
	}
	var out profileAsset
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		out.SHA256 = prepared.SHA256
		out.ContentType = kind
		out.Size = prepared.Size
		if err := tx.QueryRow(r.Context(), `INSERT INTO quote_document_profile_assets(tenant_id,sha256,content_type,size,created_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5::uuid) RETURNING id::text`, p.TenantID, out.SHA256, kind, out.Size, p.ID).Scan(&out.ID); err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, "", "quote.profile_asset_uploaded", nil, map[string]any{"asset_id": out.ID, "sha256": out.SHA256, "content_type": kind})
	})
	respond(w, 201, out, e)
}
func (m *Module) profileAssetGet(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	id := r.PathValue("assetId")
	if !uuidRe.MatchString(id) {
		respond(w, 0, nil, bad("invalid asset id"))
		return
	}
	var asset profileAsset
	e = m.tx(r.Context(), p, fence.PermViewsProvide, false, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `SELECT id::text,sha256,content_type,size FROM quote_document_profile_assets WHERE id=$1::uuid`, id).Scan(&asset.ID, &asset.SHA256, &asset.ContentType, &asset.Size)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		return err
	})
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	f, e := (attachments.Store{}).Open(p.TenantID, asset.SHA256, "original")
	if errors.Is(e, fs.ErrNotExist) {
		// The asset is registered but its bytes are not in this store (a restored
		// copy, or a store moved without its files): not found, not a server fault.
		respond(w, 0, nil, failure{status: 404, message: "asset content is not stored"})
		return
	}
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if asset.ContentType == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, asset.SHA256))
	if r.Header.Get("If-None-Match") == fmt.Sprintf(`"%s"`, asset.SHA256) {
		w.WriteHeader(304)
		return
	}
	_, _ = io.Copy(w, f)
}

func (m *Module) selectProfile(w http.ResponseWriter, r *http.Request) {
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
		ExpectedDraftRevision int64  `json:"expected_draft_revision"`
		ProfileID             string `json:"profile_id"`
	}
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	// An empty profile_id returns the draft to the standard document (U19).
	if in.ExpectedDraftRevision < 1 || (in.ProfileID != "" && !uuidRe.MatchString(in.ProfileID)) {
		respond(w, 0, nil, bad("invalid profile selection"))
		return
	}
	var out draftRow
	e = m.tx(r.Context(), p, fence.PermNodesContribute, true, func(tx pgx.Tx) error {
		// Bundle refresh takes profile, quote, then draft. Take the snapshot
		// first, including when this selection will later fail its revision.
		profile, err := readProfileSnapshot(r.Context(), tx, in.ProfileID)
		if err != nil {
			return err
		}
		q, err := readQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if q.State != "draft" || q.Archived {
			return conflict("quote is not editable")
		}
		current, err := readDraft(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if current.DraftRevision != in.ExpectedDraftRevision {
			return conflict("draft revision is stale")
		}
		doc, err := decodeDocument(current.Document)
		if err != nil {
			return err
		}
		before := doc.Profile
		doc.Profile = profile
		raw, err := marshalDraft(doc)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE quote_drafts SET document=$1::jsonb,draft_revision=draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$2::uuid WHERE quote_node_id=$3::uuid`, string(raw), p.ID, id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE business_quotes SET revision=revision+1 WHERE quote_node_id=$1::uuid`, id)
		if err != nil {
			return err
		}
		out, err = readDraft(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		after := map[string]any{"profile_id": "", "profile_revision": 0, "draft_revision": out.DraftRevision}
		if profile != nil {
			after["profile_id"], after["profile_revision"] = profile.ID, profile.Revision
		}
		return appendEvent(r.Context(), tx, p, id, "quote.profile_selected", map[string]any{"profile": before, "draft_revision": current.DraftRevision}, after)
	})
	respond(w, 200, out, e)
}
