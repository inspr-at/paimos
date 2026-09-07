// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/localjournal"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

type configuredWorkspace struct {
	Label    string `json:"label,omitempty"`
	Handle   string `json:"handle"`
	Identity string `json:"identity"`
	Path     string `json:"path"`
}
type configuredAccount struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
type configuredProject struct {
	ProjectID    int64                      `json:"project_id"`
	AccountLabel string                     `json:"account_label"`
	AccountKey   string                     `json:"account_key,omitempty"`
	Accounts     []configuredAccount        `json:"accounts,omitempty"`
	Profiles     []lifecycleintents.Profile `json:"profiles"`
	Workspaces   []configuredWorkspace      `json:"workspaces"`
}
type lifecycleConfig struct {
	Projects []configuredProject `json:"projects"`
}
type runtimeRegistrationRecord struct {
	Key          string                                     `json:"key"`
	Lease        string                                     `json:"lease"`
	Generation   string                                     `json:"generation"`
	FirstAttempt time.Time                                  `json:"first_attempt"`
	Runtime      lifecycleintents.Runtime                   `json:"runtime"`
	Health       map[string]agentmessage.RuntimeHealthInput `json:"health,omitempty"`
}
type daemonLifecycle struct {
	mu         sync.Mutex
	supervisor *agentd.Supervisor
	primary    *nativeConsumers
	reporter   *cliReporter
	projects   []*projectLifecycle
	evidence   []runtimeconsumer.Evidence
	journal    *localjournal.Journal[runtimeRegistrationRecord]
}
type projectLifecycle struct {
	owner            *daemonLifecycle
	config           configuredProject
	registration     lifecycleintents.Registration
	record           runtimeRegistrationRecord
	authority        *lifecycleclient.HTTP
	runner           *lifecycleclient.Runner
	refresh          time.Time
	bound            map[string]bool
	prepared         map[string]agentd.StartRequest
	lost             bool
	consumers        *lifecycleclient.Consumers
	consumerEvidence []runtimeconsumer.Evidence
	healthAt         time.Time
}

func newDaemonLifecycle(path, root, instance, reportURL, keyFile string, supervisor *agentd.Supervisor, primary *nativeConsumers, reporter *cliReporter) (*daemonLifecycle, error) {
	raw, err := lifecycleclient.ReadPrivate(path, 64<<10)
	if err != nil {
		return nil, errors.New("lifecycle configuration requires a protected owner-only JSON file")
	}
	var config lifecycleConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(config.Projects) == 0 || len(config.Projects) > 4 {
		return nil, errors.New("lifecycle project configuration invalid")
	}
	dir, err := agentd.InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	journal, err := localjournal.Open(localjournal.Config[runtimeRegistrationRecord]{Directory: dir, Prefix: "lifecycle-runtimes", Version: 1, MaxBytes: 2 << 20, MaxRecords: 512,
		Key: func(r runtimeRegistrationRecord) (string, error) { return r.Key, nil }, Validate: func(r runtimeRegistrationRecord) error {
			if r.Key == "" || uuid.Validate(r.Generation) != nil || !lifecycleclient.ValidProof(r.Lease) || r.FirstAttempt.IsZero() {
				return lifecycleclient.ErrOwnership
			}
			return nil
		}})
	if err != nil {
		return nil, errors.New("private lifecycle authority journal unavailable; preserve it and reconcile outstanding intents")
	}
	d := &daemonLifecycle{supervisor: supervisor, primary: primary, reporter: reporter, journal: journal}
	seen := map[int64]bool{}
	for _, c := range config.Projects {
		if c.ProjectID <= 0 || seen[c.ProjectID] || len(c.Workspaces) == 0 || len(c.Workspaces) > 16 || len(c.Profiles) == 0 || len(c.Profiles) > 16 {
			return nil, errors.New("lifecycle project configuration invalid")
		}
		if c.AccountKey != "" && (len(c.Accounts) > 0 || !agentd.ValidAccountKey(c.AccountKey)) {
			return nil, errors.New("lifecycle project configuration invalid")
		}
		accounts, err := advertisedAccounts(c)
		if err != nil {
			return nil, err
		}
		seen[c.ProjectID] = true
		p := &projectLifecycle{owner: d, config: c, bound: map[string]bool{}, prepared: map[string]agentd.StartRequest{}}
		p.registration = lifecycleintents.Registration{Generation: supervisor.Status().DaemonID, Host: reporter.host, AccountLabel: c.AccountLabel, Accounts: accounts, Profiles: c.Profiles, Workspaces: []lifecycleintents.Workspace{}}
		if len(accounts) > 0 {
			p.registration.SchemaVersion = lifecycleintents.AccountChoiceSchemaV2
		}
		workspaces := map[string]bool{}
		for _, w := range c.Workspaces {
			if uuid.Validate(w.Handle) != nil || len(w.Identity) != 64 || !filepath.IsAbs(w.Path) || workspaces[w.Handle] {
				return nil, errors.New("lifecycle workspace configuration invalid")
			}
			workspaces[w.Handle] = true
			p.registration.Workspaces = append(p.registration.Workspaces, lifecycleintents.Workspace{Handle: w.Handle, Identity: w.Identity, Label: w.Label})
		}
		for _, selected := range c.Profiles {
			if _, e := configuredProfile(selected.ID, selected.Version); e != nil {
				return nil, e
			}
		}
		lease, e := lifecycleclient.NewProof()
		if e != nil {
			return nil, e
		}
		p.record = runtimeRegistrationRecord{Key: fmt.Sprintf("%s:%d", p.registration.Generation, c.ProjectID), Generation: p.registration.Generation, Lease: lease, FirstAttempt: time.Now().UTC()}
		for _, saved := range journal.Snapshot() {
			if saved.Key == p.record.Key {
				p.record = saved
				break
			}
		}
		if e = journal.Put(p.record); e != nil {
			return nil, e
		}
		p.authority, e = lifecycleclient.NewHTTP(reportURL, c.ProjectID, p.record.Lease, lifecycleclient.FileCredential(keyFile))
		if e != nil {
			return nil, e
		}
		projectDir := filepath.Join(dir, fmt.Sprintf("lifecycle-project-%d", c.ProjectID))
		p.runner, e = lifecycleclient.NewRunner(projectDir, p.registration.Generation, p.authority, p)
		if e != nil {
			return nil, e
		}
		p.consumers, e = lifecycleclient.NewConsumers(projectDir, p.authority)
		if e != nil {
			return nil, e
		}
		if p.record.Health == nil {
			p.record.Health = map[string]agentmessage.RuntimeHealthInput{}
		}
		d.projects = append(d.projects, p)
	}
	return d, nil
}
func configuredProfile(id, version string) (dispatchprofile.Profile, error) {
	for _, p := range dispatchprofile.List() {
		if p.ID == id && p.Version == version {
			return p, nil
		}
	}
	return dispatchprofile.Profile{}, errors.New("configured immutable lifecycle profile unavailable")
}

