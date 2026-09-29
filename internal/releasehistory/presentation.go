// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Presentation is how a release introduces itself (AEON-305): a short theme
// shown as the kicker, one headline sentence and a short intro, each in English
// and German. German fields may be empty; readers fall back to English.
type Presentation struct {
	ThemeEN    string    `json:"theme_en"`
	ThemeDE    string    `json:"theme_de"`
	HeadlineEN string    `json:"headline_en"`
	HeadlineDE string    `json:"headline_de"`
	IntroEN    string    `json:"intro_en"`
	IntroDE    string    `json:"intro_de"`
	Revision   int       `json:"revision"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PresentationInput is a whole presentation as written by the API or the CLI.
// ExpectedRevision, when set, must match the stored revision (0 = none yet).
type PresentationInput struct {
	ThemeEN          string `json:"theme_en"`
	ThemeDE          string `json:"theme_de"`
	HeadlineEN       string `json:"headline_en"`
	HeadlineDE       string `json:"headline_de"`
	IntroEN          string `json:"intro_en"`
	IntroDE          string `json:"intro_de"`
	ExpectedRevision *int   `json:"expected_revision,omitempty"`
}

// Presentation limits, in characters. The migration enforces the same bounds.
const (
	MaxTheme    = 80
	MaxHeadline = 200
	MaxIntro    = 600
)

// Event types written for every presentation change.
const (
	PresentationSetEvent     = "release.presentation_set"
	PresentationClearedEvent = "release.presentation_cleared"
)

var (
	// ErrPresentationInvalid wraps every validation failure; the message says which field.
	ErrPresentationInvalid = errors.New("invalid release presentation")
	// ErrPresentationConflict means expected_revision no longer matches.
	ErrPresentationConflict = errors.New("release presentation changed since it was read")
	// ErrPresentationDenied means the actor may not present releases of this project.
	ErrPresentationDenied = errors.New("presenting releases requires releases.deploy on the project")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPresentationInvalid, fmt.Sprintf(format, args...))
}

// Normalize trims every field and checks it: English theme and headline are
// required, theme and headline are one line, and all fields stay within limits.
func (in PresentationInput) Normalize() (PresentationInput, error) {
	fields := []struct {
		name     string
		value    *string
		max      int
		required bool
		oneLine  bool
	}{
		{"theme_en", &in.ThemeEN, MaxTheme, true, true},
		{"theme_de", &in.ThemeDE, MaxTheme, false, true},
		{"headline_en", &in.HeadlineEN, MaxHeadline, true, true},
		{"headline_de", &in.HeadlineDE, MaxHeadline, false, true},
		{"intro_en", &in.IntroEN, MaxIntro, false, false},
		{"intro_de", &in.IntroDE, MaxIntro, false, false},
	}
	for _, f := range fields {
		v := strings.TrimSpace(*f.value)
		if !utf8.ValidString(v) {
			return in, invalid("%s is not valid UTF-8", f.name)
		}
		if f.required && v == "" {
			return in, invalid("%s is required", f.name)
		}
		if n := utf8.RuneCountInString(v); n > f.max {
			return in, invalid("%s has %d characters; at most %d", f.name, n, f.max)
		}
		for _, r := range v {
			if r == '\n' || r == '\r' {
				if f.oneLine {
					return in, invalid("%s must be one line", f.name)
				}
				continue
			}
			if unicode.IsControl(r) {
				return in, invalid("%s contains a control character", f.name)
			}
		}
		*f.value = v
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision < 0 {
		return in, invalid("expected_revision must not be negative")
	}
	return in, nil
}

func (in PresentationInput) same(p Presentation) bool {
	return in.ThemeEN == p.ThemeEN && in.ThemeDE == p.ThemeDE && in.HeadlineEN == p.HeadlineEN &&
		in.HeadlineDE == p.HeadlineDE && in.IntroEN == p.IntroEN && in.IntroDE == p.IntroDE
}

// AuthorizePresentation admits an active person or agent holding
// releases.deploy on the project, the same authority that ships its releases.
func AuthorizePresentation(ctx context.Context, tx pgx.Tx, actor tenant.Principal, projectID string) error {
	var kind, status string
	if err := tx.QueryRow(ctx, `SELECT kind,status FROM principals WHERE tenant_id=$1 AND id=$2`, actor.TenantID, actor.ID).Scan(&kind, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPresentationDenied
		}
		return err
	}
	if status != "active" || (kind != string(tenant.Person) && kind != string(tenant.Agent)) {
		return ErrPresentationDenied
	}
	if err := authz.RequireTx(ctx, tx, actor, "releases.deploy", authz.Scope{ProjectID: projectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return ErrPresentationDenied
		}
		return err
	}
	return nil
}

const presentationColumns = `theme_en,theme_de,headline_en,headline_de,intro_en,intro_de,revision,updated_at`

func scanPresentation(row pgx.Row, p *Presentation) error {
	return row.Scan(&p.ThemeEN, &p.ThemeDE, &p.HeadlineEN, &p.HeadlineDE, &p.IntroEN, &p.IntroDE, &p.Revision, &p.UpdatedAt)
}

// LoadPresentations returns the project's presentations by version. Row-level
// security limits them to the transaction's tenant and visible projects.
func LoadPresentations(ctx context.Context, tx pgx.Tx, projectID string) (map[string]Presentation, error) {
	rows, err := tx.Query(ctx, `SELECT version,`+presentationColumns+` FROM release_presentations WHERE project_node_id=$1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Presentation{}
	for rows.Next() {
		var version string
		var p Presentation
		if err := rows.Scan(&version, &p.ThemeEN, &p.ThemeDE, &p.HeadlineEN, &p.HeadlineDE, &p.IntroEN, &p.IntroDE, &p.Revision, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.UpdatedAt = p.UpdatedAt.UTC()
		out[version] = p
	}
	return out, rows.Err()
}

// LoadPresentation returns one version's presentation, or nil.
func LoadPresentation(ctx context.Context, tx pgx.Tx, projectID, version string, forUpdate bool) (*Presentation, error) {
	q := `SELECT ` + presentationColumns + ` FROM release_presentations WHERE project_node_id=$1 AND version=$2`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	var p Presentation
	if err := scanPresentation(tx.QueryRow(ctx, q, projectID, version), &p); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.UpdatedAt = p.UpdatedAt.UTC()
	return &p, nil
}

// PresentationChange is the outcome of a write, as the API and CLI report it.
type PresentationChange struct {
	Version   string        `json:"version"`
	ProjectID string        `json:"project_node_id"`
	Changed   bool          `json:"changed"`
	Before    *Presentation `json:"before"`
	After     *Presentation `json:"after"`
}

func presentationEvent(version, projectID string, p *Presentation) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"version": version, "project_node_id": projectID, "revision": p.Revision,
		"theme_en": p.ThemeEN, "theme_de": p.ThemeDE, "headline_en": p.HeadlineEN,
		"headline_de": p.HeadlineDE, "intro_en": p.IntroEN, "intro_de": p.IntroDE,
	}
}

