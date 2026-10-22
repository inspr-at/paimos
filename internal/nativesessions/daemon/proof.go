// SPDX-License-Identifier: AGPL-3.0-only

// Package daemon defines bounded private evidence between the paired daemon
// and the native HTTP transport. It never supplies a person or a permission.
package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const ProofHeader = "X-Aeon-Native-Proof"
const MaxProofBytes = 16384

type identity nativecontract.NativeIdentity
type evidence nativecontract.NativeEvidence
type exit nativecontract.ExitEvidence

// TerminalEvidence is observed lifecycle metadata, never terminal history.
// It contains no output, input, reasoning, tool bodies or credentials.
type TerminalEvidence struct {
	RuntimeEpoch string `json:"runtime_epoch"`
	OutputBytes  uint64 `json:"output_bytes"`
	Frames       uint64 `json:"frames"`
	OutputSHA256 string `json:"output_sha256"`
}

// Proof is private to the authenticated paired connection. Identities and
// observations are rechecked against SQL; possession never grants control.
type Proof struct {
	WorkspaceRoot string                     `json:"workspace_root,omitempty"`
	DaemonID      string                     `json:"daemon_id"`
	Generation    string                     `json:"generation"`
	Ledger        string                     `json:"ledger"`
	Capability    string                     `json:"capability"`
	WorkerLease   string                     `json:"worker_lease"`
	SessionRef    string                     `json:"session_ref,omitempty"`
	ReservationID string                     `json:"reservation_id,omitempty"`
	ObservedAt    time.Time                  `json:"observed_at"`
	ExpiresAt     time.Time                  `json:"expires_at"`
	Identity      identity                   `json:"identity"`
	Evidence      evidence                   `json:"evidence"`
	Exit          *exit                      `json:"exit,omitempty"`
	LocalStart    *nativecontract.LocalStart `json:"local_start,omitempty"`
	Terminal      *TerminalEvidence          `json:"terminal,omitempty"`
}

func (p *Proof) SetRuntime(i nativecontract.NativeIdentity, e nativecontract.NativeEvidence, x *nativecontract.ExitEvidence) {
	p.Identity, p.Evidence = identity(i), evidence(e)
	p.Exit = nil
	if x != nil {
		v := exit(*x)
		p.Exit = &v
	}
}
func (p Proof) Runtime() (nativecontract.NativeIdentity, nativecontract.NativeEvidence, *nativecontract.ExitEvidence) {
	var x *nativecontract.ExitEvidence
	if p.Exit != nil {
		v := nativecontract.ExitEvidence(*p.Exit)
		x = &v
	}
	return nativecontract.NativeIdentity(p.Identity), nativecontract.NativeEvidence(p.Evidence), x
}
func refused() error {
	return &nativecontract.ValidationError{Reason: nativecontract.ReasonGrantMissing, Path: "$"}
}

func Encode(p Proof) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxProofBytes {
		return "", refused()
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func Decode(r *http.Request) (Proof, error) {
	var p Proof
	values := r.Header.Values(ProofHeader)
	if len(values) != 1 || len(values[0]) > base64.RawURLEncoding.EncodedLen(MaxProofBytes) {
		return p, refused()
	}
	raw, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || len(raw) > MaxProofBytes {
		return p, refused()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(&struct{}{}) != io.EOF || len(p.WorkerLease) < 16 || len(p.WorkerLease) > 256 || len(p.SessionRef) > 256 || len(p.DaemonID) < 1 || len(p.DaemonID) > 128 || len(p.Generation) < 1 || len(p.Generation) > 128 || len(p.Ledger) != 36 || len(p.Capability) != 64 {
		return Proof{}, refused()
	}
	if p.Terminal != nil {
		digest, err := hex.DecodeString(p.Terminal.OutputSHA256)
		if err != nil || len(digest) != 32 || p.Terminal.RuntimeEpoch != p.Identity.RuntimeEpoch || p.Terminal.RuntimeEpoch == "" || p.Terminal.OutputBytes > 9007199254740991 || p.Terminal.Frames > 9007199254740991 {
			return Proof{}, refused()
		}
	}
	return p, nil
}
