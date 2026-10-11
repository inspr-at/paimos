// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

var ErrRoutineUnavailable = errors.New("routine runtime/workspace qualification unavailable")

// RoutineRepository is local, owner-approved configuration. It is never
// populated from a run's prompt, server path or repository URL. Root is a
// private directory outside Repository; BaseCommit is an approved exact SHA.
type RoutineRepository struct {
	TenantID, OwnerID, ProjectID, AccountID, ComputerID string
	Repository, Root, BaseCommit                        string
	DaemonDigest, CapabilityDigest, HostMappingDigest   string
}

type RoutineResourceOwner struct{ TenantID, AccountID, HostID, RunID string }

// RoutineResourceLease is the AEON-1089 runisolation.Lease surface. S14 wiring
// adapts runisolation.Acquire; S11 never creates a second port/DB allocator.
// Missing wiring refuses launch. Finish(false) retains disputed resources.
type RoutineResourceLease interface {
	Environment() []string
	HandoffPorts()
	Running(int) error
	Finish(bool) error
}

type RoutineWorkspaceConfig struct {
	Repositories     []RoutineRepository
	AcquireResources func(context.Context, RoutineResourceOwner) (RoutineResourceLease, error)
}

// RoutineExecutionAdapter is independently qualified by S10/S15. Existing
// native/manual adapters do not acquire routine authority by implementing Start.
type RoutineExecutionAdapter interface {
	RoutineExecutionSupported(agentsetup.RoutineGrant) bool
}

// RoutineFixtureExitObserver proves every fixture using the resource lease
// exited by exact identity. Root exit alone leaves ports/DB ownership disputed.
type RoutineFixtureExitObserver interface{ RoutineFixturesExited() bool }

type RoutineWorkspace struct {
	Grant  agentsetup.RoutineGrant `json:"grant"`
	ID     string                  `json:"id"`
	Path   string                  `json:"path"`
	Branch string                  `json:"branch"`
	// Continuations keep the checkout identity, while Grant binds the new
	// launch. Empty preserves the original schema-5 allocation representation.
	CheckoutLaunchID string `json:"checkout_launch_id,omitempty"`
}

type routineAllocation struct {
	RoutineWorkspace
	Ready          bool `json:"ready"`
	FixturesExited bool `json:"fixtures_exited,omitempty"`
}

func routineGrant(run Run) (*agentsetup.RoutineGrant, error) {
	var trace struct {
		Grant *agentsetup.RoutineGrant `json:"routine_launch"`
		RunID string                   `json:"routine_run_id"`
	}
	if len(run.Trace) > 262144 {
		return nil, ErrRoutineUnavailable
	}
	if len(run.Trace) > 0 && json.Unmarshal(run.Trace, &trace) != nil {
		return nil, ErrRoutineUnavailable
	}
	if (run.RoutineLaunchID == nil) != (trace.Grant == nil) {
		return nil, ErrRoutineUnavailable
	}
	if trace.RunID != "" && trace.Grant == nil {
		return nil, ErrRoutineUnavailable
	}
	if trace.Grant != nil && (trace.Grant.Validate() != nil || *run.RoutineLaunchID != trace.Grant.LaunchID || trace.Grant.AgentRunID != run.ID || trace.RunID != trace.Grant.RoutineRunID) {
		return nil, ErrRoutineUnavailable
	}
	return trace.Grant, nil
}

