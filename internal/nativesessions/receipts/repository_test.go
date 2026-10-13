// SPDX-License-Identifier: AGPL-3.0-only

package receipts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type testAuthority struct {
	intent     nativereceipts.Intent
	final      FinalAuthority
	owner      string
	generation string
	revoked    bool
}

func (a *testAuthority) ReceiptTx(_ context.Context, _ pgx.Tx, daemon nativecontract.DaemonActor, r nativecontract.Receipt) (ReceiptAuthority, error) {
	if a.revoked {
		return ReceiptAuthority{}, nativereceipts.Fail(nativecontract.ReasonPermissionRevoked)
	}
	if daemon.DaemonGeneration != a.generation {
		return ReceiptAuthority{}, nativereceipts.Fail(nativecontract.ReasonWrongGeneration)
	}
	return ReceiptAuthority{Intent: a.intent, OwnerPersonID: a.owner}, nil
}
func (a *testAuthority) ReadTx(_ context.Context, _ pgx.Tx, actor nativecontract.Actor, b nativecontract.Binding) error {
	if actor.PrincipalID != a.owner || !b.Equal(a.final.CurrentBinding) {
		return nativereceipts.Fail(nativecontract.ReasonWrongPrincipal)
	}
	return nil
}
func (a *testAuthority) FinalTx(_ context.Context, _ pgx.Tx, daemon nativecontract.DaemonActor, in nativecontract.FinalPublish) (FinalAuthority, error) {
	if a.revoked {
		return FinalAuthority{}, nativereceipts.Fail(nativecontract.ReasonPermissionRevoked)
	}
	if daemon.DaemonGeneration != a.generation {
		return FinalAuthority{}, nativereceipts.Fail(nativecontract.ReasonWrongGeneration)
	}
	return a.final, nil
}

