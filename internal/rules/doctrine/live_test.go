// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"os"
	"strings"
	"testing"
)

// TestLivePublicDoctrine reads the public atelier at the release nixcfg pins
// (2026-09-29). It needs the network, so it runs only with
// AEON_DOCTRINE_LIVE=1.
func TestLivePublicDoctrine(t *testing.T) {
	if os.Getenv("AEON_DOCTRINE_LIVE") != "1" {
		t.Skip("set AEON_DOCTRINE_LIVE=1 to read github.com/inspr-at/inspr-modules")
	}
	const repo, tag, pinned = "inspr-at/inspr-modules", "v260922101217.0.0", "21b814057825c06b7f1e93f9deacdb4c549e11c6"
	gh := &GitHub{}
	commit, err := gh.Commit(t.Context(), repo, tag)
	if err != nil || commit.SHA != pinned {
		t.Fatalf("commit %+v %v", commit, err)
	}
	files, skipped, err := Fetch(t.Context(), gh, repo, commit.SHA, DefaultPaths)
	if err != nil {
		t.Fatal(err)
	}
	views := Render(repo, commit.SHA, false, files)
	rules := 0
	for _, v := range views {
		if v.Problem != "" {
			t.Errorf("%s: %s", v.Path, v.Problem)
		}
		var content string
		for _, f := range files {
			if f.Path == v.Path {
				content = string(f.Content)
			}
		}
		for _, r := range v.Rules {
			if !strings.Contains(content, r.Source) || r.StartLine < 1 {
				t.Errorf("%s L%d: source is not the exact bytes", v.Path, r.StartLine)
			}
		}
		rules += len(v.Rules)
		t.Logf("%s: %d rules, %d sets", v.Path, len(v.Rules), len(v.Sets))
	}
	t.Logf("%d files, %d rules, skipped %+v", len(views), rules, skipped)
	if len(views) == 0 || rules == 0 {
		t.Fatal("no doctrine indexed")
	}
}
