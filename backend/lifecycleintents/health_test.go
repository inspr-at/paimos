// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

func TestLifecycleRuntimeHealthFreshStaleOfflineAndUnknown(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	page, e := f.s.RuntimeHealth(ctx, f.human, f.project)
	if e != nil || len(page.Runtimes) != 1 || len(page.Runtimes[0].Layers) != 4 {
		t.Fatalf("initial health %v", e)
	}
	for _, layer := range page.Runtimes[0].Layers {
		if layer.State != "unknown" || layer.Status != "unknown" || layer.Reason != "not_reported" {
			t.Fatal("missing evidence represented as healthy")
		}
	}
	_, e = agentmessage.NewService(db.DB).PublishRuntimeHealth(ctx, agentmessage.ConsumerCredentials{Principal: f.reporter, RuntimeLease: testLease}, f.project, agentmessage.RuntimeHealthInput{RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, Sequence: 1, Layer: "fallback", State: "unhealthy", Reason: "consumer_crash_loop", FailureCount: 3})
	if e != nil {
		t.Fatal(e)
	}
	f.now = time.Now().UTC()
	page, e = f.s.RuntimeHealth(ctx, f.human, f.project)
	if e != nil {
		t.Fatal(e)
	}
	layer := page.Runtimes[0].Layers[2]
	if layer.State != "unhealthy" || layer.Status != "fresh" || layer.FailureCount != 3 || layer.UpdatedAt == "" {
		t.Fatal("typed health not projected")
	}
	f.now = f.now.Add(61 * time.Second)
	page, e = f.s.RuntimeHealth(ctx, f.human, f.project)
	if e != nil || page.Runtimes[0].Status != "fresh" || page.Runtimes[0].Layers[2].Status != "stale" {
		t.Fatal("stale evidence reported fresh")
	}
	f.now = f.now.Add(61 * time.Second)
	page, e = f.s.RuntimeHealth(ctx, f.human, f.project)
	if e != nil || page.Runtimes[0].Status != "offline" || page.Runtimes[0].Layers[2].Status != "offline" {
		t.Fatal("expired runtime not offline")
	}
	registration := f.registration
	registration.Generation = uuid.NewString()
	next, e := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, registration)
	if e != nil {
		t.Fatal(e)
	}
	page, e = f.s.RuntimeHealth(ctx, f.human, f.project)
	if e != nil || len(page.Runtimes) != 1 || page.Runtimes[0].RuntimeID != next.ID || page.Runtimes[0].Layers[2].Status != "unknown" {
		t.Fatal("old machine generation leaked into replacement health")
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{"lease", "api_key_id", "user_id", "target_ref", "workspace_path", "total"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private runtime metadata disclosed")
		}
	}
}

func TestLifecycleRuntimeHealthViewerReauthorizationAndNoCountOracle(t *testing.T) {
	// Pin the service clock so every secondary credential must use it too;
	// wall-clock inserts can otherwise become future-created under slow CI.
	f := setupWithClock(t, func() time.Time {
		return time.Date(2026, time.January, 2, 3, 4, 5, 123000000, time.UTC)
	})
	ctx := context.Background()
	res, e := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status) VALUES('health-viewer','disabled','member','member','active')`)
	if e != nil {
		t.Fatal(e)
	}
	user, _ := res.LastInsertId()
	credential := uuid.NewString()
	if _, e = db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), user, credential, stamp(f.now.Add(time.Hour)), stamp(f.now)); e != nil {
		t.Fatal(e)
	}
	viewer, _ := auth.NewSessionPrincipal(credential, user, user, false)
	if _, e = db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'viewer')`, user, f.project); e != nil {
		t.Fatal(e)
	}
	if page, e := f.s.RuntimeHealth(ctx, viewer, f.project); e != nil || len(page.Runtimes) != 1 {
		t.Fatal("authorized viewer blocked")
	}
	if _, e = db.DB.Exec(`UPDATE project_members SET access_level='none' WHERE user_id=?`, user); e != nil {
		t.Fatal(e)
	}
	for _, project := range []int64{f.project, 987654321} {
		if _, e = f.s.RuntimeHealth(ctx, viewer, project); e != ErrUnavailable {
			t.Fatal("hidden/missing project disclosed")
		}
	}
	if _, e = db.DB.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, f.reporter.APIKeyID()); e != nil {
		t.Fatal(e)
	}
	page, e := f.s.RuntimeHealth(ctx, f.human, f.project)
	if e != nil || len(page.Runtimes) != 0 {
		t.Fatal("revoked owner still advertised")
	}
	if _, e = f.s.RuntimeHealth(ctx, f.reporter, f.project); e != ErrUnavailable {
		t.Fatal("reporter read browser health")
	}
}

func TestLifecycleWorkspaceLabelAndServerProvedMapping(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	session := f.managed(t)
	if e := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: session.ID, Generation: uuid.NewString()}); e != nil {
		t.Fatal(e)
	}
	out, e := f.s.Runtimes(ctx, f.human, f.project)
	if e != nil || len(out[0].Sessions) != 1 || out[0].Sessions[0].WorkspaceHandle != f.runtime.Workspaces[0].Handle {
		t.Fatal("proved mapping absent")
	}
	for _, value := range []string{"Project workspace", "Release 2 - checkout", "WORK.space_1", ""} {
		if !validWorkspaceLabel(value) {
			t.Fatalf("valid label refused %q", value)
		}
	}
	for _, value := range []string{"/Users/operator/work", "C:\\workspace", " leading", "trailing ", "multiline\nlabel", strings.Repeat("a", 49), "https://private.example", "token=private"} {
		if validWorkspaceLabel(value) {
			t.Fatal("unsafe label admitted")
		}
	}
	// A legacy/corrupt duplicate advertisement must never invent a unique match.
	broken := f.registration
	broken.Workspaces = append([]Workspace{}, f.registration.Workspaces...)
	broken.Workspaces = append(broken.Workspaces, Workspace{Handle: uuid.NewString(), Identity: broken.Workspaces[0].Identity})
	if validateRegistration(broken) != ErrInvalid {
		t.Fatal("duplicate identity accepted")
	}
	if workspaceHandleForIdentity(broken.Workspaces, f.runtime.Workspaces[0].Identity) != "" {
		t.Fatal("ambiguous identity mapped")
	}
	broken.Workspaces = []Workspace{{Handle: uuid.NewString(), Identity: fmt.Sprintf("%064x", 2), Label: "Explicit workspace"}}
	if workspaceHandleForIdentity(broken.Workspaces, f.runtime.Workspaces[0].Identity) != "" {
		t.Fatal("unmatched identity guessed")
	}
	broken.Generation = uuid.NewString()
	broken.Host = "second-machine"
	registered, e := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, broken)
	if e != nil || registered.Workspaces[0].Label != "Explicit workspace" {
		t.Fatal("explicit label lost")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "/fixture/workspace") || strings.Contains(string(raw), "canonical_path") {
		t.Fatal("workspace path disclosed")
	}
}
