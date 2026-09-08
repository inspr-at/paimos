// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package agentmessage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
)

type consumerFixture struct {
	s              *Service
	project, agent int64
	c              ConsumerCredentials
	r              ConsumerRegistration
}

func testConsumerProof(n byte) string {
	b := make([]byte, 32)
	b[0] = n
	return base64.RawURLEncoding.EncodeToString(b)
}
func newConsumerFixture(t *testing.T, kind string) *consumerFixture {
	return newConsumerFixtureOwned(t, kind, `{"account_label":"chatgpt"}`, "chatgpt", "", "", "", "", "")
}
func newConsumerFixtureOwned(t *testing.T, kind, registrationJSON, accountLabel, accountKey, profileID, profileVersion, profileModel, profileEffort string) *consumerFixture {
	t.Helper()
	s, project := openBusTestDB(t)
	f := &consumerFixture{s: s, project: project}
	res, e := s.db.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('consumer-admin','disabled','admin','super_admin','active',1)`)
	if e != nil {
		t.Fatal(e)
	}
	user, _ := res.LastInsertId()
	res, e = s.db.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture','not-a-credential','fixture','*')`, user)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := res.LastInsertId()
	p, e := auth.NewAPIKeyPrincipal(key, user, auth.ParseScopes("*"))
	if e != nil {
		t.Fatal(e)
	}
	f.c = ConsumerCredentials{Principal: p, RuntimeLease: testConsumerProof(1), ConsumerLease: testConsumerProof(2), AttemptNonce: testConsumerProof(3)}
	f.r = ConsumerRegistration{RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), SessionID: uuid.NewString(), SessionGeneration: uuid.NewString(), Generation: uuid.NewString(), Kind: kind}
	if e = s.db.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, project).Scan(&f.agent); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte("paimos-lifecycle-runtime-v1\x00" + f.r.RuntimeGeneration + "\x00" + f.c.RuntimeLease))
	_, e = s.db.Exec(`INSERT INTO lifecycle_runtimes(id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at) VALUES(?,?,?,'consumer-machine',?,?,?, ?,?,?)`, f.r.RuntimeID, project, f.r.RuntimeGeneration, user, key, digest[:], registrationJSON, consumerStamp(time.Now().Add(time.Hour)), consumerStamp(time.Now()))
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,account_label,account_key,dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,workspace_identity,workspace_path,workspace_kind,workspace_mode) VALUES(?,?,?,'amy','codex','consumer-machine',zeroblob(32),zeroblob(32),'managed','worker','none',0,1,0,1,1,'working',?,?,?,?,?,?,printf('%064d',1),'/fixture/workspace','directory','exclusive')`, f.r.SessionID, project, f.agent, accountLabel, accountKey, profileID, profileVersion, profileModel, profileEffort)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(`INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at) VALUES(?,?,?,?)`, f.r.SessionID, f.r.RuntimeID, f.r.SessionGeneration, consumerStamp(time.Now()))
	if e != nil {
		t.Fatal(e)
	}
	if kind == "attention" {
		configureAttentionReceiver(t, s, project)
		targets, e := s.ListTargets(context.Background(), project, "codex:amy")
		if e != nil || len(targets) != 1 {
			t.Fatalf("targets %v", e)
		}
		f.r.TargetID = targets[0].ID
		f.r.TargetVersion = int64(targets[0].Version)
	} else {
		target, e := s.RegisterTarget(context.Background(), RegisterTargetInput{ProjectID: project, Address: "codex:amy", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread, TargetRef: "fixture-private-target", MaximumLevel: "simple", Role: "simple_fallback"})
		if e != nil {
			t.Fatal(e)
		}
		f.r.TargetID = target.ID
		f.r.TargetVersion = int64(target.Version)
		allowBusSender(t, s, project, "codex:amy")
	}
	return f
}
func (f *consumerFixture) register(t *testing.T) ConsumerStream {
	t.Helper()
	out, e := f.s.RegisterConsumer(context.Background(), f.c, f.project, f.r)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *consumerFixture) message(t *testing.T) *Envelope {
	t.Helper()
	out, e := f.s.SendEnvelope(context.Background(), SendEnvelopeInput{ProjectID: f.project, Sender: "sender", To: "codex:amy", Body: "fixture private content", DeliveryLevel: "simple", IdempotencyKey: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *consumerFixture) claim(t *testing.T, stream ConsumerStream) ConsumerAttempt {
	t.Helper()
	page, e := f.s.ClaimConsumer(context.Background(), f.c, f.project, stream.ID, ConsumerClaim{ExpectedRevision: stream.Revision, RequestKey: uuid.NewString()})
	if e != nil || page.Attempt == nil {
		t.Fatalf("claim: %v", e)
	}
	if page.Delivery != nil || page.Attention != nil {
		t.Fatal("claim exposed payload")
	}
	return *page.Attempt
}
func (f *consumerFixture) expireAttempt(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	tx, e := f.s.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	a, e := loadAttempt(ctx, tx, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = expireConsumerAttemptAt(ctx, tx, &a, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}

func TestConsumerPrivateAuthorityAndNoOracle(t *testing.T) {
	f := newConsumerFixture(t, "fallback")
	ctx := context.Background()
	for name, mutate := range map[string]func(*ConsumerCredentials, *ConsumerRegistration){
		"runtime proof":              func(c *ConsumerCredentials, _ *ConsumerRegistration) { c.RuntimeLease = testConsumerProof(9) },
		"public generation as proof": func(c *ConsumerCredentials, r *ConsumerRegistration) { c.RuntimeLease = r.RuntimeGeneration },
		"consumer proof":             func(c *ConsumerCredentials, _ *ConsumerRegistration) { c.ConsumerLease = "" },
		"runtime generation":         func(_ *ConsumerCredentials, r *ConsumerRegistration) { r.RuntimeGeneration = uuid.NewString() },
		"session generation":         func(_ *ConsumerCredentials, r *ConsumerRegistration) { r.SessionGeneration = uuid.NewString() },
		"target revision":            func(_ *ConsumerCredentials, r *ConsumerRegistration) { r.TargetVersion++ },
	} {
		t.Run(name, func(t *testing.T) {
			c, r := f.c, f.r
			mutate(&c, &r)
			if _, e := f.s.RegisterConsumer(ctx, c, f.project, r); e == nil {
				t.Fatal("unproved binding accepted")
			}
		})
	}
	stream := f.register(t)
	f.message(t)
	for _, id := range []string{stream.ID, uuid.NewString()} {
		c := f.c
		c.ConsumerLease = testConsumerProof(9)
		if _, e := f.s.ClaimConsumer(ctx, c, f.project, id, ConsumerClaim{ExpectedRevision: 1, RequestKey: uuid.NewString()}); e != ErrConsumerUnavailable {
			t.Fatalf("oracle error: %v", e)
		}
	}
	if _, e := f.s.db.Exec(`UPDATE api_keys SET scopes='' WHERE id=?`, f.c.Principal.APIKeyID()); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.ClaimConsumer(ctx, f.c, f.project, stream.ID, ConsumerClaim{ExpectedRevision: 1, RequestKey: uuid.NewString()}); e != ErrConsumerUnavailable {
		t.Fatalf("revoked claim %v", e)
	}
	var n int
	if e := f.s.db.QueryRow(`SELECT COUNT(*) FROM agent_consumer_attempts`).Scan(&n); e != nil || n != 0 {
		t.Fatal("refused authority mutated attempts")
	}
}

func TestConsumerConcurrentClaimOneTimePayloadCompletionAndReplacement(t *testing.T) {
	f := newConsumerFixture(t, "fallback")
	ctx := context.Background()
	stream := f.register(t)
	message := f.message(t)
	request := ConsumerClaim{ExpectedRevision: stream.Revision, RequestKey: uuid.NewString()}
	var wg sync.WaitGroup
	pages := make(chan ConsumerPage, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := f.s.ClaimConsumer(ctx, f.c, f.project, stream.ID, request)
			pages <- p
			errs <- e
		}()
	}
	wg.Wait()
	close(pages)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var attempt ConsumerAttempt
	for p := range pages {
		if p.Attempt == nil || p.Delivery != nil || p.Attention != nil {
			t.Fatal("claim leaked or lost work")
		}
		if attempt.ID != "" && attempt.ID != p.Attempt.ID {
			t.Fatal("concurrent duplicate")
		}
		attempt = *p.Attempt
	}
	for name, c := range map[string]ConsumerCredentials{"wrong nonce": {Principal: f.c.Principal, RuntimeLease: f.c.RuntimeLease, ConsumerLease: f.c.ConsumerLease, AttemptNonce: testConsumerProof(8)}, "wrong lease": {Principal: f.c.Principal, RuntimeLease: f.c.RuntimeLease, ConsumerLease: testConsumerProof(8), AttemptNonce: f.c.AttemptNonce}} {
		if _, e := f.s.ExecuteConsumer(ctx, c, f.project, stream.ID, attempt.ID, stream.Revision); e != ErrConsumerUnavailable {
			t.Fatalf("%s: %v", name, e)
		}
	}
	pages = make(chan ConsumerPage, 8)
	errs = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := f.s.ExecuteConsumer(ctx, f.c, f.project, stream.ID, attempt.ID, stream.Revision)
			pages <- p
			errs <- e
		}()
	}
	wg.Wait()
	close(pages)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	payloads := 0
	for p := range pages {
		if p.Attempt.State != "executing" {
			t.Fatal("state not committed")
		}
		if p.Delivery != nil {
			payloads++
			if p.Delivery.Envelope.Cursor != message.Cursor || p.Delivery.Work.TargetRef != "fixture-private-target" {
				t.Fatal("wrong payload")
			}
		}
	}
	if payloads != 1 {
		t.Fatalf("payloads %d", payloads)
	}
	if _, e := f.s.CompleteConsumer(ctx, f.c, f.project, stream.ID, attempt.ID, ConsumerCompletion{ExpectedRevision: 1, Outcome: "applied", EffectiveLevel: "simple"}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.db.Exec(`UPDATE agent_consumer_streams SET expires_at=? WHERE id=?`, consumerStamp(time.Now().Add(-time.Hour)), stream.ID); e != nil {
		t.Fatal(e)
	}
	old := f.c
	f.r.Generation = uuid.NewString()
	f.c.ConsumerLease = testConsumerProof(4)
	replacement := f.register(t)
	if replacement.Revision != 2 {
		t.Fatal("revision not incremented")
	}
	result, e := f.s.CompleteConsumer(ctx, old, f.project, stream.ID, attempt.ID, ConsumerCompletion{ExpectedRevision: 1, Outcome: "applied", EffectiveLevel: "simple"})
	if e != nil || result.Cursor != message.Cursor {
		t.Fatalf("completion replay %v", e)
	}
	if _, e = f.s.CompleteConsumer(ctx, old, f.project, stream.ID, attempt.ID, ConsumerCompletion{ExpectedRevision: 1, Outcome: "outcome_unknown", EffectiveLevel: "simple"}); e != ErrConsumerConflict {
		t.Fatalf("changed replay %v", e)
	}
	var raw string
	if e = f.s.db.QueryRow(`SELECT owner_json||result_json FROM agent_consumer_attempts WHERE id=?`, attempt.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{old.RuntimeLease, old.ConsumerLease, old.AttemptNonce, "fixture private content", "fixture-private-target"} {
		if strings.Contains(raw, secret) {
			t.Fatal("private material persisted in consumer ledger")
		}
	}
}

func TestConsumerExpiryReleasesOnlyUnissuedPayloadAndQuarantinesUnknown(t *testing.T) {
	f := newConsumerFixture(t, "fallback")
	ctx := context.Background()
	stream := f.register(t)
	f.message(t)
	first := f.claim(t, stream)
	f.expireAttempt(t, first.ID)
	f.c.AttemptNonce = testConsumerProof(4)
	second := f.claim(t, stream)
	if second.ResourceID != first.ResourceID || second.ID == first.ID {
		t.Fatal("canonical work not preserved")
	}
	stale := f.c
	stale.AttemptNonce = testConsumerProof(3)
	page, e := f.s.ExecuteConsumer(ctx, stale, f.project, stream.ID, first.ID, 1)
	if e != nil || page.Delivery != nil || page.Attempt.State != "released" {
		t.Fatal("stale attempt received payload")
	}
	page, e = f.s.ExecuteConsumer(ctx, f.c, f.project, stream.ID, second.ID, 1)
	if e != nil || page.Delivery == nil {
		t.Fatal(e)
	}
	f.expireAttempt(t, second.ID)
	f.c.AttemptNonce = testConsumerProof(5)
	if _, e = f.s.ClaimConsumer(ctx, f.c, f.project, stream.ID, ConsumerClaim{ExpectedRevision: 1, RequestKey: uuid.NewString()}); e != ErrConsumerUnknown {
		t.Fatalf("unknown reissued %v", e)
	}
	if _, e = f.s.db.Exec(`UPDATE agent_consumer_streams SET expires_at=? WHERE id=?`, consumerStamp(time.Now().Add(-time.Hour)), stream.ID); e != nil {
		t.Fatal(e)
	}
	f.r.Generation = uuid.NewString()
	f.c.ConsumerLease = testConsumerProof(6)
	if _, e = f.s.RegisterConsumer(ctx, f.c, f.project, f.r); e != ErrConsumerHandoff {
		t.Fatalf("ambiguous owner replaced %v", e)
	}
	for _, query := range []string{`UPDATE agent_message_deliveries SET state='pending',lease_until=NULL`, `UPDATE agent_message_deliveries SET state='handed_off'`, fmt.Sprintf(`INSERT INTO agent_message_cursors(project_id,project_agent_id,address,cursor) VALUES(%d,%d,'codex:amy',%d)`, f.project, f.agent, first.Cursor)} {
		if _, e = f.s.db.Exec(query); e == nil {
			t.Fatal("old binary bypassed sticky fence")
		}
	}
}

func TestConsumerSafeLegacyHandoffAndExactTarget(t *testing.T) {
	f := newConsumerFixture(t, "fallback")
	ctx := context.Background()
	f.message(t)
	page, e := f.s.ListInbox(ctx, InboxInput{ProjectID: f.project, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex})
	if e != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil {
		t.Fatalf("legacy lease %v", e)
	}
	if _, e = f.s.RegisterConsumer(ctx, f.c, f.project, f.r); e != ErrConsumerHandoff {
		t.Fatalf("stole live legacy lease %v", e)
	}
	if _, e = f.s.db.Exec(`UPDATE agent_message_deliveries SET lease_until=?`, consumerStamp(time.Now().Add(-time.Hour))); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.RegisterConsumer(ctx, f.c, f.project, f.r); e != ErrConsumerHandoff {
		t.Fatalf("stole expired legacy lease %v", e)
	}
	work := page.Messages[0].DeliveryWork
	if _, e = f.s.CompleteLocalDelivery(ctx, CompleteDeliveryInput{ProjectID: f.project, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor, DeliveryID: work.DeliveryID, EffectiveLevel: "simple"}); e != nil {
		t.Fatal(e)
	}
	stream := f.register(t)
	f.message(t)
	attempt := f.claim(t, stream)
	if _, e = f.s.db.Exec(`UPDATE agent_message_targets SET enabled=0 WHERE id=?`, f.r.TargetID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ExecuteConsumer(ctx, f.c, f.project, stream.ID, attempt.ID, 1); e != ErrConsumerUnavailable {
		t.Fatalf("disabled target issued %v", e)
	}
}

func TestConsumerAttentionHealthCoalescingAndFences(t *testing.T) {
	f := newConsumerFixture(t, "attention")
	ctx := context.Background()
	stream := f.register(t)
	in := RuntimeHealthInput{RuntimeID: f.r.RuntimeID, RuntimeGeneration: f.r.RuntimeGeneration, Sequence: 1, Layer: "fallback", State: "unhealthy", Reason: "consumer_crash_loop", FailureCount: 3}
	one, e := f.s.PublishRuntimeHealth(ctx, f.c, f.project, in)
	if e != nil || !one.Published {
		t.Fatalf("publish %v", e)
	}
	replay, e := f.s.PublishRuntimeHealth(ctx, f.c, f.project, in)
	if e != nil || replay != one {
		t.Fatal("health retry changed")
	}
	in.Sequence++
	in.FailureCount = 4
	two, e := f.s.PublishRuntimeHealth(ctx, f.c, f.project, in)
	if e != nil || two.Published {
		t.Fatal("episode was not coalesced")
	}
	attempt := f.claim(t, stream)
	page, e := f.s.ExecuteConsumer(ctx, f.c, f.project, stream.ID, attempt.ID, 1)
	if e != nil || page.Attention == nil || len(page.Attention.Items) != 1 {
		t.Fatalf("attention payload %v", e)
	}
	if _, e = f.s.AckAttention(ctx, AttentionAckInput{ProjectID: f.project, Address: "codex:amy", Agent: "amy", Cursor: attempt.Cursor, BatchID: attempt.ResourceID}); e != ErrConsumerUnavailable {
		t.Fatal("legacy attention ack accepted")
	}
	if _, e = f.s.db.Exec(`UPDATE agent_attention_batches SET state='pending',lease_until=NULL`); e == nil {
		t.Fatal("old binary attention update accepted")
	}
	if _, e = f.s.CompleteConsumer(ctx, f.c, f.project, stream.ID, attempt.ID, ConsumerCompletion{ExpectedRevision: 1, Outcome: "applied", EffectiveLevel: "simple"}); e != nil {
		t.Fatal(e)
	}
	in.Sequence++
	in.State = "healthy"
	in.Reason = "recovered"
	in.FailureCount = 0
	if _, e = f.s.PublishRuntimeHealth(ctx, f.c, f.project, in); e != nil {
		t.Fatal(e)
	}
	var active int
	if e = f.s.db.QueryRow(`SELECT COUNT(*) FROM agent_attention_items ai WHERE ` + activeAttentionItemPredicate).Scan(&active); e != nil || active != 0 {
		t.Fatal("recovery did not resolve source")
	}
	in.Reason = "private free text"
	if _, e = f.s.PublishRuntimeHealth(ctx, f.c, f.project, in); e != ErrConsumerInvalid {
		t.Fatal("health accepted free text")
	}
}

func TestConsumerV3ScopeFenceMatchAndCrossClass(t *testing.T) {
	v3 := `{"schema_version":3,"account_scopes":[{"account_label":"chatgpt","profiles":[{"id":"codex-sol-high","version":"1"}]},{"account_label":"cursor_context","accounts":[{"key":"cursor-op","label":"Cursor"}],"profiles":[{"id":"cursor-composer","version":"1"}]}]}`
	match := newConsumerFixtureOwned(t, "fallback", v3, "chatgpt", "", "codex-sol-high", "1", "codex", "high")
	if _, err := match.s.RegisterConsumer(context.Background(), match.c, match.project, match.r); err != nil {
		t.Fatalf("matching v3 chatgpt consumer refused: %v", err)
	}
	crossed := newConsumerFixtureOwned(t, "fallback", v3, "cursor_context", "codex-work", "codex-sol-high", "1", "codex", "high")
	if _, err := crossed.s.RegisterConsumer(context.Background(), crossed.c, crossed.project, crossed.r); err != ErrConsumerUnavailable {
		t.Fatalf("cross-class v3 consumer accepted: %v", err)
	}
	named := newConsumerFixtureOwned(t, "fallback", `{"schema_version":3,"account_scopes":[{"account_label":"chatgpt","accounts":[{"key":"codex-work","label":"Work"}],"profiles":[{"id":"codex-sol-high","version":"1"}]}]}`, "chatgpt", "", "codex-sol-high", "1", "codex", "high")
	if _, err := named.s.RegisterConsumer(context.Background(), named.c, named.project, named.r); err != ErrConsumerUnavailable {
		t.Fatalf("named chatgpt scope accepted class-only consumer: %v", err)
	}
}
