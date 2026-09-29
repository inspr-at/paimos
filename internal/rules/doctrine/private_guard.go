// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// A missing, revoked or incomplete private guard cannot prove an outgoing
// public edit safe. The guard is a hash corpus of the full private tree at
// the pinned commit, not the path-filtered rule index. Load only this
// tenant's authorized cache; never fetch private text with the public
// installation token, and never return the corpus to the UI.
func (m *Module) privateGuard(ctx context.Context, tx pgx.Tx, actor tenant.Principal) (*guardCorpus, error) {
	unavailable := func() error {
		return fail(422, "private_index_unavailable", "Public proposals require an authorized, successfully indexed private doctrine source. Restore its index before proposing.")
	}
	sources, err := listSources(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, s := range sources {
		if s.Repository != privateRepository {
			continue
		}
		if s.Visibility != "private" || s.IndexedAt == nil || s.IndexError != "" || s.CredentialRef == "" || m.credentials.authorize(s.CredentialRef, actor.TenantID, s.Repository) != nil {
			return nil, unavailable()
		}
		raw, err := loadGuardCorpus(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, unavailable()
		}
		corpus, err := unmarshalGuard(raw)
		if err != nil || corpus.empty() {
			return nil, unavailable()
		}
		return corpus, nil
	}
	return nil, unavailable()
}
