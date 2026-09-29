// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestLocalPiSetupWithServerSelectedProfile(t *testing.T) {
	f := newFixture(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'pi-anthropic','1','pi','anthropic','anthropic/test-model','high','fast')`, f.tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	home := physicalSetupTemp(t)
	store, err := agentsetup.OpenStore(filepath.Join(home, "pairing-state"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := setupAccount(t, f, "pi", home)
	candidate.ProfileID, candidate.Provider, candidate.Identity = "", "anthropic", "anthropic"
	now := time.Now().UTC()
	e := &agentsetup.Engine{
		Store:    store,
		API:      agentsetup.HTTPClient{Origin: origin, HTTP: &http.Client{Transport: handlerTransport{f.h}}},
		Local:    &localSetupDaemon{root: store.Path(), active: map[string]bool{}, fenced: map[string]bool{}},
		Services: &agentsetup.ServiceManager{Platform: agentsetup.Platform{OS: "darwin", Arch: "arm64"}, Home: home, UID: os.Getuid(), Executable: candidate.Path, Executor: localSetupExecutor{candidate.Path}},
		Now:      func() time.Time { return now },
	}
	p, err := e.Begin(t.Context(), agentsetup.Options{Origin: origin, TenantID: f.tenantID, ComputerName: "pi fixture", Workspace: physicalSetupTemp(t), Platform: agentsetup.Platform{OS: "darwin", Arch: "arm64"}, Candidates: []agentsetup.Candidate{candidate}})
	if err != nil || p.Stage != "awaiting_approval" {
		t.Fatal("pi device creation failed", err)
	}
	now = now.Add(time.Minute)
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal("server default could not be pinned", err)
	}
	var review agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": p.UserCode}, true, "", 200), &review)
	f.call("POST", "/api/agent-pairing/requests/"+p.RequestID+"/approve", map[string]any{"request_digest": review.Digest, "verification": "connect_only", "selected_account_keys": []string{review.Requested[0].AccountKey}}, true, "", 200)
	now = now.Add(time.Minute)
	if p, err = e.Step(t.Context()); err != nil || p.Stage != "connected" {
		t.Fatal("pi redemption failed", err)
	}
	c, err := agentsetup.ReadRuntimeConfig(store.Path())
	if err != nil || len(c.Accounts) != 1 || c.Accounts[0].Harness != "pi" || c.Accounts[0].Identity != "anthropic" || c.Accounts[0].Home != home {
		t.Fatal("runtime pi binding lost", err)
	}
}

func TestPiPairingConnectOnly(t *testing.T) {
	f := newFixture(t)
	p := f.propose("pi")
	c := p.review.VerificationCapabilities["pi"]
	if c.Supported || c.Policy != "unavailable" || c.Reason == "" {
		t.Fatal("pi verification advertised incorrectly")
	}
	f.call("POST", "/api/agent-pairing/requests/"+p.id+"/approve", map[string]any{"request_digest": p.review.Digest, "verification": "one_per_harness", "selected_account_keys": []string{"pi-local"}}, true, "", 409)
	f.approve(p, "connect_only")
	v := f.redeem(p)
	if len(v.Enrollments) != 1 || v.Enrollments[0].Harness != "pi" || v.Enrollments[0].VerificationRunID != nil || v.Enrollments[0].VerificationState != "not_selected" {
		t.Fatal("pi enrollment did not remain connect-only")
	}
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	f.probe(v, v.Enrollments[0], key, 200)
	var harness, label string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT harness,label FROM agent_accounts WHERE id=$1`, v.Enrollments[0].AccountID).Scan(&harness, &label); err != nil || harness != "pi" || label != "pi personal test" {
		t.Fatal("pi account mapping failed", err)
	}
}

func TestPiPairingSelectsOnlyTheConfiguredProvider(t *testing.T) {
	f := newFixture(t)
	p := f.propose("pi")
	// The prior default profile has no matching provider. A configured account
	// must select the matching profile even when another provider is newer.
	var matching string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'pi-anthropic','1','pi','anthropic','anthropic/test-model','high','fast') RETURNING id::text`, f.tenantID).Scan(&matching); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'pi-openai','1','pi','openai','openai/test-model','high','fast')`, f.tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p.id = uuid(t, f.db)
	p.request["request_id"] = p.id
	p.device = nonce()
	p.request["device_hash"] = hash(p.device)
	p.request["accounts"] = []map[string]string{{"account_key": "pi-local", "harness": "pi", "label": "pi / anthropic (local profile)", "provider": "anthropic"}}
	f.submit(p)
	if len(p.review.Requested) != 1 || p.review.Requested[0].ProfileID != matching || p.review.Requested[0].Provider != "anthropic" {
		t.Fatal("provider model binding lost")
	}
	f.approve(p, "connect_only")
	v := f.redeem(p)
	if len(v.Enrollments) != 1 || v.Enrollments[0].ProfileID != matching {
		t.Fatal("wrong profile enrolled")
	}
	p.id = uuid(t, f.db)
	p.request["request_id"] = p.id
	p.request["accounts"] = []map[string]string{{"account_key": "pi-local", "harness": "pi", "label": "pi / anthropic (local profile)", "provider": "anthropic", "model_profile_id": f.profiles["pi"]}}
	f.call("POST", "/api/agent-pairing/device", p.request, false, "", 400)
}

func TestAllFiveGuidedHarnesses(t *testing.T) {
	f := newFixture(t)
	p := f.propose("codex", "claude", "cursor", "grok", "pi")
	f.approve(p, "connect_only")
	if v := f.redeem(p); len(v.Enrollments) != 5 {
		t.Fatal("fifth harness was not enrolled")
	}
	// The guided command is now `pair --url` and offers every signed-in
	// harness; the guide names pi through its verification capability.
	var guide struct {
		Capabilities map[string]json.RawMessage `json:"verification_capabilities"`
	}
	decodeResult(t, f.call("GET", "/api/agent-pairing/guide", nil, false, "", 200), &guide)
	if _, ok := guide.Capabilities["pi"]; !ok {
		t.Fatal("guide omits pi")
	}
	var v agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/lookup", map[string]string{"user_code": p.code}, true, "", 200), &v)
	if _, ok := v.VerificationCapabilities["pi"]; !ok {
		t.Fatal("review omits pi capability")
	}
}
