// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type step struct {
	Name string
	ID   string
	Uses string
	Run  string
	If   string
	With map[string]string
	Env  map[string]string
}

type job struct {
	Needs       []string
	Permissions map[string]string
	Outputs     map[string]string
	Environment string
	Steps       []step
}

type workflow struct {
	On          map[string]any
	Permissions map[string]string
	Jobs        map[string]job
}

func root(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func named(t *testing.T, j job, name string) (int, step) {
	t.Helper()
	for i, s := range j.Steps {
		if s.Name == name {
			return i, s
		}
	}
	t.Fatalf("missing step %q", name)
	return -1, step{}
}

func TestImageDoesNotWaitForClients(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	if len(w.On) != 1 || w.On["push"] == nil {
		t.Fatal("publishing must remain tag-push only")
	}
	if len(w.Jobs["image"].Needs) != 0 {
		t.Fatal("server image waits for another job")
	}
	if !reflect.DeepEqual(w.Jobs["assets"].Needs, []string{"agentd-darwin", "image"}) {
		t.Fatal("assets must wait for signed darwin binaries and the verified digest")
	}
	image := w.Jobs["image"]
	if image.Outputs["digest"] != "${{ steps.push.outputs.digest }}" || image.Outputs["version"] != "${{ steps.version.outputs.version }}" {
		t.Fatal("missing image digest/version outputs")
	}
	_, draft := named(t, w.Jobs["assets"], "Create draft GitHub release with signed assets")
	for _, flag := range []string{"--draft", "--verify-tag"} {
		if !strings.Contains(draft.Run, flag) {
			t.Fatalf("release creation missing %s", flag)
		}
	}
	if draft.Env["DIGEST"] != "${{ needs.image.outputs.digest }}" || draft.Env["VERSION"] != "${{ needs.image.outputs.version }}" {
		t.Fatal("release notes must use the image job's coordinate and digest")
	}
	if _, ok := w.Jobs["homebrew-tap"]; ok {
		t.Fatal("Homebrew must stay on AEON-356's separate post-publication workflow")
	}
}

func TestSmokeBeforePushAndAttest(t *testing.T) {
	j := readWorkflow(t, "release.yml").Jobs["image"]
	buildIndex, build := named(t, j, "Build cached smoke image")
	smokeIndex, smoke := named(t, j, "Smoke production image")
	pushIndex, push := named(t, j, "Build and push")
	attestIndex, attest := named(t, j, "Attest pushed image")
	verifyIndex, verify := named(t, j, "Verify pushed image attestation")
	if !(buildIndex < smokeIndex && smokeIndex < pushIndex && pushIndex < attestIndex && attestIndex < verifyIndex) {
		t.Fatal("push or attestation can bypass the smoke gate")
	}
	for _, name := range []string{"Require an annotated release tag", "Reject an existing GitHub release", "Reject an existing image tag"} {
		i, _ := named(t, j, name)
		if i >= buildIndex {
			t.Fatalf("%s must precede the image build", name)
		}
	}
	if build.With["load"] != "true" || build.With["push"] == "true" || build.With["cache-to"] != "" {
		t.Fatal("smoke build must stay local until the gate passes")
	}
	resolveIndex, resolve := named(t, j, "Resolve loaded smoke image")
	if !(buildIndex < resolveIndex && resolveIndex < smokeIndex) || resolve.ID != "smoke-image" || resolve.Env["SMOKE_TAG"] != build.With["tags"] {
		t.Fatal("smoke must resolve exactly the tag loaded by the build")
	}
	if smoke.Env["AEON_SMOKE_IMAGE"] != "${{ steps.smoke-image.outputs.image }}" || smoke.Run != "bash scripts/smoke-image.sh" {
		t.Fatal("smoke must exercise the loaded immutable image ID")
	}
	for _, key := range []string{"context", "platforms", "build-args", "cache-from"} {
		if build.With[key] != push.With[key] || build.With[key] == "" {
			t.Fatalf("smoked and pushed build inputs differ: %s", key)
		}
	}
	if push.With["context"] != "." || push.With["push"] != "true" || push.With["provenance"] != "mode=max" || !strings.Contains(push.With["cache-to"], "type=registry,") || !strings.HasSuffix(push.With["cache-to"], ",mode=max") {
		t.Fatal("pushed image requires generated history, registry cache and provenance")
	}
	if push.With["tags"] != "ghcr.io/inspr-at/aeon:${{ steps.version.outputs.version }}" {
		t.Fatal("publish only the immutable release coordinate")
	}
	if !strings.HasPrefix(attest.Uses, "actions/attest-build-provenance@") || attest.With["subject-name"] != "ghcr.io/inspr-at/aeon" || attest.With["subject-digest"] != "${{ steps.push.outputs.digest }}" || attest.With["push-to-registry"] != "true" || attest.With["create-storage-record"] != "false" {
		t.Fatal("attestation must bind the pushed digest without new token scopes")
	}
	for _, binding := range []string{"oci://ghcr.io/inspr-at/aeon@${DIGEST}", "--repo \"$GITHUB_REPOSITORY\"", "--signer-workflow \"$GITHUB_REPOSITORY/.github/workflows/release.yml\"", "--source-ref \"$GITHUB_REF\"", "--source-digest \"$GITHUB_SHA\""} {
		if !strings.Contains(verify.Run, binding) {
			t.Fatalf("verification missing binding %s", binding)
		}
	}
	for _, s := range []step{smoke, push, attest, verify} {
		if s.If != "" {
			t.Fatalf("gate step %s has an overriding condition", s.Name)
		}
	}
}

func TestLoadedImageResolverFailsClosed(t *testing.T) {
	_, resolve := named(t, readWorkflow(t, "release.yml").Jobs["image"], "Resolve loaded smoke image")
	_, dryResolve := named(t, readWorkflow(t, "release-image-check.yml").Jobs["image-dry-run"], "Resolve loaded smoke image")
	if resolve.Run != dryResolve.Run || !reflect.DeepEqual(resolve.Env, dryResolve.Env) {
		t.Fatal("production and dry run must resolve the same loaded image")
	}
	for _, tc := range []struct {
		name, response string
		ok             bool
	}{
		{"one-image", "sha256:" + strings.Repeat("a", 64), true},
		{"missing", "", false},
		{"short-id", "abc123", false},
		{"multiple-images", "sha256:" + strings.Repeat("a", 64) + "\nsha256:" + strings.Repeat("b", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/bash\nprintf '%s\\n' '" + tc.response + "'\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "output")
			cmd := exec.Command("bash", "-c", resolve.Run)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SMOKE_TAG=fixture:loaded", "GITHUB_OUTPUT="+output)
			if err := cmd.Run(); (err == nil) != tc.ok {
				t.Fatalf("resolver result %v, want success %v", err, tc.ok)
			}
			if tc.ok {
				b, err := os.ReadFile(output)
				if err != nil || string(b) != "image="+tc.response+"\n" {
					t.Fatalf("resolver output %q, error %v", b, err)
				}
			}
		})
	}
}

