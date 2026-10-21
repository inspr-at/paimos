// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"slices"
	"testing"
)

// AEON-1495 risk: a sibling origin that is not an exact HTTPS origin (a path,
// credentials, plain HTTP in prod, a repeat) widens who may read the owner's
// summary across instances.
func TestSiblingInstancesAreExactOrigins(t *testing.T) {
	got, err := ParseInstances("ppm", " pma=https://pm.example.com , lab=https://lab.example:8443", false)
	if err != nil || got.Here != "ppm" || !slices.Equal(got.Origins(), []string{"https://pm.example.com", "https://lab.example:8443"}) || got.Siblings[0].Tag != "pma" {
		t.Fatalf("parse %+v %v", got, err)
	}
	if got, err = ParseInstances("", "", false); err != nil || got.Here != "" || len(got.Siblings) != 0 {
		t.Fatalf("empty %+v %v", got, err)
	}
	if _, err = ParseInstances("ppm", "pma=http://localhost:5173", true); err != nil {
		t.Fatalf("dev http origin refused: %v", err)
	}
	for _, bad := range []struct{ here, siblings string }{
		{"", "pma=https://pm.example.com"},
		{"PPM", ""},
		{"ppm", "pma=http://pm.example.com"},
		{"ppm", "pma=https://pm.example.com/"},
		{"ppm", "pma=https://pm.example.com/app"},
		{"ppm", "pma=https://user@pm.example.com"},
		{"ppm", "pma=https://pm.example.com?x=1"},
		{"ppm", "pma"},
		{"ppm", "ppm=https://pm.example.com"},
		{"ppm", "pma=https://a.example,pma=https://b.example"},
		{"ppm", "a=https://a.example,b=https://a.example"},
		{"ppm", "a=https://a.example,b=https://b.example,c=https://c.example,d=https://d.example,e=https://e.example"},
	} {
		if _, err := ParseInstances(bad.here, bad.siblings, false); err == nil {
			t.Fatalf("accepted %q %q", bad.here, bad.siblings)
		}
	}
}
