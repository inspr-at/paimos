// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNixVendorHashGate(t *testing.T) {
	jobs := treeMap(treeWorkflow(t, "ci.yml")["jobs"])
	encoded, err := yaml.Marshal(jobs["release-check-run"])
	if err != nil {
		t.Fatal(err)
	}
	var j job
	if err := yaml.Unmarshal(encoded, &j); err != nil {
		t.Fatal(err)
	}
	if j.RunsOn != "ubuntu-latest" {
		t.Fatal("Nix vendor gate must remain on hosted Linux")
	}
	if treeMap(jobs["release-check-run"])["timeout-minutes"] != 20 {
		t.Fatal("Nix vendor gate must have a bounded job timeout")
	}
	installIndex, install := named(t, j, "Install Nix for the vendor hash gate")
	cacheIndex, cache := named(t, j, "Cache Nix build inputs")
	checkIndex, check := named(t, j, "Verify the Go vendor hash")
	if installIndex >= cacheIndex || cacheIndex >= checkIndex {
		t.Fatal("Nix installation and cache restore must precede the hash check")
	}
	for _, s := range []step{install, cache, check} {
		if s.If != "needs.ci-plan.outputs.nix_vendor != 'false'" || s.ContinueOnError {
			t.Fatalf("%s must run on missing classification and propagate failures", s.Name)
		}
	}
	if install.Uses != "cachix/install-nix-action@13d8dd58da0234aa297dedd986986ccb8e7f3e24" ||
		cache.Uses != "nix-community/cache-nix-action@135667ec418502fa5a3598af6fb9eb733888ce6a" {
		t.Fatal("Nix actions must retain commit pins")
	}
	if cache.With["primary-key"] != "nix-vendor-v1-${{ runner.os }}-${{ runner.arch }}-${{ hashFiles('go.mod', 'go.sum', 'flake.nix', 'flake.lock') }}" ||
		cache.With["save"] != "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}" {
		t.Fatal("Nix cache must bind all dependency inputs and save only on main pushes")
	}
	if treeMap(treeMap(jobs["ci-plan"])["outputs"])["nix_vendor"] != "${{ steps.plan.outputs.nix_vendor }}" {
		t.Fatal("ci-plan must expose the Nix selection")
	}
	if check.Run != "bash scripts/check-nix-vendor-hash.sh" {
		t.Fatal("CI must run the reusable vendor hash check")
	}
	// Execute the actual workflow command with a closed Nix adapter. This proves
	// rebuild-on-cache-hit argv and failure propagation without running a build
	// inside Go tests; the real stale/fixed Nix builds are worker evidence.
	for _, fixture := range []struct {
		name      string
		build     string
		rebuild   string
		wantExit  int
		wantCalls int
	}{
		{"cold-or-warm-success", "0", "0", 0, 2},
		{"cold-build-failure", "1", "0", 1, 1},
		{"cached-rebuild-failure", "0", "37", 37, 2},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "argv")
			adapter := "#!/bin/bash\nprintf '%s\\n' \"$@\" >> \"$CALL_LOG\"\n" +
				"if [[ \" $* \" == *' --rebuild '* ]]; then exit \"$NIX_REBUILD_EXIT\"; fi\nexit \"$NIX_BUILD_EXIT\"\n"
			if err := os.WriteFile(filepath.Join(dir, "nix"), []byte(adapter), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", check.Run)
			cmd.Dir = root(t)
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "CALL_LOG=" + log,
				"NIX_BUILD_EXIT=" + fixture.build, "NIX_REBUILD_EXIT=" + fixture.rebuild}
			output, err := cmd.CombinedOutput()
			if fixture.wantExit == 0 && err != nil {
				t.Fatalf("valid hash failed: %v: %s", err, output)
			}
			if fixture.wantExit != 0 {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != fixture.wantExit {
					t.Fatalf("Nix failure must propagate unchanged: %v: %s", err, output)
				}
			}
			argv, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"build", ".#aeon.goModules", "-L", "--no-link", "--no-write-lock-file", "--cores", "2", "--max-jobs", "1"}
			want := strings.Join(args, "\n") + "\n"
			if fixture.wantCalls == 2 {
				want += strings.Join(args, "\n") + "\n--rebuild\n"
			}
			if string(argv) != want {
				t.Fatalf("dependency check must force the shared hash rebuild: %s", argv)
			}
		})
	}
}

// Preserve the historical full-CI fixtures. Strip only AEON-703's additions,
// whose selection, pins, cache authority and failure behavior are tested above.
func normalizeNixVendorAdditions(t *testing.T, id string, j map[string]any) {
	t.Helper()
	switch id {
	case "ci-plan":
		delete(treeMap(j["outputs"]), "nix_vendor")
		s := reuseStep(t, j, "Classify local PR diff")
		run, _ := s["run"].(string)
		const current = `printf 'lane=full\nspecs=[]\nnix_vendor=true\n'`
		const historical = `printf 'lane=full\nspecs=[]\n'`
		if strings.Count(run, current) != 1 {
			t.Fatal("Nix fallback must keep the full trusted classification")
		}
		s["run"] = strings.Replace(run, current, historical, 1)
	case "release-check-run":
		delete(j, "timeout-minutes")
		var original []any
		for _, value := range reuseSteps(j) {
			switch treeMap(value)["name"] {
			case "Install Nix for the vendor hash gate", "Cache Nix build inputs", "Verify the Go vendor hash":
			default:
				original = append(original, value)
			}
		}
		j["steps"] = original
	}
}
