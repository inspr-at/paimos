// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type workstationFixture struct {
	m           *Module
	owner       tenant.Principal
	key, other  agentKeyCreatedJSON
	runtime     agentKeyCreatedJSON
	deviceProof string
	computer    string
	signer      *ecdsa.PrivateKey
	mux         *http.ServeMux
	serve       http.Handler
}

func workstationSigner(n int64) *ecdsa.PrivateKey {
	d := big.NewInt(n)
	x, y := elliptic.P256().ScalarBaseMult(d.Bytes())
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: d}
}

func newWorkstationFixture(t *testing.T) *workstationFixture {
	t.Helper()
	m, owner := keyFixture(t)
	scopes := []string{}
	for _, p := range authz.Registry {
		// The fixture switches to built-in Admin below. Recurrence automation
		// requires an explicit custom-role grant, even for workstation keys.
		if p.AgentGrantable && p.Key != "recurrences.manage" {
			scopes = append(scopes, p.Key)
		}
	}
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "Synthetic workstation", "scopes": scopes}))
	other := decodeKey(t, keyRequest(m, owner, map[string]any{"principal_id": key.PrincipalID, "name": "Unmarked sibling", "scopes": scopes}))
	f := &workstationFixture{m: m, owner: owner, key: key, other: other, runtime: key, deviceProof: strings.Repeat("b", 64), signer: workstationSigner(1), mux: http.NewServeMux()}
	ctx := t.Context()
	err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1`, key.PrincipalID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
		 VALUES($1,gen_random_uuid(),'123456789',$2,$2,$2,'{}','synthetic','redeemed') RETURNING id::text`, owner.TenantID, strings.Repeat("a", 64)).Scan(&f.computer); err != nil {
			return err
		}
		public := base64.StdEncoding.EncodeToString(elliptic.Marshal(f.signer.Curve, f.signer.X, f.signer.Y))
		proofHash := sha256.Sum256([]byte(f.deviceProof))
		_, err := tx.Exec(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,local_auth_public_key,setup_state)
		 VALUES($1,$2,$2,$3,$4,'synthetic-daemon',$5,$6,'connected')`, owner.TenantID, f.computer, key.PrincipalID, key.ID, hex.EncodeToString(proofHash[:]), public)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Mount(f.mux)
	authz.New(m.pool).Mount(f.mux)
	approvals.New(m.pool).Mount(f.mux)
	rules.New(m.pool).Mount(f.mux)
	secured := m.Middleware(f.mux)
	f.serve = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, r.Pattern = f.mux.Handler(r); secured.ServeHTTP(w, r) })
	return f
}

func (f *workstationFixture) mark(p tenant.Principal, key, computer string, marked bool) *httptest.ResponseRecorder {
	body := map[string]any{"owner_workstation": marked}
	if marked {
		body["workstation_computer_id"] = computer
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("PUT", "http://example.com/api/agent-keys/"+key+"/owner-workstation", strings.NewReader(string(raw)))
	r.Header.Set("Origin", "http://example.com")
	r.SetPathValue("id", key)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.m.handleOwnerWorkstation(w, r)
	return w
}

func (f *workstationFixture) enable(t *testing.T) {
	t.Helper()
	if w := f.mark(f.owner, f.key.ID, f.computer, true); w.Code != 200 {
		t.Fatalf("mark: %d", w.Code)
	}
	add := []string{}
	for _, p := range authz.Registry {
		if authz.OwnerWorkstationPermission(p.Key) {
			add = append(add, p.Key)
		}
	}
	body, _ := json.Marshal(map[string]any{"add": add})
	if w := scopesRequest(f.m, f.owner, f.key.ID, "PATCH", string(body)); w.Code != 200 {
		t.Fatalf("grant governance: %d", w.Code)
	}
}

func (f *workstationFixture) call(key, method, path, body, proof string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	if proof != "" {
		r.Header.Set("Aeon-Step-Up", proof)
	}
	w := httptest.NewRecorder()
	f.serve.ServeHTTP(w, r)
	return w
}

type workstationTestChallenge struct {
	Code    string    `json:"code"`
	ID      string    `json:"challenge_id"`
	Nonce   string    `json:"nonce"`
	Digest  string    `json:"action_digest"`
	Summary string    `json:"summary"`
	Expires time.Time `json:"expires_at"`
}

func (f *workstationFixture) fetchChallenge(key, computer, proof, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/agentd/step-ups/"+id, nil)
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Aeon-Computer-ID", computer)
	r.Header.Set("Aeon-Device-Proof", proof)
	w := httptest.NewRecorder()
	f.serve.ServeHTTP(w, r)
	return w
}

func (f *workstationFixture) readChallenge(t *testing.T, w *httptest.ResponseRecorder) workstationTestChallenge {
	t.Helper()
	if w.Code != 428 {
		t.Fatalf("challenge status: %d", w.Code)
	}
	var c workstationTestChallenge
	var fields map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &fields) != nil || len(fields) != 3 || fields["code"] == nil || fields["challenge_id"] == nil || fields["expires_at"] == nil {
		t.Fatal("428 must contain only code, challenge_id and expires_at")
	}
	if json.Unmarshal(w.Body.Bytes(), &c) != nil || c.Code != "step_up_required" || c.ID == "" || c.Expires.IsZero() || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid challenge contract")
	}
	expires := c.Expires
	fetched := f.fetchChallenge(f.runtime.Token, f.computer, f.deviceProof, c.ID)
	if fetched.Code != 200 || json.Unmarshal(fetched.Body.Bytes(), &c) != nil || c.Nonce == "" || c.Digest == "" || c.Summary == "" || !c.Expires.Equal(expires) {
		t.Fatalf("daemon challenge: %d", fetched.Code)
	}
	return c
}

func (f *workstationFixture) challenge(t *testing.T, method, path, body string) workstationTestChallenge {
	t.Helper()
	return f.readChallenge(t, f.call(f.key.Token, method, path, body, ""))
}

func workstationProof(t *testing.T, key *ecdsa.PrivateKey, c workstationTestChallenge) string {
	t.Helper()
	sum := sha256.Sum256([]byte(c.Nonce + c.Digest))
	raw, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return c.ID + "." + base64.StdEncoding.EncodeToString(raw)
}

func TestWorkstationMarkOwnerOnlyBindingAndUnmarkedIsolation(t *testing.T) {
	f := newWorkstationFixture(t)
	admin := f.owner
	err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Synthetic admin') RETURNING id::text`, f.owner.TenantID).Scan(&admin.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, f.owner.TenantID, admin.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{admin, {ID: f.key.PrincipalID, TenantID: f.owner.TenantID, Kind: tenant.Agent}, {}} {
		if w := f.mark(p, f.key.ID, f.computer, true); w.Code != 403 {
			t.Fatalf("non-owner mark: %d", w.Code)
		}
	}
	if w := f.mark(f.owner, f.key.ID, "00000000-0000-4000-8000-000000000000", true); w.Code != 403 {
		t.Fatalf("wrong computer: %d", w.Code)
	}
	if w := f.call(f.key.Token, "GET", "/api/roles", "", ""); w.Code != 403 {
		t.Fatalf("unmarked governance: %d", w.Code)
	}
	f.enable(t)
	if w := f.mark(f.owner, f.other.ID, f.computer, true); w.Code != 409 {
		t.Fatalf("second designation: %d", w.Code)
	}
	if w := f.call(f.other.Token, "GET", "/api/roles", "", ""); w.Code != 403 {
		t.Fatalf("sibling inherited designation: %d", w.Code)
	}
	if w := f.call(f.key.Token, "PUT", "/api/agent-keys/"+f.key.ID+"/owner-workstation", `{"owner_workstation":false}`, ""); w.Code != 403 {
		t.Fatalf("key cleared designation: %d", w.Code)
	}
	if w := f.call(f.key.Token, "GET", "/api/me", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), f.computer) {
		t.Fatal("me omitted workstation")
	}
	if w := f.mark(f.owner, f.key.ID, "", false); w.Code != 200 {
		t.Fatalf("unmark: %d", w.Code)
	}
	if w := f.call(f.key.Token, "GET", "/api/roles", "", ""); w.Code != 403 {
		t.Fatalf("unmarked retained governance: %d", w.Code)
	}
}

