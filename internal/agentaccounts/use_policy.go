// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/jackc/pgx/v5"
)

const ContextSkipReason = "no account allowed for this project's context"

func applyUse(ctx context.Context, tx pgx.Tx, accounts []Account, projectID string) ([]Account, error) {
	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = a.ID
	}
	allowed, err := accountuse.AllowedIDs(ctx, tx, ids, projectID)
	if err != nil {
		return nil, err
	}
	kept := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if allowed[a.ID] {
			kept = append(kept, a)
		}
	}
	return kept, nil
}

// ProjectDailySnapshot creates a decision-only copy. Accounting, the plan
// projection and all saved usage retain every owned account.
func ProjectDailySnapshot(ctx context.Context, tx pgx.Tx, s agentplan.Snapshot, projectID string) (agentplan.Snapshot, map[string]bool, error) {
	ids := []string{}
	for _, state := range s.DailyState {
		for _, a := range state.Accounts {
			ids = append(ids, a.AccountID)
		}
	}
	allowed, err := accountuse.AllowedIDs(ctx, tx, ids, projectID)
	if err != nil {
		return s, nil, err
	}
	out := s
	out.DailyState = make(map[string]agentplan.DailyState, len(s.DailyState))
	denied := map[string]bool{}
	for harness, state := range s.DailyState {
		accounts := []agentplan.DailyAccount{}
		for _, a := range state.Accounts {
			if allowed[a.AccountID] {
				accounts = append(accounts, a)
			}
		}
		denied[harness] = len(state.Accounts) > 0 && len(accounts) == 0
		out.DailyState[harness] = agentplan.SummarizeDaily(accounts)
	}
	return out, denied, nil
}
