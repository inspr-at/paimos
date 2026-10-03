// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/stepup"
)

// StepUpConfig is constructed only by paired serve. Neither the socket caller
// nor a process environment can supply the origin, computer, fetcher or signer.
type StepUpConfig struct {
	Origin, ComputerID string
	Ready              bool
	Fetch              func(context.Context, string) (stepup.Challenge, error)
	Sign               func(context.Context, []byte, string) (string, error)
}

type StepUpManager struct {
	cfg          StepUpConfig
	mu           sync.Mutex
	attempted    map[string]time.Time
	busy, closed bool
	cancel       context.CancelFunc
	now          func() time.Time
	peer         func(*http.Request) (attachObservation, error)
}

func NewStepUpManager(cfg StepUpConfig) (*StepUpManager, error) {
	if ValidateBaseURL(cfg.Origin) != nil || !stepup.ValidID(cfg.ComputerID) || cfg.Fetch == nil || cfg.Sign == nil {
		return nil, errors.New("paired Touch ID configuration unavailable")
	}
	return &StepUpManager{cfg: cfg, attempted: make(map[string]time.Time), now: time.Now, peer: stepUpPeer}, nil
}

func (m *StepUpManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
}

// Unlike attach's separate interactive helper, the requester here IS an agent
// (including headless MCP). It may ask for a prompt, never authorize/sign. Reuse
// the kernel peer identity and bounded ancestry checks without requiring a TTY.
func stepUpPeer(r *http.Request) (attachObservation, error) {
	before, ok := r.Context().Value(attachPeerKey{}).(attachObservation)
	if !ok || before.PID < 1 {
		return attachObservation{}, errors.New("kernel peer unavailable")
	}
	now, err := observeAttachProcess(before.PID)
	if !ok || err != nil || before.Process != now.Process || now.UID != os.Getuid() {
		return attachObservation{}, errors.New("kernel peer changed or unavailable")
	}
	seen := map[int]attachObservation{}
	if !independentAttachAncestry(now.PID, -1, now.UID, observeAttachProcessIdentity, seen) {
		return attachObservation{}, errors.New("kernel ancestry unavailable")
	}
	for pid, prior := range seen {
		after, err := observeAttachProcessIdentity(pid)
		if err != nil || attachProcessIdentity(after) != prior {
			return attachObservation{}, errors.New("kernel ancestry changed")
		}
	}
	return now, nil
}

func (m *StepUpManager) confirm(r *http.Request, id string) (stepup.Proof, error) {
	fail := func(message string) (stepup.Proof, error) { return stepup.Proof{}, errors.New(message) }
	if !stepup.ValidID(id) {
		return fail("invalid Touch ID challenge")
	}
	peer, err := m.peer(r)
	if err != nil {
		return fail("kernel peer verification failed")
	}
	m.mu.Lock()
	now := m.now()
	for key, expiry := range m.attempted {
		if !expiry.After(now) {
			delete(m.attempted, key)
		}
	}
	_, replay := m.attempted[id]
	if m.closed || m.busy || !m.cfg.Ready || replay || len(m.attempted) >= 256 {
		m.mu.Unlock()
		return fail("Touch ID unavailable, busy or challenge already attempted; check aeon-agentd status")
	}
	ctx, cancel := context.WithTimeout(r.Context(), stepup.ConfirmTimeout)
	m.busy, m.cancel = true, cancel
	m.attempted[id] = now.Add(stepup.MaxLifetime)
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); m.busy, m.cancel = false, nil; m.mu.Unlock() }()
	challenge, err := m.cfg.Fetch(ctx, id)
	if err != nil {
		return fail("paired instance refused or could not fetch the Touch ID challenge")
	}
	if !challenge.Valid(m.cfg.ComputerID, m.now()) {
		return fail("invalid, expired or wrong-computer Touch ID challenge")
	}
	// Keep the replay guard for the full server expiry even if fetch was slow.
	m.mu.Lock()
	if challenge.ExpiresAt.After(m.attempted[id]) {
		m.attempted[id] = challenge.ExpiresAt
	}
	m.mu.Unlock()
	confirmedPeer, err := m.peer(r)
	if err != nil || confirmedPeer != peer {
		return fail("kernel peer changed before Touch ID")
	}
	ctx, expire := context.WithDeadline(ctx, challenge.ExpiresAt)
	defer expire()
	if ctx.Err() != nil {
		return fail("Touch ID confirmation expired or cancelled")
	}
	signature, err := m.cfg.Sign(ctx, challenge.Hash(), challenge.Summary)
	if err != nil {
		return fail("Touch ID cancelled, denied, timed out or unavailable; action not confirmed")
	}
	if ctx.Err() != nil || !m.now().Before(challenge.ExpiresAt) {
		return fail("Touch ID confirmation expired or cancelled")
	}
	after, err := m.peer(r)
	if err != nil || after != peer {
		return fail("kernel peer changed during Touch ID")
	}
	if !stepup.ValidSignature(signature) {
		return fail("Touch ID signer returned an invalid proof")
	}
	return stepup.Proof{ChallengeID: id, Signature: signature}, nil
}

func (m *StepUpManager) serve(w http.ResponseWriter, r *http.Request, token string) {
	w.Header().Set("Cache-Control", "no-store")
	if !authorized(r, token) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stepup.Info{Origin: m.cfg.Origin, ComputerID: m.cfg.ComputerID, Ready: m.cfg.Ready})
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, 256)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in stepup.Request
	if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "only challenge_id is accepted", 400)
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	out, err := m.confirm(r, in.ChallengeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
