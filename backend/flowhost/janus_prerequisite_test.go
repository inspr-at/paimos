// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package flowhost

import (
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"strings"
	"testing"
)

func TestJanusPrerequisiteProjectionIsScopedAndOpaque(t *testing.T) {
	for _, status := range []string{baselinebatch.BatchActive, baselinebatch.BatchCompleted, baselinebatch.BatchPaused, baselinebatch.BatchCancelled} {
		batch := &baselinebatch.Batch{ID: 42, Status: status, Progress: baselinebatch.Progress{JanusPrerequisite: &baselinebatch.JanusPrerequisiteEvidence{HandoffID: "private-owner-handoff", StageKey: "deployment", ObservedAt: "2026-09-14T10:00:00Z"}}}
		gate := mapJanus(batch, "2026-09-14T10:01:00Z", "2026-09-14T10:11:00Z")
		positive := status == baselinebatch.BatchActive || status == baselinebatch.BatchCompleted
		if (gate.Status == "pass") != positive {
			t.Fatalf("%s: %+v", status, gate)
		}
		if positive && (gate.EvidenceRef == nil || strings.Contains(*gate.EvidenceRef, "private-owner") || gate.ObservedAt == nil || gate.FreshUntil == nil) {
			t.Fatalf("unbound/raw evidence: %+v", gate)
		}
		batch.Progress.JanusPrerequisite = nil
		if mapJanus(batch, "", "").Status != "unknown" {
			t.Fatal("missing proof passed")
		}
	}
}
