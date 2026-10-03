// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type preferenceEffective struct {
	Selector          modelprefs.Cell `json:"selector"`
	Profile           *Profile        `json:"profile"`
	Label             string          `json:"label"`
	Brand             string          `json:"brand"`
	FollowsLatest     bool            `json:"follows_latest"`
	Pinned            bool            `json:"pinned"`
	TodayVersion      string          `json:"today_version"`
	UnavailableReason string          `json:"unavailable_reason,omitempty"`
}
type preferenceViewRow struct {
	KindID      string              `json:"kind_id"`
	SetBy       string              `json:"set_by"`
	LockedBy    string              `json:"locked_by"`
	ChangedHere bool                `json:"changed_here"`
	ResetTo     string              `json:"reset_to"`
	Warnings    []string            `json:"warnings"`
	Normal      preferenceEffective `json:"normal"`
	Complex     preferenceEffective `json:"complex"`
}
type preferenceView struct {
	Choices          []preferenceChoice `json:"choices"`
	ChoicesTruncated bool               `json:"choices_truncated"`
	Residency        struct {
		modelprefs.ResidencyResult
		QualifyingRoutes int `json:"qualifying_routes"`
	} `json:"residency"`
	Rows    []preferenceViewRow `json:"rows"`
	Changes int                 `json:"changes"`
}

type preferenceChoice struct {
	Profile         Profile `json:"profile"`
	Line            string  `json:"line"`
	ModelVersion    string  `json:"model_version"`
	Retired         bool    `json:"retired"`
	ReviewLadder    bool    `json:"review_ladder"`
	ReviewReason    string  `json:"review_reason"`
	ResidencyRoutes int     `json:"residency_routes"`
}
type preferenceDocument struct {
	Revision          int64                       `json:"revision"`
	PersonID          *string                     `json:"person_id"`
	Kinds             []modelprefs.Kind           `json:"kinds"`
	Levels            map[string]*preferenceLevel `json:"levels"`
	Views             map[string]*preferenceView  `json:"views"`
	Can               map[string]bool             `json:"can"`
	ResidencyLockMode string                      `json:"residency_lock_mode"`
}

