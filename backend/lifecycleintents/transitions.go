// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"

	"github.com/inspr-at/paimos/backend/auth"
)

func (s *Service) Claim(ctx context.Context, p auth.Principal, project int64, runtimeID, lease string) (*Intent, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.begin(ctx, p, project, auth.PrincipalAPIKey)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	runtime, err := s.runtime(ctx, tx, project, runtimeID, &p, lease, true)
	if err != nil {
		return nil, err
	}
	if err = s.expireClaims(ctx, tx, p, project); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM lifecycle_intents WHERE runtime_id=? AND state IN ('requested','claimed','executing') ORDER BY CASE WHEN state='requested' THEN 1 ELSE 0 END,created_at,id LIMIT 33`, runtimeID)
	if err != nil {
		return nil, ErrStorage
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, ErrStorage
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, ErrStorage
	}
	for _, id := range ids {
		in, e := loadIntent(ctx, tx, project, id)
		if e != nil {
			return nil, e
		}
		if e = s.refresh(ctx, tx, p, &in); e != nil {
			return nil, e
		}
		if terminal(in.State) {
			continue
		}
		// A repeated claim returns the original work, including executing state.
		// It never resets execution or hands an abandoned claim to another daemon.
		if in.State == "requested" {
			if e = s.validateTargetForOutcome(ctx, tx, project, in.Request, runtime, "", in.ID); e != nil {
				if e == ErrStorage {
					return nil, e
				}
				if e = s.change(ctx, tx, p, &in, "failed", "ownership_lost", ""); e != nil {
					return nil, e
				}
				continue
			}
			if e = s.change(ctx, tx, p, &in, "claimed", "", ""); e != nil {
				return nil, e
			}
		}
		if tx.Commit() != nil {
			return nil, ErrStorage
		}
		return &in, nil
	}
	if tx.Commit() != nil {
		return nil, ErrStorage
	}
	return nil, nil
}
func (t Transition) validate() error {
	if !validID(t.RuntimeID) || !validID(t.RuntimeGeneration) || t.ExpectedRevision < 1 {
		return ErrInvalid
	}
	switch t.State {
	case "executing":
		if t.Reason != "" || t.ResultSessionID != "" {
			return ErrInvalid
		}
	case "completed":
		if t.Reason != "applied" {
			return ErrInvalid
		}
	case "failed":
		if t.ResultSessionID != "" {
			return ErrInvalid
		}
		switch t.Reason {
		case "failed", "unsupported", "ownership_lost", "outcome_unknown":
		default:
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if t.ResultSessionID != "" && !validID(t.ResultSessionID) {
		return ErrInvalid
	}
	if t.Readiness != nil {
		if t.State != "completed" || t.ResultSessionID != "" {
			return ErrInvalid
		}
		return validateReadinessReport(t.Readiness)
	}
	return nil
}
func (s *Service) Transition(ctx context.Context, p auth.Principal, project int64, id, lease string, t Transition) (Intent, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.begin(ctx, p, project, auth.PrincipalAPIKey)
	if err != nil {
		return Intent{}, err
	}
	defer tx.Rollback()
	if err = t.validate(); err != nil {
		return Intent{}, err
	}
	in, err := loadIntent(ctx, tx, project, id)
	if err != nil {
		return Intent{}, err
	}
	if in.Request.RuntimeID != t.RuntimeID || in.Request.RuntimeGeneration != t.RuntimeGeneration {
		return Intent{}, ErrUnavailable
	}
	// Only a readiness intent may carry an observation, and a readiness intent
	// can only complete by carrying one. A report attached to any other
	// operation, or a bare readiness completion, is refused outright.
	readinessIntent := in.Request.Operation == "readiness"
	if !readinessIntent && t.Readiness != nil {
		return Intent{}, ErrInvalid
	}
	if readinessIntent && t.State == "completed" && t.Readiness == nil {
		return Intent{}, ErrInvalid
	}
	// A retired generation may read its exact terminal replay with its original
	// proof, but can never claim or alter an outcome after lease expiry.
	runtime, err := s.runtime(ctx, tx, project, t.RuntimeID, &p, lease, !terminal(in.State))
	if err != nil {
		return Intent{}, err
	}
	if err = s.refresh(ctx, tx, p, &in); err != nil {
		return Intent{}, err
	}
	if in.State == t.State && in.Revision == t.ExpectedRevision+1 && in.Reason == t.Reason && in.ResultSessionID == t.ResultSessionID {
		if s.creator(ctx, tx, in) != nil {
			return Intent{}, ErrUnavailable
		}
		if tx.Commit() != nil {
			return Intent{}, ErrStorage
		}
		return in, nil
	}
	if terminal(in.State) {
		if tx.Commit() != nil {
			return Intent{}, ErrStorage
		}
		return Intent{}, ErrConflict
	}
	if in.Revision != t.ExpectedRevision {
		return Intent{}, ErrConflict
	}
	if !((in.State == "claimed" && (t.State == "executing" || t.State == "failed")) || (in.State == "executing" && (t.State == "completed" || t.State == "failed"))) {
		return Intent{}, ErrConflict
	}
	// Recheck the exact current authority immediately before side effects are
	// permitted and before their outcome can become authoritative.
	if t.State != "failed" {
		if err = s.validateTargetForOutcome(ctx, tx, project, in.Request, runtime, t.ResultSessionID, in.ID); err != nil {
			if err == ErrStorage {
				return Intent{}, err
			}
			if err = s.change(ctx, tx, p, &in, "failed", "ownership_lost", ""); err != nil {
				return Intent{}, err
			}
			if tx.Commit() != nil {
				return Intent{}, ErrStorage
			}
			return Intent{}, ErrUnavailable
		}
	}
	if t.State == "completed" {
		if err = s.completeEffect(ctx, tx, in, runtime, t); err != nil {
			return Intent{}, err
		}
	}
	if err = s.change(ctx, tx, p, &in, t.State, t.Reason, t.ResultSessionID); err != nil {
		return Intent{}, err
	}
	if tx.Commit() != nil {
		return Intent{}, ErrStorage
	}
	return in, nil
}
