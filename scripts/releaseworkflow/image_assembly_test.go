// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExternalImageAssemblyAndEvidence(t *testing.T) {
	release := readWorkflow(t, "release.yml").Jobs["image-platform"]
	dry := readWorkflow(t, "release-image-check.yml").Jobs["image-dry-run"]
	for _, name := range []string{"Freeze external build inputs", "Compile web and Go outside Docker", "Prepare pinned runtime closure", "Bind runtime closure digest", "Start assembly and smoke timing", "Record assembly and smoke timing", "Prove two clean image rebuilds", "Upload image assembly evidence"} {
		_, source := named(t, release, name)
		_, counterpart := named(t, dry, name)
		if !reflect.DeepEqual(source, counterpart) {
			t.Fatalf("external assembly rehearsal drift: %s", name)
		}
	}
	for _, j := range []job{release, dry} {
		last := -1
		for _, name := range []string{"Require native platform", "Freeze external build inputs", "Compile web and Go outside Docker", "Prepare pinned runtime closure", "Bind runtime closure digest", "Start assembly and smoke timing", "Build cached smoke image", "Resolve loaded smoke image", "Smoke production image", "Record assembly and smoke timing", "Prove two clean image rebuilds"} {
			index, s := named(t, j, name)
			if index <= last || s.ContinueOnError {
				t.Fatalf("assembly order/gate changed at %s", name)
			}
			last = index
		}
		_, compile := named(t, j, "Compile web and Go outside Docker")
		if compile.Run != "node scripts/build-image-inputs.mjs build" || compile.Env["SOURCE_DATE_EPOCH"] != "${{ steps.inputs.outputs.epoch }}" || compile.Env["VERSION"] != "${{ steps.version.outputs.version }}" || compile.Env["ARCH"] != "${{ matrix.arch }}" {
			t.Fatal("host build inputs are not fixed")
		}
		_, runtime := named(t, j, "Prepare pinned runtime closure")
		if runtime.If != "" || runtime.With["file"] != "scripts/Dockerfile.runtime" || runtime.With["provenance"] != "false" || !strings.Contains(runtime.With["build-args"], "SOURCE_DATE_EPOCH=0") || !strings.Contains(runtime.With["outputs"], "tar=false,rewrite-timestamp=true") {
			t.Fatal("runtime preparation is not a separately frozen closure")
		}
		_, bind := named(t, j, "Bind runtime closure digest")
		if bind.ID != "runtime" || bind.If != "" || len(bind.Env) != 0 || bind.Run != `node scripts/image-evidence.mjs runtime "$RUNNER_TEMP/aeon-runtime" linux/${{ matrix.arch }}` {
			t.Fatal("runtime closure digest is not bound from the prepared layout")
		}
		_, build := named(t, j, "Build cached smoke image")
		if build.With["load"] != "true" || build.With["provenance"] != "false" || strings.Contains(build.With["build-args"], "BUILDKIT_MULTI_PLATFORM") {
			t.Fatal("Docker exporter must load a single-platform result without provenance or forced manifest lists")
		}
		if build.With["outputs"] != "type=docker,rewrite-timestamp=true,oci-mediatypes=true" || !strings.Contains(build.With["build-contexts"], "@${{ steps.runtime.outputs.digest }}") || !strings.Contains(build.With["build-args"], "SOURCE_DATE_EPOCH=${{ steps.inputs.outputs.epoch }}") {
			t.Fatal("assembly lacks immutable base or timestamp normalization")
		}
		_, timing := named(t, j, "Record assembly and smoke timing")
		if timing.If != "always()" || timing.Env["ASSEMBLY_OUTCOME"] != "${{ steps.build.outcome }}" || timing.Env["SMOKE_OUTCOME"] != "${{ steps.smoke.outcome }}" || timing.Run != `node scripts/image-evidence.mjs timing "$RUNNER_TEMP/image-timing" linux/${{ matrix.arch }}` {
			t.Fatal("timing conceals incomplete evidence")
		}
		_, proof := named(t, j, "Prove two clean image rebuilds")
		if proof.If != "" || proof.Env["SMOKE_CONFIG_DIGEST"] != "${{ steps.build.outputs.imageid }}" || proof.Env["RUNTIME_DIGEST"] != "${{ steps.runtime.outputs.digest }}" || !strings.Contains(proof.Run, "reproduce-image.mjs") {
			t.Fatal("proof can skip or cannot bind the smoked runtime inputs")
		}
		_, upload := named(t, j, "Upload image assembly evidence")
		if upload.If != "always()" || upload.With["if-no-files-found"] != "error" || !strings.Contains(upload.With["path"], "image-reproducibility.json") || !strings.Contains(upload.With["path"], "image-timing.json") {
			t.Fatal("timing/proof evidence lost")
		}
	}
}

