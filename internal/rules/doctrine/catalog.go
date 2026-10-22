// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/rulesimport"
	"github.com/jackc/pgx/v5"
)

// Release is one pinned doctrine repository. It is a pointer: repository, ref
// and commit, never the doctrine text.
type Release struct {
	Repository string
	Ref        string
	Commit     string
	Stale      bool
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
	// ManagedFiles replaces pointers only for the opt-in agentd envelope.
	ManagedFiles bool
	Files        []PinnedFile
}

const ManagedPackMaxBytes = 128000
const ManagedFileMaxBytes = 64000
const ManagedMaxFiles = 64

// PinnedFile contains exact cache bytes, authorized for this request's tenant.
type PinnedFile struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	Visibility string `json:"visibility"`
	BlobSHA    string `json:"blob_sha"`
	Content    string `json:"content"`
}

var ErrPinMissing = errors.New("rules_pin_missing")
var ErrPackTooLarge = errors.New("rules_pack_too_large")

// LoadPinnedCatalog applies the same host grant checks as legacy delivery,
// then reads only the current pin. Public-main observations are never prose.
func LoadPinnedCatalog(ctx context.Context, tx pgx.Tx) (Catalog, error) {
	return loadCatalog(ctx, tx, true)
}

// LoadCatalog reads the pinned sources and the rules indexed at those commits.
// A source that is not indexed yet still contributes its pin.
func LoadCatalog(ctx context.Context, tx pgx.Tx) (Catalog, error) {
	return loadCatalog(ctx, tx, false)
}

func loadCatalog(ctx context.Context, tx pgx.Tx, pinned bool) (Catalog, error) {
	sources, err := listSources(ctx, tx)
	if err != nil {
		return Catalog{}, err
	}
	cat := Catalog{Releases: make([]Release, 0, len(sources)), Rules: []Indexed{}, ManagedFiles: pinned}
	if pinned && len(sources) == 0 {
		return Catalog{}, ErrPinMissing
	}
	totalBytes, totalFiles := 0, 0
	for _, s := range sources {
		var pinnedPaths []string
		stale := false
		if s.CredentialRef != "" {
			var tenantID string
			if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
				return Catalog{}, err
			}
			credentials, _ := ctx.Value(catalogCredentialsKey{}).(Credentials)
			if err := credentials.authorizeSource(s, tenantID); err != nil {
				if errors.Is(err, errMirrorCommitChanged) && s.IndexedAt != nil {
					// The host export advances independently of this indexed pin.
					// Its grant and read-only marker remain valid; deliver only
					// the pinned cache, never bytes from the new export.
					stale = true
				} else {
					// Fail closed before exposing an inaccessible source's cache
					// or matching identity, including revoked or invalid mirrors.
					return Catalog{}, err
				}
			}
		}
		cat.Releases = append(cat.Releases, Release{Repository: s.Repository, Ref: s.Ref, Commit: s.Commit, Stale: stale})
		if pinned {
			if s.IndexedAt == nil || !shaPattern.MatchString(s.Commit) {
				return Catalog{}, ErrPinMissing
			}
			var size int
			pinnedPaths, size, err = pinnedKernelPaths(ctx, tx, s)
			if err != nil {
				return Catalog{}, err
			}
			totalFiles += len(pinnedPaths)
			totalBytes += size
			if totalFiles > ManagedMaxFiles || totalBytes > ManagedPackMaxBytes {
				return Catalog{}, ErrPackTooLarge
			}
		}
		if s.IndexedAt == nil {
			continue
		}
		files, err := cachedFilesForDelivery(ctx, tx, s, pinnedPaths)
		if err != nil {
			return Catalog{}, err
		}
		if pinned && len(files) != len(pinnedPaths) {
			return Catalog{}, ErrPinMissing
		}
		for _, file := range Render(s.Repository, s.Commit, s.Visibility == "private", files) {
			for _, rule := range file.Rules {
				if rule.Identity == "" {
					continue
				}
				cat.Rules = append(cat.Rules, Indexed{Identity: rule.Identity, Key: rule.Key, Text: rule.Text})
			}
		}
		if pinned {
			for _, f := range files {
				if !strings.HasSuffix(f.Path, ".md") {
					continue
				}
				if !utf8.Valid(f.Content) || BlobSHA(f.Content) != f.BlobSHA {
					return Catalog{}, ErrPinMissing
				}
				cat.Files = append(cat.Files, PinnedFile{Repository: s.Repository, Commit: s.Commit, Path: f.Path, Visibility: s.Visibility, BlobSHA: f.BlobSHA, Content: string(f.Content)})
			}
		}
	}
	slices.SortFunc(cat.Releases, func(a, b Release) int { return strings.Compare(a.Repository, b.Repository) })
	slices.SortFunc(cat.Rules, func(a, b Indexed) int { return strings.Compare(a.Identity, b.Identity) })
	slices.SortFunc(cat.Files, func(a, b PinnedFile) int {
		if a.Visibility != b.Visibility {
			if a.Visibility == "public" {
				return -1
			}
			return 1
		}
		if d := strings.Compare(a.Repository, b.Repository); d != 0 {
			return d
		}
		return strings.Compare(a.Path, b.Path)
	})
	return cat, nil
}

// Select metadata before loading bytes: on-demand packs can exceed the managed
// file budget, and person/role files have no audience binding on this channel.
func pinnedKernelPaths(ctx context.Context, tx pgx.Tx, s Source) ([]string, int, error) {
	rows, err := tx.Query(ctx, `SELECT path,octet_length(content) FROM doctrine_cache WHERE source_id=$1 AND commit_sha=$2 AND path LIKE '%.md' ORDER BY path LIMIT 65`, s.ID, s.Commit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	selected := []string{}
	matched := make([]bool, len(s.Paths))
	count, total := 0, 0
	for rows.Next() {
		var name string
		var size int
		if err := rows.Scan(&name, &size); err != nil {
			return nil, 0, err
		}
		count++
		if count > MaxFiles {
			return nil, 0, ErrPackTooLarge
		}
		for i, pattern := range s.Paths {
			if ok, _ := path.Match(pattern, name); ok {
				matched[i] = true
			}
		}
		file, err := rulesimport.Classify(name, size)
		if err != nil || file.Kind != "kernel" || file.Layer != rulesimport.LayerCompany || file.Placement != rulesimport.PlacementAlwaysOn {
			continue
		}
		if size > ManagedFileMaxBytes || size > ManagedPackMaxBytes-total {
			return nil, 0, ErrPackTooLarge
		}
		total += size
		selected = append(selected, name)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if count == 0 || slices.Contains(matched, false) {
		return nil, 0, ErrPinMissing
	}
	return selected, total, nil
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
		status := ""
		if rel.Stale {
			status = " (stale: host mirror advanced; serving the indexed pin)"
		}
		if rel.Ref != "" {
			fmt.Fprintf(&b, "- %s@%s %s%s\n", rel.Repository, rel.Ref, rel.Commit, status)
		} else {
			fmt.Fprintf(&b, "- %s@%s%s\n", rel.Repository, rel.Commit, status)
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
