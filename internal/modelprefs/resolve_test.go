// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"encoding/json"
	"testing"
)

func str(s string) *string { return &s }
func TestCellPrecedenceAndLockBounds(t *testing.T) {
	cell := func(id string) Cell { return Cell{Mode: "pinned", ProfileID: id} }
	base := func() []Scope {
		return []Scope{
			{Level: "default", Rows: map[string]Row{"backend": {Cells: map[string]Cell{"normal": cell("D"), "complex": cell("DC")}}, "other": {Cells: map[string]Cell{"complex": cell("DO")}}}},
			{Level: "person", Rows: map[string]Row{"backend": {Cells: map[string]Cell{"normal": cell("Y")}}}},
			{Level: "project", Rows: map[string]Row{"backend": {Cells: map[string]Cell{"normal": cell("P"), "complex": cell("PC")}}, "other": {Cells: map[string]Cell{"complex": cell("PO")}}}},
		}
	}
	tests := []struct {
		name, bucket, want, setBy, locked string
		fallback                          bool
		change                            func([]Scope)
	}{
		{name: "project wins", bucket: "normal", want: "P", setBy: "project"},
		{name: "person wins missing project bucket", bucket: "normal", want: "Y", setBy: "person", change: func(c []Scope) { delete(c[2].Rows, "backend") }},
		{name: "person section lock missing bucket", bucket: "complex", want: "DC", setBy: "default", locked: "person", change: func(c []Scope) { c[1].PrefsLocked = true }},
		{name: "person lock no row", bucket: "normal", want: "D", setBy: "default", locked: "person", change: func(c []Scope) { c[1].PrefsLocked = true; delete(c[1].Rows, "backend") }},
		{name: "default section lock", bucket: "normal", want: "D", setBy: "default", locked: "default", change: func(c []Scope) { c[0].PrefsLocked = true }},
		{name: "person row lock", bucket: "complex", want: "DC", setBy: "default", locked: "person", change: func(c []Scope) { r := c[1].Rows["backend"]; r.Locked = true; c[1].Rows["backend"] = r }},
		{name: "other stays within person bound", bucket: "complex", want: "DO", setBy: "default", locked: "person", fallback: true, change: func(c []Scope) { c[1].PrefsLocked = true; delete(c[0].Rows, "backend") }},
		{name: "other lock tightens bound", bucket: "complex", want: "DO", setBy: "default", locked: "default", fallback: true, change: func(c []Scope) {
			for i := range c {
				delete(c[i].Rows, "backend")
			}
			r := c[0].Rows["other"]
			r.Locked = true
			c[0].Rows["other"] = r
		}},
		{name: "auto still names lock", bucket: "complex", setBy: "default", locked: "person", change: func(c []Scope) {
			c[1].PrefsLocked = true
			for i := range c {
				delete(c[i].Rows, "backend")
				delete(c[i].Rows, "other")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			if tt.change != nil {
				tt.change(c)
			}
			r := ResolveCell(c, "backend", tt.bucket)
			got := ""
			if r.Cell != nil {
				got = r.Cell.ProfileID
			}
			if got != tt.want || r.SetBy != tt.setBy || r.LockedBy != tt.locked || r.KindFallback != tt.fallback {
				t.Fatalf("%+v profile=%s", r, got)
			}
		})
	}
	// Editor tabs evaluate the same walk over the prefix, including a lock here.
	c := base()
	if r := ResolveCell(c[:1], "backend", "normal"); r.Cell.ProfileID != "D" {
		t.Fatal(r)
	}
	if r := ResolveCell(c[:2], "backend", "normal"); r.Cell.ProfileID != "Y" {
		t.Fatal(r)
	}
}
func TestResidencyUserChoiceBelowLock(t *testing.T) {
	for _, tt := range []struct {
		d, y, p, want string
		dl, yl, loose bool
		locked        string
	}{
		{d: "eu", y: "any", want: "any", dl: true, loose: true, locked: "default"},
		{d: "eu", y: "local", want: "local", dl: true, locked: "default"},
		{d: "local", y: "eu", p: "any", want: "any", dl: true, yl: true, loose: true, locked: "default"},
		{d: "any", y: "local", p: "eu", want: "eu", yl: true, loose: true, locked: "person"},
		{d: "any", y: "local", p: "eu", want: "eu", dl: true, yl: true, loose: true, locked: "default"},
		{d: "eu", p: "any", want: "any", yl: true, loose: true, locked: "person"},
		{d: "local", y: "eu", p: "any", want: "any"},
		{want: "any"},
	} {
		c := []Scope{{Level: "default", ResidencyLocked: tt.dl}, {Level: "person", ResidencyLocked: tt.yl}, {Level: "project"}}
		for i, v := range []string{tt.d, tt.y, tt.p} {
			if v != "" {
				c[i].Residency = str(v)
			}
		}
		r := ResolveResidency(c)
		if r.Value != tt.want || r.LoosenedLock != tt.loose || r.LockedBy != tt.locked {
			t.Fatalf("%+v: %+v", tt, r)
		}
		if Strictest(r.Value, "local") != "local" {
			t.Fatal("ticket requirement loosened")
		}
	}
	for _, a := range []string{"any", "eu", "local"} {
		for _, b := range []string{"any", "eu", "local"} {
			if Strictness(Strictest(a, b)) < Strictness(a) || Strictness(Strictest(a, b)) < Strictness(b) {
				t.Fatal(a, b)
			}
		}
	}
}
func TestPlacementDecodesJSONTokens(t *testing.T) {
	for _, raw := range []string{`{"area":"security"}`, `{"area":" security "}`} {
		if p := PlacementFields(json.RawMessage(raw)); p.Area != "security" {
			t.Fatal(p)
		}
	}
	p := PlacementFields(json.RawMessage(`{"complexity":" L ","route_role":"build-hard","complexity_source":"person"}`))
	if p.Complexity != "L" || p.RouteRole != "build-hard" || p.ComplexitySource != "person" {
		t.Fatal(p)
	}
	for _, raw := range []string{"", `null`, `{}`, `{"area":null}`, `{"area":42}`, `{"area":{}}`} {
		if p := PlacementFields(json.RawMessage(raw)); p.Area != "" {
			t.Fatalf("%s: %+v", raw, p)
		}
	}
}
func TestBucketUsesTicketRole(t *testing.T) {
	for _, tt := range []struct{ c, role, want string }{{"L", "build", "complex"}, {"", "build-hard", "complex"}, {"M", "build-hard", "normal"}, {"S", "build-hard", "normal"}, {"", "review-gate", "normal"}, {"", "build", "normal"}} {
		if got := BucketOf(tt.c, tt.role); got != tt.want {
			t.Fatal(tt, got)
		}
	}
}
