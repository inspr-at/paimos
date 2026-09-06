//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

type fixtureDaemon struct {
	status      agentd.RuntimeStatus
	unavailable bool
	quiesces    int
}

func (d *fixtureDaemon) RuntimeStatus(context.Context) (agentd.RuntimeStatus, error) {
	if d.unavailable {
		return agentd.RuntimeStatus{}, errors.New("fixture unavailable")
	}
	return d.status, nil
}
func (d *fixtureDaemon) QuiesceRuntime(_ context.Context, g string, _ []string) error {
	if g != d.status.DaemonID {
		return errors.New("wrong generation")
	}
	d.quiesces++
	d.status.Closed = true
	for i := range d.status.Sessions {
		if d.status.Sessions[i].Owned {
			d.status.Sessions[i].Owned = false
			d.status.Sessions[i].State = agentd.StateStopped
		}
	}
	return nil
}

type fixtureService struct {
	state         ServiceState
	starts, stops int
	crash         bool
	succeedAfter  int
	daemon        *fixtureDaemon
}

func (s *fixtureService) Inspect(context.Context) (ServiceState, error) { return s.state, nil }
func (s *fixtureService) InstallAction() string                         { return "fixture-manager install reviewed-service" }
func (s *fixtureService) Start(context.Context) error {
	s.starts++
	if s.crash || s.starts < s.succeedAfter {
		return errors.New("fixture crash")
	}
	s.state.Running = true
	s.state.Loaded = true
	s.state.PID = 222
	s.daemon.unavailable = false
	s.daemon.status.PID = 222
	s.daemon.status.Closed = false
	return nil
}
func (s *fixtureService) Stop(context.Context) error {
	s.stops++
	s.state.Running = false
	s.state.PID = 0
	s.daemon.unavailable = true
	return nil
}
func readyRemote(context.Context) RemoteEvidence {
	return RemoteEvidence{Auth: layer("cli_auth", Known, "verified", ""), Identity: layer("server_identity", Known, "verified", ""), Agents: layer("canonical_agents", Known, "verified", ""), Profiles: layer("dispatch_profiles", Known, "verified", ""), Targets: layer("targets", Unknown, "ownership_unverified", "")}
}
func fixtureRuntime(t *testing.T) (*Runtime, *fixtureService, *fixtureDaemon, *[]time.Duration) {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "runtime-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root := filepath.Join(parent, "state")
	d := &fixtureDaemon{unavailable: true, status: agentd.RuntimeStatus{DaemonID: "fixture-generation", Instance: "fixture", ReporterConfigured: true, ReporterLastSuccess: time.Now()}}
	s := &fixtureService{state: ServiceState{Verified: true, Definition: "reviewed"}, daemon: d}
	waits := []time.Duration{}
	r, e := New(Config{Instance: "fixture", StateRoot: root, Service: s, Daemon: d, Remote: readyRemote, Wait: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.pathsSafe(true); e != nil {
		t.Fatal(e)
	}
	return r, s, d, &waits
}
func putFixture(t *testing.T, path, body string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func findLayer(t *testing.T, r Report, name string) Layer {
	t.Helper()
	for _, l := range r.Layers {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("missing layer %s", name)
	return Layer{}
}
func TestRuntimeDoctorLayersAreReadOnlyAndTruthful(t *testing.T) {
	r, s, d, _ := fixtureRuntime(t)
	s.state.Running = true
	s.state.PID = 222
	d.unavailable = false
	d.status.PID = 222
	d.status.Sessions = []agentd.RuntimeSession{{State: agentd.StateOwnershipLost}, {PID: 333, State: agentd.StateRunning, Owned: true, WorkspaceVerified: false}}
	putFixture(t, filepath.Join(r.directory, "sessions.journal"), "malformed\n")
	before, _ := os.ReadFile(filepath.Join(r.directory, "sessions.journal"))
	report := r.Doctor(context.Background())
	after, _ := os.ReadFile(filepath.Join(r.directory, "sessions.journal"))
	if !bytes.Equal(before, after) || s.starts != 0 || s.stops != 0 || report.Ready {
		t.Fatal("doctor mutated or invented readiness")
	}
	for _, n := range []string{"cli_auth", "server_identity", "daemon_service", "private_paths", "reporter_lease", "targets", "consumers", "stale_generations", "workspace_ownership", "browser_intents", "journal"} {
		findLayer(t, report, n)
	}
	if findLayer(t, report, "journal").Code != "journal_corrupt_or_unsafe" || findLayer(t, report, "stale_generations").State != ActionRequired || findLayer(t, report, "workspace_ownership").State != ActionRequired {
		t.Fatal("missing negative evidence")
	}
}
func TestRuntimeSetupIdempotentAndRepairStaleSocketLock(t *testing.T) {
	r, s, _, _ := fixtureRuntime(t)
	lock, e := lockState(r.directory, "agentd.lock")
	if e != nil {
		t.Fatal(e)
	}
	lock.Close()
	original, _ := os.Stat(filepath.Join(r.directory, "agentd.lock"))
	socket := filepath.Join(r.directory, "agentd.sock")
	ln, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	_ = os.Chmod(socket, 0600)
	if _, e = r.Setup(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Setup(context.Background()); e != nil {
		t.Fatal(e)
	}
	current, _ := os.Stat(filepath.Join(r.directory, "agentd.lock"))
	if !os.SameFile(original, current) || s.starts != 1 {
		t.Fatal("setup not idempotent or lock inode replaced")
	}
	if _, e = os.Lstat(socket); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("stale socket retained")
	}
}
func TestRuntimeHeldLockIsNeverStale(t *testing.T) {
	r, s, _, _ := fixtureRuntime(t)
	l, e := agentd.AcquireInstanceLock(r.StateRoot, r.Instance)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	_, e = r.Repair(context.Background())
	if e == nil || s.starts != 0 || s.stops != 0 {
		t.Fatal("repair touched live lock owner")
	}
}
func TestRuntimeRepairCircuitPersistsAndAttentionCoalesces(t *testing.T) {
	r, s, _, waits := fixtureRuntime(t)
	s.crash = true
	if _, e := r.Repair(context.Background()); e == nil {
		t.Fatal("crash reported success")
	}
	if s.starts != 3 {
		t.Fatalf("starts=%d", s.starts)
	}
	b, e := readPrivate(filepath.Join(r.directory, "runtime-attention.json"), 4096)
	if e != nil {
		t.Fatal(e)
	}
	if len(*waits) != 3 || (*waits)[0] != time.Second || (*waits)[1] != 2*time.Second || (*waits)[2] != 4*time.Second {
		t.Fatal("backoff not bounded")
	}
	for range 4 {
		_, _ = r.Repair(context.Background())
	}
	after, _ := readPrivate(filepath.Join(r.directory, "runtime-attention.json"), 4096)
	if s.starts != 3 || !bytes.Equal(b, after) {
		t.Fatal("circuit bypass or repeated attention")
	}
}
func TestRuntimeRepairPreservesCorruptionAndUnsafePaths(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink", "hardlink", "mode"} {
		t.Run(kind, func(t *testing.T) {
			r, s, _, _ := fixtureRuntime(t)
			p := filepath.Join(r.directory, "sessions.journal")
			switch kind {
			case "corrupt":
				putFixture(t, p, "broken\n")
			case "symlink":
				e := os.Symlink(filepath.Join(t.TempDir(), "unrelated"), p)
				if e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				other := filepath.Join(t.TempDir(), "unrelated")
				putFixture(t, other, "private")
				if e := os.Link(other, p); e != nil {
					t.Fatal(e)
				}
			case "mode":
				putFixture(t, p, "")
				_ = os.Chmod(p, 0644)
			}
			if _, e := r.Repair(context.Background()); e == nil || s.starts != 0 || s.stops != 0 {
				t.Fatal("unsafe repair authorized")
			}
		})
	}
}
func TestRuntimeResetExactPreviewPreservesBoundaries(t *testing.T) {
	r, s, d, _ := fixtureRuntime(t)
	s.state.Running = true
	s.state.Loaded = true
	s.state.PID = 222
	d.unavailable = false
	d.status.PID = 222
	d.status.Sessions = []agentd.RuntimeSession{{PID: 333, Owned: true, State: agentd.StateRunning}, {PID: 444, Owned: false, State: agentd.StateOwnershipLost}}
	putFixture(t, filepath.Join(r.directory, "sessions.journal"), "")
	putFixture(t, filepath.Join(r.directory, "sessions.checkpoint.json"), `{"version":1,"records":[]}`)
	preserved := []string{"reporter-key", "config.yaml", "unrelated.log"}
	for _, n := range preserved {
		putFixture(t, filepath.Join(r.directory, n), "fixture-preserve")
	}
	if e := os.Mkdir(filepath.Join(r.directory, "reporter-leases"), 0700); e != nil {
		t.Fatal(e)
	}
	putFixture(t, filepath.Join(r.directory, "reporter-leases", "lease"), "fixture-private")
	before, e := r.PreviewReset(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(before.Plan.Processes) != 2 || s.stops != 0 {
		t.Fatal("preview touched or adopted unknown PID")
	}
	changed := *before.Plan
	d.status.DaemonID = "replacement"
	if _, e = r.Reset(context.Background(), changed.Token); e == nil || d.quiesces != 0 {
		t.Fatal("stale preview accepted")
	}
	d.status.DaemonID = "fixture-generation"
	result, e := r.Reset(context.Background(), before.Plan.Token)
	if e != nil {
		t.Fatal(e)
	}
	if d.quiesces != 1 || s.state.Running {
		t.Fatal("owned runtime not stopped")
	}
	for _, n := range preserved {
		b, e := os.ReadFile(filepath.Join(r.directory, n))
		if e != nil || string(b) != "fixture-preserve" {
			t.Fatal("preservation violated")
		}
	}
	i, _ := os.Stat(result.Archive)
	if i.Mode().Perm() != 0700 {
		t.Fatal("archive exposed")
	}
	b, e := readPrivate(filepath.Join(result.Archive, "sessions.checkpoint.json"), 4096)
	if e != nil || len(b) == 0 {
		t.Fatal("checkpoint not recoverable")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "fixture-private") || strings.Contains(string(raw), "reporter-key") {
		t.Fatal("private data printed")
	}
}
func TestRuntimeResetCorruptOriginalsQuarantinedPrivately(t *testing.T) {
	r, _, _, _ := fixtureRuntime(t)
	putFixture(t, filepath.Join(r.directory, "sessions.journal"), "unclassified fixture\n")
	putFixture(t, filepath.Join(r.directory, "agentd.log"), "unclassified log\n")
	p, e := r.PreviewReset(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	out, e := r.Reset(context.Background(), p.Plan.Token)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"sessions.journal", "agentd.log"} {
		if _, e := safeFile(filepath.Join(out.Archive, "quarantine", name), false); e != nil {
			t.Fatal(e)
		}
	}
	if r.checkJournal() != nil {
		t.Fatal("reset did not clear corrupt active state")
	}
	if _, e := os.Stat(filepath.Join(out.Archive, "manifest.json")); e != nil {
		t.Fatal(e)
	}
}
func TestRuntimeResetBlocksUnownedServicePID(t *testing.T) {
	r, s, d, _ := fixtureRuntime(t)
	s.state.Running = true
	s.state.PID = 200
	d.unavailable = false
	d.status.PID = 201
	if _, e := r.PreviewReset(context.Background()); e == nil || s.stops != 0 {
		t.Fatal("unowned service accepted")
	}
}

func TestRuntimePlatformCrashCountStopsServiceBeforeRetry(t *testing.T) {
	r, s, _, _ := fixtureRuntime(t)
	s.state.Restarts = 9
	if _, e := r.Repair(context.Background()); e == nil || s.starts != 0 || s.stops != 1 {
		t.Fatal("platform crash loop not stopped")
	}
	c, e := r.readCircuit()
	if e != nil || !c.Open {
		t.Fatal("platform crash circuit not persisted")
	}
}
func TestRuntimeOpenCircuitCompletesInterruptedStop(t *testing.T) {
	r, s, d, _ := fixtureRuntime(t)
	s.state.Running = true
	s.state.PID = 222
	d.unavailable = false
	d.status.PID = 222
	if e := r.saveCircuit(circuit{Attempts: 3, Open: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Repair(context.Background()); e == nil || s.state.Running || d.quiesces != 1 {
		t.Fatal("interrupted circuit left runtime spinning")
	}
}
func TestRuntimeLeaseMetadataIsCheckedWithoutReadingKeys(t *testing.T) {
	r, _, _, _ := fixtureRuntime(t)
	dir := filepath.Join(r.directory, "reporter-leases")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "fixture")
	putFixture(t, p, "unread fixture lease")
	_ = os.Chmod(p, 0644)
	if findLayer(t, r.Doctor(context.Background()), "private_paths").State != ActionRequired {
		t.Fatal("unsafe reporter lease metadata accepted")
	}
}

func TestRuntimeLiveUnownedSocketCannotBeRotatedOrReset(t *testing.T) {
	r, s, _, _ := fixtureRuntime(t)
	path := filepath.Join(r.directory, "agentd.sock")
	listener, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = r.PreviewReset(context.Background()); e == nil {
		t.Fatal("live unowned socket accepted for reset")
	}
	if _, e = r.Repair(context.Background()); e == nil || s.starts != 0 {
		t.Fatal("live socket rotated")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("live socket removed")
	}
}

func TestRuntimeThirdAttemptCanSucceedWithoutResettingBudget(t *testing.T) {
	r, s, d, _ := fixtureRuntime(t)
	s.succeedAfter = 3
	if _, e := r.Repair(context.Background()); e != nil || s.starts != 3 {
		t.Fatal("third successful attempt rejected")
	}
	c, e := r.readCircuit()
	if e != nil || c.Attempts != 3 || c.Open {
		t.Fatal("successful attempt corrupted persistent budget")
	}
	if _, e = r.Repair(context.Background()); e != nil || s.starts != 3 {
		t.Fatal("healthy runtime restarted")
	}
	s.state.Running = false
	s.state.PID = 0
	d.unavailable = true
	if _, e = r.Repair(context.Background()); e == nil || s.starts != 3 {
		t.Fatal("spent budget restarted crashed runtime")
	}
	c, e = r.readCircuit()
	if e != nil || !c.Open {
		t.Fatal("subsequent crash did not trip circuit")
	}
}