func TestReadOnlyDryRunAndLeastPrivilege(t *testing.T) {
	release := readWorkflow(t, "release.yml")
	dry := readWorkflow(t, "release-image-check.yml")
	if !reflect.DeepEqual(release.Permissions, map[string]string{"contents": "read"}) || !reflect.DeepEqual(dry.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("workflow defaults must be read-only")
	}
	if !reflect.DeepEqual(release.Jobs["image"].Permissions, map[string]string{"contents": "write", "actions": "read", "packages": "write", "attestations": "write", "id-token": "write"}) || !reflect.DeepEqual(release.Jobs["assets"].Permissions, map[string]string{"contents": "write"}) {
		t.Fatal("token writes must be scoped to the image and assets jobs")
	}
	if release.Jobs["agentd-darwin"].Environment != "release-signing" || !reflect.DeepEqual(release.Jobs["agentd-darwin"].Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("darwin signing boundary changed")
	}
	if _, ok := dry.On["workflow_dispatch"]; !ok {
		t.Fatal("missing manual dry run")
	}
	if _, ok := dry.On["pull_request_target"]; ok {
		t.Fatal("dry run must never use pull_request_target")
	}
	_, productionBuild := named(t, release.Jobs["image"], "Build cached smoke image")
	_, dryBuild := named(t, dry.Jobs["image-dry-run"], "Build cached smoke image")
	if !reflect.DeepEqual(productionBuild.With, dryBuild.With) || productionBuild.Uses != dryBuild.Uses {
		t.Fatal("dry run must exercise the production smoke build")
	}
	tap := readWorkflow(t, "homebrew-tap.yml")
	if !reflect.DeepEqual(tap.On, map[string]any{"release": map[string]any{"types": []any{"published"}}}) || !reflect.DeepEqual(tap.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("Homebrew must remain read-only and publication-triggered")
	}
	pin := regexp.MustCompile(`@[a-f0-9]{40}$`)
	for _, w := range []workflow{release, dry, tap} {
		for _, j := range w.Jobs {
			for _, s := range j.Steps {
				if s.Uses != "" && !pin.MatchString(s.Uses) {
					t.Fatalf("unpinned action %s", s.Uses)
				}
			}
		}
	}
	for _, j := range dry.Jobs {
		if len(j.Permissions) != 0 || j.Environment != "" {
			t.Fatal("dry run acquires privileged tokens or environments")
		}
		for _, s := range j.Steps {
			if s.With["push"] == "true" || s.With["cache-to"] != "" || strings.Contains(s.Uses, "attest") || strings.Contains(s.Uses, "login-action") || strings.Contains(s.Run, "gh release") {
				t.Fatalf("dry run has a publication side effect: %s", s.Name)
			}
			for _, v := range s.Env {
				if strings.Contains(v, "secrets.") {
					t.Fatal("dry run must not request secrets")
				}
			}
		}
	}
}

