// SPDX-License-Identifier: AGPL-3.0-only

package hooknote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

type slotKey struct{}

type connSlot struct {
	conn      net.Conn
	peer      Process
	snapErr   error
	mu        sync.Mutex
	nonce     string
	wrote     bool
	settled   string
	offered   bool
	binding   Binding
	source    NoteSource
	deadline  time.Time
	challenge string
	begun     bool
}

// Server is the credential-free POST /v1/inbox-hook handler. It does not read
// a bearer token. Authority is the kernel peer plus the exact binding.
type Server struct {
	mu      sync.RWMutex
	source  NoteSource
	grants  *Registry
	observe func(int) (Process, error)
}

func NewServer() *Server { return &Server{} }

// Set installs the offer source and the approved bindings. A nil source stays
// Unavailable, which releases no body.
func (s *Server) Set(source NoteSource, grants *Registry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.source = source
	s.grants = grants
}

func (s *Server) current() (NoteSource, *Registry) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.source
	if src == nil {
		src = Unavailable{}
	}
	g := s.grants
	if g == nil {
		g = NewRegistry()
	}
	return src, g
}

func (s *Server) observer() func(int) (Process, error) {
	if s == nil {
		return Observe
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.observe != nil {
		return s.observe
	}
	return Observe
}

// Annotate snapshots the accepted socket before any request bytes are trusted.
func Annotate(ctx context.Context, c net.Conn) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	peer, err := Snapshot(c)
	return context.WithValue(ctx, slotKey{}, &connSlot{conn: c, peer: peer, snapErr: err})
}

type wireRequest struct {
	Op              string `json:"op"`
	Challenge       string `json:"challenge,omitempty"`
	Generation      string `json:"generation,omitempty"`
	Epoch           Epoch  `json:"epoch,omitempty"`
	Event           string `json:"event,omitempty"`
	VendorRef       string `json:"vendor_ref,omitempty"`
	AssertedSession string `json:"asserted_session,omitempty"`
	EnvSession      string `json:"env_session,omitempty"`
	Subagent        bool   `json:"subagent,omitempty"`
	Nonce           string `json:"nonce,omitempty"`
	Outcome         string `json:"outcome,omitempty"`
}

type wireBegin struct {
	Challenge  string `json:"challenge"`
	Generation string `json:"generation"`
	Epoch      Epoch  `json:"epoch"`
}