func TestSinglePlatformProvenanceExport(t *testing.T) {
	release := readWorkflow(t, "release.yml").Jobs["image-platform"]
	dry := readWorkflow(t, "release-image-check.yml").Jobs["image-dry-run"]
	_, smoke := named(t, release, "Build cached smoke image")
	_, push := named(t, release, "Build and push")
	_, export := named(t, dry, "Export production image with provenance locally")
	for _, s := range []step{push, export} {
		if s.With["build-args"] != smoke.With["build-args"] || strings.Contains(s.With["build-args"], "BUILDKIT_MULTI_PLATFORM") || s.With["provenance"] != "mode=max" || s.With["load"] == "true" {
			t.Fatal("provenance export must keep smoke inputs and use an index-capable exporter without loading")
		}
		if s.With["platforms"] != "linux/${{ matrix.arch }}" || !strings.Contains(s.With["outputs"], "rewrite-timestamp=true,oci-mediatypes=true") {
			t.Fatal("export must preserve native platform and reproducible OCI media types")
		}
	}
	// The smoked image is the only thing that may be exported: same context,
	// same digest-bound runtime, on top of the same build arguments.
	for _, s := range []step{push, export} {
		for _, key := range []string{"context", "build-contexts"} {
			if s.With[key] != smoke.With[key] || s.With[key] == "" {
				t.Fatalf("%s differs from the smoke build: %s", s.Name, key)
			}
		}
	}
	if smoke.With["context"] != "." || smoke.With["build-contexts"] != "aeon-runtime=oci-layout://${{ runner.temp }}/aeon-runtime@${{ steps.runtime.outputs.digest }}\n" {
		t.Fatal("smoke build is not bound to the working tree and the runtime digest")
	}
	if export.With["outputs"] != "type=oci,dest=${{ runner.temp }}/release-${{ matrix.arch }}.tar,tar=true,rewrite-timestamp=true,oci-mediatypes=true" {
		t.Fatal("rehearsal provenance must write an explicit local OCI tar")
	}
}

// Every tag build and rehearsal prepares the runtime closure for itself: from
// the pinned recipe, once, without a registry or builder cache, and saves it
// nowhere. The tag build's platform job ends with the digest handoff, so no
// later step can delay or fail publication.
func runtimeClosureProblems(release, dry workflow) []string {
	const assembly = "type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}"
	var problems []string
	for name, w := range map[string]workflow{"release": release, "rehearsal": dry} {
		for id, j := range w.Jobs {
			prepared := 0
			for _, s := range j.Steps {
				label := name + " " + id + "/" + s.Name
				if s.Name == "" {
					label += s.Uses
				}
				if s.With["file"] == "scripts/Dockerfile.runtime" {
					prepared++
					if (id != "image-platform" && id != "image-dry-run") || s.Name != "Prepare pinned runtime closure" || prepared > 1 {
						problems = append(problems, label+": prepares the runtime closure a second time")
					}
					if s.With["no-cache"] != "true" || s.With["cache-from"] != "" || s.With["cache-to"] != "" || s.With["build-contexts"] != "" {
						problems = append(problems, label+": runtime closure is not prepared cold from the pinned recipe alone")
					}
					if s.With["push"] != "" || s.With["load"] != "" || s.With["tags"] != "" || s.With["outputs"] != "type=oci,dest=${{ runner.temp }}/aeon-runtime,tar=false,rewrite-timestamp=true" {
						problems = append(problems, label+": runtime closure must only be written to the local layout")
					}
				} else if strings.Contains(s.With["file"]+s.With["build-contexts"]+s.Run, "Dockerfile.runtime") || strings.Contains(s.Run, "runtime-closure") {
					problems = append(problems, label+": runtime closure has a second source")
				}
				if from := s.With["cache-from"]; from != "" && from != assembly {
					problems = append(problems, label+": reads a registry cache other than the assembly cache")
				}
				to := s.With["cache-to"]
				if to != "" && (name != "release" || s.Name != "Build and push" || to != assembly+",mode=max") {
					problems = append(problems, label+": writes a registry cache other than the assembly cache")
				}
				if name == "rehearsal" && (s.With["push"] == "true" || strings.Contains(s.With["outputs"], "push=true") || strings.Contains(s.With["outputs"], "type=registry")) {
					problems = append(problems, label+": rehearsal writes to a registry")
				}
			}
			if (id == "image-platform" || id == "image-dry-run") && prepared != 1 {
				problems = append(problems, name+" "+id+": runtime closure is not prepared from the pinned recipe")
			}
		}
	}
	steps := release.Jobs["image-platform"].Steps
	for _, s := range steps {
		if s.ContinueOnError {
			problems = append(problems, "release image-platform/"+s.Name+": may not ignore a failure")
		}
	}
	if last := len(steps) - 1; last < 0 || !strings.HasPrefix(steps[last].Uses, "actions/upload-artifact@") || steps[last].With["name"] != "image-digest-${{ matrix.arch }}" || steps[last].If != "" {
		problems = append(problems, "release image-platform: the digest handoff must be the last step")
	}
	return problems
}

