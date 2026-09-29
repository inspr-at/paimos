// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

// A missing, revoked or incomplete private index cannot prove an outgoing
// public edit safe. Load only this tenant's authorized cached doctrine; never
// fetch private text with the public installation token.
func (m *Module) privateTexts(ctx context.Context, tx pgx.Tx, actor tenant.Principal) ([]string, error) {
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
		for _, skip := range s.Skipped {
			if skip.Reason != "not a doctrine rule file" {
				return nil, unavailable()
			}
		}
		files, err := cachedFiles(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		texts := []string{}
		for _, file := range Render(s.Repository, s.Commit, true, files) {
			if file.Problem != "" || file.Sidecar != nil && file.Sidecar.Problem != "" {
				return nil, unavailable()
			}
			for _, rule := range file.Rules {
				texts = append(texts, rule.Text)
			}
		}
		if len(texts) == 0 {
			return nil, unavailable()
		}
		// Include file/set TLDRs and unmatched rule entries too, not just what the
		// current renderer attaches to a rule. Reject ambiguous YAML documents.
		for _, file := range files {
			if !strings.HasSuffix(file.Path, ".tldr.yaml") {
				continue
			}
			var side sidecarFile
			var extra any
			decoder := yaml.NewDecoder(bytes.NewReader(file.Content))
			decoder.KnownFields(true)
			if decoder.Decode(&side) != nil || decoder.Decode(&extra) != io.EOF {
				return nil, unavailable()
			}
			if side.File != nil {
				texts = append(texts, side.File.EN, side.File.DE)
			}
			for _, group := range []map[string]sidecarText{side.Sets, side.Rules} {
				for _, entry := range group {
					texts = append(texts, entry.EN, entry.DE)
				}
			}
		}
		return texts, nil
	}
	return nil, unavailable()
}
