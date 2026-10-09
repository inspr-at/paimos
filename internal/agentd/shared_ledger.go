// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
)

// LedgerConfig is local control, never server input. The daemon's server and
// installed-service inventory are checked by the caller before handover.
type LedgerConfig struct {
	Path   string `json:"path"`
	Label  string `json:"label"`
	Root   string `json:"root"`
	Origin string `json:"origin"`
}
type ledgerBinding struct {
	Config     LedgerConfig            `json:"config"`
	Member     agentsetup.LedgerMember `json:"member"`
	Generation string                  `json:"generation"`
}
type ledgerAPI interface {
	LedgerView(context.Context) (agentsetup.View, int, error)
	EnrollLedger(context.Context, string) error
	SetLedgerGeneration(string)
}

func (r *Remote) LedgerView(ctx context.Context) (agentsetup.View, int, error) {
	var view struct {
		agentsetup.View
		HostCapacity struct {
			Policy struct {
				MaximumAgents int `json:"maximum_agents"`
			} `json:"policy"`
		} `json:"host_capacity"`
	}
	err := r.Client.Do(ctx, "GET", "/api/agent-pairing/self", nil, &view)
	r.mu.Lock()
	r.ledgerPeerTelemetry = err == nil && slices.Contains(view.ServerCapabilities, "peer-running-v1")
	r.mu.Unlock()
	return view.View, view.HostCapacity.Policy.MaximumAgents, err
}
func (r *Remote) EnrollLedger(ctx context.Context, generation string) error {
	var view agentsetup.View
	if err := r.Client.Do(ctx, "POST", "/api/agent-pairing/self/ledger", map[string]string{"generation": generation}, &view); err != nil {
		return err
	}
	if !view.LedgerMode || view.LedgerGeneration == nil || *view.LedgerGeneration != generation {
		return agentsetup.ErrLedgerGeneration
	}
	return nil
}
func (r *Remote) SetLedgerGeneration(generation string) {
	r.mu.Lock()
	r.ledgerGeneration = generation
	r.mu.Unlock()
}
func (r *Remote) ledgerHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		headers = map[string]string{}
	}
	r.mu.RLock()
	generation := r.ledgerGeneration
	r.mu.RUnlock()
	if generation != "" {
		headers[agentsetup.LedgerGenerationHeader] = generation
	}
	return headers
}

func (s *Supervisor) ledgerLogin(account string, generation string) agentsetup.LedgerLogin {
	for _, a := range s.accounts {
		if a.ID == account {
			identity := a.VerifiedIdentity
			if a.Harness == Pi || identity == agentsetup.CodexChatGPTLogin {
				identity = ""
			}
			return agentsetup.LedgerLogin{Harness: a.Harness, Identity: agentsetup.LedgerFingerprint(generation, "login/"+a.Harness, strings.ToLower(strings.TrimSpace(identity)))}
		}
	}
	// A removed enrollment with surviving evidence remains a conservative hold.
	return agentsetup.LedgerLogin{Harness: "*", Identity: "unknown"}
}
func (s *Supervisor) ledgerCandidates(ids []string, generation string) []agentsetup.LedgerCandidate {
	out := make([]agentsetup.LedgerCandidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, agentsetup.LedgerCandidate{AccountID: id, Login: s.ledgerLogin(id, generation)})
	}
	return out
}

