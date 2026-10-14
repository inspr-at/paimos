// SPDX-License-Identifier: AGPL-3.0-only

package receipts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativeattach"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeintegration"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type testAuthority struct {
	intent     nativereceipts.Intent
	intents    map[string]nativereceipts.Intent
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
	intent := a.intent
	if a.intents != nil {
		var found bool
		intent, found = a.intents[r.OperationID]
		if !found {
			return ReceiptAuthority{}, nativereceipts.Fail(nativecontract.ReasonGrantMissing)
		}
	}
	return ReceiptAuthority{Intent: intent, OwnerPersonID: a.owner}, nil
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
	f.agent = tenant.Principal{TenantID: tid, ID: uuid.NewString(), Kind: tenant.Agent, Scopes: []string{"harness.worker", "harness.read"}}
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
	f.authority = &testAuthority{owner: f.owner.ID, generation: f.actor.DaemonGeneration, intent: nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: uuid.NewString(), OperationID: uuid.NewString(), Binding: &f.binding, Operation: "input", State: "accepted", InputID: &inputID, SourceSequence: "0", UpdatedAt: f.now}, ActorID: f.owner.ID, PayloadSHA256: strings.Repeat("b", 64), ExpiresAt: f.now.Add(time.Hour)}, final: FinalAuthority{Grant: grant, CurrentBinding: f.binding, Sender: f.agent, OriginalActor: f.owner, Target: chat.NativeFinalTarget{OwnerPersonID: f.owner.ID, ConversationID: thread.ID, RoleID: role.ID, SessionID: session}, ConversationRef: grant.ConversationRef, IssuedAt: f.now, RetentionApproved: true}}
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
	// Archival keeps the existing recovery constraint: even an archived
	// registration must retain its request, actor and unknown-process evidence.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped',archived_at=clock_timestamp(),recovery_process_state='unknown',recovery_request_id=$2,recovery_request_digest=$3,recovery_actor_id=$4,recovery_reason='fixture archived registration' WHERE id=$1`, f.binding.HarnessSessionID, uuid.NewString(), bytes.Repeat([]byte{3}, 32), f.owner.ID); err != nil {
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
	for _, substitute := range []tenant.Principal{f.owner, {TenantID: f.agent.TenantID, ID: f.agent.ID, Kind: tenant.Agent, Scopes: []string{"harness.worker", "harness.read", "chat.send", "chat.receive"}}} {
		f.authority.final.Sender = substitute
		_, err := f.repo.Persist(f.ctx(t), f.actor, f.final)
		reason(t, err, nativecontract.ReasonWrongPrincipal)
	}
	f.authority.final.Sender = f.agent
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
	for _, originalActor := range []bool{false, true} {
		t.Run(fmt.Sprint("original_actor_", originalActor), func(t *testing.T) {
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
			// Revoke the CURRENT role under the actual access fence. The native
			// authorizer remains unchanged, so RequireTx must observe the revocation.
			revokedID := f.agent.ID
			if originalActor {
				revokedID = f.owner.ID
			}
			if originalActor {
				permissions, _ := authz.BuiltinPermissions("member")
				kept := make([]string, 0, len(permissions))
				for _, permission := range permissions {
					if permission != "chat.send" {
						kept = append(kept, permission)
					}
				}
				var role string
				if err = holder.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'native_final_without_send','Final actor without chat send') RETURNING id::text`, f.owner.TenantID).Scan(&role); err != nil {
					t.Fatal(err)
				}
				if _, err = holder.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.owner.TenantID, role, kept); err != nil {
					t.Fatal(err)
				}
				if _, err = holder.Exec(ctx, `UPDATE role_bindings SET role_id=$3 WHERE tenant_id=$1 AND principal_id=$2`, f.owner.TenantID, revokedID, role); err != nil {
					t.Fatal(err)
				}
			} else if _, err = holder.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.agent.TenantID, revokedID); err != nil {
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
		})
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

// Risk: the native wiring loses acknowledged input/final correlation, uses
// broad daemon chat scopes, or frees the root on viewer detach/uncertain exit.
// The fake transport supplies explicit native evidence; real P3 journal,
// fenced server receipts, chat storage, C0 local adapter and attach client run.
func TestNativePilotDeliveryPersistsFinalAndKeepsHoldThroughDetach(t *testing.T) {
	f := newFixture(t)
	f.authority.intents = map[string]nativereceipts.Intent{}
	journal, err := nativereceipts.Open(filepath.Join(t.TempDir(), "receipts"), func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	provider := &pilotTransport{identity: nativecontract.NativeIdentity{Provider: "claude", NativeSessionRef: "exact-conversation", RuntimeEpoch: f.binding.RuntimeEpoch, ProcessIdentityDigest: strings.Repeat("c", 64), PID: 1234, StartedAt: f.now}, running: true, now: f.now}
	writer := &pilotWriter{f: f}
	releases := 0
	delivery, err := nativeintegration.NewDelivery(nativeintegration.DeliveryOptions{Binding: f.binding, Journal: journal, Transport: provider, Authority: writer, Reports: pilotReports{f}, Finals: Sink{Repository: f.repo, Daemon: f.actor}, Now: func() time.Time { return f.now },
		Retention: func(ctx context.Context, in nativecontract.FinalPublish) (nativecontract.FinalDrainGrant, nativereceipts.RetentionApproval, error) {
			if f.authority.revoked {
				return nativecontract.FinalDrainGrant{}, nativereceipts.RetentionApproval{}, nativereceipts.Fail(nativecontract.ReasonPermissionRevoked)
			}
			return f.authority.final.Grant, nativereceipts.RetentionApproval{OwnerPersonID: f.owner.ID, Approved: true, Until: f.authority.final.Grant.ExpiresAt}, nil
		}, Release: func(ctx context.Context, b nativecontract.Binding, exit nativecontract.ExitEvidence) error {
			var state string
			if err := f.d.Admin.QueryRow(ctx, `SELECT receipt->>'state' FROM native_session_receipts WHERE tenant_id=$1 AND receipt->>'operation'='stop'`, f.agent.TenantID).Scan(&state); err != nil {
				return err
			}
			if state != "exit_confirmed" || !b.Equal(f.binding) || !exit.RootExited || !exit.GroupAbsent || exit.ProcessIdentityDigest != provider.identity.ProcessIdentityDigest {
				return nativereceipts.Fail(nativecontract.ReasonOwnershipUnconfirmed)
			}
			releases++
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	host := &pilotHost{f: f, delivery: delivery, provider: provider}
	view := host.snapshot()
	projector, err := nativestream.NewProjector(nativestream.ExportPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := nativestream.NewLocalAdapter(host, pilotLocalAuthority{f: f, view: view}, projector, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	adapter.Mount(mux)
	root, err := os.MkdirTemp("", "pilot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(socket+".token", []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	session, err := nativeattach.Open(t.Context(), socket, nativeattach.Options{Snapshot: view, Control: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := session.Submit(t.Context(), "person input retained only in the native transport", "now")
	if err != nil || receipt.State != "write_acknowledged" || receipt.InputID == nil {
		t.Fatalf("native input %+v %v", receipt, err)
	}
	operation := receipt.OperationID
	f.final.InputID = *receipt.InputID
	f.final.TurnRef = provider.turn
	if _, err = delivery.Final(f.ctx(t), operation, f.final); err == nil {
		t.Fatal("final persisted without exact native completion")
	}
	for _, stage := range []string{"turn_started", "turn_completed"} {
		provider.sequence++
		evidence := nativecontract.NativeEvidence{Stage: stage, InputID: *receipt.InputID, RequestID: receipt.RequestID, TurnRef: provider.turn, SourceSequence: provider.sequence, ObservedAt: f.now}
		if stage == "turn_completed" {
			wrong := evidence
			wrong.InputID = uuid.NewString()
			_, err = delivery.Observe(f.ctx(t), operation, provider.identity, wrong)
			reason(t, err, nativecontract.ReasonIdentityMismatch)
		}
		if _, err = delivery.Observe(f.ctx(t), operation, provider.identity, evidence); err != nil {
			t.Fatal(err)
		}
	}
	final, err := delivery.Final(f.ctx(t), operation, f.final)
	if err != nil || final.State != "persisted" || final.MessageID == nil {
		t.Fatalf("explicit final %+v %v", final, err)
	}
	var sender, owner, text string
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT sender_principal_id::text,recipient_principal_id::text,body FROM inbox_messages WHERE id=$1`, *final.MessageID).Scan(&sender, &owner, &text); err != nil {
		t.Fatal(err)
	}
	if sender != f.agent.ID || owner != f.owner.ID || text != f.final.Text {
		t.Fatal("final attribution or retained answer changed")
	}
	page, err := session.Receipts(t.Context(), "")
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "turn_completed" {
		t.Fatalf("exact C0 receipt GET %+v %v", page, err)
	}
	if err = session.Detach(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !provider.running || provider.closed != 0 || releases != 0 || host.detached != 1 {
		t.Fatal("detach stopped the writer or released occupancy")
	}
	// Start one more exact turn, then interrupt it deliberately. A completed
	// first turn cannot serve as the interrupt target of this second turn.
	input := nativecontract.Input{RequestID: uuid.NewString(), InputID: uuid.NewString(), ExpectedBinding: f.binding, Mode: "now", Text: "second exact turn", ExpiresAt: f.now.Add(time.Hour)}
	op := uuid.NewString()
	host.register(op, input)
	second, err := delivery.Send(f.ctx(t), f.owner.ID, op, input)
	if err != nil {
		t.Fatal(err)
	}
	provider.sequence++
	_, err = delivery.Observe(f.ctx(t), op, provider.identity, nativecontract.NativeEvidence{Stage: "turn_started", RequestID: input.RequestID, InputID: input.InputID, TurnRef: provider.turn, SourceSequence: provider.sequence, ObservedAt: f.now})
	if err != nil {
		t.Fatal(err)
	}
	interrupt := nativecontract.Interrupt{RequestID: uuid.NewString(), ExpectedBinding: f.binding, ExpectedTurnRef: provider.turn, ExpiresAt: input.ExpiresAt}
	interruptOp := uuid.NewString()
	host.registerControl(interruptOp, interrupt.RequestID, "interrupt", interrupt, interrupt.ExpiresAt)
	applied, err := delivery.Interrupt(f.ctx(t), f.owner.ID, interruptOp, interrupt)
	if err != nil || applied.State != "applied" || provider.interrupts != 1 || second.InputID == nil {
		t.Fatalf("exact turn interrupt %+v %v", applied, err)
	}
	stop := nativecontract.Stop{RequestID: uuid.NewString(), ExpectedBinding: f.binding, Kind: "graceful", ExpectedProcessIdentityDigest: provider.identity.ProcessIdentityDigest, ExpiresAt: input.ExpiresAt}
	stopOp := uuid.NewString()
	host.registerControl(stopOp, stop.RequestID, "stop", stop, stop.ExpiresAt)
	provider.sequence++
	_, err = delivery.Close(f.ctx(t), f.owner.ID, stopOp, stop, provider.sequence)
	if err == nil || releases != 0 || provider.closed != 1 {
		t.Fatalf("unconfirmed group exit released occupancy: %v", err)
	}
	provider.groupAbsent = true
	exit, _ := provider.Wait(f.ctx(t))
	confirmed, err := delivery.ConfirmExit(f.ctx(t), stopOp, provider.sequence, exit)
	if err != nil || confirmed.State != "exit_confirmed" || releases != 1 || provider.closed != 1 {
		t.Fatalf("confirmed exit %+v %v", confirmed, err)
	}
}

type pilotTransport struct {
	identity             nativecontract.NativeIdentity
	now                  time.Time
	sequence             uint64
	turn                 string
	running, groupAbsent bool
	closed, interrupts   int
}

func (p *pilotTransport) Identity() nativecontract.NativeIdentity { return p.identity }
func (p *pilotTransport) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{Input: true, ChatSupported: true, Interrupt: true, InterruptSupported: true, Stop: true, EventsSupported: true, CorrelatedReceipts: true, Steer: "native", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}
}
func (p *pilotTransport) Events() <-chan nativecontract.ProviderEvent { return nil }
func (p *pilotTransport) Send(_ context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	p.sequence++
	p.turn = "provider-turn-" + in.InputID
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID, InputID: in.InputID, SourceSequence: p.sequence, ObservedAt: p.now}, nil
}
func (p *pilotTransport) Interrupt(_ context.Context, in nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	if in.ExpectedTurnRef != p.turn {
		return nativecontract.NativeEvidence{}, nativereceipts.Fail(nativecontract.ReasonIdentityMismatch)
	}
	p.interrupts++
	p.sequence++
	return nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID, SourceSequence: p.sequence, ObservedAt: p.now}, nil
}
func (p *pilotTransport) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, nativereceipts.Fail(nativecontract.ReasonUnsupported)
}
func (p *pilotTransport) Close(ctx context.Context) (nativecontract.ExitEvidence, error) {
	p.closed++
	p.running = false
	return p.Wait(ctx)
}
func (p *pilotTransport) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	return nativecontract.ExitEvidence{RootExited: !p.running, GroupAbsent: p.groupAbsent, ProcessIdentityDigest: p.identity.ProcessIdentityDigest, ObservedAt: p.now}, nil
}

