// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package runtimehealth provides bounded local service recovery. The platform
// manager owns daemon lifetimes; only agentd owns and controls harness children.
package runtimehealth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

type State string

const (
	Known          State = "known"
	Unknown        State = "unknown"
	ActionRequired State = "action_required"
	Repaired       State = "repaired"
	Preserved      State = "preserved"
)

type Layer struct {
	Name   string `json:"name"`
	State  State  `json:"state"`
	Code   string `json:"code"`
	Action string `json:"action,omitempty"`
}
type Report struct {
	Instance  string     `json:"instance"`
	Ready     bool       `json:"ready"`
	Layers    []Layer    `json:"layers"`
	Preserved []string   `json:"preserved"`
	Bootstrap string     `json:"bootstrap"`
	Archive   string     `json:"archive,omitempty"`
	Plan      *ResetPlan `json:"reset,omitempty"`
}

func (r Report) WriteHuman(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "Runtime %s (ready: %t)\n", r.Instance, r.Ready); err != nil {
		return err
	}
	for _, l := range r.Layers {
		if _, err := fmt.Fprintf(w, "%s: %s — %s", l.Name, l.State, l.Code); err != nil {
			return err
		}
		if l.Action != "" {
			fmt.Fprintf(w, "; %s", l.Action)
		}
		fmt.Fprintln(w)
	}
	if r.Plan != nil {
		for _, p := range r.Plan.Processes {
			fmt.Fprintf(w, "process: pid %d (%s)\n", p.PID, p.Kind)
		}
		for _, p := range r.Plan.Paths {
			if slices.Contains(r.Plan.AbsentPaths, p) {
				fmt.Fprintf(w, "eligible path (currently absent): %s\n", p)
			} else {
				fmt.Fprintf(w, "runtime path: %s\n", p)
			}
		}
		fmt.Fprintf(w, "preview: %s\n", r.Plan.Token)
	}
	for _, p := range r.Preserved {
		fmt.Fprintf(w, "preserved: %s\n", p)
	}
	if r.Archive != "" {
		fmt.Fprintf(w, "private archive: %s\n", r.Archive)
	}
	_, err := fmt.Fprintf(w, "next: %s\n", r.Bootstrap)
	return err
}

type RemoteEvidence struct{ Auth, Identity, Agents, Profiles, Targets Layer }
type RemoteProbe func(context.Context) RemoteEvidence

// Future consumers and typed intents report authenticated current-generation
// evidence here. Nil means unknown; a socket alone cannot imply readiness.
type ExtensionProbe func(context.Context, agentd.RuntimeStatus) []Layer

type Daemon interface {
	RuntimeStatus(context.Context) (agentd.RuntimeStatus, error)
	QuiesceRuntime(context.Context, string, []string) error
}
type ServiceState struct {
	Verified, Loaded, Running bool
	PID                       int
	Restarts                  int
	Definition                string
}
type ServiceManager interface {
	Inspect(context.Context) (ServiceState, error)
	Start(context.Context) error
	Stop(context.Context) error
	InstallAction() string
}
type Config struct {
	Instance, StateRoot string
	Service             ServiceManager
	Daemon              Daemon
	Remote              RemoteProbe
	Extensions          ExtensionProbe
	Now                 func() time.Time
	Wait                func(context.Context, time.Duration) error
}
type Runtime struct {
	Config
	directory string
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var ErrActionRequired = errors.New("runtime requires action; inspect readiness layers")

func New(c Config) (*Runtime, error) {
	if !namePattern.MatchString(c.Instance) || !filepath.IsAbs(c.StateRoot) || strings.ContainsAny(c.StateRoot, "\x00\r\n") || filepath.Clean(c.StateRoot) != c.StateRoot || c.Service == nil || c.Daemon == nil {
		return nil, errors.New("runtime requires a safe named instance, absolute state root and service/daemon adapters")
	}
	dir, err := agentd.InstanceStateDir(c.StateRoot, c.Instance)
	if err != nil {
		return nil, errors.New("runtime paths invalid")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Wait == nil {
		c.Wait = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Runtime{Config: c, directory: dir}, nil
}
func layer(name string, state State, code, action string) Layer {
	return Layer{name, state, code, action}
}
func (r *Runtime) report() Report {
	return Report{Instance: r.Instance, Layers: []Layer{}, Preserved: []string{"credentials and reporter leases", "instance configuration and declarative services", "remote history, targets and orchestrator bindings", "worktrees and unrelated repositories", "shared vendor services and unmanaged sessions"}, Bootstrap: "paimos --instance " + r.Instance + " runtime setup --state-root " + shellQuote(r.StateRoot)}
}
