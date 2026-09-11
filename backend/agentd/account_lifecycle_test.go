// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func bindLifecycle(t *testing.T, supervisor *Supervisor, projects ...int64) {
	t.Helper()
	supervisor.BindAccountLifecycleProjects(projects)
}

func TestAccountLifecycleConnectReplayConflictDisconnectInUseAndStaleStart(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true, "personal": true}}
	root := t.TempDir()
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-lifecycle", StateRoot: root, Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	bindLifecycle(t, supervisor, 42)
	generation := supervisor.Status().DaemonID
	connect := AccountLifecycleRequest{
		IdempotencyKey: "connect-coordinator", Operation: AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex,
	}
	first, err := supervisor.ApplyAccountLifecycle(context.Background(), connect)
	if err != nil || first.State != "connected" || first.Revision != 1 || first.Mode != AccountLifecycleExplicit {
		t.Fatalf("connect=%+v err=%v", first, err)
	}
	if !containsKey(first.AttachedKeys, "coordinator") || !containsKey(first.AttachedKeys, "personal") {
		t.Fatalf("first mutation must freeze enrolled keys: %+v", first.AttachedKeys)
	}
	replay, err := supervisor.ApplyAccountLifecycle(context.Background(), connect)
	if err != nil || !replay.Replayed || replay.Revision != first.Revision || replay.RuntimeGeneration != first.RuntimeGeneration {
		t.Fatalf("duplicate replay=%+v err=%v", replay, err)
	}
	changed := connect
	changed.AccountKey = "personal"
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), changed); !errors.Is(err, ErrAccountLifecycleConflict) {
		t.Fatalf("changed retry err=%v", err)
	}
	staleGen := connect
	staleGen.IdempotencyKey = "stale-generation"
	staleGen.RuntimeGeneration = uuid.NewString()
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), staleGen); !errors.Is(err, ErrAccountStale) {
		t.Fatalf("foreign generation err=%v", err)
	}
	foreignProject := connect
	foreignProject.IdempotencyKey = "foreign-project"
	foreignProject.ProjectID = 99
	foreignProject.ExpectedRevision = 0
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), foreignProject); !errors.Is(err, ErrAccountForeign) {
		t.Fatalf("foreign project connect err=%v", err)
	}
	workspace := t.TempDir()
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: workspace, Prompt: "work", Identity: "codex:worker",
		ProjectID: 42, AccountKey: "coordinator", AttachmentRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "ambient", Identity: "codex:ambient",
		ProjectID: 42,
	}); !errors.Is(err, ErrAccountDetached) {
		t.Fatalf("explicit mode must refuse class-only start: %v", err)
	}
	disconnect := AccountLifecycleRequest{
		IdempotencyKey: "disconnect-coordinator", Operation: AccountLifecycleDisconnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex, ExpectedRevision: 1,
	}
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), disconnect); !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("in-use disconnect err=%v", err)
	}
	if _, err := supervisor.Stop(context.Background(), session.ID, scopedControl(supervisor, session, "stop-coordinator", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), disconnect); !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("in-use refusal must replay: %v", err)
	}
	disconnect.IdempotencyKey = "disconnect-coordinator-after-stop"
	disconnected, err := supervisor.ApplyAccountLifecycle(context.Background(), disconnect)
	if err != nil || disconnected.State != "disconnected" || containsKey(disconnected.AttachedKeys, "coordinator") {
		t.Fatalf("disconnect=%+v err=%v", disconnected, err)
	}
	if disconnected.Advertisement != AccountAdvertisementRestartRequired {
		t.Fatalf("disconnect must explain restart-required advertisement: %+v", disconnected)
	}
	if _, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: workspace, Prompt: "stale", Identity: "codex:stale",
		ProjectID: 42, AccountKey: "coordinator", AttachmentRevision: disconnected.Revision,
	}); !errors.Is(err, ErrAccountDetached) {
		t.Fatalf("stale start err=%v spawned=%d", err, len(adapter.requests))
	}
	if _, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:other-project",
		ProjectID: 99, AccountKey: "coordinator",
	}); !errors.Is(err, ErrAccountForeign) {
		t.Fatalf("unbound project start err=%v", err)
	}
	restarted, err := NewSupervisor(SupervisorConfig{Instance: "account-lifecycle", StateRoot: root, Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	bindLifecycle(t, restarted, 42)
	keys, mode, revision := restarted.AttachedAccountKeys(42, AdapterCodex)
	if mode != AccountLifecycleExplicit || revision != disconnected.Revision || containsKey(keys, "coordinator") || !containsKey(keys, "personal") {
		t.Fatalf("restart persistence keys=%v mode=%s revision=%d", keys, mode, revision)
	}
	if _, err := restarted.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: workspace, Prompt: "work", Identity: "codex:restart",
		ProjectID: 42, AccountKey: "coordinator", AttachmentRevision: revision,
	}); !errors.Is(err, ErrAccountDetached) {
		t.Fatalf("restarted stale start err=%v", err)
	}
}

