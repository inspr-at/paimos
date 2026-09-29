// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/workorders"
)

// AttachConfig is supplied by paired serve only. Neither local request fields
// nor AEON_URL can set the origin, credentials, computer or allowlisted folder.
type AttachConfig struct {
	Origin, ComputerID, Host, Workspace string
	Executables                         map[string]string
	LocalAuth                           LocalAuthenticator
	Exchange                            func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error)
}
type AttachManager struct {
	mu       sync.Mutex
	cfg      AttachConfig
	observe  func(int) (attachObservation, error)
	sessions map[string]*localAttach
}
type localAttach struct {
	peer               attachwatch.Process
	snapshot           attachwatch.Snapshot
	tail               *attachTail
	view               attachwatch.View
	requested          bool
	sequence           int64
	touched            time.Time
	confirmation       chan error
	cancelConfirmation context.CancelFunc
	confirmedDigest    string
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
}
type AttachLocalView struct {
	Reason    string               `json:"reason,omitempty"`
	ID        string               `json:"id"`
	Origin    string               `json:"origin"`
	Snapshot  attachwatch.Snapshot `json:"snapshot"`
	Digest    string               `json:"request_digest"`
	State     string               `json:"state"`
	Code      string               `json:"user_code,omitempty"`
	SessionID *string              `json:"session_id,omitempty"`
}

