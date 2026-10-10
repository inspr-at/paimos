// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func (f *fixture) sourceEvent(node, typ, beforeState, afterState string, fields map[string]any) int64 {
	f.t.Helper()
	var id int64
	f.tx(func(tx pgx.Tx) error {
		var snapshot []byte
		if err := tx.QueryRow(f.t.Context(), `SELECT to_jsonb(n)-'tenant_id' FROM nodes n WHERE id=$1`, node).Scan(&snapshot); err != nil {
			return err
		}
		var before, after map[string]any
		if err := json.Unmarshal(snapshot, &before); err != nil {
			return err
		}
		if err := json.Unmarshal(snapshot, &after); err != nil {
			return err
		}
		before["state"], after["state"], after["fields"] = beforeState, afterState, fields
		e, err := events.Append(f.t.Context(), tx, f.p, events.Change{NodeID: &node, Type: typ, Before: before, After: after, At: &f.now})
		id = e.ID
		return err
	})
	return id
}

func (f *fixture) assertContextAndNoRuns(r Recurrence, count int, identifiers ...string) {
	f.t.Helper()
	got := f.receipts(r.ID)
	if len(got) != count {
		f.t.Fatalf("receipts: %+v; want %d", got, count)
	}
	f.tx(func(tx pgx.Tx) error {
		var bodies string
		var runs int
		if err := tx.QueryRow(f.t.Context(), `SELECT coalesce(string_agg(n.body,'\n'),'') FROM nodes n JOIN recurrence_occurrences o ON o.node_id=n.id WHERE o.recurrence_id=$1`, r.ID).Scan(&bodies); err != nil {
			return err
		}
		for _, value := range identifiers {
			if !strings.Contains(bodies, value) {
				f.t.Fatalf("missing source identifier %q: %s", value, bodies)
			}
		}
		if err := tx.QueryRow(f.t.Context(), `SELECT count(*) FROM agent_runs`).Scan(&runs); err != nil {
			return err
		}
		if runs != 0 {
			f.t.Fatalf("event ran an agent: %d", runs)
		}
		return nil
	})
}

