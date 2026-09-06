// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

func (r *Runtime) Doctor(ctx context.Context) Report {
	out := r.report()
	remote := RemoteEvidence{Auth: layer("cli_auth", Unknown, "authentication_unchecked", "configure the named instance"), Identity: layer("server_identity", Unknown, "identity_unchecked", "verify the deployment identity"), Agents: layer("canonical_agents", Unknown, "agents_unchecked", "select an authorized project"), Profiles: layer("dispatch_profiles", Unknown, "profiles_unchecked", "inspect immutable execution profiles"), Targets: layer("targets", Unknown, "targets_unchecked", "inspect authorized target registration")}
	if r.Remote != nil {
		remote = r.Remote(ctx)
	}
	out.Layers = append(out.Layers, remote.Auth, remote.Identity, remote.Agents, remote.Profiles, remote.Targets)
	safe := r.pathsSafe(false) == nil
	paths := layer("private_paths", Known, "private_owned_paths", "")
	if !safe {
		paths = layer("private_paths", ActionRequired, "missing_or_unsafe_private_paths", "run setup for missing directories; operator must resolve unsafe ownership or modes")
	}
	out.Layers = append(out.Layers, paths)
	svc, se := r.Service.Inspect(ctx)
	service := layer("daemon_service", Known, "verified_running_service", "")
	if se != nil || !svc.Verified {
		service = layer("daemon_service", ActionRequired, "service_definition_unverified", "review a direct named-instance service definition; activation: "+r.Service.InstallAction())
	} else if !svc.Running {
		service = layer("daemon_service", ActionRequired, "service_stopped", "run runtime setup; explicit reviewed activation: "+r.Service.InstallAction())
	}
	out.Layers = append(out.Layers, service)
	var status agentd.RuntimeStatus
	var de error = ErrActionRequired
	if safe {
		status, de = r.Daemon.RuntimeStatus(ctx)
	}
	trusted := de == nil && status.Instance == r.Instance && status.DaemonID != "" && status.PID > 0 && svc.Verified && svc.Running && svc.PID == status.PID
	daemonLayer := layer("daemon_generation", Unknown, "generation_unverified", "inspect the verified platform service")
	if trusted && !status.Closed {
		daemonLayer = layer("daemon_generation", Known, "live_owned_generation", "")
	} else if trusted {
		daemonLayer = layer("daemon_generation", ActionRequired, "generation_quiesced", "run runtime repair")
	}
	out.Layers = append(out.Layers, daemonLayer)
	lockLayer := layer("socket_lock", Unknown, "ownership_unchecked", "run runtime doctor after restoring private paths")
	if safe {
		held, err := lockHeld(r.directory)
		_, sockErr := safeFile(filepath.Join(r.directory, "agentd.sock"), true)
		switch {
		case err != nil:
			lockLayer = layer("socket_lock", ActionRequired, "lock_unsafe", "operator must restore private lock metadata")
		case held && trusted && sockErr == nil:
			lockLayer = layer("socket_lock", Known, "live_instance_lock", "")
		case held:
			lockLayer = layer("socket_lock", Unknown, "lock_owner_unavailable", "do not rotate a held lock; inspect the service")
		default:
			lockLayer = layer("socket_lock", ActionRequired, "stopped_or_stale_socket_lock", "run runtime repair")
		}
	}
	out.Layers = append(out.Layers, lockLayer)
	journal := layer("journal", Unknown, "journal_unchecked", "restore private paths")
	if safe {
		if err := r.checkJournal(); err != nil {
			journal = layer("journal", ActionRequired, "journal_corrupt_or_unsafe", "preview runtime reset; corrupt originals are privately preserved")
		} else {
			journal = layer("journal", Known, "journal_valid", "")
		}
	}
	out.Layers = append(out.Layers, journal)
	reporter := layer("reporter_lease", Unknown, "reporter_unchecked", "connect the owned daemon")
	generations := layer("stale_generations", Unknown, "generations_unchecked", "connect the owned daemon")
	workspace := layer("workspace_ownership", Unknown, "workspace_unchecked", "connect the owned daemon")
	if trusted {
		switch {
		case !status.ReporterConfigured:
			reporter = layer("reporter_lease", ActionRequired, "reporter_not_configured", "configure the reporter through the declarative service")
		case status.ReporterUnavailable:
			reporter = layer("reporter_lease", ActionRequired, "reporter_unavailable", "verify reporter authorization and private lease metadata")
		case status.ReporterLastSuccess.IsZero() || r.Now().Sub(status.ReporterLastSuccess) > 90*time.Second || status.ReporterLastSuccess.After(r.Now().Add(5*time.Second)):
			reporter = layer("reporter_lease", Unknown, "reporter_freshness_unknown", "wait for an authenticated reporter cycle")
		default:
			reporter = layer("reporter_lease", Known, "authenticated_reporter_current", "")
		}
		generations = layer("stale_generations", Known, "no_stale_generation_observed", "")
		workspace = layer("workspace_ownership", Known, "owned_workspaces_verified", "")
		for _, s := range status.Sessions {
			if s.State == agentd.StateOwnershipLost {
				generations = layer("stale_generations", ActionRequired, "ownership_lost_preserved", "start a fresh explicit generation; old PIDs are never adopted")
			}
			if s.Owned && !s.WorkspaceVerified {
				workspace = layer("workspace_ownership", ActionRequired, "workspace_provenance_changed", "operator must resolve workspace provenance; repair never edits repositories")
			}
			if s.Owned && !s.ReporterBound && reporter.State == Known {
				reporter = layer("reporter_lease", Unknown, "session_lease_unconfirmed", "wait for authenticated session registration")
			}
		}
	}
	out.Layers = append(out.Layers, reporter, generations, workspace)
	extensions := []Layer{layer("consumers", Unknown, "consumer_supervision_unavailable", "PAI-923 must provide authenticated generation-scoped consumer evidence"), layer("browser_intents", Unknown, "browser_intents_unavailable", "PAI-924 must provide authenticated intent readiness")}
	if trusted && r.Extensions != nil {
		for _, l := range r.Extensions(ctx, status) {
			for i := range extensions {
				if l.Name == extensions[i].Name {
					extensions[i] = l
				}
			}
		}
	}
	out.Layers = append(out.Layers, extensions...)
	if safe {
		c, e := r.readCircuit()
		if e != nil {
			out.Layers = append(out.Layers, layer("repair_budget", ActionRequired, "repair_state_unreadable", "preview runtime reset"))
		} else if c.Open {
			out.Layers = append(out.Layers, layer("repair_budget", ActionRequired, "repair_circuit_open", "read durable runtime attention; preview reset after correcting the cause"))
		} else {
			out.Layers = append(out.Layers, layer("repair_budget", Known, "repair_budget_available", ""))
		}
	}
	out.Ready = true
	for _, l := range out.Layers {
		if l.State != Known && l.State != Repaired {
			out.Ready = false
		}
	}
	return out
}
func (r *Runtime) pathsSafe(create bool) error {
	if e := privateDir(r.StateRoot, create); e != nil {
		return e
	}
	if e := privateDir(r.directory, create); e != nil {
		return e
	}
	for _, n := range []string{"agentd.lock", "sessions.journal", "sessions.checkpoint.json", "runtime-repair.json", "runtime-attention.json", "runtime-operation.lock", "agentd.log", "agentd.stdout.log", "agentd.stderr.log"} {
		if _, e := safeFile(filepath.Join(r.directory, n), false); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if _, e := safeFile(filepath.Join(r.directory, "agentd.sock"), true); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	leases := filepath.Join(r.directory, "reporter-leases")
	if e := privateDir(leases, false); e != nil {
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	} else {
		directory, e := os.Open(leases)
		if e != nil {
			return errors.New("reporter lease metadata unavailable")
		}
		entries, e := directory.ReadDir(4097)
		_ = directory.Close()
		if e != nil && !errors.Is(e, io.EOF) || len(entries) > 4096 {
			return errors.New("reporter lease metadata exceeds bound")
		}
		for _, entry := range entries {
			if _, e := safeFile(filepath.Join(leases, entry.Name()), false); e != nil {
				return errors.New("reporter lease metadata unsafe")
			}
		}
	}
	if !writableDirectory(r.directory) {
		return errors.New("private state is not writable")
	}

	return nil
}
func (r *Runtime) checkJournal() error {
	read := func(name string) ([]byte, error) {
		b, e := readPrivate(filepath.Join(r.directory, name), 2<<20)
		if errors.Is(e, os.ErrNotExist) {
			return nil, nil
		}
		return b, e
	}
	c, e := read("sessions.checkpoint.json")
	if e != nil {
		return e
	}
	j, e := read("sessions.journal")
	if e != nil {
		return e
	}
	return agentd.ValidateRuntimeJournal(c, j)
}
