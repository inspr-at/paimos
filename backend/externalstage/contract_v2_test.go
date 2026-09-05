// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"errors"
	"fmt"
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
		{"ambiguous-calendar", func(a *ArtifactEvidenceV2) { a.Version = "26.09.05.09.30" }},
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