// Risk: ordinary ticket edits consume the budget before a relevant source event.
func TestEventCandidatesExcludeUnrelatedProjectEdits(t *testing.T) {
	for _, event := range []string{"node.done", "knowledge.changed"} {
		t.Run(event, func(t *testing.T) {
			f := setup(t)
			work := f.node("work", &f.project, "Busy ticket")
			source := work
			if event == "knowledge.changed" {
				f.tx(func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'guideline','Guideline','GUI','book') ON CONFLICT DO NOTHING`, f.p.TenantID)
					return err
				})
				source = f.node("guideline", &f.project, "Product guidance")
			}
			in := f.input()
			in.Trigger = Trigger{Kind: "event", Event: event}
			r := f.create(in)
			f.now = f.now.Add(time.Minute)
			for i := 0; i <= batchSize; i++ {
				f.sourceEvent(work, "node.updated", "open", "open", map[string]any{})
			}
			id := f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
			f.run()
			got := f.receipts(r.ID)
			if len(got) != 1 || got[0].SourceEventID == nil || *got[0].SourceEventID != id {
				t.Fatalf("ordinary edits delayed the relevant event in one pass: %+v", got)
			}
		})
	}
}

// Risk: filtered transitions need one transaction each, or a claim scans without a bound.
func TestSourceClaimSkipsFilteredEventsInBoundedBatches(t *testing.T) {
	f := setup(t)
	source := f.node("work", &f.project, "Feature moving through states")
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "node.done"}
	r := f.create(in)
	f.now = f.now.Add(time.Minute)
	var first int64
	for i := 0; i < 125; i++ {
		id := f.sourceEvent(source, "node.updated", "open", "in_progress", map[string]any{})
		if i == 0 {
			first = id
		}
	}
	wanted := f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
	next := f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
	f.now = f.now.Add(time.Hour)
	future := f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
	f.now = f.now.Add(-time.Hour)
	actor, err := ensureActor(t.Context(), f.m, f.p.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	claim := func() []string {
		t.Helper()
		current := f.get(r.ID)
		deferred := []string{}
		f.tx(func(tx pgx.Tx) error {
			if err := db.SetLocalStatementTimeout(t.Context(), tx, 5*time.Second); err != nil {
				return err
			}
			if _, err := lock(t.Context(), tx, f.p.TenantID, false); err != nil {
				return err
			}
			return f.m.consumeSource(t.Context(), tx, actor, current, f.now, &deferred)
		})
		return deferred
	}
	claim()
	if cursor := f.get(r.ID).EventCursor; cursor <= first || cursor >= wanted {
		t.Fatalf("claim did not skip a bounded batch: cursor %d, first %d, relevant %d", cursor, first, wanted)
	}
	f.assertContextAndNoRuns(r, 0)
	claim()
	got := f.receipts(r.ID)
	if len(got) != 1 || got[0].SourceEventID == nil || *got[0].SourceEventID != wanted || f.get(r.ID).EventCursor != wanted {
		t.Fatalf("second claim failed to reach the relevant event: %+v", got)
	}
	claim()
	got = f.receipts(r.ID)
	if len(got) != 2 || got[1].SourceEventID == nil || *got[1].SourceEventID != next || f.get(r.ID).EventCursor != next {
		t.Fatalf("claim skipped the next matching event: %+v", got)
	}
	if deferred := claim(); len(deferred) != 1 || deferred[0] != r.ID || f.get(r.ID).EventCursor != next {
		t.Fatalf("future event was consumed instead of deferred: %v", deferred)
	}
	f.assertContextAndNoRuns(r, 2, source)
	f.now = f.now.Add(time.Hour)
	f.run()
	got = f.receipts(r.ID)
	if len(got) != 3 || got[2].SourceEventID == nil || *got[2].SourceEventID != future {
		t.Fatalf("deferred event was lost: %+v", got)
	}
	f.run()
	f.assertContextAndNoRuns(r, 3, source)
}

// Risk: a lower-ID recurrence's matching backlog stalls legacy release triggers.
func TestEventRecurrenceBacklogDoesNotStarveRelease(t *testing.T) {
	f := setup(t)
	source := f.node("work", &f.project, "Busy feature")
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "node.done"}
	busy, release := f.create(in), f.create(in)
	if busy.ID > release.ID {
		busy, release = release, busy
	}
	in.Trigger = Trigger{Kind: "event", Event: "release.published"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["expected_revision"] = release.Revision
	f.call(f.p, "PUT", "/api/recurrences/"+release.ID, body, 200)
	f.now = f.now.Add(time.Minute)
	for i := 0; i <= batchSize; i++ {
		f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
	}
	var publication int64
	f.tx(func(tx pgx.Tx) error {
		e, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &f.project, Type: "release.published", After: Publication{ProjectID: f.project, Name: "Fair delivery", Version: "v1", PublishedAt: f.now}, At: &f.now})
		publication = e.ID
		return err
	})
	f.run()
	got := f.receipts(release.ID)
	if len(got) != 1 || got[0].SourceEventID == nil || *got[0].SourceEventID != publication {
		t.Fatalf("backlog starved release delivery in the same tenant pass: %+v", got)
	}
	if count := len(f.receipts(busy.ID)); count == 0 || count+len(got) > batchSize {
		t.Fatalf("tenant claim budget was lost: busy %d, release %d", count, len(got))
	}
	f.run()
	f.assertContextAndNoRuns(release, 1, `"event":"release.published"`)
}

// Risk: status spelling, unrelated updates or replay create false/duplicate work.
func TestDoneEventCategoryFiltersOrderedDeliveryAndReplay(t *testing.T) {
	f := setup(t)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=field_schema||'{"states":[{"state":"shipped","category":"done"},{"state":"done","category":"doing"}]}'::jsonb WHERE slug='work'`)
		return err
	})
	other := f.node("project", nil, "Second source project")
	source := f.node("work", &other, "Landed feature")
	outside := f.node("work", &f.project, "Outside filter")
	in := f.input()
	in.OverlapPolicy = "create"
	in.Trigger = Trigger{Kind: "event", Event: "node.done", Filter: &EventFilter{ProjectIDs: []string{other}, HasReleaseCopy: true, ExcludeHidden: true}}
	r := f.create(in)
	f.now = f.now.Add(time.Minute)
	copy := map[string]any{"pill_en": "Feature landed", "pill_de": "Funktion verfügbar", "benefit_en": "Relevant benefit", "benefit_de": "Relevanter Nutzen", "hide_from_release_notes": false}
	f.sourceEvent(source, "node.updated", "open", "shipped", map[string]any{})
	hidden := map[string]any{}
	for k, v := range copy {
		hidden[k] = v
	}
	hidden["hide_from_release_notes"] = true
	f.sourceEvent(source, "node.updated", "open", "shipped", hidden)
	f.sourceEvent(outside, "node.updated", "open", "shipped", copy)
	f.sourceEvent(source, "node.updated", "shipped", "shipped", copy)
	f.sourceEvent(source, "node.updated", "open", "done", copy) // tenant maps done to Doing
	first := f.sourceEvent(source, "node.updated", "open", "shipped", copy)
	second := f.sourceEvent(source, "status_autopilot.changed", "open", "shipped", copy)
	f.run()
	f.run()
	got := f.receipts(r.ID)
	if len(got) != 2 || got[0].SourceEventID == nil || *got[0].SourceEventID != first || *got[1].SourceEventID != second {
		t.Fatalf("ordered source delivery: %+v", got)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrences SET event_cursor=$2 WHERE id=$1`, r.ID, first-1)
		return err
	})
	f.run()
	f.assertContextAndNoRuns(r, 2, source, `"event":"node.done"`, fmt.Sprint(first))
}

// Risk: knowledge type/tag/entry selectors leak unrelated content or repeat work.
func TestKnowledgeEventSelectorsContextAndReplay(t *testing.T) {
	f := setup(t)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'guideline','Guideline','GUI','book'),($1,'memory','Memory','MEM','book') ON CONFLICT DO NOTHING`, f.p.TenantID)
		return err
	})
	entry := f.node("guideline", &f.project, "Core messaging")
	another := f.node("guideline", &f.project, "Other guideline")
	memory := f.node("memory", &f.project, "Memory")
	in := f.input()
	in.OverlapPolicy = "create"
	in.Trigger = Trigger{Kind: "event", Event: "knowledge.changed", Filter: &EventFilter{EntryID: entry, KnowledgeType: "guideline", Tag: "website"}}
	r := f.create(in)
	in.Trigger.Filter = &EventFilter{KnowledgeType: "guideline", Tag: "website"}
	typeAndTag := f.create(in)
	in.Trigger.Filter = &EventFilter{EntryID: entry}
	entryOnly := f.create(in)
	f.now = f.now.Add(time.Minute)
	fields := map[string]any{"tags": []string{"website"}, "body": "do not copy source content"}
	f.sourceEvent(another, "knowledge.updated", "backlog", "backlog", fields)
	f.sourceEvent(memory, "knowledge.updated", "backlog", "backlog", fields)
	f.sourceEvent(entry, "knowledge.updated", "backlog", "backlog", map[string]any{"tags": []string{"other"}})
	f.sourceEvent(entry, "knowledge.updated", "backlog", "backlog", map[string]any{"tags": map[string]bool{"website": true}})
	first := f.sourceEvent(entry, "knowledge.created", "backlog", "backlog", fields)
	f.sourceEvent(entry, "knowledge.updated", "backlog", "cancelled", fields)
	f.run()
	f.run()
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrences SET event_cursor=$2 WHERE id=$1`, r.ID, first-1)
		return err
	})
	f.run()
	f.assertContextAndNoRuns(r, 2, entry, `"entry_id"`, `"event":"knowledge.changed"`)
	f.assertContextAndNoRuns(typeAndTag, 3, another, entry)
	f.assertContextAndNoRuns(entryOnly, 4, entry)
	f.tx(func(tx pgx.Tx) error {
		var leaked bool
		err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n JOIN recurrence_occurrences o ON o.node_id=n.id WHERE o.recurrence_id=$1 AND n.body LIKE '%do not copy source content%')`, r.ID).Scan(&leaked)
		if leaked {
			t.Fatal("source content copied")
		}
		return err
	})
}

