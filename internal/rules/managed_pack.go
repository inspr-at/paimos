// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/rules/doctrine"
)

//go:embed hard-floor-v2.1.txt
var ManagedSafetyFloor string

const ManagedSafetyFloorSHA256 = "3d1c0e8f22e2261bc7d2fa7caef924cbe34dd01bba567abf5e891ebb7724e913"

var ErrRulesPinMissing = errors.New("rules_pin_missing")
var ErrRulesPackTooLarge = errors.New("rules_pack_too_large")
var ErrRepositoryRules = errors.New("rules_repository_unavailable")

type RepositoryRules struct {
	Commit string `json:"commit,omitempty"`
	Body   string `json:"body,omitempty"`
}

// AssembleManagedPack has no clock, I/O or caller-dependent ordering. The floor
// is the only compiled prose; every other byte came from an authorized pin.
func AssembleManagedPack(m Merged, repository RepositoryRules) (string, error) {
	if len(m.PinnedFiles) == 0 {
		return "", ErrRulesPinMissing
	}
	if len(m.PinnedFiles) > doctrine.ManagedMaxFiles {
		return "", ErrRulesPackTooLarge
	}
	floorHash := sha256.Sum256([]byte(ManagedSafetyFloor))
	if len(ManagedSafetyFloor) > 2048 || hex.EncodeToString(floorHash[:]) != ManagedSafetyFloorSHA256 {
		return "", ErrRulesPinMissing
	}
	files := slices.Clone(m.PinnedFiles)
	slices.SortFunc(files, func(a, b doctrine.PinnedFile) int {
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
	var out strings.Builder
	out.WriteString(ManagedSafetyFloor)
	write := func(kind, repository, commit, name, body string) error {
		if len(body) > doctrine.ManagedFileMaxBytes && kind != "database" {
			return ErrRulesPackTooLarge
		}
		if !utf8.ValidString(body) {
			return ErrRulesPinMissing
		}
		// Only remove byte-identical, full-line floor blocks. Hashing the
		// candidate binds deduplication to the frozen floor, not similar prose.
		for start := 0; start < len(body); {
			i := strings.Index(body[start:], ManagedSafetyFloor)
			if i < 0 {
				break
			}
			i += start
			if (i == 0 || body[i-1] == '\n') && sha256.Sum256([]byte(body[i:i+len(ManagedSafetyFloor)])) == floorHash {
				body = body[:i] + body[i+len(ManagedSafetyFloor):]
				start = i
			} else {
				start = i + len(ManagedSafetyFloor)
			}
		}
		label, _ := json.Marshal(struct {
			Kind       string `json:"kind"`
			Repository string `json:"repository,omitempty"`
			Commit     string `json:"commit,omitempty"`
			Path       string `json:"path,omitempty"`
		}{kind, repository, commit, name})
		header := "\n\n--- AEON RULE FILE " + string(label) + " ---\n"
		const footer = "\n--- END AEON RULE FILE ---\n"
		if len(header)+len(body)+len(footer) > doctrine.ManagedPackMaxBytes-out.Len() {
			return ErrRulesPackTooLarge
		}
		out.WriteString(header)
		out.WriteString(body)
		out.WriteString(footer)
		return nil
	}
	previous := ""
	total := len(m.Body)
	if total > doctrine.ManagedPackMaxBytes {
		return "", ErrRulesPackTooLarge
	}
	for _, file := range files {
		if len(file.Content) > doctrine.ManagedFileMaxBytes || total > doctrine.ManagedPackMaxBytes-len(file.Content) {
			return "", ErrRulesPackTooLarge
		}
		total += len(file.Content)
		identity := file.Repository + "/" + file.Path
		if identity == previous || file.Repository == "" || len(file.Repository) > 141 || len(file.Path) > 300 || path.IsAbs(file.Path) || path.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "../") || file.Path == "." || file.Visibility != "public" && file.Visibility != "private" || !validPackSHA(file.Commit, 40) || doctrine.BlobSHA([]byte(file.Content)) != file.BlobSHA {
			return "", ErrRulesPinMissing
		}
		previous = identity
		if err := write("doctrine", file.Repository, file.Commit, file.Path, file.Content); err != nil {
			return "", err
		}
	}
	if repository.Commit != "" {
		if !validPackSHA(repository.Commit, 40) {
			return "", ErrRepositoryRules
		}
		if err := write("repository", "", repository.Commit, "AGENTS.md", repository.Body); err != nil {
			return "", err
		}
	}
	if err := write("database", "", "", "", m.Body); err != nil {
		return "", err
	}
	return out.String(), nil
}

func validPackSHA(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
