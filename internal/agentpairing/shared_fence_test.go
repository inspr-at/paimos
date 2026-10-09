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
var sharedFenceCallers = map[string][]string{
	"db.LockTree":               {"authz/project_members.go", "crossreview/policy.go", "db/fences.go", "delivery/alerts.go", "delivery/audit.go", "delivery/audit_store.go", "delivery/reviews_api.go", "delivery/routing.go", "modelregistry/module.go", "modelregistry/preparation.go", "operatoractor/actor.go", "workorders/common.go"},
	"db.LockTenant":             {"auth/store.go", "crossreview/reporter.go", "db/fences.go", "delivery/alerts.go", "delivery/api.go", "delivery/audit_store.go", "delivery/audit_webhook.go", "delivery/flow_store.go", "delivery/metrics_api.go", "delivery/module.go", "delivery/quarantine.go", "delivery/reconcile.go", "delivery/store.go", "delivery/webhook.go", "delivery/workqueue.go", "delivery/workqueue_api.go", "engineadmission/module.go", "modelregistry/module.go", "modelregistry/preferences_http.go", "modelregistry/preparation.go", "modelregistry/routes_write.go", "statusautopilot/attention_bulk.go", "workorders/common.go"},
	"db.LockCurrentTree":        {"agentpairing/lifecycle.go", "nodes/module.go"},
	"agentpairing.LockRead":     {"agentaccounts/residency_evidence.go", "agentruns/runs.go"},
	"agentpairing.Lock":         {"agentaccounts/route.go", "agentpairing/lifecycle.go", "agentpairing/provision.go", "agentruns/runs.go", "agentruns/telemetry.go", "crossreview/module.go", "knowledge/tagger.go", "knowledge/undo.go", "modelregistry/preparation.go", "nodes/bulk.go", "nodes/nodes.go"},
	"agentpairing.LockMutation": {"agentaccounts/module.go", "agentpairing/module.go", "agentruns/queue.go", "auth/owner_workstation.go", "harness/agent_recovery.go", "harness/module.go", "modelprovider/settings.go", "parentbenefits/module.go", "portal/market.go", "portal/moderate.go", "portal/module.go", "portal/products.go"},
	"authz.LockProjectMutation": {"authz/agent_creation.go", "authz/members.go", "authz/project_members.go", "importer/users_backfill.go", "importer/writer.go", "statusautopilot/settings.go"},
	"authz.LockProjectWrite":    {"attachments/module.go", "decisiondesk/notifications.go", "events/causal_undo.go", "events/module.go", "harness/lead_decisions.go", "knowledge/learnings.go", "nodes/causal_undo.go", "nodes/portal_publish.go", "themes/store.go", "themes/undo.go"},
	"operatoractor.Ensure":      {"auth/store.go", "authz/operator.go", "operatoractor/actor.go"},
	"rules.PrepareWrite":        {"knowledge/learning_draft.go"},
}