// Risk: service visibility bypasses event references or a revoked source grant.
func TestSourceEventVisibilityAndLiveOwnerAuthorization(t *testing.T) {
	f := setup(t)
	source := f.node("work", &f.project, "Visible feature")
	hiddenProject := f.node("project", nil, "Hidden project")
	var personID string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project member') RETURNING id::text`, f.p.TenantID).Scan(&personID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='member'`, f.p.TenantID, personID, f.project)
		return err
	})
	member := tenant.Principal{ID: personID, TenantID: f.p.TenantID, Kind: tenant.Person}
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "node.done"}
	in.OverlapPolicy = "create"
	var r Recurrence
	if err := json.Unmarshal(f.call(member, "POST", "/api/recurrences", in, 201), &r); err != nil {
		t.Fatal(err)
	}
	in.Trigger.Filter = &EventFilter{ProjectIDs: []string{hiddenProject}}
	f.call(member, "POST", "/api/recurrences", in, 403)
	f.now = f.now.Add(time.Minute)
	f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{"dependency": hiddenProject})
	f.run()
	f.assertContextAndNoRuns(r, 0)
	f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
	f.run()
	f.assertContextAndNoRuns(r, 1, source)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1`, personID)
		return err
	})
	f.sourceEvent(source, "node.updated", "open", "accepted", map[string]any{})
	if err := f.m.RunTenant(t.Context(), f.p.TenantID); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("revoked owner error: %v", err)
	}
	f.assertContextAndNoRuns(r, 1)
}

func (f *fixture) sender() (tenant.Principal, ed25519.PrivateKey) {
	f.t.Helper()
	p := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Agent, Scopes: []string{Permission, "nodes.read"}, KeyCreatorID: f.p.ID}
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Event sender') RETURNING id::text`, f.p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'event_sender','Event sender') RETURNING id::text`, f.p.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(f.t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3),($1,$2,'nodes.read')`, f.p.TenantID, role, Permission); err != nil {
			return err
		}
		_, err := tx.Exec(f.t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.p.TenantID, p.ID, role, f.project)
		return err
	})
	// An ephemeral test authority; no operator key or configuration is read.
	return p, ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
}

