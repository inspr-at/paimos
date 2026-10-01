// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/linkvault"
)

var (
	ErrToken       = errors.New("invalid token")
	ErrUnavailable = errors.New("signing key store unavailable")
)

const keysetPurpose = "aithema-signing-keyset-v1"

// Config pins the exact token issuer and delegated audience. Skew defaults to
// 60 seconds; negative values or anything over 60 seconds are refused.
type Config struct {
	Issuer   string
	Audience string
	Skew     time.Duration
	Clock    func() time.Time
}

// KeySet uses Ed25519 (EdDSA), permitted by the binding contract. The standard
// library provides fixed-size keys/signatures without algorithm negotiation.
// It derives a distinct vault key from Aeon's existing host session secret.
type KeySet struct {
	store Store
	key   []byte
	cfg   Config
}

type signingKey struct {
	ID       string `json:"kid"`
	Seed     []byte `json:"seed,omitempty"`
	Public   []byte `json:"public"`
	RetireAt int64  `json:"retire_at,omitempty"`
}
type keyState struct {
	Active    signingKey   `json:"active"`
	Next      signingKey   `json:"next"`
	Retired   []signingKey `json:"retired,omitempty"`
	RotatedAt int64        `json:"rotated_at"`
}

func New(ctx context.Context, store Store, master []byte, cfg Config) (*KeySet, error) {
	if store == nil || !validRef(store.Binding()) || len(master) < 32 || !validIssuer(cfg.Issuer) || !utf8.ValidString(cfg.Audience) || len(cfg.Audience) == 0 || utf8.RuneCountInString(cfg.Audience) > 256 || cfg.Skew < 0 || cfg.Skew > time.Duration(MaxSkewSeconds)*time.Second {
		return nil, errors.New("invalid signing configuration")
	}
	if cfg.Skew == 0 {
		cfg.Skew = time.Duration(MaxSkewSeconds) * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	sum := sha256.Sum256(append([]byte("aeon/aithema-signing-v1\x00"), master...))
	k := &KeySet{store: store, key: sum[:], cfg: cfg}
	if err := k.transact(ctx, func(*keyState, int64) error { return nil }); err != nil {
		return nil, err
	}
	return k, nil
}

func newKey() (signingKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return signingKey{}, ErrUnavailable
	}
	id := sha256.Sum256(pub)
	return signingKey{ID: base64.RawURLEncoding.EncodeToString(id[:]), Seed: priv.Seed(), Public: pub}, nil
}
func checkKey(key signingKey, private bool) bool {
	if len(key.Public) != ed25519.PublicKeySize {
		return false
	}
	id := sha256.Sum256(key.Public)
	if key.ID != base64.RawURLEncoding.EncodeToString(id[:]) {
		return false
	}
	if private {
		if len(key.Seed) != ed25519.SeedSize || key.RetireAt != 0 {
			return false
		}
		return bytes.Equal(ed25519.NewKeyFromSeed(key.Seed).Public().(ed25519.PublicKey), key.Public)
	}
	return len(key.Seed) == 0 && key.RetireAt > 0
}

func (k *KeySet) transact(ctx context.Context, fn func(*keyState, int64) error) error {
	return k.store.Update(ctx, func(stored []byte) ([]byte, error) {
		now := k.cfg.Clock().Unix()
		var state keyState
		var original string
		if len(stored) == 0 {
			var err error
			state.Active, err = newKey()
			if err != nil {
				return nil, err
			}
			state.Next, err = newKey()
			if err != nil {
				return nil, err
			}
			state.RotatedAt = now
		} else {
			plain, err := linkvault.Decrypt(k.key, k.store.Binding(), keysetPurpose, stored)
			if err != nil || json.Unmarshal([]byte(plain), &state) != nil {
				return nil, ErrUnavailable
			}
			original = plain
			if !checkKey(state.Active, true) || !checkKey(state.Next, true) || state.Active.ID == state.Next.ID {
				return nil, ErrUnavailable
			}
			seen := map[string]bool{state.Active.ID: true, state.Next.ID: true}
			for _, retired := range state.Retired {
				if !checkKey(retired, false) || seen[retired.ID] {
					return nil, ErrUnavailable
				}
				seen[retired.ID] = true
			}
		}
		kept := state.Retired[:0]
		for _, retired := range state.Retired {
			if retired.RetireAt > now {
				kept = append(kept, retired)
			}
		}
		state.Retired = kept
		// Normal traffic rotates daily. The next public key has been prepublished
		// throughout that interval. Explicit Rotate is available to trusted callers.
		if now-state.RotatedAt >= 24*60*60 {
			if err := rotateState(&state, now); err != nil {
				return nil, err
			}
		}
		if err := fn(&state, now); err != nil {
			return nil, err
		}
		after, err := json.Marshal(state)
		if err != nil {
			return nil, ErrUnavailable
		}
		if len(stored) != 0 && bytes.Equal([]byte(original), after) {
			return stored, nil
		}
		encrypted, err := linkvault.Encrypt(k.key, k.store.Binding(), keysetPurpose, string(after))
		if err != nil {
			return nil, ErrUnavailable
		}
		return encrypted, nil
	})
}

func rotateState(state *keyState, now int64) error {
	next, err := newKey()
	if err != nil {
		return err
	}
	old := state.Active
	// Mint never issues future-iat tokens. Retain the old public key for the
	// complete lifetime plus maximum permitted verifier skew after rotation.
	old.Seed = nil
	old.RetireAt = now + MaxLifetimeSeconds + MaxSkewSeconds
	state.Retired = append(state.Retired, old)
	state.Active, state.Next, state.RotatedAt = state.Next, next, now
	return nil
}
func (k *KeySet) Rotate(ctx context.Context) error {
	return k.transact(ctx, func(state *keyState, now int64) error { return rotateState(state, now) })
}

// JWK contains public Ed25519 verification material only.
type JWK struct {
	Type      string `json:"kty"`
	Curve     string `json:"crv"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	ID        string `json:"kid"`
	X         string `json:"x"`
}
type JWKS struct {
	Keys []JWK `json:"keys"`
}

func (k *KeySet) JWKS(ctx context.Context) (JWKS, error) {
	out := JWKS{Keys: []JWK{}}
	err := k.transact(ctx, func(s *keyState, _ int64) error {
		keys := append([]signingKey{s.Active, s.Next}, s.Retired...)
		for _, key := range keys {
			out.Keys = append(out.Keys, JWK{Type: "OKP", Curve: "Ed25519", Algorithm: "EdDSA", Use: "sig", ID: key.ID, X: base64.RawURLEncoding.EncodeToString(key.Public)})
		}
		return nil
	})
	if err != nil {
		return JWKS{}, err
	}
	return out, nil
}
