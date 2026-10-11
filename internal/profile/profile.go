// SPDX-License-Identifier: AGPL-3.0-only
// Package profile provides tenant-scoped personal profiles and avatar endpoints.
// The coordinator mounts New(pool, store) as an httpapi.Module and registers
// UndoHandlers() with events.WithUndoHandlers. ProfileImporter is the CLI entry
// point the coordinator wires to "paimos import paimos-profiles".
package profile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/language"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

var shortNameRE = regexp.MustCompile(`^[a-z0-9._-]{2,24}$`)
var palette = [...]string{"slate", "sage", "moss", "ocean", "steel", "denim", "iris", "plum", "rose", "clay", "sand", "teal"}

// Profile is the public response. Email comes from the bound identity and is never writable.
type Profile struct {
	PrincipalID     string            `json:"principal_id"`
	Email           *string           `json:"email"`
	FirstName       string            `json:"first_name"`
	LastName        string            `json:"last_name"`
	PreferredName   string            `json:"preferred_name"`
	ShortName       string            `json:"short_name"`
	Initials        string            `json:"initials"`
	Timezone        string            `json:"timezone"`
	Locale          string            `json:"locale"`
	GreetingEnabled bool              `json:"greeting_enabled"`
	AvatarColor     string            `json:"avatar_color"`
	AvatarHashes    map[string]string `json:"avatar_hashes"`
	WeekStart       int               `json:"week_start"`
	// DailyPointsPerDay is the default pace (AEON-1175): percentage points a day
	// agents may use of an account's window; null follows the built-in 10.
	DailyPointsPerDay *int  `json:"daily_points_per_day"`
	Revision          int64 `json:"revision"`
}

// State is the exact reversible profile projection, excluding identity email.
type State struct {
	PrincipalID        string            `json:"principal_id"`
	FirstName          string            `json:"first_name"`
	LastName           string            `json:"last_name"`
	PreferredName      string            `json:"preferred_name"`
	ShortName          string            `json:"short_name"`
	InitialsOverride   string            `json:"initials_override"`
	Timezone           string            `json:"timezone"`
	Locale             string            `json:"locale"`
	GreetingEnabled    bool              `json:"greeting_enabled"`
	AvatarOriginalHash string            `json:"avatar_original_hash"`
	AvatarHashes       map[string]string `json:"avatar_hashes"`
	DailyPointsPerDay  *int              `json:"daily_points_per_day"`
	Revision           int64             `json:"revision"`
}

