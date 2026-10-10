// SPDX-License-Identifier: AGPL-3.0-only

package readmelayout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outboundCloser is the last sentence of the pinned server-egress inventory.
// Feature prose appended after it is the AEON-986 hotspot this test rejects.
const outboundCloser = "it does not claim to simulate every optional integration or arbitrary elapsed time."

func TestReadmeCarriesNoFeatureProse(t *testing.T) {
	root := moduleRoot(t)
	readme := readText(t, filepath.Join(root, "README.md"))
	_, after, found := strings.Cut(readme, outboundCloser)
	if !found {
		t.Fatal("outbound inventory closing sentence missing")
	}
	if rest := strings.TrimSpace(after); rest != "" {
		t.Fatalf("README carries feature prose after the outbound inventory: %q", firstLine(rest))
	}
	for _, marker := range []string{
		"AEON-417 B adds an external **shadow authority**",
		"The Decision Desk UI (AEON-567) lives at `/decision-desk`",
	} {
		if strings.Contains(readme, marker) {
			t.Fatalf("README still carries feature prose %q", marker)
		}
	}
	assertFeature(t, root, "docs/features/ci-shadow-authority.md", []string{
		"AEON-417 B adds an external **shadow authority**",
		"There is no check writer or activation flag.",
		"missing images, recipes or admission refuse execution.",
	})
	assertFeature(t, root, "docs/features/decision-desk.md", []string{
		"The Decision Desk UI (AEON-567) lives at `/decision-desk`",
		"Decision Desk question groundwork (AEON-562)",
		"`source_handover_id` remains unavailable pending its verified ask-source adapter.",
	})
}

func assertFeature(t *testing.T, root, rel string, markers []string) {
	t.Helper()
	body := readText(t, filepath.Join(root, rel))
	for _, marker := range markers {
		if !strings.Contains(body, marker) {
			t.Fatalf("%s missing %q", rel, marker)
		}
	}
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	if len(line) > 120 {
		return line[:120]
	}
	return line
}

func readText(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
