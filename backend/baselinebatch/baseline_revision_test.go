// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
)

func TestStartedBatchPreservesAithemaRevisionAcrossRestartAndLaterImport(t *testing.T) {
	fixture := openBridgeFixture(t)
	if fixture.batch.Baseline.Revision != 1 {
		t.Fatalf("start baseline revision=%d want 1", fixture.batch.Baseline.Revision)
	}

	if err := appdb.DB.Close(); err != nil {
		t.Fatal(err)
	}
	appdb.DB = nil
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	deliveryStore := delivery.NewStore(appdb.DB, delivery.Options{
		Clock: delivery.ClockFunc(func() time.Time { return fixture.now }),
	})
	service := NewService(
		appdb.DB,
		ClockFunc(func() time.Time { return fixture.now }),
		deliveryStore,
		lifecycleintents.NewService(appdb.DB),
		managedharness.NewService(appdb.DB),
		nil,
	)

	ctx := context.Background()
	got, err := service.GetBatch(ctx, fixture.actor, fixture.projectID, fixture.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Baseline.Revision != 1 {
		t.Fatalf("restart GET baseline revision=%d want 1", got.Baseline.Revision)
	}

	later := aithemaRevisionFixture(t, "baseline:v1", 2, "Magic links are single-use")
	if _, err := service.Import(ctx, fixture.actor, fixture.projectID, ImportRequest{
		Handover: later, Selected: []string{"req.login"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = service.GetBatch(ctx, fixture.actor, fixture.projectID, fixture.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Baseline.Revision != 1 {
		t.Fatalf("later revision mutated historical batch to %d", got.Baseline.Revision)
	}
	workflow, err := service.Workflow(ctx, fixture.actor, fixture.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflow.Batches) == 0 || workflow.Batches[0].Baseline.Revision != 1 {
		t.Fatalf("list lost historical baseline revision: %+v", workflow.Batches)
	}
}

func TestUnknownHistoricalBaselineRevisionIsExplicitNull(t *testing.T) {
	raw, err := json.Marshal(BaselineClaim{BaselineRef: "baseline:legacy"})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Revision *int `json:"revision"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Revision != nil {
		t.Fatalf("unknown historical revision encoded as %v, want null", *wire.Revision)
	}
}

func aithemaRevisionFixture(t *testing.T, baselineRef string, revision int, criterion string) []byte {
	t.Helper()
	requirements := []Requirement{{
		Ref: "req.login", Statement: "Users sign in with email",
		AcceptanceCriteria: []string{criterion}, ConstraintRefs: []string{},
	}}
	digest, err := ContentDigest(requirements, nil)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := RevisionSeal(baselineRef, revision, digest)
	if err != nil {
		t.Fatal(err)
	}
	handover, err := json.Marshal(map[string]any{
		"handover_version": HandoverVersion,
		"stream_ref":       "stream:inspr397",
		"exported_at":      "2026-09-11T10:00:00.000Z",
		"baseline": map[string]any{
			"baseline_ref": baselineRef, "revision": revision,
			"content_digest": digest, "revision_seal": seal,
			"approved_by": "party:aithema-reviewer", "approved_at": "2026-09-11T09:59:00.000Z",
			"requirements": requirements, "constraints": []any{},
		},
		"pending_proposals": []any{}, "decisions": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handover
}
