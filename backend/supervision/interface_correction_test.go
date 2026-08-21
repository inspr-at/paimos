// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	storedb "github.com/inspr-at/paimos/backend/db"
	"modernc.org/sqlite"
)

func TestGrantTargetsAreExactLiveOrderedAndOutsideSealedActions(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "input.respond", "run.pause", "run.resume"},
		OperationKeyDigest: sha256.Sum256([]byte("targets-lease"))})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "approval", PromptTemplate: "approval_required",
		OperationKeyDigest: sha256.Sum256([]byte("targets-approval"))})
	if err != nil {
		t.Fatal(err)
	}
	choice, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, Kind: "choice", PromptTemplate: "choice_required",
		OptionCodes:        []string{"choice_1", "choice_2"},
		OperationKeyDigest: sha256.Sum256([]byte("targets-choice"))})
	if err != nil {
		t.Fatal(err)
	}
	grantKey := sha256.Sum256([]byte("targets-grant"))
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: grantKey})
	if err != nil {
		t.Fatalf("issue grant: %v (%s)", err, ErrorCode(err))
	}
	wantActions := []Action{"issue.priority.set", "run.cancel.running", "input.respond", "run.pause"}
	if fmt.Sprint(grant.Actions) != fmt.Sprint(wantActions) {
		t.Fatalf("sealed actions=%v want=%v", grant.Actions, wantActions)
	}
	if len(grant.Targets) != 5 {
		t.Fatalf("targets=%+v", grant.Targets)
	}
	if grant.Targets[0].Action != "issue.priority.set" || grant.Targets[0].RunID != 0 ||
		grant.Targets[0].InputRequestID != "" || grant.Targets[1].Action != "run.cancel.running" ||
		grant.Targets[1].RunID != runID || grant.Targets[1].RuntimeRevision != 0 {
		t.Fatalf("static/run target shapes=%+v", grant.Targets[:2])
	}
	inputTargets := grant.Targets[2:4]
	if inputTargets[0].InputRequestID > inputTargets[1].InputRequestID {
		t.Fatalf("input targets not deterministic: %+v", inputTargets)
	}
	byID := map[string]GrantTarget{inputTargets[0].InputRequestID: inputTargets[0], inputTargets[1].InputRequestID: inputTargets[1]}
	if target := byID[approval.RequestID]; target.Action != "input.respond" || target.InputKind != "approval" ||
		target.InputRequestRevision != 1 || len(target.OptionCodes) != 0 || target.RunID != 0 {
		t.Fatalf("approval target=%+v", target)
	}
	if target := byID[choice.RequestID]; target.InputKind != "choice" ||
		fmt.Sprint(target.OptionCodes) != "[choice_1 choice_2]" || target.InputRequestRevision != 1 {
		t.Fatalf("choice target=%+v", target)
	}
	if pause := grant.Targets[4]; pause.Action != "run.pause" || pause.RunID != runID ||
		pause.RuntimeState != "running" || pause.RuntimeRevision != 1 || pause.InputRequestID != "" {
		t.Fatalf("pause target=%+v", pause)
	}
	canary := "PAIMOS_SECRET_TARGET_CANARY_7f3d"
	if _, err := database.Exec(`UPDATE api_keys SET name=? WHERE id=?`, canary, runner.APIKeyID()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET agent_name=? WHERE id=?`, canary, runID); err != nil {
		t.Fatal(err)
	}
	secretCheck, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
		Revision: grant.Revision})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(secretCheck)
	if err != nil || strings.Contains(string(encoded), canary) {
		t.Fatalf("target projection leaked secret sentinel json=%s err=%v", encoded, err)
	}

	// Superseding one input changes only the ephemeral target set. Same-key
	// replay neither mutates the grant nor changes its sealed action union.
	revised, err := service.CreateInputRequest(context.Background(), runner, InputCreateRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, RequestID: approval.RequestID, Kind: "approval",
		PromptTemplate: "approval_required", OperationKeyDigest: sha256.Sum256([]byte("targets-approval-v2"))})
	if err != nil {
		t.Fatal(err)
	}
	var grantRows, grantEvents int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_capability_grants WHERE grant_id=?`, grant.GrantID).Scan(&grantRows); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE grant_id=?`, grant.GrantID).Scan(&grantEvents); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: grantKey})
	if err != nil {
		t.Fatalf("grant replay: %v (%s)", err, ErrorCode(err))
	}
	if replayed.Revision != grant.Revision || fmt.Sprint(replayed.Actions) != fmt.Sprint(grant.Actions) {
		t.Fatalf("replay changed sealed grant old=%+v new=%+v", grant, replayed)
	}
	foundOld, foundNew := false, false
	for _, target := range replayed.Targets {
		foundOld = foundOld || target.InputRequestID == approval.RequestID && target.InputRequestRevision == approval.Revision
		foundNew = foundNew || target.InputRequestID == revised.RequestID && target.InputRequestRevision == revised.Revision
	}
	if foundOld || !foundNew {
		t.Fatalf("replay did not replace target old=%t new=%t targets=%+v", foundOld, foundNew, replayed.Targets)
	}
	var afterRows, afterEvents int
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_capability_grants WHERE grant_id=?`, grant.GrantID).Scan(&afterRows)
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE grant_id=?`, grant.GrantID).Scan(&afterEvents)
	if afterRows != grantRows || afterEvents != grantEvents {
		t.Fatalf("replay mutated grant rows %d->%d events %d->%d", grantRows, afterRows, grantEvents, afterEvents)
	}
	var commandsBefore, commandsAfter int
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_commands`).Scan(&commandsBefore)
	_, err = service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "input.respond", InputRequestID: approval.RequestID,
		InputRequestRevision: approval.Revision, InputResponse: "approve",
		OperationKeyDigest: sha256.Sum256([]byte("targets-stale-command"))})
	if !IsCode(err, CodeStaleTarget) {
		t.Fatalf("captured stale target error=%v code=%s", err, ErrorCode(err))
	}
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_commands`).Scan(&commandsAfter)
	if commandsAfter != commandsBefore {
		t.Fatalf("stale target created command %d->%d", commandsBefore, commandsAfter)
	}

	revokeKey := sha256.Sum256([]byte("targets-revoke"))
	revoked, err := service.RevokeActorGrant(context.Background(), human, GrantRevokeRequest{GrantID: grant.GrantID,
		Revision: grant.Revision, OperationKeyDigest: revokeKey})
	if err != nil || len(revoked.Targets) != 0 {
		t.Fatalf("revoke projection=%+v err=%v", revoked, err)
	}
	revokedReplay, err := service.RevokeActorGrant(context.Background(), human, GrantRevokeRequest{GrantID: grant.GrantID,
		Revision: grant.Revision, OperationKeyDigest: revokeKey})
	if err != nil || len(revokedReplay.Targets) != 0 || fmt.Sprint(revokedReplay.Actions) != fmt.Sprint(grant.Actions) {
		t.Fatalf("revoke replay=%+v err=%v", revokedReplay, err)
	}
}

func TestGrantTargetsDropStaleRunnerAuthorityWithoutChangingSealedActions(t *testing.T) {
	t.Run("disabled key", func(t *testing.T) {
		database := openSupervisionTestDB(t)
		deliveryID, humanID, human := seedGrantTarget(t, database)
		runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
		service := NewService(database, Options{})
		lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
			DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "run.pause", "run.resume"},
			OperationKeyDigest: sha256.Sum256([]byte("filter-key-lease"))})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
			OperationKeyDigest: sha256.Sum256([]byte("filter-key-grant"))})
		if err != nil || len(grant.Targets) < 3 {
			t.Fatalf("initial grant=%+v err=%v", grant, err)
		}
		if _, err := database.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
			runner.APIKeyID()); err != nil {
			t.Fatal(err)
		}
		filtered, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
			Revision: grant.Revision})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(filtered.Actions) != fmt.Sprint(grant.Actions) || len(filtered.Targets) != 1 ||
			filtered.Targets[0].Action != "issue.priority.set" {
			t.Fatalf("disabled key surface actions=%v targets=%+v lease=%+v", filtered.Actions, filtered.Targets, lease)
		}
	})

	t.Run("lease expiry boundary", func(t *testing.T) {
		database := openSupervisionTestDB(t)
		deliveryID, humanID, human := seedGrantTarget(t, database)
		runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
		service := NewService(database, Options{})
		service.leaseTTL = 10 * time.Second
		lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
			DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"},
			OperationKeyDigest: sha256.Sum256([]byte("filter-expiry-lease"))})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
			OperationKeyDigest: sha256.Sum256([]byte("filter-expiry-grant"))})
		if err != nil || !containsAction(grant.Actions, "run.cancel.running") {
			t.Fatalf("initial grant=%+v err=%v", grant, err)
		}
		before, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
			Revision: grant.Revision})
		if err != nil || !grantHasTarget(before, "run.cancel.running") {
			t.Fatalf("T-1 target surface=%+v err=%v", before.Targets, err)
		}
		if remaining := time.Until(lease.ExpiresAt.Add(time.Millisecond)); remaining > 0 {
			time.Sleep(remaining)
		}
		filtered, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
			Revision: grant.Revision})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(filtered.Actions) != fmt.Sprint(grant.Actions) || len(filtered.Targets) != 1 ||
			filtered.Targets[0].Action != "issue.priority.set" {
			t.Fatalf("expired lease surface actions=%v targets=%+v", filtered.Actions, filtered.Targets)
		}
		after, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
			Revision: grant.Revision})
		if err != nil || grantHasTarget(after, "run.cancel.running") {
			t.Fatalf("T+1 target surface=%+v err=%v", after.Targets, err)
		}
	})
}

func TestGrantTargetsCoverQueuedAndRunnerAuthorityLoss(t *testing.T) {
	t.Run("queued cancel", func(t *testing.T) {
		database := openSupervisionTestDB(t)
		deliveryID, humanID, human := seedGrantTarget(t, database)
		runID, _ := seedRunnerActivation(t, database, deliveryID, humanID)
		if _, err := database.Exec(`UPDATE agent_runs SET status='queued',started_at=NULL WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		grant, err := NewService(database, Options{}).IssueActorGrant(context.Background(), human, GrantIssueRequest{
			DeliveryID: deliveryID, OperationKeyDigest: sha256.Sum256([]byte("queued-target-grant"))})
		if err != nil || !grantHasExactRunTarget(grant, "run.cancel.queued", runID) {
			t.Fatalf("queued target grant=%+v err=%v", grant, err)
		}
	})

	for _, mutation := range []string{"expired key", "inactive runner", "terminal run"} {
		t.Run(mutation, func(t *testing.T) {
			database := openSupervisionTestDB(t)
			deliveryID, humanID, human := seedGrantTarget(t, database)
			runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
			service := NewService(database, Options{})
			if _, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
				DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running", "run.pause", "run.resume"},
				OperationKeyDigest: sha256.Sum256([]byte("loss-lease-" + mutation))}); err != nil {
				t.Fatal(err)
			}
			grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
				OperationKeyDigest: sha256.Sum256([]byte("loss-grant-" + mutation))})
			if err != nil || !grantHasTarget(grant, "run.cancel.running") {
				t.Fatalf("initial target grant=%+v err=%v", grant, err)
			}
			switch mutation {
			case "expired key":
				if _, err := database.Exec(`UPDATE api_keys SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
					runner.APIKeyID()); err != nil {
					t.Fatal(err)
				}
			case "inactive runner":
				if _, err := database.Exec(`UPDATE users SET status='inactive' WHERE id=?`, runner.UserID()); err != nil {
					t.Fatal(err)
				}
			case "terminal run":
				if _, err := database.Exec(`UPDATE agent_runs SET status='completed',finished_at=datetime('now') WHERE id=?`, runID); err != nil {
					t.Fatal(err)
				}
			}
			filtered, err := service.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
				Revision: grant.Revision})
			if err != nil || grantHasTarget(filtered, "run.cancel.running") || grantHasTarget(filtered, "run.pause") {
				t.Fatalf("%s target survived targets=%+v err=%v", mutation, filtered.Targets, err)
			}
		})
	}
}

func grantHasTarget(grant GrantProjection, action Action) bool {
	for _, target := range grant.Targets {
		if target.Action == action {
			return true
		}
	}
	return false
}

func grantHasExactRunTarget(grant GrantProjection, action Action, runID int64) bool {
	for _, target := range grant.Targets {
		if target.Action == action && target.RunID == runID {
			return true
		}
	}
	return false
}

func TestGrantTargetEnumerationHasBoundedQueriesAndIndexedPlan(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, human := seedGrantTarget(t, database)
	runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"input.respond"},
		OperationKeyDigest: sha256.Sum256([]byte("target-count-lease"))})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		request := InputCreateRequest{LeaseID: lease.LeaseID, LeaseRevision: lease.Revision,
			Kind: "approval", PromptTemplate: "approval_required",
			OperationKeyDigest: sha256.Sum256([]byte(fmt.Sprintf("target-count-input-%d", index)))}
		if index == 1 {
			request.Kind, request.PromptTemplate = "choice", "choice_required"
			request.OptionCodes = []string{"choice_1", "choice_2", "choice_3", "choice_4",
				"choice_5", "choice_6", "choice_7", "choice_8"}
		}
		if _, err := service.CreateInputRequest(context.Background(), runner, request); err != nil {
			t.Fatal(err)
		}
	}
	grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("target-count-grant"))})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(`EXPLAIN QUERY PLAN SELECT request.request_id
		FROM control_capability_leases lease INDEXED BY idx_control_leases_binding
		JOIN control_input_requests request INDEXED BY idx_control_inputs_run
		 ON request.agent_run_id=lease.agent_run_id
		WHERE lease.delivery_id=? AND lease.root_issue_id=? AND request.revision>0`,
		deliveryID, lease.Target.RootIssueID)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	rows.Close()
	if !strings.Contains(plan.String(), "idx_control_leases_binding") ||
		!strings.Contains(plan.String(), "idx_control_inputs_run") {
		t.Fatalf("target plan is not indexed: %s", plan.String())
	}
	var databaseSequence int
	var databaseName, databasePath string
	if err := database.QueryRow(`PRAGMA database_list`).Scan(&databaseSequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	storedb.DB = nil
	counter := &atomic.Int64{}
	driverName := fmt.Sprintf("supervision-target-counting-sqlite-%d", countingDriverSequence.Add(1))
	sql.Register(driverName, countingDriver{inner: &sqlite.Driver{}, count: counter})
	countingDB, err := sql.Open(driverName, databasePath+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = countingDB.Close() })
	countedService := NewService(countingDB, Options{})
	counter.Store(0)
	projection, err := countedService.GetActorGrant(context.Background(), human, GrantGetRequest{GrantID: grant.GrantID,
		Revision: grant.Revision})
	if err != nil {
		t.Fatal(err)
	}
	queryCount := counter.Load()
	if queryCount < 1 || queryCount > 16 || len(projection.Targets) != 3 {
		t.Fatalf("target enumeration queries=%d targets=%+v", queryCount, projection.Targets)
	}
	// Both independent input lineages and the static target were returned under
	// the same fixed statement ceiling; option rows are aggregated by the one
	// input enumeration query rather than per target.
}

func TestActorResourcesConcealSameUserOtherCredentials(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, userID, owner := seedGrantTarget(t, database)
	service := NewService(database, Options{})
	grantKey := sha256.Sum256([]byte("credential-owner-grant"))
	grant, err := service.IssueActorGrant(context.Background(), owner, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: grantKey})
	if err != nil {
		t.Fatal(err)
	}
	command, err := service.CreateCommand(context.Background(), owner, CommandCreateRequest{GrantID: grant.GrantID,
		GrantRevision: grant.Revision, Action: "issue.priority.set", Priority: "high",
		OperationKeyDigest: sha256.Sum256([]byte("credential-owner-command"))})
	if err != nil {
		t.Fatal(err)
	}
	secondCredential := "22345678-1234-4234-9234-123456789abc"
	if _, err := database.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('supervision-bearer-2',?,'2030-12-01 00:00:00','2026-08-01 00:00:00',?)`,
		userID, secondCredential); err != nil {
		t.Fatal(err)
	}
	secondSession, _ := auth.NewSessionPrincipal(secondCredential, userID, userID, false)
	keyRow, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'other-actor',?,'paimos_other_actor',?)`, userID,
		fmt.Sprintf("%064x", sha256.Sum256([]byte("other-actor-key"))), ScopeActorWrite)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := keyRow.LastInsertId()
	otherAPIKey, _ := auth.NewAPIKeyPrincipal(keyID, userID, auth.ScopeSet{ScopeActorWrite: {}})
	var operationsBefore int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	for name, intruder := range map[string]auth.Principal{"session": secondSession, "api_key": otherAPIKey} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.GetCommand(context.Background(), intruder, CommandGetRequest{CommandID: command.CommandID}); !IsCode(err, CodeTargetNotFound) {
				t.Fatalf("get error=%v code=%s", err, ErrorCode(err))
			}
			if _, err := service.ConfirmCommand(context.Background(), intruder, CommandConfirmRequest{CommandID: command.CommandID,
				StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("intruder-confirm-" + name))}); !IsCode(err, CodeTargetNotFound) {
				t.Fatalf("confirm error=%v code=%s", err, ErrorCode(err))
			}
			if _, err := service.WithdrawCommand(context.Background(), intruder, CommandWithdrawRequest{CommandID: command.CommandID,
				StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("intruder-withdraw-" + name))}); !IsCode(err, CodeTargetNotFound) {
				t.Fatalf("withdraw error=%v code=%s", err, ErrorCode(err))
			}
			if _, err := service.RevokeActorGrant(context.Background(), intruder, GrantRevokeRequest{GrantID: grant.GrantID,
				Revision: grant.Revision, OperationKeyDigest: sha256.Sum256([]byte("intruder-revoke-" + name))}); !IsCode(err, CodeTargetNotFound) {
				t.Fatalf("revoke error=%v code=%s", err, ErrorCode(err))
			}
		})
	}
	var operationsAfter int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys`).Scan(&operationsAfter); err != nil {
		t.Fatal(err)
	}
	if operationsAfter != operationsBefore {
		t.Fatalf("cross-credential denials persisted operations %d->%d", operationsBefore, operationsAfter)
	}
	owned, err := service.GetCommand(context.Background(), owner, CommandGetRequest{CommandID: command.CommandID})
	if err != nil || owned.Display.IssueKey != "SUP-1" || owned.Display.DeliveryKey != "issue:1" {
		t.Fatalf("owner projection=%+v err=%v", owned, err)
	}
	terminalService := NewService(database, Options{Mutator: priorityMutator{}, Changes: testChanges{}})
	terminal, err := terminalService.ConfirmCommand(context.Background(), owner, CommandConfirmRequest{CommandID: command.CommandID,
		StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("credential-owner-confirm"))})
	if err != nil || terminal.Status != "applied" || terminal.Display.IssueKey != owned.Display.IssueKey ||
		terminal.Display.DeliveryKey != owned.Display.DeliveryKey {
		t.Fatalf("terminal display old=%+v terminal=%+v err=%v", owned.Display, terminal, err)
	}
	reloaded, err := service.GetCommand(context.Background(), owner, CommandGetRequest{CommandID: command.CommandID})
	if err != nil || reloaded.Display != terminal.Display {
		t.Fatalf("terminal reload display=%+v want=%+v err=%v", reloaded.Display, terminal.Display, err)
	}
	var projectID int64
	if err := database.QueryRow(`SELECT project_id FROM control_commands WHERE command_id=?`, command.CommandID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`,
		projectID, userID); err != nil {
		t.Fatal(err)
	}
	if projection, err := service.GetCommand(context.Background(), owner, CommandGetRequest{CommandID: command.CommandID}); err == nil || projection.CommandID != "" {
		t.Fatalf("viewer access disclosed command=%+v err=%v", projection, err)
	}
	if _, err := database.Exec(`UPDATE project_members SET access_level='editor' WHERE project_id=? AND user_id=?`,
		projectID, userID); err != nil {
		t.Fatal(err)
	}

	// Rotating the shared grant lineage to another credential must make the
	// original operation replay fail closed, not return the later revision.
	rotated, err := service.IssueActorGrant(context.Background(), secondSession, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: sha256.Sum256([]byte("credential-rotation"))})
	if err != nil || rotated.Revision != grant.Revision+1 {
		t.Fatalf("rotation=%+v err=%v", rotated, err)
	}
	if projection, err := service.IssueActorGrant(context.Background(), owner, GrantIssueRequest{DeliveryID: deliveryID,
		OperationKeyDigest: grantKey}); err == nil || projection.GrantID != "" {
		t.Fatalf("old owner replay disclosed projection=%+v err=%v", projection, err)
	}
	movedProject, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Moved command','MCD','active')`)
	if err != nil {
		t.Fatal(err)
	}
	movedProjectID, _ := movedProject.LastInsertId()
	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=(SELECT root_issue_id FROM control_commands WHERE command_id=?)`,
		movedProjectID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if projection, err := service.GetCommand(context.Background(), owner, CommandGetRequest{CommandID: command.CommandID}); !IsCode(err, CodeTargetNotFound) || projection.CommandID != "" {
		t.Fatalf("moved target disclosed command=%+v err=%v code=%s", projection, err, ErrorCode(err))
	}
	if _, err := database.Exec(`UPDATE projects SET status='deleted' WHERE id=?`, movedProjectID); err != nil {
		t.Fatal(err)
	}
	if projection, err := service.GetCommand(context.Background(), owner, CommandGetRequest{CommandID: command.CommandID}); !IsCode(err, CodeTargetNotFound) || projection.CommandID != "" {
		t.Fatalf("deleted target disclosed command=%+v err=%v code=%s", projection, err, ErrorCode(err))
	}
}

func TestRunnerLeaseAndPullRequireExactKeyAndDevice(t *testing.T) {
	database := openSupervisionTestDB(t)
	deliveryID, humanID, _ := seedGrantTarget(t, database)
	runID, owner := seedRunnerActivation(t, database, deliveryID, humanID)
	service := NewService(database, Options{})
	issueKey := sha256.Sum256([]byte("runner-exact-issue"))
	lease, err := service.IssueRunnerLease(context.Background(), owner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"}, OperationKeyDigest: issueKey})
	if err != nil {
		t.Fatal(err)
	}
	otherRow, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'other-runner',?,'paimos_other_runner',?)`, owner.UserID(),
		fmt.Sprintf("%064x", sha256.Sum256([]byte("other-runner-key"))), ScopeRunner)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := otherRow.LastInsertId()
	other, _ := auth.NewAPIKeyPrincipal(otherID, owner.UserID(), auth.ScopeSet{ScopeRunner: {}})
	var operationsBefore, eventsBefore int
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys`).Scan(&operationsBefore)
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE lease_id=?`, lease.LeaseID).Scan(&eventsBefore)
	if _, err := service.RenewRunnerLease(context.Background(), other, LeaseRenewRequest{LeaseID: lease.LeaseID,
		Revision: lease.Revision, DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"},
		OperationKeyDigest: sha256.Sum256([]byte("other-renew"))}); !IsCode(err, CodeTargetNotFound) {
		t.Fatalf("other-key renew error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := service.RevokeRunnerLease(context.Background(), other, LeaseRevokeRequest{LeaseID: lease.LeaseID,
		Revision: lease.Revision, DeviceID: "runner-01",
		OperationKeyDigest: sha256.Sum256([]byte("other-revoke"))}); !IsCode(err, CodeTargetNotFound) {
		t.Fatalf("other-key revoke error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := service.RenewRunnerLease(context.Background(), owner, LeaseRenewRequest{LeaseID: lease.LeaseID,
		Revision: lease.Revision, DeviceID: "other-device", SupportedActions: []Action{"run.cancel.running"},
		OperationKeyDigest: sha256.Sum256([]byte("wrong-device-renew"))}); !IsCode(err, CodeTargetNotFound) {
		t.Fatalf("wrong-device renew error=%v code=%s", err, ErrorCode(err))
	}
	if _, err := service.Pull(context.Background(), owner, PullRequest{LeaseID: lease.LeaseID,
		LeaseRevision: lease.Revision, DeviceID: "other-device"}); err == nil {
		t.Fatal("wrong-device pull succeeded")
	}
	var operationsAfter, eventsAfter int
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_operation_keys`).Scan(&operationsAfter)
	_ = database.QueryRow(`SELECT COUNT(*) FROM control_events WHERE lease_id=?`, lease.LeaseID).Scan(&eventsAfter)
	if operationsAfter != operationsBefore || eventsAfter != eventsBefore {
		t.Fatalf("denials mutated operations=%d->%d events=%d->%d",
			operationsBefore, operationsAfter, eventsBefore, eventsAfter)
	}
	rotated, err := service.IssueRunnerLease(context.Background(), other, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"},
		OperationKeyDigest: sha256.Sum256([]byte("other-issue"))})
	if err != nil || rotated.Revision != lease.Revision+1 {
		t.Fatalf("other-key rotation=%+v err=%v", rotated, err)
	}
	if projection, err := service.IssueRunnerLease(context.Background(), owner, LeaseIssueRequest{RunID: runID,
		DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"}, OperationKeyDigest: issueKey}); err == nil || projection.LeaseID != "" {
		t.Fatalf("old issue replay disclosed projection=%+v err=%v", projection, err)
	}
}

type runningCancelMutator struct{}

func (runningCancelMutator) SetIssuePriorityTx(context.Context, *sql.Tx, PriorityMutation) error {
	return errors.New("unexpected priority mutation")
}
func (runningCancelMutator) CancelQueuedRunTx(context.Context, *sql.Tx, RunCancellationMutation) error {
	return errors.New("unexpected queued cancellation")
}
func (runningCancelMutator) CancelRunningRunTx(ctx context.Context, tx *sql.Tx, mutation RunCancellationMutation) error {
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='cancelled',finished_at=datetime('now')
		WHERE id=? AND issue_id=? AND status='running'`, mutation.RunID, mutation.IssueID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("running run disappeared")
	}
	return nil
}

