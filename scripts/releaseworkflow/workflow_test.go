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
	Uses           string
	With           map[string]string
	RunsOn         string `yaml:"runs-on"`
	If             string
	TimeoutMinutes int `yaml:"timeout-minutes"`
	Strategy       struct {
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
	Env         map[string]string
	Outputs     map[string]string
	Environment string
	Steps       []step
}

type workflow struct {
	Env         map[string]string
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

// Risk: setup actions can select vulnerable cached toolchains. Every workflow
// pins the security patch; every release toolchain, including the duplicate-tag
// preflight, asserts its runtime and rejects stale or lookalike versions.
func TestReleaseToolchainsRequireExactSecurityPatches(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(root(t), ".github/workflows/*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("workflow inventory: %v", err)
	}
	assertions := map[string]int{}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var w struct {
			Jobs map[string]struct{ Steps []step }
		}
		if err := yaml.Unmarshal(body, &w); err != nil {
			t.Fatalf("%s: %v", filepath.Base(file), err)
		}
		for id, j := range w.Jobs {
			for i, setup := range j.Steps {
				for _, tool := range []struct {
					action, key, version, assertion, command, flag string
					reports                                        []string
				}{
					{"actions/setup-go@", "go-version", "1.26.10", "Assert Go 1.26.10", "go", "version", []string{
						"go version go1.26.10 linux/amd64", "go version go1.26.9 linux/amd64",
						"go version go1.26.100 linux/amd64", "go version go1.26.10rc1 linux/amd64",
					}},
					{"actions/setup-node@", "node-version", "24.22.1", "Assert Node 24.22.1", "node", "--version", []string{
						"v24.22.1", "v24.21.0", "v24.22.10", "v24.22.1-rc.1",
					}},
				} {
					if !strings.HasPrefix(setup.Uses, tool.action) {
						continue
					}
					if setup.With[tool.key] != tool.version {
						t.Fatalf("%s/%s: %s must pin %s", filepath.Base(file), id, tool.key, tool.version)
					}
					if filepath.Base(file) != "release.yml" {
						continue
					}
					assertions[id+"/"+tool.command]++
					if i+1 >= len(j.Steps) || j.Steps[i+1].Name != tool.assertion {
						t.Fatalf("%s: %s must immediately follow setup", id, tool.assertion)
					}
					guard := j.Steps[i+1]
					if guard.If != "" || guard.ContinueOnError || guard.Run == "" {
						t.Fatalf("%s: %s must be mandatory", id, tool.assertion)
					}
					for index, report := range tool.reports {
						t.Run(id+"/"+tool.command+"/"+report, func(t *testing.T) {
							dir := t.TempDir()
							stub := "#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = '" + tool.flag + "' ] || exit 90\nprintf '%s\\n' \"$PROBE_VERSION\"\n"
							if err := os.WriteFile(filepath.Join(dir, tool.command), []byte(stub), 0700); err != nil {
								t.Fatal(err)
							}
							cmd := exec.Command("bash", "-c", guard.Run)
							cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "PROBE_VERSION="+report)
							output, err := cmd.CombinedOutput()
							if string(output) != report+"\n" {
								t.Fatalf("version log: %v: %s", err, output)
							}
							if index == 0 {
								if err != nil {
									t.Fatalf("pinned version rejected: %v", err)
								}
							} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
								t.Fatalf("expected version mismatch rejection, got %v", err)
							}
						})
					}
				}
			}
		}
	}
	if !reflect.DeepEqual(assertions, map[string]int{
		"publication/node":    1,
		"image-platform/node": 1,
		"image-platform/go":   1,
		"image/node":          1,
	}) {
		t.Fatalf("release toolchain assertion inventory differs: %v", assertions)
	}
}

// Risk: a host's older pg_dump can neither read pg18 nor perform the vector-free
// backup drill. Both CI lanes must use the clients from their exact service.
// releaseYAML joins only job sections for invariant tests; the real publisher
// still calls the independently tested completion workflow with bound inputs.
func releaseYAML(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	completion := strings.SplitN(read("release-completion.yml"), "\njobs:\n", 2)
	if len(completion) != 2 {
		t.Fatal("completion jobs missing")
	}
	return read("release.yml") + "\n" + completion[1]
}

