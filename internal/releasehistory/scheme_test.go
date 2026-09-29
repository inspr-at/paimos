// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"encoding/json"
	"testing"
)

// INSPR-CalVer3 (AEON-309): both calendar schemes share the coordinate; Aeon's
// switch-over is recorded at LastCalVer2.
func TestCalendarSchemesShareTheCoordinate(t *testing.T) {
	for _, s := range []string{SchemeCalVer3, SchemeCalVer2} {
		if !CalendarScheme(s) {
			t.Fatalf("%s is a calendar scheme", s)
		}
	}
	for _, s := range []string{"", "legacy", "inspr-calendar-v1", "inspr-calver-4"} {
		if CalendarScheme(s) {
			t.Fatalf("%q is not a calendar scheme", s)
		}
	}
	if SchemeOf("260923134337.0.0") != SchemeCalVer2 || SchemeOf(LastCalVer2) != SchemeCalVer2 {
		t.Fatal("versions up to LastCalVer2 are CalVer2 history")
	}
	if SchemeOf("260929113855.0.0") != SchemeCalVer3 {
		t.Fatal("versions after LastCalVer2 are CalVer3")
	}
	if !ValidVersion(LastCalVer2) {
		t.Fatal("LastCalVer2 is a valid coordinate")
	}
}

func TestSnapshotsAcceptBothCalendarSchemes(t *testing.T) {
	for _, scheme := range []string{SchemeCalVer2, SchemeCalVer3} {
		s := noteFixture()
		s.VersionScheme = scheme
		raw, _ := json.Marshal(s)
		if _, err := NotesFromSnapshot(raw, notesVersion, "fixture"); err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
	}
	s := noteFixture()
	s.VersionScheme = "legacy"
	raw, _ := json.Marshal(s)
	if _, err := NotesFromSnapshot(raw, notesVersion, "fixture"); err == nil {
		t.Fatal("a non-calendar scheme must be rejected")
	}
}
