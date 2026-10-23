// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: an attached hook cannot subscribe through paired auth, or its stream
// keeps delivering after its person's ceiling is revoked between transactions.
func TestPairedAttachedHookNotificationsCeiling(t *testing.T) {
	v := attachedRecoveryAgent(t)
	f := v.f
	beat := map[string]any{"phase": "working", "activity_sequence": 2, "attached_hook": true, "process_ownership": v.identity}
	expect(t, v.runtimeCall(t, v.path+"/heartbeat", beat, v.lease), 200)
	server := httptest.NewServer(v.runtimeHandler(t, f.mux))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", server.URL+v.path+"/notifications", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("X-Aeon-Worker-Lease", v.lease)
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Aeon-Upgrade-Reason") != "" {
		t.Fatalf("paired attached subscription status=%d upgrade=%q", resp.StatusCode, resp.Header.Get("Aeon-Upgrade-Reason"))
	}
	scanner := bufio.NewScanner(resp.Body)
	next := func() bool {
		t.Helper()
		for scanner.Scan() {
			if scanner.Text() == "event: harness.wake" {
				if !scanner.Scan() || scanner.Text() != "data: {}" {
					t.Fatal("attached wake leaked content or changed shape")
				}
				return true
			}
		}
		return false
	}
	if !next() {
		t.Fatal("missing attached subscription barrier")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &v.session, Body: "private attached notification fixture", IdempotencyKey: uid()})
		return err
	})
	if !next() {
		t.Fatal("exact attached message did not wake its stream")
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, v.keyOwner, "viewer")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &f.project, Type: "harness.control_requested", After: map[string]any{"session_id": v.session}})
		return err
	})
	if next() {
		t.Fatal("attached stream survived its person ceiling revocation")
	}
	if scanner.Err() != nil {
		t.Fatal("attached stream did not close cleanly", scanner.Err())
	}
}

