// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"net/http"
	"slices"

	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// channelRelease is a doctrine pin. It carries no doctrine text.
type channelRelease struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
	Commit     string `json:"commit"`
}

// channelDuplicate is a published Aeon rule that copies a doctrine rule.
type channelDuplicate struct {
	Identity string `json:"identity"`
	Doctrine string `json:"doctrine"`
}

// channelView is GET /api/rules/channels. The session file omits these copies;
// the list is how doctor reports that a rule would be served twice.
type channelView struct {
	Releases   []channelRelease   `json:"releases"`
	Duplicates []channelDuplicate `json:"duplicates"`
}

const maxChannelDuplicates = 100

func (m *Module) channels(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	cat, err := doctrine.LoadCatalog(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	out := channelView{Releases: make([]channelRelease, 0, len(cat.Releases)), Duplicates: []channelDuplicate{}}
	for _, rel := range cat.Releases {
		out.Releases = append(out.Releases, channelRelease{Repository: rel.Repository, Ref: rel.Ref, Commit: rel.Commit})
	}
	sets, err := allSets(r.Context(), tx, "")
	if err != nil {
		return nil, err
	}
	var dups []channelDuplicate
	for _, set := range sets {
		if set.PublishedVersion == "" {
			continue
		}
		snap, err := loadVersion(r.Context(), tx, set.ID, set.PublishedVersion)
		if err != nil {
			return nil, err
		}
		for _, rule := range snap.Rules {
			hit, ok := cat.Match(rule.Text, rule.Source.Identity, rule.Source.Reference)
			if !ok {
				continue
			}
			dups = append(dups, channelDuplicate{Identity: rule.Identity, Doctrine: hit.Identity})
		}
	}
	slices.SortFunc(dups, func(a, b channelDuplicate) int {
		if a.Identity != b.Identity {
			if a.Identity < b.Identity {
				return -1
			}
			return 1
		}
		if a.Doctrine < b.Doctrine {
			return -1
		}
		if a.Doctrine > b.Doctrine {
			return 1
		}
		return 0
	})
	if len(dups) > maxChannelDuplicates {
		dups = dups[:maxChannelDuplicates]
	}
	out.Duplicates = dups
	return out, nil
}
