// SPDX-License-Identifier: AGPL-3.0-only

// Package attachwatch carries the content-free immutable approval protocol.
// Conversation text is never part of a snapshot, journal, event or heartbeat.
package attachwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/agentactivity"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ModeLease binds content-free attachment into the immutable approval snapshot.
const ModeLease = "lease"

// Protocol requires origin pinning, redirect refusal and kernel identity checks
// for both conversation watches and status-only leases.
const Protocol = 2

const ConsentAeon = "aeon"
const ConsentLocalAuth = "local_auth"

// Daemon-reported LocalAuthentication capability. These values are advisory:
// only a signature from the pairing-pinned key activates a strict watch.
const (
	LocalAuthAvailable   = "available"
	LocalAuthUnsupported = "unsupported"
	LocalAuthUnsigned    = "unsigned"
	LocalAuthNoGUI       = "no_gui"
	LocalAuthPolicy      = "policy"
	LocalAuthUnreported  = "unreported"
)

func LocalAuthCapabilityReported(s string) bool {
	switch s {
	case LocalAuthAvailable, LocalAuthUnsupported, LocalAuthUnsigned, LocalAuthNoGUI, LocalAuthPolicy:
		return true
	default:
		return false
	}
}

const MaxText = 16 << 10
const Lease = 60 * time.Second

// ComputerMax bounds pending, approved and active attaches on one computer.
const ComputerMax = 8

// AttemptWindow is the tenant attach/lookup attempt-counter lifetime.
const AttemptWindow = 10 * time.Minute

// LiveMax bounds the attach requests one person may have waiting or approved at
// once (not yet a session, not expired). Admission enforces it and the pending
// list returns up to exactly this many, so a request that waits is never hidden
// behind a cut-off. The message is what the paired daemon recognises and prints.
const (
	LiveMax           = 32
	LiveLimitCode     = "attach_live_limit"
	LiveLimitMessage  = "too many attach requests are waiting for approval"
	RefusalVersion    = "version_mismatch"
	RefusalPairing    = "pairing_revoked"
	RefusalTicket     = "ticket_not_visible"
	RefusalExpired    = "code_expired"
	RefusalDraining   = "draining"
	RefusalEnrollment = "enrollment_unavailable"
)

type Process struct {
	PID        int    `json:"pid"`
	UID        int    `json:"uid"`
	Started    string `json:"started"`
	Executable string `json:"executable"`
	CWD        string `json:"cwd"`
}
type Snapshot struct {
	Mode       string  `json:"mode,omitempty"`
	ComputerID string  `json:"computer_id"`
	ProjectID  string  `json:"project_id"`
	TicketID   string  `json:"ticket_id"`
	Host       string  `json:"host"`
	Harness    string  `json:"harness"`
	Process    Process `json:"process"`
	Transcript string  `json:"transcript"`
	FileID     string  `json:"file_id"`
	Platform   string  `json:"platform,omitempty"`
}

func (s Snapshot) Digest() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(append([]byte("aeon.attach.v1\x00"), b...))
	return hex.EncodeToString(h[:])
}
func Text(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
}
func PhysicalPath(s string) bool {
	return Text(s, 1024) && path.IsAbs(s) && path.Clean(s) == s && s != "/"
}
func Within(root, cwd string) bool {
	return PhysicalPath(root) && PhysicalPath(cwd) && (cwd == root || strings.HasPrefix(cwd, root+"/"))
}
func (s Snapshot) Valid() bool {
	content := s.Mode == "" && Text(s.FileID, 128) && PhysicalPath(s.Transcript)
	if s.Mode == ModeLease {
		content = s.Transcript == "" && s.FileID == ""
	}
	return content && (s.Platform == "" || s.Platform == "darwin" || s.Platform == "linux") && Text(s.Host, 128) && s.Process.PID > 0 && s.Process.UID >= 0 && Text(s.Process.Started, 128) && PhysicalPath(s.Process.Executable) && PhysicalPath(s.Process.CWD) && (s.Harness == "codex" || s.Harness == "claude" || s.Harness == "cursor" || s.Harness == "grok" || s.Harness == "gemini" || s.Harness == "opencode")
}

type DeviceRequest struct {
	Doing                     *string                 `json:"doing,omitempty"`
	ToolActivity              *agentactivity.Activity `json:"tool_activity,omitempty"`
	LocalConsentProofVersion  int                     `json:"local_consent_proof_version,omitempty"`
	LocalAuthNonce            string                  `json:"local_auth_nonce,omitempty"`
	LocalAuthSignature        string                  `json:"local_auth_signature,omitempty"`
	AttachProtocol            int                     `json:"attach_protocol,omitempty"`
	ConsentDigest             string                  `json:"consent_digest,omitempty"`
	LocalConfirmed            bool                    `json:"local_confirmed,omitempty"`
	Operation                 string                  `json:"operation"`
	RequestID                 string                  `json:"request_id"`
	ComputerID                string                  `json:"computer_id"`
	DeviceProof               string                  `json:"device_proof,omitempty"`
	PollKey                   string                  `json:"poll_key"`
	Snapshot                  Snapshot                `json:"snapshot"`
	Digest                    string                  `json:"request_digest"`
	Sequence                  int64                   `json:"sequence,omitempty"`
	Text                      string                  `json:"text,omitempty"`
	LocalAuthCapability       string                  `json:"local_auth_capability,omitempty"`
	MessageProtocol           string                  `json:"message_protocol,omitempty"`
	MessageGeneration         string                  `json:"message_generation,omitempty"`
	MessageConsentDigest      string                  `json:"message_consent_digest,omitempty"`
	HookReleaseDigest         string                  `json:"hook_release_digest,omitempty"`
	HookConfigDigest          string                  `json:"hook_config_digest,omitempty"`
	QualifiedHarnessVersion   string                  `json:"qualified_harness_version,omitempty"`
	MessageLocalAuthNonce     string                  `json:"message_local_auth_nonce,omitempty"`
	MessageLocalAuthSignature string                  `json:"message_local_auth_signature,omitempty"`
}

// ConsentDigest binds approval to this request, its snapshot (including the
// chosen content mode) and the consent policy selected by the server.
func ConsentDigest(requestID, snapshotDigest, mode string) string {
	h := sha256.Sum256([]byte("aeon.attach.consent.v1\x00" + requestID + "\x00" + snapshotDigest + "\x00" + mode))
	return hex.EncodeToString(h[:])
}
func ConsentModeValid(mode string) bool { return mode == ConsentAeon || mode == ConsentLocalAuth }

type View struct {
	AgentActivityMode        string     `json:"agent_activity_mode,omitempty"`
	LocalConsentProofVersion int        `json:"local_consent_proof_version,omitempty"`
	LocalAuthNonce           string     `json:"local_auth_nonce,omitempty"`
	ConsentMode              string     `json:"consent_mode"`
	ConsentDigest            string     `json:"consent_digest"`
	RequestID                string     `json:"request_id"`
	Digest                   string     `json:"request_digest"`
	Snapshot                 Snapshot   `json:"snapshot"`
	State                    string     `json:"state"`
	ExpiresAt                time.Time  `json:"expires_at"`
	LeaseUntil               *time.Time `json:"lease_until"`
	SessionID                *string    `json:"session_id"`
	UserCode                 string     `json:"user_code,omitempty"`
}
