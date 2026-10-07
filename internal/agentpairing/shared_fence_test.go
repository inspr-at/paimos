// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This explicit caller inventory makes newly connected entry paths reviewable.
// Actual contention and FK compatibility are exercised by boundary/recurrence tests.
func TestSharedFenceCallerInventory(t *testing.T) {
	expected := map[string][]string{
		"db.LockTree":               {"authz/project_members.go", "crossreview/policy.go", "db/fences.go", "modelregistry/module.go", "modelregistry/preparation.go", "operatoractor/actor.go", "workorders/common.go"},
		"db.LockTenant":             {"auth/store.go", "crossreview/reporter.go", "db/fences.go", "modelregistry/module.go", "modelregistry/preferences_http.go", "modelregistry/preparation.go", "modelregistry/routes_write.go", "workorders/common.go"},
		"db.LockCurrentTree":        {"agentpairing/lifecycle.go", "nodes/module.go"},
		"agentpairing.LockRead":     {"agentaccounts/residency_evidence.go", "agentruns/runs.go"},
		"agentpairing.Lock":         {"agentaccounts/route.go", "agentpairing/lifecycle.go", "agentpairing/provision.go", "agentruns/runs.go", "agentruns/telemetry.go", "crossreview/module.go", "knowledge/tagger.go", "knowledge/undo.go", "modelregistry/preparation.go", "nodes/bulk.go", "nodes/nodes.go"},
		"agentpairing.LockMutation": {"agentaccounts/module.go", "agentpairing/module.go", "agentruns/queue.go", "auth/owner_workstation.go", "harness/agent_recovery.go", "harness/module.go", "modelprovider/settings.go", "parentbenefits/module.go", "portal/market.go", "portal/moderate.go", "portal/module.go", "portal/products.go"},
		"authz.LockProjectMutation": {"authz/agent_creation.go", "authz/members.go", "authz/project_members.go", "importer/users_backfill.go", "importer/writer.go", "statusautopilot/settings.go"},
		"authz.LockProjectWrite":    {"attachments/module.go", "decisiondesk/notifications.go", "events/causal_undo.go", "events/module.go", "harness/lead_decisions.go", "knowledge/learnings.go", "nodes/causal_undo.go", "nodes/portal_publish.go", "themes/store.go", "themes/undo.go"},
		"operatoractor.Ensure":      {"auth/store.go", "authz/operator.go", "operatoractor/actor.go"},
		"rules.PrepareWrite":        {"knowledge/learning_draft.go"},
	}
	root := ".."
	observed := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			key := ""
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok {
					key = pkg.Name + "." + fn.Sel.Name
				}
			case *ast.Ident:
				key = file.Name.Name + "." + fn.Name
			}
			if key == "operatoractor.EnsureWithProduction" {
				key = "operatoractor.Ensure"
			}
			if _, ok := expected[key]; ok {
				if observed[key] == nil {
					observed[key] = map[string]bool{}
				}
				observed[key][filepath.ToSlash(rel)] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range expected {
		got := []string{}
		for path := range observed[key] {
			got = append(got, path)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s callers changed; inventory and analyze entry order: got %v want %v", key, got, want)
		}
	}
}

