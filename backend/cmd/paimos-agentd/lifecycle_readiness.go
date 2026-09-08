// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/runtimehealth"
)

// localDoctorLayers are the runtime doctor layers this daemon can observe about
// itself. The remaining layers describe the authenticated server side —
// authentication, deployment identity, canonical agents, dispatch profiles and
// target registration — which the authority has already validated before it
// authorizes any intent, and which the daemon must not re-assert on its behalf.
var localDoctorLayers = map[string]bool{
	"private_paths": true, "daemon_service": true, "daemon_generation": true, "socket_lock": true,
	"journal": true, "reporter_lease": true, "stale_generations": true, "workspace_ownership": true,
	"consumers": true, "browser_intents": true, "repair_budget": true,
}

// runtimeDoctor runs the existing local runtimehealth diagnosis in-process,
// against this daemon's own state root and supervisor. Nothing is executed,
// installed or repaired: Doctor only reads.
func (d *daemonLifecycle) runtimeDoctor(ctx context.Context) (agentd.ReadinessDoctorReport, error) {
	if d.stateRoot == "" || d.instance == "" {
		return agentd.ReadinessDoctorReport{}, errors.New("runtime doctor requires the daemon state root and instance")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return agentd.ReadinessDoctorReport{}, err
	}
	service, err := runtimehealth.NewPlatformService(runtime.GOOS, home, d.instance, d.stateRoot, "", "")
	if err != nil {
		return agentd.ReadinessDoctorReport{}, err
	}
	local, err := runtimehealth.New(runtimehealth.Config{
		Instance: d.instance, StateRoot: d.stateRoot, Service: service,
		Daemon: supervisorDoctor{d.supervisor}, Extensions: runtimehealth.ConsumerExtensions,
	})
	if err != nil {
		return agentd.ReadinessDoctorReport{}, err
	}
	report := local.Doctor(ctx)
	out := agentd.ReadinessDoctorReport{Instance: report.Instance, Ready: true}
	for _, layer := range report.Layers {
		if !localDoctorLayers[layer.Name] {
			continue
		}
		out.Layers = append(out.Layers, agentd.ReadinessDoctorLayer{Name: layer.Name, State: string(layer.State), Code: layer.Code})
		if layer.State != runtimehealth.Known && layer.State != runtimehealth.Repaired {
			out.Ready = false
		}
	}
	if len(out.Layers) == 0 {
		return agentd.ReadinessDoctorReport{}, errors.New("runtime doctor reported no local layers")
	}
	return out, nil
}

// supervisorDoctor exposes the running supervisor as the read side of the
// runtimehealth daemon adapter. Quiesce is never reachable from a readiness
// probe: Doctor does not call it.
type supervisorDoctor struct{ supervisor *agentd.Supervisor }

func (s supervisorDoctor) RuntimeStatus(ctx context.Context) (agentd.RuntimeStatus, error) {
	return s.supervisor.RuntimeStatus(ctx), nil
}

func (s supervisorDoctor) QuiesceRuntime(ctx context.Context, generation string, sessions []string) error {
	return errors.New("readiness never quiesces the runtime")
}

// readinessSpec assembles the probe inputs from this daemon's own protected
// configuration and the authorized intent. Nothing in it comes from a browser.
func (p *projectLifecycle) readinessSpec(in lifecycleintents.Intent) (agentd.ReadinessSpec, error) {
	if p.config.Readiness == nil {
		return agentd.ReadinessSpec{}, errors.New("project readiness expectation is not configured")
	}
	profile, err := configuredProfile(in.Request.DispatchProfileID, in.Request.DispatchProfileVersion)
	if err != nil {
		return agentd.ReadinessSpec{}, err
	}
	var workspace configuredWorkspace
	for _, candidate := range p.config.Workspaces {
		if candidate.Handle == in.Request.WorkspaceHandle {
			workspace = candidate
		}
	}
	if workspace.Path == "" {
		return agentd.ReadinessSpec{}, lifecycleclient.ErrOwnership
	}
	kernel := workspace.DoctrineKernel
	if kernel == "" {
		kernel = filepath.Join(workspace.Path, "doctrine", "docs", "AGENTS-KERNEL.md")
	}
	loader := workspace.DoctrineLoader
	if loader == "" {
		loader = filepath.Join(workspace.Path, "CLAUDE.md")
	}
	expect := p.config.Readiness
	return agentd.ReadinessSpec{
		BaselineDigest: in.Request.BaselineDigest,
		AccountLabel:   in.Request.AccountLabel,
		AccountKey:     in.Request.AccountKey,
		Profile:        profile,
		Expect: agentd.ReadinessExpectation{
			HostKind:             expect.HostKind,
			GenerationDigest:     expect.GenerationDigest,
			DoctrineKernelDigest: expect.DoctrineKernelDigest,
			DoctrineLoaderRef:    expect.DoctrineLoaderRef,
			Tools:                expect.Tools,
		},
		Inputs: agentd.ReadinessInputs{
			WorkspaceRoot:        workspace.Path,
			WorkspaceIdentity:    workspace.Identity,
			WorkspaceMode:        profile.WorkspaceMode,
			DoctrineKernel:       kernel,
			DoctrineLoader:       loader,
			HomeManagerCurrent:   expect.HomeManagerCurrent,
			HomeManagerInstalled: expect.HomeManagerInstalled,
			NixOSMarker:          expect.NixOSMarker,
			NixOSCurrent:         expect.NixOSCurrent,
			NixOSInstalled:       expect.NixOSInstalled,
			Instance:             p.owner.instance,
		},
		Doctor: p.owner.runtimeDoctor,
		Now:    time.Now,
	}, nil
}

// observeReadiness performs the owned probe and shapes it as the report the
// authority accepts. The observation time is this daemon's own.
func (p *projectLifecycle) observeReadiness(ctx context.Context, in lifecycleintents.Intent) (lifecycleclient.Result, error) {
	spec, err := p.readinessSpec(in)
	if err != nil {
		return lifecycleclient.Result{}, err
	}
	observation, err := p.owner.supervisor.ObserveReadiness(ctx, spec)
	if err != nil {
		return lifecycleclient.Result{}, err
	}
	return lifecycleclient.ReadinessResult(observation), nil
}
