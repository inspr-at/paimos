// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

// Use the real transition state machine: its terminal-refusal and exact
// expected-revision checks are stricter than the transport-only fixtures.
type serverRunnerAuthority struct {
	service   *lifecycleintents.Service
	project   int64
	principal auth.Principal
	lease     string
}

func (a serverRunnerAuthority) RegisterRuntime(ctx context.Context, in lifecycleintents.Registration) (lifecycleintents.Runtime, error) {
	return a.service.RegisterRuntime(ctx, a.principal, a.project, a.lease, in)
}
func (a serverRunnerAuthority) RegisterSession(ctx context.Context, runtime string, in lifecycleintents.SessionRegistration, proof string) error {
	return a.service.RegisterSession(ctx, a.principal, a.project, runtime, a.lease, proof, in)
}
func (a serverRunnerAuthority) Claim(ctx context.Context, runtime string) (*lifecycleintents.Intent, error) {
	return a.service.Claim(ctx, a.principal, a.project, runtime, a.lease)
}
func (a serverRunnerAuthority) Transition(ctx context.Context, id string, in lifecycleintents.Transition) (lifecycleintents.Intent, error) {
	return a.service.Transition(ctx, a.principal, a.project, id, a.lease, in)
}

type revokedOriginExecutor struct {
	t                *testing.T
	credential       string
	effects, commits map[string]int
}

func (*revokedOriginExecutor) Prepare(context.Context, lifecycleintents.Intent) error { return nil }
func (e *revokedOriginExecutor) Execute(_ context.Context, in lifecycleintents.Intent) (Result, error) {
	e.effects[in.ID]++
	if e.credential != "" {
		if _, err := db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, e.credential); err != nil {
			e.t.Fatal(err)
		}
		e.credential = ""
	}
	return Result{Reason: "applied"}, nil
}
func (e *revokedOriginExecutor) Committed(_ context.Context, in lifecycleintents.Intent) error {
	e.commits[in.ID]++
	return nil
}

func TestLifecycleActualServerRevokedOutcomeDoesNotStarveFreshIntent(t *testing.T) {
	ctx := context.Background()
	oldDB := db.DB
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DB.Close(); db.DB = oldDB })
	res, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('runner-server','disabled','admin','super_admin','active',1)`)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := res.LastInsertId()
	res, err = db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Runner server fixture','RSF')`)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := res.LastInsertId()
	res, err = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture','not-a-credential','fixture','*')`, user)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := res.LastInsertId()
	principal, err := auth.NewAPIKeyPrincipal(key, user, auth.ParseScopes("*"))
	if err != nil {
		t.Fatal(err)
	}
	a := serverRunnerAuthority{service: lifecycleintents.NewService(db.DB), project: project, principal: principal, lease: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	runtime, err := a.RegisterRuntime(ctx, lifecycleintents.Registration{Generation: uuid.NewString(), Host: "runner-fixture", AccountLabel: "chatgpt", Workspaces: []lifecycleintents.Workspace{}, Profiles: []lifecycleintents.Profile{}})
	if err != nil {
		t.Fatal(err)
	}
	newBrowser := func() (auth.Principal, string) {
		credential := uuid.NewString()
		if _, err = db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), user, credential); err != nil {
			t.Fatal(err)
		}
		p, e := auth.NewSessionPrincipal(credential, user, user, false)
		if e != nil {
			t.Fatal(e)
		}
		return p, credential
	}
	submit := func(p auth.Principal) lifecycleintents.Intent {
		in, created, e := a.service.Submit(ctx, p, project, lifecycleintents.Request{RequestKey: uuid.NewString(), RuntimeID: runtime.ID, RuntimeGeneration: runtime.Generation, AccountLabel: "chatgpt", Operation: "repair", RepairLayer: "reporter", TTLSeconds: 120})
		if e != nil || !created {
			t.Fatalf("submit created=%v error=%v", created, e)
		}
		return in
	}
	firstBrowser, credential := newBrowser()
	first := submit(firstBrowser)
	e := &revokedOriginExecutor{t: t, credential: credential, effects: map[string]int{}, commits: map[string]int{}}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(dir, runtime.Generation, a, e)
	if err != nil {
		t.Fatal(err)
	}
	// The effect ran once, but M176 observes origin revocation and refuses to
	// record success. This uncertainty must stay visible without blocking work.
	if err = runner.Step(ctx, runtime); !errors.Is(err, ErrUnknown) {
		t.Fatalf("refused completed outcome error=%v, want visible unknown", err)
	}
	if e.effects[first.ID] != 1 || e.commits[first.ID] != 0 {
		t.Fatal("refused effect replayed or falsely committed")
	}
	secondBrowser, _ := newBrowser()
	second := submit(secondBrowser)
	terminal, err := a.service.Get(ctx, secondBrowser, project, first.ID)
	if err != nil || terminal.State != "failed" || terminal.Reason != "authority_revoked" {
		t.Fatalf("actual terminal outcome=%s/%s error=%v", terminal.State, terminal.Reason, err)
	}
	// A fresh client instance proves quarantine survives local journal replay.
	runner, err = NewRunner(dir, runtime.Generation, a, e)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(ctx, runtime); !errors.Is(err, ErrUnknown) {
		t.Fatalf("fresh request should execute while prior uncertainty remains visible: %v", err)
	}
	if e.effects[first.ID] != 1 || e.effects[second.ID] != 1 || e.commits[first.ID] != 0 || e.commits[second.ID] != 1 {
		t.Fatalf("effects=%v commits=%v", e.effects, e.commits)
	}
	if err = runner.Step(ctx, runtime); !errors.Is(err, ErrUnknown) {
		t.Fatalf("quarantined prior outcome disappeared: %v", err)
	}
	if e.effects[first.ID] != 1 || e.effects[second.ID] != 1 {
		t.Fatal("terminal work was executed again")
	}
	retained := false
	for _, r := range runner.journal.Snapshot() {
		if r.ID == first.ID {
			retained = r.Phase == "quarantined" && r.Result.Reason == "applied" && r.Intent.ID == first.ID
		}
	}
	if !retained {
		t.Fatal("quarantine lost original local result evidence")
	}
}
