// SPDX-License-Identifier: AGPL-3.0-only
// Package themes stores tenant themes and each person's active choice. UI and
// colour derivation belong to the appearance consumers, not the data contract.
package themes

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrInvalid  = errors.New("invalid theme input")
	ErrConflict = errors.New("theme revision changed or default cannot be deleted")
	colourRE    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

type Accent struct {
	Light string  `json:"light"`
	Dark  *string `json:"dark"`
}
type Marker struct {
	Source string  `json:"source"`
	Custom *string `json:"custom"`
}
type Agents struct {
	Avatar  string  `json:"avatar"`
	Ring    *string `json:"ring"`
	Hover   bool    `json:"hover"`
	Size    *int    `json:"size"`
	Palette string  `json:"palette"`
}
type Values struct {
	Primary         Accent `json:"primary"`
	Secondary       Accent `json:"secondary"`
	RecurringMarker Marker `json:"recurring_marker"`
	Agents          Agents `json:"agents"`
}

// Porcelain preserves the shipped light/dark accents and the default artwork.
// Nil ring/size retain each avatar's native geometry when changing its style.
func Porcelain() Values {
	primary, secondary := "#a4e5df", "#e2b45a"
	return Values{Primary: Accent{"#0e6f6c", &primary}, Secondary: Accent{"#d69b31", &secondary},
		RecurringMarker: Marker{Source: "secondary"}, Agents: Agents{Avatar: "robot-1", Palette: "standard"}}
}

type Theme struct {
	ID               string     `json:"id"`
	TenantID         string     `json:"tenant_id"`
	Name             string     `json:"name"`
	Scope            string     `json:"scope"`
	OwnerPrincipalID *string    `json:"owner_principal_id"`
	Values           Values     `json:"values"`
	Revision         int64      `json:"revision"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
}
type Selection struct {
	PrincipalID string  `json:"principal_id"`
	ThemeID     *string `json:"theme_id"`
	Revision    int64   `json:"revision"`
	// Unsaved distinguishes a fresh-generation absence from an explicit default.
	// Omitted false preserves the shape of historical saved-choice snapshots.
	Unsaved bool `json:"unsaved,omitempty"`
	// generation is the public CAS value. Keep physical revisions in audit
	// snapshots so historical undo remains valid after the additive migration.
	generation int64
}
type FallbackNotice struct {
	DeletedThemeID   string `json:"deleted_theme_id"`
	DeletedThemeName string `json:"deleted_theme_name"`
}
type Active struct {
	Theme           Theme           `json:"theme"`
	DefaultThemeID  string          `json:"default_theme_id"`
	SelectedThemeID *string         `json:"selected_theme_id"`
	Revision        int64           `json:"revision"`
	FallbackNotice  *FallbackNotice `json:"fallback_notice"`
}
type Page struct {
	Items      []Theme `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type CreateInput struct {
	Name   string  `json:"name"`
	Scope  string  `json:"scope"`
	Values *Values `json:"values,omitempty"`
}
type UpdateInput struct {
	Revision int64   `json:"revision"`
	Name     *string `json:"name,omitempty"`
	Values   *Values `json:"values,omitempty"`
}
type DuplicateInput struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Revision int64  `json:"revision"`
}
type SelectionInput struct {
	ThemeID  *string `json:"theme_id"`
	Revision int64   `json:"revision"`
}

func validUUID(s string) bool {
	var id pgtype.UUID
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' && id.Scan(s) == nil && id.Valid
}
func validName(s string) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) != s || utf8.RuneCountInString(s) < 1 || utf8.RuneCountInString(s) > 80 {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func validScope(s string) bool { return s == "personal" || s == "workspace" }
func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func (v Values) validate() error {
	for _, a := range []Accent{v.Primary, v.Secondary} {
		if !colourRE.MatchString(a.Light) || (a.Dark != nil && !colourRE.MatchString(*a.Dark)) {
			return ErrInvalid
		}
	}
	m := v.RecurringMarker
	if !oneOf(m.Source, "primary", "secondary", "neutral", "custom") ||
		(m.Custom != nil && !colourRE.MatchString(*m.Custom)) || (m.Source == "custom" && m.Custom == nil) {
		return ErrInvalid
	}
	a := v.Agents
	if !oneOf(a.Avatar, "pulse", "robot-1", "robot-2", "robot-3", "robot-4", "robot-5", "orbit", "quill", "sprite") ||
		(a.Ring != nil && !oneOf(*a.Ring, "moving", "still", "off")) ||
		(a.Size != nil && (*a.Size < 30 || *a.Size > 100)) ||
		!oneOf(a.Palette, "standard", "protan", "deutan", "tritan", "monochrome") {
		return ErrInvalid
	}
	return nil
}
func same(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