func advertisedAccounts(c configuredProject) ([]lifecycleintents.AccountChoice, error) {
	if len(c.Accounts) == 0 {
		if c.AccountKey == "" {
			return nil, nil
		}
		return []lifecycleintents.AccountChoice{{Key: c.AccountKey, Label: c.AccountKey}}, nil
	}
	if len(c.Accounts) > 16 {
		return nil, errors.New("lifecycle project configuration invalid")
	}
	out := make([]lifecycleintents.AccountChoice, 0, len(c.Accounts))
	keys, labels := map[string]bool{}, map[string]bool{}
	for _, account := range c.Accounts {
		if !agentd.ValidAccountKey(account.Key) || account.Label != strings.TrimSpace(account.Label) || account.Label == "" || keys[account.Key] || labels[account.Label] {
			return nil, errors.New("lifecycle project configuration invalid")
		}
		keys[account.Key] = true
		labels[account.Label] = true
		out = append(out, lifecycleintents.AccountChoice{Key: account.Key, Label: account.Label})
	}
	return out, nil
}

func (p *projectLifecycle) configuredAccountKey(key string) bool {
	if len(p.registration.Accounts) == 0 {
		return key == ""
	}
	for _, account := range p.registration.Accounts {
		if account.Key == key {
			return true
		}
	}
	return false
}
func (d *daemonLifecycle) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for index, p := range d.projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				stepCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				err := p.step(stepCtx)
				cancel()
				e := runtimeconsumer.Evidence{Kind: "intents", Generation: d.supervisor.Status().DaemonID, ProjectID: p.config.ProjectID, State: "ready", LastSuccess: time.Now().UTC()}
				if err != nil {
					e.State = "unavailable"
					e.Reason = "authority_unavailable"
					if errors.Is(err, lifecycleclient.ErrUnknown) {
						e.Reason = "outcome_unknown"
					}
					if p.lost {
						e.Reason = "ownership_lost"
					}
				}
				d.mu.Lock()
				if len(d.evidence) < len(d.projects) {
					d.evidence = make([]runtimeconsumer.Evidence, len(d.projects))
				}
				d.evidence[index] = e
				d.mu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	wg.Wait()
}
func (d *daemonLifecycle) Snapshot() []runtimeconsumer.Evidence {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]runtimeconsumer.Evidence{}, d.evidence...)
	for _, p := range d.projects {
		out = append(out, p.consumerEvidence...)
	}
	return out
}
func (p *projectLifecycle) verifyConfiguration(ctx context.Context) error {
	for _, w := range p.config.Workspaces {
		actual, e := p.owner.supervisor.InspectWorkspace(ctx, w.Path, agentd.WorkspaceExclusive)
		if e != nil || actual.Identity != w.Identity {
			return lifecycleclient.ErrOwnership
		}
	}
	for _, v := range p.config.Profiles {
		profile, e := configuredProfile(v.ID, v.Version)
		if e != nil {
			return e
		}
		actual, e := p.owner.reporter.ResolveDispatchProfile(ctx, v.ID, v.Version, profile.Harness)
		if e != nil || actual != profile {
			return lifecycleclient.ErrOwnership
		}
		if len(p.registration.Accounts) > 0 {
			for _, account := range p.registration.Accounts {
				if !p.owner.supervisor.HasAccount(profile.Harness, account.Key) {
					return lifecycleclient.ErrOwnership
				}
			}
			continue
		}
		if p.owner.supervisor.ProbeAccount(ctx, profile.Harness) != p.config.AccountLabel {
			return lifecycleclient.ErrOwnership
		}
	}
	return nil
}
func (p *projectLifecycle) step(ctx context.Context) error {
	if p.lost {
		return lifecycleclient.ErrOwnership
	}
	now := time.Now()
	if p.record.Runtime.ID != "" {
		expiry, e := time.Parse(time.RFC3339Nano, p.record.Runtime.ExpiresAt)
		if e != nil || !now.Before(expiry) {
			p.lost = true
			return lifecycleclient.ErrOwnership
		}
	} else if now.Sub(p.record.FirstAttempt) >= 120*time.Second {
		p.lost = true
		return lifecycleclient.ErrOwnership
	}
	if now.After(p.refresh) {
		if e := p.verifyConfiguration(ctx); e != nil {
			return e
		}
		runtime, e := p.authority.RegisterRuntime(ctx, p.registration)
		if e != nil {
			return e
		}
		p.record.Runtime = runtime
		if e = p.owner.journal.Put(p.record); e != nil {
			return e
		}
		p.refresh = now.Add(30 * time.Second)
	}
	for _, s := range p.owner.supervisor.Status().Sessions {
		if s.ProjectID != p.config.ProjectID || s.AccountLabel != p.config.AccountLabel || !p.configuredAccountKey(s.AccountKey) || s.Reporter.PublicSessionID == "" || s.State == agentd.StateOwnershipLost || s.Reporter.Closed || s.PID <= 0 || p.bound[s.ID] {
			continue
		}
		if e := p.registerSession(ctx, s); e != nil {
			return e
		}
	}
	intentErr := p.runner.Step(ctx, p.record.Runtime)
	consumerErr := p.consume(ctx)
	healthErr := p.publishHealth(ctx)
	return errors.Join(intentErr, consumerErr, healthErr)
}
func (p *projectLifecycle) registerSession(ctx context.Context, s agentd.Session) error {
	lease, e := p.owner.reporter.leases.GetOrCreate(s.ID)
	if e != nil {
		return lifecycleclient.ErrOwnership
	}
	if e = p.authority.RegisterSession(ctx, p.record.Runtime.ID, lifecycleintents.SessionRegistration{SessionID: s.Reporter.PublicSessionID, Generation: s.ID}, lease); e != nil {
		return e
	}
	p.bound[s.ID] = true
	return nil
}
func (p *projectLifecycle) ownSession(in lifecycleintents.Intent) (agentd.Session, error) {
	for _, s := range p.owner.supervisor.Status().Sessions {
		if s.ID == in.Request.SessionGeneration && s.Reporter.PublicSessionID == in.Request.SessionID && p.bound[s.ID] && s.ProjectID == p.config.ProjectID && s.AccountLabel == p.config.AccountLabel && s.AccountKey == in.Request.AccountKey && p.configuredAccountKey(s.AccountKey) {
			return s, nil
		}
	}
	return agentd.Session{}, lifecycleclient.ErrOwnership
}
func (p *projectLifecycle) Prepare(ctx context.Context, in lifecycleintents.Intent) error {
	if in.Request.AccountLabel != p.config.AccountLabel {
		return lifecycleclient.ErrOwnership
	}
	if in.Request.Operation == "repair" {
		if in.Request.AccountKey != "" && !p.configuredAccountKey(in.Request.AccountKey) {
			return lifecycleclient.ErrOwnership
		}
		if in.Request.RepairLayer != "reporter" && in.Request.RepairLayer != "listeners" {
			return lifecycleclient.ErrOwnership
		}
		return nil
	}
	if !p.configuredAccountKey(in.Request.AccountKey) {
		return lifecycleclient.ErrOwnership
	}
	if e := p.verifyConfiguration(ctx); e != nil {
		return e
	}
	profile, e := configuredProfile(in.Request.DispatchProfileID, in.Request.DispatchProfileVersion)
	if e != nil {
		return e
	}
	selected := false
	for _, v := range p.config.Profiles {
		selected = selected || v.ID == profile.ID && v.Version == profile.Version
	}
	if !selected {
		return lifecycleclient.ErrOwnership
	}
	workspace := ""
	identity := ""
	for _, w := range p.config.Workspaces {
		if w.Handle == in.Request.WorkspaceHandle {
			workspace = w.Path
			identity = w.Identity
		}
	}
	if workspace == "" {
		return lifecycleclient.ErrOwnership
	}
	if in.Request.Operation != "start" {
		s, e := p.ownSession(in)
		if e != nil {
			return e
		}
		if s.Identity != profile.Harness+":"+in.Request.AgentName || s.Role != in.Request.Role || s.WorkspaceProvenance.Identity != identity || s.DispatchProfile == nil || *s.DispatchProfile != profile {
			return lifecycleclient.ErrOwnership
		}
		if in.Request.Operation == "restart" {
			if !terminalAgentdState(s.State) || s.State == agentd.StateOwnershipLost || s.PID > 0 {
				return lifecycleclient.ErrOwnership
			}
		} else if s.State != agentd.StateRunning || s.LastEventKind != agentd.EventTurnCompleted {
			return lifecycleclient.ErrOwnership
		}
	}
	if in.Request.Operation == "attach" || in.Request.Operation == "reassign" {
		return nil
	}
	if (in.Request.Operation != "start" && in.Request.Operation != "restart") || uuid.Validate(in.NewGeneration) != nil {
		return lifecycleclient.ErrOwnership
	}
	var artifact struct {
		Project struct {
			ID  int64  `json:"id"`
			Key string `json:"key"`
		} `json:"project"`
		Agent struct {
			Name      string          `json:"name"`
			ProjectID int64           `json:"project_id"`
			Body      string          `json:"body"`
			Bootstrap json.RawMessage `json:"bootstrap_steps"`
			Rules     json.RawMessage `json:"non_negotiable_rules"`
		} `json:"agent"`
	}
	if e = p.authority.Request(ctx, http.MethodGet, fmt.Sprintf("/api/projects/%d/agents/%s.json", p.config.ProjectID, url.PathEscape(in.Request.AgentName)), nil, nil, &artifact); e != nil {
		return e
	}
	if artifact.Project.ID != p.config.ProjectID || artifact.Agent.ProjectID != p.config.ProjectID || artifact.Agent.Name != in.Request.AgentName {
		return lifecycleclient.ErrOwnership
	}
	instructions, _ := json.Marshal(artifact.Agent)
	prompt := fmt.Sprintf("Act as canonical project agent %s in project %s with owned role %s. Follow the canonical instructions:\n%s", in.Request.AgentName, artifact.Project.Key, in.Request.Role, instructions)
	if in.Request.TicketID != nil {
		ticket, e := p.authority.ReadCanonicalTicket(ctx, *in.Request.TicketID)
		if e != nil {
			return e
		}
		ticketInstructions, _ := json.Marshal(ticket)
		prompt += fmt.Sprintf("\nYour authorized ticket and work shape %s:\n%s", in.Request.WorkShape, ticketInstructions)
	}
	if len(prompt) > 256<<10 {
		return lifecycleclient.ErrOwnership
	}
	request := agentd.StartRequest{IdempotencyKey: "lifecycle:" + in.ID, Adapter: profile.Harness, Workspace: workspace, WorkspaceMode: profile.WorkspaceMode, ExpectedAccountLabel: p.config.AccountLabel, AccountKey: in.Request.AccountKey, ExpectedMachineID: p.registration.Host, Identity: profile.Harness + ":" + in.Request.AgentName, ProjectID: p.config.ProjectID, Role: in.Request.Role, DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Prompt: prompt}
	if in.Request.TicketID != nil {
		request.TicketID = *in.Request.TicketID
		request.WorkShape = in.Request.WorkShape
	}
	if in.Request.ParentSessionID != nil {
		request.ParentSessionID = *in.Request.ParentSessionID
	}
	p.prepared[in.ID] = request
	return nil
}