func TestWorkstationChallengesBindExactActionAndAreSingleUse(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	path, body := "/api/roles", `{"name":"Synthetic role","permissions":["nodes.read"]}`
	c := f.challenge(t, "POST", path, body)
	proof := workstationProof(t, f.signer, c)
	for _, tc := range []struct{ name, method, path, body, proof string }{
		{"wrong body", "POST", path, `{"name":"Changed","permissions":["nodes.read"]}`, proof},
		{"wrong action", "POST", "/api/agent-keys", `{"name":"Synthetic key","scopes":[]}`, proof},
		{"wrong query", "POST", path + "?different=1", body, proof},
		{"wrong computer signer", "POST", path, body, workstationProof(t, workstationSigner(2), c)},
		{"malformed signature", "POST", path, body, c.ID + ".invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := f.call(f.key.Token, tc.method, tc.path, tc.body, tc.proof); w.Code != 403 {
				t.Fatalf("invalid proof status: %d", w.Code)
			}
		})
	}
	if w := f.call(f.key.Token, "POST", path, body, proof); w.Code != 201 {
		t.Fatalf("valid proof status: %d", w.Code)
	}
	if w := f.call(f.key.Token, "POST", path, body, proof); w.Code != 403 {
		t.Fatalf("replayed proof status: %d", w.Code)
	}
	c = f.challenge(t, "POST", path, body)
	err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE owner_workstation_challenges SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, c.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.call(f.key.Token, "POST", path, body, workstationProof(t, f.signer, c)); w.Code != 403 {
		t.Fatalf("expired proof status: %d", w.Code)
	}
	// One pending challenge per key; a newer request invalidates the old one.
	c = f.challenge(t, "POST", path, body)
	f.challenge(t, "POST", path, body)
	if w := f.call(f.key.Token, "POST", path, body, workstationProof(t, f.signer, c)); w.Code != 403 {
		t.Fatalf("superseded proof status: %d", w.Code)
	}
}