func (s *Supervisor) routineRepository(run Run, g agentsetup.RoutineGrant, adapter Adapter) (RoutineRepository, error) {
	qualified, ok := adapter.(RoutineExecutionAdapter)
	if !ok || !qualified.RoutineExecutionSupported(g) || s.routineConfig == nil || s.routineConfig.AcquireResources == nil ||
		g.TenantID != s.tenantID || g.PrincipalID != s.principalID || g.DaemonID != s.daemonID || g.DaemonGeneration != s.generation || g.LedgerGeneration != s.ledgerBinding.Generation ||
		!s.capacityNow().Before(g.ExpiresAt) || g.ExpiresAt.Sub(s.capacityNow()) > 2*time.Minute || run.requestedAccount() != g.AccountID {
		return RoutineRepository{}, ErrRoutineUnavailable
	}
	var found *RoutineRepository
	for _, repository := range s.routineConfig.Repositories {
		if repository.ProjectID != g.ProjectID || repository.AccountID != g.AccountID {
			continue
		}
		if found != nil {
			return RoutineRepository{}, ErrRoutineUnavailable
		}
		r := repository
		found = &r
	}
	if found == nil || found.TenantID != g.TenantID || found.OwnerID != g.OwnerID || found.ComputerID != g.ComputerID || found.DaemonDigest != g.DaemonDigest || found.CapabilityDigest != g.CapabilityDigest || found.HostMappingDigest != g.HostMappingDigest {
		return RoutineRepository{}, ErrRoutineUnavailable
	}
	for _, path := range []string{found.Repository, found.Root} {
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(path) || physical != filepath.Clean(path) {
			return RoutineRepository{}, ErrRoutineUnavailable
		}
	}
	info, err := os.Lstat(found.Root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || pathWithin(found.Repository, found.Root) || pathWithin(found.Root, found.Repository) {
		return RoutineRepository{}, ErrRoutineUnavailable
	}
	if len(found.BaseCommit) != 40 || strings.Trim(found.BaseCommit, "0123456789abcdef") != "" {
		return RoutineRepository{}, ErrRoutineUnavailable
	}
	return *found, nil
}

func (s *Supervisor) prepareRoutineWorkspace(ctx context.Context, run Run, g agentsetup.RoutineGrant, repository RoutineRepository) (*RoutineWorkspace, error) {
	raw, _ := json.Marshal([]string{g.TenantID, g.OwnerID, g.ProjectID, g.AccountID, g.ComputerID, g.LaunchID})
	id := fmt.Sprintf("%x", sha256.Sum256(raw))
	w := RoutineWorkspace{Grant: g, ID: id, Path: filepath.Join(repository.Root, id), Branch: "routine/" + g.LaunchID}
	predecessor := ""
	if run.CapacityHandoff {
		retained, err := s.routineContinuation(ctx, run, g, repository)
		if err != nil {
			return nil, err
		}
		w = *retained
		id, predecessor = w.ID, run.RetryOfRunID
	}
	name := "routine-workspace-" + g.LaunchID + ".json"
	previous, err := s.state.Read(name, 16384)
	if err == nil {
		var saved routineAllocation
		expected, _ := json.Marshal(w)
		if json.Unmarshal(previous, &saved) != nil {
			return nil, ErrRoutineUnavailable
		}
		observed, _ := json.Marshal(saved.RoutineWorkspace)
		if !saved.Ready || string(expected) != string(observed) {
			return nil, ErrProcessesUnconfirmed
		}
		// Reuse is only for the same durable never-launched assignment. A lost
		// allocation response without a journal cannot mint another writer.
		s.mu.Lock()
		entry := s.runs[run.ID]
		s.mu.Unlock()
		if entry == nil {
			return nil, ErrProcessesUnconfirmed
		}
		entry.mu.Lock()
		record := entry.record
		entry.mu.Unlock()
		if record.RoutineWorkspace == nil || record.RoutineWorkspace.ID != id || !noLocalProcess(record) || record.Generation != s.generation {
			return nil, ErrProcessesUnconfirmed
		}
		if err := s.acquireRoutineCheckout(run.ID, w); err != nil {
			return nil, err
		}
		if err := validateRoutineCheckout(ctx, repository, w); err != nil {
			return nil, err
		}
		if s.routineResources[run.ID] == nil {
			lease, err := s.routineConfig.AcquireResources(ctx, RoutineResourceOwner{g.TenantID, g.AccountID, g.ComputerID, g.LaunchID})
			if err != nil {
				return nil, err
			}
			s.routineResources[run.ID] = lease
		}
		return &w, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lease, err := s.routineConfig.AcquireResources(ctx, RoutineResourceOwner{g.TenantID, g.AccountID, g.ComputerID, g.LaunchID})
	if err != nil {
		return nil, err
	}
	// Retain every partial allocation; neither a Git error nor a crashed
	// caller proves that fixture/resource ownership has been reconciled.
	s.routineResources[run.ID] = lease
	allocation := routineAllocation{RoutineWorkspace: w}
	encoded, _ := json.Marshal(allocation)
	if err := s.state.Write(name, encoded, true); err != nil {
		return nil, err
	}
	if predecessor != "" {
		// Do not create a new tree or overwrite retained content. Under the
		// existing checkout lock, finish only proven-exited fixture resources
		// and transfer the durable owner to this exact continuation.
		if oldLease := s.routineResources[predecessor]; oldLease != nil {
			if err := oldLease.Finish(true); err != nil {
				return nil, err
			}
			delete(s.routineResources, predecessor)
		}
		if err := s.acquireRoutineCheckoutFrom(run.ID, w, predecessor); err != nil {
			return nil, err
		}
		allocation.Ready = true
		encoded, _ = json.Marshal(allocation)
		if err := s.state.Write(name, encoded, false); err != nil {
			return nil, err
		}
		return &w, nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Git's shared repository metadata is independent of the checkout locks.
	// Serialize worktree creation across accounts/daemons using this repository.
	common, err := routineGit(gitCtx, repository.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || len(common) > 4096 {
		return nil, ErrRoutineUnavailable
	}
	commonPath, err := filepath.EvalSymlinks(strings.TrimSpace(common))
	if err != nil || !filepath.IsAbs(commonPath) {
		return nil, ErrRoutineUnavailable
	}
	repositoryFence, err := agentsetup.OpenStore(filepath.Join(commonPath, "aeon-routine-worktrees"), true)
	if err != nil {
		return nil, err
	}
	defer repositoryFence.Close()
	for {
		err = repositoryFence.Lock()
		if err == nil {
			break
		}
		if !errors.Is(err, agentsetup.ErrBusy) {
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-gitCtx.Done():
			timer.Stop()
			return nil, gitCtx.Err()
		case <-timer.C:
		}
	}
	if _, err := routineGit(gitCtx, repository.Repository, "worktree", "add", "-b", w.Branch, w.Path, repository.BaseCommit); err != nil {
		return nil, errors.New("routine worktree allocation unconfirmed")
	}
	if err := validateRoutineCheckout(gitCtx, repository, w); err != nil {
		return nil, err
	}
	if err := s.acquireRoutineCheckout(run.ID, w); err != nil {
		return nil, err
	}
	allocation.Ready = true
	encoded, _ = json.Marshal(allocation)
	if err := s.state.Write(name, encoded, false); err != nil {
		return nil, err
	}
	return &w, nil
}

// A continuation is local recovery of the same routine lineage, not authority
// to adopt another owner's checkout. Root exit alone never clears fixtures.
func (s *Supervisor) routineContinuation(ctx context.Context, run Run, g agentsetup.RoutineGrant, repository RoutineRepository) (*RoutineWorkspace, error) {
	s.mu.Lock()
	previous := s.runs[run.RetryOfRunID]
	s.mu.Unlock()
	if previous == nil || run.RetryOfRunID == "" || run.RetryOfRunID == run.ID {
		return nil, ErrProcessesUnconfirmed
	}
	previous.mu.Lock()
	r, process := previous.record, previous.process
	previous.mu.Unlock()
	if r.RoutineWorkspace == nil || r.RunID != run.RetryOfRunID || r.WorkOrderID != run.WorkOrderID || r.TenantID != s.tenantID || r.PrincipalID != s.principalID || r.State != "failed" || !r.ExitObserved || r.Workspace != r.RoutineWorkspace.Path || r.LaunchBranch != r.RoutineWorkspace.Branch || validateLaunchRecord(r) != nil {
		return nil, ErrProcessesUnconfirmed
	}
	old := r.RoutineWorkspace.Grant
	if old.TenantID != g.TenantID || old.OwnerID != g.OwnerID || old.ExecutionPrincipalID != g.ExecutionPrincipalID || old.PrincipalID != g.PrincipalID || old.ProjectID != g.ProjectID || old.RoutineRunID != g.RoutineRunID || old.AccountID != g.AccountID || old.ComputerID != g.ComputerID || old.DaemonID != g.DaemonID || old.DaemonDigest != g.DaemonDigest || old.CapabilityDigest != g.CapabilityDigest || old.HostMappingDigest != g.HostMappingDigest || old.RuntimeCapability != g.RuntimeCapability || old.QualificationID != g.QualificationID {
		return nil, ErrProcessesUnconfirmed
	}
	w := *r.RoutineWorkspace
	if w.Path != filepath.Join(repository.Root, w.ID) {
		return nil, ErrProcessesUnconfirmed
	}
	if err := validateRoutineCheckout(ctx, repository, w); err != nil {
		return nil, err
	}
	if err := s.confirmRoutineFixtureExit(r, process); err != nil {
		return nil, err
	}
	if w.CheckoutLaunchID == "" {
		w.CheckoutLaunchID = old.LaunchID
	}
	w.Grant = g
	return &w, nil
}

// Persist exact allocation-bound fixture proof before transferring or releasing
// its owner. Missing in-memory leases after a restart are never exit evidence.
func (s *Supervisor) confirmRoutineFixtureExit(r Record, process Process) error {
	if !r.ExitObserved || r.RoutineWorkspace == nil {
		return ErrProcessesUnconfirmed
	}
	name := "routine-workspace-" + r.RoutineWorkspace.Grant.LaunchID + ".json"
	raw, err := s.state.Read(name, 16384)
	if err != nil {
		return ErrProcessesUnconfirmed
	}
	var saved routineAllocation
	if json.Unmarshal(raw, &saved) != nil || !saved.Ready {
		return ErrProcessesUnconfirmed
	}
	expected, _ := json.Marshal(r.RoutineWorkspace)
	observed, _ := json.Marshal(saved.RoutineWorkspace)
	if string(expected) != string(observed) {
		return ErrProcessesUnconfirmed
	}
	if saved.FixturesExited {
		return nil
	}
	observer, ok := process.(RoutineFixtureExitObserver)
	if !ok || !observer.RoutineFixturesExited() {
		return ErrProcessesUnconfirmed
	}
	saved.FixturesExited = true
	raw, _ = json.Marshal(saved)
	return s.state.Write(name, raw, false)
}

func validateRoutineCheckout(ctx context.Context, r RoutineRepository, w RoutineWorkspace) error {
	physical, err := filepath.EvalSymlinks(w.Path)
	if err != nil || physical != w.Path {
		return ErrRoutineUnavailable
	}
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	branch, err := routineGit(gitCtx, w.Path, "symbolic-ref", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != w.Branch {
		return ErrProcessesUnconfirmed
	}
	common, err := routineGit(gitCtx, w.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	approved, approvedErr := routineGit(gitCtx, r.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || approvedErr != nil || common != approved {
		return ErrProcessesUnconfirmed
	}
	if _, err := routineGit(gitCtx, w.Path, "merge-base", "--is-ancestor", r.BaseCommit, "HEAD"); err != nil {
		return ErrProcessesUnconfirmed
	}
	return nil
}

// Allocation must not execute repository hooks, filters, fsmonitor helpers or
// lazy network fetches before the qualified native launch boundary. Reuse the
// review helper's bounded output/environment discipline; do not read config
// values (which could include credentials), only bounded filter key names.
func routineGit(ctx context.Context, repository string, args ...string) (string, error) {
	flags := []string{"--no-replace-objects", "--no-pager", "-C", repository, "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.attributesFile=" + os.DevNull}
	environment := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "LC_ALL=") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	run := func(arguments []string) (string, error) {
		command := exec.CommandContext(ctx, "git", arguments...)
		command.Env = environment
		out := &boundedReviewBuffer{limit: 192 << 10}
		command.Stdout = out
		err := command.Run()
		return out.String(), err
	}
	// Read effective key names in this exact worktree: includes, includeIf and
	// extensions.worktreeConfig all participate in checkout configuration.
	if len(args) > 0 && (args[0] == "worktree" || args[0] == "checkout") {
		keys, err := run(append(append([]string(nil), flags...), "config", "--includes", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`))
		var exit *exec.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1 && keys == "") {
			return "", ErrRoutineUnavailable
		}
		sections := map[string]bool{}
		for _, key := range strings.Split(strings.TrimSuffix(keys, "\x00"), "\x00") {
			if key == "" {
				continue
			}
			index := strings.LastIndex(key, ".")
			if index < 7 || len(key) > 160 || strings.ContainsAny(key, "=\x00\r\n\t ") {
				return "", ErrRoutineUnavailable
			}
			sections[key[:index]] = true
		}
		if len(sections) > 64 {
			return "", ErrRoutineUnavailable
		}
		for section := range sections {
			flags = append(flags, "-c", section+".clean=", "-c", section+".smudge=", "-c", section+".process=", "-c", section+".required=false")
		}
	}
	// A destination-specific includeIf can differ from the source worktree's
	// config. Create only metadata first, then sanitize the destination's own
	// effective config before materializing any tracked files.
	if len(args) == 6 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		creation := append([]string{"worktree", "add", "--no-checkout"}, args[2:]...)
		if _, err := run(append(flags, creation...)); err != nil {
			return "", ErrRoutineUnavailable
		}
		return routineGit(ctx, args[4], "checkout", "HEAD", "--", ".")
	}
	out, err := run(append(flags, args...))
	if err != nil {
		return "", ErrRoutineUnavailable
	}
	return out, nil
}

func (s *Supervisor) acquireRoutineCheckout(runID string, w RoutineWorkspace) error {
	return s.acquireRoutineCheckoutFrom(runID, w, "")
}

func (s *Supervisor) acquireRoutineCheckoutFrom(runID string, w RoutineWorkspace, predecessor string) error {
	if s.routineCheckouts[runID] != nil {
		return nil
	}
	physical, err := filepath.EvalSymlinks(w.Path)
	if err != nil || physical != w.Path {
		return ErrRoutineUnavailable
	}
	store := s.routineCheckouts[predecessor]
	reused := store != nil
	if !reused {
		store, err = agentsetup.OpenStore(filepath.Join(w.Path, ".aeon-agentd"), true)
		if err != nil {
			return err
		}
	}
	keep := false
	defer func() {
		if !keep && !reused {
			_ = store.Close()
		}
	}()
	if !reused {
		if err := store.Lock(); err != nil {
			return err
		}
	}
	owner, _ := json.Marshal([]string{s.tenantID, s.principalID, s.daemonID, s.state.Path(), runID, w.ID})
	previous, err := store.Read("owner.json", 4096)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && string(previous) != "[]" && string(previous) != string(owner) {
		priorOwner, _ := json.Marshal([]string{s.tenantID, s.principalID, s.daemonID, s.state.Path(), predecessor, w.ID})
		if predecessor == "" || string(previous) != string(priorOwner) {
			return ErrProcessesUnconfirmed
		}
	}
	if err := store.Write("owner.json", owner, false); err != nil {
		return err
	}
	s.routineCheckouts[runID] = store
	if reused {
		delete(s.routineCheckouts, predecessor)
	}
	keep = true
	return nil
}

func (s *Supervisor) claimRoutine(ctx context.Context, run Run, ids []string, pickup *WorkerPickup, w *RoutineWorkspace) error {
	api, ok := s.api.(interface {
		ClaimRoutine(context.Context, string, string, string, []string, *WorkerPickup, agentsetup.RoutinePickup) error
	})
	if !ok {
		return ErrRoutineUnavailable
	}
	return api.ClaimRoutine(ctx, run.ID, s.daemonID, s.generation, ids, pickup, w.pickup())
}

func (s *Supervisor) routeWorkspace(ctx context.Context, runID string, ids []string, w *RoutineWorkspace) (Route, error) {
	if w == nil {
		return s.api.Route(ctx, runID, s.daemonID, ids, s.estimates)
	}
	api, ok := s.api.(interface {
		RouteRoutine(context.Context, string, string, []string, map[string]int64, agentsetup.RoutinePickup) (Route, error)
	})
	if !ok || w.Grant.Validate() != nil || w.Grant.AgentRunID != runID || w.Grant.TenantID != s.tenantID || w.Grant.PrincipalID != s.principalID || w.Grant.DaemonID != s.daemonID || w.Grant.LedgerGeneration != s.ledgerBinding.Generation {
		return Route{}, ErrRoutineUnavailable
	}
	return api.RouteRoutine(ctx, runID, s.daemonID, ids, s.estimates, w.pickup())
}

func (w RoutineWorkspace) pickup() agentsetup.RoutinePickup {
	g := w.Grant
	return agentsetup.RoutinePickup{LaunchID: g.LaunchID, WorktreeID: w.ID, Capability: g.Capability, DaemonDigest: g.DaemonDigest, CapabilityDigest: g.CapabilityDigest, ComputerID: g.ComputerID}
}

// Caller holds dispatchMu. A terminal route_pending launch state is durable
// proof that neither the adapter nor any native fixture was launched. Keep the
// journal and allocation, while releasing only this attempt's live owners.
func (s *Supervisor) releaseObsoleteRoutineRoute(r Record) error {
	if r.RoutineWorkspace == nil || r.LaunchState != launchRoutePending || (r.State != "cancelled" && r.State != "failed" && r.State != "completed") {
		return ErrProcessesUnconfirmed
	}
	if lease := s.routineResources[r.RunID]; lease != nil {
		if err := lease.Finish(true); err != nil {
			return err
		}
		delete(s.routineResources, r.RunID)
	}
	if store := s.routineCheckouts[r.RunID]; store != nil {
		if err := store.Write("owner.json", []byte("[]"), false); err != nil {
			return err
		}
		if err := store.Close(); err != nil {
			return err
		}
		delete(s.routineCheckouts, r.RunID)
	}
	return nil
}

func (s *Supervisor) releaseRoutineCheckouts() error {
	var resultErr error
	for runID, lease := range s.routineResources {
		s.mu.Lock()
		entry := s.runs[runID]
		s.mu.Unlock()
		confirmed := false
		if entry != nil {
			entry.mu.Lock()
			record, process := entry.record, entry.process
			entry.mu.Unlock()
			confirmed = record.LaunchState == launchPrepared || record.LaunchState == launchRoutePending
			if record.ExitObserved {
				confirmed = s.confirmRoutineFixtureExit(record, process) == nil
			}
		}
		if !confirmed {
			// Keep both the live lease and durable checkout owner. Closing the
			// root process does not stop a still-disputed fixture writer.
			resultErr = errors.Join(resultErr, ErrProcessesUnconfirmed)
			continue
		}
		if err := lease.Finish(true); err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		delete(s.routineResources, runID)
		if store := s.routineCheckouts[runID]; store != nil {
			if err := store.Write("owner.json", []byte("[]"), false); err != nil {
				resultErr = errors.Join(resultErr, err)
				continue
			}
			if err := store.Close(); err != nil {
				resultErr = errors.Join(resultErr, err)
				continue
			}
			delete(s.routineCheckouts, runID)
		}
	}
	if len(s.routineCheckouts) > 0 {
		resultErr = errors.Join(resultErr, ErrProcessesUnconfirmed)
	}
	return resultErr
}

func (s *Supervisor) routineEnvironment(runID string, g agentsetup.RoutineGrant) ([]string, error) {
	lease := s.routineResources[runID]
	if lease == nil {
		return nil, ErrProcessesUnconfirmed
	}
	environment := lease.Environment()
	if len(environment) != 8 {
		return nil, ErrRoutineUnavailable
	}
	allowed := map[string]bool{"AEON_RUN_ID": true, "AEON_RUN_DATABASE": true, "AEON_RUN_APP_PORT": true, "AEON_RUN_POSTGRES_PORT": true, "AEON_RUN_TEMP_DIR": true, "TMPDIR": true, "TMP": true, "TEMP": true}
	values := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !allowed[key] || values[key] != "" || value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrRoutineUnavailable
		}
		values[key] = value
	}
	if values["AEON_RUN_ID"] != g.LaunchID || len(values["AEON_RUN_DATABASE"]) != 41 || !strings.HasPrefix(values["AEON_RUN_DATABASE"], "aeon_run_") || strings.Trim(strings.TrimPrefix(values["AEON_RUN_DATABASE"], "aeon_run_"), "0123456789abcdef") != "" {
		return nil, ErrRoutineUnavailable
	}
	a, e1 := strconv.Atoi(values["AEON_RUN_APP_PORT"])
	b, e2 := strconv.Atoi(values["AEON_RUN_POSTGRES_PORT"])
	if e1 != nil || e2 != nil || a < 1 || b < 1 || a > 65535 || b > 65535 || a == b {
		return nil, ErrRoutineUnavailable
	}
	tmp := values["AEON_RUN_TEMP_DIR"]
	physical, err := filepath.EvalSymlinks(tmp)
	if err != nil || !filepath.IsAbs(tmp) || physical != tmp || values["TMPDIR"] != tmp || values["TMP"] != tmp || values["TEMP"] != tmp {
		return nil, ErrRoutineUnavailable
	}
	return append([]string(nil), environment...), nil
}

// These overrides originate only in a validated allocator lease, never in
// terminal/tool input. Existing manual calls pass nil and retain their policy.
func routineTempRoot(resources []string) string {
	for _, entry := range resources {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "AEON_RUN_TEMP_DIR" {
			return value
		}
	}
	return ""
}

func routineResourceEnvironment(base, resources []string) []string {
	if len(resources) == 0 {
		return base
	}
	keys := map[string]bool{}
	for _, entry := range resources {
		key, _, _ := strings.Cut(entry, "=")
		keys[key] = true
	}
	out := make([]string, 0, len(base)+len(resources))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !keys[key] {
			out = append(out, entry)
		}
	}
	return append(out, resources...)
}
