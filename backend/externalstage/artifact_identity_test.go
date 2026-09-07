// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

import "testing"

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

func TestBuiltOwnerArtifactCompleteRequiresTypedFields(t *testing.T) {
	var artifact BuiltOwnerArtifact
	if artifact.Complete() {
		t.Fatal("empty identity was complete")
	}
}