func TestWorkstationConcurrentProofHasExactlyOneEffect(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	body := `{"name":"Concurrent role","permissions":["nodes.read"]}`
	c := f.challenge(t, "POST", "/api/roles", body)
	proof := workstationProof(t, f.signer, c)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; codes <- f.call(f.key.Token, "POST", "/api/roles", body, proof).Code }()
	}
	close(start)
	wg.Wait()
	close(codes)
	success, denied := 0, 0
	for code := range codes {
		if code == 201 {
			success++
		} else if code == 403 {
			denied++
		} else {
			t.Fatalf("concurrent status: %d", code)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("effects: %d successes, %d denials", success, denied)
	}
}

func TestWorkstationLiveFenceAndAudit(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	for _, path := range []string{"/api/roles", "/api/members", "/api/agent-keys", "/api/audit?category=access"} {
		if w := f.call(f.key.Token, "GET", path, "", ""); w.Code != 200 {
			t.Fatalf("governance read %s: %d", path, w.Code)
		}
	}
	prefix, secret, _ := parseBearer("Bearer " + f.key.Token)
	p, ok, err := f.m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok {
		t.Fatal("fixture authentication failed")
	}
	for _, permission := range []string{"ownership.transfer", "profile.portal_read", "profile.portal_write", "quotes.portal_read", "quotes.portal_accept"} {
		ctx := authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.m.pool)
		if !errors.Is(authz.Require(ctx, permission, authz.Scope{}), authz.ErrForbidden) {
			t.Fatalf("forbidden permission reached: %s", permission)
		}
	}
	if w := f.mark(f.owner, f.key.ID, "", false); w.Code != 200 {
		t.Fatal("unmark failed")
	}
	f.enable(t)
	// A request authenticated before an unmark/re-mark cannot regain authority.
	ctx := db.WithTenantGuard(tenant.WithPrincipal(t.Context(), p), workstationGuard(p, "roles.manage", authz.Scope{}))
	called := false
	err = db.InTenant(ctx, f.m.pool, p.TenantID, func(tx pgx.Tx) error { called = true; return nil })
	if called || !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("stale marking crossed final write fence")
	}
	w := f.call(f.key.Token, "GET", "/api/audit?category=access", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "agent_key.governance_used") || !strings.Contains(w.Body.String(), f.key.ID) || !strings.Contains(w.Body.String(), f.computer) {
		t.Fatal("access audit missing key/agent/computer event")
	}
}