func TestRuntimeClosurePreparedColdAndNeverCached(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	release, dry := read("release.yml"), read("release-image-check.yml")
	if problems := runtimeClosureProblems(parseWorkflow(t, release), parseWorkflow(t, dry)); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	const prepare = "          file: scripts/Dockerfile.runtime\n"
	const noCache = "          no-cache: true\n"
	const handoff = "          path: ${{ runner.temp }}/image-digests/${{ matrix.arch }}.txt\n          if-no-files-found: error\n          retention-days: 1\n"
	// Each mutation must be rejected for its own reason.
	for _, tc := range []struct {
		name, file, old, replacement, want string
	}{
		{"builder cache allowed", "release", noCache, "", "release image-platform/Prepare pinned runtime closure: runtime closure is not prepared cold"},
		{"rehearsal builder cache allowed", "dry", noCache, "", "rehearsal image-dry-run/Prepare pinned runtime closure: runtime closure is not prepared cold"},
		{"fixed registry cache read", "release", noCache, noCache + "          cache-from: type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}-runtime\n", "Prepare pinned runtime closure: runtime closure is not prepared cold"},
		{"rehearsal registry cache read", "dry", noCache, noCache + "          cache-from: type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}-runtime\n", "rehearsal image-dry-run/Prepare pinned runtime closure: reads a registry cache"},
		{"restored through a named context", "release", noCache, noCache + "          build-contexts: aeon-runtime-closure=docker-image://ghcr.io/inspr-at/aeon:buildcache-runtime\n", "Prepare pinned runtime closure: runtime closure is not prepared cold"},
		{"restore recipe", "release", prepare, "          file: scripts/Dockerfile.runtime-cache\n", "runtime closure has a second source"},
		{"other recipe", "release", prepare, "          file: Dockerfile\n", "release image-platform: runtime closure is not prepared from the pinned recipe"},
		{"closure pushed", "release", noCache, noCache + "          push: true\n", "runtime closure must only be written to the local layout"},
		{"closure exported to the registry", "release", "type=oci,dest=${{ runner.temp }}/aeon-runtime,tar=false,rewrite-timestamp=true", "type=image,name=ghcr.io/inspr-at/aeon:buildcache-runtime,push=true", "runtime closure must only be written to the local layout"},
		{"saved as a build cache", "release", handoff, handoff + "      - name: Save verified runtime closure\n        uses: docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc\n        with:\n          file: scripts/Dockerfile.runtime\n          outputs: type=cacheonly\n          cache-to: type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}-runtime,mode=max\n", "writes a registry cache other than the assembly cache"},
		{"prepared twice", "release", handoff, handoff + "      - name: Save verified runtime closure\n        uses: docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc\n        with:\n          file: scripts/Dockerfile.runtime\n", "prepares the runtime closure a second time"},
		{"saved by a script", "release", handoff, handoff + "      - name: Save verified runtime closure\n        continue-on-error: true\n        run: node scripts/runtime-closure.mjs save \"$RUNNER_TEMP/aeon-runtime\"\n", "runtime closure has a second source"},
		{"step after the handoff", "release", handoff, handoff + "      - name: Report\n        if: always()\n        run: echo done\n", "the digest handoff must be the last step"},
		{"ignored failure", "release", "      - name: Bind runtime closure digest\n        id: runtime\n", "      - name: Bind runtime closure digest\n        id: runtime\n        continue-on-error: true\n", "Bind runtime closure digest: may not ignore a failure"},
		{"rehearsal saves a cache", "dry", "          cache-from: type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}\n", "          cache-to: type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }},mode=max\n", "writes a registry cache other than the assembly cache"},
		{"rehearsal pushes", "dry", "tar=true,rewrite-timestamp=true,oci-mediatypes=true", "push=true,rewrite-timestamp=true,oci-mediatypes=true", "rehearsal writes to a registry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := release, dry
			if tc.file == "release" {
				r = strings.Replace(r, tc.old, tc.replacement, 1)
			} else {
				d = strings.Replace(d, tc.old, tc.replacement, 1)
			}
			if r == release && d == dry {
				t.Fatal("mutation did not change a workflow")
			}
			problems := strings.Join(runtimeClosureProblems(parseWorkflow(t, r), parseWorkflow(t, d)), "\n")
			if !strings.Contains(problems, tc.want) {
				t.Fatalf("want rejection %q, got %q", tc.want, problems)
			}
		})
	}
}

