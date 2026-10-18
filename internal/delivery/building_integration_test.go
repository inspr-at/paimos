// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestDeliveryBuildingEndpointReadsFlowUnderRLS(t *testing.T) {
	// Risk: the endpoint reads another workspace's changes, answers without
	// delivery.read, counts an unmerged change, or its targets are not the
	// ones the delivery settings name.
	f := newFixture(t)
	f.at = buildingNow
	var other string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title,fields) SELECT $1,$2,'AEON-849',id,'Second change','{"priority":"high"}' FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, f.project).Scan(&other)
	})
	builder := FlowActor{Type: "agent", Label: "Builder"}
	gate := FlowActor{Type: "agent", Label: "Reviewer"}
	step := func(key, stepKey, kind string, actor FlowActor, start, end time.Time, outcome string) flowStepInput {
		s := flowStepInput{Source: "paimos", Key: key, StepKey: stepKey, Round: 1, Kind: kind, Actor: actor, Started: start, Ended: &end}
		if outcome != "" {
			s.Outcome = &outcome
		}
		return s
	}
	merged, open := at(17, 12, 0), at(17, 13, 0)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := applyFlowTx(context.Background(), tx, f.person.TenantID, []flowBatch{
			{Item: flowItemInput{Project: f.project, Kind: "change", Ref: "AEON-848", Title: "Delivery ticket", Ticket: &f.ticket, Ended: &merged}, Steps: []flowStepInput{
				step("harness/1", "build", "work", builder, at(17, 8, 0), at(17, 9, 0), "ok"),
				step("review/1", "review", "work", gate, at(17, 9, 20), at(17, 9, 20), "changes"),
				step("harness/2", "build", "rework", builder, at(17, 9, 50), at(17, 11, 0), "ok"),
				step("review/2", "review", "work", gate, at(17, 11, 10), at(17, 11, 10), "ok"),
			}},
			// Not merged yet: no figure counts it.
			{Item: flowItemInput{Project: f.project, Kind: "change", Ref: "AEON-849", Title: "Second change", Ticket: &other}, Steps: []flowStepInput{
				{Source: "paimos", Key: "harness/3", StepKey: "build", Round: 1, Kind: "work", Actor: builder, Started: open},
			}},
		}, f.at)
		return err
	})
	path := "/api/projects/" + f.project + "/delivery/building?window=7"
	var out deliveryBuilding
	f.call(t, f.person, "GET", path, nil, 200, &out)
	if out.ProjectID != f.project || out.Window != 7 || out.Since == nil || !out.Since.Equal(at(17, 8, 0)) || out.Coverage.Items != 1 || len(out.Metrics) != 4 {
		t.Fatalf("building answer %+v", out)
	}
	rounds := buildingMetricOf(t, out, "rounds")
	wantValue(t, "rounds", rounds.Current.Value, 2)
	if len(rounds.Worst) != 1 || rounds.Worst[0].NodeID != f.ticket || rounds.Worst[0].Key != "AEON-848" || !reflect.DeepEqual(rounds.Worst[0].Rounds, []string{roundChanges, roundPassed}) {
		t.Fatalf("worst rounds %+v", rounds.Worst)
	}
	// 30 min after the gate asked for changes, 50 min from the passing gate to the merge.
	idle := buildingMetricOf(t, out, "idle")
	if idle.Current.N != 2 || *idle.Current.P50 != 40 || idle.Target == nil || idle.Target.Value != 15 || idle.Target.Source != "delivery_settings" {
		t.Fatalf("idle %+v", idle)
	}
	var high deliveryBuilding
	f.call(t, f.person, "GET", path+"&priority=high", nil, 200, &high)
	if high.Coverage.Items != 0 || buildingMetricOf(t, high, "first_try").Current.Value != nil {
		t.Fatalf("priority filter kept another priority: %+v", high.Coverage)
	}

	// The delivery settings carry the same targets; saved overrides do not invent them.
	var settings struct {
		Targets *BuildingTargets `json:"targets"`
	}
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery-settings", nil, 200, &settings)
	if settings.Targets == nil || *settings.Targets != buildingTargetDefaults() || rounds.Target.Value != settings.Targets.RoundsPerItem {
		t.Fatalf("settings targets %+v", settings.Targets)
	}
	var saved struct {
		Targets *BuildingTargets `json:"targets"`
	}
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery-settings?view=overrides", nil, 200, &saved)
	if saved.Targets != nil {
		t.Fatalf("overrides view invented targets %+v", saved.Targets)
	}

	// Targets cannot be saved yet: a write that names them is refused, never silently dropped.
	f.call(t, f.person, "PUT", "/api/projects/"+f.project+"/delivery-settings", map[string]any{"targets": map[string]float64{"first_try_rate": 80}}, 400, nil)

	// Another workspace does not see the project; a key without delivery.read is refused.
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	reader := tenant.Principal{ID: f.agent.ID, TenantID: f.agent.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.read", "events.read"}}
	f.call(t, reader, "GET", path, nil, 403, nil)
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/building?window=14", nil, 400, nil)
	f.call(t, f.person, "GET", "/api/projects/"+f.project+"/delivery/building?worked_by=robot", nil, 400, nil)
}