func TestCancellationFactsUseServerClockOnConfirmationAndResult(t *testing.T) {
	t.Run("queued confirmation", func(t *testing.T) {
		database := openSupervisionTestDB(t)
		deliveryID, humanID, human := seedGrantTarget(t, database)
		runID, _ := seedRunnerActivation(t, database, deliveryID, humanID)
		if _, err := database.Exec(`UPDATE agent_runs SET status='queued',started_at=NULL WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TABLE supervision_test_hints(command_id TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		service := NewService(database, Options{Mutator: queuedCancelMutator{}, Changes: trackingChanges{}})
		grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
			OperationKeyDigest: sha256.Sum256([]byte("fact-queued-grant"))})
		if err != nil {
			t.Fatal(err)
		}
		command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
			GrantRevision: grant.Revision, Action: "run.cancel.queued", RunID: runID,
			OperationKeyDigest: sha256.Sum256([]byte("fact-queued-command"))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
			StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("fact-queued-confirm"))}); err != nil {
			t.Fatalf("confirm: %v (%s)", err, ErrorCode(err))
		}
		assertServerCancellationFact(t, database, runID, command.CommandID)
	})

	t.Run("running result", func(t *testing.T) {
		database := openSupervisionTestDB(t)
		deliveryID, humanID, human := seedGrantTarget(t, database)
		runID, runner := seedRunnerActivation(t, database, deliveryID, humanID)
		service := NewService(database, Options{Mutator: runningCancelMutator{}, Changes: testChanges{}})
		lease, err := service.IssueRunnerLease(context.Background(), runner, LeaseIssueRequest{RunID: runID,
			DeviceID: "runner-01", SupportedActions: []Action{"run.cancel.running"},
			OperationKeyDigest: sha256.Sum256([]byte("fact-running-lease"))})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := service.IssueActorGrant(context.Background(), human, GrantIssueRequest{DeliveryID: deliveryID,
			OperationKeyDigest: sha256.Sum256([]byte("fact-running-grant"))})
		if err != nil {
			t.Fatal(err)
		}
		command, err := service.CreateCommand(context.Background(), human, CommandCreateRequest{GrantID: grant.GrantID,
			GrantRevision: grant.Revision, Action: "run.cancel.running", RunID: runID,
			OperationKeyDigest: sha256.Sum256([]byte("fact-running-command"))})
		if err != nil {
			t.Fatal(err)
		}
		command, err = service.ConfirmCommand(context.Background(), human, CommandConfirmRequest{CommandID: command.CommandID,
			StatusRevision: 1, OperationKeyDigest: sha256.Sum256([]byte("fact-running-confirm"))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Claim(context.Background(), runner, ClaimRequest{CommandID: command.CommandID,
			LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, DeviceID: "runner-01",
			OperationKeyDigest: sha256.Sum256([]byte("fact-running-claim"))}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RecordResult(context.Background(), runner, ResultRequest{CommandID: command.CommandID,
			LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, EffectSequence: 1, ClaimSequence: 1,
			ResultSequence: 1, DeviceID: "runner-01", Outcome: "applied",
			OperationKeyDigest: sha256.Sum256([]byte("fact-running-result"))}); err != nil {
			t.Fatalf("result: %v (%s)", err, ErrorCode(err))
		}
		assertServerCancellationFact(t, database, runID, command.CommandID)
	})
}

func assertServerCancellationFact(t *testing.T, database *sql.DB, runID int64, commandID string) {
	t.Helper()
	var cause, storedCommand, recordedAt string
	var serverOwned int
	if err := database.QueryRow(`SELECT cancellation_cause,command_id,recorded_at,
		recorded_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM agent_run_cancellation_facts WHERE run_id=?`,
		runID).Scan(&cause, &storedCommand, &recordedAt, &serverOwned); err != nil {
		t.Fatal(err)
	}
	if cause != "operator_command" || storedCommand != commandID || recordedAt == "" || serverOwned != 1 {
		t.Fatalf("fact cause=%s command=%s recorded_at=%s server_owned=%d", cause, storedCommand, recordedAt, serverOwned)
	}
}
