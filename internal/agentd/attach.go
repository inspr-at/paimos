// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/workorders"
)

// AttachConfig is supplied by paired serve only. Neither local request fields
// nor AEON_URL can set the origin, credentials, computer or allowlisted folder.
type AttachConfig struct {
	Origin, ComputerID, Host, Workspace string
	Executables                         map[string]string
	Identities                          map[string]agentsetup.AttachIdentity
	LocalSigner                         func(context.Context, string, string, string) (string, error)
	Exchange                            func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error)
}
type AttachManager struct {
	mu        sync.Mutex
	cfg       AttachConfig
	observe   func(int) (attachObservation, error)
	ancestry  func(int) (attachObservation, error)
	signature func(context.Context, string) (attachSignature, error)
	sessions  map[string]*localAttach
	closed    bool
}
type localAttach struct {
	peer               attachwatch.Process
	snapshot           attachwatch.Snapshot
	tail               *attachTail
	view               attachwatch.View
	requested          bool
	sequence           int64
	touched            time.Time
	confirmation       chan localConsentResult
	signatureProof     string
	confirmedNonce     string
	cancelConfirmation context.CancelFunc
	confirmedDigest    string
	image              os.FileInfo
	checking           bool
}
type localConsentResult struct {
	signature string
	err       error
}

