// SPDX-License-Identifier: AGPL-3.0-only

package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/embedding"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	d                           *dbtest.DB
	s                           *Service
	mux                         *http.ServeMux
	admin, member, agent, other tenant.Principal
}

func setup(t *testing.T) fixture {
	f := fixture{d: dbtest.Open(t), mux: http.NewServeMux()}
	f.admin = tenant.Principal{TenantID: "11111111-1111-4111-8111-111111111111", ID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}
	f.member = tenant.Principal{TenantID: f.admin.TenantID, ID: "33333333-3333-4333-8333-333333333333", Kind: tenant.Person}
	f.agent = tenant.Principal{TenantID: f.admin.TenantID, ID: "44444444-4444-4444-8444-444444444444", Kind: tenant.Agent, Scopes: []string{"settings.manage"}}
	f.other = tenant.Principal{TenantID: "55555555-5555-4555-8555-555555555555", ID: "66666666-6666-4666-8666-666666666666", Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.admin, f.other} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Models')`, p.TenantID, "models-"+p.TenantID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.admin, f.member, f.agent, f.other} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,'Models')`, p.TenantID, p.ID, p.Kind)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, f.d, p.TenantID, p.ID, map[bool]string{true: "member", false: "admin"}[p.ID == f.member.ID])
	}
	f.s = New(f.d.App, bytes.Repeat([]byte{9}, 32))
	f.s.Mount(f.mux)
	return f
}

