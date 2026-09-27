// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harness"
)

func TestInstructionFilesStayAllowlisted(t *testing.T) {
	dir := t.TempDir()
	body := []byte("agents instructions\n")
	agents := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agents, body, 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "demo")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillBody := []byte("# skill\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillBody, 0o644); err != nil {
		t.Fatal(err)
	}
	neighbor := []byte("super-secret-neighbor")
	if err := os.WriteFile(filepath.Join(dir, "SECRET.txt"), neighbor, 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "CLAUDE.md"), []byte("not passed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := harness.CollectInstructionFiles([]string{agents, filepath.Join(skillDir, "SKILL.md")})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items %#v", items)
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	found := false
	for _, item := range items {
		if strings.Contains(item.LogicalName, dir) || strings.Contains(item.ContentSHA256, "super-secret") {
			t.Fatalf("provenance kept a path or neighbor: %#v", item)
		}
		if item.LogicalName == "AGENTS.md" {
			found = true
			if item.Kind != "agents" || item.ContentSHA256 != want || item.ByteSize == nil || *item.ByteSize != int64(len(body)) {
				t.Fatalf("agents item %#v", item)
			}
		}
		if item.Kind == "skill" && item.LogicalName != "demo/SKILL.md" {
			t.Fatalf("skill logical name %#v", item)
		}
	}
	if !found {
		t.Fatal("AGENTS.md missing")
	}
	neighborSum := sha256.Sum256(neighbor)
	for _, item := range items {
		if item.ContentSHA256 == hex.EncodeToString(neighborSum[:]) {
			t.Fatal("neighbor file was hashed")
		}
	}
	for _, path := range []string{
		dir,
		filepath.Join(dir, "README.md"),
		filepath.Join(dir, ".ssh", "AGENTS.md"),
		filepath.Join(dir, "notes.env", "AGENTS.md"),
	} {
		if path != dir {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("nope"), 0o000); err != nil && !os.IsExist(err) {
				t.Fatal(err)
			}
		}
		_, err := harness.CollectInstructionFiles([]string{path})
		if err == nil || strings.Contains(err.Error(), "nope") || strings.Contains(err.Error(), dir) {
			t.Fatalf("path %s err %v", filepath.Base(path), err)
		}
	}
	link := filepath.Join(dir, "link-agents.md")
	if err := os.Symlink(agents, link); err != nil {
		t.Fatal(err)
	}
	// A symlinked allowlisted basename is still a symlink and is not followed.
	renamed := filepath.Join(dir, "extra", "AGENTS.md")
	if err := os.Mkdir(filepath.Dir(renamed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(agents, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.CollectInstructionFiles([]string{renamed}); err == nil {
		t.Fatal("symlink was followed")
	}
	item, err := harness.PromptTemplateProvenance("260927120000.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	ident := sha256.Sum256([]byte("aeon.harness.provenance.prompt-template\x00" + "260927120000.0.0"))
	if item.ContentSHA256 != hex.EncodeToString(ident[:]) || item.ByteSize != nil || item.LogicalName != "prompt-template" {
		t.Fatalf("prompt identity %#v", item)
	}
	if _, err := harness.PromptTemplateProvenance("run the secret prompt now", ""); err == nil {
		t.Fatal("prompt text accepted as a version")
	}
	if _, err := harness.PromptTemplateProvenance("v1", "ABCD"); err == nil {
		t.Fatal("uppercase digest accepted")
	}
	versioned, err := harness.SetProvenanceVersion(items, "AGENTS.md", "pin-1")
	if err != nil || versioned[0].Version == nil && versioned[1].Version == nil {
		t.Fatal(err)
	}
}