// EnableLedger owns the entire handover, including server enrolment. An import
// cannot interleave with route, claim, launch intent or fork. After enrolment
// failures the local dispatch path remains blocked and retains its occupancy.
func (s *Supervisor) EnableLedger(ctx context.Context, c LedgerConfig) error {
	if s.ledgerBarrier != nil {
		s.ledgerBarrier("import_requested")
	}
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.dispatchMu.Unlock()
	return s.enableLedgerLocked(ctx, c)
}
func (s *Supervisor) enableLedgerLocked(ctx context.Context, c LedgerConfig) error {
	api, ok := s.api.(ledgerAPI)
	if !ok {
		return agentsetup.ErrLedgerUnavailable
	}
	view, maximum, err := api.LedgerView(ctx)
	if err != nil {
		return err
	}
	if err = agentsetup.RequireLedgerServer(view); err != nil {
		return err
	}
	if c.Root != filepath.Dir(s.state.Path()) {
		return ErrScope
	}
	origin, err := agentsetup.CanonicalLedgerOrigin(c.Origin)
	if err != nil {
		return err
	}
	c.Origin = origin
	if !filepath.IsAbs(c.Root) || !filepath.IsAbs(c.Path) {
		return ErrScope
	}
	var binding ledgerBinding
	s.ledgerRequired = true
	raw, err := s.state.Read("ledger-member.json", 8192)
	if err == nil {
		if json.Unmarshal(raw, &binding) != nil || binding.Config != c || binding.Member.Root != c.Root || binding.Member.Label != c.Label {
			return ErrScope
		}
	} else if errors.Is(err, os.ErrNotExist) {
		id, e := agentsetup.LedgerID()
		if e != nil {
			return e
		}
		binding = ledgerBinding{Config: c, Member: agentsetup.LedgerMember{ID: id, Label: c.Label, Root: c.Root, JoinedAt: time.Now().UTC()}}
		raw, _ = json.Marshal(binding)
		if err = s.state.Write("ledger-member.json", raw, true); err != nil {
			return err
		}
	} else {
		return err
	}
	ledger, err := agentsetup.OpenSharedLedger(c.Path, true)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			ledger.Close()
		}
	}()
	// Remember the local mode before registering: even an incomplete handover
	// must never fall back to uncounted legacy dispatch.
	s.ledgerRequired = true
	if err = ledger.Register(binding.Member); err != nil {
		return err
	}
	data, _, err := ledger.Snapshot()
	if err != nil {
		return err
	}
	generation := data.Generation
	groups := []agentsetup.LedgerGroup{}
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, entry := range s.runs {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		record := entry.record
		if record.LaunchState == launchRefused || record.ExitObserved || record.LaunchState == launchRoutePending && record.State != "route_pending" {
			entry.mu.Unlock()
			continue
		}
		if noLocalProcess(record) && record.LaunchState != launchRoutePending && record.State != "claim_pending" {
			entry.mu.Unlock()
			continue
		}
		gid := record.LedgerGroup
		if gid == "" {
			gid, err = agentsetup.LedgerID()
			if err != nil {
				entry.mu.Unlock()
				return err
			}
		}
		holds := []agentsetup.LedgerLogin{s.ledgerLogin(record.AccountID, generation)}
		state := "claimed"
		if record.LaunchState == launchRoutePending {
			holds = nil
			for _, candidate := range record.RouteCandidates {
				holds = append(holds, s.ledgerLogin(candidate.AccountID, generation))
			}
			state = "pending"
		} else if !noLocalProcess(record) {
			state = "launching"
			if record.PID > 0 {
				state = "running"
			}
		}
		group := agentsetup.LedgerGroup{ID: gid, Instance: binding.Member.ID, Generation: generation, State: state, Holds: holds, PID: record.PID, StartedAt: record.ProcessStartedAt}
		groups = append(groups, group)
		// Private evidence is committed before import. If interrupted, no admission
		// is possible and the next owner import reconstructs the same group.
		record.LedgerGroup, record.LedgerGeneration = gid, generation
		if record.LaunchState == launchRoutePending {
			record.RouteCandidates = s.ledgerCandidates(candidateIDs(record.RouteCandidates), generation)
		}
		if err = s.journal.Put(record); err != nil {
			entry.mu.Unlock()
			return err
		}
		entry.record = record
		entry.mu.Unlock()
	}
	logins := []agentsetup.LedgerLogin{}
	for _, a := range s.accounts {
		login := s.ledgerLogin(a.ID, generation)
		if !slices.Contains(logins, login) {
			logins = append(logins, login)
		}
	}
	instance := agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(generation, "instance", origin+"\x00"+s.tenantID), PID: os.Getpid(), StartedAt: s.startedAt, MaximumAgents: maximum, Logins: logins}
	// The same origin + tenant may never be paired twice just because the
	// workspaces differ. Existing same-member restarts remain allowed.
	for id, v := range data.Instances {
		if id != binding.Member.ID && v.Fingerprint == instance.Fingerprint {
			return ErrScope
		}
	}
	imported, err := ledger.Import(binding.Member.ID, instance, groups)
	if err != nil {
		return err
	}
	if imported != generation {
		return agentsetup.ErrLedgerGeneration
	}
	if err = api.EnrollLedger(ctx, generation); err != nil {
		return err
	}
	api.SetLedgerGeneration(generation)
	if err = ledger.Enrolled(binding.Member.ID, generation); err != nil {
		return err
	}
	binding.Generation = generation
	raw, _ = json.Marshal(binding)
	if err = s.state.Write("ledger-member.json", raw, false); err != nil {
		return err
	}
	if s.ledger != nil {
		s.ledger.Close()
	}
	s.ledger, s.ledgerBinding = ledger, binding
	keep = true
	// This owner has completed its fenced handover. Waiting for a peer is an
	// admission condition, not a failed import: reporting failure here can
	// prevent a partially provisioned peer from finishing its own handover.
	data, members, err := ledger.Snapshot()
	if err != nil {
		return err
	}
	if data.Generation != generation {
		return agentsetup.ErrLedgerGeneration
	}
	for _, member := range members {
		v := data.Instances[member.ID]
		if !v.Imported || !v.Enrolled {
			return nil
		}
	}
	if data.Rebuilding {
		return nil
	}
	return s.reconcileLedgerLocked(ctx)
}
func candidateIDs(candidates []agentsetup.LedgerCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.AccountID)
	}
	return ids
}

