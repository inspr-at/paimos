// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func messagingWorld(t *testing.T) (*world, *messaging, string, *httptest.Server) {
	t.Helper()
	w := newWorld(t)
	w.agent = insertPrincipal(t, w.db, w.sender.TenantID, tenant.Agent, "worker", nil)
	w.agent.Scopes = []string{"inbox.send", "inbox.read"}
	var project string
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1::uuid,id,'MSG-1','Messaging' FROM node_kinds WHERE slug='project' RETURNING id::text`, w.sender.TenantID).Scan(&project)
	})
	if err != nil {
		t.Fatal(err)
	}
	mod, err := NewMessaging(w.db.App, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m := mod.(*messaging)
	ps := map[string]tenant.Principal{}
	for _, p := range []tenant.Principal{w.sender, w.recipient, w.admin, w.agent, w.outsider} {
		ps[p.ID] = p
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	wmod := newModule(w.db.App)
	wmod.Mount(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if p, ok := ps[r.Header.Get("X-Principal")]; ok {
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		mux.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	return w, m, project, srv
}
func compatInput(to, key string) compatSend {
	return compatSend{To: to, Body: "fixture message body", Key: key, Level: "simple"}
}
func mustCompatSend(t *testing.T, m *messaging, p tenant.Principal, project string, in compatSend) CompatMessage {
	t.Helper()
	out, err := m.commitMessage(t.Context(), p, project, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func compatPost(t *testing.T, srv *httptest.Server, p tenant.Principal, path string, v any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return do(t, srv, p.ID, "POST", path, string(b), nil)
}

func TestMessagingTargetsEncryptionVersionsAndAuthorization(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	path := "/api/projects/" + project + "/message-targets"
	in := targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "fixture-private-thread", Role: "primary", MaximumLevel: "simple"}
	status, _ := compatPost(t, srv, w.sender, path, in)
	if status != 403 {
		t.Fatalf("non-admin %d", status)
	}
	status, data := compatPost(t, srv, w.admin, path, in)
	if status != 201 {
		t.Fatalf("set %d %s", status, data)
	}
	first := mustJSON[MessageTarget](t, data)
	if strings.Contains(string(data), in.Ref) || strings.Contains(string(data), "target_ref") {
		t.Fatal("reference in response")
	}
	in.Ref = "fixture-replacement-thread"
	status, data = compatPost(t, srv, w.admin, path, in)
	if status != 201 {
		t.Fatalf("replace %d %s", status, data)
	}
	second := mustJSON[MessageTarget](t, data)
	if first.ID == second.ID || second.Version != 2 {
		t.Fatal("target version was not advanced")
	}
	in.PrincipalID = w.sender.ID
	status, _ = compatPost(t, srv, w.admin, path, in)
	if status != 400 {
		t.Fatalf("rebind %d", status)
	}
	status, data = do(t, srv, w.admin.ID, "GET", path, "", nil)
	if status != 200 {
		t.Fatalf("list %d", status)
	}
	targets := mustJSON[[]MessageTarget](t, data)
	if len(targets) != 2 || targets[0].Version != 2 || targets[1].Enabled {
		t.Fatal("incorrect version history")
	}
	if strings.Contains(string(data), "fixture-") {
		t.Fatal("private target in list")
	}
	status, _ = do(t, srv, w.outsider.ID, "GET", path, "", nil)
	if status != 403 {
		t.Fatalf("foreign non-admin %d", status)
	}
	// Exercise the routine HTTP adapter with inert synthetic bytes. The public
	// literal avoids DNS; target registration never dials the endpoint.
	secretIn := targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/fixture-hook", Secret: "fixture-routine-key", Role: "primary", MaximumLevel: "simple"}
	status, data = compatPost(t, srv, w.admin, path, secretIn)
	if status != 201 {
		t.Fatalf("routine target status %d", status)
	}
	privateTarget := mustJSON[MessageTarget](t, data)
	if !privateTarget.HasSecret || strings.Contains(string(data), secretIn.Secret) || strings.Contains(string(data), secretIn.Ref) {
		t.Fatal("routine redaction failed")
	}
	var err error
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.admin.TenantID, func(tx pgx.Tx) error {
		var sealed []byte
		if err := tx.QueryRow(t.Context(), `SELECT sealed_target FROM inbox_message_targets WHERE id=$1::uuid`, privateTarget.ID).Scan(&sealed); err != nil {
			return err
		}
		if bytes.Contains(sealed, []byte(secretIn.Secret)) || bytes.Contains(sealed, []byte(secretIn.Ref)) {
			return errors.New("unencrypted target")
		}
		size := m.aead.NonceSize()
		plain, err := m.aead.Open(nil, sealed[:size], sealed[size:], []byte(w.admin.TenantID+"/"+project+"/"+privateTarget.ID))
		if err != nil {
			return err
		}
		var v struct{ Ref, Secret string }
		if err := json.Unmarshal(plain, &v); err != nil {
			return err
		}
		if v.Ref != secretIn.Ref || v.Secret != secretIn.Secret {
			return errors.New("encrypted fields did not round trip")
		}
		if _, err := m.aead.Open(nil, sealed[:size], sealed[size:], []byte(w.outsider.TenantID+"/"+project+"/"+privateTarget.ID)); err == nil {
			return errors.New("ciphertext not tenant bound")
		}
		var leaked bool
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE after::text LIKE '%fixture-%')`).Scan(&leaked); err != nil {
			return err
		}
		if leaked {
			return errors.New("private target in event")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.outsider.TenantID, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_message_targets`).Scan(&n)
		if err == nil && n != 0 {
			return errors.New("target RLS leak")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMessaging(w.db.App, nil); err == nil {
		t.Fatal("missing key accepted")
	}
	plug, err := MessagingPlugin()
	if err != nil || plug.Manifest.DigestSHA256 == "" {
		t.Fatal("invalid manifest", err)
	}
	_ = httpapi.Module(m)
}

func TestMessagingAdapterValidation(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		adapter, harness, kind, ref, level string
		valid                              bool
	}{
		{"codex", "codex", "codex_thread", "thread-fixture", "steer", true},
		{"agentd_codex", "codex", "agentd_session", `{"socket":"/tmp/fake.sock","session_id":"` + id + `"}`, "steer", true},
		{"agentd_claude", "claude", "agentd_session", `{"socket":"/tmp/fake.sock","session_id":"` + id + `"}`, "steer", true},
		{"agentd_pi", "pi", "agentd_session", `{"socket":"/tmp/fake.sock","session_id":"` + id + `"}`, "steer", true},
		{"agentd_cursor", "cursor", "agentd_session", `{"socket":"/tmp/fake.sock","session_id":"` + id + `"}`, "simple", true},
		{"agentd_cursor", "cursor", "agentd_session", `{"socket":"/tmp/fake.sock","session_id":"` + id + `"}`, "steer", false},
		{"claude_resume", "claude", "claude_session", "session_fixture", "simple", true},
		{"claude_resume", "claude", "claude_session", id, "simple", true},
		{"claude_channel", "claude", "claude_session", id, "simple", true},
		{"claude_channel", "claude", "claude_session", "session_fixture", "simple", false},
		{"codex", "claude", "codex_thread", "thread-fixture", "simple", false},
		{"codex", "codex", "claude_session", id, "simple", false},
		{"agentd_pi", "pi", "agentd_session", `{"socket":"relative","session_id":"` + id + `"}`, "simple", false},
		{"grok_bot_routine", "grok_bot", "https_webhook", "https://127.0.0.1/", "simple", false},
	} {
		t.Run(tc.adapter+"/"+tc.level+"/"+tc.ref[:min(8, len(tc.ref))], func(t *testing.T) {
			in := targetInput{Address: tc.harness + ":worker", Adapter: tc.adapter, Kind: tc.kind, Ref: tc.ref, MaximumLevel: tc.level}
			err := validateCompatTarget(t.Context(), &in)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
	in := targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "fixture", Secret: "fixture-key"}
	if validateCompatTarget(context.Background(), &in) == nil {
		t.Fatal("unexpected key accepted")
	}
}

func TestMessagingReplyObligationsHeldIsolationAndReplay(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	path := "/api/projects/" + project + "/messages"
	in := compatInput(w.recipient.ID, "request")
	in.ExpectsReply = true
	first := mustCompatSend(t, m, w.sender, project, in)
	if first.ReplyObligation != "open" || first.Status != "accepted" {
		t.Fatal("missing obligation")
	}
	replay := mustCompatSend(t, m, w.sender, project, in)
	if replay.ID != first.ID {
		t.Fatal("not idempotent")
	}
	changed := in
	changed.ExpectsReply = false
	if _, err := m.commitMessage(t.Context(), w.sender, project, changed); !errors.Is(err, errConflict) {
		t.Fatal("changed flag must conflict", err)
	}
	if _, err := m.base.ack(t.Context(), w.recipient, first.ID); err != nil {
		t.Fatal(err)
	}
	replay = mustCompatSend(t, m, w.sender, project, in)
	if replay.ReplyObligation != "open" {
		t.Fatal("ack closed obligation")
	}
	held := compatInput(w.sender.ID, "held-reply")
	held.ActionRequest = true
	held.ReplyTo = &first.ID
	heldMsg := mustCompatSend(t, m, w.recipient, project, held)
	if heldMsg.Status != "held" {
		t.Fatal("not held")
	}
	replay = mustCompatSend(t, m, w.sender, project, in)
	if replay.ReplyObligation != "open" {
		t.Fatal("held reply closed obligation")
	}
	status, data := do(t, srv, w.sender.ID, "GET", path+"/listen", "", nil)
	if status != 200 {
		t.Fatalf("listen %d %s", status, data)
	}
	if len(mustJSON[compatPage](t, data).Items) != 0 {
		t.Fatal("held message leaked into compat listen")
	}
	pending, err := m.base.pending(t.Context(), w.sender, 0, 100)
	if err != nil || len(pending) != 0 {
		t.Fatal("held message leaked into R2", err)
	}
	if _, err := m.base.ack(t.Context(), w.sender, heldMsg.ID); !errors.Is(err, errNotFound) {
		t.Fatal("held message acked", err)
	}
	badReply := compatInput(w.sender.ID, "wrong-counterpart")
	badReply.ReplyTo = &first.ID
	if _, err := m.commitMessage(t.Context(), w.other, project, badReply); !errors.Is(err, errNotFound) {
		t.Fatal("wrong counterpart accepted", err)
	}
	reply := held
	reply.Key = "accepted-reply"
	reply.ActionRequest = false
	answer := mustCompatSend(t, m, w.recipient, project, reply)
	replay = mustCompatSend(t, m, w.sender, project, in)
	if replay.ReplyObligation != "closed" {
		t.Fatal("accepted counterpart did not close")
	}
	if answer.ID == heldMsg.ID {
		t.Fatal("held row released")
	}
	status, data = do(t, srv, w.admin.ID, "GET", path, "", nil)
	if status != 200 || len(mustJSON[compatPage](t, data).Items) != 3 {
		t.Fatalf("human inspection %d", status)
	}
	status, _ = do(t, srv, w.agent.ID, "GET", path, "", nil)
	if status != 403 {
		t.Fatalf("agent inspection %d", status)
	}
	// Redacted outbox exists for both held and accepted rows.
	status, data = do(t, srv, w.admin.ID, "GET", "/api/projects/"+project+"/message-deliveries", "", nil)
	if status != 200 {
		t.Fatalf("deliveries %d", status)
	}
	ds := mustJSON[[]MessageDelivery](t, data)
	if len(ds) != 3 || strings.Contains(string(data), in.Body) {
		t.Fatal("incorrect/redaction outbox")
	}
	heldCount := 0
	for _, d := range ds {
		if d.State == "held" {
			heldCount++
			if d.TargetID != nil {
				t.Fatal("held target selected")
			}
		}
	}
	if heldCount != 1 {
		t.Fatal("held ledger missing")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.outsider.TenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"inbox_compat_messages", "inbox_reply_obligations", "inbox_message_deliveries"} {
			var n int
			if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return errors.New("tenant isolation failure")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMessagingAddressAuthSnapshotAndConcurrentSend(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	in := compatInput("codex:worker", "race")
	in.ExpectsReply = true
	targetIn := targetInput{Address: in.To, Adapter: "codex", Kind: "codex_thread", Ref: "fixture-old-thread", Role: "primary", MaximumLevel: "simple"}
	firstTarget, err := m.storeTarget(t.Context(), w.admin, project, targetIn)
	if err != nil {
		t.Fatal(err)
	}
	const count = 6
	var wg sync.WaitGroup
	results := make(chan CompatMessage, count)
	errs := make(chan error, count)
	for range count {
		wg.Go(func() { msg, e := m.commitMessage(t.Context(), w.sender, project, in); results <- msg; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for msg := range results {
		if id != "" && id != msg.ID {
			t.Fatal("duplicate concurrent send")
		}
		id = msg.ID
	}
	targetIn.Ref = "fixture-new-thread"
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetIn); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + project + "/messages/listen?to=codex:worker"
	status, data := do(t, srv, w.agent.ID, "GET", path, "", nil)
	if status != 200 {
		t.Fatalf("address listen %d %s", status, data)
	}
	page := mustJSON[compatPage](t, data)
	if len(page.Items) != 1 || page.Preamble == "" || page.Items[0].ID != id {
		t.Fatal("address listen incorrect")
	}
	status, _ = do(t, srv, w.sender.ID, "GET", path, "", nil)
	if status != 403 {
		t.Fatalf("impersonation %d", status)
	}
	status, _ = compatPost(t, srv, w.agent, "/api/projects/"+project+"/messages", compatInput(w.sender.ID, "no-scope"))
	if status != 403 {
		t.Fatalf("unscoped agent send %d", status)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		var targetID string
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT target_id::text FROM inbox_message_deliveries WHERE message_id=$1::uuid`, id).Scan(&targetID); err != nil {
			return err
		}
		if targetID != firstTarget.ID {
			return errors.New("delivery retargeted")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_reply_obligations WHERE message_id=$1::uuid`, id).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return errors.New("duplicate obligation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate names cannot acquire an address merely by supplying a name.
	insertPrincipal(t, w.db, w.sender.TenantID, tenant.Agent, "worker", nil)
	if _, err := m.commitMessage(t.Context(), w.sender, project, compatInput("pi:worker", "ambiguous")); !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, errNotFound) {
		t.Fatal("ambiguous address accepted", err)
	}
}
