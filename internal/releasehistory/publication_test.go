// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Risk: a capped display changelog, merge-only PR key or reserved tag changes
// the actual publication membership. Use a real graph, including a side branch.
func TestPublicationUsesCompleteTagAncestry(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	const old = "261016060843.0.0"
	const current = "261017115202.0.0"
	if err := os.WriteFile(filepath.Join(dir, "version.json"), []byte(`{"version":"`+current+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "version.json")
	git("commit", "-qm", "AEON-1: old work")
	oldSHA := git("rev-parse", "HEAD")
	git("tag", "v"+old)
	git("checkout", "-qb", "work/aeon-2")
	git("commit", "--allow-empty", "-qm", "implementation without a key")
	side := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("merge", "--no-ff", "-qm", "Merge pull request #2 from example/work/aeon-2\n\nShip AEON-2\n\nRefs AEON-999 only", "work/aeon-2")
	merge := git("rev-parse", "HEAD")
	git("tag", "v"+current)
	at := time.Now().UTC()
	h := History{Product: "PAIMOS AEON", Repository: "inspr-at/paimos", Releases: []Release{
		{Version: current, Tag: "v" + current, State: StatePublished, TaggedAt: &at, Evidence: Evidence{SourceCommit: merge}, ChangesOmitted: 123},
		{Version: "261017100000.0.0", Tag: "v261017100000.0.0", State: StateReserved},
		{Version: old, Tag: "v" + old, State: StatePublished},
	}}
	p, err := BuildPublication(t.Context(), Options{Repo: dir}, h)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.PreviousTag != "v"+old || !slices.Contains(p.Commits, merge) || !slices.Contains(p.Commits, side) || slices.Contains(p.Commits, oldSHA) || p.Tickets["AEON-2"] != merge || p.Tickets["AEON-999"] != "" {
		t.Fatalf("wrong publication: %+v", p)
	}
	// A follow-up HEAD must not impersonate the older published tag.
	git("commit", "--allow-empty", "-qm", "AEON-2: follow-up")
	p, err = BuildPublication(t.Context(), Options{Repo: dir}, h)
	if err != nil || p != nil {
		t.Fatalf("untagged HEAD publication: %+v %v", p, err)
	}
}