func releaseJobs(t *testing.T) workflow { return parseWorkflow(t, releaseYAML(t)) }

func nativeBuildCommands(body string) string {
	var commands []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "bash scripts/build-release-binaries.sh") {
			commands = append(commands, strings.TrimSpace(line))
		}
	}
	return strings.Join(commands, "\n")
}

func TestNightlyBackupUsesItsPostgresServiceClients(t *testing.T) {
	for _, lane := range []struct{ file, job string }{
		{"ci.yml", "go-test"}, {"nightly-full.yml", "nightly-go-test"},
	} {
		t.Run(lane.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", lane.file))
			if err != nil {
				t.Fatal(err)
			}
			var w struct {
				Jobs map[string]struct{ Steps []step }
			}
			if err := yaml.Unmarshal(data, &w); err != nil {
				t.Fatal(err)
			}
			_, run := named(t, job{Steps: w.Jobs[lane.job].Steps}, "Test this shard (essential plus changed area, or full on main)")
			if run.Env["AEON_TEST_POSTGRES_CONTAINER"] != "${{ job.services.postgres.id }}" {
				t.Fatal("backup/restore must use the PostgreSQL service's exact client version")
			}
			if run.ContinueOnError || run.If != "" {
				t.Fatal("the database regressions must remain mandatory")
			}
		})
	}
}

func TestImageDoesNotWaitForClients(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	c := readWorkflow(t, "release-completion.yml")
	if len(w.On) != 1 || w.On["push"] == nil {
		t.Fatal("publishing must remain tag-push only")
	}
	if !reflect.DeepEqual(w.Jobs["image-platform"].Needs, []string{"publication"}) || w.Jobs["image-platform"].If != "needs.publication.outputs.publish == 'true'" || !reflect.DeepEqual(w.Jobs["image"].Needs, []string{"image-platform"}) || w.Jobs["image"].If != "" {
		t.Fatal("server image waits for another job")
	}
	publication := w.Jobs["publication"]
	if len(publication.Needs) != 0 || publication.If != "" || publication.Environment != "" || publication.TimeoutMinutes != 10 ||
		!reflect.DeepEqual(publication.Permissions, map[string]string{"contents": "write", "packages": "read", "attestations": "read"}) ||
		publication.Outputs["publish"] != "${{ steps.publication.outputs.publish }}" {
		t.Fatal("tag publication preflight must precede every build without signing or package publication authority")
	}
	_, guard := named(t, publication, "Verify publication state for this tag")
	if guard.ID != "publication" || guard.If != "" || guard.ContinueOnError || guard.Run != "node scripts/release-publication.mjs" ||
		!reflect.DeepEqual(guard.Env, map[string]string{"GH_TOKEN": "${{ secrets.GITHUB_TOKEN }}", "RELEASE_TAG": "${{ github.ref_name }}"}) {
		t.Fatal("tag publication preflight can skip verification")
	}
	if len(publication.Steps) != 5 || publication.Steps[0].With["persist-credentials"] != "false" ||
		publication.Steps[3].Uses != "docker/login-action@dbcb813823bdd20940b903addbd779551569679f" ||
		publication.Steps[3].If != "" || publication.Steps[3].ContinueOnError ||
		!reflect.DeepEqual(publication.Steps[3].With, map[string]string{"registry": "ghcr.io", "username": "${{ github.actor }}", "password": "${{ secrets.GITHUB_TOKEN }}"}) {
		t.Fatal("publication verification must authenticate read-only registry inspection with the existing workflow token")
	}
	var policy struct {
		Concurrency struct {
			Group  string
			Cancel bool `yaml:"cancel-in-progress"`
		}
	}
	data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows/release.yml"))
	if err != nil || yaml.Unmarshal(data, &policy) != nil || policy.Concurrency.Group != "release-${{ github.ref }}" || policy.Concurrency.Cancel {
		t.Fatal("duplicate tag runs must serialize without cancelling the original publisher")
	}
	cmd := exec.Command("node", "--test", "--test-name-pattern=duplicate tag publication", "scripts/release-completion.test.mjs")
	cmd.Dir = root(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("duplicate tag publication regression: %v\n%s", err, output)
	}
	if !reflect.DeepEqual(w.Jobs["completion"].Needs, []string{"image"}) || w.Jobs["completion"].Uses != "./.github/workflows/release-completion.yml" {
		t.Fatal("completion must follow the verified index")
	}
	if w.Jobs["completion"].With["digest"] != "${{ needs.image.outputs.digest }}" || w.Jobs["completion"].With["tag"] != "v${{ needs.image.outputs.version }}" {
		t.Fatal("completion inputs must bind the immutable index")
	}
	if !reflect.DeepEqual(c.Jobs["assets"].Needs, []string{"prepare", "agentd-darwin"}) {
		t.Fatal("assets must wait for signed binaries and validation, independently of pin proposals")
	}
	image := w.Jobs["image"]
	if image.Environment != "release-pinning" {
		t.Fatal("index publication must retain its original environment boundary")
	}
	if image.Outputs["digest"] != "${{ steps.push.outputs.digest }}" || image.Outputs["version"] != "${{ steps.version.outputs.version }}" {
		t.Fatal("missing image outputs")
	}
	if c.On["workflow_dispatch"] == nil || c.On["workflow_call"] == nil || len(c.On) != 2 {
		t.Fatal("completion needs only reusable/manual entrypoints")
	}
	for _, guard := range []string{"github.event_name == 'workflow_dispatch'", "github.ref == 'refs/heads/main'", "github.repository == 'inspr-at/paimos'"} {
		if !strings.Contains(c.Jobs["prepare"].If, guard) {
			t.Fatalf("missing completion dispatch guard: %s", guard)
		}
	}
	_, draft := named(t, c.Jobs["assets"], "Complete draft with missing signed assets only")
	if draft.Run != "node scripts/release-completion.mjs complete release-source/dist --write" {
		t.Fatal("only additive completion may write release assets")
	}
	for _, name := range []string{"agentd-darwin", "assets"} {
		found := false
		for _, s := range c.Jobs[name].Steps {
			if s.With["path"] == "release-source" && s.With["ref"] == "${{ needs.prepare.outputs.source_sha }}" && s.With["persist-credentials"] == "false" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s does not build the attested tagged source", name)
		}
	}
	if _, ok := w.Jobs["homebrew-tap"]; ok {
		t.Fatal("tap stays on its separate protected coordinator workflow")
	}
}