func TestAccountLifecycleReconnectInvalidatesReviewedStart(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true, "personal": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-reconnect", StateRoot: t.TempDir(), Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	bindLifecycle(t, supervisor, 42)
	generation := supervisor.Status().DaemonID
	connect := AccountLifecycleRequest{
		IdempotencyKey: "connect-1", Operation: AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex,
	}
	first, err := supervisor.ApplyAccountLifecycle(context.Background(), connect)
	if err != nil {
		t.Fatal(err)
	}
	disconnect, err := supervisor.ApplyAccountLifecycle(context.Background(), AccountLifecycleRequest{
		IdempotencyKey: "disconnect-1", Operation: AccountLifecycleDisconnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex, ExpectedRevision: first.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := supervisor.ApplyAccountLifecycle(context.Background(), AccountLifecycleRequest{
		IdempotencyKey: "connect-2", Operation: AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex, ExpectedRevision: disconnect.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "reviewed-before-disconnect", Identity: "codex:stale-rev",
		ProjectID: 42, AccountKey: "coordinator", AttachmentRevision: first.Revision,
	}); !errors.Is(err, ErrAccountStale) {
		t.Fatalf("pre-disconnect start err=%v current=%d", err, reconnected.Revision)
	}
	if _, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "reviewed-after-reconnect", Identity: "codex:current-rev",
		ProjectID: 42, AccountKey: "coordinator", AttachmentRevision: reconnected.Revision,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountLifecycleCrashBetweenStateAndReceiptReconcilesStale(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true, "personal": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-crash", StateRoot: t.TempDir(), Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	bindLifecycle(t, supervisor, 42)
	generation := supervisor.Status().DaemonID
	if err := supervisor.accounts.attachments.Put(accountAttachmentSnapshot{
		ProjectID: 42, Adapter: AdapterCodex, Keys: []string{"coordinator", "personal"},
		Revision: 1, MutatingGeneration: generation,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = supervisor.ApplyAccountLifecycle(context.Background(), AccountLifecycleRequest{
		IdempotencyKey: "connect-after-crash", Operation: AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex,
	})
	if !errors.Is(err, ErrAccountStale) {
		t.Fatalf("retry without receipt err=%v", err)
	}
	keys, mode, revision := supervisor.AttachedAccountKeys(42, AdapterCodex)
	if mode != AccountLifecycleExplicit || revision != 1 || !containsKey(keys, "coordinator") || !containsKey(keys, "personal") {
		t.Fatalf("status must show the applied snapshot keys=%v mode=%s revision=%d", keys, mode, revision)
	}
	states := supervisor.AccountLifecycleStatus(42)
	if len(states) == 0 || states[0].Revision != 1 {
		t.Fatalf("reconciliation status=%+v", states)
	}
}

func TestAccountLifecycleRefusesMalformedUnknownUnsupportedAndStaleRevision(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-refuse", StateRoot: t.TempDir(), Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	bindLifecycle(t, supervisor, 42)
	generation := supervisor.Status().DaemonID
	base := AccountLifecycleRequest{
		IdempotencyKey: "ok", Operation: AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex,
	}
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mut  func(*AccountLifecycleRequest)
		want error
	}{
		{"unknown key", func(r *AccountLifecycleRequest) { r.AccountKey = "missing" }, errors.New("managed account selection is unavailable")},
		{"label as key", func(r *AccountLifecycleRequest) { r.AccountKey = "chatgpt" }, nil},
		{"claude", func(r *AccountLifecycleRequest) { r.Adapter = AdapterClaude; r.AccountKey = "coordinator" }, ErrAccountUnsupported},
		{"stale revision", func(r *AccountLifecycleRequest) { r.ExpectedRevision = 0 }, ErrAccountStale},
		{"foreign project", func(r *AccountLifecycleRequest) { r.ProjectID = 99; r.ExpectedRevision = 0 }, ErrAccountForeign},
		{"path", func(r *AccountLifecycleRequest) { r.AccountKey = "/tmp/home" }, nil},
	}
	for _, tc := range cases {
		req := base
		req.IdempotencyKey = "refuse-" + tc.name
		req.ExpectedRevision = 1
		tc.mut(&req)
		_, err := supervisor.ApplyAccountLifecycle(context.Background(), req)
		if tc.want != nil && (err == nil || err.Error() != tc.want.Error() && !errors.Is(err, tc.want)) {
			t.Fatalf("%s err=%v want=%v", tc.name, err, tc.want)
		}
		if tc.want == nil && err == nil {
			t.Fatalf("%s accepted malformed selection", tc.name)
		}
	}
}

func TestAccountLifecycleSerializesDisconnectAgainstStart(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true, "personal": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-race", StateRoot: t.TempDir(), Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	bindLifecycle(t, supervisor, 7)
	generation := supervisor.Status().DaemonID
	if _, err := supervisor.ApplyAccountLifecycle(context.Background(), AccountLifecycleRequest{
		IdempotencyKey: "seed", Operation: AccountLifecycleConnect, ProjectID: 7,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex,
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var started, detached, inUse atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			workspace := filepath.Join(root, strconv.Itoa(i))
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				t.Errorf("workspace: %v", err)
				return
			}
			_, err := supervisor.Start(context.Background(), StartRequest{
				Adapter: AdapterCodex, Workspace: workspace, Prompt: "work", Identity: "codex:race",
				ProjectID: 7, AccountKey: "coordinator", AttachmentRevision: 1, IdempotencyKey: "start-" + uuid.NewString(),
			})
			switch {
			case err == nil, errors.Is(err, ErrWorkspaceConflict):
				started.Add(1)
			case errors.Is(err, ErrAccountDetached), errors.Is(err, ErrAccountStale):
				detached.Add(1)
			default:
				t.Errorf("start err=%v", err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := supervisor.ApplyAccountLifecycle(context.Background(), AccountLifecycleRequest{
				IdempotencyKey: "disconnect", Operation: AccountLifecycleDisconnect, ProjectID: 7,
				RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: AdapterCodex, ExpectedRevision: 1,
			})
			switch {
			case err == nil, errors.Is(err, ErrAccountLifecycleConflict):
			case errors.Is(err, ErrAccountInUse):
				inUse.Add(1)
			case errors.Is(err, ErrAccountStale):
			default:
				t.Errorf("disconnect err=%v", err)
			}
		}(i)
	}
	wg.Wait()
	if started.Load()+detached.Load() == 0 {
		t.Fatal("no start outcomes")
	}
	keys, mode, _ := supervisor.AttachedAccountKeys(7, AdapterCodex)
	if mode == AccountLifecycleExplicit && containsKey(keys, "coordinator") && detached.Load() > 0 {
		t.Fatal("detached starts while coordinator remained attached")
	}
	if !containsKey(keys, "coordinator") {
		if _, err := supervisor.Start(context.Background(), StartRequest{
			Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "after", Identity: "codex:after",
			ProjectID: 7, AccountKey: "coordinator", AttachmentRevision: 2,
		}); !errors.Is(err, ErrAccountDetached) && !errors.Is(err, ErrAccountStale) {
			t.Fatalf("post-race start err=%v in_use=%d started=%d", err, inUse.Load(), started.Load())
		}
	}
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}