func (p *projectLifecycle) Forget(id string) { delete(p.prepared, id) }
func (p *projectLifecycle) Execute(ctx context.Context, in lifecycleintents.Intent) (lifecycleclient.Result, error) {
	if in.Request.Operation == "repair" {
		var err error
		if in.Request.RepairLayer == "reporter" {
			err = p.owner.supervisor.RepairReporter(ctx)
		} else {
			err = p.owner.primary.RepairProject(ctx, p.config.ProjectID)
			if err == nil {
				err = p.consumers.Repair()
			}
			if err == nil {
				err = p.consume(ctx)
			}
			if err == nil {
				for _, kind := range []string{"fallback", "attention"} {
					if p.optionalReceiverUnconfigured(kind) {
						continue
					}
					state, _, _ := healthEvidence(kind, p.config.ProjectID, p.consumerEvidence, time.Now())
					if state != "healthy" {
						err = lifecycleclient.ErrOwnership
					}
				}
			}
		}
		return lifecycleclient.Result{Reason: "applied"}, err
	}
	if in.Request.Operation == "attach" || in.Request.Operation == "reassign" {
		if _, e := p.ownSession(in); e != nil {
			return lifecycleclient.Result{}, e
		}
		return lifecycleclient.Result{Reason: "applied", SessionID: in.Request.SessionID}, nil
	}
	request, ok := p.prepared[in.ID]
	delete(p.prepared, in.ID)
	if !ok {
		return lifecycleclient.Result{}, lifecycleclient.ErrUnknown
	}
	session, e := p.owner.supervisor.StartReserved(ctx, request, in.NewGeneration)
	if e != nil || session.ID != in.NewGeneration {
		return lifecycleclient.Result{}, lifecycleclient.ErrUnknown
	}
	for {
		for _, s := range p.owner.supervisor.Status().Sessions {
			if s.ID != session.ID {
				continue
			}
			if s.State != agentd.StateRunning {
				return lifecycleclient.Result{}, lifecycleclient.ErrUnknown
			}
			if s.Reporter.PublicSessionID != "" {
				if e = p.registerSession(ctx, s); e != nil {
					return lifecycleclient.Result{}, e
				}
				return lifecycleclient.Result{Reason: "applied", SessionID: s.Reporter.PublicSessionID}, nil
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lifecycleclient.Result{}, lifecycleclient.ErrUnknown
		case <-timer.C:
		}
	}
}

// An absent optional receiver remains explicitly unconfigured after a primary
// repair. Missing, stale or mixed evidence cannot stand in for that observation.
func (p *projectLifecycle) optionalReceiverUnconfigured(kind string) bool {
	seen := false
	generation := p.owner.supervisor.Status().DaemonID
	for _, evidence := range p.consumerEvidence {
		if evidence.Kind != kind || evidence.ProjectID != p.config.ProjectID {
			continue
		}
		seen = true
		if evidence.Generation != generation || evidence.State != "unavailable" || evidence.Reason != "receiver_not_configured" {
			return false
		}
	}
	return seen
}
func (p *projectLifecycle) Committed(ctx context.Context, in lifecycleintents.Intent) error {
	if in.Request.Operation != "attach" && in.Request.Operation != "reassign" {
		return nil
	}
	parent := ""
	ticket := int64(0)
	if in.Request.ParentSessionID != nil {
		parent = *in.Request.ParentSessionID
	}
	if in.Request.TicketID != nil {
		ticket = *in.Request.TicketID
	}
	return p.owner.supervisor.CommitLifecycleBinding(ctx, in.Request.SessionGeneration, in.Request.SessionID, parent, ticket, in.Request.WorkShape)
}

type runtimeServices struct {
	primary   *nativeConsumers
	lifecycle *daemonLifecycle
}

func (s *runtimeServices) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, service := range []agentd.RuntimeConsumers{s.primary, s.lifecycle} {
		wg.Add(1)
		go func() { defer wg.Done(); service.Run(ctx) }()
	}
	wg.Wait()
}
func (s *runtimeServices) Snapshot() []runtimeconsumer.Evidence {
	out := []runtimeconsumer.Evidence{}
	for _, e := range s.primary.Snapshot() {
		if e.Kind == "primary" {
			out = append(out, e)
		}
	}
	return append(out, s.lifecycle.Snapshot()...)
}
