// SPDX-License-Identifier: AGPL-3.0-only

package statusnames

import (
	"errors"
	"strings"
	"testing"
)

// The risk: a name that reads like another status, or an over-long one, gets
// saved, and people can no longer tell two statuses apart.
func TestNormalizeRefusesAmbiguousNamesPerLocale(t *testing.T) {
	refused := func(in Names, code, field, msg string) {
		t.Helper()
		_, err := Normalize(in)
		var e *Error
		if !errors.As(err, &e) || e.Code != code || e.Field() != field || (msg != "" && e.Msg != msg) {
			t.Fatalf("%v: got %#v, want %s at %s", in, err, code, field)
		}
	}
	refused(Names{"in_progress": {EN: strings.Repeat("ö", 25)}}, "status_name_too_long", "names.in_progress.en", "At most 24 characters.")
	refused(Names{"in_progress": {EN: "Done"}}, "status_name_taken", "names.in_progress.en", "“Done” is already the name of another status.")
	refused(Names{"in_progress": {EN: "done"}}, "status_name_taken", "names.in_progress.en", "")
	refused(Names{"in_progress": {EN: "Building"}, "qa": {EN: "building"}}, "status_name_taken", "names.qa.en", "“building” is already the name of another status.")
	refused(Names{"qa": {DE: "In Arbeit"}}, "status_name_taken", "names.qa.de", "")
	refused(Names{"queued": {EN: "Open"}}, "status_name_taken", "names.queued.en", "")
	refused(Names{"recurring": {EN: "Repeats"}}, "status_name_unknown", "names.recurring", "")
	refused(Names{"open": {EN: "Ready\nnow"}}, "status_name_invalid", "names.open.en", "")

	got, err := Normalize(Names{
		"in_progress": {EN: "  Building ", DE: "In Umsetzung"},
		// A renamed status frees its built-in name for another one.
		"open": {EN: "Ready"}, "qa": {EN: "Open"},
		"done": {EN: "   "}, "blocked": {EN: "Blocked", DE: "Blockiert"},
		"new": {EN: strings.Repeat("ö", 24)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got["in_progress"] != (Name{EN: "Building", DE: "In Umsetzung"}) || got["qa"] != (Name{EN: "Open"}) || got["new"].EN != strings.Repeat("ö", 24) {
		t.Fatalf("normalized %#v", got)
	}
	if _, ok := got["done"]; ok {
		t.Fatal("an empty name must fall back to the built-in, not be stored")
	}
	if _, ok := got["blocked"]; ok {
		t.Fatal("a name equal to its built-in is no rename")
	}
}

func TestDisplayFallsBackPerLocale(t *testing.T) {
	names := Names{"in_progress": {EN: "Building"}, "qa": {DE: "Prüfung"}}
	for _, tc := range []struct{ state, locale, want string }{
		{"in_progress", "en", "Building"}, {"in-progress", "en", "Building"}, {"active", "en", "Building"},
		// Only an EN rename: the German UI keeps the German built-in.
		{"in_progress", "de", "In Arbeit"},
		{"qa", "de", "Prüfung"}, {"qa", "en", "QA"},
		{"canceled", "de", "Abgebrochen"}, {"queued", "en", "Queued"},
		{"recurring", "en", "recurring"},
	} {
		if got := names.Display(tc.state, tc.locale); got != tc.want {
			t.Fatalf("%s/%s = %q, want %q", tc.state, tc.locale, got, tc.want)
		}
	}
	if names.Renamed("in_progress", "de") != "" || names.Renamed("in_progress", "en") != "Building" {
		t.Fatal("renamed lookup")
	}
}
