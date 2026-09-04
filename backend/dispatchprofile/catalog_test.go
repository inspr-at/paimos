// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package dispatchprofile

import "testing"

func TestCatalogIsStableClosedAndDetached(t *testing.T) {
	profiles := List()
	if len(profiles) != 7 {
		t.Fatalf("profile count = %d", len(profiles))
	}
	for index, profile := range profiles {
		if err := Validate(profile); err != nil {
			t.Fatalf("profile %q: %v", profile.ID, err)
		}
		if index > 0 && profiles[index-1].ID >= profile.ID {
			t.Fatal("catalog is not in stable id order")
		}
		resolved, err := Resolve(profile.ID, profile.Version, profile.Harness)
		if err != nil || resolved != profile {
			t.Fatalf("resolve %q = %#v, %v", profile.ID, resolved, err)
		}
	}
	profiles[0].Model = "tampered"
	if fresh := List(); fresh[0].Model == "tampered" {
		t.Fatal("List leaked mutable catalog storage")
	}
}

func TestResolveRejectsEveryUnpinnedAxis(t *testing.T) {
	profile := List()[0]
	for _, test := range []struct{ id, version, harness string }{
		{"missing", profile.Version, profile.Harness},
		{profile.ID, "latest", profile.Harness},
		{profile.ID, profile.Version, "other"},
	} {
		if _, err := Resolve(test.id, test.version, test.harness); err == nil {
			t.Fatalf("Resolve(%q,%q,%q) succeeded", test.id, test.version, test.harness)
		}
	}
}

func TestValidateSnapshotDoesNotRequireLiveCatalogMembership(t *testing.T) {
	profile := Profile{ID: "retired-profile", Version: "2026-08", Harness: "codex", Model: "retired-model", Effort: "high",
		MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"}
	if err := ValidateSnapshot(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(profile.ID, profile.Version, profile.Harness); err == nil {
		t.Fatal("retired snapshot unexpectedly became a live catalog entry")
	}
}