// Nothing is attested unless the pushed platform manifest carries the smoked
// image config; the rehearsal applies the same check to its local export.
func TestPushedImageMatchesSmokedBuild(t *testing.T) {
	j := readWorkflow(t, "release.yml").Jobs["image-platform"]
	_, smoke := named(t, j, "Build cached smoke image")
	pushAt, push := named(t, j, "Build and push")
	checkAt, check := named(t, j, "Require pushed image to equal the smoked build")
	runtimeAt, _ := named(t, j, "Verify pushed runtime matches smoke")
	attestAt, _ := named(t, j, "Attest pushed image")
	if checkAt != pushAt+1 || runtimeAt != checkAt+1 || attestAt != runtimeAt+1 {
		t.Fatal("the pushed image must be compared directly after the push and before attestation")
	}
	if smoke.ID != "build" || push.ID != "push" || check.If != "" || check.ContinueOnError || check.Uses != "" ||
		check.Run != `node scripts/verify-pushed-image.mjs registry "ghcr.io/inspr-at/aeon@${DIGEST}" linux/${{ matrix.arch }}` ||
		!reflect.DeepEqual(check.Env, map[string]string{"DIGEST": "${{ steps.push.outputs.digest }}", "SMOKE_CONFIG_DIGEST": "${{ steps.build.outputs.imageid }}"}) {
		t.Fatal("the comparison is not bound to the pushed digest and the smoked build")
	}
	_, proof := named(t, j, "Prove two clean image rebuilds")
	if proof.Env["SMOKE_CONFIG_DIGEST"] != check.Env["SMOKE_CONFIG_DIGEST"] {
		t.Fatal("push comparison and reproducibility proof must use the same smoked config")
	}
	dry := readWorkflow(t, "release-image-check.yml").Jobs["image-dry-run"]
	exportAt, export := named(t, dry, "Export production image with provenance locally")
	compareAt, compare := named(t, dry, "Require exported image to equal the smoked build")
	if compareAt != exportAt+1 || compare.If != "" || compare.ContinueOnError || !reflect.DeepEqual(compare.Env, map[string]string{"SMOKE_CONFIG_DIGEST": "${{ steps.build.outputs.imageid }}"}) {
		t.Fatal("rehearsal does not compare its provenance export with the smoked build")
	}
	if !strings.Contains(export.With["outputs"], "dest=${{ runner.temp }}/release-${{ matrix.arch }}.tar,tar=true") {
		t.Fatal("rehearsal export location changed")
	}
	for _, fragment := range []string{
		"set -euo pipefail",
		`tar -xf "$RUNNER_TEMP/release-${{ matrix.arch }}.tar" -C "$RUNNER_TEMP/release-export"`,
		`node scripts/verify-pushed-image.mjs layout "$RUNNER_TEMP/release-export" linux/${{ matrix.arch }}`,
		"node scripts/verify-pushed-image.mjs probe linux/${{ matrix.arch }}",
	} {
		if !strings.Contains(compare.Run, fragment) {
			t.Fatalf("rehearsal comparison lacks %s", fragment)
		}
	}
}