// RefreshLedger is called before queue admission. It automatically enrols an
// existing computer when the tenant requires it and re-imports after rebuild.
func (s *Supervisor) RefreshLedger(ctx context.Context, c LedgerConfig) error {
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.dispatchMu.Unlock()
	api, ok := s.api.(ledgerAPI)
	if !ok {
		return nil
	}
	view, maximum, err := api.LedgerView(ctx)
	if err != nil {
		return err
	}
	if !view.LedgerMode && !s.ledgerRequired {
		if _, err = s.state.Read("ledger-member.json", 8192); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if s.ledger == nil {
		return s.enableLedgerLocked(ctx, c)
	}
	data, _, err := s.ledger.Snapshot()
	if err != nil {
		return err
	}
	if data.Generation != s.ledgerBinding.Generation {
		return s.enableLedgerLocked(ctx, c)
	}
	if view.LedgerGeneration == nil || *view.LedgerGeneration != data.Generation {
		return s.enableLedgerLocked(ctx, c)
	}
	if err = s.ledger.Maximum(s.ledgerBinding.Member.ID, data.Generation, maximum); err != nil {
		return err
	}
	if reporter, ok := s.api.(interface {
		PeerRunning(context.Context, int) error
	}); ok {
		peer := 0
		for _, group := range data.Groups {
			if group.Instance != s.ledgerBinding.Member.ID {
				peer++
			}
		}
		if err = reporter.PeerRunning(ctx, peer); err != nil {
			return err
		}
	}
	return s.reconcileLedgerLocked(ctx)
}
func (s *Supervisor) releaseLedger(record Record) error {
	if record.LedgerGroup == "" {
		return nil
	}
	if s.ledger == nil {
		return agentsetup.ErrLedgerUnavailable
	}
	return s.ledger.Release(s.ledgerBinding.Member.ID, record.LedgerGeneration, record.LedgerGroup)
}
func (s *Supervisor) reconcileLedgerLocked(ctx context.Context) error {
	if s.ledger == nil {
		return nil
	}
	data, _, err := s.ledger.Snapshot()
	if err != nil {
		return err
	}
	records := s.journal.Snapshot()
	if len(records) > 4096 {
		return agentsetup.ErrLedgerUnavailable
	}
	for _, r := range records {
		if !noLocalProcess(r) {
			proof := s.ledgerExitProof
			if proof == nil {
				proof = confirmedLedgerExit
			}
			s.mu.Lock()
			entry := s.runs[r.RunID]
			s.mu.Unlock()
			if entry != nil {
				entry.mu.Lock()
				r = entry.record
				if entry.process == nil && !noLocalProcess(r) && proof(r) {
					r.ExitObserved = true
					if err = s.journal.Put(r); err != nil {
						entry.mu.Unlock()
						return err
					}
					entry.record = r
				}
				entry.mu.Unlock()
			}
		}
		if r.LedgerGroup == "" {
			continue
		}
		if r.LaunchState == launchRoutePending && r.State == "route_pending" {
			if err = s.ledger.CheckGroup(s.ledgerBinding.Member.ID, r.LedgerGeneration, r.LedgerGroup); err != nil {
				return err
			}
			route, e := s.api.Route(ctx, r.RunID, s.daemonID, candidateIDs(r.RouteCandidates), s.estimates)
			if e != nil {
				// A generic 409 includes capacity/grant/generation refusals. Release only
				// after independent run state confirms this attempt can no longer route.
				var status *client.StatusError
				if errors.As(e, &status) && status.Status == 409 {
					run, readErr := s.api.GetRun(ctx, r.RunID)
					if readErr == nil && run.ID == r.RunID && run.AgentPrincipalID == r.PrincipalID && run.WorkOrderID == r.WorkOrderID && (run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled") {
						if err = s.releaseLedger(r); err != nil {
							return err
						}
						r.LaunchState, r.State = launchRoutePending, run.Status
						r.LedgerGroup, r.LedgerGeneration = "", ""
						r.RouteCandidates = nil
						if err = s.journal.Put(r); err != nil {
							return err
						}
						s.setLedgerRecord(r)
						continue
					}
				}
				return e
			}
			if err = s.confirmLedgerRoute(&r, route); err != nil {
				return err
			}
			r.LaunchState, r.State = launchPrepared, "claim_pending"
			r.ClaimRoute = &route
			r.AccountID = route.AccountID
			if err = s.journal.Put(r); err != nil {
				return err
			}
			s.setLedgerRecord(r)
		} else if r.ExitObserved || noLocalProcess(r) && r.State != "claim_pending" {
			// A rebuild imports only occupancy. Proven-exited records need no new
			// group, so their old coordinates are cleared without a stale mutation.
			if _, exists := data.Groups[r.LedgerGroup]; exists {
				if err = s.releaseLedger(r); err != nil {
					return err
				}
			}
			if err = s.clearLedgerCoordinates(r.RunID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Supervisor) setLedgerRecord(r Record) {
	s.mu.Lock()
	entry := s.runs[r.RunID]
	s.mu.Unlock()
	if entry != nil {
		entry.mu.Lock()
		entry.record = r
		entry.mu.Unlock()
	}
}
func (s *Supervisor) confirmLedgerRoute(r *Record, route Route) error {
	if route.DaemonID != s.daemonID || route.AccountKey == "" || len(route.Reservations) == 0 {
		return ErrScope
	}
	candidate := false
	for _, c := range r.RouteCandidates {
		if c.AccountID == route.AccountID {
			candidate = true
		}
	}
	if !candidate {
		return ErrScope
	}
	binding := false
	for _, a := range s.accounts {
		binding = binding || a.ID == route.AccountID && a.Key == route.AccountKey
	}
	if !binding {
		return ErrScope
	}
	for _, reservation := range route.Reservations {
		if reservation.ID == "" {
			return ErrScope
		}
	}
	return s.ledger.Claimed(s.ledgerBinding.Member.ID, r.LedgerGeneration, r.LedgerGroup, s.ledgerLogin(route.AccountID, r.LedgerGeneration))
}

// LeaveLedger is reachable only after the owning daemon is durably fenced and
// its journal proves every possible process has exited. It never signals a PID.
func (s *Supervisor) LeaveLedger(ctx context.Context) error {
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.dispatchMu.Unlock()
	if s.ledger == nil {
		return agentsetup.ErrLedgerUnavailable
	}
	fenced, err := s.readFence("")
	if err != nil || !fenced {
		return ErrDraining
	}
	if err = s.reconcileLedgerLocked(ctx); err != nil {
		return err
	}
	for _, r := range s.journal.Snapshot() {
		if !noLocalProcess(r) || r.LaunchState == launchRoutePending && r.State == "route_pending" || r.State == "claim_pending" || len(r.Pending) > 0 || r.SettlementGap {
			return ErrProcessesUnconfirmed
		}
		if err = s.releaseLedger(r); err != nil {
			return err
		}
	}
	if err = s.ledger.Leave(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, true); err != nil {
		return err
	}
	ledger := s.ledger
	s.ledger = nil
	return ledger.Close()
}

// PeerRunning is advisory and negotiated independently. Servers that strictly
// decode capacity signals never receive a field they have not advertised.
func (r *Remote) PeerRunning(ctx context.Context, count int) error {
	r.mu.RLock()
	supported := r.ledgerPeerTelemetry
	r.mu.RUnlock()
	if !supported {
		return nil
	}
	if count < 0 || count > 4096 {
		return ErrScope
	}
	raw, err := json.Marshal(sampleHost(ctx, false))
	if err != nil {
		return err
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return ErrScope
	}
	body["peer_running"] = count
	return r.Client.Do(ctx, "POST", "/api/agent-pairing/self/capacity", body, nil)
}

func (s *Supervisor) clearLedgerCoordinates(run string) error {
	s.mu.Lock()
	entry := s.runs[run]
	s.mu.Unlock()
	if entry == nil {
		return ErrNotOwned
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	r := entry.record
	if r.LedgerGroup == "" {
		return nil
	}
	if !noLocalProcess(r) || r.State == "claim_pending" || r.State == "route_pending" {
		return ErrProcessesUnconfirmed
	}
	r.LedgerGroup, r.LedgerGeneration = "", ""
	r.RouteCandidates = nil
	if err := s.journal.Put(r); err != nil {
		return err
	}
	entry.record = r
	return nil
}
