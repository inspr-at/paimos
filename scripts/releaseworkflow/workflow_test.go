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
	Name            string
	ID              string
	Uses            string
	Run             string
	If              string
	With            map[string]string
	Env             map[string]string
	ContinueOnError bool `yaml:"continue-on-error"`
}

type job struct {
	RunsOn   string `yaml:"runs-on"`
	If       string
	Strategy struct {
		FailFast bool `yaml:"fail-fast"`
		Matrix   struct {
			Include []struct {
				Arch   string
				Cache  string
				Runner string
			}
		}
	}
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
	if len(w.Jobs["image-platform"].Needs) != 0 || !reflect.DeepEqual(w.Jobs["image"].Needs, []string{"image-platform"}) || w.Jobs["image"].If != "" {
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
	j := readWorkflow(t, "release.yml").Jobs["image-platform"]
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
	if push.With["tags"] != "" || push.With["outputs"] != "type=image,name=ghcr.io/inspr-at/aeon,push-by-digest=true,name-canonical=true,push=true" {
		t.Fatal("platform jobs must publish by digest without tagging the release")
	}
	if !strings.HasPrefix(attest.Uses, "actions/attest-build-provenance@") || attest.With["subject-name"] != "ghcr.io/inspr-at/aeon" || attest.With["subject-digest"] != "${{ steps.push.outputs.digest }}" || attest.With["push-to-registry"] != "true" || attest.With["create-storage-record"] != "false" {
		t.Fatal("attestation must bind the pushed digest without new token scopes")
	}
	for _, binding := range []string{"oci://ghcr.io/inspr-at/aeon@${DIGEST}", "--repo \"$GITHUB_REPOSITORY\"", "--signer-workflow \"$GITHUB_REPOSITORY/.github/workflows/release.yml\"", "--source-ref \"$GITHUB_REF\"", "--source-digest \"$GITHUB_SHA\"", "--deny-self-hosted-runners"} {
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
	_, resolve := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], "Resolve loaded smoke image")
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

func TestPublishedRuntimeMustMatchSmokedRuntime(t *testing.T) {
	j := readWorkflow(t, "release.yml").Jobs["image-platform"]
	pushIndex, _ := named(t, j, "Build and push")
	identityIndex, identity := named(t, j, "Verify pushed runtime matches smoke")
	attestIndex, _ := named(t, j, "Attest pushed image")
	if !(pushIndex < identityIndex && identityIndex < attestIndex) || identity.If != "" || identity.ContinueOnError {
		t.Fatal("runtime identity gate can be bypassed")
	}
	for _, tc := range []struct {
		name, published string
		ok              bool
	}{
		{"same-export", "smoked", true},
		{"clock-only", "clock-only", true},
		{"different-rootfs", "different-rootfs", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			smoked := "sha256:" + strings.Repeat("a", 64)
			// Stub only Docker transport; identity data are the unchanged configs
			// of real BuildKit exports, including two injected creation clocks.
			stub := `#!/bin/bash
if [[ $1 == pull ]]; then exit 0; fi
if [[ ${@: -1} == "$SMOKED_IMAGE" ]]; then cat "$SMOKED_FIXTURE"; else cat "$PUBLISHED_FIXTURE"; fi
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", identity.Run)
			cmd.Dir = root(t)
			fixtures := filepath.Join(root(t), "scripts/releaseworkflow/testdata/runtime-identity")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RUNNER_TEMP="+dir, "SMOKED_IMAGE="+smoked, "DIGEST=sha256:"+strings.Repeat("c", 64), "SMOKED_FIXTURE="+filepath.Join(fixtures, "smoked.json"), "PUBLISHED_FIXTURE="+filepath.Join(fixtures, tc.published+".json"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("identity gate error=%v output=%s", err, output)
			}
		})
	}
}

// Rehearsal remains read-only and keeps its existing build arguments. The
// release-only epoch changes export clocks, not Dockerfile/runtime inputs.
func withoutSourceEpoch(t *testing.T, args string) string {
	t.Helper()
	line := "SOURCE_DATE_EPOCH=${{ steps.source-epoch.outputs.epoch }}\n"
	if strings.Count(args, line) != 1 {
		t.Fatal("release build must use the single pinned source epoch")
	}
	return strings.Replace(args, line, "", 1)
}

func TestExportsShareSourceEpoch(t *testing.T) {
	j := readWorkflow(t, "release.yml").Jobs["image-platform"]
	epochIndex, epoch := named(t, j, "Pin both exports to the source epoch")
	buildIndex, build := named(t, j, "Build cached smoke image")
	_, push := named(t, j, "Build and push")
	if epochIndex >= buildIndex || epoch.ID != "source-epoch" || epoch.If != "" || epoch.ContinueOnError || build.With["build-args"] != push.With["build-args"] {
		t.Fatal("exports do not share an unconditional source epoch")
	}
	withoutSourceEpoch(t, build.With["build-args"])
	for _, value := range []string{"1700000000", "", "bad", "1700000000\n1700000100"} {
		dir := t.TempDir()
		stub := "#!/bin/bash\n[[ $1 == show && $2 == -s && $3 == --format=%ct && $4 == HEAD ]] || exit 1\nprintf '%s\\n' \"$FIXTURE_EPOCH\"\n"
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(dir, "output")
		cmd := exec.Command("bash", "-c", epoch.Run)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURE_EPOCH="+value, "GITHUB_OUTPUT="+output)
		if err := cmd.Run(); (err == nil) != (value == "1700000000") {
			t.Fatalf("source epoch %q: %v", value, err)
		}
		if value == "1700000000" {
			body, err := os.ReadFile(output)
			if err != nil || string(body) != "epoch=1700000000\n" {
				t.Fatal("source epoch output differs")
			}
		}
	}
}

func TestReadOnlyDryRunAndLeastPrivilege(t *testing.T) {
	release := readWorkflow(t, "release.yml")
	dry := readWorkflow(t, "release-image-check.yml")
	web := readWorkflow(t, "docker-web-check.yml")
	if !reflect.DeepEqual(release.Permissions, map[string]string{"contents": "read"}) || !reflect.DeepEqual(dry.Permissions, map[string]string{"contents": "read"}) || !reflect.DeepEqual(web.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("workflow defaults must be read-only")
	}
	if web.Jobs["docker-web-stage"].RunsOn != "ubuntu-24.04" {
		t.Fatal("Docker web check must use the hosted ubuntu-24.04 runner")
	}
	paths := web.On["pull_request"].(map[string]any)["paths"].([]any)
	for _, path := range []string{"web/**", "internal/**/*.json", "Dockerfile", "version.json", "scripts/**", "go.mod", "NOTICE", ".dockerignore"} {
		found := false
		for _, actual := range paths {
			found = found || actual == path
		}
		if !found {
			t.Fatalf("web check misses %s", path)
		}
	}
	if !reflect.DeepEqual(release.Jobs["image-platform"].Permissions, map[string]string{"contents": "write", "actions": "read", "packages": "write", "attestations": "write", "id-token": "write"}) || !reflect.DeepEqual(release.Jobs["image"].Permissions, map[string]string{"contents": "write", "packages": "write", "attestations": "write", "id-token": "write"}) || !reflect.DeepEqual(release.Jobs["assets"].Permissions, map[string]string{"contents": "write"}) {
		t.Fatal("token writes must be scoped to the image and assets jobs")
	}
	if release.Jobs["agentd-darwin"].Environment != "release-signing" || !reflect.DeepEqual(release.Jobs["agentd-darwin"].Permissions, map[string]string{"contents": "read", "actions": "read"}) {
		t.Fatal("darwin signing boundary changed")
	}
	if _, ok := dry.On["workflow_dispatch"]; !ok {
		t.Fatal("missing manual dry run")
	}
	if _, ok := dry.On["pull_request_target"]; ok {
		t.Fatal("dry run must never use pull_request_target")
	}
	_, productionBuild := named(t, release.Jobs["image-platform"], "Build cached smoke image")
	_, dryBuild := named(t, dry.Jobs["image-dry-run"], "Build cached smoke image")
	productionBuild.With["build-args"] = withoutSourceEpoch(t, productionBuild.With["build-args"])
	if !reflect.DeepEqual(productionBuild.With, dryBuild.With) || productionBuild.Uses != dryBuild.Uses {
		t.Fatal("dry run must exercise the production smoke build")
	}
	tap := readWorkflow(t, "homebrew-tap.yml")
	if !reflect.DeepEqual(tap.On, map[string]any{"release": map[string]any{"types": []any{"published"}}}) || !reflect.DeepEqual(tap.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("Homebrew must remain read-only and publication-triggered")
	}
	pin := regexp.MustCompile(`@[a-f0-9]{40}$`)
	for _, w := range []workflow{release, dry, tap, web} {
		for _, j := range w.Jobs {
			for _, s := range j.Steps {
				if s.Uses != "" && !pin.MatchString(s.Uses) {
					t.Fatalf("unpinned action %s", s.Uses)
				}
			}
		}
	}
	for _, name := range []string{"release-image-check.yml", "docker-web-check.yml"} {
		data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secrets.") {
			t.Fatalf("%s must not request secrets", name)
		}
		for _, j := range readWorkflow(t, name).Jobs {
			if (len(j.Permissions) != 0 && !reflect.DeepEqual(j.Permissions, map[string]string{"contents": "read"})) || j.Environment != "" {
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
	_, guard := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], "Require an annotated release tag")
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
		{"image-platform", "Reject an existing GitHub release", "v260930120000.0.0"},
		{"image", "Reject an existing GitHub release", "v260930120000.0.0"},
		{"assets", "Reject an existing GitHub release", "v260930120000.0.0"},
		{"image-platform", "Reject an existing image tag", "260930120000.0.0"},
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

func TestNativePlatformsAndIndexPublication(t *testing.T) {
	release := readWorkflow(t, "release.yml")
	dry := readWorkflow(t, "release-image-check.yml")
	platform := release.Jobs["image-platform"]
	dryPlatform := dry.Jobs["image-dry-run"]
	if !reflect.DeepEqual(platform.Strategy, dryPlatform.Strategy) || platform.Strategy.FailFast || len(platform.Strategy.Matrix.Include) != 2 {
		t.Fatal("production and dry run require the same two independent native jobs")
	}
	for i, expected := range []struct{ arch, runner string }{{"amd64", "ubuntu-24.04"}, {"arm64", "ubuntu-24.04-arm"}} {
		entry := platform.Strategy.Matrix.Include[i]
		if entry.Arch != expected.arch || entry.Runner != expected.runner || entry.Cache != map[string]string{"amd64": "buildcache", "arm64": "buildcache-arm64"}[expected.arch] {
			t.Fatal("incorrect native runner mapping")
		}
	}
	for _, j := range []job{platform, dryPlatform} {
		if j.RunsOn != "${{ matrix.runner }}" || j.If != "" {
			t.Fatal("platform jobs must always run on the native matrix")
		}
		probeIndex, probe := named(t, j, "Require native platform")
		buildIndex, build := named(t, j, "Build cached smoke image")
		if probeIndex >= buildIndex || probe.If != "" || build.With["platforms"] != "linux/${{ matrix.arch }}" || build.With["cache-from"] != "type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}" {
			t.Fatal("native check, platform or independent cache missing")
		}
		for _, tc := range []struct {
			arch, machine string
			ok            bool
		}{{"amd64", "x86_64", true}, {"arm64", "aarch64", true}, {"arm64", "x86_64", false}, {"amd64", "aarch64", false}} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "uname"), []byte("#!/bin/bash\necho "+tc.machine+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", probe.Run)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "ARCH="+tc.arch)
			if err := cmd.Run(); (err == nil) != tc.ok {
				t.Fatalf("native probe %s/%s: %v", tc.arch, tc.machine, err)
			}
		}
		for _, s := range j.Steps {
			if strings.Contains(s.Uses, "qemu") {
				t.Fatal("emulated release build")
			}
		}
	}
	_, push := named(t, platform, "Build and push")
	if push.With["cache-to"] != "type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }},mode=max" {
		t.Fatal("parallel cache writes overlap")
	}
	verifyIndex, _ := named(t, platform, "Verify pushed image attestation")
	exportIndex, export := named(t, platform, "Record platform digest")
	if exportIndex <= verifyIndex || !strings.Contains(export.Run, `image-digests/$ARCH.txt`) {
		t.Fatal("digest handoff must follow verified attestation")
	}
	index := release.Jobs["image"]
	pushIndex, merge := named(t, index, "Publish multi-arch index")
	attestIndex, attest := named(t, index, "Attest pushed image")
	indexVerifyIndex, _ := named(t, index, "Verify pushed image attestation")
	if !(pushIndex < attestIndex && attestIndex < indexVerifyIndex) || attest.With["subject-digest"] != "${{ steps.push.outputs.digest }}" {
		t.Fatal("index attestation gate missing")
	}
	for _, name := range []string{"Reject an existing GitHub release", "Reject an existing image tag"} {
		i, _ := named(t, index, name)
		if i >= pushIndex {
			t.Fatal("index immutability check must precede publication")
		}
	}
	for _, fragment := range []string{"release-image-index.mjs sources", "imagetools create --dry-run", "release-image-index.mjs verify", `--tag "ghcr.io/inspr-at/aeon:${VERSION}"`, "containerimage.descriptor", "imagetools inspect --raw", `echo "digest=$digest"`} {
		if !strings.Contains(merge.Run, fragment) {
			t.Fatalf("index publication missing %s", fragment)
		}
	}
	if strings.Count(merge.Run, "release-image-index.mjs verify") != 2 || merge.If != "" {
		t.Fatal("validate index before and after publication")
	}
}

func TestPinProposalFollowsVerificationWithoutWaitingForAssets(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	image := w.Jobs["image"]
	verifyIndex, _ := named(t, image, "Verify pushed image attestation")
	recordIndex, record := named(t, image, "Record pushed digest")
	pinIndex, pin := named(t, image, "Propose verified nixcfg deployment pin")
	if recordIndex != verifyIndex+1 || pinIndex != recordIndex+1 || pin.If != "" || pin.ID != "pin" {
		t.Fatal("record the verified digest before the optional pin proposal")
	}
	if record.Env["DIGEST"] != "${{ steps.push.outputs.digest }}" || !strings.Contains(record.Run, "ghcr.io/inspr-at/aeon@${DIGEST}") {
		t.Fatal("digest summary must bind the verified release index")
	}
	if !pin.ContinueOnError {
		t.Fatal("a pin proposal failure must not fail image or skip assets")
	}
	for _, s := range image.Steps {
		if s.ID != "pin" && s.ContinueOnError {
			t.Fatal("only the optional pin proposal may ignore failures")
		}
	}
	if image.Environment != "release-pinning" || image.Outputs["pin_evidence"] != "${{ steps.pin.outputs.pin_evidence }}" {
		t.Fatal("pin credentials and release evidence must use the documented boundary")
	}
	for key, expected := range map[string]string{
		"GH_TOKEN":             "${{ secrets.GITHUB_TOKEN }}",
		"VERSION":              "${{ steps.version.outputs.version }}",
		"DIGEST":               "${{ steps.push.outputs.digest }}",
		"AEON_PIN_BOT_ENABLED": "${{ vars.AEON_PIN_BOT_ENABLED }}",
		"AEON_PIN_APP_ID":      "${{ secrets.AEON_PIN_APP_ID }}",
		"AEON_PIN_APP_KEY":     "${{ secrets.AEON_PIN_APP_KEY }}",
		"INDEX_PUSHED_AT":      "${{ steps.push.outputs.pushed_at }}",
	} {
		if pin.Env[key] != expected {
			t.Fatalf("pin proposal input %s is not bound to the approved release", key)
		}
	}
	for _, fragment := range []string{`args=(scripts/release-pin-pr.mjs)`, `if [ "$AEON_PIN_BOT_ENABLED" = true ]`, `args+=(--write)`, `if ! node "${args[@]}"; then`} {
		if !strings.Contains(pin.Run, fragment) {
			t.Fatalf("pin proposal lacks explicit read-only/write routing: %s", fragment)
		}
	}
	for _, fragment := range []string{"::warning::Deployment pin proposal failed", "Deployment pin proposal failed; release assets will still be built.", `>> "$GITHUB_STEP_SUMMARY"`, "exit 1"} {
		if !strings.Contains(pin.Run, fragment) {
			t.Fatalf("pin failure must retain its outcome, annotation and summary: %s", fragment)
		}
	}
	_, draft := named(t, w.Jobs["assets"], "Create draft GitHub release with signed assets")
	if draft.Env["PIN_EVIDENCE"] != "${{ needs.image.outputs.pin_evidence }}" || !strings.Contains(draft.Run, `"$PIN_EVIDENCE"`) {
		t.Fatal("draft release must carry the image job's verified pin evidence")
	}
	for name, j := range w.Jobs {
		for _, s := range j.Steps {
			for key, value := range s.Env {
				if strings.HasPrefix(key, "AEON_PIN_APP_") && (name != "image" || s.ID != "pin" || !strings.Contains(value, "secrets.AEON_PIN_APP_")) {
					t.Fatal("pin credentials escaped the verified image proposal step")
				}
			}
			if strings.Contains(s.Run, "gh pr merge") || strings.Contains(s.Run, "--auto") || strings.Contains(s.Run, "nixos-rebuild") {
				t.Fatal("release workflow must never merge or deploy a pin proposal")
			}
		}
	}
	// CI has scalar and list needs; only parse the steps used by this check.
	var ci struct {
		Jobs map[string]struct{ Steps []step }
	}
	data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &ci); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ci.Jobs["release-check"].Steps {
		found = found || strings.Contains(s.Run, "node --test scripts/release-pin-pr.test.mjs")
	}
	if !found {
		t.Fatal("pin regression tests must run in ordinary draft PR CI")
	}
}