func checkVersion(version string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !ValidVersion(v) {
		return "", invalid("version %q is not an inspr-calendar-v2 coordinate", version)
	}
	return v, nil
}

// PlanPresentation validates a write and returns what it would change without
// writing. lock takes the row lock a following write needs; a read-only dry run
// passes false. The caller must already have authorized the actor.
func PlanPresentation(ctx context.Context, tx pgx.Tx, projectID, version string, in PresentationInput, lock bool) (PresentationChange, PresentationInput, error) {
	v, err := checkVersion(version)
	if err != nil {
		return PresentationChange{}, in, err
	}
	in, err = in.Normalize()
	if err != nil {
		return PresentationChange{}, in, err
	}
	before, err := LoadPresentation(ctx, tx, projectID, v, lock)
	if err != nil {
		return PresentationChange{}, in, err
	}
	if in.ExpectedRevision != nil {
		current := 0
		if before != nil {
			current = before.Revision
		}
		if current != *in.ExpectedRevision {
			return PresentationChange{}, in, ErrPresentationConflict
		}
	}
	change := PresentationChange{Version: v, ProjectID: projectID, Before: before}
	if before != nil && in.same(*before) {
		change.After = before
		return change, in, nil
	}
	after := Presentation{ThemeEN: in.ThemeEN, ThemeDE: in.ThemeDE, HeadlineEN: in.HeadlineEN, HeadlineDE: in.HeadlineDE, IntroEN: in.IntroEN, IntroDE: in.IntroDE, Revision: 1}
	if before != nil {
		after.Revision = before.Revision + 1
	}
	change.Changed, change.After = true, &after
	return change, in, nil
}