func TestSharedFenceCallerInventory(t *testing.T) {
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
			if _, ok := sharedFenceCallers[key]; ok {
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
	for key, want := range sharedFenceCallers {
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
		// Stall alerts (AEON-849) prepare System under the tenant fence in a
		// separate transaction, then enter the shared tree fence before rows,
		// lead authority and inbox acceptance. Acceptance is the first event.
		{"../delivery/alerts.go", "alertItem", "db.LockTenant(", "systemactor.Ensure("},
		{"../delivery/alerts.go", "alertItem", "db.LockTree(", "authz.RequireTx("},
		{"../delivery/alerts.go", "alertItem", "db.LockTree(", "FOR KEY SHARE"},
		{"../delivery/alerts.go", "alertItem", "FOR KEY SHARE", "inbox.AcceptMessageTx("},
		{"../delivery/alerts.go", "alertItem", "INSERT INTO delivery_alerts", "inbox.AcceptMessageTx("},
		{"../delivery/alerts.go", "alertItem", "inbox.AcceptMessageTx(", "events.Append("},
		// Merge audit (AEON-852) prepares the System actor in its own fenced
		// transaction. Writes and retries take tenant/tree before audit and
		// recipient rows; recipient authorization and writes precede events.
		{"../delivery/audit_store.go", "auditActor", "db.LockTenant(", "systemactor.Ensure("},
		{"../delivery/audit_webhook.go", "auditCheckEvent", "db.LockTenant(", "INSERT INTO delivery_github_events"},
		{"../delivery/audit_store.go", "auditMerge", "db.LockTree(", "auditRecipientTx("},
		{"../delivery/audit_store.go", "auditMerge", "auditRecipientTx(", "INSERT INTO delivery_merge_audit"},
		{"../delivery/audit_store.go", "auditMerge", "INSERT INTO delivery_merge_audit", "events.Append("},
		{"../delivery/audit.go", "retryAuditAlerts", "db.LockTree(", "FOR NO KEY UPDATE"},
		{"../delivery/audit.go", "retryAuditAlerts", "db.LockTree(", "auditRecipientTx("},
		{"../delivery/audit.go", "retryAuditAlerts", "auditRecipientTx(", "UPDATE delivery_merge_audit"},
		{"../delivery/audit.go", "retryAuditAlerts", "UPDATE delivery_merge_audit", "auditAlert("},
		// Merge-queue quarantine (AEON-850) takes the shared tenant fence before
		// failure rows and the ledger. recordTx appends the event counter after.
		// Delivery Flow (AEON-1004): tenant fence, then authorization and the
		// item/step/incident rows; the value-free hints are appended last.
		{"../delivery/flow_store.go", "flowWrite", "db.LockTenant(", "build(ctx, tx, apply)"},
		{"../delivery/flow_store.go", "flowWrite", "build(ctx, tx, apply)", "events.Append("},
		{"../delivery/quarantine.go", "quarantineEvent", "db.LockTenant(", "INSERT INTO delivery_queue_failures"},
		{"../delivery/quarantine.go", "quarantineEvent", "INSERT INTO delivery_queue_failures", "recordTx("},
		// Shadow admission (AEON-887) re-checks authority under the tenant fence
		// after the GitHub WIP read. The decision row precedes the event.
		{"../engineadmission/module.go", "admit", "db.LockTenant(", "INSERT INTO engine_admission_decisions"},
		{"../engineadmission/module.go", "admit", "INSERT INTO engine_admission_decisions", "events.Append("},
		{"../engineadmission/module.go", "settings", "db.LockTenant(", "authz.RequireTx("},
		{"../engineadmission/module.go", "settings", "authz.RequireTx(", "events.Append("},
		// Attention bulk (AEON-914) enters the canonical tenant fence, then the
		// pairing advisory, then the shared tree advisory, before any batch row.
		// Item commits lock the batch before node resolution; the summary event
		// is appended only after that batch lock, with no later fence.
		{"../statusautopilot/attention_bulk.go", "attentionMutationContext", "db.LockTenant(", "aeon-pairing:"},
		{"../statusautopilot/attention_bulk.go", "attentionMutationContext", "aeon-pairing:", "return lock("},
		{"../statusautopilot/attention_bulk.go", "processAttentionBatch", "lockAttentionBatch(", "resolveAttention("},
		{"../statusautopilot/attention_bulk.go", "processAttentionBatch", "lockAttentionBatch(", "events.Append("},
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
	// The message-id writeback touches only the row inserted before acceptance.
	// It must not take a fence, advisory lock or FK share lock after that event.
	body := functionBody(t, "../delivery/alerts.go", "alertItem")
	accept := strings.Index(body, "inbox.AcceptMessageTx(")
	if accept < 0 {
		t.Fatal("delivery.alertItem missing inbox acceptance")
	}
	tail := body[accept:]
	if strings.Contains(tail, "FOR KEY SHARE") || strings.Contains(tail, "db.Lock") || strings.Contains(tail, "pg_advisory") || strings.Contains(tail, "FOR UPDATE") || strings.Contains(tail, "FOR NO KEY UPDATE") {
		t.Error("delivery.alertItem must not acquire fences or row locks after inbox acceptance")
	}
	if !strings.Contains(tail, "UPDATE delivery_alerts SET inbox_message_id") || strings.Index(tail, "events.Append(") < strings.Index(tail, "UPDATE delivery_alerts SET inbox_message_id") {
		t.Error("delivery.alertItem must write the accepted message id before the stall event")
	}
	// The bulk summary is the last event in the function. Nothing after it may
	// take a tenant, tree, pairing or batch fence.
	bulk := functionBody(t, "../statusautopilot/attention_bulk.go", "processAttentionBatch")
	summary := strings.LastIndex(bulk, "events.Append(")
	if summary < 0 {
		t.Fatal("attention bulk missing summary event")
	}
	afterSummary := bulk[summary:]
	if strings.Contains(afterSummary, "db.Lock") || strings.Contains(afterSummary, "pg_advisory") || strings.Contains(afterSummary, "FOR UPDATE") || strings.Contains(afterSummary, "FOR NO KEY UPDATE") || strings.Contains(afterSummary, "lockAttentionBatch(") || strings.Contains(afterSummary, "lock(") {
		t.Error("attention bulk must not acquire fences after the summary event")
	}
}

func TestEngineAdmissionJoinsSharedTenantFence(t *testing.T) {
	// Risk: shadow admission reads GitHub between two transactions. The write
	// must re-enter db.LockTenant, and that caller stays in the shared inventory.
	body := functionBody(t, "../engineadmission/module.go", "admit")
	lock := strings.Index(body, "db.LockTenant(")
	preview := strings.Index(body, "admissionAuthority(")
	insert := strings.Index(body, "INSERT INTO engine_admission_decisions")
	event := strings.Index(body, "events.Append(")
	if lock < 0 || preview < 0 || preview >= lock || insert < lock || event < insert {
		t.Fatal("admit must preview authority, re-check it under the tenant fence, then append the event")
	}
	if strings.Index(body[lock:], "admissionAuthority(") < 0 {
		t.Fatal("admit must re-check admission authority after the tenant fence")
	}
	tail := body[event:]
	if strings.Contains(tail, "db.Lock") || strings.Contains(tail, "FOR UPDATE") || strings.Contains(tail, "FOR NO KEY UPDATE") {
		t.Fatal("admit must not take another lock after the event")
	}
	var listed bool
	for _, path := range sharedFenceCallers["db.LockTenant"] {
		if path == "engineadmission/module.go" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("engine admission must be inventoried as a db.LockTenant caller")
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
