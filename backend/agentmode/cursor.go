// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/secretvault"
)

const (
	cursorPlaintextVersion = byte(1)
	cursorPlaintextLength  = 129
	cursorCiphertextLength = 158
	cursorDomain           = "agent-mode:delivery-cursor:v1"
	defaultCursorTTL       = 15 * time.Minute
)

type CursorBinding struct {
	UserID           int64
	PermissionsEpoch int64
	PermissionDigest [32]byte
	RouteDigest      [32]byte
	FilterDigest     [32]byte
}

type CursorClaims struct {
	CursorBinding
	ExpiresAt time.Time
	HighWater int64
}

type CursorCodec struct {
	clock   delivery.Clock
	ttl     time.Duration
	encrypt func(string, []byte) ([]byte, error)
	decrypt func(string, []byte) ([]byte, error)
}

func NewCursorCodec(clock delivery.Clock) *CursorCodec {
	if clock == nil {
		clock = delivery.ClockFunc(time.Now)
	}
	return &CursorCodec{clock: clock, ttl: defaultCursorTTL, encrypt: secretvault.Encrypt, decrypt: secretvault.Decrypt}
}

// NewCursorCodecWithCrypt is the deterministic test seam. Production code
// uses NewCursorCodec so the fixed-width plaintext is always sealed by the
// secretvault domain key.
func NewCursorCodecWithCrypt(clock delivery.Clock, ttl time.Duration,
	encrypt, decrypt func(string, []byte) ([]byte, error)) *CursorCodec {
	codec := NewCursorCodec(clock)
	if ttl > 0 {
		codec.ttl = ttl
	}
	if encrypt != nil {
		codec.encrypt = encrypt
	}
	if decrypt != nil {
		codec.decrypt = decrypt
	}
	return codec
}

func (c *CursorCodec) Encode(binding CursorBinding, highWater int64) (string, error) {
	if c == nil {
		return "", fmt.Errorf("%w: invalid cursor claims", ErrCursor)
	}
	return c.EncodeAt(binding, highWater, c.clock.Now().UTC())
}

// EncodeAt seals a cursor whose expiry is derived from the caller's captured
// snapshot instant. Snapshot readers use this path so every time-dependent
// field in one response has exactly the same clock basis.
func (c *CursorCodec) EncodeAt(binding CursorBinding, highWater int64, issuedAt time.Time) (string, error) {
	if c == nil || binding.UserID <= 0 || binding.PermissionsEpoch < 0 || highWater < 0 || issuedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid cursor claims", ErrCursor)
	}
	plain := make([]byte, cursorPlaintextLength)
	plain[0] = cursorPlaintextVersion
	binary.BigEndian.PutUint64(plain[1:9], uint64(binding.UserID))
	binary.BigEndian.PutUint64(plain[9:17], uint64(binding.PermissionsEpoch))
	expires := issuedAt.UTC().Add(c.ttl).Unix()
	binary.BigEndian.PutUint64(plain[17:25], uint64(expires))
	binary.BigEndian.PutUint64(plain[25:33], uint64(highWater))
	copy(plain[33:65], binding.PermissionDigest[:])
	copy(plain[65:97], binding.RouteDigest[:])
	copy(plain[97:129], binding.FilterDigest[:])
	sealed, err := c.encrypt(cursorDomain, plain)
	if err != nil {
		return "", err
	}
	if len(sealed) != cursorCiphertextLength || sealed[0] != 1 {
		return "", fmt.Errorf("%w: cursor sealer returned a non-v1 width", ErrCursor)
	}
	token := base64.RawURLEncoding.EncodeToString(sealed)
	if len(token) != CursorEncodedLength {
		return "", fmt.Errorf("%w: cursor encoding width", ErrCursor)
	}
	return token, nil
}

func (c *CursorCodec) Decode(token string, expected CursorBinding) (CursorClaims, error) {
	claims, _, err := c.DecodeScopes(token, expected.UserID, []CursorBinding{expected})
	if err != nil || expected.PermissionsEpoch < 0 {
		return CursorClaims{}, ErrCursor
	}
	if claims.PermissionsEpoch != expected.PermissionsEpoch ||
		subtle.ConstantTimeCompare(claims.PermissionDigest[:], expected.PermissionDigest[:]) != 1 {
		return CursorClaims{}, ErrCursor
	}
	return claims, nil
}

// DecodeScopes decrypts a cursor exactly once, compares every supplied
// route/filter candidate in constant time, and returns the sole matching
// scope. Authorization/permission claims are checked against a freshly pinned
// StreamState after the caller chooses that sealed scope.
func (c *CursorCodec) DecodeScopes(token string, userID int64, expected []CursorBinding) (CursorClaims, int, error) {
	if c == nil || len(token) != CursorEncodedLength || userID <= 0 || len(expected) == 0 || len(expected) > 2 {
		return CursorClaims{}, -1, ErrCursor
	}
	encoding := base64.RawURLEncoding.Strict()
	sealed, err := encoding.DecodeString(token)
	canonical := ""
	if err == nil {
		canonical = encoding.EncodeToString(sealed)
	}
	if err != nil || len(sealed) != cursorCiphertextLength || sealed[0] != 1 ||
		subtle.ConstantTimeCompare([]byte(token), []byte(canonical)) != 1 {
		return CursorClaims{}, -1, ErrCursor
	}
	plain, err := c.decrypt(cursorDomain, sealed)
	if err != nil || len(plain) != cursorPlaintextLength || plain[0] != cursorPlaintextVersion {
		return CursorClaims{}, -1, ErrCursor
	}
	claims := CursorClaims{CursorBinding: CursorBinding{
		UserID:           int64(binary.BigEndian.Uint64(plain[1:9])),
		PermissionsEpoch: int64(binary.BigEndian.Uint64(plain[9:17])),
	}, ExpiresAt: time.Unix(int64(binary.BigEndian.Uint64(plain[17:25])), 0).UTC(),
		HighWater: int64(binary.BigEndian.Uint64(plain[25:33]))}
	copy(claims.PermissionDigest[:], plain[33:65])
	copy(claims.RouteDigest[:], plain[65:97])
	copy(claims.FilterDigest[:], plain[97:129])
	matchCount, matchIndex := 0, -1
	for index := range expected {
		match := subtle.ConstantTimeCompare(claims.RouteDigest[:], expected[index].RouteDigest[:]) &
			subtle.ConstantTimeCompare(claims.FilterDigest[:], expected[index].FilterDigest[:])
		matchCount += match
		if match == 1 {
			matchIndex = index
		}
	}
	if claims.UserID != userID || claims.HighWater < 0 || !claims.ExpiresAt.After(c.clock.Now().UTC()) || matchCount != 1 {
		return CursorClaims{}, -1, ErrCursor
	}
	return claims, matchIndex, nil
}
