// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulescompare"
)

func TestRulesCompareCLIReportsBlockerAndRefusesUnsafeIO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("## Checks\n- Keep the fixture boundary.\n  Why: tests need a reason.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCompare(t, &out, "--context", "project", "--file", path); err != nil {
		t.Fatal(err)
	}
	var report rulescompare.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != rulescompare.Schema || report.Limits.ExecutionVerified || report.Limits.RolloutAuthorized || report.PublishedMergeCompared {
		t.Fatalf("%+v", report.Limits)
	}
	if !strings.Contains(out.String(), "published_layer_unavailable") || strings.Contains(out.String(), "Keep the fixture boundary.") {
		t.Fatal(out.String())
	}
	reportPath := filepath.Join(dir, "report.json")
	out.Reset()
	if err := runCompare(t, &out, "--context", "project", "--file", path, "--out", reportPath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(reportPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(err, info)
	}
	if err = runCompare(t, &out, "--context", "project", "--file", path, "--out", reportPath); err == nil {
		t.Fatal("report overwrite accepted")
	}
	loose := filepath.Join(dir, "loose.json")
	if err = os.WriteFile(loose, []byte(`{"items":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = runCompare(t, io.Discard, "--context", "project", "--file", path, "--provenance", loose); err == nil {
		t.Fatal("loose provenance file accepted")
	}
	secret := filepath.Join(dir, ".ssh", "AGENTS.md")
	if err = runCompare(t, io.Discard, "--context", "project", "--file", secret); err == nil || strings.Contains(err.Error(), "NEIGHBOR") {
		t.Fatal(err)
	}
	if err = runCompare(t, io.Discard, "--file", path); err == nil {
		t.Fatal("missing context accepted")
	}
}

func TestRulesCompareCLIReadsPrivateMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	body := "## Checks\n<!-- aeon-rule: fixture.check -->\n- Keep the fixture boundary.\n  Why: tests need a reason.\n  source: AEON-251\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(dir, "merged.json")
	payload := []byte(`{"schema":"aeon.rules.cache.v1","bundle":{"context":{"tenant_id":"10000000-0000-4000-8000-000000000001","project_id":"10000000-0000-4000-8000-000000000002","person_id":"10000000-0000-4000-8000-000000000003","role":"builder","harness":"codex"},"versions":[],"version":"floor-only","sha256":"","body":"","byte_size":0,"rules":[],"floor":""}}`)
	if err := rules.WriteFile(merged, payload, false); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCompare(t, &out, "--context", "project", "--file", path, "--merged", merged); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "merge_integrity") || strings.Contains(out.String(), "Keep the fixture boundary.") {
		t.Fatal(out.String())
	}
}

func runCompare(t *testing.T, out io.Writer, args ...string) error {
	t.Helper()
	rt := &runtime{program: "aeon", stdout: out, stderr: io.Discard}
	cmd := rt.cmdRulesCompare()
	pos, err := rt.parse(cmd, args)
	if err != nil {
		return err
	}
	if err = cmd.checkArgs(pos); err != nil {
		return err
	}
	return cmd.run(pos)
}
