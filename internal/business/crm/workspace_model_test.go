// SPDX-License-Identifier: AGPL-3.0-only

package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestWorkspaceModelRewritesCustomerNoteEndToEnd(t *testing.T) {
	f := setup(t)
	models := modelprovider.New(f.db.App, bytes.Repeat([]byte{8}, 32))
	installNoteFake(t, &f, WorkspaceNotes{Provider: models})
	enableNoteTool(t, f)
	w := jsonRequest(t, f, f.admin, "POST", "/api/crm/organisations", map[string]any{"name": "Client", "customer_notes": "Invoices go to accounting."})
	expect(t, w, 201)
	var c Customer
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	path := "/api/crm/organisations/" + c.ID + "/note-ai"
	calls := 0
	var change func()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-only-provider-value" {
			t.Error("wrong model endpoint or credential")
		}
		var in struct {
			Model    string                  `json:"model"`
			Messages []modelprovider.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Model != "local-fixture" || len(in.Messages) != 2 || !strings.Contains(in.Messages[1].Content, c.CustomerNotes) {
			t.Error("wrong model context")
		}
		if change != nil {
			change()
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Send invoices to the accounting team."},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":10}}`)
	}))
	defer srv.Close()
	// Even an enrolled scout route does not substitute for workspace opt-in.
	routeNoteModel(t, f)
	expect(t, jsonRequest(t, f, f.admin, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision}), 409)
	key := modelprovider.Secret("test-only-provider-value")
	s := modelprovider.Settings{Enabled: true, BaseURL: srv.URL + "/v1", ChatModel: "local-fixture"}
	ctx := tenant.WithPrincipal(context.Background(), f.admin)
	conf, err := models.Save(ctx, f.admin, modelprovider.Write{Settings: s, ExpectedRevision: 0, APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, jsonRequest(t, f, f.admin, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision}), 409)
	if calls != 0 {
		t.Fatal("workspace/feature default contacted a model")
	}
	s.Features.CRMNoteRewrite = true
	conf, err = models.Save(ctx, f.admin, modelprovider.Write{Settings: s, ExpectedRevision: conf.Revision})
	if err != nil {
		t.Fatal(err)
	}
	w = request(f.handler, f.admin, "GET", path, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal("workspace model not enabled")
	}
	w = jsonRequest(t, f, f.admin, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision})
	expect(t, w, 201)
	var draft struct {
		ID        string `json:"id"`
		DraftText string `json:"draft_text"`
		Applied   bool   `json:"applied"`
	}
	if json.Unmarshal(w.Body.Bytes(), &draft) != nil || draft.ID == "" || draft.Applied || calls != 1 {
		t.Fatal("model did not create exactly one unapplied draft")
	}
	w = request(f.handler, f.admin, "GET", "/api/crm/organisations/"+c.ID, "")
	expect(t, w, 200)
	var current Customer
	if json.Unmarshal(w.Body.Bytes(), &current) != nil || current.CustomerNotes != c.CustomerNotes {
		t.Fatal("model changed original notes")
	}
	found := false
	for _, ev := range logEvents(t, f) {
		if ev.Type == "crm.note_rewrite_drafted" {
			found = true
			if !strings.Contains(string(ev.After), "workspace_provider_id") || strings.Contains(string(ev.After), draft.DraftText) || strings.Contains(string(ev.After), string(key)) {
				t.Fatal("invalid model audit evidence")
			}
		}
	}
	if !found {
		t.Fatal("model draft audit missing")
	}
	// An admin disabling the feature during generation invalidates the result.
	change = func() {
		s.Features.CRMNoteRewrite = false
		var err error
		conf, err = models.Save(ctx, f.admin, modelprovider.Write{Settings: s, ExpectedRevision: conf.Revision})
		if err != nil {
			t.Error(err)
		}
	}
	expect(t, jsonRequest(t, f, f.admin, "POST", path+"/generate", map[string]any{"expected_revision": c.Revision}), 409)
	if count(t, f, f.admin.TenantID, `SELECT count(*) FROM crm_note_rewrite_drafts`) != 1 {
		t.Fatal("stale provider result created a draft")
	}
	expect(t, request(f.handler, f.admin, "POST", "/api/crm/organisations/"+c.ID+"/note-rewrite/"+draft.ID+"/apply", ""), 200)
}
