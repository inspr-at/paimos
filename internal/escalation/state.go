// SPDX-License-Identifier: AGPL-3.0-only
// Package escalation implements tier A of AEON-729. It neither launches a
// process nor approves review, merge or deployment. AEON-601 uses ReserveTx in
// its final retry transaction and rolls it back when admission fails.
package escalation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const MaxAttempts = 2
const MaxCostMicros int64 = 20_000_000
const FixRoundThreshold = 3

type checkEvidence struct {
	Attempts []string `json:"attempts"`
}
type State struct {
	RouteRole       string         `json:"route_role,omitempty"`
	RouteArea       string         `json:"route_area,omitempty"`
	PrefsRevision   int64          `json:"prefs_revision,omitempty"`
	RouteBlock      string         `json:"route_block_reason,omitempty"`
	FindingRounds   map[string]int `json:"finding_rounds,omitempty"`
	WaitReason      string         `json:"wait_reason,omitempty"`
	EpisodeID       string         `json:"episode_id"`
	Revision        int64          `json:"revision"`
	Status          string         `json:"status"`
	Reason          string         `json:"reason"`
	FixRounds       int            `json:"fix_rounds"`
	Attempts        int            `json:"attempts"`
	HeldCost        int64          `json:"held_cost_micros"`
	MaxAttempts     int            `json:"max_attempts"`
	MaxCost         int64          `json:"max_cost_micros"`
	PlannedProfile  string         `json:"planned_profile_id,omitempty"`
	QuestionID      string         `json:"question_id,omitempty"`
	QuestionPending bool           `json:"question_pending,omitempty"`
	LastOutcomeID   string         `json:"last_outcome_id,omitempty"`
	UsedProfiles    []string       `json:"used_profile_ids,omitempty"`
	// Bounded detector internals are withheld from the public projection.
	LastFixRound    int                      `json:"last_fix_round,omitempty"`
	LastReviewRound int                      `json:"last_review_round,omitempty"`
	LastFinding     string                   `json:"last_finding,omitempty"`
	Checks          map[string]checkEvidence `json:"checks,omitempty"`
}

type Signal struct {
	ReviewID            string `json:"review_id"`
	Verdict             string `json:"verdict"`
	Round               int    `json:"round"`
	FindingsFingerprint string `json:"findings_fingerprint"`
	Result              string `json:"result"`
	Repo                string `json:"repo"`
	Number              int    `json:"number"`
	Name                string `json:"name"`
	AttemptID           string `json:"attempt_id"`
}

// apply uses explicit round/execution identities, never summary similarity or
// elapsed-time guesses. Inputs have already passed outcome payload validation.
func (s *State) apply(kind string, payload json.RawMessage) error {
	var in Signal
	if err := json.Unmarshal(payload, &in); err != nil {
		return err
	}
	switch kind {
	case "review_verdict":
		if in.Verdict == "ok" {
			s.Status = "resolved"
			s.Reason = "recorded_ok"
			s.PlannedProfile = ""
			s.WaitReason = ""
			s.QuestionPending = false
			return nil
		}
		if in.Round <= s.LastReviewRound {
			return nil
		}
		if in.FindingsFingerprint != "" {
			if s.FindingRounds == nil {
				s.FindingRounds = map[string]int{}
			}
			if s.FindingRounds[in.FindingsFingerprint] > 0 {
				s.stuck("repeated_findings")
			}
			if len(s.FindingRounds) >= 32 && s.FindingRounds[in.FindingsFingerprint] == 0 {
				s.stuck("evidence_limit")
			} else {
				s.FindingRounds[in.FindingsFingerprint] = in.Round
			}
		}
		s.LastReviewRound = in.Round
		s.LastFinding = in.FindingsFingerprint
	case "fix_round":
		if in.Round <= s.LastFixRound {
			return nil
		}
		s.LastFixRound = in.Round
		s.FixRounds++
		if s.FixRounds >= FixRoundThreshold {
			s.stuck("failed_fix_rounds")
		}
	case "ci_result":
		if in.Name == "" || in.AttemptID == "" {
			return nil
		}
		keyBytes, _ := json.Marshal([]any{in.Repo, in.Number, in.Name})
		sum := sha256.Sum256(keyBytes)
		key := hex.EncodeToString(sum[:])
		if s.Checks == nil {
			s.Checks = map[string]checkEvidence{}
		}
		if in.Result == "pass" {
			delete(s.Checks, key)
			return nil
		}
		evidence := s.Checks[key]
		for _, id := range evidence.Attempts {
			if id == in.AttemptID {
				return nil
			}
		}
		if len(s.Checks) >= 32 && len(evidence.Attempts) == 0 {
			s.stuck("evidence_limit")
			return nil
		}
		if len(evidence.Attempts) < 2 {
			evidence.Attempts = append(evidence.Attempts, in.AttemptID)
		}
		s.Checks[key] = evidence
		if len(evidence.Attempts) >= 2 {
			s.stuck("repeated_ci_failure")
		}
	}
	return nil
}
func (s *State) stuck(reason string) {
	if s.Status == "observing" {
		s.Status = "stuck"
		s.Reason = reason
	}
}

// Public excludes detector fingerprints and account details.
func (s State) Public() map[string]any {
	out := map[string]any{"episode_id": s.EpisodeID, "revision": s.Revision, "status": s.Status, "reason": s.Reason, "fix_rounds": s.FixRounds, "attempts": s.Attempts, "held_cost_micros": s.HeldCost, "max_attempts": MaxAttempts, "max_cost_micros": MaxCostMicros, "last_outcome_id": s.LastOutcomeID}
	if s.RouteRole != "" {
		out["route_role"] = s.RouteRole
		out["route_area"] = s.RouteArea
		out["prefs_revision"] = s.PrefsRevision
	}
	if s.RouteBlock != "" {
		out["route_block_reason"] = s.RouteBlock
	}
	if s.WaitReason != "" {
		out["wait_reason"] = s.WaitReason
	}
	if s.PlannedProfile != "" {
		out["planned_profile_id"] = s.PlannedProfile
	}
	if s.QuestionID != "" {
		out["question_id"] = s.QuestionID
	}
	if s.QuestionPending {
		out["question_pending"] = true
	}
	if len(s.UsedProfiles) > 0 {
		out["used_profile_ids"] = s.UsedProfiles
	}
	return out
}
