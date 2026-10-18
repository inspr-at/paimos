// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// PauseProgress is one coherent snapshot, independent of a pause request and
// the immutable command label of a terminal session. Only its worker may write
// it, through the authenticated heartbeat while holding the session row lock.
type PauseProgress struct {
	ReportedAt     time.Time `json:"reported_at"`
	Step           *string   `json:"step,omitempty"`
	NextPoint      *string   `json:"next_point,omitempty"`
	NextPointInMin *float64  `json:"next_point_in_min,omitempty"`
	FinishInMin    *float64  `json:"finish_in_min,omitempty"`
	FinishOutcome  *string   `json:"finish_outcome,omitempty"`
	Interrupt      *bool     `json:"interrupt,omitempty"`
	Command        *string   `json:"command,omitempty"`
	CommandLeftMin *float64  `json:"command_left_min,omitempty"`
}

// Raw fields distinguish an omitted snapshot from an explicit clear. Updating
// a single field never refreshes estimates omitted from that new snapshot.
type pauseProgressReport struct {
	Step           json.RawMessage `json:"step,omitempty"`
	NextPoint      json.RawMessage `json:"next_point,omitempty"`
	NextPointInMin json.RawMessage `json:"next_point_in_min,omitempty"`
	FinishInMin    json.RawMessage `json:"finish_in_min,omitempty"`
	FinishOutcome  json.RawMessage `json:"finish_outcome,omitempty"`
	Interrupt      json.RawMessage `json:"interrupt,omitempty"`
	Command        json.RawMessage `json:"command,omitempty"`
	CommandLeftMin json.RawMessage `json:"command_left_min,omitempty"`
}

func reportPauseProgress(ctx context.Context, tx pgx.Tx, s Session, in pauseProgressReport) (Session, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return s, err
	}
	if string(raw) == "{}" {
		return s, nil
	}
	var progress PauseProgress
	if err := json.Unmarshal(raw, &progress); err != nil {
		return s, workorders.Fail(400, "invalid pause planning report")
	}
	for _, text := range []*string{progress.Step, progress.NextPoint, progress.FinishOutcome, progress.Command} {
		if text != nil {
			if utf8.RuneCountInString(*text) > 240 || !agentactivity.SafeText(*text) {
				return s, workorders.Fail(400, "pause planning text must be a public label of at most 240 characters")
			}
			*text = strings.TrimSpace(*text)
		}
	}
	for _, minutes := range []*float64{progress.NextPointInMin, progress.FinishInMin, progress.CommandLeftMin} {
		if minutes != nil && (math.IsNaN(*minutes) || math.IsInf(*minutes, 0) || *minutes < 0 || *minutes > 1440) {
			return s, workorders.Fail(400, "pause planning minutes must be from 0 through 1440")
		}
	}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&progress.ReportedAt); err != nil {
		return s, err
	}
	raw, err = json.Marshal(progress)
	if err != nil {
		return s, err
	}
	return scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET pause_progress=$2::jsonb WHERE id=$1 RETURNING `+sessionColumns, s.ID, string(raw)))
}

type leavingEstimate struct {
	finish, nextPoint, commandLeft *time.Duration
	interrupt                      *bool
}

func (e leavingEstimate) canInterrupt() bool {
	return e.interrupt == nil || *e.interrupt
}

func (e leavingEstimate) levelFits(level string, budget time.Duration) bool {
	return level == "wrap_up" && e.finishFits(budget) || e.handoverFits(budget)
}

func freshPauseEstimate(s Session, now time.Time, interval time.Duration) leavingEstimate {
	fresh := func(at time.Time) bool { return !at.After(now) && now.Sub(at) <= 2*interval }
	var estimate leavingEstimate
	if progress := s.PauseProgress; progress != nil {
		// An explicit planning snapshot supersedes the legacy ready ETA, even
		// when cleared or stale. Liveness alone cannot make it fresh again.
		if fresh(progress.ReportedAt) {
			remaining := func(minutes *float64) *time.Duration {
				if minutes == nil {
					return nil
				}
				d := max(0, progress.ReportedAt.Add(time.Duration(*minutes*float64(time.Minute))).Sub(now))
				return &d
			}
			estimate.finish = remaining(progress.FinishInMin)
			estimate.nextPoint = remaining(progress.NextPointInMin)
			estimate.commandLeft = remaining(progress.CommandLeftMin)
			estimate.interrupt = progress.Interrupt
		}
	} else if s.EtaReadyAt != nil && s.EtaReportedAt != nil && fresh(*s.EtaReportedAt) {
		d := max(0, s.EtaReadyAt.Sub(now))
		estimate.finish = &d
	}
	return estimate
}

func (e leavingEstimate) finishFits(budget time.Duration) bool {
	return e.finish != nil && *e.finish >= 0 && *e.finish < 10*time.Minute && *e.finish <= budget
}

func (e leavingEstimate) handoverFits(budget time.Duration) bool {
	if e.nextPoint == nil {
		return false
	}
	wait := time.Duration(0)
	if e.commandLeft != nil && (e.interrupt == nil || !*e.interrupt) {
		wait = *e.commandLeft
	}
	return wait+*e.nextPoint+30*time.Second <= budget
}