func TestDarwinRestoreGateRequiresExactReleaseVersion(t *testing.T) {
	j := readWorkflow(t, "release-completion.yml").Jobs["agentd-darwin"]
	for _, kind := range []string{"agentd", "cli"} {
		gateName, assetPrefix, versionPrefix := "Verify darwin signature", "paimos-agentd-darwin-", "paimos-agentd "
		if kind == "cli" {
			gateName, assetPrefix, versionPrefix = "Verify darwin CLI signature and version", "aeon-cli-darwin-", ""
		}
		gateIndex, gate := named(t, j, gateName)
		if gate.If != "" || gate.Env["VERSION"] != "${{ needs.prepare.outputs.version }}" || j.Env["AEON_DEVELOPER_ID_TEAM"] != "P66J39QV6V" {
			t.Fatal("restored and newly signed binaries must verify the exact prepared version and team")
		}
		for i, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/upload-artifact@") && i <= gateIndex {
				t.Fatal("Darwin verification must precede artifact upload")
			}
		}
		const version = "261002120000.0.0"
		for _, arch := range []string{"arm64", "amd64"} {
			for _, tc := range []struct {
				name, embedded, team             string
				signed, probeFails, ok, executed bool
			}{
				{"same-coordinate", version, "P66J39QV6V", true, false, true, true},
				{"same-coordinate-ledger", version + " ledger-v1", "P66J39QV6V", true, false, true, true},
				{"signed-other-coordinate", "261001120000.0.0", "P66J39QV6V", true, false, false, true},
				{"version-suffix", version + "-other", "P66J39QV6V", true, false, false, true},
				{"failed-version-probe", version, "P66J39QV6V", true, true, false, true},
				{"wrong-team", version, "OTHERTEAM00", true, false, false, false},
				{"invalid-signature", version, "P66J39QV6V", false, false, false, false},
			} {
				if kind == "cli" && tc.name == "same-coordinate-ledger" {
					continue
				}
				t.Run(kind+"/"+arch+"/"+tc.name, func(t *testing.T) {
					dir := t.TempDir()
					binDir := filepath.Join(dir, "release-source", "dist")
					if err := os.MkdirAll(binDir, 0700); err != nil {
						t.Fatal(err)
					}
					// Model a valid Developer ID signature independently of the embedded
					// coordinate; only the version probe distinguishes the older release.
					codesign := `#!/bin/bash
case "$1" in
  --verify) [ "$SIGNATURE_OK" = true ] && { [ "$BINARY_KIND" != cli ] || [ "$SIGNED_TEAM" = P66J39QV6V ]; } ;;
  -dv) printf 'TeamIdentifier=%s\n' "$SIGNED_TEAM" >&2 ;;
  *) exit 1 ;;
esac
`
					binary := `#!/bin/bash
[ "$1" = --version ] || exit 1
printf 'executed\n' >> "$TRACE"
printf '%s%s\n' "$BINARY_PREFIX" "$BINARY_VERSION"
[ "$VERSION_FAIL" != true ]
`
					asset := assetPrefix + arch
					for path, content := range map[string]string{filepath.Join(dir, "codesign"): codesign, filepath.Join(binDir, asset): binary} {
						if err := os.WriteFile(path, []byte(content), 0700); err != nil {
							t.Fatal(err)
						}
					}
					trace := filepath.Join(dir, "trace")
					cmd := exec.Command("bash", "-c", gate.Run)
					cmd.Dir = dir
					cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "ASSET_NAME="+asset,
						"BINARY_KIND="+kind, "BINARY_PREFIX="+versionPrefix, "VERSION="+version, "AEON_DEVELOPER_ID_TEAM=P66J39QV6V", "SIGNED_TEAM="+tc.team,
						"SIGNATURE_OK="+map[bool]string{true: "true", false: "false"}[tc.signed], "BINARY_VERSION="+tc.embedded,
						"VERSION_FAIL="+map[bool]string{true: "true", false: "false"}[tc.probeFails], "TRACE="+trace)
					output, err := cmd.CombinedOutput()
					if (err == nil) != tc.ok {
						t.Fatalf("Darwin gate success %v, want %v: %s", err == nil, tc.ok, output)
					}
					if tc.signed && !tc.probeFails && tc.team == "P66J39QV6V" && !tc.ok && !strings.Contains(string(output), "version does not match") {
						t.Fatalf("validly signed stale agent must fail the version check: %s", output)
					}
					_, err = os.Stat(trace)
					if (err == nil) != tc.executed {
						t.Fatal("the version probe must run only after signature and team verification")
					}
				})
			}
		}
	}
}

