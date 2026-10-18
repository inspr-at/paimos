// SPDX-License-Identifier: AGPL-3.0-only

package goals

import "time"

const (
	GoalKind       = "goal"
	OutcomeKind    = "outcome"
	MaxBodyBytes   = 256 << 10
	MaxOutcomes    = 200
	MaxOperations  = 200
	MaxTargets     = 20
	MaxSteps       = 20
	MaxRetiredRefs = 200
	PassedGrace    = 10 * time.Minute
	StallAfter     = 4 * time.Hour
)

// IsGoalKind deliberately includes release without deriving any membership
// from release/journey storage. Releases remain project-owned nodes.
func IsGoalKind(slug string) bool { return slug == GoalKind || slug == "release" }

type State string

const (
	Moving State = "moving"
	Queued State = "queued"
	Paused State = "paused"
	Done   State = "done"
)

func (s State) valid() bool { return s == Moving || s == Queued || s == Paused || s == Done }

type Step struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	State string `json:"state"`
}

// NextStep returns the current step, otherwise the first later step. The
// caller validates the ordered list before storing it in nodes.fields.
func NextStep(steps []Step) *Step {
	for _, step := range steps {
		if step.State == "now" {
			return &step
		}
	}
	for _, step := range steps {
		if step.State == "later" {
			return &step
		}
	}
	return nil
}

// ETAFields are flat node fields, shared by goals and outcomes. The history
// is the event log; no previous-value column or field is stored.
type ETAFields struct {
	Text  string     `json:"eta_text,omitempty"`
	At    *time.Time `json:"eta_at,omitempty"`
	SetBy string     `json:"eta_set_by,omitempty"`
	SetAt *time.Time `json:"eta_set_at,omitempty"`
}

type GoalFields struct {
	ETAFields
	Steps        []Step     `json:"steps,omitempty"`
	OverridePct  *int       `json:"progress_override_pct,omitempty"`
	OverrideNote string     `json:"progress_override_note,omitempty"`
	OverrideBy   string     `json:"progress_override_by,omitempty"`
	OverrideAt   *time.Time `json:"progress_override_at,omitempty"`
	RetiredRefs  []string   `json:"retired_refs,omitempty"`
}

type OutcomeFields struct {
	ETAFields
	Ref            string     `json:"outcome_ref"`
	Note           string     `json:"note,omitempty"`
	StateSet       State      `json:"state_set,omitempty"`
	HandedOverAt   *time.Time `json:"handed_over_at,omitempty"`
	HandedOverBy   string     `json:"handed_over_by,omitempty"`
	HandedOverText string     `json:"handed_over_detail,omitempty"`
}

// Watermark orders time-driven reads even when no event has been appended.
// Receipts retain the original pair; replay never refreshes it.
type Watermark struct {
	ReadThrough int64 `json:"read_through"`
	Generation  int64 `json:"generation"`
}

func (w Watermark) OlderThan(other Watermark) bool {
	return w.ReadThrough < other.ReadThrough || w.ReadThrough == other.ReadThrough && w.Generation < other.Generation
}

// ChangePing is the entire payload of a derived goal-level notification.
// Contributor values belong only in outcome-scoped events and filtered reads.
type ChangePing struct {
	GoalID string `json:"goal_id"`
	Watermark
}

func Passed(at *time.Time, state State, now time.Time) bool {
	return at != nil && state != Done && now.After(at.Add(PassedGrace))
}

type Stall struct {
	Stalled bool      `json:"stalled"`
	Since   time.Time `json:"since"`
}

// DeriveStall receives the last change of the visibility-filtered displayed
// pct (including a manual override), or created_at when it never changed.
func DeriveStall(moving int, since, now time.Time) Stall {
	return Stall{Stalled: moving > 0 && !now.Before(since.Add(StallAfter)), Since: since}
}
