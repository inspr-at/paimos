// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
)

type fakeNoteGenerator func(context.Context, modelregistry.Profile, NotePrompt) (NoteGeneration, error)

func (f fakeNoteGenerator) GenerateNote(ctx context.Context, p modelregistry.Profile, prompt NotePrompt) (NoteGeneration, error) {
	return f(ctx, p, prompt)
}

func installNoteFake(t *testing.T, f *fixture, generator NoteGenerator) {
	t.Helper()
	plug, err := PluginWithNoteGenerator(generator)
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err = reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	f.handler = (&httpapi.Server{Pool: f.db.App, Modules: []httpapi.Module{NewWithNoteGenerator(f.db.App, reg, generator), events.New(f.db.App)}}).Handler()
}

func enableNoteTool(t *testing.T, f fixture) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plugin_installations SET permissions=array_append(permissions,$1) WHERE plugin_id=$2`, fence.PermToolsInvoke, ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func routeNoteModel(t *testing.T, f fixture) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
			VALUES($1::uuid,'fake-scout','1','codex','openai','fake-model','medium','fast') RETURNING id::text`, f.admin.TenantID).Scan(&id); err != nil {
			return err
		}
		// Quick jobs needs a healthy account with room; a legacy ladder alone
		// must never enable generation after the migration.
		var runner, account string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Note runner') RETURNING id::text`, f.admin.TenantID).Scan(&runner); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, f.admin.TenantID, runner); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(t.Context(), `SELECT now()`).Scan(&now); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,state,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
		 VALUES($1,'note-account','codex','note-host',$2,'Note account','available',$3,true,'note-generation',$4) RETURNING id::text`, f.admin.TenantID, runner, now, f.admin.ID).Scan(&account); err != nil {
			return err
		}
		until := now.Add(time.Hour)
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Override, schedule.OverrideUntil, schedule.Reserve = "sprint", &until, capacity.ReserveOff
		raw, err := json.Marshal(schedule)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, f.admin.TenantID, f.admin.ID, account, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAINoteRequiresToolGrantAndModelAndNeverApplies(t *testing.T) {
	f := setup(t)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Client", "customer_notes": "Invoices go to accounting."})
	expect(t, w, 201)
	var c Customer
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	calls := 0
	installNoteFake(t, &f, fakeNoteGenerator(func(_ context.Context, p modelregistry.Profile, prompt NotePrompt) (NoteGeneration, error) {
		calls++
		if p.ID == "" || prompt.CustomerName != "Client" || prompt.CurrentNotes != c.CustomerNotes || prompt.Instruction == "" {
			t.Fatalf("unexpected model/prompt: %+v %+v", p, prompt)
		}
		return NoteGeneration{Text: "Send invoices to the accounting team.", InputTokens: 20, OutputTokens: 10, CostMicros: 30}, nil
	}))
	path := "/api/crm/organisations/" + c.ID + "/note-ai"
	w = request(f.handler, f.admin, "GET", path, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Body.String())
	}
	enableNoteTool(t, f)
	w = request(f.handler, f.admin, "GET", path, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "No model is configured") {
		t.Fatal(w.Body.String())
	}
	profileID := routeNoteModel(t, f)
	w = request(f.handler, f.admin, "GET", path, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal(w.Body.String())
	}
	member := f.admin
	member.ID = f.member
	member.Roles = []string{"member"}
	expect(t, request(f.handler, member, "GET", path, ""), 403)
	expect(t, jsonRequest(t, f, member, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision}), 403)
	expect(t, jsonRequest(t, f, f.admin, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision + 1}), 409)
	w = jsonRequest(t, f, f.admin, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision})
	expect(t, w, 201)
	var draft struct {
		ID        string `json:"id"`
		DraftText string `json:"draft_text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.ID == "" || draft.DraftText == c.CustomerNotes || calls != 1 {
		t.Fatalf("draft %+v calls %d", draft, calls)
	}
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	var unchanged Customer
	if err := json.Unmarshal(w.Body.Bytes(), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.CustomerNotes != c.CustomerNotes {
		t.Fatal("generation applied notes")
	}
	var found bool
	for _, ev := range logEvents(t, f) {
		if ev.Type == "crm.note_rewrite_drafted" && strings.Contains(string(ev.After), profileID) {
			found = true
			if strings.Contains(string(ev.After), draft.DraftText) {
				t.Fatal("note text leaked into event")
			}
		}
	}
	if !found {
		t.Fatal("AI draft event missing model evidence")
	}
	w = request(f.handler, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/note-rewrite/"+draft.ID+"/apply", "")
	expect(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), draft.DraftText) {
		t.Fatal("human apply did not save proposal")
	}
}

func TestAINoteRejectsRevisionRaceAfterGeneration(t *testing.T) {
	f := setup(t)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Client", "customer_notes": "Old notes."})
	expect(t, w, 201)
	var c Customer
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	installNoteFake(t, &f, fakeNoteGenerator(func(ctx context.Context, _ modelregistry.Profile, _ NotePrompt) (NoteGeneration, error) {
		err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE nodes SET fields=jsonb_set(fields,'{customer_notes}',to_jsonb('Changed meanwhile'::text),true) WHERE id=$1::uuid`, c.ID)
			return err
		})
		if err != nil {
			return NoteGeneration{}, err
		}
		return NoteGeneration{Text: "Generated notes."}, nil
	}))
	enableNoteTool(t, f)
	routeNoteModel(t, f)
	before := len(logEvents(t, f))
	w = jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/note-ai/generate", map[string]any{"expected_revision": c.Revision})
	expect(t, w, 409)
	if len(logEvents(t, f)) != before {
		t.Fatal("stale generation wrote an event")
	}
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_note_rewrite_drafts`) != 0 {
		t.Fatal("stale generation wrote a draft")
	}
}