func (f *fixture) signedCall(p tenant.Principal, key ed25519.PrivateKey, recurrence string, in ExternalEvent, change func(*http.Request), status int) []byte {
	f.t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		f.t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/recurrences/"+recurrence+"/events", strings.NewReader(string(raw)))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	req.Header.Set("X-Aeon-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(key, signatureMessage(p.TenantID, recurrence, raw))))
	if change != nil {
		change(req)
	}
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, req)
	if out.Code != status {
		f.t.Fatalf("signed intake: %d %s; want %d", out.Code, out.Body.String(), status)
	}
	return out.Body.Bytes()
}

// Risk: bad signatures, cross-recurrence replay, sender changes or retry create work.
func TestExternalEventSignatureOwnershipReplayAndNoAgentExecution(t *testing.T) {
	for _, event := range []string{"external.tag", "external.deploy"} {
		t.Run(event, func(t *testing.T) {
			f := setup(t)
			sender, key := f.sender()
			in := f.input()
			in.OverlapPolicy = "create"
			in.Trigger = Trigger{Kind: "event", Event: event, External: &ExternalSender{PrincipalID: sender.ID, PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}
			r := f.create(in)
			other := f.create(in)
			f.now = f.now.Add(time.Minute)
			e := ExternalEvent{DeliveryID: "delivery-123", Event: event, OccurredAt: f.now, Source: "product/repository", Ref: "v1.2.3"}
			f.signedCall(sender, key, r.ID, e, func(req *http.Request) { req.Header.Del("X-Aeon-Signature") }, 401)
			f.signedCall(sender, key, r.ID, e, func(req *http.Request) {
				req.Header.Set("X-Aeon-Signature", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)))
			}, 401)
			f.signedCall(sender, key, r.ID, e, func(req *http.Request) { req.URL.Path = "/api/recurrences/" + other.ID + "/events" }, 401)
			stale := e
			stale.OccurredAt = f.now.Add(-6 * time.Minute)
			f.signedCall(sender, key, r.ID, stale, nil, 401)
			future := e
			future.OccurredAt = f.now.Add(6 * time.Minute)
			f.signedCall(sender, key, r.ID, future, nil, 401)
			unscoped := sender
			unscoped.Scopes = nil
			f.signedCall(unscoped, key, r.ID, e, nil, 403)
			wrong := e
			wrong.Event = "external.unknown"
			f.signedCall(sender, key, r.ID, wrong, nil, 400)
			large := e
			large.Source = strings.Repeat("x", 9000)
			f.signedCall(sender, key, r.ID, large, nil, 400)
			hiddenSource := f.node("project", nil, "Hidden external source")
			hiddenPayload := e
			hiddenPayload.Source = hiddenSource
			f.signedCall(sender, key, r.ID, hiddenPayload, nil, 404)
			var first, retry externalReceipt
			if err := json.Unmarshal(f.signedCall(sender, key, r.ID, e, nil, 202), &first); err != nil {
				t.Fatal(err)
			}
			if first.EventID <= 0 || first.Duplicate {
				t.Fatalf("first receipt %+v", first)
			}
			f.assertContextAndNoRuns(r, 0) // intake never directly runs work
			if err := json.Unmarshal(f.signedCall(sender, key, r.ID, e, nil, 202), &retry); err != nil {
				t.Fatal(err)
			}
			if !retry.Duplicate || retry.EventID != first.EventID {
				t.Fatalf("replay %+v %+v", first, retry)
			}
			changed := e
			changed.Ref = "changed"
			f.signedCall(sender, key, r.ID, changed, nil, 409)
			f.run()
			f.run()
			f.assertContextAndNoRuns(r, 1, e.DeliveryID, e.Source, e.Ref)
			if got := f.receipts(r.ID); got[0].SourceEventID == nil || *got[0].SourceEventID != first.EventID {
				t.Fatalf("receipt lost intake identity: %+v", got)
			}
			// A template edit and a cursor replay retain the sender-owned delivery receipt.
			body := map[string]any{}
			raw, _ := json.Marshal(r.Input)
			_ = json.Unmarshal(raw, &body)
			body["expected_revision"] = r.Revision
			f.call(f.p, "PUT", "/api/recurrences/"+r.ID, body, 200)
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE recurrences SET event_cursor=0 WHERE id=$1`, r.ID)
				return err
			})
			f.run()
			f.assertContextAndNoRuns(r, 1)
			f.signedCall(sender, key, r.ID, e, nil, 202)
			// Revoke write authority while retaining project visibility: the denial
			// must prove the live write check, rather than an invisible-record 404.
			next := e
			next.DeliveryID = "delivery-456"
			f.signedCall(sender, key, r.ID, next, nil, 202)
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1`, sender.ID)
				return err
			})
			f.signedCall(sender, key, r.ID, next, nil, 403)
			if err := f.m.RunTenant(t.Context(), f.p.TenantID); err == nil || !strings.Contains(err.Error(), "permission") {
				t.Fatalf("revoked sender: %v", err)
			}
			f.assertContextAndNoRuns(r, 1)
		})
	}
}

