// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: latest-run joins leak private runs or lose page bounds; legacy/unknown
// states and unconfirmed exits must not imply successful execution. Draft rules
// must inherit exactly the saved scope without exposing another person's rules.
func TestRoutineUIReadsScopeLatestRunAndDraftPolicy(t *testing.T) {
	f := setup(t)
	projectReader, workspaceReader := projectPrincipal(f, "viewer"), viewer(f)
	base := intentInput(f)
	items := map[string]Recurrence{}
	for _, scope := range []string{"personal", "project", "workspace"} {
		in := base
		d := *base.Definition
		d.Scope = DefinitionScope{Kind: scope}
		if scope == "project" {
			d.Scope.ProjectID = f.project
		}
		in.Definition = &d
		item := f.create(in)
		items[scope] = item
		first := f.manual(item.ID, "first")
		latest := f.manual(item.ID, "last")
		if first.Run == nil || latest.Run == nil {
			t.Fatal("fixture lost latest run")
		}
		f.tx(func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE routine_runs SET created_at=$2 WHERE id=$1`, first.Run.ID, f.now); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET created_at=$2 WHERE id=$1`, latest.Run.ID, f.now.AddDate(0, 0, 1))
			return err
		})
		got := f.get(item.ID).LastRun
		if got == nil || got.ID != latest.Run.ID || got.State != "created" || got.Reason != "The occurrence was created; execution has not started." {
			t.Fatalf("latest S21 run: %+v", got)
		}
		request := map[string]any{"scope": map[string]string{"personal": "user", "project": "project", "workspace": "tenant"}[scope], "expected_revision": 0, "reason": "", "rules": []routineguard.Rule{{ID: "custom." + scope, Checkpoint: "both", Method: "words", Values: []string{"scope marker"}, Result: routineguard.Block}}}
		f.call(f.p, "PUT", "/api/recurrences/"+item.ID+"/guardrails", request, 200)
	}
	for _, tc := range []struct {
		p    tenant.Principal
		want map[string]bool
	}{{f.p, map[string]bool{"personal": true, "project": true, "workspace": true}}, {projectReader, map[string]bool{"project": true}}, {workspaceReader, map[string]bool{"project": true, "workspace": true}}} {
		var page struct {
			Items []Recurrence `json:"items"`
			Next  *string      `json:"next_cursor"`
		}
		if err := json.Unmarshal(f.call(tc.p, "GET", "/api/recurrences", nil, 200), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != len(tc.want) || page.Next != nil {
			t.Fatalf("scope page: %+v", page)
		}
		for _, item := range page.Items {
			if !tc.want[item.Definition.Scope.Kind] || item.LastRun == nil {
				t.Fatalf("scope/run leaked or missing: %+v", item)
			}
		}
	}
	for scope, item := range items {
		var saved, draft routineguard.Policy
		if err := json.Unmarshal(f.call(f.p, "GET", "/api/recurrences/"+item.ID+"/guardrails", nil, 200), &saved); err != nil {
			t.Fatal(err)
		}
		query := "/api/routine-guardrails?scope=" + scope
		if scope == "project" {
			query += "&project_id=" + f.project
		}
		if err := json.Unmarshal(f.call(f.p, "GET", query, nil, 200), &draft); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(saved, draft) {
			t.Fatalf("draft differs from saved %s policy", scope)
		}
	}
	raw := f.call(projectReader, "GET", "/api/routine-guardrails?scope=personal", nil, 200)
	if strings.Contains(string(raw), "custom.personal") {
		t.Fatal("another person's rule leaked")
	}
	f.call(projectReader, "GET", "/api/routine-guardrails?scope=workspace", nil, 403)
	for _, query := range []string{"", "?scope=project", "?scope=personal&project_id=" + f.project, "?scope=personal&owner_id=" + f.p.ID, "?scope=personal&scope=project", "?scope=" + strings.Repeat("x", 300)} {
		f.call(f.p, "GET", "/api/routine-guardrails"+query, nil, 400)
	}
	item := items["project"]
	for _, state := range []string{"running", "waiting_for_person", "held", "failed", "skipped", "unknown", "done"} {
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET state=$2 WHERE recurrence_id=$1`, item.ID, state)
			return err
		})
		got := f.get(item.ID).LastRun
		want, reason := lastRunState(state)
		if state == "done" {
			want, reason = "held", "Successful execution has not been confirmed by process evidence."
		}
		if got.State != want || got.Reason != reason {
			t.Fatalf("%s: %+v", state, got)
		}
	}
	run := f.get(item.ID).LastRun
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,assignment_digest,state,process_receipt)
 SELECT tenant_id,id,'confirmed','build',agent_run_id,assignment_digest,'done','{"exit_confirmed":true,"exit_code":1}'::jsonb FROM routine_runs WHERE id=$1`, run.ID)
		return err
	})
	if got := f.get(item.ID).LastRun; got.State != "failed" {
		t.Fatal("failed exit shown successful", got)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_attempts SET process_receipt='{"exit_confirmed":true,"exit_code":0}'::jsonb WHERE run_id=$1`, run.ID)
		return err
	})
	if got := f.get(item.ID).LastRun; got.State != "done" || got.Reason != "" {
		t.Fatal("confirmed exit not shown done", got)
	}
	// The latest-run enrichment must not grow the existing 100-row UUID page.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO recurrences(tenant_id,project_id,parent_id,template,trigger,next_at,created_by_principal_id)
 SELECT tenant_id,project_id,parent_id,template,trigger,next_at,created_by_principal_id FROM recurrences CROSS JOIN generate_series(1,101) WHERE id=$1`, item.ID)
		return err
	})
	var page struct {
		Items []Recurrence `json:"items"`
		Next  *string      `json:"next_cursor"`
	}
	if err := json.Unmarshal(f.call(projectReader, "GET", "/api/recurrences", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 100 || page.Next == nil {
		t.Fatalf("unbounded recurrence page: %d %+v", len(page.Items), page.Next)
	}
	for _, item := range page.Items {
		if item.Definition == nil && item.LastRun != nil {
			t.Fatal("run was joined to the wrong recurrence")
		}
	}
}