func NewAttachManager(c AttachConfig) (*AttachManager, error) {
	if ValidateBaseURL(c.Origin) != nil || !workorders.UUID(c.ComputerID) || !attachwatch.PhysicalPath(c.Workspace) || !attachwatch.Text(c.Host, 128) || c.Exchange == nil {
		return nil, errors.New("paired attach configuration unavailable")
	}
	if c.LocalAuth == nil {
		c.LocalAuth = systemLocalAuthenticator{}
	}
	return &AttachManager{cfg: c, observe: observeAttachProcess, sessions: make(map[string]*localAttach)}, nil
}
func (m *AttachManager) localView(id string, s *localAttach) AttachLocalView {
	return AttachLocalView{ID: id, Origin: m.cfg.Origin, Snapshot: s.snapshot, Digest: s.snapshot.Digest(), State: s.view.State, Code: s.view.UserCode, SessionID: s.view.SessionID}
}
func (m *AttachManager) request(s *localAttach, id, operation string) attachwatch.DeviceRequest {
	return attachwatch.DeviceRequest{Operation: operation, RequestID: id, ComputerID: m.cfg.ComputerID, Snapshot: s.snapshot, Digest: s.snapshot.Digest(), Sequence: s.sequence}
}
func (m *AttachManager) end(ctx context.Context, id string, s *localAttach) {
	delete(m.sessions, id)
	if s.cancelConfirmation != nil {
		s.cancelConfirmation()
	}
	s.tail.close()
	if s.requested {
		_, _ = m.cfg.Exchange(ctx, m.request(s, id, "detach"))
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
	for id, s := range m.sessions {
		m.end(ctx, id, s)
	}
}
func (m *AttachManager) handle(ctx context.Context, peer attachObservation, in AttachLocalRequest) (AttachLocalView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reject := errors.New("attach rejected; inspect the process, approved folder and transcript")
	if in.Operation == "preview" {
		if len(m.sessions) >= 8 || !workorders.UUID(in.ProjectID) || !workorders.UUID(in.TicketID) {
			return AttachLocalView{}, reject
		}
		observed, err := m.observe(in.PID)
		if err != nil || observed.UID != os.Getuid() || !independentAttachPeer(peer, observed, m.observe) || !attachwatch.Within(m.cfg.Workspace, observed.CWD) {
			return AttachLocalView{}, reject
		}
		approved := m.cfg.Executables[in.Harness]
		physical, err := filepath.EvalSymlinks(approved)
		if err != nil || approved == "" || observed.Executable != physical {
			return AttachLocalView{}, reject
		}
		tail, err := openAttachTail(in.Transcript, observed.UID)
		if err != nil {
			return AttachLocalView{}, reject
		}
		var b [16]byte
		if _, err = rand.Read(b[:]); err != nil {
			tail.close()
			return AttachLocalView{}, reject
		}
		id := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		s := &localAttach{peer: peer.Process, snapshot: attachwatch.Snapshot{ComputerID: m.cfg.ComputerID, ProjectID: in.ProjectID, TicketID: in.TicketID, Host: m.cfg.Host, Harness: in.Harness, Process: observed.Process, Transcript: in.Transcript, FileID: tail.id, Platform: runtime.GOOS}, tail: tail, touched: time.Now(), view: attachwatch.View{State: "local_review"}}
		if !s.snapshot.Valid() {
			tail.close()
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
	observed, err := m.observe(s.snapshot.Process.PID)
	if err != nil || observed.Process != s.snapshot.Process || !independentAttachPeer(peer, observed, m.observe) || s.tail.check() != nil || in.Digest != s.snapshot.Digest() {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("identity changed; watch detached")
	}
	if in.Operation == "confirm" && !s.requested {
		// Best-effort terminal factor only; same-UID agents can emulate a PTY.
		// The person approval is the gate; strict mode also needs LocalAuthentication.
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
		s.view = view
		s.requested = true
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
		case authErr := <-s.confirmation:
			s.cancelConfirmation()
			if authErr != nil {
				m.end(ctx, in.ID, s)
				s.view.State = "detached"
				v := m.localView(in.ID, s)
				v.Reason = authErr.Error()
				return v, nil
			}
			s.confirmedDigest = s.view.ConsentDigest
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
	req.LocalConfirmed = s.confirmedDigest != "" && s.confirmedDigest == s.view.ConsentDigest
	s.sequence++
	req.Sequence = s.sequence
	if s.view.State == "active" {
		req.Text, err = s.tail.next()
		if err != nil {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, err
		}
	}
	observed, err = m.observe(s.snapshot.Process.PID)
	if err != nil || observed.Process != s.snapshot.Process || !independentAttachPeer(peer, observed, m.observe) || s.tail.check() != nil {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("identity changed; watch detached")
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
	// Missing fields are the legacy mode-A server. A new consent binding may
	// change while pending, but never after approval or activation.
	if view.ConsentMode != "" && (!attachwatch.ConsentModeValid(view.ConsentMode) || view.ConsentDigest != attachwatch.ConsentDigest(in.ID, view.Digest, view.ConsentMode)) ||
		s.view.State != "pending" && (view.ConsentMode != s.view.ConsentMode || view.ConsentDigest != s.view.ConsentDigest) ||
		view.State == "active" && view.ConsentMode == attachwatch.ConsentLocalAuth && s.confirmedDigest != view.ConsentDigest {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch consent binding changed or local confirmation missing")
	}
	if view.State == "approved" && view.ConsentMode == attachwatch.ConsentLocalAuth && s.confirmation == nil {
		// This goroutine owns only a result channel, not session state; revocation,
		// peer loss and Close cancel it. Socket polls stay fast during the OS dialog.
		authCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		s.cancelConfirmation = cancel
		result := make(chan error, 1)
		s.confirmation = result
		reason := fmt.Sprintf("Allow watching %s session PID %d on %s", s.snapshot.Harness, s.snapshot.Process.PID, s.snapshot.Host)
		go func() { result <- m.cfg.LocalAuth.Confirm(authCtx, reason) }()
	}
	if view.State != "pending" && view.State != "active" && view.State != "approved" {
		m.end(ctx, in.ID, s)
		return AttachLocalView{}, errors.New("watch ended; attach again")
	}
	if s.view.State != "active" && view.State == "active" {
		// Approval time is not permission to upload turns written before activation.
		if err = s.tail.startNow(); err != nil {
			m.end(ctx, in.ID, s)
			return AttachLocalView{}, err
		}
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
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}
