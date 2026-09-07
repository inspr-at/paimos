// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"strings"
	"testing"
)

func TestReviewBindingTreatsScopeAsSet(t *testing.T) {
	draft := Draft{
		Revision: 3,
		Baseline: BaselineClaim{ContentDigest: "sha256:digest", RevisionSeal: "sha256:seal"},
	}
	worker := WorkerSelection{WorkerName: "builder"}
	left := reviewBinding(draft, ModeManual, []string{"req.b", "req.a", "req.b"}, worker)
	right := reviewBinding(draft, ModeManual, []string{"req.a", "req.b"}, worker)
	if left != right {
		t.Fatalf("same membership different order: %s vs %s", left, right)
	}
	changed := reviewBinding(draft, ModeManual, []string{"req.a"}, worker)
	if left == changed {
		t.Fatal("changed membership reused the reviewed binding")
	}
	if canonicalScopeKey([]string{"req.b", " req.a "}) != `["req.a","req.b"]` {
		t.Fatalf("canonical key=%q", canonicalScopeKey([]string{"req.b", " req.a "}))
	}
}

func TestCanonicalScopeKeyDistinguishesCommaContainingRefs(t *testing.T) {
	a := []string{"req.a,req.b", "req.c"}
	b := []string{"req.a", "req.b,req.c"}
	if strings.Join(canonicalRequirementRefs(a), ",") != strings.Join(canonicalRequirementRefs(b), ",") {
		t.Fatal("fixture no longer demonstrates the CSV collision")
	}
	if canonicalScopeKey(a) == canonicalScopeKey(b) {
		t.Fatalf("CSV-colliding memberships hashed equal: %s", canonicalScopeKey(a))
	}
	if sameCanonicalScope(a, b) {
		t.Fatal("CSV-colliding memberships compared equal")
	}
	if !sameCanonicalScope([]string{"req.c", "req.a,req.b"}, a) {
		t.Fatal("same-set reorder was refused")
	}
	draft := Draft{
		Revision: 1,
		Baseline: BaselineClaim{ContentDigest: "sha256:digest", RevisionSeal: "sha256:seal"},
	}
	worker := WorkerSelection{WorkerName: "builder"}
	if reviewBinding(draft, ModeManual, a, worker) == reviewBinding(draft, ModeManual, b, worker) {
		t.Fatal("CSV-colliding memberships reused the reviewed binding")
	}
}