func TestDarwinHookCLIIsSignedAndRehearsedBeforeAssembly(t *testing.T) {
	w := readWorkflow(t, "release-completion.yml")
	dry := readWorkflow(t, "release-image-check.yml")
	buildAt, build := named(t, w.Jobs["agentd-darwin"], "Build darwin paimos-agentd with LocalAuthentication")
	signAt, sign := named(t, w.Jobs["agentd-darwin"], "Sign and notarize paimos-agentd")
	_, rehearsal := named(t, dry.Jobs["agentd-rehearsal"], "Build darwin paimos-agentd with LocalAuthentication")
	if buildAt >= signAt || nativeBuildCommands(build.Run) != nativeBuildCommands(rehearsal.Run) || !strings.Contains(build.Run, "build-release-binaries.sh cli-darwin") {
		t.Fatal("native rehearsal must build the same CLI and daemon as production")
	}
	for _, key := range []string{"AEON_DARWIN_ARCH", "GOCACHE", "GOMODCACHE", "GOFLAGS"} {
		if build.Env[key] != rehearsal.Env[key] {
			t.Fatalf("native rehearsal differs in %s", key)
		}
	}
	if build.Env["RESTORED_AGENTD"] != "${{ steps.restore.outputs.restored }}" || build.Env["RESTORED_CLI"] != "${{ steps.restore-cli.outputs.restored }}" {
		t.Fatal("only verified restored assets may skip native rebuilding")
	}
	for _, fragment := range []string{
		`sign-notarize.sh --identifier aeon-cli "dist/aeon-cli-darwin-${{ matrix.arch }}"`,
		`codesign --verify --strict -R='anchor apple generic and identifier "aeon-cli" and certificate leaf[subject.OU] = "P66J39QV6V"'`,
	} {
		if !strings.Contains(sign.Run, fragment) {
			t.Fatal("CLI signing requirement missing")
		}
	}
	for _, j := range []job{w.Jobs["agentd-darwin"], dry.Jobs["agentd-rehearsal"]} {
		found := false
		for i, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
				found = true
				if !strings.Contains(s.With["path"], "dist/aeon-cli-darwin-${{ matrix.arch }}") || (j.Environment == "release-signing" && i <= signAt) {
					t.Fatal("CLI uploaded before signing or omitted")
				}
			}
		}
		if !found {
			t.Fatal("missing native artifact handoff")
		}
	}
	_, assemble := named(t, w.Jobs["assets"], "Build linux paimos-agentd and aeon-cli")
	if !strings.Contains(assemble.Run, "build-release-binaries.sh cli-linux") || strings.Contains(assemble.Run, "build-release-binaries.sh cli\n") {
		t.Fatal("assets assembly can overwrite signed Darwin CLI bytes")
	}
}