func TestWorkstationForeignTenantAndSelfApproval(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	var approval string
	err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,agent_principal_id,proposed_by_principal_id,scope,resource_kind,rationale,expires_at)
		 VALUES($1,$2,$2,'nodes.read','tenant','synthetic',clock_timestamp()+interval '1 hour') RETURNING id::text`, f.owner.TenantID, f.key.PrincipalID).Scan(&approval)
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/approvals/" + approval + "/decision"
	body := `{"decision":"approved","reason":"synthetic"}`
	c := f.challenge(t, "POST", path, body)
	if w := f.call(f.key.Token, "POST", path, body, workstationProof(t, f.signer, c)); w.Code != 403 {
		t.Fatalf("self approval: %d", w.Code)
	}
	var otherApproval string
	err = db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		var agent string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Other synthetic agent') RETURNING id::text`, f.owner.TenantID).Scan(&agent); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,agent_principal_id,proposed_by_principal_id,scope,resource_kind,rationale,expires_at) VALUES($1,$2,$2,'nodes.read','tenant','synthetic',clock_timestamp()+interval '1 hour') RETURNING id::text`, f.owner.TenantID, agent).Scan(&otherApproval)
	})
	if err != nil {
		t.Fatal(err)
	}
	otherPath := "/api/approvals/" + otherApproval + "/decision"
	c = f.challenge(t, "POST", otherPath, body)
	if w := f.call(f.key.Token, "POST", otherPath, body, workstationProof(t, f.signer, c)); w.Code != 200 {
		t.Fatalf("other-agent approval: %d", w.Code)
	}
	// A valid proof from another tenant fails even when both computers pin the
	// same synthetic public key and the action text is identical.
	foreignFixture := newWorkstationFixture(t)
	foreignFixture.enable(t)
	roleBody := `{"name":"Foreign proof","permissions":["nodes.read"]}`
	foreignProof := foreignFixture.challenge(t, "POST", "/api/roles", roleBody)
	f.challenge(t, "POST", "/api/roles", roleBody)
	if w := f.call(f.key.Token, "POST", "/api/roles", roleBody, workstationProof(t, foreignFixture.signer, foreignProof)); w.Code != 403 {
		t.Fatalf("foreign tenant signature: %d", w.Code)
	}
	// Even a valid signature cannot cross tenant-bound challenge storage. Use a
	// second tenant in this same database, with the same fixture signer on purpose.
	var foreign string
	err = db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('foreign-workstation','Synthetic foreign tenant') RETURNING id::text`).Scan(&foreign)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(t.Context(), f.m.pool, foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM owner_workstation_challenges`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("foreign challenges visible")
		}
		p := tenant.Principal{ID: f.key.PrincipalID, TenantID: foreign, Kind: tenant.Agent, KeyID: f.key.ID, OwnerWorkstation: true, WorkstationComputerID: f.computer, WorkstationGeneration: 1}
		_, _, err := authz.WorkstationKeyTx(t.Context(), tx, p)
		if !errors.Is(err, authz.ErrForbidden) {
			return fmt.Errorf("foreign key binding accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkstationGovernancePermissionRoutes(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	// The device is fake; these permission-backed transport probes exercise the
	// real authentication, role ceiling, transaction fence and step-up boundary.
	// Resource-specific handlers are covered separately by the real CRUD tests.
	for _, permission := range []string{"members.manage", "roles.manage", "keys.read", "keys.manage", "settings.manage", "audit.read", "harness.watch", "harness.force_stop", "harness.recover", "rules.publish", "approvals.decide", "approvals.decide_high"} {
		t.Run(permission, func(t *testing.T) {
			pattern, path := "", ""
			candidates := []string{}
			for candidate, perm := range authz.RoutePermissions {
				if perm == permission && !strings.Contains(candidate, "owner-workstation") {
					candidates = append(candidates, candidate)
				}
			}
			sort.Strings(candidates)
			if len(candidates) > 0 {
				pattern = candidates[0]
			}
			if permission == "approvals.decide_high" { // This is the decision handler's second, live check.
				pattern = "POST /api/approvals/{approvalId}/decision"
			}
			if pattern == "" {
				t.Fatal("permission has no registered route")
			}
			method, raw, _ := strings.Cut(pattern, " ")
			path = raw
			for strings.Contains(path, "{") {
				start := strings.Index(path, "{")
				end := strings.Index(path, "}")
				path = path[:start] + "00000000-0000-4000-8000-000000000001" + path[end+1:]
			}
			handler := f.m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, _ := tenant.PrincipalFrom(r.Context())
				err := db.InTenant(r.Context(), f.m.pool, p.TenantID, func(tx pgx.Tx) error { return authz.RequireTx(r.Context(), tx, p, permission, authz.Scope{}) })
				if err != nil {
					w.WriteHeader(403)
					return
				}
				w.WriteHeader(204)
			}))
			call := func(proof string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
				r.Pattern = pattern
				r.Header.Set("Authorization", "Bearer "+f.key.Token)
				if proof != "" {
					r.Header.Set("Aeon-Step-Up", proof)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			w := call("")
			if w.Code == 428 {
				c := f.readChallenge(t, w)
				w = call(workstationProof(t, f.signer, c))
			}
			if w.Code != 204 {
				t.Fatalf("governance route %s: %d", pattern, w.Code)
			}
		})
	}
}

func TestWorkstationCreatesAndRotatesOrdinaryKeys(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	body := `{"name":"New synthetic worker","scopes":["nodes.read"]}`
	c := f.challenge(t, "POST", "/api/agent-keys", body)
	key := decodeKey(t, f.call(f.key.Token, "POST", "/api/agent-keys", body, workstationProof(t, f.signer, c)))
	body = `{"rotate_key_id":"` + key.ID + `"}`
	c = f.challenge(t, "POST", "/api/agent-keys", body)
	rotated := decodeKey(t, f.call(f.key.Token, "POST", "/api/agent-keys", body, workstationProof(t, f.signer, c)))
	if rotated.OwnerWorkstation || rotated.PrincipalID != key.PrincipalID {
		t.Fatal("rotation changed authority")
	}
	if w := f.call(key.Token, "GET", "/api/me", "", ""); w.Code != 401 {
		t.Fatalf("rotated key still active: %d", w.Code)
	}
}

func TestWorkstationPublishesRulesOnlyWithProof(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	var layer struct {
		ID string `json:"id"`
	}
	w := f.call(f.key.Token, "POST", "/api/rules/layers", `{"layer":"company"}`, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &layer) != nil || layer.ID == "" {
		t.Fatalf("company layer: %d", w.Code)
	}
	var set struct {
		ID string `json:"id"`
	}
	w = f.call(f.key.Token, "POST", "/api/rules/sets", `{"layer_id":"`+layer.ID+`","name":"Synthetic policy"}`, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &set) != nil || set.ID == "" {
		t.Fatalf("rule set: %d", w.Code)
	}
	path := "/api/rules/sets/" + set.ID + "/publish"
	body := `{"expected_revision":1,"version":"261002120000.0.0"}`
	c := f.challenge(t, "POST", path, body)
	if w := f.call(f.key.Token, "POST", path, body, workstationProof(t, f.signer, c)); w.Code != 200 {
		t.Fatalf("publish with proof: %d", w.Code)
	}
}

func TestWorkstationCeilingsRevocationAndMissingPin(t *testing.T) {
	f := newWorkstationFixture(t)
	set := func(sql string, args ...any) {
		t.Helper()
		if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error { _, err := tx.Exec(t.Context(), sql, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	set(`UPDATE agent_pairing_computers SET local_auth_public_key='' WHERE id=$1`, f.computer)
	if w := f.mark(f.owner, f.key.ID, f.computer, true); w.Code != 403 {
		t.Fatalf("unpinned computer: %d", w.Code)
	}
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(f.signer.Curve, f.signer.X, f.signer.Y))
	set(`UPDATE agent_pairing_computers SET local_auth_public_key=$2 WHERE id=$1`, f.computer, public)
	f.enable(t)
	c := f.challenge(t, "POST", "/api/roles", `{"name":"Before demotion","permissions":[]}`)
	set(`UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1`, f.key.PrincipalID)
	if w := f.call(f.key.Token, "POST", "/api/roles", `{"name":"Before demotion","permissions":[]}`, workstationProof(t, f.signer, c)); w.Code != 403 {
		t.Fatalf("demoted role: %d", w.Code)
	}
	set(`UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1`, f.key.PrincipalID)
	set(`UPDATE agent_keys SET scopes=array_remove(scopes,'roles.manage') WHERE id=$1`, f.key.ID)
	if w := f.call(f.key.Token, "POST", "/api/roles", `{}`, ""); w.Code != 403 {
		t.Fatalf("removed key scope: %d", w.Code)
	}
	set(`UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`, f.computer)
	if w := f.call(f.key.Token, "GET", "/api/roles", "", ""); w.Code != 401 {
		t.Fatalf("revoked pairing: %d", w.Code)
	}
}

func TestWorkstationSignatureAndActionEncoding(t *testing.T) {
	p := tenant.Principal{ID: "agent", TenantID: "tenant", KeyID: "key", WorkstationComputerID: "computer"}
	r := httptest.NewRequest("POST", "/api/roles?x=1", strings.NewReader("{}"))
	digest, _ := workstationAction(p, r, []byte("{}"))
	c := workstationTestChallenge{ID: "id", Nonce: strings.Repeat("1", 64), Digest: digest}
	key := workstationSigner(1)
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(key.Curve, key.X, key.Y))
	_, signature, _ := strings.Cut(workstationProof(t, key, c), ".")
	if !verifyWorkstationSignature(public, c.Nonce, c.Digest, signature) {
		t.Fatal("documented concatenation rejected")
	}
	for _, change := range []func(*tenant.Principal, *http.Request){func(p *tenant.Principal, r *http.Request) { p.TenantID = "other" }, func(p *tenant.Principal, r *http.Request) { p.KeyID = "other" }, func(p *tenant.Principal, r *http.Request) { p.WorkstationComputerID = "other" }, func(p *tenant.Principal, r *http.Request) { r.Header.Set("If-Match", "changed") }} {
		other := p
		req := r.Clone(context.Background())
		change(&other, req)
		got, _ := workstationAction(other, req, []byte("{}"))
		if got == digest {
			t.Fatal("action context unbound")
		}
	}
}

func TestWorkstationProofCannotOutlivePinnedSigner(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	body := `{"name":"Synthetic role","permissions":["nodes.read"]}`
	challenge := f.challenge(t, "POST", "/api/roles", body)
	handler := f.m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a pin replacement after proof consumption, before the final write.
		replacement := workstationSigner(2)
		public := base64.StdEncoding.EncodeToString(elliptic.Marshal(replacement.Curve, replacement.X, replacement.Y))
		err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_computers SET local_auth_public_key=$2 WHERE id=$1`, f.computer, public)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := tenant.PrincipalFrom(r.Context())
		wrote := false
		err = db.InTenant(r.Context(), f.m.pool, p.TenantID, func(tx pgx.Tx) error { wrote = true; return nil })
		if !errors.Is(err, authz.ErrForbidden) || wrote {
			t.Fatal("proof survived a changed pinned signer")
		}
		w.WriteHeader(403)
	}))
	r := httptest.NewRequest("POST", "/api/roles", strings.NewReader(body))
	r.Pattern = "POST /api/roles"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.key.Token)
	r.Header.Set("Aeon-Step-Up", workstationProof(t, f.signer, challenge))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("pin replacement status: %d", w.Code)
	}
}