type wireOffer struct {
	Note  Note   `json:"note"`
	Nonce string `json:"nonce"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost || r.URL.Path != "/v1/inbox-hook" {
		http.NotFound(w, r)
		return
	}
	// The release switch is not a token check. Authorization is ignored.
	if !Enabled() {
		http.Error(w, "not available", http.StatusNotFound)
		return
	}
	slot, _ := r.Context().Value(slotKey{}).(*connSlot)
	if slot == nil || slot.snapErr != nil || slot.conn == nil {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	if err := Recheck(slot.conn, slot.peer); err != nil {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2500*time.Millisecond)
	defer cancel()
	r = r.WithContext(ctx)
	requestDeadline, _ := ctx.Deadline()
	_ = slot.conn.SetReadDeadline(requestDeadline)
	_ = http.NewResponseController(w).SetWriteDeadline(requestDeadline)
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req wireRequest
	if dec.Decode(&req) != nil || dec.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "rejected", http.StatusBadRequest)
		return
	}
	if req.Op == "settle" {
		s.settle(w, r, slot, req)
		return
	}
	if req.Op != "begin" && req.Op != "offer" {
		http.Error(w, "rejected", 400)
		return
	}
	src, grants := s.current()
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.wrote {
		http.Error(w, "rejected", 403)
		return
	}
	var binding Binding
	var err error
	if req.Op == "begin" {
		if slot.begun || !ValidNonce(req.Challenge) {
			http.Error(w, "rejected", 403)
			return
		}
		binding, err = grants.Select(slot.peer, Claim{Event: req.Event, VendorRef: req.VendorRef, AssertedSession: req.AssertedSession, EnvSession: req.EnvSession, Subagent: req.Subagent}, s.observer())
		if err != nil {
			writeGate(w, err)
			return
		}
		slot.challenge = req.Challenge
		slot.binding = binding
		slot.source = src
		slot.begun = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wireBegin{req.Challenge, binding.MessageGeneration, binding.Epoch})
		return
	}
	if !slot.begun || !ValidNonce(req.Challenge) || subtle.ConstantTimeCompare([]byte(slot.challenge), []byte(req.Challenge)) != 1 || req.Generation != slot.binding.MessageGeneration || req.Epoch != slot.binding.Epoch {
		http.Error(w, "rejected", 403)
		return
	}
	binding = slot.binding
	src = slot.source
	// A connection/invocation can claim once, including when its response is lost.
	slot.wrote = true
	if err = grants.BindingCurrent(binding, slot.peer, s.observer()); err != nil {
		writeGate(w, err)
		return
	}
	if err = Recheck(slot.conn, slot.peer); err != nil {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	note, nonce, err := src.Offer(r.Context(), binding)
	if err != nil {
		writeGate(w, err)
		return
	}
	if !ValidNonce(nonce) {
		http.Error(w, "rejected", http.StatusBadRequest)
		return
	}
	if err = Recheck(slot.conn, slot.peer); err != nil {
		_ = src.Settle(r.Context(), nonce, OutcomeUncertain, binding.Epoch)
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	if Expired(note, time.Now()) {
		_ = src.Settle(r.Context(), nonce, OutcomeUncertain, binding.Epoch)
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	deadline, _ := parseDeadline(note.Deadline)
	if note.Origin == OriginAgent {
		note.Body = ""
		note.Owner = ""
	} else if err = validateOwnerNote(note); err != nil {
		_ = src.Settle(r.Context(), nonce, OutcomeUncertain, binding.Epoch)
		http.Error(w, "rejected", http.StatusBadRequest)
		return
	}
	slot.nonce = nonce
	slot.wrote = true
	slot.offered = true
	slot.binding = binding
	slot.source = src
	slot.deadline = deadline
	validator, ok := src.(DisclosureValidator)
	if !ok || validator.Validate(r.Context(), nonce, binding) != nil {
		_ = src.Settle(r.Context(), nonce, OutcomeUncertain, binding.Epoch)
		http.Error(w, "rejected", 403)
		return
	}
	err = grants.Disclose(binding, slot.peer, s.observer(), func() error {
		if Recheck(slot.conn, slot.peer) != nil || Expired(note, time.Now()) || r.Context().Err() != nil {
			return ErrRevoked
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(wireOffer{Note: note, Nonce: nonce})
	})
	if err != nil {
		_ = src.Settle(r.Context(), nonce, OutcomeUncertain, binding.Epoch)
		slot.settled = OutcomeUncertain
		// Encoder may already have written; never include diagnostics or text here.
		http.Error(w, "rejected", 403)
	}
}

func (s *Server) settle(w http.ResponseWriter, r *http.Request, slot *connSlot, req wireRequest) {
	if !ValidNonce(req.Nonce) || !ValidOutcome(req.Outcome) {
		http.Error(w, "rejected", http.StatusBadRequest)
		return
	}
	if err := Recheck(slot.conn, slot.peer); err != nil {
		s.recordUncertain(r.Context(), slot, req.Nonce)
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if !slot.wrote || !slot.offered || slot.source == nil || subtle.ConstantTimeCompare([]byte(slot.nonce), []byte(req.Nonce)) != 1 {
		http.Error(w, "rejected", http.StatusBadRequest)
		return
	}
	if slot.settled != "" {
		if slot.settled == req.Outcome {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "rejected", http.StatusConflict)
		return
	}
	_, grants := s.current()
	observe := s.observer()
	outcome := req.Outcome
	if outcome == OutcomeShown && !slot.deadline.IsZero() && !time.Now().Before(slot.deadline) {
		outcome = OutcomeUncertain
	}
	// Commit checks the grant before the receipt is sent. Shown is sent only
	// while this binding is still the single current grant. A revocation or
	// a second grant that arrives after this check, during Settle, does not
	// recall a note the hook has already written.
	decided, err := grants.Commit(req.Nonce, outcome, slot.binding.Epoch, slot.binding, slot.peer, observe)
	if err != nil || decided == "" {
		decided = OutcomeUncertain
	}
	settleErr := slot.source.Settle(r.Context(), req.Nonce, decided, slot.binding.Epoch)
	if settleErr != nil {
		// Every confirmed server answer replaces the local proposal.
		// SettledError carries that stored receipt, including dropped.
		// A plain error means the response was lost: store uncertain.
		// Do not call Settle again. There is no read-only reconcile on the
		// attached exchange (message_offer and message_receipt only).
		adopted := OutcomeUncertain
		var settled *SettledError
		if errors.As(settleErr, &settled) && ValidOutcome(settled.Outcome) {
			stored, aerr := grants.Adopt(req.Nonce, settled.Outcome)
			if aerr != nil || stored == "" {
				adopted = OutcomeUncertain
			} else {
				adopted = stored
			}
		} else if decided != OutcomeUncertain {
			downgraded, derr := grants.Commit(req.Nonce, OutcomeUncertain, slot.binding.Epoch, slot.binding, slot.peer, observe)
			if derr != nil || downgraded == "" {
				downgraded = OutcomeUncertain
			}
			adopted = downgraded
		}
		slot.settled = adopted
		writeGate(w, settleErr)
		return
	}
	slot.settled = decided
	if decided != req.Outcome {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) recordUncertain(ctx context.Context, slot *connSlot, nonce string) {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if !slot.offered || slot.source == nil || slot.settled != "" || subtle.ConstantTimeCompare([]byte(slot.nonce), []byte(nonce)) != 1 {
		return
	}
	if slot.source.Settle(ctx, nonce, OutcomeUncertain, slot.binding.Epoch) != nil {
		return
	}
	slot.settled = OutcomeUncertain
}

func writeGate(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrEmpty):
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrUnavailable):
		http.Error(w, "not available", http.StatusNotFound)
	case errors.Is(err, ErrMalformedNonce):
		http.Error(w, "rejected", http.StatusBadRequest)
	default:
		http.Error(w, "rejected", http.StatusForbidden)
	}
}

func validateOwnerNote(note Note) error {
	if note.Origin != OriginOwner || !ValidID(note.ID) || !validOwner(note.Owner) || !validBody(note.Body) || !plain(note.Created, 40) || Expired(note, time.Now()) {
		return ErrLimit
	}
	return nil
}

func validOwner(s string) bool {
	if !plain(s, 80) {
		return false
	}
	n := 0
	for _, r := range s {
		n++
		if r >= 0x80 && r <= 0x9f {
			return false
		}
		// Format controls include bidi overrides. The notice is terminal text.
		if r == 0x202A || r == 0x202B || r == 0x202C || r == 0x202D || r == 0x202E || r == 0x2066 || r == 0x2067 || r == 0x2068 || r == 0x2069 || r == 0x200E || r == 0x200F || r == 0x061C {
			return false
		}
	}
	return n > 0 && n <= MaxOwnerRunes
}

func validBody(s string) bool {
	if s == "" || len(s) > MaxBodyBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}