// SavePresentation writes a presentation and one release.presentation_set
// event, inside the caller's tenant transaction. An identical write changes
// nothing and records no event. The caller must already have authorized actor.
func SavePresentation(ctx context.Context, tx pgx.Tx, actor tenant.Principal, projectID, version string, in PresentationInput) (PresentationChange, error) {
	change, in, err := PlanPresentation(ctx, tx, projectID, version, in, true)
	if err != nil || !change.Changed {
		return change, err
	}
	var saved Presentation
	err = scanPresentation(tx.QueryRow(ctx, `INSERT INTO release_presentations
  (tenant_id,project_node_id,version,theme_en,theme_de,headline_en,headline_de,intro_en,intro_de,revision,updated_by)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10)
  ON CONFLICT (tenant_id,project_node_id,version) DO UPDATE SET
   theme_en=EXCLUDED.theme_en, theme_de=EXCLUDED.theme_de, headline_en=EXCLUDED.headline_en,
   headline_de=EXCLUDED.headline_de, intro_en=EXCLUDED.intro_en, intro_de=EXCLUDED.intro_de,
   revision=release_presentations.revision+1, updated_by=EXCLUDED.updated_by, updated_at=now()
  RETURNING `+presentationColumns,
		actor.TenantID, projectID, change.Version, in.ThemeEN, in.ThemeDE, in.HeadlineEN, in.HeadlineDE, in.IntroEN, in.IntroDE, actor.ID), &saved)
	if err != nil {
		return change, err
	}
	saved.UpdatedAt = saved.UpdatedAt.UTC()
	change.After = &saved
	nodeID := projectID
	if _, err := events.Append(ctx, tx, actor, events.Change{NodeID: &nodeID, Type: PresentationSetEvent,
		Before: presentationEvent(change.Version, projectID, change.Before), After: presentationEvent(change.Version, projectID, &saved)}); err != nil {
		return change, err
	}
	return change, nil
}

// ClearPresentation removes a presentation and records one
// release.presentation_cleared event. Clearing nothing records nothing.
func ClearPresentation(ctx context.Context, tx pgx.Tx, actor tenant.Principal, projectID, version string) (PresentationChange, error) {
	v, err := checkVersion(version)
	if err != nil {
		return PresentationChange{}, err
	}
	before, err := LoadPresentation(ctx, tx, projectID, v, true)
	if err != nil || before == nil {
		return PresentationChange{Version: v, ProjectID: projectID}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM release_presentations WHERE project_node_id=$1 AND version=$2`, projectID, v); err != nil {
		return PresentationChange{}, err
	}
	nodeID := projectID
	if _, err := events.Append(ctx, tx, actor, events.Change{NodeID: &nodeID, Type: PresentationClearedEvent,
		Before: presentationEvent(v, projectID, before), After: map[string]any{"version": v, "project_node_id": projectID}}); err != nil {
		return PresentationChange{}, err
	}
	return PresentationChange{Version: v, ProjectID: projectID, Changed: true, Before: before}, nil
}
