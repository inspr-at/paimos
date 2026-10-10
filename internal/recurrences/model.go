// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const Permission = "recurrences.manage"
const Job = "recurring-work"

type Template struct {
	Name                 string   `json:"name,omitempty"`
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	Criteria             []string `json:"acceptance_criteria"`
	EstimateHours        float64  `json:"estimate_hours"`
	Priority             string   `json:"priority"`
	Tags                 []string `json:"tags"`
	Type                 string   `json:"type"`
	PillEN               string   `json:"pill_en,omitempty"`
	PillDE               string   `json:"pill_de,omitempty"`
	BenefitEN            string   `json:"benefit_en,omitempty"`
	BenefitDE            string   `json:"benefit_de,omitempty"`
	HideFromReleaseNotes *bool    `json:"hide_from_release_notes,omitempty"`
}
type Input struct {
	ProjectID     string   `json:"project_id"`
	ParentID      string   `json:"parent_id"`
	Template      Template `json:"template"`
	Trigger       Trigger  `json:"trigger"`
	QueueEach     bool     `json:"queue_each"`
	OverlapPolicy string   `json:"overlap_policy"`
	CatchUpPolicy string   `json:"catch_up_policy"`
}
type Recurrence struct {
	Input
	ID              string     `json:"id"`
	Paused          bool       `json:"paused"`
	Revision        int64      `json:"revision"`
	OccurrenceCount int64      `json:"occurrence_count"`
	NextAt          *time.Time `json:"next_at"`
	EventCursor     int64      `json:"event_cursor"`
	ActiveSince     time.Time  `json:"active_since"`
	CreatedBy       string     `json:"created_by_principal_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastResult      *Result    `json:"last_result,omitempty"`
	OpenPrevious    *Result    `json:"open_previous,omitempty"`
}
type Result struct {
	Occurrence
	NodeKey string `json:"key,omitempty"`
	Title   string `json:"title,omitempty"`
	State   string `json:"state,omitempty"`
}
type Occurrence struct {
	RecurrenceID  string    `json:"recurrence_id"`
	Key           string    `json:"occurrence_key"`
	Number        int64     `json:"number"`
	ScheduledAt   time.Time `json:"scheduled_at"`
	NodeID        *string   `json:"node_id"`
	SourceEventID *int64    `json:"source_event_id"`
	Outcome       string    `json:"outcome"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
}

var variablePattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