func TestWorkstationDaemonFetchAuthenticatesComputerAndOriginalPrompt(t *testing.T) {
	f := newWorkstationFixture(t)
	// Designate a sibling key; the daemon still uses the original runtime key.
	f.key, f.other = f.other, f.key
	f.enable(t)
	deviceProof := strings.Repeat("b", 64)
	sum := sha256.Sum256([]byte(deviceProof))
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_computers SET lifecycle_hash=$2 WHERE id=$1`, f.computer, hex.EncodeToString(sum[:]))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"INNOCUOUS PROMPT SUBSTITUTION","permissions":["nodes.read"]}`
	c := f.challenge(t, "POST", "/api/roles", body)
	fetch := f.fetchChallenge
	w := fetch(f.other.Token, f.computer, deviceProof, c.ID)
	var got map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("daemon fetch: %d", w.Code)
	}
	if len(got) != 5 || got["summary"] != c.Summary || got["nonce"] != c.Nonce || got["action_digest"] != c.Digest || got["computer_id"] != f.computer || strings.Contains(c.Summary, "INNOCUOUS") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("daemon prompt is not the bounded original server challenge")
	}
	for _, tc := range []struct {
		name, key, computer, proof string
		status                     int
	}{
		{"agent key alone", f.key.Token, "", "", 403},
		{"sibling credential with device proof", f.key.Token, f.computer, deviceProof, 404},
		{"runtime key alone", f.other.Token, "", "", 403},
		{"wrong proof", f.other.Token, f.computer, strings.Repeat("c", 64), 404},
		{"missing proof", f.other.Token, f.computer, "", 403},
		{"wrong computer", f.other.Token, f.owner.ID, deviceProof, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := fetch(tc.key, tc.computer, tc.proof, c.ID)
			if w.Code != tc.status {
				t.Fatalf("credential substitution: %d want %d", w.Code, tc.status)
			}
			if strings.Contains(w.Body.String(), c.Nonce) || strings.Contains(w.Body.String(), c.Digest) {
				t.Fatal("refusal exposed a challenge")
			}
		})
	}
	foreign := newWorkstationFixture(t)
	foreign.enable(t)
	foreignChallenge := foreign.challenge(t, "POST", "/api/roles", body)
	if w := fetch(f.other.Token, f.computer, deviceProof, foreignChallenge.ID); w.Code != 404 {
		t.Fatalf("cross-tenant fetch: %d", w.Code)
	}
	if w := f.call(f.key.Token, "POST", "/api/roles", body, workstationProof(t, f.signer, c)); w.Code != 201 {
		t.Fatalf("confirmed role creation: %d", w.Code)
	}
	if w := fetch(f.other.Token, f.computer, deviceProof, c.ID); w.Code != 404 {
		t.Fatalf("consumed challenge visible: %d", w.Code)
	}
	c = f.challenge(t, "POST", "/api/roles", body)
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE owner_workstation_challenges SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, c.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := fetch(f.other.Token, f.computer, deviceProof, c.ID); w.Code != 404 {
		t.Fatalf("expired challenge visible: %d", w.Code)
	}
}