func TestHarnessNotificationsCommitWakeLeaseAndRevocation(t *testing.T) {
	// Risk: controls/messages still wait for polling, or the new channel leaks
	// content or survives an exact-key revocation. Reading the initial SSE frame
	// is the subscription barrier; no sleep or elapsed-time assertion is used.
	f := fixture(t)
	lease := "notifications-fixture-lease-000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "notifications-fixture-reference", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox", "stop"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	expect(t, f.call(f.agent, "GET", path+"/notifications", nil, "wrong-lease-000000000000000000000001"), 403)
	expect(t, f.call(f.person, "GET", path+"/notifications", nil, lease), 403)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.agent)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path+"/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("X-Aeon-Worker-Lease", lease)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("notification status %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	next := func() bool {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if line == "event: harness.wake" {
				if !scanner.Scan() || scanner.Text() != "data: {}" {
					t.Fatal("wake exposed content or lost its fixed shape")
				}
				return true
			}
		}
		return false
	}
	if !next() {
		t.Fatal("missing reconnect wake")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &id, Body: "private notification fixture", IdempotencyKey: uid()})
		return err
	})
	if !next() {
		t.Fatal("committed message did not wake the daemon")
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	// The wake did not acknowledge or consume the queued control.
	expect(t, f.call(f.person, "POST", path+"/controls/stop", map[string]any{}, ""), 201)
	if !next() {
		t.Fatal("committed Stop did not wake the daemon")
	}
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 1 {
		t.Fatal("wake consumed queued Stop")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE id=$1`, f.agent.AuthKeyID); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &f.project, Type: "harness.control_requested", After: map[string]any{"session_id": id}})
		return err
	})
	if next() {
		t.Fatal("revoked bearer received another wake")
	}
	if scanner.Err() != nil {
		t.Fatal("revocation did not close the stream cleanly", scanner.Err())
	}
	expect(t, f.call(f.agent, "GET", path+"/notifications", nil, lease), 403)
}

type notificationRangeTrace struct {
	reads            atomic.Int32
	started, release chan struct{}
}

func (tr *notificationRangeTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=$1 AND e.id>$2") && tr.reads.Add(1) == 1 {
		close(tr.started)
		select {
		case <-tr.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*notificationRangeTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Risk: another session's heartbeat burst fans out into one authority/data
// transaction per event, or coalescing loses a real control or includes an
// event committed with revocation after the batch's authority check. Hold the
// first range read while the fast observer proves the burst reached fan-out.
func TestHarnessNotificationBurstReadsOneDurableRange(t *testing.T) {
	for _, mode := range []string{"coalesce", "revoked_after_authorization"} {
		t.Run(mode, func(t *testing.T) {
			checkHarnessNotificationBurst(t, mode == "revoked_after_authorization")
		})
	}
}

func checkHarnessNotificationBurst(t *testing.T, revokeAfterCheck bool) {
	t.Helper()
	f := fixture(t)
	lease := "notification-burst-lease-00000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "notification-burst-reference", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox", "stop"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	trace := &notificationRangeTrace{started: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(trace.release) }) }
	defer release()
	cfg := f.db.App.Config()
	cfg.ConnConfig.Tracer = trace
	if _, err := db.ConfigurePool(cfg); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer db.CloseNotificationConnections(pool)
	mux := http.NewServeMux()
	harness.New(pool).Mount(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.agent)))
	}))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+base+"/"+id+"/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("X-Aeon-Worker-Lease", lease)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("notification status %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	wake := func() bool {
		t.Helper()
		if !scanner.Scan() {
			return false
		}
		for _, want := range []string{"event: harness.wake", "data: {}", ""} {
			if scanner.Text() != want {
				t.Fatalf("unexpected wake frame %q want %q", scanner.Text(), want)
			}
			if want != "" && !scanner.Scan() {
				t.Fatal("incomplete wake frame")
			}
		}
		return true
	}
	if !wake() {
		t.Fatal("initial wake missing")
	}
	observer, err := db.SubscribeNotifications(ctx, pool, "aeon_events", f.agent.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(context.Background())
	appendHint := func(typ, session string) {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := events.Append(ctx, tx, f.person, events.Change{NodeID: &f.project, Type: typ, After: map[string]any{"session_id": session}})
			return err
		})
		if n, err := observer.WaitForNotification(ctx); err != nil || db.IsResync(n) {
			t.Fatalf("fast observer missed burst hint: %v %v", n, err)
		}
	}
	otherSession := uid()
	appendHint("harness.heartbeat", otherSession)
	select {
	case <-trace.started:
	case <-ctx.Done():
		t.Fatal("first data read did not reach barrier")
	}
	for range 40 {
		appendHint("harness.heartbeat", otherSession)
	}
	revokeAndHint := func() {
		t.Helper()
		f.tx(t, f.person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE id=$1`, f.agent.AuthKeyID); err != nil {
				return err
			}
			_, err := events.Append(ctx, tx, f.person, events.Change{NodeID: &f.project, Type: "harness.control_requested", After: map[string]any{"session_id": id}})
			return err
		})
		if n, err := observer.WaitForNotification(ctx); err != nil || db.IsResync(n) {
			t.Fatalf("fast observer missed revocation commit: %v %v", n, err)
		}
	}
	if revokeAfterCheck {
		// The first key/proof check has finished, but its data statement has
		// not started. The new control and revocation commit together here.
		revokeAndHint()
		release()
		if wake() || scanner.Err() != nil {
			t.Fatalf("post-check revocation event escaped the batch boundary: %v", scanner.Err())
		}
		if reads := trace.reads.Load(); reads != 1 {
			t.Fatalf("revoked stream read another batch: %d", reads)
		}
		return
	}
	appendHint("harness.control_requested", id)
	release()
	if !wake() {
		t.Fatal("coalesced burst lost the committed control wake")
	}
	// The last old hint can be represented by an overflow resync. It may
	// cause one more authorized read/wake, never one per old event.
	revokeAndHint()
	for wake() {
	}
	if scanner.Err() != nil {
		t.Fatal("revoked burst stream did not close cleanly", scanner.Err())
	}
	if reads := trace.reads.Load(); reads < 1 || reads > 2 {
		t.Fatalf("42 queued hints caused %d durable range reads; want at most two including overflow resync", reads)
	}
}

