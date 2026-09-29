// SPDX-License-Identifier: AGPL-3.0-only

package deploytarget

import "testing"

func TestNormalizeRequiresDestinationAndChange(t *testing.T) {
	for _, target := range []*Target{{}, {Service: "aeon", Change: "deploy image"}, {Environment: "production", Change: "deploy image"}, {Hosts: []string{"edge-1"}, Service: "aeon"}, {Hosts: []string{"edge-1", "edge-1"}, Service: "aeon", Change: "deploy image"}} {
		if _, _, err := Normalize(target); err == nil {
			t.Fatalf("accepted incomplete target: %+v", target)
		}
	}
}

func TestNormalizeDigestCoversTargetAndCanonicalHosts(t *testing.T) {
	a, digestA, err := Normalize(&Target{Hosts: []string{"edge-2", "edge-1"}, Environment: "production", Service: "aeon", Image: "aeon:v1", Change: "deploy image"})
	if err != nil {
		t.Fatal(err)
	}
	_, digestB, err := Normalize(&Target{Hosts: []string{"edge-1", "edge-2"}, Environment: "production", Service: "aeon", Image: "aeon:v1", Change: "deploy image"})
	if err != nil {
		t.Fatal(err)
	}
	if digestA != digestB || a.Hosts[0] != "edge-1" {
		t.Fatal("host order changed target identity")
	}
	_, changed, err := Normalize(&Target{Hosts: []string{"edge-1", "edge-2"}, Environment: "production", Service: "aeon", Image: "aeon:v2", Change: "deploy image"})
	if err != nil {
		t.Fatal(err)
	}
	if changed == digestA {
		t.Fatal("image mutation kept approval digest")
	}
}

func TestNormalizeAbsentTarget(t *testing.T) {
	target, digest, err := Normalize(nil)
	if err != nil || target != nil || digest != "" {
		t.Fatalf("absent target changed: %v %s %v", target, digest, err)
	}
}
