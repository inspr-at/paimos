// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

const MaxCacheBytes = 512 * 1024

// Cache is content-addressed as an envelope, binding ALL selectors, immutable
// version references and rendered bytes. It contains no credentials or details.
// The digest detects corruption; the independent floor pin is the offline trust
// anchor. Like all unsigned local caches it is not a defense against an attacker
// already able to rewrite the cache AND the operator's trusted floor pin.
type Cache struct {
	Schema   string    `json:"schema"`
	Instance string    `json:"instance"`
	Bundle   Merged    `json:"bundle"`
	StoredAt time.Time `json:"stored_at"`
	SHA256   string    `json:"sha256"`
	// Stale is set only when this verified cache was served because Aeon was
	// unreachable. A fresh fetch omits it. The bundle bytes stay as stored.
	Stale bool `json:"stale,omitempty"`
}

func (c Cache) hash() string { c.SHA256 = ""; return jsonDigest(c) }
func ValidateMerged(m Merged, c Context, now time.Time) error {
	if err := ValidateContext(c); err != nil {
		return err
	}
	if m.Context != c || !utf8.ValidString(m.Body) || m.ByteSize != len(m.Body) || m.ByteSize > MaxBytes || m.ByteSize == 0 || m.SHA256 != digest([]byte(m.Body)) || m.Floor == "" || !containsFloor(m.Body, m.Floor) {
		return errors.New("rules response context, digest, byte budget or floor does not match")
	}
	if m.ValidUntil != nil && !now.Before(*m.ValidUntil) {
		return errors.New("cached rules crossed an expiry boundary; fetch again or keep only the locked floor")
	}
	if len(m.Versions) == 0 || len(m.Rules) > MaxRules*100 {
		return errors.New("rules response lacks version evidence or exceeds bounds")
	}
	if !releasehistory.ValidVersion(m.Version) {
		return errors.New("invalid merged version")
	}
	for _, v := range m.Versions {
		if !releasehistory.ValidVersion(v.Version) || len(v.SHA256) != 64 {
			return errors.New("invalid snapshot version evidence")
		}
	}
	for _, r := range m.Rules {
		if r.Details != "" {
			return errors.New("always-on rules must not contain details")
		}
	}
	return nil
}
func EncodeCache(instance string, m Merged, now time.Time) ([]byte, error) {
	if err := ValidateMerged(m, m.Context, now); err != nil {
		return nil, err
	}
	c := Cache{Schema: "aeon.rules.cache.v1", Instance: instance, Bundle: m, StoredAt: now.UTC()}
	c.SHA256 = c.hash()
	raw, err := json.Marshal(c)
	if len(raw) > MaxCacheBytes {
		return nil, errors.New("rules cache exceeds byte bound")
	}
	return raw, err
}
func DecodeCache(raw []byte, instance string, ctx Context, now time.Time) (Merged, error) {
	if len(raw) > MaxCacheBytes {
		return Merged{}, errors.New("rules cache exceeds byte bound")
	}
	var c Cache
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Merged{}, errors.New("invalid rules cache")
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return Merged{}, errors.New("trailing cache data")
	}
	if c.Schema != "aeon.rules.cache.v1" || c.Instance != instance || c.SHA256 != c.hash() || c.StoredAt.After(now.Add(time.Minute)) {
		return Merged{}, errors.New("rules cache integrity or instance mismatch")
	}
	if err := ValidateMerged(c.Bundle, ctx, now); err != nil {
		return Merged{}, err
	}
	return c.Bundle, nil
}

// MarkStale records that a verified cache was used while Aeon was unreachable.
// The bundle, its digest and the independent floor pin stay as stored; only the
// envelope gains the mark and a new envelope digest. An already-marked cache is
// returned unchanged. A cache that fails integrity is refused and must not be
// rewritten into something that looks verified.
func MarkStale(raw []byte, instance string, ctx Context, now time.Time) ([]byte, error) {
	if _, err := DecodeCache(raw, instance, ctx, now); err != nil {
		return nil, err
	}
	var c Cache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errors.New("invalid rules cache")
	}
	if c.Stale {
		return raw, nil
	}
	c.Stale = true
	c.SHA256 = c.hash()
	out, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if _, err = DecodeCache(out, instance, ctx, now); err != nil {
		return nil, err
	}
	return out, nil
}

// Offline never substitutes a different context. Failure retains only the
// independently verified floor, with an explicit gap; it never invents a version.
func Offline(raw []byte, instance string, c Context, floor string, now time.Time) (Merged, error) {
	m, err := DecodeCache(raw, instance, c, now)
	if err == nil && !containsFloor(m.Body, floor) {
		err = errors.New("cache omits the pinned locked floor")
	}
	if err == nil {
		return m, nil
	}
	body := "# Aeon locked floor (offline; full rules unavailable)\n\n" + floor
	if floor == "" || !utf8.ValidString(floor) || len(body) > MaxBytes {
		return Merged{}, errors.New("valid bounded independent safety floor required")
	}
	return Merged{Context: c, Versions: []VersionRef{}, Version: "floor-only", Body: body, ByteSize: len(body), SHA256: digest([]byte(body)), Floor: floor, Rules: []Rule{}}, err
}
func containsFloor(body, floor string) bool {
	if strings.TrimSpace(floor) == "" {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(floor), "\n") {
		if strings.TrimSpace(line) != "" && !strings.Contains("\n"+body, "\n"+line+"\n") {
			return false
		}
	}
	return true
}

// VerifyFloor validates a floor against a separately provided trusted digest.
func VerifyFloor(raw []byte, pin string) (string, error) {
	if len(raw) == 0 || len(raw) > MaxBytes-256 || !utf8.Valid(raw) || digest(raw) != pin || strings.ContainsRune(string(raw), 0) {
		return "", errors.New("floor digest or byte bound mismatch")
	}
	return string(raw), nil
}