func parseWorkflow(t *testing.T, data string) workflow {
	t.Helper()
	var w workflow
	if err := yaml.Unmarshal([]byte(data), &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// A shipped binary must never be compiled from a restored Go cache: those are
// also written by CI jobs on the self-hosted pool and the go command does not
// re-verify build-cache entries. The rehearsal must carry identical settings.
func goIsolationProblems(release, dry workflow) []string {
	const temp, modules, flags = "${{ runner.temp }}/", "${{ runner.temp }}/go-mod", "-mod=readonly"
	var problems []string
	for name, w := range map[string]workflow{"release": release, "rehearsal": dry} {
		for id, j := range w.Jobs {
			caches := map[string]string{}
			for _, s := range j.Steps {
				label := name + " " + id + "/" + s.Name + s.Uses
				if strings.HasPrefix(s.Uses, "actions/setup-go@") && s.With["cache"] != "false" {
					problems = append(problems, label+": restores a Go cache")
				}
				if strings.HasPrefix(s.Uses, "actions/cache") {
					problems = append(problems, label+": cache action in a release workflow")
				}
				cache, hasCache := s.Env["GOCACHE"]
				mod, hasMod := s.Env["GOMODCACHE"]
				if hasCache {
					if !strings.HasPrefix(cache, temp) || len(cache) == len(temp) || strings.Contains(cache, "..") {
						problems = append(problems, label+": GOCACHE outside RUNNER_TEMP")
					}
					if other, ok := caches[cache]; ok {
						problems = append(problems, label+": steps share a build cache with "+other)
					}
					caches[cache] = s.Name
				}
				if (hasCache || hasMod) && (mod != modules || s.Env["GOFLAGS"] != flags || cache == mod) {
					problems = append(problems, label+": GOMODCACHE or GOFLAGS is not the verified read-only module setup")
				}
			}
		}
	}
	find := func(w workflow, id, name string) (step, bool) {
		for _, s := range w.Jobs[id].Steps {
			if s.Name == name {
				return s, true
			}
		}
		return step{}, false
	}
	for _, pair := range []struct {
		job, name, dryJob, dryName, cache string
		commands                          []string
	}{
		{"agentd-darwin", "Build darwin paimos-agentd with LocalAuthentication", "agentd-rehearsal", "Build darwin paimos-agentd with LocalAuthentication", "go-build-agentd", []string{"bash scripts/build-release-binaries.sh darwin-agentd"}},
		{"image-platform", "Release history", "image-dry-run", "Release history fixture", "go-build-history", []string{"go mod download\n", "go mod verify\n", "go run ./internal/releasehistory/generate"}},
		{"image-platform", "Compile web and Go outside Docker", "image-dry-run", "Compile web and Go outside Docker", "go-build-image", []string{"node scripts/build-image-inputs.mjs build"}},
		// Each proof run creates its own build cache; none may be passed in.
		{"image-platform", "Prove two clean image rebuilds", "image-dry-run", "Prove two clean image rebuilds", "", []string{"node scripts/reproduce-image.mjs"}},
		// One build cache per compilation is named inside the production body.
		{"assets", "Build linux paimos-agentd and aeon-cli", "assets-rehearsal", "Exact production assets assembly", "", nil},
	} {
		label := pair.job + "/" + pair.name
		source, ok := find(release, pair.job, pair.name)
		counterpart, dryOK := find(dry, pair.dryJob, pair.dryName)
		if !ok || !dryOK {
			problems = append(problems, label+": shipped Go step or its rehearsal is missing")
			continue
		}
		want := ""
		if pair.cache != "" {
			want = temp + pair.cache
		}
		if source.Env["GOCACHE"] != want || source.Env["GOMODCACHE"] != modules || source.Env["GOFLAGS"] != flags || source.If != "" || source.ContinueOnError {
			problems = append(problems, label+": not compiled cold from verified modules")
		}
		for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOFLAGS"} {
			if source.Env[key] != counterpart.Env[key] {
				problems = append(problems, label+": rehearsal differs in "+key)
			}
		}
		at := 0
		for _, command := range pair.commands {
			for _, s := range []step{source, counterpart} {
				if !strings.Contains(s.Run, command) {
					problems = append(problems, label+": missing "+strings.TrimSpace(command))
				}
			}
			next := strings.Index(source.Run[at:], command)
			if next < 0 {
				problems = append(problems, label+": modules are not verified before "+strings.TrimSpace(command))
				break
			}
			at += next + len(command)
		}
	}
	if assets, ok := find(release, "assets", "Build linux paimos-agentd and aeon-cli"); ok {
		for _, line := range []string{
			`GOCACHE="$RUNNER_TEMP/go-build-linux-agentd" bash scripts/build-release-binaries.sh linux-agentd` + "\n",
			`GOCACHE="$RUNNER_TEMP/go-build-cli" bash scripts/build-release-binaries.sh cli` + "\n",
		} {
			if strings.Count(assets.Run, line) != 1 {
				problems = append(problems, "assets: every compilation needs its own empty build cache")
			}
		}
		if strings.Count(assets.Run, "build-release-binaries.sh linux-agentd")+strings.Count(assets.Run, "build-release-binaries.sh cli") != 2 {
			problems = append(problems, "assets: a compilation runs without a named build cache")
		}
	}
	return problems
}

func TestShippedGoBuildsCompileColdFromVerifiedModules(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root(t), ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	release, dry := read("release.yml"), read("release-image-check.yml")
	if problems := goIsolationProblems(parseWorkflow(t, release), parseWorkflow(t, dry)); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	setups := 0
	for _, w := range []workflow{parseWorkflow(t, release), parseWorkflow(t, dry)} {
		for _, id := range []string{"agentd-darwin", "image-platform", "assets", "image-dry-run", "agentd-rehearsal", "assets-rehearsal"} {
			for _, s := range w.Jobs[id].Steps {
				if strings.HasPrefix(s.Uses, "actions/setup-go@") {
					setups++
				}
			}
		}
	}
	if setups != 6 {
		t.Fatalf("expected one setup-go in each of the six Go jobs, found %d", setups)
	}
	// Each mutation must be rejected for its own reason.
	for _, tc := range []struct {
		name, file, old, replacement, want string
	}{
		{"cache enabled", "release", "          cache: false\n", "          cache: true\n", "restores a Go cache"},
		{"cache default", "release", "          cache: false\n", "", "restores a Go cache"},
		{"rehearsal cache default", "dry", "          cache: false\n", "", "restores a Go cache"},
		{"cache action", "release", "      - name: Release checks\n", "      - uses: actions/cache@0000000000000000000000000000000000000000\n      - name: Release checks\n", "cache action in a release workflow"},
		{"default build cache", "release", "GOCACHE: ${{ runner.temp }}/go-build-agentd", "GOCACHE: /Users/runner/Library/Caches/go-build", "GOCACHE outside RUNNER_TEMP"},
		{"escaping build cache", "release", "GOCACHE: ${{ runner.temp }}/go-build-agentd", "GOCACHE: ${{ runner.temp }}/../go-build", "GOCACHE outside RUNNER_TEMP"},
		{"history and image share", "release", "GOCACHE: ${{ runner.temp }}/go-build-image", "GOCACHE: ${{ runner.temp }}/go-build-history", "steps share a build cache"},
		{"no build cache", "release", "          GOCACHE: ${{ runner.temp }}/go-build-image\n", "", "image-platform/Compile web and Go outside Docker: not compiled cold"},
		{"default module cache", "release", "          GOMODCACHE: ${{ runner.temp }}/go-mod\n          GOFLAGS: -mod=readonly\n        run: node scripts/build-image-inputs.mjs build", "          GOFLAGS: -mod=readonly\n        run: node scripts/build-image-inputs.mjs build", "GOMODCACHE or GOFLAGS"},
		{"writable modules", "release", "GOFLAGS: -mod=readonly", "GOFLAGS: -mod=mod", "GOMODCACHE or GOFLAGS"},
		{"history unverified", "release", "          go mod verify\n", "", "image-platform/Release history: missing go mod verify"},
		{"history verified late", "release", "          go mod download\n          go mod verify\n          go run ./internal/releasehistory/generate -repo . -repository \"${GITHUB_REPOSITORY}\"\n", "          go mod download\n          go run ./internal/releasehistory/generate -repo . -repository \"${GITHUB_REPOSITORY}\"\n          go mod verify\n", "modules are not verified before go run"},
		{"proof given a build cache", "release", "          # Each proof run creates its own empty GOCACHE; none is passed here.\n", "          GOCACHE: ${{ runner.temp }}/go-build-proof\n", "image-platform/Prove two clean image rebuilds: not compiled cold"},
		{"assets share one cache", "release", `GOCACHE="$RUNNER_TEMP/go-build-cli" bash`, `GOCACHE="$RUNNER_TEMP/go-build-linux-agentd" bash`, "every compilation needs its own empty build cache"},
		{"assets unnamed cache", "release", `GOCACHE="$RUNNER_TEMP/go-build-cli" bash scripts/build-release-binaries.sh cli`, "bash scripts/build-release-binaries.sh cli", "every compilation needs its own empty build cache"},
		{"rehearsal drifts", "dry", "GOCACHE: ${{ runner.temp }}/go-build-image", "GOCACHE: ${{ runner.temp }}/go-build-other", "rehearsal differs in GOCACHE"},
		{"rehearsal assets without modules", "dry", "      - name: Exact production assets assembly\n        env:\n          GOMODCACHE: ${{ runner.temp }}/go-mod\n          GOFLAGS: -mod=readonly\n", "      - name: Exact production assets assembly\n", "rehearsal differs in GOMODCACHE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := release, dry
			if tc.file == "release" {
				r = strings.Replace(r, tc.old, tc.replacement, 1)
			} else {
				d = strings.Replace(d, tc.old, tc.replacement, 1)
			}
			if r == release && d == dry {
				t.Fatal("mutation did not change a workflow")
			}
			problems := strings.Join(goIsolationProblems(parseWorkflow(t, r), parseWorkflow(t, d)), "\n")
			if !strings.Contains(problems, tc.want) {
				t.Fatalf("want rejection %q, got %q", tc.want, problems)
			}
		})
	}
}