type pilotWriter struct{ f *fixture }

func (w *pilotWriter) RequireWrite(ctx context.Context, b nativecontract.Binding, in nativereceipts.Intent) error {
	if !b.Equal(w.f.binding) || in.ActorID != w.f.owner.ID {
		return nativereceipts.Fail(nativecontract.ReasonWrongPrincipal)
	}
	return db.InTenant(ctx, w.f.d.App, w.f.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, w.f.agent.TenantID); err != nil {
			return err
		}
		for _, permission := range []string{"harness.control", "chat.send", "chat.receive"} {
			if err := authz.RequireTx(ctx, tx, w.f.owner, permission, authz.Scope{ProjectID: b.ProjectID}); err != nil {
				return err
			}
		}
		return nil
	})
}

type pilotReports struct{ f *fixture }

func (p pilotReports) Report(ctx context.Context, r nativecontract.Receipt) (nativecontract.Receipt, error) {
	result, err := p.f.repo.Report(tenant.WithPrincipal(ctx, p.f.agent), p.f.actor, r)
	return result.Receipt, err
}

type pilotHost struct {
	nativecontract.Host
	f          *fixture
	delivery   *nativeintegration.Delivery
	provider   *pilotTransport
	controller *nativecontract.ControllerView
	detached   int
}