func validateVariables(value string) error {
	for _, m := range variablePattern.FindAllStringSubmatch(value, -1) {
		switch m[1] {
		case "occurrence", "date", "release_name", "release_version":
		default:
			return fmt.Errorf("unknown template variable %q", m[1])
		}
	}
	rest := variablePattern.ReplaceAllString(value, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return fmt.Errorf("invalid template variable")
	}
	return nil
}
func (in *Input) normalize(now time.Time) error {
	if !workorders.UUID(in.ProjectID) || !workorders.UUID(in.ParentID) {
		return fmt.Errorf("project_id and parent_id must be UUIDs")
	}
	in.ProjectID = strings.ToLower(in.ProjectID)
	in.ParentID = strings.ToLower(in.ParentID)
	t := &in.Template
	if t.HideFromReleaseNotes == nil {
		hidden := true
		t.HideFromReleaseNotes = &hidden
	}
	t.Name = strings.TrimSpace(t.Name)
	if len(t.Name) > 80 {
		return fmt.Errorf("name must be at most 80 bytes")
	}
	if t.Type == "" {
		t.Type = "work"
	}
	if t.Priority == "" {
		t.Priority = "medium"
	}
	if t.Criteria == nil {
		t.Criteria = []string{}
	}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	if t.Type == "ticket" || t.Type == "task" || t.Type == "epic" {
		t.Type = "work"
	}
	if t.Type != "work" {
		return fmt.Errorf("template type must be work (ticket, task and epic are compatibility aliases)")
	}
	switch t.Priority {
	case "critical", "high", "medium", "low":
	default:
		return fmt.Errorf("invalid priority")
	}
	if strings.TrimSpace(t.Title) == "" || len(t.Title) > 512 || len(t.Description) > 65536 || math.IsNaN(t.EstimateHours) || math.IsInf(t.EstimateHours, 0) || t.EstimateHours < 0 || t.EstimateHours > 200 || len(t.Criteria) > 100 || len(t.Tags) > 50 {
		return fmt.Errorf("template exceeds its limits")
	}
	if len(t.PillEN) > 512 || len(t.PillDE) > 512 || len(t.BenefitEN) > 4096 || len(t.BenefitDE) > 4096 {
		return fmt.Errorf("release copy exceeds its limits")
	}
	values := append([]string{t.Title, t.Description, t.PillEN, t.PillDE, t.BenefitEN, t.BenefitDE}, t.Criteria...)
	for _, value := range values {
		if err := validateVariables(value); err != nil {
			return err
		}
	}
	for _, c := range t.Criteria {
		if strings.TrimSpace(c) == "" || len(c) > 4096 {
			return fmt.Errorf("criteria must be nonblank and at most 4096 bytes")
		}
	}
	seen := map[string]bool{}
	for i, tag := range t.Tags {
		tag = strings.ToLower(tag)
		if !workorders.UUID(tag) || seen[tag] {
			return fmt.Errorf("tags must be unique tag-node UUIDs")
		}
		seen[tag] = true
		t.Tags[i] = tag
	}
	if in.QueueEach && (t.EstimateHours <= 0 || len(t.Criteria) == 0) {
		return fmt.Errorf("queue_each requires an estimate and acceptance criteria")
	}
	if in.OverlapPolicy == "" {
		in.OverlapPolicy = "skip"
	}
	if in.CatchUpPolicy == "" {
		in.CatchUpPolicy = "one"
	}
	if in.OverlapPolicy != "skip" && in.OverlapPolicy != "create" || in.CatchUpPolicy != "one" {
		return fmt.Errorf("invalid overlap or catch-up policy")
	}
	switch in.Trigger.Kind {
	case "time":
		if in.Trigger.StartDate == "" {
			loc, err := time.LoadLocation(in.Trigger.Timezone)
			if err != nil || in.Trigger.Timezone == "Local" {
				return fmt.Errorf("invalid timezone")
			}
			in.Trigger.StartDate = now.In(loc).Format(time.DateOnly)
		}
		_, err := parseSchedule(in.Trigger)
		return err
	case "event":
		if err := in.Trigger.normalizeEvent(); err != nil {
			return err
		}
		if in.Trigger.RRULE != "" || in.Trigger.TimeOfDay != "" || in.Trigger.Timezone != "" || in.Trigger.StartDate != "" {
			return fmt.Errorf("event trigger must not contain time fields")
		}
		if in.Trigger.EventStart != "" && in.Trigger.EventStart != "now" && in.Trigger.EventStart != "hour" && in.Trigger.EventStart != "morning" {
			return fmt.Errorf("event_start must be now, hour or morning")
		}
		if len(in.Trigger.EventTimezone) > 128 || in.Trigger.EventTimezone == "Local" {
			return fmt.Errorf("invalid event timezone")
		}
		if in.Trigger.EventTimezone != "" {
			if _, err := time.LoadLocation(in.Trigger.EventTimezone); err != nil {
				return fmt.Errorf("invalid event timezone")
			}
		}
	default:
		return fmt.Errorf("trigger kind must be time or event")
	}
	return nil
}
func render(value string, number int64, at time.Time, trigger Trigger, name, version string) string {
	zone := trigger.Timezone
	if trigger.Kind == "event" {
		zone = trigger.EventTimezone
	}
	if zone != "" {
		if loc, err := time.LoadLocation(zone); err == nil {
			at = at.In(loc)
		}
	}
	return strings.NewReplacer("{{occurrence}}", fmt.Sprint(number), "{{date}}", at.Format(time.DateOnly), "{{release_name}}", name, "{{release_version}}", version).Replace(value)
}

const recurrenceColumns = `id::text,project_id::text,parent_id::text,template,trigger,queue_each,overlap_policy,catch_up_policy,paused,revision,occurrence_count,next_at,event_cursor,active_since,created_by_principal_id::text,created_at,updated_at`

func scanRecurrence(row pgx.Row) (Recurrence, error) {
	var r Recurrence
	var template, trigger []byte
	err := row.Scan(&r.ID, &r.ProjectID, &r.ParentID, &template, &trigger, &r.QueueEach, &r.OverlapPolicy, &r.CatchUpPolicy, &r.Paused, &r.Revision, &r.OccurrenceCount, &r.NextAt, &r.EventCursor, &r.ActiveSince, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(template, &r.Template)
	}
	if err == nil {
		err = json.Unmarshal(trigger, &r.Trigger)
	}
	if r.NextAt != nil {
		at := r.NextAt.UTC()
		r.NextAt = &at
	}
	r.ActiveSince = r.ActiveSince.UTC()
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, err
}
func load(ctx context.Context, tx pgx.Tx, id string, lock bool) (Recurrence, error) {
	query := `SELECT ` + recurrenceColumns + ` FROM recurrences WHERE id=$1 AND retired_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanRecurrence(tx.QueryRow(ctx, query, id))
}

const occurrenceColumns = `recurrence_id::text,occurrence_key,number,scheduled_at,node_id::text,source_event_id,outcome,reason,created_at`

func scanOccurrence(row pgx.Row) (Occurrence, error) {
	var o Occurrence
	err := row.Scan(&o.RecurrenceID, &o.Key, &o.Number, &o.ScheduledAt, &o.NodeID, &o.SourceEventID, &o.Outcome, &o.Reason, &o.CreatedAt)
	o.ScheduledAt = o.ScheduledAt.UTC()
	o.CreatedAt = o.CreatedAt.UTC()
	return o, err
}
