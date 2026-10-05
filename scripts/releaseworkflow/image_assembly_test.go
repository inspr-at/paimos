// SPDX-License-Identifier: AGPL-3.0-only
package releaseworkflow

import (
	"reflect"
	"strings"
	"testing"
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
		_, build := named(t, j, "Build cached smoke image")
		if build.With["load"] != "true" || build.With["provenance"] != "false" || strings.Contains(build.With["build-args"], "BUILDKIT_MULTI_PLATFORM") {
			t.Fatal("Docker exporter must load a single-platform result without provenance or forced manifest lists")
		}
		if build.With["outputs"] != "type=docker,rewrite-timestamp=true,oci-mediatypes=true" || !strings.Contains(build.With["build-contexts"], "@${{ steps.runtime.outputs.digest }}") || !strings.Contains(build.With["build-args"], "SOURCE_DATE_EPOCH=${{ steps.inputs.outputs.epoch }}") {
			t.Fatal("assembly lacks immutable base or timestamp normalization")
		}
		_, timing := named(t, j, "Record assembly and smoke timing")
		if timing.If != "always()" || timing.Env["ASSEMBLY_OUTCOME"] != "${{ steps.build.outcome }}" || timing.Env["SMOKE_OUTCOME"] != "${{ steps.smoke.outcome }}" || !strings.Contains(timing.Run, "image-evidence.mjs timing") {
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
	if export.With["outputs"] != "type=oci,dest=${{ runner.temp }}/release-${{ matrix.arch }}.tar,tar=true,rewrite-timestamp=true,oci-mediatypes=true" {
		t.Fatal("rehearsal provenance must write an explicit local OCI tar")
	}
}

func TestRuntimeCacheSavedAfterSmoke(t *testing.T) {
	j := readWorkflow(t, "release.yml").Jobs["image-platform"]
	proofAt, _ := named(t, j, "Prove two clean image rebuilds")
	saveAt, save := named(t, j, "Save verified runtime closure")
	pushAt, _ := named(t, j, "Build and push")
	_, prepare := named(t, j, "Prepare pinned runtime closure")
	if !(proofAt < saveAt && saveAt < pushAt) || save.If != "" || save.With["outputs"] != "type=cacheonly" || save.With["cache-to"] != "type=registry,ref=ghcr.io/inspr-at/aeon:${{ matrix.cache }}-runtime,mode=max" {
		t.Fatal("runtime cache must export only after the proven smoke gate, per architecture")
	}
	for _, key := range []string{"file", "context", "platforms", "cache-from", "build-args"} {
		if save.With[key] != prepare.With[key] || save.With[key] == "" {
			t.Fatalf("runtime cache preparation differs: %s", key)
		}
	}
	dry := readWorkflow(t, "release-image-check.yml").Jobs["image-dry-run"]
	for _, s := range dry.Steps {
		if s.Name == "Save verified runtime closure" || s.With["cache-to"] != "" {
			t.Fatal("rehearsal must not save a runtime closure")
		}
	}
}