// Risk: a queued after-turn message is drained by the busy loop, or the
// daemon can acknowledge a different delivery level from the one requested.
func TestHarnessDrainSelectsBoundaryInputAndChecksCompletionLevel(t *testing.T) {
	t.Run("direct exact-session inbox is priority", exerciseDirectPriorityInbox)
	f := fixture(t)
	lease := "level-fixture-lease-000000000000000000000975"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "level-fixture-reference", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox", "steer"}}, "")
	expect(t, w, 201)
	session := decode(t, w)["id"].(string)
	for _, level := range []string{"simple", "steer"} {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			accepted, err := inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &session, Body: level, IdempotencyKey: uid()})
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,recipient_address,body,key_digest,request_digest,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level,recipient_session_id) VALUES($1,$2::text::uuid,$3,$4,$5,'paimos:fixture',$6,$2::text,$2::text,$7::uuid,$8,false,false,$6,$9::uuid)`, f.person.TenantID, uid(), f.project, f.person.ID, f.agent.ID, level, accepted.ID, accepted.SentEventID, session)
			return err
		})
	}
	path := base + "/" + session
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{"delivery_level": "steer"}, lease)
	expect(t, w, 200)
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["body"] != "steer" || items[0]["delivery_level"] != "steer" {
		t.Fatalf("busy drain: %s %v", w.Body.String(), err)
	}
	item := items[0]
	completion := map[string]any{"delivery_id": item["delivery_id"], "cursor": item["cursor"], "effective_level": "simple"}
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", completion, lease), 409)
	completion["effective_level"] = "steer"
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", completion, lease), 200)
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{"delivery_level": "steer"}, lease)
	expect(t, w, 200)
	if string(bytes.TrimSpace(w.Body.Bytes())) != "[]" {
		t.Fatal("busy drain took simple input", w.Body.String())
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["body"] != "simple" {
		t.Fatalf("idle drain: %s %v", w.Body.String(), err)
	}
	// The idle drain leased after-turn input and left it open, which is what
	// the supervisor does when the turn goes busy before completion. A steer
	// drain must leave that lease open, replay a matching steer lease, and an
	// empty drain must still return the same simple lease.
	waitingID, _ := items[0]["delivery_id"].(string)
	if waitingID == "" || items[0]["delivery_level"] != "simple" {
		t.Fatalf("idle drain leased no simple delivery: %s", w.Body.String())
	}
	queueBoundaryInput(t, f, session, "steer", "steer while waiting")
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{"delivery_level": "steer"}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["body"] != "steer while waiting" || items[0]["delivery_level"] != "steer" {
		t.Fatalf("steer drain stopped at the open simple lease: %s %v", w.Body.String(), err)
	}
	steerID, _ := items[0]["delivery_id"].(string)
	if steerID == "" || steerID == waitingID {
		t.Fatalf("steer drain did not lease the steer message: %s", w.Body.String())
	}
	queueBoundaryInput(t, f, session, "steer", "steer behind open lease")
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{"delivery_level": "steer"}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["delivery_id"] != steerID || items[0]["body"] != "steer while waiting" {
		t.Fatalf("steer drain did not replay the matching open lease: %s %v", w.Body.String(), err)
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["delivery_id"] != waitingID || items[0]["body"] != "simple" || items[0]["delivery_level"] != "simple" {
		t.Fatalf("idle drain did not return the waiting simple lease: %s %v", w.Body.String(), err)
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{"delivery_level": "steer"}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["delivery_id"] != steerID {
		t.Fatalf("idle drain disturbed the open steer lease: %s %v", w.Body.String(), err)
	}
	completion = map[string]any{"delivery_id": steerID, "cursor": items[0]["cursor"], "effective_level": "steer"}
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", completion, lease), 200)
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{"delivery_level": "steer"}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["body"] != "steer behind open lease" || items[0]["delivery_level"] != "steer" {
		t.Fatalf("steer behind the open simple lease was dropped: %s %v", w.Body.String(), err)
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]string{}, lease)
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0]["delivery_id"] != waitingID || items[0]["body"] != "simple" {
		t.Fatalf("completing steer released the waiting simple lease: %s %v", w.Body.String(), err)
	}
}

func queueBoundaryInput(t *testing.T, f *harnessFixture, session, level, body string) {
	t.Helper()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		accepted, err := inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &session, Body: body, IdempotencyKey: uid()})
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,recipient_address,body,key_digest,request_digest,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level,recipient_session_id) VALUES($1,$2::text::uuid,$3,$4,$5,'paimos:fixture',$6,$2::text,$2::text,$7::uuid,$8,false,false,$10,$9::uuid)`, f.person.TenantID, uid(), f.project, f.person.ID, f.agent.ID, body, accepted.ID, accepted.SentEventID, session, level)
		return err
	})
}