func (f fixture) call(p tenant.Principal, method, path string, body any) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(context.Background(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}

// Use a map: Secret's JSON marshaller deliberately masks writes during logging.
func writeSettings(s Settings, revision int64, key *string) map[string]any {
	out := map[string]any{"enabled": s.Enabled, "base_url": s.BaseURL, "chat_model": s.ChatModel, "embedding_model": s.EmbeddingModel, "features": s.Features, "expected_revision": revision}
	if key != nil {
		out["api_key"] = *key
	}
	return out
}

func configOf(t *testing.T, w *httptest.ResponseRecorder) Config {
	t.Helper()
	var c Config
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSettingsOwnershipVaultAndRevision(t *testing.T) {
	f := setup(t)
	w := f.call(f.admin, "GET", "/api/settings/model-provider", nil)
	expect(t, w, 200)
	c := configOf(t, w)
	if c.Enabled || c.Features.CRMNoteRewrite || c.Features.Embeddings || c.Revision != 0 || c.HasAPIKey {
		t.Fatal("fresh workspace models enabled")
	}
	for _, p := range []tenant.Principal{f.member, f.agent} {
		for _, method := range []string{"GET", "PUT", "POST"} {
			path := "/api/settings/model-provider"
			if method == "POST" {
				path += "/test"
			}
			expect(t, f.call(p, method, path, map[string]any{"expected_revision": 0}), 403)
		}
	}
	expect(t, f.call(tenant.Principal{}, "GET", "/api/settings/model-provider", nil), 401)
	s := Settings{BaseURL: "http://localhost:11434/v1", ChatModel: "fixture-chat"}
	key := "test-only-provider-value"
	w = f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 0, &key))
	expect(t, w, 200)
	c = configOf(t, w)
	if !c.HasAPIKey || c.Revision != 1 || strings.Contains(w.Body.String(), key) {
		t.Fatal("credential response disclosed or lost key")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.admin.TenantID, func(tx pgx.Tx) error {
		var raw string
		var sealed []byte
		if err := tx.QueryRow(t.Context(), `SELECT settings::text,credential FROM workspace_model_provider`).Scan(&raw, &sealed); err != nil {
			return err
		}
		if strings.Contains(raw, key) || bytes.Contains(sealed, []byte(key)) {
			t.Fatal("key stored cleartext")
		}
		if _, err := linkvault.Decrypt(f.s.key, f.other.TenantID, credentialID, sealed); err == nil {
			t.Fatal("credential crossed tenant boundary")
		}
		var eventsRaw string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(after::text,''),'') FROM events WHERE type='tenant.model_provider_updated'`).Scan(&eventsRaw); err != nil {
			return err
		}
		if strings.Contains(eventsRaw, key) || strings.Contains(eventsRaw, s.BaseURL) {
			t.Fatal("credential or endpoint entered events")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	foreign := configOf(t, f.call(f.other, "GET", "/api/settings/model-provider", nil))
	if foreign.ProviderID != "" || foreign.HasAPIKey {
		t.Fatal("provider crossed tenant boundary")
	}
	expect(t, f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 0, nil)), 409)
	w = f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 1, nil))
	expect(t, w, 200)
	if !configOf(t, w).HasAPIKey {
		t.Fatal("omitted key was not retained")
	}
	s.BaseURL = "http://localhost:11435/v1"
	w = f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 2, nil))
	expect(t, w, 200)
	if configOf(t, w).HasAPIKey {
		t.Fatal("old key retained for different endpoint")
	}
	expect(t, f.call(f.admin, "PUT", "/api/settings/model-provider", map[string]any{}), 400)
}

func TestDisabledFeaturesMakeNoRequestsAndConnectionTestIsExplicit(t *testing.T) {
	f := setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer srv.Close()
	if _, err := f.s.Chat(t.Context(), f.admin.TenantID, "", 0, nil); err != ErrDisabled {
		t.Fatal("unconfigured chat allowed")
	}
	if p, err := f.s.Embeddings(t.Context(), f.admin.TenantID); err != nil || p != nil {
		t.Fatal("unconfigured embeddings allowed")
	}
	expect(t, f.call(f.admin, "POST", "/api/settings/model-provider/test", map[string]any{"expected_revision": 0}), 409)
	s := Settings{BaseURL: srv.URL + "/v1", ChatModel: "fixture-chat", Features: Features{CRMNoteRewrite: true}}
	w := f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 0, nil))
	expect(t, w, 200)
	c := configOf(t, w)
	if _, err := f.s.Chat(t.Context(), f.admin.TenantID, c.ProviderID, c.Revision, nil); err != ErrDisabled {
		t.Fatal("disabled provider allowed chat")
	}
	if p, err := f.s.Embeddings(t.Context(), f.admin.TenantID); err != nil || p != nil {
		t.Fatal("disabled provider allowed embeddings")
	}
	if calls != 0 {
		t.Fatal("ordinary config read/write made a request")
	}
	expect(t, f.call(f.admin, "POST", "/api/settings/model-provider/test", map[string]any{"expected_revision": c.Revision + 1}), 409)
	expect(t, f.call(f.admin, "POST", "/api/settings/model-provider/test", map[string]any{"expected_revision": c.Revision}), 200)
	if calls != 1 {
		t.Fatal("connection test did not make exactly one request")
	}
	s.Enabled = true
	s.Features.CRMNoteRewrite = false
	w = f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, c.Revision, nil))
	expect(t, w, 200)
	c = configOf(t, w)
	if _, err := f.s.Chat(t.Context(), f.admin.TenantID, c.ProviderID, c.Revision, nil); err != ErrDisabled {
		t.Fatal("unselected feature allowed chat")
	}
	worker := embedding.NewWorker(f.d.App, nil, embedding.Options{Resolve: f.s.Embeddings})
	if n, err := worker.ProcessOnce(t.Context()); err != nil || n != 0 {
		t.Fatal("disabled index queue was consumed")
	}
	if calls != 1 {
		t.Fatal("feature gates contacted endpoint")
	}
}

func TestWorkspaceEmbeddingsUseSameEndpointAndKey(t *testing.T) {
	f := setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-only-provider-value" {
			t.Error("wrong embedding endpoint or key")
		}
		var in struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Model != "fixture-embed" || len(in.Input) != 1 {
			t.Error("wrong embedding request")
		}
		vec := make([]float32, embedding.Dimensions)
		vec[0] = 1
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vec}}})
	}))
	defer srv.Close()
	s := Settings{Enabled: true, BaseURL: srv.URL + "/v1", ChatModel: "fixture-chat", EmbeddingModel: "fixture-embed", Features: Features{Embeddings: true}}
	key := "test-only-provider-value"
	w := f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 0, &key))
	expect(t, w, 200)
	p, err := f.s.Embeddings(t.Context(), f.admin.TenantID)
	if err != nil || p == nil {
		t.Fatal("embeddings unavailable")
	}
	vecs, err := p.Embed(t.Context(), []string{"hello"})
	if err != nil || len(vecs) != 1 || len(vecs[0]) != embedding.Dimensions || calls != 1 {
		t.Fatal("compatible embeddings failed")
	}
	s.EmbeddingModel = "other-embed"
	w = f.call(f.admin, "PUT", "/api/settings/model-provider", writeSettings(s, 1, nil))
	expect(t, w, 200)
	other, err := f.s.Embeddings(t.Context(), f.admin.TenantID)
	if err != nil || other.Model() == p.Model() {
		t.Fatal("changed embedding model reused vector identity")
	}
	if p, err := f.s.Embeddings(t.Context(), f.other.TenantID); err != nil || p != nil {
		t.Fatal("tenant provider leaked")
	}
}
