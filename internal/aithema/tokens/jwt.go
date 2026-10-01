// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
)

const maxTokenBytes = 16 << 10

func (k *KeySet) MintSession(ctx context.Context, claims Claims) (string, error) {
	return k.mint(ctx, Session, claims)
}
func (k *KeySet) MintDelegated(ctx context.Context, claims Claims) (string, error) {
	return k.mint(ctx, Delegated, claims)
}

func (k *KeySet) mint(ctx context.Context, kind Kind, input Claims) (string, error) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > maxTokenBytes/2 {
		return "", ErrClaims
	}
	claims, err := ValidateClaims(kind, raw)
	if err != nil {
		return "", err
	}
	payload, err := CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	var token string
	err = k.transact(ctx, func(state *keyState, now int64) error {
		if !k.matches(kind, claims) || claims.IssuedAt > now || claims.ExpiresAt <= now {
			return ErrClaims
		}
		header, _ := json.Marshal(struct {
			Algorithm string `json:"alg"`
			ID        string `json:"kid"`
			Type      string `json:"typ"`
		}{"EdDSA", state.Active.ID, "JWT"})
		signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(state.Active.Seed), []byte(signingInput))
		token = signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}
func (k *KeySet) matches(kind Kind, c Claims) bool {
	aud := k.cfg.Audience
	if kind == Session {
		aud = "aithema"
	}
	return c.Issuer == k.cfg.Issuer && c.Audience == aud
}
func (k *KeySet) VerifySession(ctx context.Context, token string) (Claims, error) {
	return k.verify(ctx, Session, token)
}
func (k *KeySet) VerifyDelegated(ctx context.Context, token string) (Claims, error) {
	return k.verify(ctx, Delegated, token)
}

func (k *KeySet) verify(ctx context.Context, kind Kind, token string) (Claims, error) {
	if len(token) > maxTokenBytes {
		return Claims{}, ErrToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrToken
	}
	decode := func(part string) ([]byte, error) {
		// Go's base64 decoder ignores CR/LF; compact JWTs never permit them.
		if strings.ContainsAny(part, "\r\n=") {
			return nil, ErrToken
		}
		return base64.RawURLEncoding.Strict().DecodeString(part)
	}
	header, err := decode(parts[0])
	if err != nil {
		return Claims{}, ErrToken
	}
	h, err := decodeObject(header)
	if err != nil {
		return Claims{}, ErrToken
	}
	for field := range h {
		if field != "alg" && field != "kid" && field != "typ" {
			return Claims{}, ErrToken
		}
	}
	if h["alg"] != "EdDSA" {
		return Claims{}, ErrToken
	}
	id, ok := h["kid"].(string)
	if !ok || len(id) == 0 || len(id) > 128 {
		return Claims{}, ErrToken
	}
	if typ, exists := h["typ"]; exists && typ != "JWT" {
		return Claims{}, ErrToken
	}
	payload, err := decode(parts[1])
	if err != nil {
		return Claims{}, ErrToken
	}
	signature, err := decode(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Claims{}, ErrToken
	}
	var claims Claims
	err = k.transact(ctx, func(state *keyState, now int64) error {
		var public ed25519.PublicKey
		for _, key := range append([]signingKey{state.Active, state.Next}, state.Retired...) {
			if key.ID == id {
				public = key.Public
				break
			}
		}
		if len(public) == 0 || !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), signature) {
			return ErrToken
		}
		var err error
		claims, err = ValidateClaims(kind, payload)
		if err != nil || !k.matches(kind, claims) {
			return ErrToken
		}
		skew := int64(k.cfg.Skew.Seconds())
		if claims.IssuedAt > now+skew || claims.ExpiresAt <= now-skew {
			return ErrToken
		}
		return nil
	})
	if err != nil {
		return Claims{}, err
	}
	return claims, nil
}