func shell(t *testing.T, run, git, gh string, vars ...string) error {
	t.Helper()
	dir := t.TempDir()
	for name, script := range map[string]string{"git": git, "gh": gh} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", run)
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + dir + ":" + os.Getenv("PATH")}, vars...)...)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}

func TestAnnotatedTagGuardFailsClosed(t *testing.T) {
	_, guard := named(t, readWorkflow(t, "release.yml").Jobs["image"], "Require an annotated release tag")
	for _, tc := range []struct {
		name, git string
		ok        bool
	}{
		{"annotated", "echo tag", true},
		{"lightweight", "echo commit", false},
		{"missing", "exit 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := shell(t, guard.Run, tc.git, "exit 1", "VERSION=260930120000.0.0")
			if (err == nil) != tc.ok {
				t.Fatalf("guard result %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestImmutabilityGuardsFailClosed(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	for _, target := range []struct{ job, step, coordinate string }{
		{"image", "Reject an existing GitHub release", "v260930120000.0.0"},
		{"assets", "Reject an existing GitHub release", "v260930120000.0.0"},
		{"image", "Reject an existing image tag", "260930120000.0.0"},
	} {
		t.Run(target.job+"/"+target.step, func(t *testing.T) {
			_, guard := named(t, w.Jobs[target.job], target.step)
			for _, tc := range []struct {
				name, gh string
				ok       bool
			}{
				{"existing-including-draft-or-partial", "printf '%s\\n' '" + target.coordinate + "'", false},
				{"different-coordinate", "printf '%s\\n' '" + target.coordinate + "1'", true},
				{"absent", "exit 0", true},
				{"lookup-error", "exit 1", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := shell(t, guard.Run, "exit 1", tc.gh, "VERSION=260930120000.0.0", "GITHUB_REPOSITORY=fixture/aeon")
					if (err == nil) != tc.ok {
						t.Fatalf("guard result %v, want success %v", err, tc.ok)
					}
				})
			}
		})
	}
}

func TestSmokeImageOwnership(t *testing.T) {
	for _, prebuilt := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "prebuilt"}[prebuilt], func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "docker.log")
			stubs := map[string]string{
				"docker": `printf '%s\n' "$*" >> "$DOCKER_LOG"
case "$*" in
  *"stat -c %u:%g:%a"*) echo '65532:65532:750' ;;
  *"--entrypoint /bin/sh"*) exit 19 ;;
esac`,
				"openssl": "echo fixture",
				"trash":   "exit 0",
			}
			for name, script := range stubs {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+script+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", filepath.Join(root(t), "scripts/smoke-image.sh"))
			image := ""
			if prebuilt {
				image = "sha256:" + strings.Repeat("a", 64)
			}
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "AEON_SMOKE_TMP_ROOT="+dir, "DOCKER_LOG="+log, "AEON_SMOKE_IMAGE="+image, "AEON_SMOKE_VERSION=260930120000.0.0")
			if err := cmd.Run(); err == nil {
				t.Fatal("runtime dependency check failure must fail smoke")
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(b)
			if prebuilt {
				if strings.Contains(calls, "build ") || strings.Contains(calls, "image rm ") || !strings.Contains(calls, "--entrypoint /bin/sh "+image) {
					t.Fatalf("prebuilt image rebuilt, removed or not exercised: %s", calls)
				}
			} else if !strings.Contains(calls, "build --build-arg VERSION=260930120000.0.0") || !strings.Contains(calls, "image rm aeon-smoke:fixture") {
				t.Fatalf("standalone build/cleanup behavior changed: %s", calls)
			}
		})
	}
}
