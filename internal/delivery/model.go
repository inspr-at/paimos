// SPDX-License-Identifier: AGPL-3.0-only
// Package delivery observes platform/GitHub facts; it grants no execution,
// enqueue, merge, publication or deployment authority.
package delivery

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"time"
)

type State string

const (
	Built       State = "built"
	Reviewed    State = "reviewed"
	Pushed      State = "pushed"
	CIGreen     State = "ci_green"
	InQueue     State = "in_queue"
	Merged      State = "merged"
	QueueFailed State = "queue_failed"
	Held        State = "held"
)

func validState(s State) bool {
	return slices.Contains([]State{Built, Reviewed, Pushed, CIGreen, InQueue, Merged, QueueFailed, Held}, s)
}
func owner(s State) string {
	switch s {
	case Built, Reviewed, CIGreen:
		return "coordinator"
	case Pushed:
		return "ci"
	case InQueue:
		return "queue"
	case QueueFailed:
		return "builder"
	case Held:
		return "person"
	}
	return ""
}

type Settings struct {
	RequiredChecks *[]string     `json:"required_checks,omitempty"`
	Deadlines      map[State]int `json:"deadlines,omitempty"`
}

func defaults() Settings {
	checks := []string{"go", "web", "release-check", "e2e", "migration-compat"}
	return Settings{&checks, map[State]int{Reviewed: 30, Pushed: 60, CIGreen: 20, InQueue: 60, QueueFailed: 120}}
}
func effective(parent, child Settings) Settings {
	out := Settings{parent.RequiredChecks, map[State]int{}}
	for k, v := range parent.Deadlines {
		out.Deadlines[k] = v
	}
	if child.RequiredChecks != nil {
		out.RequiredChecks = child.RequiredChecks
	}
	for k, v := range child.Deadlines {
		out.Deadlines[k] = v
	}
	return out
}

type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// Observation is the replay source. It contains normalized platform facts at
// observation time, not the raw webhook, PR title, body, author or review output.
type Observation struct {
	ID           string    `json:"id"`
	Project      *string   `json:"project_id"`
	Ticket       *string   `json:"ticket_node_id"`
	Repository   string    `json:"repository"`
	PR           *int64    `json:"pull_request"`
	Branch       string    `json:"branch"`
	Head         string    `json:"head_sha"`
	Base         string    `json:"base_sha"`
	LinkSource   *string   `json:"link_source"`
	Open         bool      `json:"open"`
	Merged       bool      `json:"merged"`
	Queued       bool      `json:"queued"`
	QueueHead    string    `json:"queue_head"`
	QueueFailure bool      `json:"queue_failed"`
	Built        bool      `json:"built"`
	ReviewedHead string    `json:"reviewed_head"`
	Checks       []Check   `json:"checks"`
	HoldReason   *string   `json:"hold_reason"`
	HeldFrom     *State    `json:"held_from_state,omitempty"`
	Settings     Settings  `json:"settings"`
	At           time.Time `json:"at"`
}
type Item struct {
	ID           string      `json:"id"`
	Project      *string     `json:"project_id"`
	Ticket       *string     `json:"ticket_node_id"`
	Repository   string      `json:"repository"`
	PR           *int64      `json:"pull_request"`
	Branch       string      `json:"branch"`
	Head         string      `json:"head_sha"`
	State        State       `json:"state"`
	Since        time.Time   `json:"state_since"`
	Owner        string      `json:"owner"`
	Deadline     *time.Time  `json:"deadline_at"`
	HeldReason   *string     `json:"held_reason"`
	HeldFrom     *State      `json:"held_from_state"`
	ChecksPassed int         `json:"required_checks_passed"`
	ChecksTotal  int         `json:"required_checks_total"`
	LinkSource   *string     `json:"link_source"`
	Updated      time.Time   `json:"updated_at"`
	Observation  Observation `json:"-"`
}

func allSuccess(o Observation) bool {
	if o.Settings.RequiredChecks == nil || len(*o.Settings.RequiredChecks) == 0 {
		return false
	}
	for _, name := range *o.Settings.RequiredChecks {
		found := false
		for _, c := range o.Checks {
			if c.Name == name {
				found = c.Status == "completed" && c.Conclusion == "success"
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func state(o Observation) State {
	if o.Merged {
		return Merged
	}
	if o.HoldReason != nil {
		return Held
	}
	if o.QueueFailure {
		return QueueFailed
	}
	if o.Queued && o.Open {
		return InQueue
	}
	if o.Open && allSuccess(o) {
		return CIGreen
	}
	if o.ReviewedHead != "" {
		if o.PR == nil || o.Head != o.ReviewedHead {
			return Reviewed
		}
		if o.Open {
			return Pushed
		}
	}
	return Built
}
func project(o Observation, before *Item) Item {
	s := state(o)
	since := o.At
	// A new head is a new delivery episode even when its state has the same name.
	if before != nil && before.State == s && before.Head == o.Head {
		since = before.Since
	}
	var deadline *time.Time
	if n := o.Settings.Deadlines[s]; n > 0 {
		t := since.Add(time.Duration(n) * time.Minute)
		deadline = &t
	}
	if s == Held || s == Merged {
		deadline = nil
	}
	passed, total := checkProgress(o)
	return Item{ID: o.ID, Project: o.Project, Ticket: o.Ticket, Repository: o.Repository, PR: o.PR, Branch: o.Branch, Head: o.Head, State: s, Since: since, Owner: owner(s), Deadline: deadline, HeldReason: o.HoldReason, HeldFrom: o.HeldFrom, ChecksPassed: passed, ChecksTotal: total, LinkSource: o.LinkSource, Updated: o.At, Observation: o}
}
func stableID(tenant, repo, subject string) string {
	h := sha256.Sum256([]byte("aeon-delivery\x00" + tenant + "\x00" + repo + "\x00" + subject))
	h[6] = (h[6] & 15) | 80
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func snapshot(i *Item) any {
	if i == nil {
		return nil
	}
	return map[string]any{"state": i.State, "head_sha": i.Head, "owner": i.Owner, "deadline_at": i.Deadline}
}

// Count only current-head configured checks, with the same success rule as state().
func checkProgress(o Observation) (passed, total int) {
	if o.Settings.RequiredChecks == nil {
		return
	}
	for _, name := range *o.Settings.RequiredChecks {
		total++
		for _, c := range o.Checks {
			if c.Name == name {
				if c.Status == "completed" && c.Conclusion == "success" {
					passed++
				}
				break
			}
		}
	}
	return
}
