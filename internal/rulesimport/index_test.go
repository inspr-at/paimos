// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const indexKernel = "# Synthetic kernel\r\n\r\n## Git\r\n\r\n<!-- aeon-rule: no-force -->\r\n- 🔴 Never force-push the default branch.\r\n  Why: it rewrites shared history.\r\n\r\n- Prefer small commits.\r\n"

func TestIndexBytesMatchesImport(t *testing.T) {
	got, err := IndexBytes("docs/AGENTS-KERNEL.md", []byte(indexKernel), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.File.Kind != "kernel" || got.File.Layer != LayerCompany || got.File.Path != "docs/AGENTS-KERNEL.md" || len(got.File.SHA256) != 64 {
		t.Fatalf("file = %+v", got.File)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("rules = %+v", got.Rules)
	}
	locked, small := got.Rules[0], got.Rules[1]
	if locked.Strength != StrengthLocked || locked.Sources[0].StartLine != 5 || locked.Sources[0].EndLine != 7 || locked.Sources[0].HeadingPath != "Synthetic kernel / Git" {
		t.Fatalf("locked = %+v", locked)
	}
	if RuleKey(locked) != "no-force" || !strings.HasPrefix(RuleKey(small), "t-") || small.Sources[0].StartLine != 9 {
		t.Fatalf("keys %q %q, small %+v", RuleKey(locked), RuleKey(small), small.Sources[0])
	}

	// The same bytes through a local import give the same identities and ranges.
	dir := t.TempDir()
	path := writeDoc(t, dir, "docs/AGENTS-KERNEL.md", indexKernel)
	plan := mustBuild(t, Request{Context: ContextTemplate, Files: []string{path}})
	for _, rule := range got.Rules {
		var match *Rule
		for i := range plan.Rules {
			if plan.Rules[i].Identity == rule.Identity {
				match = &plan.Rules[i]
			}
		}
		if match == nil || match.ID != rule.ID || match.Sources[0].StartLine != rule.Sources[0].StartLine || match.Sources[0].SHA256 != rule.Sources[0].SHA256 {
			t.Fatalf("index %+v differs from import %+v", rule, match)
		}
	}
}

func TestIndexBytesRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		path string
		raw  []byte
		want error
	}{
		"nul":          {"docs/AGENTS-CORE.md", []byte("# a\n\x00"), ErrNotText},
		"not utf-8":    {"docs/AGENTS-CORE.md", []byte{'#', ' ', 0xff}, ErrNotText},
		"too large":    {"docs/AGENTS-CORE.md", []byte(strings.Repeat("a", MaxFileBytes+1)), ErrByteBound},
		"unrecognized": {"docs/AGENTS-INDEX.md", []byte("# Index\n- a\n"), ErrUnrecognizedFile},
		"empty path":   {"", []byte("# a\n"), ErrProhibitedPath},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := IndexBytes(tc.path, tc.raw, false); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if Classifies("docs/AGENTS-INDEX.md") || !Classifies("docs/AGENTS-DOMAIN-NIX.md") {
		t.Fatal("classification")
	}
}

// A private repository may mix personal and other sections in one file; the
// index keeps both instead of refusing the file as a public plan would.
func TestIndexBytesPrivateKeepsMixedFile(t *testing.T) {
	raw := []byte("# Repo\n\n## Personal\n\n- Call me Markus.\n\n## Git\n\n- Small commits.\n")
	if _, err := IndexBytes("AGENTS.md", raw, false); !errors.Is(err, ErrMixedContext) {
		t.Fatalf("public mixed file err = %v", err)
	}
	got, err := IndexBytes(filepath.ToSlash("AGENTS.md"), raw, true)
	if err != nil || len(got.Rules) != 2 {
		t.Fatalf("private mixed file = %+v, %v", got.Rules, err)
	}
}