// Risk: a foreign tenant event or known recurrence UUID crosses isolation.
func TestNewEventTriggersRejectForeignTenantSourcesAndSenders(t *testing.T) {
	f := setup(t)
	sender, key := f.sender()
	in := f.input()
	in.Trigger = Trigger{Kind: "event", Event: "node.done"}
	done := f.create(in)
	in.Trigger = Trigger{Kind: "event", Event: "knowledge.changed"}
	knowledge := f.create(in)
	in.Trigger = Trigger{Kind: "event", Event: "external.tag", External: &ExternalSender{PrincipalID: sender.ID, PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}
	external := f.create(in)
	foreign := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000001", Kind: tenant.Agent, Scopes: []string{Permission}}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign-trigger','Foreign trigger')`, foreign.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Foreign sender') RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
			return err
		}
		var node string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'WORK-1',id,'Foreign source' FROM node_kinds WHERE slug='work' RETURNING id::text`, foreign.TenantID).Scan(&node); err != nil {
			return err
		}
		for _, typ := range []string{"node.updated", "knowledge.updated", "recurrence.external_received"} {
			if _, err := events.Append(t.Context(), tx, foreign, events.Change{NodeID: &node, Type: typ, Before: map[string]string{"state": "open"}, After: map[string]string{"state": "done"}, At: &f.now}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	f.run()
	for _, r := range []Recurrence{done, knowledge, external} {
		f.assertContextAndNoRuns(r, 0)
	}
	e := ExternalEvent{DeliveryID: "foreign-123", Event: "external.tag", OccurredAt: f.now, Source: "foreign", Ref: "tag"}
	f.signedCall(foreign, key, external.ID, e, nil, 404)
}

// Risk: excessive or contradictory selectors are accepted as silent no-ops.
func TestNewEventTriggerValidationBounds(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	base := Input{ProjectID: "10000000-0000-4000-8000-000000000001", ParentID: "10000000-0000-4000-8000-000000000002", Template: Template{Title: "Check"}}
	for _, trigger := range []Trigger{
		{Kind: "event", Event: "release.published", Filter: &EventFilter{ProjectIDs: make([]string, 21)}},
		{Kind: "event", Event: "release.published", Filter: &EventFilter{ProjectIDs: []string{base.ProjectID, base.ProjectID}}},
		{Kind: "event", Event: "release.published", Filter: &EventFilter{Tag: "unsupported"}},
		{Kind: "event", Event: "release.published", Filter: &EventFilter{HasReleaseCopy: true}},
		{Kind: "event", Event: "node.done", Filter: &EventFilter{ProjectIDs: make([]string, 21)}},
		{Kind: "event", Event: "node.done", Filter: &EventFilter{ProjectIDs: []string{base.ProjectID, base.ProjectID}}},
		{Kind: "event", Event: "node.done", Filter: &EventFilter{EntryID: base.ParentID}},
		{Kind: "event", Event: "knowledge.changed", Filter: &EventFilter{HasReleaseCopy: true}},
		{Kind: "event", Event: "knowledge.changed", Filter: &EventFilter{KnowledgeType: "unknown"}},
		{Kind: "event", Event: "knowledge.changed", Filter: &EventFilter{Tag: strings.Repeat("x", 129)}},
		{Kind: "event", Event: "external.tag"},
		{Kind: "event", Event: "external.tag", External: &ExternalSender{PrincipalID: base.ProjectID, PublicKey: "invalid"}},
		{Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "09:00", Timezone: "UTC", Filter: &EventFilter{}},
	} {
		in := base
		in.Trigger = trigger
		if err := in.normalize(now); err == nil {
			t.Fatalf("accepted invalid trigger %+v", trigger)
		}
	}
}

// Risks: lost publications during downtime/overlap, duplicate automatic/manual
// effects, source leakage and revocation bypass. Use the real fenced occurrence
// and RLS paths; the injected S04 gate is qualification-fixture authority only.
func TestReleaseSubscriptionOrderedReplayAndSourceAuthority(t *testing.T) {
	t.Run("off by default and ordered publication retention", func(t *testing.T) {
		f := setup(t)
		source := f.node("project", nil, "Publication source")
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||'{"project_key":"SRC"}'::jsonb WHERE id=$1`, source)
			return err
		})
		in := intentInput(f)
		in.OverlapPolicy = "skip"
		in.Template.Title = "Review {{release_name}} {{release_version}}"
		in.Trigger = Trigger{Kind: "event", Event: "release.published", Filter: &EventFilter{ProjectIDs: []string{source}}}
		r := f.create(in)
		if !r.Paused {
			t.Fatal("saving a scoped subscription must not activate it")
		}
		f.tx(func(tx pgx.Tx) error {
			// Synthetic active fixture, never production enablement or consent.
			_, err := tx.Exec(t.Context(), `UPDATE recurrences SET paused=false WHERE id=$1`, r.ID)
			return err
		})
		first := Publication{ProjectKey: "SRC", ProjectID: source, Name: "First publication", Version: "v1", PublishedAt: f.now.Add(time.Hour)}
		second := Publication{ProjectKey: "SRC", ProjectID: source, Name: "Second publication", Version: "v2", PublishedAt: f.now.Add(2 * time.Hour)}
		f.now = f.now.Add(3 * time.Hour) // Both arrived during downtime.
		f.m.WithHistory([]Publication{second, first})
		f.run()
		if len(f.receipts(r.ID)) != 0 || f.get(r.ID).EventCursor != r.EventCursor {
			t.Fatal("missing project gate consumed a publication")
		}
		f.m.WithReleaseSubscriptionGate(func(ctx context.Context, tx pgx.Tx, project string) (bool, error) {
			var enabled bool
			err := tx.QueryRow(ctx, `SELECT id=$1 FROM nodes WHERE id=$1`, f.project).Scan(&enabled)
			if project != f.project {
				t.Fatalf("gate checked source rather than output project: %s", project)
			}
			return enabled, err
		})
		f.run()
		got := f.receipts(r.ID)
		if len(got) != 1 || got[0].Outcome != "created" || got[0].Key != "release:"+publicationKey(first) || got[0].SourceEventID == nil {
			t.Fatalf("first ordered publication: %+v", got)
		}
		cursor := f.get(r.ID).EventCursor
		path := "/api/recurrences/" + r.ID + "/run-now"
		f.call(f.p, "POST", path, map[string]string{"idempotency_key": "overlap", "release_key": publicationKey(second)}, 409)
		f.run()
		if len(f.receipts(r.ID)) != 1 || f.get(r.ID).EventCursor != cursor {
			t.Fatal("open overlap consumed a publication or wrote a skip receipt")
		}
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='accepted' WHERE id=$1`, got[0].NodeID)
			return err
		})
		f.run()
		if len(f.receipts(r.ID)) != 1 || f.get(r.ID).EventCursor != cursor {
			t.Fatal("closed ticket bypassed an unfinished routine run")
		}
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET state='completed' WHERE recurrence_id=$1`, r.ID)
			return err
		})
		// Manual-first recovery uses the same publication key as the scheduler.
		var manual Occurrence
		if err := json.Unmarshal(f.call(f.p, "POST", path, map[string]string{"idempotency_key": "recovered", "release_key": publicationKey(second)}, 200), &manual); err != nil {
			t.Fatal(err)
		}
		if manual.Run == nil || manual.Key != "release:"+publicationKey(second) || manual.SourceEventID == nil {
			t.Fatalf("manual publication lineage: %+v", manual)
		}
		// Restart and duplicate delivery cannot create a third ticket or intent.
		gate := f.m.releaseSubscriptionGate
		f.install(New(f.d.App).WithHistory([]Publication{first, second}).WithReleaseSubscriptionGate(gate))
		f.run()
		f.run()
		got = f.receipts(r.ID)
		if len(got) != 2 || got[1].Key != manual.Key || f.get(r.ID).EventCursor <= cursor {
			t.Fatalf("recovery skipped or duplicated a publication: %+v", got)
		}
		var retry Occurrence
		if err := json.Unmarshal(f.call(f.p, "POST", path, map[string]string{"idempotency_key": "retry", "release_key": publicationKey(first)}, 200), &retry); err != nil || retry.NodeID == nil || *retry.NodeID != *got[0].NodeID || retry.Run == nil {
			t.Fatalf("automatic-first replay: %+v, %v", retry, err)
		}
		f.tx(func(tx pgx.Tx) error {
			var correct bool
			err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM routine_runs WHERE recurrence_id=$1)=2
 AND (SELECT count(*) FROM routine_effect_outbox)=2 AND NOT EXISTS(SELECT 1 FROM routine_attempts)
 AND NOT EXISTS(SELECT 1 FROM routine_runs WHERE execute_consent)
 AND NOT EXISTS(SELECT 1 FROM nodes n JOIN recurrence_occurrences o ON o.node_id=n.id WHERE o.recurrence_id=$1
 AND (n.project_id<>$2 OR n.body NOT LIKE '%'||$3||'%'))`, r.ID, f.project, source).Scan(&correct)
			if err == nil && !correct {
				t.Fatal("run/source lineage changed or automatic execution started")
			}
			return err
		})
	})
	t.Run("source visibility revocation and bounded choices", func(t *testing.T) {
		f := setup(t)
		source := f.node("project", nil, "Private publication source")
		manager := projectPrincipal(f, "member")
		in := f.input()
		in.Trigger = Trigger{Kind: "event", Event: "release.published", Filter: &EventFilter{ProjectIDs: []string{source}}}
		f.call(manager, "POST", "/api/recurrences", in, 403)
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='viewer'`, f.p.TenantID, manager.ID, source)
			return err
		})
		var r Recurrence
		if err := json.Unmarshal(f.call(manager, "POST", "/api/recurrences", in, 201), &r); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Hour)
		var first int64
		f.tx(func(tx pgx.Tx) error {
			for i := 0; i < 102; i++ {
				e, err := events.Append(t.Context(), tx, f.p, events.Change{NodeID: &source, Type: "release.published",
					After: map[string]any{"project_id": source, "name": strings.Repeat("é", 300), "version": fmt.Sprintf("v%d", i), "published_at": f.now, "body": "private content must never be copied"}, At: &f.now})
				if err != nil {
					return err
				}
				if i == 0 {
					first = e.ID
				}
			}
			return nil
		})
		var page struct {
			Items     []ReleaseChoice `json:"items"`
			Truncated bool            `json:"truncated"`
		}
		path := "/api/recurrences/" + r.ID
		raw := f.call(manager, "GET", path+"/releases", nil, 200)
		if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 100 || !page.Truncated || len(page.Items[0].Name) > 256 || strings.Contains(string(raw), "private content") {
			t.Fatalf("bounded source choices: count=%d truncated=%v, %v", len(page.Items), page.Truncated, err)
		}
		f.call(projectPrincipal(f, "viewer"), "GET", path+"/releases", nil, 403)
		f.m.WithReleaseSubscriptionGate(func(context.Context, pgx.Tx, string) (bool, error) { return true, nil })
		f.run()
		got := f.receipts(r.ID)
		if len(got) != 1 || got[0].SourceEventID == nil || *got[0].SourceEventID != first {
			t.Fatalf("oldest source publication: %+v", got)
		}
		cursor := f.get(r.ID).EventCursor
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_id=$2`, manager.ID, source)
			return err
		})
		f.call(manager, "GET", path+"/releases", nil, 403)
		f.call(manager, "POST", path+"/run-now", map[string]string{"idempotency_key": "revoked", "release_key": page.Items[0].Key}, 403)
		if err := f.m.RunTenant(t.Context(), f.p.TenantID); err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("source revocation must refuse under the occurrence fence: %v", err)
		}
		if len(f.receipts(r.ID)) != 1 || f.get(r.ID).EventCursor != cursor {
			t.Fatal("revoked source changed receipts or consumed pending publications")
		}
		f.tx(func(tx pgx.Tx) error {
			var leaked bool
			err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n JOIN recurrence_occurrences o ON o.node_id=n.id WHERE o.recurrence_id=$1 AND n.body LIKE '%private content%')`, r.ID).Scan(&leaked)
			if leaked {
				t.Fatal("source content copied to target")
			}
			return err
		})
		// A real foreign project is retained, so tenant denial cannot pass only
		// because an arbitrary UUID does not exist anywhere.
		foreign := "20000000-0000-4000-8000-000000000001"
		var foreignProject string
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign-release','Foreign release')`, foreign); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'PRJ-1',id,'Foreign publication source' FROM node_kinds WHERE slug='project' RETURNING id::text`, foreign).Scan(&foreignProject)
		}); err != nil {
			t.Fatal(err)
		}
		in.Trigger.Filter.ProjectIDs = []string{foreignProject}
		f.call(f.p, "POST", "/api/recurrences", in, 404)
	})
}
