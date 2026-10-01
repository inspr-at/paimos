// SPDX-License-Identifier: AGPL-3.0-only

// Package tokens provides Aithema JWT and JWKS primitives without a journal
// dependency. Route authorization, generation and epoch freshness belong to
// the caller; a valid signature alone grants no route or project access.
package tokens

import (
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxLifetimeSeconds int64 = 900
	MaxSkewSeconds     int64 = 60
	MaxSafeInteger     int64 = 9007199254740991
)

type Kind string

const (
	Session   Kind = "session"
	Delegated Kind = "delegated"
)

var ErrClaims = errors.New("invalid token claims")

// Actor is the person represented by a delegated plugin principal.
type Actor struct {
	Subject string `json:"sub"`
}

// Claims is the closed union of token-claims.schema.json's two claim sets.
// Use ValidateClaims for untrusted JSON; ordinary Unmarshal is not validation.
// nbf is intentionally absent: the binding schema forbids additional claims,
// so a token carrying nbf is refused rather than widening the contract.
type Claims struct {
	Issuer       string   `json:"iss"`
	Audience     string   `json:"aud"`
	Subject      string   `json:"sub"`
	Actor        *Actor   `json:"act,omitempty"`
	TenantID     string   `json:"tid"`
	ProjectID    string   `json:"pid"`
	SessionID    string   `json:"sid"`
	Generation   int64    `json:"gen,omitempty"`
	AuthEpoch    int64    `json:"auth_epoch"`
	Scope        []string `json:"scope,omitempty"`
	ActorKind    string   `json:"actor_kind,omitempty"`
	HostMode     string   `json:"host_mode,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	IssuedAt     int64    `json:"iat"`
	ExpiresAt    int64    `json:"exp"`
	ID           string   `json:"jti,omitempty"`
}

func validRef(s string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(s)
}
func validUUID(s string) bool {
	return regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(s)
}
func validIssuer(s string) bool {
	return strings.HasPrefix(s, "https://") && len(s) > len("https://") && !strings.ContainsFunc(s, unicode.IsSpace)
}

func exactInteger(v any, minimum int64) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	// Bound hostile exponents before allocating big integers. All contract
	// integers fit in 16 digits; the JWT input size is bounded separately.
	if len(n.String()) > 64 {
		return 0, false
	}
	if i := strings.IndexAny(n.String(), "eE"); i >= 0 {
		e, ok := new(big.Int).SetString(n.String()[i+1:], 10)
		if !ok || !e.IsInt64() || e.Int64() < -64 || e.Int64() > 64 {
			return 0, false
		}
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	value := r.Num().Int64()
	return value, value >= minimum && value <= MaxSafeInteger
}

func list(v any, allowed map[string]bool) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	seen := make(map[string]bool, len(arr))
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok || !allowed[s] || seen[s] {
			return nil, false
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, true
}

// ValidateClaims applies the binding schema and capabilities.json invariants
// before any lossy numeric conversion. Issuer/audience/time-at-use are checked
// separately by KeySet.VerifySession or VerifyDelegated.
func ValidateClaims(kind Kind, raw []byte) (Claims, error) {
	var c Claims
	obj, err := decodeObject(raw)
	if err != nil {
		return c, err
	}
	required := []string{"iss", "aud", "sub", "tid", "pid", "sid", "auth_epoch", "iat", "exp"}
	switch kind {
	case Session:
		required = append(required, "scope", "actor_kind", "host_mode")
	case Delegated:
		required = append(required, "act", "gen", "capabilities")
	default:
		return c, ErrClaims
	}
	allowed := map[string]bool{"jti": true}
	for _, field := range required {
		allowed[field] = true
		if _, ok := obj[field]; !ok {
			return c, ErrClaims
		}
	}
	for field := range obj {
		if !allowed[field] {
			return c, ErrClaims
		}
	}
	text := func(field string) string { s, _ := obj[field].(string); return s }
	c.Issuer, c.Audience, c.Subject = text("iss"), text("aud"), text("sub")
	c.TenantID, c.ProjectID, c.SessionID = text("tid"), text("pid"), text("sid")
	if !validIssuer(c.Issuer) || !validRef(c.Subject) || !validRef(c.TenantID) || !validRef(c.ProjectID) || !validUUID(c.SessionID) {
		return Claims{}, ErrClaims
	}
	var ok bool
	if c.AuthEpoch, ok = exactInteger(obj["auth_epoch"], 1); !ok {
		return Claims{}, ErrClaims
	}
	if c.IssuedAt, ok = exactInteger(obj["iat"], 0); !ok {
		return Claims{}, ErrClaims
	}
	if c.ExpiresAt, ok = exactInteger(obj["exp"], 0); !ok || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > MaxLifetimeSeconds {
		return Claims{}, ErrClaims
	}
	if _, has := obj["jti"]; has {
		c.ID = text("jti")
		if !validUUID(c.ID) {
			return Claims{}, ErrClaims
		}
	}
	switch kind {
	case Session:
		c.ActorKind, c.HostMode = text("actor_kind"), text("host_mode")
		if c.Audience != "aithema" || (c.ActorKind != "person" && c.ActorKind != "anonymous") || (c.HostMode != "review" && c.HostMode != "working_spec_only") {
			return Claims{}, ErrClaims
		}
		c.Scope, ok = list(obj["scope"], map[string]bool{"session.converse": true, "session.upload": true, "session.confirm": true, "session.export": true})
	case Delegated:
		if n := utf8.RuneCountInString(c.Audience); n < 1 || n > 256 {
			return Claims{}, ErrClaims
		}
		act, valid := obj["act"].(map[string]any)
		if !valid || len(act) != 1 {
			return Claims{}, ErrClaims
		}
		sub, valid := act["sub"].(string)
		if !valid || !validRef(sub) {
			return Claims{}, ErrClaims
		}
		c.Actor = &Actor{Subject: sub}
		if c.Generation, ok = exactInteger(obj["gen"], 1); !ok {
			return Claims{}, ErrClaims
		}
		c.Capabilities, ok = list(obj["capabilities"], map[string]bool{"intake.read": true, "intake.write": true, "aithema.journal.read": true, "aithema.journal.write": true, "aithema.authority.read": true, "aithema.ledger": true})
	}
	if !ok {
		return Claims{}, ErrClaims
	}
	return c, nil
}
