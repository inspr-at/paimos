// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestParseReleaseIdentityRejectsGenericExternalRef(t *testing.T) {
	if _, _, _, _, ok := ParseReleaseIdentity("inspr-calendar-v1:stable:260907120000:26.09.07.12.00.00"); ok {
		t.Fatal("colon-delimited external_ref parsed as release identity")
	}
	if _, _, _, _, ok := ParseReleaseIdentity("ghcr:inspr-at/pharos/releases/26.09.07.12.00.00"); ok {
		t.Fatal("registry coordinate parsed as release identity")
	}
	got := FormatReleaseIdentity(VersionSchemeINSPRCalendar, "stable", 260907120000, "26.09.07.12.00.00")
	scheme, channel, version, sequence, ok := ParseReleaseIdentity(got)
	if !ok || scheme != string(VersionSchemeINSPRCalendar) || channel != "stable" || sequence != 260907120000 || version != "26.09.07.12.00.00" {
		t.Fatalf("typed identity parse=%q %q %q %d ok=%v", scheme, channel, version, sequence, ok)
	}
}

func TestBuiltOwnerArtifactCompleteRequiresReleaseSetNotIndex(t *testing.T) {
	config, _ := hex.DecodeString(strings.Repeat("33", 32))
	index, _ := hex.DecodeString(strings.Repeat("44", 32))
	releaseSet, _ := hex.DecodeString(strings.Repeat("66", 32))
	artifact := BuiltOwnerArtifact{
		Digest: config, OCIIndex: index, Commit: strings.Repeat("a", 40),
		Coordinate: "ghcr:inspr-at/pharos/releases/26.09.07.12.00.00",
		Scheme:     string(VersionSchemeINSPRCalendar), Channel: "stable",
		Sequence: 260907120000, Version: "26.09.07.12.00.00",
	}
	if artifact.Complete() {
		t.Fatal("OCI index completed release identity")
	}
	artifact.ReleaseManifest = releaseSet
	if !artifact.Complete() {
		t.Fatal("typed release-set identity was incomplete")
	}
}

func TestApplyImplementationEvidenceKeepsDigestClassesDistinct(t *testing.T) {
	var expected BuiltOwnerArtifact
	config := strings.Repeat("33", 32)
	index := strings.Repeat("44", 32)
	releaseSet := strings.Repeat("66", 32)
	if err := applyImplementationEvidence(&expected, "artifact", "digest", "", config); err != nil {
		t.Fatal(err)
	}
	if err := applyImplementationEvidence(&expected, "artifact", "external_ref", FormatOCIManifestRef(index), ""); err != nil {
		t.Fatal(err)
	}
	if err := applyImplementationEvidence(&expected, "artifact", "external_ref", FormatReleaseManifestRef(releaseSet), ""); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(expected.Digest) != config || hex.EncodeToString(expected.OCIIndex) != index ||
		hex.EncodeToString(expected.ReleaseManifest) != releaseSet {
		t.Fatalf("digest classes mixed: %+v", expected)
	}
}

func TestApplyImplementationEvidenceRejectsMalformedRecognizedPrefixes(t *testing.T) {
	cases := []string{
		"release-manifest:sha256:notadigest",
		"oci-manifest:sha256:zzzz",
		"release-coordinate:latest",
		"inspr-release-v1:not/a/tuple",
		"inspr-release-v1:inspr-calendar-v1:stable:1:26.09.07.12.00.00",
	}
	for _, value := range cases {
		var expected BuiltOwnerArtifact
		if err := applyImplementationEvidence(&expected, "artifact", "external_ref", value, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q err=%v", value, err)
		}
		if expected.Complete() || len(expected.ReleaseManifest) != 0 || len(expected.OCIIndex) != 0 || expected.Coordinate != "" || expected.Scheme != "" {
			t.Fatalf("%q fell through: %+v", value, expected)
		}
	}
}

func TestApplyImplementationEvidenceIgnoresGenericExternalRef(t *testing.T) {
	var expected BuiltOwnerArtifact
	if err := applyImplementationEvidence(&expected, "artifact", "external_ref", "suite:not-release-identity", ""); err != nil {
		t.Fatal(err)
	}
	if expected.Complete() || len(expected.ReleaseManifest) != 0 {
		t.Fatalf("generic external_ref filled identity: %+v", expected)
	}
}

// PAI-979: typed release identities carry the v2 scheme end to end and reject
// cross-era spellings.
func TestParseReleaseIdentityINSPRCalendarV2(t *testing.T) {
	got := FormatReleaseIdentity(VersionSchemeINSPRCalendarV2, "stable", 260910081500, "260910081500.0.0")
	scheme, channel, version, sequence, ok := ParseReleaseIdentity(got)
	if !ok || scheme != string(VersionSchemeINSPRCalendarV2) || channel != "stable" || sequence != 260910081500 || version != "260910081500.0.0" {
		t.Fatalf("v2 identity roundtrip=%q scheme=%s channel=%s version=%s sequence=%d ok=%v", got, scheme, channel, version, sequence, ok)
	}
	for _, value := range []string{
		"inspr-release-v1:inspr-calendar-v2/stable/1/26.09.10.08.15.00",
		"inspr-release-v1:inspr-calendar-v1/stable/1/260910081500.0.0",
		"inspr-release-v1:inspr-calendar-v3/stable/1/260910081500.0.0",
		"inspr-release-v1:inspr-calendar-v2/stable/1/260910081500.0.0-rc1",
	} {
		if _, _, _, _, ok := ParseReleaseIdentity(value); ok {
			t.Fatalf("cross-era or unknown identity accepted: %s", value)
		}
	}
	artifact := BuiltOwnerArtifact{
		Digest: make([]byte, 32), ReleaseManifest: make([]byte, 32),
		Commit:     strings.Repeat("a", 40),
		Coordinate: "ghcr:inspr-at/paimos/releases/260910081500.0.0",
		Scheme:     string(VersionSchemeINSPRCalendarV2), Channel: "stable",
		Sequence: 260910081500, Version: "260910081500.0.0",
	}
	if !artifact.Complete() {
		t.Fatal("complete v2 built artifact rejected")
	}
	artifact.Version = "26.09.10"
	if artifact.Complete() {
		t.Fatal("v1 spelling accepted under the v2 scheme")
	}
}
