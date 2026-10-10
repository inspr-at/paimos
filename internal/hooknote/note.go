// SPDX-License-Identifier: AGPL-3.0-only

// Package hooknote is the credential-free attached-note boundary between a
// verified hook and agentd. S2-3 implements NoteSource against the paired
// attach exchange; until then the daemon installs Unavailable and releases
// no body. The nonce is a one-use receipt challenge, not a credential:
// possession without the authenticated daemon and the approved peer is useless.
//
// # Protection boundary
//
// Inside the boundary: other sessions; sibling and subagent contexts the hook
// input identifies as separate; other harness processes; other UIDs and PID
// reuse; revocation or a second grant before the hook finishes writing;
// replay; a malformed nonce; and forged hook input aimed at a different
// session. A revocation or a second grant that arrives during or after
// settlement does not recall a note the hook has already written in full.
// That receipt is the outcome the server recorded.
//
// Outside the boundary, with no stronger claim: a process inside the approved
// session's own process tree (for example a tool shell that session runs) that
// obtains or settles that session's own note. It is the same recipient. An
// inherited socket or a direct-child shell that execs the approved helper is
// that case. Deliberate same-UID tampering, including passing a descriptor on
// purpose or replacing an executable on disk, is outside the boundary as well.
// The same shapes are refused when the process belongs to another session or
// another harness. The receipt label for a full write is "hook reported".
package hooknote

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// MaxBodyBytes is the raw UTF-8 ceiling, enforced before a note is framed.
	MaxBodyBytes = 4 << 10
	// MaxContextRunes is the rendered external-data block, counted after escaping.
	// Claude's hook context limit is 10_000 characters and can spill past that;
	// 8_000 leaves room for the frame and rejects over-long output instead of
	// truncating it into something that looks complete.
	MaxContextRunes = 8000
	// MaxOutputRunes is the full hook JSON, including the user-visible notice.
	MaxOutputRunes = 10000
	MaxOwnerRunes  = 80
)

const (
	OriginOwner = "owner"
	OriginAgent = "agent"

	// OutcomeShown is a full harness-stdout write of an owner note.
	// ReceiptHookReported is the honest label: the hook reported the handoff.
	// It is not evidence that a model read the text.
	OutcomeShown        = "shown"
	ReceiptHookReported = "hook reported"
	// OutcomeUncertain means bytes may have left the daemon but the required
	// evidence is missing. The attempt is not offered again.
	OutcomeUncertain = "uncertain"
	// OutcomeDropped means an agent-origin note produced no model-visible output.
	OutcomeDropped = "dropped"
)

var (
	ErrUnavailable    = errors.New("attached notes are not available")
	ErrEmpty          = errors.New("no attached note")
	ErrRevoked        = errors.New("message generation revoked")
	ErrMalformedNonce = errors.New("malformed nonce")
	ErrPeer           = errors.New("hook peer rejected")
	ErrAmbiguous      = errors.New("ambiguous attached session")
	ErrForged         = errors.New("forged hook binding")
	ErrLimit          = errors.New("attached note exceeds its limit")
	ErrExpired        = errors.New("attached note offer expired")
)

// SettledError is a compare-and-settle result the server already stored.
// Outcome is that stored receipt. A lost response is a plain error and
// carries no outcome; the caller stores uncertain and does not retry.
type SettledError struct {
	Outcome string
}

func (e *SettledError) Error() string {
	if e == nil || e.Outcome == "" {
		return "attached receipt not confirmed"
	}
	return "attached receipt " + e.Outcome
}

// Note is one owner or agent message. Agent-origin notes are notification-only:
// their body must not be placed in hook output. Deadline is the offer expiry
// (RFC3339). The hook checks it before any stdout; a missing or past deadline
// produces no output.
type Note struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Body     string `json:"body,omitempty"`
	Origin   string `json:"origin"`
	Created  string `json:"created"`
	Deadline string `json:"deadline,omitempty"`
}

// Epoch is one binding's settlement generation: its message generation plus
// the registry revocation counter that was current when the grant was offered.
// Revoking that generation bumps the counter. A zero Counter or an empty
// Generation is not current and must not settle as shown.
type Epoch struct {
	Generation string
	Counter    uint64
}