func defaultState(id string) State {
	return State{PrincipalID: id, Timezone: "Europe/Vienna", Locale: "de-AT", GreetingEnabled: true, AvatarHashes: map[string]string{}}
}
func (s State) public(email *string) Profile {
	initials := s.InitialsOverride
	if initials == "" {
		initials = derivedInitials(s)
	}
	h := sha256.Sum256([]byte(s.PrincipalID))
	week := 1
	tag, err := language.Parse(s.Locale)
	if err == nil {
		region, _ := tag.Region()
		if region.String() == "US" || region.String() == "CA" || region.String() == "JP" {
			week = 0
		}
	}
	return Profile{PrincipalID: s.PrincipalID, Email: email, FirstName: s.FirstName, LastName: s.LastName, PreferredName: s.PreferredName, ShortName: s.ShortName, Initials: initials, Timezone: s.Timezone, Locale: s.Locale, GreetingEnabled: s.GreetingEnabled, AvatarColor: palette[int(h[0])%len(palette)], AvatarHashes: s.AvatarHashes, WeekStart: week, DailyPointsPerDay: s.DailyPointsPerDay, Revision: s.Revision}
}
func derivedInitials(s State) string {
	first, last := s.FirstName, s.LastName
	if first == "" {
		first = s.PreferredName
	}
	var out []rune
	for _, v := range []string{first, last} {
		for _, r := range v {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
	}
	if len(out) == 0 {
		for _, r := range s.ShortName {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}
func validUUID(s string) bool { var v pgtype.UUID; return len(s) == 36 && v.Scan(s) == nil && v.Valid }
func validateField(key string, value json.RawMessage) string {
	var v string
	if key == "daily_points_per_day" {
		// The plan reads this as the person's default pace; null follows the built-in default.
		var n int
		if string(value) != "null" && (json.Unmarshal(value, &n) != nil || n < 1 || n > 50) {
			return "must be a whole number from 1 to 50, or null"
		}
		return ""
	}
	if string(value) == "null" {
		return "must not be null"
	}
	switch key {
	case "first_name", "last_name", "preferred_name":
		if json.Unmarshal(value, &v) != nil || !utf8.ValidString(v) || utf8.RuneCountInString(v) > 100 || strings.TrimSpace(v) != v {
			return "must be a trimmed string of at most 100 characters"
		}
	case "short_name":
		if json.Unmarshal(value, &v) != nil || (v != "" && !shortNameRE.MatchString(v)) {
			return "must be lowercase [a-z0-9._-], 2–24 characters"
		}
	case "initials":
		if json.Unmarshal(value, &v) != nil || utf8.RuneCountInString(v) > 3 || strings.TrimSpace(v) != v {
			return "must be at most 3 characters"
		}
	case "timezone":
		if json.Unmarshal(value, &v) != nil || v == "" || v == "Local" || len(v) > 128 {
			return "must be an IANA time zone"
		}
		if _, err := time.LoadLocation(v); err != nil {
			return "must be an IANA time zone"
		}
	case "locale":
		if json.Unmarshal(value, &v) != nil || v == "" {
			return "must be a BCP 47 language tag"
		}
		tag, err := language.Parse(v)
		if err != nil || tag.String() == "und" {
			return "must be a canonical BCP 47 language tag"
		}
	case "greeting_enabled":
		var b bool
		if json.Unmarshal(value, &b) != nil || string(value) == "null" {
			return "must be a boolean"
		}
	default:
		return "unknown field"
	}
	return ""
}
func ValidatePatch(raw []byte) (map[string]json.RawMessage, map[string]string) {
	fields := map[string]json.RawMessage{}
	errs := map[string]string{}
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil {
		errs["body"] = "must be a JSON object"
		return nil, errs
	}
	if len(fields) == 0 {
		errs["body"] = "at least one field is required"
	}
	for k, v := range fields {
		if msg := validateField(k, v); msg != "" {
			errs[k] = msg
		}
	}
	return fields, errs
}
func applyPatch(s *State, fields map[string]json.RawMessage) {
	for k, v := range fields {
		switch k {
		case "first_name":
			_ = json.Unmarshal(v, &s.FirstName)
		case "last_name":
			_ = json.Unmarshal(v, &s.LastName)
		case "preferred_name":
			_ = json.Unmarshal(v, &s.PreferredName)
		case "short_name":
			_ = json.Unmarshal(v, &s.ShortName)
		case "initials":
			_ = json.Unmarshal(v, &s.InitialsOverride)
		case "timezone":
			_ = json.Unmarshal(v, &s.Timezone)
		case "locale":
			_ = json.Unmarshal(v, &s.Locale)
			tag, _ := language.Parse(s.Locale)
			s.Locale = tag.String()
		case "greeting_enabled":
			_ = json.Unmarshal(v, &s.GreetingEnabled)
		case "daily_points_per_day":
			s.DailyPointsPerDay = nil
			_ = json.Unmarshal(v, &s.DailyPointsPerDay)
		}
	}
}

const columns = `principal_id::text,first_name,last_name,preferred_name,short_name,initials_override,timezone,locale,greeting_enabled,avatar_original_hash,avatar_hashes,daily_points_per_day,revision`

func scan(row pgx.Row) (State, error) {
	var s State
	var raw []byte
	err := row.Scan(&s.PrincipalID, &s.FirstName, &s.LastName, &s.PreferredName, &s.ShortName, &s.InitialsOverride, &s.Timezone, &s.Locale, &s.GreetingEnabled, &s.AvatarOriginalHash, &raw, &s.DailyPointsPerDay, &s.Revision)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s.AvatarHashes)
	return s, err
}
func read(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (State, bool, error) {
	if lock {
		// Lock the key even before a profile row exists, so two first writes
		// cannot race through INSERT ... ON CONFLICT and lose a patch.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 580))`, tenantID+":"+id); err != nil {
			return State{}, false, err
		}
	}
	q := `SELECT ` + columns + ` FROM personal_profiles WHERE tenant_id=$1 AND principal_id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	s, err := scan(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultState(id), false, nil
	}
	return s, true, err
}
func save(ctx context.Context, tx pgx.Tx, tenantID string, s State) error {
	hashes, err := json.Marshal(s.AvatarHashes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,first_name,last_name,preferred_name,short_name,initials_override,timezone,locale,greeting_enabled,avatar_original_hash,avatar_hashes,daily_points_per_day,revision)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT(tenant_id,principal_id) DO UPDATE SET first_name=EXCLUDED.first_name,last_name=EXCLUDED.last_name,preferred_name=EXCLUDED.preferred_name,short_name=EXCLUDED.short_name,initials_override=EXCLUDED.initials_override,timezone=EXCLUDED.timezone,locale=EXCLUDED.locale,greeting_enabled=EXCLUDED.greeting_enabled,avatar_original_hash=EXCLUDED.avatar_original_hash,avatar_hashes=EXCLUDED.avatar_hashes,daily_points_per_day=EXCLUDED.daily_points_per_day,revision=EXCLUDED.revision`, tenantID, s.PrincipalID, s.FirstName, s.LastName, s.PreferredName, s.ShortName, s.InitialsOverride, s.Timezone, s.Locale, s.GreetingEnabled, s.AvatarOriginalHash, hashes, s.DailyPointsPerDay, s.Revision)
	return err
}
func identityEmail(ctx context.Context, tx pgx.Tx, tenantID, id string) (*string, error) {
	var email *string
	err := tx.QueryRow(ctx, `SELECT i.email FROM principals p LEFT JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1 AND p.id=$2 AND p.kind='person'`, tenantID, id).Scan(&email)
	return email, err
}
func same(a, b State) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func change(ctx context.Context, tx pgx.Tx, p tenant.Principal, before State, existed bool, after State) error {
	if same(before, after) {
		return nil
	}
	after.Revision = before.Revision + 1
	if err := save(ctx, tx, p.TenantID, after); err != nil {
		return err
	}
	var prior any
	if existed {
		prior = before
	}
	_, err := events.Append(ctx, tx, p, events.Change{Type: "profile.updated", Before: prior, After: after})
	return err
}

type Module struct {
	Pool  *pgxpool.Pool
	Store attachments.Store
}

var _ httpapi.Module = (*Module)(nil)

// New returns the module mounted by the coordinator in cmd/aeon.
func New(pool *pgxpool.Pool, store attachments.Store) *Module {
	return &Module{Pool: pool, Store: store}
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/profile", m.get)
	mux.HandleFunc("PATCH /api/me/profile", m.patch)
	mux.HandleFunc("POST /api/me/avatar", m.uploadAvatar)
	mux.HandleFunc("DELETE /api/me/avatar", m.deleteAvatar)
	mux.HandleFunc("GET /api/people/{principalId}/avatar/{size}", m.avatar)
}
func actor(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || !validUUID(p.ID) || !validUUID(p.TenantID) {
		httpapi.WriteError(w, 401, "person authentication required")
		return p, false
	}
	return p, true
}
func viewer(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !validUUID(p.ID) || !validUUID(p.TenantID) {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	return p, true
}
func apiError(w http.ResponseWriter, err error) {
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "not found")
	case errors.As(err, &pg) && pg.ConstraintName == "personal_profiles_short_name":
		httpapi.WriteJSON(w, 409, map[string]any{"errors": map[string]string{"short_name": "already used in this tenant"}})
	default:
		httpapi.WriteError(w, 500, "profile operation failed")
	}
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	var out Profile
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		email, err := identityEmail(r.Context(), tx, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		s, _, err := read(r.Context(), tx, p.TenantID, p.ID, false)
		out = s.public(email)
		return err
	})
	if err != nil {
		apiError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var raw json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil {
		httpapi.WriteJSON(w, 400, map[string]any{"errors": map[string]string{"body": "invalid JSON"}})
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		httpapi.WriteJSON(w, 400, map[string]any{"errors": map[string]string{"body": "invalid JSON"}})
		return
	}
	fields, errs := ValidatePatch(raw)
	if len(errs) > 0 {
		httpapi.WriteJSON(w, 400, map[string]any{"errors": errs})
		return
	}
	var out Profile
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		email, err := identityEmail(r.Context(), tx, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		before, exists, err := read(r.Context(), tx, p.TenantID, p.ID, true)
		if err != nil {
			return err
		}
		after := before
		applyPatch(&after, fields)
		if err := change(r.Context(), tx, p, before, exists, after); err != nil {
			return err
		}
		if !same(before, after) {
			after.Revision++
		}
		out = after.public(email)
		return nil
	})
	if err != nil {
		apiError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func ensurePerson(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&kind)
	if err != nil {
		return err
	}
	if kind != "person" {
		return pgx.ErrNoRows
	}
	return nil
}

type invalidInput string

func (e invalidInput) Error() string { return string(e) }
func badInput(msg string) error      { return invalidInput(msg) }