type fixture struct {
	d            *dbtest.DB
	chat         *chat.Module
	repo         *Repository
	authority    *testAuthority
	owner, agent tenant.Principal
	actor        nativecontract.DaemonActor
	binding      nativecontract.Binding
	final        nativecontract.FinalPublish
	now          time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	tid, project := uuid.NewString(), uuid.NewString()
	_, err := f.d.Admin.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'native receipts fixture')`, tid, "native-"+tid)
	if err != nil {
		t.Fatal(err)
	}
	f.owner = tenant.Principal{TenantID: tid, ID: uuid.NewString(), Kind: tenant.Person}
	f.agent = tenant.Principal{TenantID: tid, ID: uuid.NewString(), Kind: tenant.Agent, Scopes: []string{"harness.worker", "harness.read", "chat.receive", "chat.send"}}
	for _, p := range []tenant.Principal{f.owner, f.agent} {
		if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,'native fixture')`, tid, p.ID, p.Kind); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, f.d, tid, p.ID, "member")
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'NATIVE-1',id,'Native project' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, tid, project); err != nil {
		t.Fatal(err)
	}
	f.chat = chat.New(f.d.App, chat.Options{Enabled: true})
	mux := http.NewServeMux()
	f.chat.Mount(mux)
	call := func(path string, body any, out any) {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), f.owner))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("fixture %s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var role chat.Role
	call("/api/projects/"+project+"/chat-roles", chat.RoleCreate{Kind: "lead", SlotKey: "lead"}, &role)
	var thread chat.Thread
	call("/api/projects/"+project+"/chat-threads/resolve", chat.ThreadResolve{RoleID: role.ID}, &thread)
	session := uuid.NewString()
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,capabilities,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'claude','fixture','managed','coordinator',ARRAY['chat'],$6,$7)`, tid, session, project, f.agent.ID, f.owner.ID, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)); err != nil {
		t.Fatal(err)
	}
	call("/api/chat-threads/"+thread.ID+"/binding", chat.BindingWrite{SessionID: session, ExpectedEpoch: "0"}, &thread)
	f.binding = nativecontract.Binding{TenantID: tid, ProjectID: project, OwnedSessionID: uuid.NewString(), HarnessSessionID: session, RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "original-generation", BindingRevision: 1}
	f.actor = nativecontract.DaemonActor{Actor: nativecontract.Actor{TenantID: tid, PrincipalID: f.agent.ID, PrincipalKind: "agent", OwnerPersonID: f.owner.ID, KeyCeiling: f.agent.Scopes}, ComputerID: uuid.NewString(), DaemonID: f.binding.DaemonID, DaemonGeneration: f.binding.DaemonGeneration, LedgerGeneration: uuid.NewString(), CapabilityDigest: strings.Repeat("a", 64)}
	inputID := uuid.NewString()
	grant := nativecontract.FinalDrainGrant{GrantID: uuid.NewString(), Binding: f.binding, ConversationRef: "opaque-conversation", ExpiresAt: f.now.Add(24 * time.Hour), MaximumFinals: 64, MaximumBytes: 4 << 20}
	f.final = nativecontract.FinalPublish{RequestID: uuid.NewString(), ExpectedBinding: f.binding, FinalID: uuid.NewString(), InputID: inputID, TurnRef: "opaque-turn", Text: "explicit final canary", DrainGrantID: grant.GrantID}
	f.authority = &testAuthority{owner: f.owner.ID, generation: f.actor.DaemonGeneration, intent: nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: uuid.NewString(), OperationID: uuid.NewString(), Binding: &f.binding, Operation: "input", State: "accepted", InputID: &inputID, SourceSequence: "0", UpdatedAt: f.now}, ActorID: f.owner.ID, PayloadSHA256: strings.Repeat("b", 64), ExpiresAt: f.now.Add(time.Hour)}, final: FinalAuthority{Grant: grant, CurrentBinding: f.binding, Sender: f.agent, Target: chat.NativeFinalTarget{OwnerPersonID: f.owner.ID, ConversationID: thread.ID, RoleID: role.ID, SessionID: session}, ConversationRef: grant.ConversationRef, IssuedAt: f.now, RetentionApproved: true}}
	f.repo, err = New(f.d.App, f.authority, f.chat, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) ctx(t *testing.T) context.Context {
	return tenant.WithPrincipal(t.Context(), f.agent)
}
func reason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var e *nativereceipts.Error
	if !errors.As(err, &e) || e.Reason != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}

func (f *fixture) complete(t *testing.T) {
	t.Helper()
	r := f.authority.intent.Receipt
	for i, state := range []string{"accepted", "write_started", "write_acknowledged", "turn_started", "turn_completed"} {
		r.State = state
		if i >= 2 {
			r.SourceSequence = fmt.Sprint(i - 1)
		}
		if i >= 3 {
			turn := f.final.TurnRef
			r.TurnRef = &turn
		}
		out, err := f.repo.Report(f.ctx(t), f.actor, r)
		if err != nil || out.Receipt.State != state {
			t.Fatalf("report %s: %+v %v", state, out, err)
		}
	}
}

// Risk: after native exit/API acknowledgement loss, a final is duplicated or
// gains old-generation input authority. Replayed IDs persist once through real
// chat-isolated storage and an exact final-drain claim of a restarted daemon.
func TestFinalDrainReplayAfterExitPersistsOnceAndRechecksAuthority(t *testing.T) {
	f := newFixture(t)
	f.complete(t)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),archived_at=clock_timestamp() WHERE id=$1`, f.binding.HarnessSessionID); err != nil {
		t.Fatal(err)
	}
	f.actor.DaemonGeneration = "restarted-generation"
	f.authority.generation = f.actor.DaemonGeneration
	first, err := f.repo.Persist(f.ctx(t), f.actor, f.final)
	if err != nil || first.State != "persisted" || first.MessageID == nil {
		t.Fatalf("drain %+v %v", first, err)
	}
	// Treat the first response as lost; an exact replay has one durable message.
	second, err := f.repo.Persist(f.ctx(t), f.actor, f.final)
	if err != nil || second.MessageID == nil || *second.MessageID != *first.MessageID {
		t.Fatalf("replay %+v %v", second, err)
	}
	var messages, projections, privateEvents int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM inbox_messages WHERE tenant_id=$1 AND chat_thread_id=$2),(SELECT count(*) FROM native_session_finals WHERE tenant_id=$1),(SELECT count(*) FROM events WHERE tenant_id=$1 AND type='chat.live_message')`, f.agent.TenantID, f.authority.final.Target.ConversationID).Scan(&messages, &projections, &privateEvents); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || projections != 1 || privateEvents != 1 {
		t.Fatalf("duplicate final: %d %d %d", messages, projections, privateEvents)
	}
	for _, mutate := range []func(*nativecontract.FinalPublish){func(f *nativecontract.FinalPublish) { f.Text = "changed answer" }, func(f *nativecontract.FinalPublish) { f.TurnRef = "different-turn" }} {
		changed := f.final
		mutate(&changed)
		_, err := f.repo.Persist(f.ctx(t), f.actor, changed)
		if changed.TurnRef != f.final.TurnRef {
			reason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
		} else {
			reason(t, err, nativecontract.ReasonIdentityMismatch)
		}
	}
	// A drain-only restarted daemon cannot publish an old-generation control
	// receipt by borrowing the special final path's authority.
	_, err = f.repo.Report(f.ctx(t), f.actor, f.authority.intent.Receipt)
	reason(t, err, nativecontract.ReasonWrongGeneration)
	f.authority.revoked = true
	rejected, err := f.repo.Persist(f.ctx(t), f.actor, f.final)
	if err != nil || rejected.State != "rejected" || rejected.Reason == nil || *rejected.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatalf("revoked retry %+v %v", rejected, err)
	}
	f.authority.revoked = false
	f.now = f.authority.final.Grant.ExpiresAt
	expired, err := f.repo.Persist(f.ctx(t), f.actor, f.final)
	if err != nil || expired.State != "expired" || expired.Reason == nil || *expired.Reason != nativecontract.ReasonPublicationGrantExpired {
		t.Fatalf("expired retry %+v %v", expired, err)
	}
}

// Risk: a final authorizer read made before the actual mutation can survive
// access revocation. Hold the canonical tenant fence, observe the real waiting
// final transaction, revoke the sender scope, then release the barrier.
func TestFinalDrainRevocationWinsBeforeFinalWrite(t *testing.T) {
	f := newFixture(t)
	f.complete(t)
	ctx, cancel := context.WithTimeout(f.ctx(t), 15*time.Second)
	defer cancel()
	holder, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err = holder.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true)`, f.agent.TenantID); err != nil {
		t.Fatal(err)
	}
	if err = db.LockTree(ctx, holder, f.agent.TenantID); err != nil {
		t.Fatal(err)
	}
	pid := holder.Conn().PgConn().PID()
	ready := make(chan struct{})
	result := make(chan struct {
		r   nativecontract.NativeFinalReceipt
		err error
	}, 1)
	go func() {
		defer close(ready)
		r, err := f.repo.Persist(ctx, f.actor, f.final)
		result <- struct {
			r   nativecontract.NativeFinalReceipt
			err error
		}{r, err}
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, ready); lock == "" {
		t.Fatal("final write never overlapped the access fence")
	}
	// Revoke the CURRENT sender role, rather than changing the fake authorizer.
	if _, err = holder.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.agent.TenantID, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	out := dbtest.Await(t, ctx, result)
	if out.err != nil || out.r.State != "rejected" || out.r.Reason == nil || *out.r.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatalf("revoked final: %+v %v", out.r, out.err)
	}
	var count int
	if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM native_session_finals WHERE tenant_id=$1`, f.agent.TenantID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("revoked final projection committed")
	}
}

func TestReceiptProjectionRejectsQueueAsDeliveryAndScopesPages(t *testing.T) {
	f := newFixture(t)
	r := f.authority.intent.Receipt
	if _, err := f.repo.Report(f.ctx(t), f.actor, r); err != nil {
		t.Fatal(err)
	}
	jump := r
	jump.State = "turn_completed"
	jump.SourceSequence = "1"
	turn := f.final.TurnRef
	jump.TurnRef = &turn
	_, err := f.repo.Report(f.ctx(t), f.actor, jump)
	reason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	for i := 0; i < 2; i++ {
		if _, err := f.repo.Report(f.ctx(t), f.actor, r); err != nil {
			t.Fatal(err)
		}
	}
	actor := nativecontract.Actor{TenantID: f.owner.TenantID, PrincipalID: f.owner.ID, PrincipalKind: "person", OwnerPersonID: f.owner.ID}
	page, err := f.repo.Receipts(tenant.WithPrincipal(t.Context(), f.owner), actor, f.binding, nativecontract.PageQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "accepted" || page.Truncated {
		t.Fatalf("receipt page %+v %v", page, err)
	}
	var attempts, events int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT attempts FROM native_session_receipts WHERE tenant_id=$1),(SELECT count(*) FROM events WHERE tenant_id=$1 AND type='native_session.receipt_changed')`, f.agent.TenantID).Scan(&attempts, &events); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || events != 1 {
		t.Fatalf("receipt retry duplicated state/event: %d %d", attempts, events)
	}
	foreign := f.binding
	foreign.TenantID = uuid.NewString()
	_, err = f.repo.Receipts(tenant.WithPrincipal(t.Context(), f.owner), actor, foreign, nativecontract.PageQuery{Limit: 1})
	reason(t, err, nativecontract.ReasonWrongInstance)
	// The raw receipt projection is tenant-RLS protected even without a handler.
	other := uuid.NewString()
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'other')`, other, "other-"+other); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(db.AllProjects(t.Context(), "fixture RLS"), f.d.App, other, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM native_session_receipts WHERE tenant_id=$1`, f.agent.TenantID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("cross-tenant receipt visible")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