func (m *Module) preferenceDocument(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (preferenceDocument, error) {
	person, err := currentPreferencePerson(ctx, tx, p)
	if err != nil {
		return preferenceDocument{}, err
	}
	out := preferenceDocument{PersonID: person, Levels: map[string]*preferenceLevel{"project": nil}, Views: map[string]*preferenceView{"project": nil}, Can: map[string]bool{}, ResidencyLockMode: "warn"}
	kinds, err := visibleKinds(ctx, tx, project)
	if err != nil {
		return out, err
	}
	out.Kinds = kinds
	chain, err := modelprefs.LoadChain(ctx, tx, person, project)
	if err != nil {
		return out, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return out, err
	}
	profiles, err := listPickerProfiles(ctx, tx)
	if err != nil {
		return out, err
	}
	truncated := len(profiles) > 256
	if truncated {
		profiles = profiles[:256]
	}
	routes, err := listRoutes(ctx, tx)
	if err != nil {
		return out, err
	}
	reviewProfiles := map[string]bool{}
	for _, route := range routes {
		if route.Role == "review-gate" {
			reviewProfiles[route.ProfileID] = true
		}
	}
	routeProfiles := map[string]string{}
	for _, profile := range profiles {
		if profile.Enabled {
			routeProfiles[profile.ID] = profile.Harness
		}
	}
	for i, s := range chain {
		if i == 2 && project == "" {
			continue
		}
		level, err := levelResponse(ctx, tx, s, kinds)
		if err != nil {
			return out, err
		}
		out.Levels[s.Level] = &level
		out.Revision += s.Revision
		view := &preferenceView{Rows: []preferenceViewRow{}, ChoicesTruncated: truncated}
		view.Residency.ResidencyResult = modelprefs.ResolveResidency(chain[:i+1])
		view.Changes = len(level.Rows)
		if s.Residency != nil {
			view.Changes++
		}
		if s.PrefsLocked {
			view.Changes++
		}
		if s.ResidencyLocked {
			view.Changes++
		}
		preview := p
		var previewPerson *string
		previewProject := ""
		if i == 0 {
			preview.Kind = tenant.Agent
			preview.KeyCreatorID = ""
		} else {
			previewPerson = person
		}
		if i == 2 {
			previewProject = project
		}
		// Count account/profile routes, not profiles. No evidence means EU/local
		// has zero routes, even for a locally installed vendor CLI.
		counts, countErr := agentaccounts.ResidencyProfileRouteCounts(ctx, tx, routeProfiles, previewProject, view.Residency.Value, now)
		err = countErr
		if err != nil {
			return out, err
		}
		for _, count := range counts {
			view.Residency.QualifyingRoutes += count
		}
		view.Choices = make([]preferenceChoice, 0, len(profiles))
		for _, profile := range profiles {
			_, line, version := ProfileLine(profile)
			reason := ""
			if !reviewProfiles[profile.ID] {
				reason = "Outside the review ladder"
			} else if (profile.Tier != "strong" && profile.Tier != "frontier") || profile.Effort != "xhigh" {
				reason = "Reviews require strong or frontier models at extra high effort"
			} else if capability := agentverification.For(profile.Harness, "darwin", "arm64"); !capability.Supported {
				reason = capability.Reason
			}
			view.Choices = append(view.Choices, preferenceChoice{Profile: profile, Line: line, ModelVersion: version, ReviewLadder: reviewProfiles[profile.ID], ReviewReason: reason, ResidencyRoutes: counts[profile.ID]})
		}
		resolvedCells := map[string]WorkResolution{}
		for _, kind := range kinds {
			if kind.ProjectID != nil && i < 2 {
				continue
			}
			cr := modelprefs.ResolveCell(chain[:i+1], kind.Slug, "normal")
			row := preferenceViewRow{KindID: kind.ID, SetBy: cr.SetBy, LockedBy: cr.LockedBy, Warnings: []string{}}
			_, own := s.Rows[kind.Slug]
			row.ChangedHere = i > 0 && own && (cr.LockedBy == "" || cr.LockedBy == s.Level)
			if row.ChangedHere {
				row.ResetTo = "default"
				if i == 2 {
					row.ResetTo = "person"
				}
			}
			for _, bucket := range []string{"normal", "complex"} {
				role, complexity := "build", "M"
				if bucket == "complex" {
					role, complexity = "build-hard", "L"
				}
				q := WorkQuery{Role: role, Area: kind.Slug, Complexity: complexity, PersonID: previewPerson, ProjectID: previewProject}
				if kind.Slug == "review" {
					q.Role = "review-gate"
					q.AuthorFamily = "openai"
				}
				cell := modelprefs.ResolveCell(chain[:i+1], kind.Slug, bucket)
				cacheCell, _ := json.Marshal(cell.Cell)
				cacheKey := q.Role + bucket + string(cacheCell)
				resolved, hit := resolvedCells[cacheKey]
				if !hit {
					resolved, err = ResolveWork(ctx, tx, preview, q, now)
					if err != nil {
						return out, err
					}
					resolvedCells[cacheKey] = resolved
				}
				e := preferenceEffective{Selector: modelprefs.Cell{Mode: "auto"}, Profile: resolved.Profile, Label: "Automatic · follows the ticket's role", UnavailableReason: resolved.Trace.Fallback}
				if cell.Cell != nil {
					e.Selector = *cell.Cell
				}
				e.FollowsLatest = e.Selector.Mode == "latest"
				e.Pinned = e.Selector.Mode == "pinned"
				if resolved.Trace.Blocked != "" {
					e.UnavailableReason = resolved.Trace.Blocked
				}
				if resolved.Profile != nil {
					e.Label = resolved.Profile.DisplayName
					if e.Label == "" {
						e.Label = resolved.Profile.Model
					}
					e.Brand = resolved.Profile.Family
					_, _, e.TodayVersion = ProfileLine(*resolved.Profile)
				}
				if kind.Slug == "review" && e.Selector.Mode == "auto" {
					e.Label = "Another family · strongest available"
				}
				if e.UnavailableReason != "" {
					row.Warnings = append(row.Warnings, e.UnavailableReason)
				}
				if bucket == "normal" {
					row.Normal = e
				} else {
					row.Complex = e
				}
			}
			view.Rows = append(view.Rows, row)
		}
		out.Views[s.Level] = view
		out.Can["edit_"+s.Level] = authorizePreference(ctx, tx, p, s.Level, project, person) == nil
	}
	out.Can["edit_project"] = project != "" && out.Can["edit_project"]
	out.Can["add_default_kind"] = out.Can["edit_default"]
	out.Can["add_project_kind"] = out.Can["edit_project"]
	return out, nil
}
