// SPDX-License-Identifier: AGPL-3.0-only

// Package autopilotlanes stores person-approved dispatch policy. It neither
// schedules nor launches work, and never reads Status autopilot settings.
package autopilotlanes

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

var uuid = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var clockTime = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

type Scope struct {
	Kind          string  `json:"kind"`
	ReleaseNodeID *string `json:"release_node_id"`
}
type Budget struct {
	AgentHours float64 `json:"agent_hours"`
}
type Window struct {
	Timezone string `json:"timezone"`
	Days     []int  `json:"days"`
	Start    string `json:"start"`
	End      string `json:"end"`
}
type Policy struct {
	AllowedKinds           []string `json:"allowed_kinds"`
	AllowedTags            []string `json:"allowed_tags"`
	AllowedAreas           []string `json:"allowed_areas"`
	MaxTicketEstimateHours float64  `json:"max_ticket_estimate_hours"`
	ParallelLimit          int      `json:"parallel_limit"`
	Budget                 Budget   `json:"budget"`
	Window                 Window   `json:"window"`
}
type Lane struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"project_id"`
	OwnerPrincipalID string    `json:"owner_principal_id"`
	Name             string    `json:"name"`
	Priority         int       `json:"priority"`
	Revision         int64     `json:"revision"`
	Enabled          bool      `json:"enabled"`
	Paused           bool      `json:"paused"`
	PauseReason      string    `json:"pause_reason"`
	Scope            Scope     `json:"scope"`
	Policy           Policy    `json:"policy"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func boundedText(s string, max int, empty bool) bool {
	return utf8.ValidString(s) && len(s) <= max && !strings.ContainsRune(s, 0) && (empty || strings.TrimSpace(s) != "")
}
func validHours(h, max float64) bool { return !math.IsNaN(h) && !math.IsInf(h, 0) && h > 0 && h <= max }
func validateScope(s Scope) error {
	if s.Kind == "queued_tickets" && s.ReleaseNodeID == nil {
		return nil
	}
	if s.Kind == "release" && s.ReleaseNodeID != nil && uuid.MatchString(*s.ReleaseNodeID) {
		return nil
	}
	return errors.New("scope requires queued_tickets without a release, or release with a release_node_id")
}
func validatePolicy(p Policy) error {
	for _, list := range [][]string{p.AllowedKinds, p.AllowedTags, p.AllowedAreas} {
		if list == nil || len(list) > 32 {
			return errors.New("filter arrays required, at most 32 values each")
		}
		seen := map[string]bool{}
		for _, value := range list {
			if !boundedText(value, 80, false) || strings.TrimSpace(value) != value || seen[value] {
				return errors.New("filters must be distinct nonempty values of at most 80 bytes")
			}
			seen[value] = true
		}
	}
	if !validHours(p.MaxTicketEstimateHours, 200) || p.ParallelLimit < 1 || p.ParallelLimit > 20 || !validHours(p.Budget.AgentHours, 10000) {
		return errors.New("finite positive estimate/budget and parallel_limit 1..20 required")
	}
	w := p.Window
	if !boundedText(w.Timezone, 100, false) || w.Timezone == "Local" {
		return errors.New("IANA timezone required")
	}
	if _, err := time.LoadLocation(w.Timezone); err != nil {
		return errors.New("unknown timezone")
	}
	if len(w.Days) < 1 || len(w.Days) > 7 || !clockTime.MatchString(w.Start) || !clockTime.MatchString(w.End) || w.Start == w.End {
		return errors.New("weekdays and distinct HH:MM endpoints required")
	}
	seen := map[int]bool{}
	for _, day := range w.Days {
		if day < 1 || day > 7 || seen[day] {
			return errors.New("distinct ISO weekdays 1..7 required")
		}
		seen[day] = true
	}
	return nil
}
func includes(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
func matches(policy Policy, kind string, fields map[string]any) bool {
	if len(policy.AllowedKinds) > 0 && !includes(policy.AllowedKinds, kind) {
		return false
	}
	area, _ := fields["area"].(string)
	if len(policy.AllowedAreas) > 0 && !includes(policy.AllowedAreas, area) {
		return false
	}
	if len(policy.AllowedTags) == 0 {
		return true
	}
	if tags, ok := fields["tags"].([]any); ok {
		for _, tag := range tags {
			if s, ok := tag.(string); ok && includes(policy.AllowedTags, s) {
				return true
			}
		}
	}
	return false
}

type WindowInstance struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

// nextWindow is advisory wall-clock policy, not a launch grant. ISO weekdays
// belong to the start date. Include yesterday for an active overnight window.
// Skip nonexistent DST endpoints rather than silently shifting the approval.
func nextWindow(w Window, now time.Time) *WindowInstance {
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return nil
	}
	local := now.In(loc)
	sh, sm := clockParts(w.Start)
	eh, em := clockParts(w.End)
	for offset := -1; offset <= 7; offset++ {
		day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, offset)
		weekday := int(day.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		allowed := false
		for _, d := range w.Days {
			allowed = allowed || d == weekday
		}
		if !allowed {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), sh, sm, 0, 0, loc)
		endDay := day
		if w.End < w.Start {
			endDay = endDay.AddDate(0, 0, 1)
		}
		end := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), eh, em, 0, 0, loc)
		if start.Format("15:04") != w.Start || end.Format("15:04") != w.End || !end.After(start) || !end.After(now) {
			continue
		}
		return &WindowInstance{start.UTC(), end.UTC()}
	}
	return nil
}
func clockParts(s string) (int, int) {
	return int(s[0]-'0')*10 + int(s[1]-'0'), int(s[3]-'0')*10 + int(s[4]-'0')
}