type AttachLocalRequest struct {
	Operation  string `json:"operation"`
	ID         string `json:"id,omitempty"`
	Digest     string `json:"request_digest,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Harness    string `json:"harness,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
	TicketID   string `json:"ticket_id,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	StatusOnly bool   `json:"status_only,omitempty"`
}
type AttachLocalView struct {
	ConsentMode string               `json:"consent_mode,omitempty"`
	Reason      string               `json:"reason,omitempty"`
	ID          string               `json:"id"`
	Origin      string               `json:"origin"`
	Snapshot    attachwatch.Snapshot `json:"snapshot"`
	Digest      string               `json:"request_digest"`
	State       string               `json:"state"`
	Code        string               `json:"user_code,omitempty"`
	SessionID   *string              `json:"session_id,omitempty"`
	// ExpiresAt is the server's expiry of a request that waits for approval; the
	// helper uses it to say how long the code stays valid.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func NewAttachManager(c AttachConfig) (*AttachManager, error) {
	if ValidateBaseURL(c.Origin) != nil || !workorders.UUID(c.ComputerID) || !attachwatch.PhysicalPath(c.Workspace) || !attachwatch.Text(c.Host, 128) || c.Exchange == nil {
		return nil, errors.New("paired attach configuration unavailable")
	}
	if c.LocalSigner == nil {
		c.LocalSigner = func(context.Context, string, string, string) (string, error) {
			return "", errors.New("Touch ID unavailable; upgrade this computer's pairing to enable Touch ID")
		}
	}
	c.Executables = maps.Clone(c.Executables)
	c.Identities = maps.Clone(c.Identities)
	return &AttachManager{cfg: c, observe: observeAttachProcess, ancestry: observeAttachProcessIdentity, signature: inspectAttachSignature, sessions: make(map[string]*localAttach)}, nil
}
func (m *AttachManager) localView(id string, s *localAttach) AttachLocalView {
	v := AttachLocalView{ConsentMode: s.view.ConsentMode, ID: id, Origin: m.cfg.Origin, Snapshot: s.snapshot, Digest: s.snapshot.Digest(), State: s.view.State, Code: s.view.UserCode, SessionID: s.view.SessionID}
	if !s.view.ExpiresAt.IsZero() {
		expires := s.view.ExpiresAt
		v.ExpiresAt = &expires
	}
	return v
}
func (m *AttachManager) request(s *localAttach, id, operation string) attachwatch.DeviceRequest {
	return attachwatch.DeviceRequest{LocalConsentProofVersion: attachwatch.LocalConsentProofVersion, Operation: operation, RequestID: id, ComputerID: m.cfg.ComputerID, Snapshot: s.snapshot, Digest: s.snapshot.Digest(), Sequence: s.sequence}
}
func (m *AttachManager) end(ctx context.Context, id string, s *localAttach) {
	m.endAs(ctx, id, s, "detach")
}
func (m *AttachManager) endAs(ctx context.Context, id string, s *localAttach, operation string) {
	delete(m.sessions, id)
	if s.cancelConfirmation != nil {
		s.cancelConfirmation()
	}
	if s.tail != nil {
		s.tail.close()
	}
	if s.requested {
		_, _ = m.cfg.Exchange(ctx, m.request(s, id, operation))
	}
}
func (m *AttachManager) Sweep(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		// Local previews may stay open while the person reads the prompt. After
		// confirmation the foreground helper must poll; no daemon-side renewal.
		limit := 10 * time.Minute
		if s.requested {
			limit = 15 * time.Second
		}
		if time.Since(s.touched) > limit {
			m.end(ctx, id, s)
		}
	}
}
func (m *AttachManager) Close(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for id, s := range m.sessions {
		m.end(ctx, id, s)
	}
}
func (m *AttachManager) handle(ctx context.Context, peer attachObservation, in AttachLocalRequest) (AttachLocalView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reject := errors.New("attach rejected; inspect the process and approved folder")
	if m.closed {
		return AttachLocalView{}, reject
	}
	if in.Operation == "preview" {
		if len(m.sessions) >= 8 || !workorders.UUID(in.ProjectID) || !workorders.UUID(in.TicketID) {
			return AttachLocalView{}, reject
		}
		observed, err := m.observe(in.PID)
		if err != nil || observed.UID != os.Getuid() || !independentAttachPeer(peer, observed, m.ancestry) || !attachwatch.Within(m.cfg.Workspace, observed.CWD) {
			return AttachLocalView{}, reject
		}
		image, err := m.validateHarnessImageUnlocked(ctx, observed, in.Harness)
		if err != nil {
			return AttachLocalView{}, err
		}
		if m.closed || len(m.sessions) >= 8 || !independentAttachPeer(peer, observed, m.ancestry) {
			return AttachLocalView{}, reject
		}
		// Status-only must be explicit; a missing transcript never downgrades watch consent.
		if !in.StatusOnly && in.Transcript == "" {
			return AttachLocalView{}, errors.New("watch requires --transcript PATH; choose --status-only for no conversation text")
		}
		if in.StatusOnly && in.Transcript != "" {
			return AttachLocalView{}, errors.New("status-only attach must not include a transcript")
		}
		mode, fileID := attachwatch.ModeLease, ""
		var tail *attachTail
		if !in.StatusOnly {
			tail, err = openAttachTail(in.Transcript, observed.UID)
			if err != nil {
				return AttachLocalView{}, reject
			}
			mode, fileID = "", tail.id
		}
		var b [16]byte
		if _, err = rand.Read(b[:]); err != nil {
			if tail != nil {
				tail.close()
			}
			return AttachLocalView{}, reject
		}
		id := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		s := &localAttach{peer: peer.Process, snapshot: attachwatch.Snapshot{Mode: mode, ComputerID: m.cfg.ComputerID, ProjectID: in.ProjectID, TicketID: in.TicketID, Host: m.cfg.Host, Harness: in.Harness, Process: observed.Process, Transcript: in.Transcript, FileID: fileID, Platform: runtime.GOOS}, tail: tail, touched: time.Now(), view: attachwatch.View{State: "local_review"}}
		s.image = image
		if !s.snapshot.Valid() {
			if tail != nil {
				tail.close()
			}
			return AttachLocalView{}, reject
		}
		m.sessions[id] = s
		return m.localView(id, s), nil
	}
	s := m.sessions[in.ID]
	if s == nil || s.peer != peer.Process {
		return AttachLocalView{}, reject
	}
	if in.Operation == "detach" {
		m.end(ctx, in.ID, s)
		s.view.State = "detached"
		return m.localView(in.ID, s), nil
	}
	if s.checking {
		return AttachLocalView{}, reject
	}
	s.checking = true
	defer func() { s.checking = false }()
	observed, err := m.observe(s.snapshot.Process.PID)
	if errors.Is(err, errAttachExited) {
		m.endAs(ctx, in.ID, s, "exited")
		s.view.State = "confirmed_exited"
		return m.localView(in.ID, s), nil
	}
	if err != nil || observed.Process != s.snapshot.Process || !m.unchangedHarnessImage(observed, s.image) || !independentAttachPeer(peer, observed, m.ancestry) || s.tail != nil && s.tail.check() != nil || in.Digest != s.snapshot.Digest() {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("identity changed; watch detached")
	}
	if in.Operation == "confirm" && !s.requested {
		if err := m.recheckHarnessImage(ctx, peer, observed, in.ID, s); err != nil {
			if m.sessions[in.ID] == s {
				m.end(ctx, in.ID, s)
			}
			return AttachLocalView{}, err
		}
		// Best-effort terminal factor only; same-UID agents can emulate a PTY.
		// The person approval is the gate; strict mode also needs the pairing-pinned enclave signature.
		s.sequence = 0
		view, err := m.cfg.Exchange(ctx, m.request(s, in.ID, "request"))
		if err != nil {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, errors.New("paired instance refused attach")
		}
		if view.Digest != s.snapshot.Digest() || view.RequestID != in.ID || view.Snapshot != s.snapshot {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, errors.New("paired instance snapshot mismatch")
		}
		s.requested = true
		if !attachwatch.ConsentModeValid(view.ConsentMode) || view.ConsentDigest != attachwatch.ConsentDigest(in.ID, view.Digest, view.ConsentMode) {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, errors.New("paired instance lacks attach consent binding; update Aeon and agentd")
		}
		s.view = view
		s.touched = time.Now()
		return m.localView(in.ID, s), nil
	}
	if in.Operation != "poll" || !s.requested {
		return AttachLocalView{}, reject
	}
	if time.Since(s.touched) < time.Second {
		return AttachLocalView{}, errors.New("attach poll too frequent")
	}
	// LeaseUntil belongs to the DB clock. The server checks it before accepting
	// each poll; only local foreground inactivity uses our monotonic clock.
	if time.Since(s.touched) > 15*time.Second {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch unreachable; attach again")
	}
	if s.confirmation != nil && s.confirmedDigest == "" {
		select {
		case result := <-s.confirmation:
			authErr := result.err
			if authErr == nil && result.signature == "" {
				authErr = errors.New("local confirmation returned an empty signature")
			}
			s.cancelConfirmation()
			if authErr != nil {
				m.end(ctx, in.ID, s)
				s.view.State = "detached"
				v := m.localView(in.ID, s)
				v.Reason = authErr.Error()
				return v, nil
			}
			s.confirmedDigest = s.view.ConsentDigest
			s.confirmedNonce = s.view.LocalAuthNonce
			s.signatureProof = result.signature
		default:
		}
	}
	req := m.request(s, in.ID, "poll")
	// A pending poll discovers the policy pinned by browser approval; a setting
	// may have changed since the previous pending response. Only echo a pin once
	// approval has been observed, otherwise that legitimate change would conflict.
	if s.view.State != "pending" {
		req.ConsentDigest = s.view.ConsentDigest
	}
	if s.view.State == "approved" && s.confirmedDigest == s.view.ConsentDigest && s.confirmedNonce == s.view.LocalAuthNonce {
		req.LocalAuthSignature = s.signatureProof
		req.LocalAuthNonce = s.confirmedNonce
	}
	s.sequence++
	req.Sequence = s.sequence
	if s.view.State == "active" && s.tail != nil {
		req.Text, err = s.tail.next()
		if err != nil {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, err
		}
	}
	observed, err = m.observe(s.snapshot.Process.PID)
	if err != nil || observed.Process != s.snapshot.Process || !m.unchangedHarnessImage(observed, s.image) || !independentAttachPeer(peer, observed, m.ancestry) || s.tail != nil && s.tail.check() != nil {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("identity changed; watch detached")
	}
	if err := m.recheckHarnessImage(ctx, peer, observed, in.ID, s); err != nil {
		if m.sessions[in.ID] == s {
			m.end(ctx, in.ID, s)
		}
		return AttachLocalView{}, err
	}
	view, err := m.cfg.Exchange(ctx, req)
	// Never retry an uncertain text submission, and never retain it for recovery.
	req.Text = ""
	if err != nil {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch unreachable or revoked; attach again")
	}
	if view.Digest != s.snapshot.Digest() || view.RequestID != in.ID || view.Snapshot != s.snapshot {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch binding changed")
	}
	// A new consent binding may change while pending, never after approval.
	if !attachwatch.ConsentModeValid(view.ConsentMode) || view.ConsentDigest != attachwatch.ConsentDigest(in.ID, view.Digest, view.ConsentMode) ||
		s.view.State != "pending" && (view.ConsentMode != s.view.ConsentMode || view.ConsentDigest != s.view.ConsentDigest) ||
		view.State == "active" && view.ConsentMode == attachwatch.ConsentLocalAuth && s.confirmedDigest != view.ConsentDigest {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch consent binding changed or local confirmation missing")
	}
	if view.State == "approved" && view.ConsentMode == attachwatch.ConsentLocalAuth && (!attachwatch.LocalAuthNonceValid(view.LocalAuthNonce) || s.view.State == "approved" && s.view.LocalAuthNonce != view.LocalAuthNonce) {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("local confirmation challenge changed or missing")
	}
	if view.State == "approved" && view.ConsentMode == attachwatch.ConsentLocalAuth && s.confirmation == nil {
		// This goroutine owns only a result channel, not session state; revocation,
		// peer loss and Close cancel it. Socket polls stay fast during the OS dialog.
		authCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		s.cancelConfirmation = cancel
		result := make(chan localConsentResult, 1)
		s.confirmation = result
		reason := attachwatch.LocalConsentReason(s.snapshot)
		consent, nonce := view.ConsentDigest, view.LocalAuthNonce
		go func() {
			proof, err := m.cfg.LocalSigner(authCtx, consent, nonce, reason)
			result <- localConsentResult{signature: proof, err: err}
		}()
	}
	if view.State != "pending" && view.State != "active" && view.State != "approved" {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch ended; attach again")
	}
	if s.view.State != "active" && view.State == "active" && s.tail != nil {
		// Approval time is not permission to upload turns written before activation.
		if err = s.tail.startNow(); err != nil {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, err
		}
	}
	if view.State == "active" {
		s.signatureProof = ""
	}
	s.view = view
	s.touched = time.Now()
	return m.localView(in.ID, s), nil
}
func (m *AttachManager) serve(w http.ResponseWriter, r *http.Request, token string) {
	if !authorized(r, token) {
		http.Error(w, "unauthorized", 403)
		return
	}
	peer, err := attachPeer(r)
	if err != nil {
		http.Error(w, "interactive kernel-checked helper required", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in AttachLocalRequest
	if workorders.Decode(r, &in) != nil {
		http.Error(w, "invalid attach", 400)
		return
	}
	view, err := m.handle(r.Context(), peer, in)
	if err != nil {
		var identityError *AttachLocalError
		if errors.As(err, &identityError) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(identityError)
		} else {
			http.Error(w, err.Error(), 409)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}