// Execute the real release-binaries script against recording stand-ins for go
// and file: in GitHub Actions it must refuse anything but an empty build cache
// and a module cache inside RUNNER_TEMP, and verify modules before compiling.
func TestReleaseBinariesRefuseWarmOrUnverifiedGo(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(root(t), "scripts/build-release-binaries.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const goStub = `#!/bin/bash
printf '%s|%s|%s|%s\n' "$*" "${GOCACHE:-}" "${GOMODCACHE:-}" "${GOFLAGS:-}" >> "$GO_LOG"
case "$1" in
  mod) [ "$2" != "${GO_FAIL:-}" ] || exit 1 ;;
  build)
    out=""
    while [ $# -gt 0 ]; do if [ "$1" = -o ]; then out="$2"; fi; shift; done
    printf 'CGO_ENABLED=%s\nGOOS=%s\nGOARCH=%s\n' "$CGO_ENABLED" "$GOOS" "$GOARCH" > "$out"
    if [ -n "${GOCACHE:-}" ]; then mkdir -p "$GOCACHE" && : > "$GOCACHE/compiled"; fi
    ;;
  version) cat "$3" ;;
esac
`
	run := func(t *testing.T, mode string, vars ...string) (string, string, error) {
		t.Helper()
		dir := t.TempDir()
		for path, body := range map[string]string{
			"repo/scripts/build-release-binaries.sh": string(script),
			"repo/version.json":                      `{"version":"261005120000.0.0"}`,
			"bin/go":                                 goStub,
			"bin/file":                               "#!/bin/bash\necho \"$1: ELF executable, statically linked\"\n",
		} {
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		log := filepath.Join(dir, "go.log")
		cmd := exec.Command("/bin/bash", filepath.Join(dir, "repo/scripts/build-release-binaries.sh"), mode)
		// The surrounding CI job has its own Actions and Go settings; none may leak in.
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			switch key {
			case "GITHUB_ACTIONS", "RUNNER_TEMP", "GOCACHE", "GOMODCACHE", "GOFLAGS", "VERSION", "AEON_DEVELOPER_ID_TEAM", "AEON_DARWIN_ARCH", "PATH":
			default:
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "GO_LOG="+log)
		for _, v := range vars {
			cmd.Env = append(cmd.Env, strings.ReplaceAll(v, "$TEMP", dir))
		}
		output, err := cmd.CombinedOutput()
		calls, _ := os.ReadFile(log)
		return strings.ReplaceAll(string(calls), dir, "$TEMP"), string(output), err
	}
	hosted := []string{"GITHUB_ACTIONS=true", "RUNNER_TEMP=$TEMP/runner", "GOCACHE=$TEMP/runner/go-build-cli", "GOMODCACHE=$TEMP/runner/go-mod"}
	t.Run("cold hosted build verifies modules first", func(t *testing.T) {
		calls, output, err := run(t, "cli", hosted...)
		if err != nil {
			t.Fatalf("%v: %s", err, output)
		}
		lines := strings.Split(strings.TrimSpace(calls), "\n")
		const settings = "|$TEMP/runner/go-build-cli|$TEMP/runner/go-mod|-mod=readonly"
		if len(lines) != 10 || lines[0] != "mod download"+settings || lines[1] != "mod verify"+settings {
			t.Fatalf("modules must be downloaded and verified before any build: %q", lines)
		}
		builds := 0
		for _, line := range lines[2:] {
			if !strings.HasSuffix(line, settings) {
				t.Fatalf("go ran outside the cold caches or with writable modules: %s", line)
			}
			if strings.HasPrefix(line, "build ") {
				builds++
			}
		}
		if builds != 4 {
			t.Fatalf("want four CLI builds, got %d: %q", builds, lines)
		}
	})
	for _, tc := range []struct {
		name, want string
		vars       []string
	}{
		{"no build cache", "GOCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], hosted[3]}},
		{"no module cache", "GOMODCACHE must be a directory inside RUNNER_TEMP", hosted[:3]},
		{"default build cache", "GOCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], "GOCACHE=$TEMP/home/.cache/go-build", hosted[3]}},
		{"default module cache", "GOMODCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], hosted[2], "GOMODCACHE=$TEMP/home/go/pkg/mod"}},
		{"runner temp itself", "GOCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], "GOCACHE=$TEMP/runner", hosted[3]}},
		{"sibling with the same prefix", "GOCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], "GOCACHE=$TEMP/runner-other/go-build", hosted[3]}},
		{"escape through dot-dot", "GOCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], "GOCACHE=$TEMP/runner/../home/go-build", hosted[3]}},
		{"relative build cache", "GOCACHE must be a directory inside RUNNER_TEMP", []string{hosted[0], hosted[1], "GOCACHE=go-build", hosted[3]}},
		{"one directory for both", "GOCACHE and GOMODCACHE must differ", []string{hosted[0], hosted[1], hosted[2], "GOMODCACHE=$TEMP/runner/go-build-cli"}},
		{"no runner temp", "RUNNER_TEMP is not set", []string{hosted[0], hosted[2], hosted[3]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, output, err := run(t, "cli", tc.vars...)
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("want rejection %q, got %v: %s", tc.want, err, output)
			}
			if calls != "" {
				t.Fatalf("go ran before the caches were accepted: %s", calls)
			}
		})
	}
	t.Run("used build cache", func(t *testing.T) {
		dir := t.TempDir()
		used := filepath.Join(dir, "go-build-cli")
		if err := os.MkdirAll(used, 0o700); err != nil {
			t.Fatal(err)
		}
		vars := []string{"GITHUB_ACTIONS=true", "RUNNER_TEMP=" + dir, "GOCACHE=" + used, "GOMODCACHE=" + filepath.Join(dir, "go-mod")}
		if calls, output, err := run(t, "cli", vars...); err != nil || !strings.Contains(calls, "mod verify") {
			t.Fatalf("an existing empty directory is still cold: %v: %s", err, output)
		}
		// The stand-in left an entry behind, exactly like a restored cache.
		calls, output, err := run(t, "cli", vars...)
		if err == nil || !strings.Contains(output, "GOCACHE is not empty; a shipped binary needs a cold build cache") || calls != "" {
			t.Fatalf("a used build cache must stop the build before go runs: %v: %s: %s", err, output, calls)
		}
		if err := os.WriteFile(filepath.Join(dir, "not-a-directory"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		vars[2] = "GOCACHE=" + filepath.Join(dir, "not-a-directory")
		if calls, output, err := run(t, "cli", vars...); err == nil || !strings.Contains(output, "GOCACHE is not empty") || calls != "" {
			t.Fatalf("a file in place of the build cache must stop the build: %v: %s: %s", err, output, calls)
		}
	})
	for _, failing := range []string{"download", "verify"} {
		t.Run("failed module "+failing+" stops the build", func(t *testing.T) {
			calls, output, err := run(t, "linux-agentd", append([]string{"GO_FAIL=" + failing}, hosted...)...)
			if err == nil || strings.Contains(calls, "build ") || !strings.Contains(calls, "mod "+failing) {
				t.Fatalf("a failed go mod %s must stop before compiling: %v: %s: %s", failing, err, output, calls)
			}
		})
	}
	t.Run("local build keeps its caches but still verifies read-only modules", func(t *testing.T) {
		calls, output, err := run(t, "linux-agentd")
		if err != nil {
			t.Fatalf("%v: %s", err, output)
		}
		lines := strings.Split(strings.TrimSpace(calls), "\n")
		if len(lines) != 6 || lines[0] != "mod download|||-mod=readonly" || lines[1] != "mod verify|||-mod=readonly" || !strings.HasPrefix(lines[2], "build ") {
			t.Fatalf("local build order or flags changed: %q", lines)
		}
	})
	t.Run("checking signed binaries compiles nothing", func(t *testing.T) {
		calls, output, err := run(t, "verify-darwin", "GITHUB_ACTIONS=true")
		if err == nil || !strings.Contains(output, "missing dist/paimos-agentd-darwin-arm64") || calls != "" {
			t.Fatalf("verify-darwin must not need or touch Go caches: %v: %s: %s", err, output, calls)
		}
	})
}

// The draft notes name each architecture's runtime closure without adding a
// second "Digest: " line, which scripts/verify-live.mjs requires to be unique.
func TestDraftNotesNameRuntimeClosures(t *testing.T) {
	w := readWorkflow(t, "release.yml")
	platform, index := w.Jobs["image-platform"], w.Jobs["image"]
	_, bind := named(t, platform, "Bind runtime closure digest")
	_, draft := named(t, w.Jobs["assets"], "Create draft GitHub release with signed assets")
	for _, arch := range []string{"amd64", "arm64"} {
		key := "runtime_" + arch
		if bind.ID != "runtime" || platform.Outputs[key] != "${{ steps.runtime.outputs."+key+" }}" || index.Outputs[key] != "${{ needs.image-platform.outputs."+key+" }}" {
			t.Fatalf("runtime closure digest for %s does not reach the image job", arch)
		}
		env := "RUNTIME_" + strings.ToUpper(arch)
		if draft.Env[env] != "${{ needs.image.outputs."+key+" }}" || strings.Count(draft.Run, `"Runtime closure linux/`+arch+`: ${`+env+`:-unknown}"`) != 1 {
			t.Fatalf("draft notes do not name the %s runtime closure", arch)
		}
	}
	if len(platform.Outputs) != 2 || strings.Count(draft.Run, `"Digest: `) != 1 || strings.Count(draft.Run, "Digest: ") != 1 {
		t.Fatal("release notes must keep exactly one Digest line, for the release index")
	}
}
