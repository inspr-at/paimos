// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"fmt"
	"net/http"

	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/jackc/pgx/v5"
)

type doctrineCatalogKey struct{}
type requestDoctrineCatalog struct {
	loaded bool
	cat    doctrine.Catalog
	err    error
}

// The cache belongs to one request/transaction only. Every subsequent request
// must recheck current credential grants, even if a previous catalog was ready.
func withDoctrineCatalog(ctx context.Context) context.Context {
	return context.WithValue(ctx, doctrineCatalogKey{}, &requestDoctrineCatalog{})
}

func loadDoctrineCatalog(ctx context.Context, tx pgx.Tx) (doctrine.Catalog, error) {
	cache, _ := ctx.Value(doctrineCatalogKey{}).(*requestDoctrineCatalog)
	if cache == nil {
		cache = &requestDoctrineCatalog{}
	}
	if !cache.loaded {
		cache.cat, cache.err = doctrine.LoadCatalog(ctx, tx)
		if cache.err != nil {
			// Do not expose credential paths, grants, repository identities or
			// underlying errors from an inaccessible catalog.
			cache.err = fail(http.StatusServiceUnavailable, "doctrine_unavailable", "Doctrine catalog unavailable; check source credential grants and server configuration.")
		}
		cache.loaded = true
	}
	return cache.cat, cache.err
}

// rejectDoctrineCopy refuses a publication that would put a git-backed rule
// on the Aeon channel as well. The message points at Propose a change, which
// is how doctrine is edited (a pull request, AEON-319).
func rejectDoctrineCopy(ctx context.Context, tx pgx.Tx, rules []Rule) error {
	cat, err := loadDoctrineCatalog(ctx, tx)
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