func TestRehearsalEmbedsItsCandidateVersion(t *testing.T) {
	j := readWorkflow(t, "release-image-check.yml").Jobs["image-dry-run"]
	historyIndex, history := named(t, j, "Release history fixture")
	buildIndex, _ := named(t, j, "Build cached smoke image")
	if historyIndex >= buildIndex || history.Env["VERSION"] != "${{ steps.version.outputs.version }}" || !strings.Contains(history.Run, `-candidate "$VERSION"`) {
		t.Fatal("rehearsal image must embed its own reserved version before building")
	}
	_, published := named(t, readWorkflow(t, "release.yml").Jobs["image-platform"], "Release history")
	if strings.Contains(published.Run, "-candidate") {
		t.Fatal("tagged release builds must keep their normal history generation")
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
	for _, key := range []string{"context", "platforms", "build-args", "cache-from", "build-contexts"} {
		if build.With[key] != push.With[key] || build.With[key] == "" {
			t.Fatalf("smoked and pushed build inputs differ: %s", key)
		}
	}
	if push.With["context"] != "." || push.With["push"] != "true" || push.With["provenance"] != "mode=max" || !strings.Contains(push.With["cache-to"], "type=registry,") || !strings.HasSuffix(push.With["cache-to"], ",mode=max") {
		t.Fatal("pushed image requires generated history, registry cache and provenance")
	}
	if push.With["tags"] != "" || push.With["outputs"] != "type=image,name=ghcr.io/inspr-at/aeon,push-by-digest=true,name-canonical=true,push=true,rewrite-timestamp=true,oci-mediatypes=true" {
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

func TestExportsShareSourceEpoch(t *testing.T) {
	j := readWorkflow(t, "release.yml").Jobs["image-platform"]
	epochIndex, epoch := named(t, j, "Pin both exports to the source epoch")
	buildIndex, build := named(t, j, "Build cached smoke image")
	_, push := named(t, j, "Build and push")
	if epochIndex >= buildIndex || epoch.ID != "source-epoch" || epoch.If != "" || epoch.ContinueOnError || epoch.Env["FROZEN_SOURCE_EPOCH"] != "${{ steps.inputs.outputs.epoch }}" || build.With["build-args"] != push.With["build-args"] {
		t.Fatal("exports do not share an unconditional source epoch")
	}
	if strings.Count(build.With["build-args"], "SOURCE_DATE_EPOCH=${{ steps.inputs.outputs.epoch }}\n") != 1 {
		t.Fatal("exports must share the frozen build-input epoch")
	}
	for _, value := range []string{"1700000000", "1700000100", "", "bad", "1700000000\n1700000100"} {
		dir := t.TempDir()
		stub := "#!/bin/bash\n[[ $1 == show && $2 == -s && $3 == --format=%ct && $4 == HEAD ]] || exit 1\nprintf '%s\\n' \"$FIXTURE_EPOCH\"\n"
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(dir, "output")
		cmd := exec.Command("bash", "-c", epoch.Run)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURE_EPOCH="+value, "FROZEN_SOURCE_EPOCH=1700000000", "GITHUB_OUTPUT="+output)
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
	completion := readWorkflow(t, "release-completion.yml")
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
	if !reflect.DeepEqual(release.Jobs["image-platform"].Permissions, map[string]string{"contents": "write", "actions": "read", "packages": "write", "attestations": "write", "id-token": "write"}) || !reflect.DeepEqual(release.Jobs["image"].Permissions, map[string]string{"contents": "write", "packages": "write", "attestations": "write", "id-token": "write"}) || !reflect.DeepEqual(completion.Jobs["assets"].Permissions, map[string]string{"contents": "write", "packages": "read", "attestations": "read"}) {
		t.Fatal("token writes must be scoped to the image and assets jobs")
	}
	if completion.Jobs["agentd-darwin"].Environment != "release-signing" || !reflect.DeepEqual(completion.Jobs["agentd-darwin"].Permissions, map[string]string{"contents": "write", "packages": "read", "attestations": "read"}) {
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
	if !reflect.DeepEqual(productionBuild.With, dryBuild.With) || productionBuild.Uses != dryBuild.Uses {
		t.Fatal("dry run must exercise the production smoke build")
	}
	tap := readWorkflow(t, "homebrew-tap.yml")
	if _, ok := tap.On["workflow_dispatch"]; !ok || len(tap.On) != 1 || !reflect.DeepEqual(tap.Permissions, map[string]string{"contents": "read", "actions": "read"}) {
		t.Fatal("Homebrew must use read-only main dispatch, never tag-controlled release workflows")
	}
	for _, j := range tap.Jobs {
		if !strings.Contains(j.If, "github.ref == 'refs/heads/main'") || !strings.Contains(j.If, "github.actor_id == '276789'") || j.Environment != "homebrew-tap" {
			t.Fatal("tap secrets require reviewed main-only coordinator dispatch")
		}
		for _, s := range j.Steps {
			if strings.Contains(s.Uses, "actions/checkout@") && (s.With["ref"] != "${{ github.workflow_sha }}" || s.With["persist-credentials"] != "false") {
				t.Fatal("tap code must come from the main workflow commit")
			}
		}
	}
	pin := regexp.MustCompile(`@[a-f0-9]{40}$`)
	for _, w := range []workflow{release, completion, dry, tap, web} {
		for _, j := range w.Jobs {
			for _, s := range j.Steps {
				if s.Uses != "" && !strings.HasPrefix(s.Uses, "./.github/workflows/") && !pin.MatchString(s.Uses) {
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
				"node": `printf '%s\n' "$*" >> "$DOCKER_LOG"
case "$*" in
  *parseRuntimeApkPins*) printf '%s\n' '1.2.3-r0' '0.19.0-r3' ;;
esac`,
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
			} else if !strings.Contains(calls, "scripts/assemble-image.mjs aeon-smoke:fixture") || !strings.Contains(calls, "image rm aeon-smoke:fixture") {
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
	for i, j := range []job{platform, dryPlatform} {
		wantIf := ""
		if i == 0 {
			wantIf = "needs.publication.outputs.publish == 'true'"
		}
		if j.RunsOn != "${{ matrix.runner }}" || j.If != wantIf {
			t.Fatal("fresh coordinates and rehearsals must run on the native matrix")
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
	w := readWorkflow(t, "release-completion.yml")
	pinJob := w.Jobs["pin"]
	if !reflect.DeepEqual(pinJob.Needs, []string{"prepare"}) || pinJob.Environment != "release-pinning" {
		t.Fatal("pin credentials require the verified completion stage")
	}
	verifyIndex, _ := named(t, pinJob, "Reverify index before pin proposal")
	pinIndex, pin := named(t, pinJob, "Propose verified nixcfg deployment pin")
	if pinIndex <= verifyIndex || !pin.ContinueOnError || pin.ID != "pin" || pin.If != "" {
		t.Fatal("optional pin must follow verification without blocking assets")
	}
	for key, expected := range map[string]string{
		"VERSION": "${{ needs.prepare.outputs.version }}", "DIGEST": "${{ needs.prepare.outputs.digest }}", "RELEASE_SOURCE_SHA": "${{ needs.prepare.outputs.source_sha }}",
		"AEON_PIN_BOT_ENABLED": "${{ vars.AEON_PIN_BOT_ENABLED }}", "AEON_PIN_APP_ID": "${{ secrets.AEON_PIN_APP_ID }}", "AEON_PIN_APP_KEY": "${{ secrets.AEON_PIN_APP_KEY }}",
	} {
		if pin.Env[key] != expected {
			t.Fatalf("unbound pin input %s", key)
		}
	}
	for _, fragment := range []string{`args=(scripts/release-pin-pr.mjs)`, `args+=(--write)`, `if ! node "${args[@]}"; then`, "::warning::Deployment pin proposal failed", `>> "$GITHUB_STEP_SUMMARY"`, "exit 1"} {
		if !strings.Contains(pin.Run, fragment) {
			t.Fatalf("missing pin safety %s", fragment)
		}
	}
	for name, j := range w.Jobs {
		for _, s := range j.Steps {
			for key, value := range s.Env {
				if strings.HasPrefix(key, "AEON_PIN_APP_") && (name != "pin" || s.ID != "pin" || !strings.Contains(value, "secrets.AEON_PIN_APP_")) {
					t.Fatal("pin credentials escaped the proposal step")
				}
			}
			if strings.Contains(s.Run, "gh pr merge") || strings.Contains(s.Run, "--auto") || strings.Contains(s.Run, "nixos-rebuild") {
				t.Fatal("completion must never merge or deploy")
			}
		}
	}

	// CI has scalar and list needs; only parse the steps used by this check.
	var ci struct {
		Jobs map[string]struct {
			Steps []step
			If    string
			Needs any
		}
	}
	data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &ci); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ci.Jobs["release-check-run"].Steps {
		found = found || strings.Contains(s.Run, "node --test scripts/release-pin-pr.test.mjs")
	}
	if !found {
		t.Fatal("pin regression tests must run in full draft PR CI")
	}
	worker, gate := ci.Jobs["release-check-run"], ci.Jobs["release-check"]
	if worker.If != "always() && needs.ci-plan.result == 'success' && (needs.ci-plan.outputs.lane == 'full') && needs.tree-reuse.outputs.reuse != 'merge_group' && needs.tree-reuse.outputs.reuse != 'pull_request'" || gate.If != "always()" ||
		!reflect.DeepEqual(gate.Needs, []any{"ci-plan", "release-check-run", "tree-reuse", "cache-prime"}) {
		t.Fatal("pin regressions must retain classified validation and the required aggregate")
	}
}

func TestCurrentWithdrawalLedgerBeforeImagePublication(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	for _, id := range []string{"image-platform", "image"} {
		j := w.Jobs[id]
		guardIndex, guard := named(t, j, "Enforce current release withdrawal ledger")
		pushName := "Build and push"
		if id == "image" {
			pushName = "Publish multi-arch index"
		}
		pushIndex, _ := named(t, j, pushName)
		if guardIndex >= pushIndex || guard.If != "" || guard.ContinueOnError || guard.Run != `node scripts/release-withdrawals.mjs coordinate "$VERSION" --current-main` || guard.Env["VERSION"] != "${{ steps.version.outputs.version }}" || guard.Env["GH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
			t.Fatal("withdrawn coordinate can bypass current-main guard", id)
		}
	}
}
