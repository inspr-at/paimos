// SPDX-License-Identifier: AGPL-3.0-only

// Package stepup defines the client/daemon boundary for per-action confirmation.
package stepup

import (
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"math/big"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

const Header = "Aeon-Step-Up"
const MaxLifetime = 5 * time.Minute
const ConfirmTimeout = 90 * time.Second

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var hex256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidID(id string) bool { return uuid.MatchString(id) }

// Required is the entire 428 body visible to an agent. In particular, neither
// the signing nonce nor prompt text is relayed through the requesting agent.
type Required struct {
	Code        string    `json:"code"`
	ChallengeID string    `json:"challenge_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func ValidExpiry(expires, now time.Time) bool {
	return expires.After(now) && !expires.After(now.Add(MaxLifetime))
}

// Challenge is fetched by the daemon at its pinned origin using pairing proof.
type Challenge struct {
	Summary      string    `json:"summary"`
	Nonce        string    `json:"nonce"`
	ActionDigest string    `json:"action_digest"`
	ExpiresAt    time.Time `json:"expires_at"`
	ComputerID   string    `json:"computer_id"`
}

func (c Challenge) Valid(computer string, now time.Time) bool {
	if !ValidID(computer) || c.ComputerID != computer || !hex256.MatchString(c.Nonce) || !hex256.MatchString(c.ActionDigest) || !ValidExpiry(c.ExpiresAt, now) || c.Summary == "" || len(c.Summary) > 256 || !utf8.ValidString(c.Summary) {
		return false
	}
	for _, r := range c.Summary {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// Hash signs nonce || action_digest as their fixed-width lowercase hex wire
// strings, using ECDSA P-256 with SHA-256 (the enclave API accepts the hash).
func (c Challenge) Hash() []byte {
	h := sha256.Sum256([]byte(c.Nonce + c.ActionDigest))
	return h[:]
}

func ValidSignature(signature string) bool {
	if signature == "" || len(signature) > 100 {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(signature)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != signature {
		return false
	}
	var pair struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(raw, &pair)
	return err == nil && len(rest) == 0 && pair.R != nil && pair.S != nil && pair.R.Sign() > 0 && pair.S.Sign() > 0 && pair.R.BitLen() <= 256 && pair.S.BitLen() <= 256
}

type Request struct {
	ChallengeID string `json:"challenge_id"`
}
type Proof struct {
	ChallengeID string `json:"challenge_id"`
	Signature   string `json:"signature"`
}
type Info struct {
	Origin     string `json:"origin"`
	ComputerID string `json:"computer_id"`
	Ready      bool   `json:"ready"`
}