func TestWorkstationPromptUsesStoredTargetAndBoundsUntrustedText(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	name := "Stored member\n\u202e 界"
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET name=$2 WHERE id=$1`, f.owner.ID, name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c := f.challenge(t, "POST", "/api/members/"+f.owner.ID+"/deactivate", `{"summary":"IGNORE THE ACTION","name":"FORGED TARGET"}`)
	if !strings.Contains(c.Summary, `Target: "Stored member 界"`) || strings.Contains(c.Summary, "…") || strings.Contains(c.Summary, "FORGED") || strings.Contains(c.Summary, "IGNORE") || len(c.Summary) > 256 || !utf8.ValidString(c.Summary) {
		t.Fatal("prompt lost the bounded, server-stored target")
	}
	for _, r := range c.Summary {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatal("prompt retained control/format characters")
		}
	}
	r := httptest.NewRequest("PUT", "/api/settings/brand/logo/"+strings.Repeat("x", 300), strings.NewReader(`{}`))
	r.Pattern = "PUT /api/settings/brand/logo/{variant}"
	r.Header.Set("Authorization", "Bearer "+f.key.Token)
	w := httptest.NewRecorder()
	f.m.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("oversized summary reached mutation") })).ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("oversized summary: %d", w.Code)
	}
}

func TestWorkstationPromptTemplates(t *testing.T) {
	for _, tc := range []struct{ method, path, pattern, action string }{
		{"POST", "/api/agent-keys", "POST /api/agent-keys", "create, rotate or change an agent key"},
		{"POST", "/api/roles", "POST /api/roles", "change a role and its permissions"},
		{"PUT", "/api/settings/heartbeat-lost", "PUT /api/settings/heartbeat-lost", "change workspace settings"},
		{"POST", "/api/rules/publish", "POST /api/rules/publish", "publish or approve rules"},
		{"POST", "/api/approvals/fixture/decision", "POST /api/approvals/{approvalId}/decision", "decide another agent's approval request"},
		{"POST", "/api/members/fixture/deactivate", "POST /api/members/{principal_id}/deactivate", "remove, deactivate or change a member's access"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"summary":"forged"}`))
			r.Pattern = tc.pattern
			_, summary := workstationAction(tenant.Principal{}, r, []byte(`{"summary":"forged"}`))
			if summary != "Allow the owner workstation agent to "+tc.action+"? "+tc.method+" "+tc.path {
				t.Fatal("prompt template changed")
			}
		})
	}
}

