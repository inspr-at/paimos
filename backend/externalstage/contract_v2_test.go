// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateArtifactEvidenceV2RequiresExplicitSchemeAndImmutableManifest(t *testing.T) {
	valid := ArtifactEvidenceV2{
		VersionScheme: VersionSchemeINSPRCalendar, Version: "26.09.05.09.30.01",
		ReleaseChannel: "stable", ReleaseSequence: 260905093001,
		Digest: "sha256:" + fmt.Sprintf("%064x", 876), CommitDigest: fmt.Sprintf("%040x", 876),
		ReleaseManifestCoordinate: "ghcr:inspr-at/pharos/releases/26.09.05.09.30.01",
		ReleaseManifestDigest:     "sha256:" + fmt.Sprintf("%064x", 877),
	}
	if err := validateArtifactEvidenceV2(valid, []byte("not-present")); err != nil {
		t.Fatalf("valid calendar identity: %v", err)
	}
	legacy := valid
	legacy.VersionScheme = VersionSchemeLegacy
	legacy.Version = "0.1.95"
	legacy.ReleaseSequence = 195
	if err := validateArtifactEvidenceV2(legacy, nil); err != nil {
		t.Fatalf("valid legacy identity: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ArtifactEvidenceV2)
	}{
		{"missing-scheme", func(a *ArtifactEvidenceV2) { a.VersionScheme = "" }},
		{"unknown-scheme", func(a *ArtifactEvidenceV2) { a.VersionScheme = "semver" }},
		{"noncanonical-five-part-calendar", func(a *ArtifactEvidenceV2) { a.Version = "26.09.05.09.30" }},
		{"invalid-calendar", func(a *ArtifactEvidenceV2) { a.Version = "26.02.30" }},
		{"missing-channel", func(a *ArtifactEvidenceV2) { a.ReleaseChannel = "" }},
		{"negative-sequence", func(a *ArtifactEvidenceV2) { a.ReleaseSequence = -1 }},
		{"mutable-coordinate", func(a *ArtifactEvidenceV2) { a.ReleaseManifestCoordinate = "latest" }},
		{"invalid-manifest-digest", func(a *ArtifactEvidenceV2) { a.ReleaseManifestDigest = "sha256:1234" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := validateArtifactEvidenceV2(candidate, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestINSPRCalendarVersionIsExactAndGregorian(t *testing.T) {
	for _, value := range []string{"26.01.01", "26.12.31", "28.02.29", "26.09.05.23.59.59"} {
		if !validINSPRCalendarVersion(value) {
			t.Fatalf("valid version rejected: %s", value)
		}
	}
	for _, value := range []string{"2026.09.05", "26.9.05", "26.09.5", "26.09.05.12", "26.09.05.24.00.00", "27.02.29", "26.13.01", "v26.09.05"} {
		if validINSPRCalendarVersion(value) {
			t.Fatalf("invalid version accepted: %s", value)
		}
	}
}

// PAI-979: INSPR calendar v2 identities are validated against the exact
// YYMMDDhhmmss.0.0 grammar and real UTC dates; v1 spellings never pass under
// the v2 scheme and vice versa, and unknown schemes fail closed.
func TestValidateArtifactEvidenceV2AcceptsINSPRCalendarV2(t *testing.T) {
	base := ArtifactEvidenceV2{
		VersionScheme: VersionSchemeINSPRCalendarV2, Version: "260910081500.0.0",
		ReleaseChannel: "stable", ReleaseSequence: 260910081500,
		Digest: "sha256:" + strings.Repeat("1", 64), CommitDigest: strings.Repeat("a", 40),
		ReleaseManifestCoordinate: "ghcr:inspr-at/paimos/releases/260910081500.0.0",
		ReleaseManifestDigest:     "sha256:" + strings.Repeat("2", 64),
	}
	if err := validateArtifactEvidenceV2(base, nil); err != nil {
		t.Fatalf("valid calendar v2 identity: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ArtifactEvidenceV2)
	}{
		{"v1-spelling-under-v2", func(a *ArtifactEvidenceV2) { a.Version = "26.09.10.08.15.00" }},
		{"v2-spelling-under-v1", func(a *ArtifactEvidenceV2) { a.VersionScheme = VersionSchemeINSPRCalendar }},
		{"unknown-scheme", func(a *ArtifactEvidenceV2) { a.VersionScheme = "inspr-calendar-v3" }},
		{"empty-scheme", func(a *ArtifactEvidenceV2) { a.VersionScheme = "" }},
		{"ten-digit-major", func(a *ArtifactEvidenceV2) { a.Version = "2609100815.0.0" }},
		{"fourteen-digit-major", func(a *ArtifactEvidenceV2) { a.Version = "20260910081500.0.0" }},
		{"missing-constant", func(a *ArtifactEvidenceV2) { a.Version = "260910081500" }},
		{"patch-bump", func(a *ArtifactEvidenceV2) { a.Version = "260910081500.0.1" }},
		{"minor-bump", func(a *ArtifactEvidenceV2) { a.Version = "260910081500.1.0" }},
		{"prerelease-suffix", func(a *ArtifactEvidenceV2) { a.Version = "260910081500.0.0-rc1" }},
		{"build-suffix", func(a *ArtifactEvidenceV2) { a.Version = "260910081500.0.0+g39d0b59" }},
		{"leading-zero-year", func(a *ArtifactEvidenceV2) { a.Version = "090910081500.0.0" }},
		{"impossible-day", func(a *ArtifactEvidenceV2) { a.Version = "260230081500.0.0" }},
		{"hour-24", func(a *ArtifactEvidenceV2) { a.Version = "260910240000.0.0" }},
		{"second-60", func(a *ArtifactEvidenceV2) { a.Version = "260910081560.0.0" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			tc.mutate(&a)
			if err := validateArtifactEvidenceV2(a, nil); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestINSPRCalendarV2VersionIsExactAndGregorian(t *testing.T) {
	for _, value := range []string{"260910081500.0.0", "261231235959.0.0", "280229120000.0.0", "100101000000.0.0", "991231235959.0.0"} {
		if !validINSPRCalendarV2Version(value) {
			t.Fatalf("expected valid v2 coordinate: %s", value)
		}
		if validINSPRCalendarVersion(value) {
			t.Fatalf("v2 coordinate must not pass the v1 grammar: %s", value)
		}
	}
	for _, value := range []string{"26.09.10", "26.09.10.08.15.00", "5.21.0", "260229120000.0.0", "260431120000.0.0", "261301000000.0.0", "000101000000.0.0", " 260910081500.0.0", "260910081500.0.0 ", "v260910081500.0.0", "260910081500.00.0", "260910081500.0.00"} {
		if validINSPRCalendarV2Version(value) {
			t.Fatalf("expected invalid v2 coordinate: %q", value)
		}
	}
	// Mixed-era readers discriminate on the scheme, never on shape.
	if ValidVersionForScheme(VersionSchemeINSPRCalendar, "260910081500.0.0") || ValidVersionForScheme(VersionSchemeINSPRCalendarV2, "26.09.10") {
		t.Fatal("scheme discrimination leaked across eras")
	}
	if ValidVersionForScheme("", "260910081500.0.0") || ValidVersionForScheme("semver", "5.21.0") {
		t.Fatal("unknown scheme must fail closed")
	}
	if !ValidVersionForScheme(VersionSchemeLegacy, "5.21.0") {
		t.Fatal("legacy spellings stay opaque under the legacy scheme")
	}
}
