// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"fmt"

	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/jackc/pgx/v5"
)

// rejectDoctrineCopy refuses a publication that would put a git-backed rule
// on the Aeon channel as well. The message points at Propose a change, which
// is how doctrine is edited (a pull request, AEON-319).
func rejectDoctrineCopy(ctx context.Context, tx pgx.Tx, rules []Rule) error {
	cat, err := doctrine.LoadCatalog(ctx, tx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		hit, ok := cat.Match(rule.Text, rule.Source.Identity, rule.Source.Reference)
		if !ok {
			continue
		}
		return fail(409, "doctrine_duplicate", fmt.Sprintf("Rule %q duplicates the doctrine rule %s. Propose a change instead of publishing a second copy.", rule.Identity, hit.Identity))
	}
	return nil
}