func (h *pilotHost) snapshot() nativecontract.SessionView {
	return nativecontract.SessionView{OwnedSessionID: h.f.binding.OwnedSessionID, CurrentBinding: &h.f.binding, OwnerPersonID: h.f.owner.ID, Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", Capabilities: h.provider.Capabilities(), Revision: 1, Generation: 1, ConversationRef: "exact-conversation", Controller: h.controller}
}
func (h *pilotHost) Attach(_ context.Context, in nativecontract.AttachIntent) (nativecontract.AttachResult, error) {
	h.controller = &nativecontract.ControllerView{ControllerID: uuid.NewString(), ActorID: h.f.owner.ID, ClientID: in.ClientID, Epoch: uuid.NewString(), ExpiresAt: time.Now().Add(30 * time.Second)}
	proof := "fixture-derived-control"
	return nativecontract.AttachResult{Binding: h.f.binding, Snapshot: h.snapshot(), ViewerID: uuid.NewString(), Mode: in.Mode, Controller: h.controller, ControlCapability: &proof, ExpiresAt: h.controller.ExpiresAt, ExpiresInMS: 30000, StreamPath: "/v1/native-sessions/" + h.f.binding.OwnedSessionID + "/stream"}, nil
}
func (h *pilotHost) register(op string, in nativecontract.Input) {
	digest, _ := nativereceipts.Digest(in)
	id := in.InputID
	h.f.authority.intents[op] = nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: in.RequestID, OperationID: op, Binding: &h.f.binding, Operation: "input", State: "accepted", InputID: &id, SourceSequence: "0", UpdatedAt: h.f.now}, ActorID: h.f.owner.ID, PayloadSHA256: digest, ExpiresAt: in.ExpiresAt}
}
func (h *pilotHost) registerControl(op, request, kind string, value any, expires time.Time) {
	digest, _ := nativereceipts.Digest(value)
	h.f.authority.intents[op] = nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: request, OperationID: op, Binding: &h.f.binding, Operation: kind, State: "accepted", SourceSequence: "0", UpdatedAt: h.f.now}, ActorID: h.f.owner.ID, PayloadSHA256: digest, ExpiresAt: expires}
}
func (h *pilotHost) Input(ctx context.Context, in nativecontract.Input) (nativecontract.OperationResult, error) {
	op := uuid.NewString()
	h.register(op, in)
	r, err := h.delivery.Send(tenant.WithPrincipal(ctx, h.f.agent), h.f.owner.ID, op, in)
	return nativecontract.OperationResult{Receipt: r}, err
}
func (h *pilotHost) Detach(_ context.Context, in nativecontract.Detach) (nativecontract.OperationResult, error) {
	h.detached++
	h.controller = nil
	return nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: in.RequestID, OperationID: uuid.NewString(), Binding: &h.f.binding, Operation: "detach", State: "applied", SourceSequence: "0", UpdatedAt: h.f.now}}, nil
}
func (h *pilotHost) Receipts(ctx context.Context, b nativecontract.Binding, q nativecontract.PageQuery) (nativecontract.ReceiptPage, error) {
	a := nativecontract.Actor{TenantID: h.f.owner.TenantID, PrincipalID: h.f.owner.ID, PrincipalKind: "person"}
	return h.f.repo.Receipts(tenant.WithPrincipal(ctx, h.f.owner), a, b, q)
}

type pilotLocalAuthority struct {
	f    *fixture
	view nativecontract.SessionView
}

func (a pilotLocalAuthority) Authenticate(r *http.Request) (nativecontract.Actor, error) {
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
		return nativecontract.Actor{}, nativereceipts.Fail(nativecontract.ReasonGrantMissing)
	}
	return nativecontract.Actor{TenantID: a.f.owner.TenantID, PrincipalID: a.f.owner.ID, PrincipalKind: "person"}, nil
}
func (a pilotLocalAuthority) WithAccess(ctx context.Context, actor nativecontract.Actor, in nativestream.LocalRequest, deliver func(nativecontract.ValidationContext) error) error {
	if actor.PrincipalID != a.f.owner.ID || in.ExpectedBinding != nil && !in.ExpectedBinding.Equal(a.f.binding) {
		return nativestream.Refuse(nativecontract.ReasonWrongGeneration)
	}
	return deliver(nativecontract.ValidationContext{Binding: &a.f.binding, Generation: 1, PrincipalKind: "person", Capabilities: &a.view.Capabilities, Mode: "structured", State: "running", View: in.View})
}