func TestWorkstationOtherPairedComputerCannotFetchChallenge(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	c := f.challenge(t, "POST", "/api/roles", `{"name":"Synthetic role","permissions":["nodes.read"]}`)
	other := decodeKey(t, keyRequest(f.m, f.owner, map[string]any{"name": "Other computer", "scopes": []string{"harness.worker"}}))
	var computer string
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1`, other.PrincipalID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state) VALUES($1,gen_random_uuid(),'987654321',$2,$2,$2,'{}','synthetic','redeemed') RETURNING id::text`, f.owner.TenantID, strings.Repeat("c", 64)).Scan(&computer); err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(f.deviceProof))
		public := base64.StdEncoding.EncodeToString(elliptic.Marshal(f.signer.Curve, f.signer.X, f.signer.Y))
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,local_auth_public_key,setup_state) VALUES($1,$2,$2,$3,$4,'other-daemon',$5,$6,'connected')`, f.owner.TenantID, computer, other.PrincipalID, other.ID, hex.EncodeToString(hash[:]), public)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A fully authenticated second computer in the same tenant is still refused.
	for _, id := range []string{computer, f.computer} {
		w := f.fetchChallenge(other.Token, id, f.deviceProof, c.ID)
		if w.Code != 404 || strings.Contains(w.Body.String(), c.Nonce) || strings.Contains(w.Body.String(), c.Digest) {
			t.Fatalf("other paired computer fetch: %d", w.Code)
		}
	}
}