// Binding is the daemon's exact process binding. Client stdin, vendor
// references and environment session ids never populate it. Epoch is stamped
// by Select and is the value Settle compares.
type Binding struct {
	SessionID         string
	MessageGeneration string
	DaemonGeneration  string
	Harness           Process
	HookExecutable    string
	HookDev           uint64
	HookIno           uint64
	VendorRef         string
	Epoch             Epoch
}

// NoteSource is the local offer/receipt port. Offer returns the note, including
// its deadline, and a 256-bit nonce bound to that binding (session, harness,
// message generation, daemon generation, and this source), or ErrEmpty when
// the queue has nothing. The same nonce presented for a different binding is
// not a receipt.
//
// Settle is the server's compare-and-settle and the terminal receipt.
// The caller checks the local grant first. A grant that is already revoked
// or ambiguous is sent as uncertain, so a write that did not complete is
// never shown. Once the hook has written the note in full, the caller sends
// shown. Revocation or a second grant during that call does not change it
// and does not recall the note; those events only block a later offer.
//
// The server records shown only when its own epoch still matches. A stale
// epoch is stored as uncertain and returned as SettledError. The caller
// stores every confirmed SettledError outcome, including dropped, in place
// of the local proposal. A transport failure is a plain error: nothing in the
// error is a recorded receipt, the caller stores uncertain, and it does not
// call Settle again. AEON-393's exchange cannot fill that gap. It accepts
// only message_offer and message_receipt, and message_receipt is this call.
// An already-terminal row is visible only to a second message_receipt.
//
// Settle is idempotent for the same nonce. An uncertain receipt is never
// upgraded to shown. A different outcome conflicts. A nonce settles at most
// once; replay, a late receipt after terminal uncertainty, and a second offer
// of the same note are refused. Expiry is the note deadline. Callers check it
// before any output and settle uncertain when it has passed. Settle is called
// only after the hook has finished its stdout attempt.
type NoteSource interface {
	Offer(ctx context.Context, binding Binding) (Note, string, error)
	Settle(ctx context.Context, nonce, outcome string, epoch Epoch) error
}

// DisclosureValidator performs a fresh remote consent/attempt check after the
// offer returns. Missing or ambiguous validation releases no body.
type DisclosureValidator interface {
	Validate(context.Context, string, Binding) error
}

// Expired reports whether the offer deadline is missing, malformed, or not
// strictly in the future. now is the caller's clock.
func Expired(note Note, now time.Time) bool {
	deadline, ok := parseDeadline(note.Deadline)
	if !ok {
		return true
	}
	return !now.Before(deadline)
}

func parseDeadline(s string) (time.Time, bool) {
	if !plain(s, 40) {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// Unavailable is the production source until the offer/receipt machine lands.
// It never releases a body.
type Unavailable struct{}

func (Unavailable) Offer(context.Context, Binding) (Note, string, error) {
	return Note{}, "", ErrUnavailable
}

func (Unavailable) Settle(context.Context, string, string, Epoch) error {
	return ErrUnavailable
}

// Delivery is off unless a test in this process opts in. There is no
// environment variable, config key or socket token that turns it on.
var delivery struct {
	mu sync.Mutex
	n  int
}

// Enabled reports the release switch. The default is off.
func Enabled() bool {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	return delivery.n > 0
}

// EnableForTest turns the release switch on until cleanup runs. Production
// main never calls it.
func EnableForTest(cleanup func(func())) {
	delivery.mu.Lock()
	delivery.n++
	delivery.mu.Unlock()
	cleanup(func() {
		delivery.mu.Lock()
		delivery.n--
		delivery.mu.Unlock()
	})
}

// ValidNonce is 256 bits of lowercase hex.
func ValidNonce(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidOutcome is the closed set the hook is allowed to report.
func ValidOutcome(s string) bool {
	return s == OutcomeShown || s == OutcomeUncertain || s == OutcomeDropped
}

// ValidID is a lowercase UUID. It is a label, not proof of which session was selected.
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

func plain(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}
