package agentmode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/delivery"
)

func TestAgentModeReplayQueryUsesCanonicalAuthorizationAndBoundParameters(t *testing.T) {
	if !strings.HasPrefix(agentModeReplayQuery, auth.AgentModeAuthorizationCTE+",\nreplay AS (") {
		t.Fatal("replay query does not begin with the canonical authorization CTE")
	}
	if placeholders := strings.Count(agentModeReplayQuery, "?"); placeholders != 11 {
		t.Fatalf("replay query placeholders=%d, want 11 bound values", placeholders)
	}
	if strings.Contains(agentModeReplayQuery, "%!") || strings.Contains(agentModeReplayQuery, "%s") ||
		strings.Contains(agentModeReplayQuery, "%d") {
		t.Fatal("replay query contains a runtime formatting directive")
	}
}

func TestStreamMoveDualAccessResetsOldScopeAndRefetchesTargetAndGlobal(t *testing.T) {
	database := openAgentModeTestDB(t)
	sourceResult, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Source','SRC','active')`)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, _ := sourceResult.LastInsertId()
	targetResult, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Target','TGT','active')`)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := targetResult.LastInsertId()
	issueResult, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Move-safe delivery','in-progress')`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issueResult.LastInsertId()
	userID := insertAgentModeUser(t, database, "dual-admin", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	actor := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID, Actor: actor,
		Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: "stream-move-start"}); err != nil {
		t.Fatal(err)
	}
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	filters := Filters{Attention: "all", Health: "all"}
	sourceRequest := Request{UserID: userID, RouteProjectID: &sourceID, Filters: filters}
	targetRequest := Request{UserID: userID, RouteProjectID: &targetID, Filters: filters}
	globalRequest := Request{UserID: userID, Filters: filters}
	sourceSession, err := streamer.Open(context.Background(), sourceRequest, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sourceSession.Close()
	targetSession, err := streamer.Open(context.Background(), targetRequest, "")
	if err != nil {
		t.Fatal(err)
	}
	defer targetSession.Close()
	globalSession, err := streamer.Open(context.Background(), globalRequest, "")
	if err != nil {
		t.Fatal(err)
	}
	defer globalSession.Close()

	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if _, err := tx.Exec(`UPDATE issues SET project_id=? WHERE id=?`, targetID, issueID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := store.ProjectMoveTx(context.Background(), tx, effects, issueID, sourceID, targetID, actor, "stream-move"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	var revoked, targetHint int64
	if err := database.QueryRow(`SELECT revoked_project_id,project_id_hint FROM delivery_change_log
		WHERE kind='project_move' ORDER BY id DESC LIMIT 1`).Scan(&revoked, &targetHint); err != nil {
		t.Fatal(err)
	}
	if revoked != sourceID || targetHint != targetID {
		t.Fatalf("move audience revoked=%d target=%d", revoked, targetHint)
	}
	if _, err := sourceSession.Drain(context.Background()); !errors.Is(err, ErrReset) {
		t.Fatalf("old source scope drain error=%v, want generic reset", err)
	}
	for name, session := range map[string]*StreamSession{"target": targetSession, "global": globalSession} {
		batch, err := session.Drain(context.Background())
		if err != nil {
			t.Fatalf("%s drain: %v", name, err)
		}
		if batch.Kind != "refetch" || len(batch.Cursor) != CursorEncodedLength || len(batch.Hints) != 1 ||
			batch.Hints[0].DeliveryID == "" {
			t.Fatalf("%s batch=%+v", name, batch)
		}
	}
}

func TestStreamRejectsPerDeliveryGapButAllowsInterleavedHiddenRows(t *testing.T) {
	database := openAgentModeTestDB(t)
	visibleProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Visible','VIS','active')`)
	visibleProjectID, _ := visibleProject.LastInsertId()
	hiddenProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Hidden','HID','active')`)
	hiddenProjectID, _ := hiddenProject.LastInsertId()
	visibleIssue := insertStreamIssue(t, database, visibleProjectID, 1, "Visible root")
	hiddenIssue := insertStreamIssue(t, database, hiddenProjectID, 1, "Hidden root")
	userID := insertAgentModeUser(t, database, "stream-member", "member", "member")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer'),(?,?,'none')`,
		visibleProjectID, userID, hiddenProjectID, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	actor := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	for _, issueID := range []int64{visibleIssue, hiddenIssue} {
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID, Actor: actor,
			Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation",
			IdempotencyKey: fmt.Sprintf("stream-attempt-%d", issueID)}); err != nil {
			t.Fatal(err)
		}
	}
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	request := Request{UserID: userID, RouteProjectID: &visibleProjectID, Filters: Filters{Attention: "all", Health: "all"}}
	legitimate, err := streamer.Open(context.Background(), request, "")
	if err != nil {
		t.Fatal(err)
	}
	defer legitimate.Close()
	if _, err := database.Exec(`UPDATE issues SET title='Hidden interleave' WHERE id=?`, hiddenIssue); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Visible contiguous' WHERE id=?`, visibleIssue); err != nil {
		t.Fatal(err)
	}
	batch, err := legitimate.Drain(context.Background())
	if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 1 || batch.Hints[0].DeliveryID == "" {
		t.Fatalf("interleaved hidden row caused false reset: batch=%+v err=%v", batch, err)
	}

	corrupt, err := streamer.Open(context.Background(), request, "")
	if err != nil {
		t.Fatal(err)
	}
	defer corrupt.Close()
	if _, err := database.Exec(`UPDATE issues SET title='Second hidden interleave' WHERE id=?`, hiddenIssue); err != nil {
		t.Fatal(err)
	}
	var deliveryID, sequence, revision int64
	var deliveryKey string
	if err := database.QueryRow(`SELECT d.id,d.delivery_key,d.change_sequence_high_water,
		COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0)
		FROM deliveries d WHERE d.issue_id=?`, visibleIssue).Scan(&deliveryID, &deliveryKey, &sequence, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TRIGGER trg_delivery_change_sequence_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TRIGGER trg_deliveries_change_high_water_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,
		project_id_hint,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
		VALUES(lower(hex(randomblob(16))),?,?,?,?,?,?,'issue','issue',?,?)`, deliveryID, visibleIssue, deliveryKey,
		visibleProjectID, sequence+2, revision, visibleIssue, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := corrupt.Drain(context.Background()); !errors.Is(err, ErrReset) {
		t.Fatalf("visible per-delivery gap error=%v, want generic reset", err)
	}
}

func TestStreamUnauthorizedOnlyCheckpointsButVisibleFilterExitRefetchesWithoutIdentity(t *testing.T) {
	database := openAgentModeTestDB(t)
	visibleProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Visible filter','VFL','active')`)
	visibleProjectID, _ := visibleProject.LastInsertId()
	hiddenProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Hidden filter','HFL','active')`)
	hiddenProjectID, _ := hiddenProject.LastInsertId()
	visibleIssue := insertStreamIssue(t, database, visibleProjectID, 1, "keep visible")
	hiddenIssue := insertStreamIssue(t, database, hiddenProjectID, 1, "never visible")
	userID := insertAgentModeUser(t, database, "filter-member", "member", "member")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer'),(?,?,'none')`,
		visibleProjectID, userID, hiddenProjectID, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	actor := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	for _, issueID := range []int64{visibleIssue, hiddenIssue} {
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID, Actor: actor,
			Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation",
			IdempotencyKey: fmt.Sprintf("filter-attempt-%d", issueID)}); err != nil {
			t.Fatal(err)
		}
	}
	request := Request{UserID: userID, RouteProjectID: &visibleProjectID,
		Filters: Filters{Attention: "all", Health: "all", Query: "keep"}}
	session, err := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })}).Open(
		context.Background(), request, "")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := database.Exec(`UPDATE issues SET title='hidden changed' WHERE id=?`, hiddenIssue); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := session.Drain(context.Background())
	if err != nil || checkpoint.Kind != "checkpoint" || len(checkpoint.Hints) != 0 || len(checkpoint.Cursor) != CursorEncodedLength {
		t.Fatalf("unauthorized-only batch=%+v err=%v", checkpoint, err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='left the filter' WHERE id=?`, visibleIssue); err != nil {
		t.Fatal(err)
	}
	refetch, err := session.Drain(context.Background())
	if err != nil || refetch.Kind != "refetch" || len(refetch.Hints) != 0 || len(refetch.Cursor) != CursorEncodedLength {
		t.Fatalf("visible filter-exit batch=%+v err=%v", refetch, err)
	}
}

func TestStreamRetentionEmptyTailAndAheadOfTailCursor(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Retention','RET','active')`)
	projectID, _ := project.LastInsertId()
	issueID := insertStreamIssue(t, database, projectID, 1, "Retention root")
	userID := insertAgentModeUser(t, database, "retention-admin", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
		Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, Policies: delivery.DefaultPolicy(),
		ReasonCode: "instrumentation", IdempotencyKey: "retention-attempt"}); err != nil {
		t.Fatal(err)
	}
	request := Request{UserID: userID, Filters: Filters{Attention: "all", Health: "all"}}
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	state, err := streamer.reader.StreamState(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ahead, err := streamer.cursor.EncodeAt(state.Binding, state.HighWater+1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streamer.Open(context.Background(), request, ahead); !errors.Is(err, ErrReset) {
		t.Fatalf("ahead-of-tail cursor error=%v, want reset", err)
	}

	before, err := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })}).Read(
		context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var tail int64
	if err := database.QueryRow(`SELECT MAX(id) FROM delivery_change_log`).Scan(&tail); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainChangesThrough(context.Background(), tail); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })}).Read(
		context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"no resume": "", "pre-prune tail": before.Cursor, "fresh": fresh.Cursor} {
		session, openErr := streamer.Open(context.Background(), request, token)
		if openErr != nil {
			t.Fatalf("%s open: %v", name, openErr)
		}
		batch, drainErr := session.Drain(context.Background())
		session.Close()
		if drainErr != nil || batch.Kind != "" {
			t.Fatalf("%s empty retained tail batch=%+v err=%v", name, batch, drainErr)
		}
	}
	postPruneState, err := streamer.reader.StreamState(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if postPruneState.HighWater != tail || postPruneState.RetentionFloor != tail {
		t.Fatalf("effective tail=%d floor=%d want %d", postPruneState.HighWater, postPruneState.RetentionFloor, tail)
	}
	preFloor, err := streamer.cursor.EncodeAt(postPruneState.Binding, tail-1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streamer.Open(context.Background(), request, preFloor); !errors.Is(err, ErrReset) {
		t.Fatalf("pre-floor cursor error=%v, want reset", err)
	}
}

func TestSnapshotCursorsUseDistinctProjectRouteAndGlobalFilterEventScopes(t *testing.T) {
	database := openAgentModeTestDB(t)
	project, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Scope','SCP','active')`)
	projectID, _ := project.LastInsertId()
	otherProject, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Other','OTH','active')`)
	otherProjectID, _ := otherProject.LastInsertId()
	issueID := insertStreamIssue(t, database, projectID, 1, "Scoped root")
	otherIssueID := insertStreamIssue(t, database, otherProjectID, 1, "Other root")
	userID := insertAgentModeUser(t, database, "scope-admin", "admin", "admin")
	otherUserID := insertAgentModeUser(t, database, "scope-other", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	for _, candidate := range []int64{issueID, otherIssueID} {
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: candidate,
			Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, Policies: delivery.DefaultPolicy(),
			ReasonCode: "instrumentation", IdempotencyKey: fmt.Sprintf("scope-attempt-%d", candidate)}); err != nil {
			t.Fatal(err)
		}
	}
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	projectSnapshot, err := reader.Read(context.Background(), Request{UserID: userID, RouteProjectID: &projectID,
		Filters: Filters{Attention: "all", Health: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	projectEvents := Request{UserID: userID, Filters: Filters{ProjectID: &projectID, Attention: "all", Health: "all"}}
	projectRouteEvents := Request{UserID: userID, RouteProjectID: &projectID,
		Filters: Filters{Attention: "all", Health: "all"}}
	projectSession, err := streamer.OpenCompatible(context.Background(), []Request{projectEvents, projectRouteEvents}, projectSnapshot.Cursor)
	if err != nil {
		t.Fatalf("project snapshot cursor to canonical events: %v", err)
	}
	if projectSession.request.RouteProjectID == nil || *projectSession.request.RouteProjectID != projectID {
		t.Fatalf("project cursor selected global-filter mode: %+v", projectSession.request)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Project replay' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	if batch, err := projectSession.Drain(context.Background()); err != nil || batch.Kind != "refetch" {
		t.Fatalf("project replay batch=%+v err=%v", batch, err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Other replay' WHERE id=?`, otherIssueID); err != nil {
		t.Fatal(err)
	}
	if batch, err := projectSession.Drain(context.Background()); err != nil || batch.Kind != "checkpoint" || len(batch.Hints) != 0 {
		t.Fatalf("project route observed other project batch=%+v err=%v", batch, err)
	}
	projectSession.Close()

	deliveryKey := fmt.Sprintf("issue:%d", issueID)
	detailSnapshot, err := reader.Read(context.Background(), Request{UserID: userID, DetailDeliveryKey: deliveryKey,
		Filters: Filters{Attention: "all", Health: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	detailSession, err := streamer.Open(context.Background(), Request{UserID: userID,
		Filters: Filters{Attention: "all", Health: "all"}}, detailSnapshot.Cursor)
	if err != nil {
		t.Fatalf("detail selection cursor to canonical events: %v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET title='Detail replay' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	if batch, err := detailSession.Drain(context.Background()); err != nil || batch.Kind != "refetch" {
		t.Fatalf("detail replay batch=%+v err=%v", batch, err)
	}
	detailSession.Close()
	for name, wrong := range map[string]Request{
		"user":    {UserID: otherUserID, Filters: Filters{Attention: "all", Health: "all"}},
		"filter":  {UserID: userID, Filters: Filters{Attention: "required", Health: "all"}},
		"project": {UserID: userID, Filters: Filters{ProjectID: &otherProjectID, Attention: "all", Health: "all"}},
	} {
		if _, err := streamer.Open(context.Background(), wrong, detailSnapshot.Cursor); !errors.Is(err, ErrReset) {
			t.Fatalf("wrong %s binding error=%v, want reset", name, err)
		}
	}
}

func TestGlobalProjectFilterSelectedOutsideKeepsAuthorizedOtherProjectConvergent(t *testing.T) {
	database := openAgentModeTestDB(t)
	projectA, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Filter A','FIA','active')`)
	projectAID, _ := projectA.LastInsertId()
	projectB, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Filter B','FIB','active')`)
	projectBID, _ := projectB.LastInsertId()
	issueA := insertStreamIssue(t, database, projectAID, 1, "A root")
	issueB := insertStreamIssue(t, database, projectBID, 1, "B root")
	userID := insertAgentModeUser(t, database, "filter-scope-admin", "admin", "admin")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	actor := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	for _, issueID := range []int64{issueA, issueB} {
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID, Actor: actor,
			Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation",
			IdempotencyKey: fmt.Sprintf("selected-outside-%d", issueID)}); err != nil {
			t.Fatal(err)
		}
	}
	deliveryB := fmt.Sprintf("issue:%d", issueB)
	request := Request{UserID: userID, Filters: Filters{ProjectID: &projectAID, Attention: "all", Health: "all",
		SelectedDelivery: deliveryB}}
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	snapshot, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SelectedOutside == nil || snapshot.SelectedOutside.Reason != SelectedFilterExcluded ||
		snapshot.SelectedOutside.Row.DeliveryID != deliveryB {
		t.Fatalf("explicit selected outside=%+v", snapshot.SelectedOutside)
	}
	projectRoute := request
	projectRoute.RouteProjectID = &projectAID
	projectRoute.Filters.ProjectID = nil
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	session, err := streamer.OpenCompatible(context.Background(), []Request{request, projectRoute}, snapshot.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.request.RouteProjectID != nil {
		t.Fatalf("global-filter cursor selected project-route mode: %+v", session.request)
	}
	if _, err := database.Exec(`UPDATE issues SET title='B changed outside filter' WHERE id=?`, issueB); err != nil {
		t.Fatal(err)
	}
	batch, err := session.Drain(context.Background())
	if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 0 {
		t.Fatalf("selected-outside B batch=%+v err=%v", batch, err)
	}
}

func TestGlobalProjectFilterActiveFallbackKeepsAuthorizedOtherProjectConvergent(t *testing.T) {
	database := openAgentModeTestDB(t)
	projectA, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Empty A','EMA','active')`)
	projectAID, _ := projectA.LastInsertId()
	projectB, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Active B','ACB','active')`)
	projectBID, _ := projectB.LastInsertId()
	issueB := insertStreamIssue(t, database, projectBID, 1, "Only B active")
	userID := insertAgentModeUser(t, database, "fallback-scope-member", "member", "member")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer'),(?,?,'viewer')`,
		projectAID, userID, projectBID, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueB,
		Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, Policies: delivery.DefaultPolicy(),
		ReasonCode: "instrumentation", IdempotencyKey: "fallback-B"}); err != nil {
		t.Fatal(err)
	}
	request := Request{UserID: userID, Filters: Filters{ProjectID: &projectAID, Attention: "all", Health: "all"}}
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	snapshot, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantB := fmt.Sprintf("issue:%d", issueB)
	if len(snapshot.Rows) != 0 || snapshot.SelectedOutside == nil ||
		snapshot.SelectedOutside.Reason != SelectedActiveFallback || snapshot.SelectedOutside.Row.DeliveryID != wantB {
		t.Fatalf("active fallback=%+v rows=%+v", snapshot.SelectedOutside, snapshot.Rows)
	}
	projectRoute := request
	projectRoute.RouteProjectID = &projectAID
	projectRoute.Filters.ProjectID = nil
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	session, err := streamer.OpenCompatible(context.Background(), []Request{request, projectRoute}, snapshot.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := database.Exec(`UPDATE issues SET title='B fallback changed' WHERE id=?`, issueB); err != nil {
		t.Fatal(err)
	}
	batch, err := session.Drain(context.Background())
	if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 0 {
		t.Fatalf("fallback B batch=%+v err=%v", batch, err)
	}
}

func TestLegacyV0StreamMoveTerminalAndReplacementConvergeWithoutIdentityLeaks(t *testing.T) {
	database := openAgentModeTestDB(t)
	projectA, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Legacy A','LVA','active')`)
	projectAID, _ := projectA.LastInsertId()
	projectB, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Legacy B','LVB','active')`)
	projectBID, _ := projectB.LastInsertId()
	issueID := insertStreamIssue(t, database, projectAID, 1, "Legacy moving root")
	sourceUser := insertAgentModeUser(t, database, "legacy-source", "member", "member")
	targetUser := insertAgentModeUser(t, database, "legacy-target", "member", "member")
	dualUser := insertAgentModeUser(t, database, "legacy-dual", "member", "member")
	neitherUser := insertAgentModeUser(t, database, "legacy-neither", "member", "member")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES
		(?,?,'viewer'),(?,?,'none'),(?,?,'none'),(?,?,'viewer'),
		(?,?,'viewer'),(?,?,'viewer'),(?,?,'none'),(?,?,'none')`,
		projectAID, sourceUser, projectBID, sourceUser,
		projectAID, targetUser, projectBID, targetUser,
		projectAID, dualUser, projectBID, dualUser,
		projectAID, neitherUser, projectBID, neitherUser); err != nil {
		t.Fatal(err)
	}
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,'running',0)`, issueID, projectAID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	filters := Filters{Attention: "all", Health: "all"}
	open := func(t *testing.T, request Request) *StreamSession {
		t.Helper()
		session, openErr := streamer.Open(context.Background(), request, "")
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(session.Close)
		return session
	}
	source := open(t, Request{UserID: sourceUser, RouteProjectID: &projectAID, Filters: filters})
	target := open(t, Request{UserID: targetUser, RouteProjectID: &projectBID, Filters: filters})
	sourceGlobal := open(t, Request{UserID: sourceUser, Filters: filters})
	targetGlobal := open(t, Request{UserID: targetUser, Filters: filters})
	dual := open(t, Request{UserID: dualUser, Filters: filters})
	neither := open(t, Request{UserID: neitherUser, Filters: filters})

	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectBID, issueID); err != nil {
		t.Fatal(err)
	}
	for name, session := range map[string]*StreamSession{"source-route": source, "source-global": sourceGlobal} {
		if batch, err := session.Drain(context.Background()); !errors.Is(err, ErrReset) || batch.Kind != "" || len(batch.Hints) != 0 {
			t.Fatalf("%s move batch=%+v err=%v, want generic reset", name, batch, err)
		}
	}
	wantKey := fmt.Sprintf("issue:%d", issueID)
	for name, session := range map[string]*StreamSession{"target-route": target, "target-global": targetGlobal, "dual-global": dual} {
		batch, err := session.Drain(context.Background())
		if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 1 ||
			batch.Hints[0].DeliveryID != wantKey {
			t.Fatalf("%s move batch=%+v err=%v", name, batch, err)
		}
	}
	if batch, err := neither.Drain(context.Background()); err != nil || batch.Kind != "checkpoint" || len(batch.Hints) != 0 {
		t.Fatalf("neither move batch=%+v err=%v", batch, err)
	}

	targetTerminal := open(t, Request{UserID: targetUser, RouteProjectID: &projectBID, Filters: filters})
	sourceTerminal := open(t, Request{UserID: sourceUser, Filters: filters})
	targetGlobalTerminal := open(t, Request{UserID: targetUser, Filters: filters})
	dualTerminal := open(t, Request{UserID: dualUser, Filters: filters})
	neitherTerminal := open(t, Request{UserID: neitherUser, Filters: filters})
	if _, err := database.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	for name, session := range map[string]*StreamSession{"target-route": targetTerminal, "target-global": targetGlobalTerminal, "dual-global": dualTerminal} {
		if batch, err := session.Drain(context.Background()); !errors.Is(err, ErrReset) || batch.Kind != "" || len(batch.Hints) != 0 {
			t.Fatalf("%s last-terminal batch=%+v err=%v", name, batch, err)
		}
	}
	for name, session := range map[string]*StreamSession{"source-global": sourceTerminal, "neither-global": neitherTerminal} {
		if batch, err := session.Drain(context.Background()); err != nil || batch.Kind != "checkpoint" || len(batch.Hints) != 0 {
			t.Fatalf("%s last-terminal batch=%+v err=%v", name, batch, err)
		}
	}

	sourceRestore := open(t, Request{UserID: sourceUser, Filters: filters})
	targetRestore := open(t, Request{UserID: targetUser, Filters: filters})
	dualRestore := open(t, Request{UserID: dualUser, Filters: filters})
	neitherRestore := open(t, Request{UserID: neitherUser, Filters: filters})
	if _, err := database.Exec(`UPDATE agent_runs SET status='running' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	for name, session := range map[string]*StreamSession{"target-global": targetRestore, "dual-global": dualRestore} {
		batch, err := session.Drain(context.Background())
		if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 1 || batch.Hints[0].DeliveryID != wantKey {
			t.Fatalf("%s restore batch=%+v err=%v", name, batch, err)
		}
	}
	for name, session := range map[string]*StreamSession{"source-global": sourceRestore, "neither-global": neitherRestore} {
		if batch, err := session.Drain(context.Background()); err != nil || batch.Kind != "checkpoint" || len(batch.Hints) != 0 {
			t.Fatalf("%s restore batch=%+v err=%v", name, batch, err)
		}
	}
	replacement := open(t, Request{UserID: targetUser, RouteProjectID: &projectBID, Filters: filters})
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE agent_runs SET status='completed' WHERE id=?`, runID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,delivery_instrumentation_version)
		VALUES(?,?,'running',0)`, issueID, projectBID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	batch, err := replacement.Drain(context.Background())
	if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 1 || batch.Hints[0].DeliveryID != wantKey {
		t.Fatalf("replacement-active batch=%+v err=%v", batch, err)
	}
}

func TestCanonicalStreamMoveHideAndRestoreAudienceMatrix(t *testing.T) {
	database := openAgentModeTestDB(t)
	projectA, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Canonical A','CNA','active')`)
	projectAID, _ := projectA.LastInsertId()
	projectB, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Canonical B','CNB','active')`)
	projectBID, _ := projectB.LastInsertId()
	issueID := insertStreamIssue(t, database, projectAID, 1, "Canonical moving root")
	adminID := insertAgentModeUser(t, database, "canonical-writer", "admin", "admin")
	sourceUser := insertAgentModeUser(t, database, "canonical-source", "member", "member")
	targetUser := insertAgentModeUser(t, database, "canonical-target", "member", "member")
	dualUser := insertAgentModeUser(t, database, "canonical-dual", "member", "member")
	neitherUser := insertAgentModeUser(t, database, "canonical-neither", "member", "member")
	if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES
		(?,?,'viewer'),(?,?,'none'),(?,?,'none'),(?,?,'viewer'),
		(?,?,'viewer'),(?,?,'viewer'),(?,?,'none'),(?,?,'none')`,
		projectAID, sourceUser, projectBID, sourceUser,
		projectAID, targetUser, projectBID, targetUser,
		projectAID, dualUser, projectBID, dualUser,
		projectAID, neitherUser, projectBID, neitherUser); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
		Actor:    delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)},
		Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: "canonical-audience"}); err != nil {
		t.Fatal(err)
	}
	streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
	filters := Filters{Attention: "all", Health: "all"}
	openAll := func(t *testing.T) map[string]*StreamSession {
		t.Helper()
		out := map[string]*StreamSession{}
		for name, userID := range map[string]int64{
			"source": sourceUser, "target": targetUser, "dual": dualUser, "neither": neitherUser,
		} {
			session, err := streamer.Open(context.Background(), Request{UserID: userID, Filters: filters}, "")
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			out[name] = session
			t.Cleanup(session.Close)
		}
		return out
	}
	wantKey := fmt.Sprintf("issue:%d", issueID)
	assertCheckpoint := func(name string, session *StreamSession) {
		t.Helper()
		batch, err := session.Drain(context.Background())
		if err != nil || batch.Kind != "checkpoint" || len(batch.Hints) != 0 {
			t.Fatalf("%s batch=%+v err=%v, want checkpoint", name, batch, err)
		}
	}
	assertRefetch := func(name string, session *StreamSession) {
		t.Helper()
		batch, err := session.Drain(context.Background())
		if err != nil || batch.Kind != "refetch" || len(batch.Hints) != 1 || batch.Hints[0].DeliveryID != wantKey {
			t.Fatalf("%s batch=%+v err=%v, want refetch %s", name, batch, err, wantKey)
		}
	}
	assertReset := func(name string, session *StreamSession) {
		t.Helper()
		batch, err := session.Drain(context.Background())
		if !errors.Is(err, ErrReset) || batch.Kind != "" || len(batch.Hints) != 0 {
			t.Fatalf("%s batch=%+v err=%v, want identity-free reset", name, batch, err)
		}
	}

	move := openAll(t)
	if _, err := database.Exec(`UPDATE issues SET project_id=? WHERE id=?`, projectBID, issueID); err != nil {
		t.Fatal(err)
	}
	assertReset("source move-away", move["source"])
	assertRefetch("target move-in", move["target"])
	assertRefetch("dual move", move["dual"])
	assertCheckpoint("neither move", move["neither"])

	hide := openAll(t)
	if _, err := database.Exec(`UPDATE issues SET deleted_at=? WHERE id=?`, now.Format(time.RFC3339Nano), issueID); err != nil {
		t.Fatal(err)
	}
	assertCheckpoint("source hidden", hide["source"])
	assertReset("target removal", hide["target"])
	assertReset("dual removal", hide["dual"])
	assertCheckpoint("neither removal", hide["neither"])

	restore := openAll(t)
	if _, err := database.Exec(`UPDATE issues SET deleted_at=NULL WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	assertCheckpoint("source restore", restore["source"])
	assertRefetch("target restore", restore["target"])
	assertRefetch("dual restore", restore["dual"])
	assertCheckpoint("neither restore", restore["neither"])
}

func TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges(t *testing.T) {
	t.Run("subscribe before high-water", func(t *testing.T) {
		database := openAgentModeTestDB(t)
		project, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Subscribe race','SUB','active')`)
		projectID, _ := project.LastInsertId()
		issueID := insertStreamIssue(t, database, projectID, 1, "Before subscribe")
		userID := insertAgentModeUser(t, database, "subscribe-admin", "admin", "admin")
		now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
			Actor:    delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
			Policies: delivery.DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: "subscribe-race"}); err != nil {
			t.Fatal(err)
		}
		hub := NewWakeHub()
		streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now }), Hub: hub})
		streamer.afterSubscribe = func() {
			if _, err := database.Exec(`UPDATE issues SET title='Committed after subscribe' WHERE id=?`, issueID); err != nil {
				t.Fatal(err)
			}
			hub.Notify(context.Background(), delivery.ChangeHint{})
		}
		session, err := streamer.Open(context.Background(), Request{UserID: userID,
			Filters: Filters{Attention: "all", Health: "all"}}, "")
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		var tail int64
		if err := database.QueryRow(`SELECT MAX(id) FROM delivery_change_log`).Scan(&tail); err != nil {
			t.Fatal(err)
		}
		if session.highWater != tail {
			t.Fatalf("session high-water=%d tail=%d", session.highWater, tail)
		}
		select {
		case <-session.Wake():
		default:
			t.Fatal("commit after subscribe did not reach the subscribed session")
		}
		if batch, err := session.Drain(context.Background()); err != nil || batch.Kind != "" {
			t.Fatalf("already captured commit batch=%+v err=%v", batch, err)
		}
	})

	t.Run("overflow lost wake coalescing and restart", func(t *testing.T) {
		database := openAgentModeTestDB(t)
		project, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Durable replay','DRP','active')`)
		projectID, _ := project.LastInsertId()
		issueID := insertStreamIssue(t, database, projectID, 1, "Durable root")
		userID := insertAgentModeUser(t, database, "durable-admin", "admin", "admin")
		now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
			Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, Policies: delivery.DefaultPolicy(),
			ReasonCode: "instrumentation", IdempotencyKey: "durable-replay"}); err != nil {
			t.Fatal(err)
		}
		request := Request{UserID: userID, Filters: Filters{Attention: "all", Health: "all"}}
		reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
		baseline, err := reader.Read(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		lostHub := NewWakeHub()
		lost, err := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now }), Hub: lostHub}).Open(
			context.Background(), request, baseline.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		defer lost.Close()
		if _, err := database.Exec(`UPDATE issues SET title='No wake delivered' WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		select {
		case <-lost.Wake():
			t.Fatal("unexpected wake on isolated hub")
		default:
		}
		if batch, err := lost.Drain(context.Background()); err != nil || batch.Kind != "refetch" {
			t.Fatalf("poll after lost wake batch=%+v err=%v", batch, err)
		}

		coalescedHub := NewWakeHub()
		coalesced, err := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now }), Hub: coalescedHub}).Open(
			context.Background(), request, "")
		if err != nil {
			t.Fatal(err)
		}
		defer coalesced.Close()
		for index := 0; index < 2; index++ {
			if _, err := database.Exec(`UPDATE issues SET title=? WHERE id=?`, fmt.Sprintf("Coalesced %d", index), issueID); err != nil {
				t.Fatal(err)
			}
			coalescedHub.Notify(context.Background(), delivery.ChangeHint{})
		}
		if len(coalesced.wake) != 1 {
			t.Fatalf("coalesced wake depth=%d, want 1", len(coalesced.wake))
		}
		if batch, err := coalesced.Drain(context.Background()); err != nil || batch.Kind != "refetch" {
			t.Fatalf("coalesced batch=%+v err=%v", batch, err)
		}

		restartBase, err := reader.Read(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE issues SET title='Across streamer restart' WHERE id=?`, issueID); err != nil {
			t.Fatal(err)
		}
		restarted, err := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now }),
			Hub: NewWakeHub()}).Open(context.Background(), request, restartBase.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		if batch, err := restarted.Drain(context.Background()); err != nil || batch.Kind != "refetch" {
			t.Fatalf("restart replay batch=%+v err=%v", batch, err)
		}

		overflow, err := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })}).Open(
			context.Background(), request, "")
		if err != nil {
			t.Fatal(err)
		}
		defer overflow.Close()
		for index := 0; index <= MaxReplayBatch; index++ {
			if _, err := database.Exec(`UPDATE issues SET title=? WHERE id=?`, fmt.Sprintf("Overflow %d", index), issueID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := overflow.Drain(context.Background()); !errors.Is(err, ErrReset) {
			t.Fatalf("overflow drain error=%v, want reset", err)
		}
	})

	t.Run("permission grant and revoke", func(t *testing.T) {
		database := openAgentModeTestDB(t)
		project, _ := database.Exec(`INSERT INTO projects(name,key,status) VALUES('Permission stream','PER','active')`)
		projectID, _ := project.LastInsertId()
		issueID := insertStreamIssue(t, database, projectID, 1, "Permission root")
		adminID := insertAgentModeUser(t, database, "permission-writer", "admin", "admin")
		userID := insertAgentModeUser(t, database, "permission-member", "member", "member")
		if _, err := database.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'none')`,
			projectID, userID); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
		if _, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
			Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", adminID)}, Policies: delivery.DefaultPolicy(),
			ReasonCode: "instrumentation", IdempotencyKey: "permission-stream"}); err != nil {
			t.Fatal(err)
		}
		request := Request{UserID: userID, Filters: Filters{Attention: "all", Health: "all"}}
		streamer := NewStreamer(database, StreamerOptions{Clock: delivery.ClockFunc(func() time.Time { return now })})
		beforeGrant, err := streamer.Open(context.Background(), request, "")
		if err != nil {
			t.Fatal(err)
		}
		defer beforeGrant.Close()
		if _, err := database.Exec(`UPDATE project_members SET access_level='viewer' WHERE project_id=? AND user_id=?`,
			projectID, userID); err != nil {
			t.Fatal(err)
		}
		if _, err := beforeGrant.Drain(context.Background()); !errors.Is(err, ErrReset) {
			t.Fatalf("grant did not reset established binding: %v", err)
		}
		afterGrant, err := streamer.Open(context.Background(), request, "")
		if err != nil {
			t.Fatal(err)
		}
		defer afterGrant.Close()
		if _, err := database.Exec(`UPDATE project_members SET access_level='none' WHERE project_id=? AND user_id=?`,
			projectID, userID); err != nil {
			t.Fatal(err)
		}
		if _, err := afterGrant.Drain(context.Background()); !errors.Is(err, ErrReset) {
			t.Fatalf("revoke did not reset established binding: %v", err)
		}
	})
}

func insertStreamIssue(t *testing.T, database interface {
	Exec(string, ...any) (sql.Result, error)
}, projectID int64, number int, title string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, title)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