func TestSharedFencePrimitiveOrder(t *testing.T) {
	checks := []struct{ path, fn, first, second string }{
		{"../db/fences.go", "LockTree", "LockTenant(", "pg_advisory_xact_lock"},
		{"lifecycle.go", "Lock", "db.LockCurrentTree(", "aeon-pairing:"},
		{"../auth/store.go", "revokeAgentKeyTx", "db.LockTenant(", "lockAgentKey("},
		{"../rules/module.go", "endpoint", "lockAccess(", "pg_advisory_xact_lock"},
		{"../rules/learning_draft.go", "PrepareWrite", "lockAccess(", "pg_advisory_xact_lock"},
		{"../recurrences/module.go", "lock", "FOR NO KEY UPDATE", "pg_try_advisory_xact_lock"},
		// Attached heartbeats and recovery enter tenant/tree/pairing before
		// policy/session/recovery rows; control events are flushed afterward.
		{"../harness/module.go", "heartbeat", "agentpairing.LockMutation(", "lockActivityPolicy("},
		{"../harness/agent_recovery.go", "requestAgentRecovery", "agentpairing.LockMutation(", "recoveryPerson("},
		{"../harness/agent_recovery.go", "readAgentRecovery", "agentpairing.LockMutation(", "authz.RequireTx("},
		{"../harness/agent_recovery.go", "claimAgentRecoveries", "agentpairing.LockMutation(", "recoveryDaemon("},
		{"../harness/agent_recovery.go", "completeAgentRecovery", "agentpairing.LockMutation(", "recoveryDaemon("},
		// Learning decisions (AEON-788) enter the project fence and re-authorize
		// before the learning advisory lock and node rows; the event is appended last.
		{"../knowledge/learnings.go", "fenceLearningWrite", "authz.LockProjectWrite(", "authz.RequireTx("},
		{"../knowledge/learnings.go", "acceptLearning", "fenceLearningWrite(", "lockLearning("},
		{"../knowledge/learnings.go", "acceptLearning", "lockNode(", "events.Append("},
		{"../knowledge/learnings.go", "dismissLearning", "fenceLearningWrite(", "lockLearning("},
		{"../knowledge/learnings.go", "dismissLearning", "lockLearning(", "events.Append("},
		// Autopilot settings and project overrides (AEON-696) enter the shared
		// project fence before the settings row; the event is appended after it.
		{"../statusautopilot/settings.go", "settings", "authz.LockProjectMutation(", "INSERT INTO status_autopilot_settings"},
		{"../statusautopilot/settings.go", "settings", "INSERT INTO status_autopilot_settings", "events.Append("},
		{"../statusautopilot/settings.go", "project", "authz.LockProjectMutation(", "INSERT INTO status_autopilot_projects"},
		{"../statusautopilot/settings.go", "project", "INSERT INTO status_autopilot_projects", "events.Append("},
		// Review policy (AEON-851) enters the shared tree fence before the
		// project row and re-checks authority there. Review rows are marked
		// dirty before the event counter.
		{"../crossreview/policy.go", "writePolicy", "db.LockTree(", "policyProject("},
		{"../crossreview/policy.go", "writePolicy", "db.LockTree(", "authz.RequireTx("},
		{"../crossreview/policy.go", "writePolicy", "UPDATE work_order_reviews", "events.Append("},
		// Publication re-reads under the tenant fence after network I/O, before
		// it writes the review row. The binding invalidation commits earlier,
		// in its own transaction, and does not hold this fence across the post.
		{"../crossreview/reporter.go", "publishReview", "db.LockTenant(", "github_status=$2"},
	}
	for _, c := range checks {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := parser.ParseFile(token.NewFileSet(), c.path, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		var body string
		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == c.fn {
				body = string(raw[int(fn.Pos())-1 : int(fn.End())-1])
				break
			}
		}
		first, second := strings.Index(body, c.first), strings.Index(body, c.second)
		if first < 0 || second < 0 || first >= second {
			t.Errorf("%s.%s must acquire %s before %s", c.path, c.fn, c.first, c.second)
		}
	}
	// The first admin check is before the fence. The revocation re-check is the
	// admin call that remains after LockProjectMutation.
	for _, fn := range []string{"settings", "project"} {
		body := functionBody(t, "../statusautopilot/settings.go", fn)
		lock := strings.Index(body, "authz.LockProjectMutation(")
		if lock < 0 || !strings.Contains(body[lock:], "admin(") {
			t.Errorf("statusautopilot.%s must re-check admin after LockProjectMutation", fn)
		}
	}
}

func functionBody(t *testing.T, path, name string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range tree.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return string(raw[int(fn.Pos())-1 : int(fn.End())-1])
		}
	}
	t.Fatalf("%s missing function %s", path, name)
	return ""
}
