// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Release is one pinned doctrine repository. It is a pointer: repository, ref
// and commit, never the doctrine text.
type Release struct {
	Repository string
	Ref        string
	Commit     string
}

// Indexed is one git-backed rule from the AEON-318 index. Identity is
// repository/path#anchor.
type Indexed struct {
	Identity string
	Key      string
	Text     string
}

// Catalog is the workspace's pinned doctrine, used to keep each rule on one
// channel (AEON-320). Doctrine text stays here so the session file can name
// the release and omit the rules.
type Catalog struct {
	Releases []Release
	Rules    []Indexed
}

// LoadCatalog reads the pinned sources and the rules indexed at those commits.
// A source that is not indexed yet still contributes its pin.
func LoadCatalog(ctx context.Context, tx pgx.Tx) (Catalog, error) {
	sources, err := listSources(ctx, tx)
	if err != nil {
		return Catalog{}, err
	}
	cat := Catalog{Releases: make([]Release, 0, len(sources)), Rules: []Indexed{}}
	for _, s := range sources {
		if s.CredentialRef != "" {
			var tenantID string
			if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
				return Catalog{}, err
			}
			credentials, _ := ctx.Value(catalogCredentialsKey{}).(Credentials)
			if err := credentials.authorizeSource(s, tenantID); err != nil {
				// Fail closed before reading the cache or exposing a matching
				// identity. Publication cannot certify an inaccessible source.
				return Catalog{}, err
			}
		}
		cat.Releases = append(cat.Releases, Release{Repository: s.Repository, Ref: s.Ref, Commit: s.Commit})
		if s.IndexedAt == nil {
			continue
		}
		files, err := cachedFiles(ctx, tx, s)
		if err != nil {
			return Catalog{}, err
		}
		for _, file := range Render(s.Repository, s.Commit, s.Visibility == "private", files) {
			for _, rule := range file.Rules {
				if rule.Identity == "" {
					continue
				}
				cat.Rules = append(cat.Rules, Indexed{Identity: rule.Identity, Key: rule.Key, Text: rule.Text})
			}
		}
	}
	slices.SortFunc(cat.Releases, func(a, b Release) int { return strings.Compare(a.Repository, b.Repository) })
	slices.SortFunc(cat.Rules, func(a, b Indexed) int { return strings.Compare(a.Identity, b.Identity) })
	return cat, nil
}

// FloorPointer keeps a doctrine-backed floor in the existing bounded floor
// contract without copying doctrine text into a session, cache or bootstrap.
func (c Catalog) FloorPointer(identity string, rule Indexed) string {
	for _, rel := range c.Releases {
		if strings.HasPrefix(rule.Identity, rel.Repository+"/") && shaPattern.MatchString(rel.Commit) {
			return fmt.Sprintf("- [%s] Keep the harness safety floor %s at commit %s in force.\n", identity, rule.Identity, rel.Commit)
		}
	}
	return ""
}

// Pointer is the session-file note that names the expected doctrine release.
// It contains no rule text. An empty catalog returns an empty string so a
// workspace without doctrine keeps the previous session file bytes.
func (c Catalog) Pointer() string {
	if len(c.Releases) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Doctrine (harness channel, not repeated here):\n")
	for _, rel := range c.Releases {
		if rel.Ref != "" {
			fmt.Fprintf(&b, "- %s@%s %s\n", rel.Repository, rel.Ref, rel.Commit)
		} else {
			fmt.Fprintf(&b, "- %s@%s\n", rel.Repository, rel.Commit)
		}
	}
	b.WriteByte('\n')
	return b.String()
}

// Match reports the doctrine rule an Aeon rule copies. A copy is the same
// trimmed text, or a source identity or reference equal to the AEON-318
// identity (repository/path#anchor). The lowest identity wins when several match.
func (c Catalog) Match(text, sourceIdentity, sourceReference string) (Indexed, bool) {
	text = strings.TrimSpace(text)
	sourceIdentity = strings.TrimSpace(sourceIdentity)
	sourceReference = strings.TrimSpace(sourceReference)
	var hit Indexed
	found := false
	for _, rule := range c.Rules {
		if rule.Identity == "" {
			continue
		}
		copied := (sourceIdentity != "" && sourceIdentity == rule.Identity) ||
			(sourceReference != "" && (sourceReference == rule.Identity || sourceReference == "doctrine:"+rule.Identity)) ||
			(text != "" && text == strings.TrimSpace(rule.Text))
		if !copied {
			continue
		}
		if !found || rule.Identity < hit.Identity {
			hit = rule
			found = true
		}
	}
	return hit, found
}
