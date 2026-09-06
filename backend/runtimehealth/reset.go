// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type ProcessPreview struct {
	PID  int    `json:"pid"`
	Kind string `json:"kind"`
}
type ResetPlan struct {
	AbsentPaths []string         `json:"absent_paths"`
	Sessions    []string         `json:"-"`
	Token       string           `json:"token"`
	CanApply    bool             `json:"can_apply"`
	Processes   []ProcessPreview `json:"processes"`
	Paths       []string         `json:"paths"`
	Generation  string           `json:"-"`
	Definition  string           `json:"-"`
}

// Only this closed list is eligible for reset. Reporter lease storage, keys,
// config, arbitrary files and all paths outside this instance are preserved.
var resetFiles = []string{"sessions.checkpoint.json", "sessions.journal", "agentd.log", "agentd.stdout.log", "agentd.stderr.log", "runtime-repair.json", "runtime-attention.json"}

func (r *Runtime) PreviewReset(ctx context.Context) (Report, error) {
	out := r.report()
	plan, e := r.preview(ctx)
	out.Plan = &plan
	if e != nil {
		out.Layers = append(out.Layers, layer("reset", ActionRequired, "ownership_or_private_paths_unverified", "restore service ownership and private file metadata before reset"))
		return out, ErrActionRequired
	}
	out.Layers = append(out.Layers, layer("reset", Known, "preview_only", "apply only this preview with runtime reset --confirm "+plan.Token))
	return out, nil
}
func (r *Runtime) preview(ctx context.Context) (ResetPlan, error) {
	p := ResetPlan{Processes: []ProcessPreview{}, Paths: []string{}}
	if r.pathsSafe(false) != nil {
		return p, ErrActionRequired
	}
	svc, e := r.Service.Inspect(ctx)
	if e != nil || !svc.Verified {
		return p, ErrActionRequired
	}
	p.Definition = svc.Definition
	held, e := lockHeld(r.directory)
	if e != nil {
		return p, ErrActionRequired
	}
	if svc.Running || held {
		status, e := r.Daemon.RuntimeStatus(ctx)
		if e != nil || status.Instance != r.Instance || status.PID != svc.PID || !svc.Running || status.DaemonID == "" {
			return p, ErrActionRequired
		}
		p.Generation = status.DaemonID
		p.Processes = append(p.Processes, ProcessPreview{status.PID, "owned_daemon"})
		for _, s := range status.Sessions {
			if s.Owned {
				if s.PID <= 0 {
					return p, ErrActionRequired
				}
				p.Processes = append(p.Processes, ProcessPreview{s.PID, "owned_child"})
				p.Sessions = append(p.Sessions, s.ID)
			}
		}
	}
	if !svc.Running && !held {
		if e = r.verifyStaleSocket(); e != nil {
			return p, ErrActionRequired
		}
	}
	sort.Slice(p.Processes, func(i, j int) bool { return p.Processes[i].PID < p.Processes[j].PID })
	sort.Strings(p.Sessions)
	hash := sha256.New()
	fmt.Fprintln(hash, p.Sessions)
	fmt.Fprintln(hash, r.Instance, p.Generation, svc.Definition, svc.PID, svc.Loaded)
	for _, process := range p.Processes {
		fmt.Fprintln(hash, process.PID, process.Kind)
	}
	names := append(append([]string{}, resetFiles...), "agentd.sock", "agentd.lock")
	for _, name := range names {
		path := filepath.Join(r.directory, name)
		p.Paths = append(p.Paths, path)
		i, e := safeFile(path, name == "agentd.sock")
		if errors.Is(e, os.ErrNotExist) {
			p.AbsentPaths = append(p.AbsentPaths, path)
			fmt.Fprintln(hash, name, "missing")
			continue
		}
		if e != nil {
			return p, ErrActionRequired
		}
		// Journals may advance with heartbeats. Confirming a preview commits only
		// the exact paths and current generation, not journal byte contents.
		fmt.Fprintln(hash, path, i.Mode(), i.IsDir())
	}
	p.Token = hex.EncodeToString(hash.Sum(nil))
	p.CanApply = true
	return p, nil
}
func (r *Runtime) Reset(ctx context.Context, token string) (out Report, resultErr error) {
	out = r.report()
	defer func() {
		if resultErr != nil && len(out.Layers) == 0 {
			out.Layers = append(out.Layers, layer("reset", ActionRequired, "reset_incomplete", "inspect private archive and current service state; preview again before retry"))
		}
	}()
	if token == "" {
		return r.PreviewReset(ctx)
	}
	if r.pathsSafe(false) != nil {
		return out, ErrActionRequired
	}
	op, e := lockState(r.directory, "runtime-operation.lock")
	if e != nil {
		return out, ErrActionRequired
	}
	defer op.Close()
	plan, e := r.preview(ctx)
	out.Plan = &plan
	if e != nil || token != plan.Token || !plan.CanApply {
		out.Layers = append(out.Layers, layer("reset", ActionRequired, "preview_changed", "preview again; no processes were stopped"))
		return out, ErrActionRequired
	}
	if plan.Generation != "" {
		if e = r.Daemon.QuiesceRuntime(ctx, plan.Generation, plan.Sessions); e != nil {
			out.Layers = append(out.Layers, layer("reset", ActionRequired, "owned_children_not_reaped", "inspect the current daemon; no archive was made"))
			return out, ErrActionRequired
		}
		status, e := r.Daemon.RuntimeStatus(ctx)
		if e != nil || status.DaemonID != plan.Generation || !status.Closed {
			return out, ErrActionRequired
		}
		for _, s := range status.Sessions {
			if s.Owned {
				return out, ErrActionRequired
			}
		}
	}
	if e = r.Service.Stop(ctx); e != nil {
		return out, ErrActionRequired
	}
	// Stop completion alone is insufficient: take the same lock as serve and
	// check the platform PID again before moving any state.
	lock, e := lockState(r.directory, "agentd.lock")
	if e != nil {
		return out, ErrActionRequired
	}
	defer lock.Close()
	service, e := r.Service.Inspect(ctx)
	if e != nil || service.Running || service.Definition != plan.Definition {
		return out, ErrActionRequired
	}
	if e = r.verifyStaleSocket(); e != nil {
		return out, ErrActionRequired
	}
	archiveRoot := filepath.Join(r.StateRoot, "reset-archives")
	if privateDir(archiveRoot, true) != nil {
		return out, ErrActionRequired
	}
	archive, e := os.MkdirTemp(archiveRoot, r.Now().UTC().Format("20060102T150405Z")+"-")
	if e != nil {
		return out, errors.New("private archive creation failed")
	}
	if os.Chmod(archive, 0700) != nil {
		return out, errors.New("private archive protection failed")
	}
	out.Archive = archive
	if syncDir(r.StateRoot) != nil || syncDir(archiveRoot) != nil || syncDir(archive) != nil {
		return out, errors.New("archive directory durability unconfirmed")
	}
	// Raw logs and corrupt journals cannot prove that their content is secret
	// free. Preserve those originals in a distinct private quarantine; the
	// ordinary archive contains validated state and a content-free manifest.
	journalValid := r.checkJournal() == nil
	quarantined := false
	for _, name := range resetFiles {
		source := filepath.Join(r.directory, name)
		if _, e := safeFile(source, false); errors.Is(e, os.ErrNotExist) {
			continue
		} else if e != nil {
			return out, ErrActionRequired
		}
		destination := archive
		known := (name == "sessions.checkpoint.json" || name == "sessions.journal") && journalValid
		if name == "runtime-repair.json" {
			_, e = r.readCircuit()
			known = e == nil
		}
		if !known {
			quarantined = true
			destination = filepath.Join(archive, "quarantine")
			if privateDir(destination, true) != nil {
				return out, ErrActionRequired
			}
			if syncDir(archive) != nil {
				return out, errors.New("quarantine directory durability unconfirmed")
			}
		}
		if e = os.Rename(source, filepath.Join(destination, name)); e != nil {
			return out, errors.New("archive incomplete; original and moved files remain recoverable")
		}
		if syncDir(destination) != nil || syncDir(r.directory) != nil {
			return out, errors.New("archive durability unconfirmed")
		}
	}
	if e = r.removeStaleSocket(); e != nil {
		return out, e
	}
	// Record sockets/locks as inert metadata; never restore a socket or rotate
	// the inode on which serve and reset synchronize.
	manifest := struct {
		Instance   string           `json:"instance"`
		At         time.Time        `json:"at"`
		Paths      []string         `json:"paths"`
		Processes  []ProcessPreview `json:"stopped_processes"`
		Lock       string           `json:"lock"`
		Quarantine bool             `json:"quarantine"`
	}{r.Instance, r.Now().UTC(), plan.Paths, plan.Processes, "retained_unlocked_after_reset", quarantined}
	body, _ := json.Marshal(manifest)
	if writePrivate(filepath.Join(archive, "manifest.json"), body) != nil {
		return out, errors.New("archive manifest durability unconfirmed")
	}
	out.Layers = append(out.Layers, layer("reset", Repaired, "owned_runtime_stopped_and_archived", "bootstrap starts a new generation"))
	if quarantined {
		out.Layers = append(out.Layers, layer("recovery", Preserved, "unclassified_originals_quarantined", "review private originals before any manual restoration"))
	}
	out.Layers = append(out.Layers, layer("ownership_lost_generations", Preserved, "no_pid_adoption", "historical ownership loss cannot prove that an unmanaged process has exited"))
	return out, nil
}
