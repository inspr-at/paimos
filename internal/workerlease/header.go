// SPDX-License-Identifier: AGPL-3.0-only

// Package workerlease carries the private generation proof alongside the agent key.
package workerlease

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// Scheme is the Authorization scheme for one worker call. Authorization is
// forwarded and redacted by ordinary reverse proxies. Hop-by-hop names such
// as Proxy-Authorization are removed before they reach Aeon.
const Scheme = "Aeon-Worker"

const LegacyHeader = "X-Aeon-Worker-Lease"

const (
	minLease = 32
	maxLease = 4096
	maxKey   = 256
	// Scheme, two separators, the agent key and the lease.
	maxAuthorization = len(Scheme) + 1 + maxKey + 1 + maxLease
)

// Value formats Authorization for a worker call: the agent key and the lease
// travel together, so a proxy cannot keep one and drop the other.
func Value(agentKey, lease string) string {
	return Scheme + " " + agentKey + " " + lease
}

// Install sets that Authorization value and removes separate proof headers.
func Install(h http.Header, agentKey, lease string) {
	if h == nil || agentKey == "" || lease == "" {
		return
	}
	h.Set("Authorization", Value(agentKey, lease))
	h.Del(LegacyHeader)
	h.Del("Proxy-Authorization")
}

// Carries reports whether this outgoing request holds worker proof.
func Carries(h http.Header) bool {
	if h == nil {
		return false
	}
	if h.Get(LegacyHeader) != "" || workerScheme(h.Get("Authorization")) {
		return true
	}
	return false
}

// Proofs returns the lease fragments an error body must not echo.
func Proofs(h http.Header) []string {
	if h == nil {
		return nil
	}
	var out []string
	if lease := leaseFragment(h.Get("Authorization")); lease != "" {
		out = append(out, lease)
	}
	if legacy := h.Get(LegacyHeader); legacy != "" {
		out = append(out, legacy)
	}
	return out
}

// AgentToken returns the agent-key token from a Bearer or Aeon-Worker value.
// malformed is true when the worker scheme is present but not exactly one key
// and one lease; callers fail closed and do not try another credential.
func AgentToken(value string) (token string, malformed bool) {
	if workerScheme(value) {
		key, _, ok := splitWorker(value)
		if !ok {
			return "", true
		}
		return key, false
	}
	scheme, rest, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(rest), false
}

// Read accepts the legacy header for release 137. Ambiguous or malformed proof
// fails closed, including when a valid legacy value accompanies a bad new one.
// The deprecation warning contains only fixed text, never request data.
func Read(r *http.Request) string {
	if r == nil {
		return ""
	}
	legacy := r.Header.Values(LegacyHeader)
	if len(legacy) != 0 {
		slog.WarnContext(r.Context(), "X-Aeon-Worker-Lease is deprecated; use Authorization: Aeon-Worker (release 137 transition)")
	}
	if len(legacy) > 1 || len(legacy) == 1 && len(legacy[0]) > maxLease {
		return ""
	}
	auths := r.Header.Values("Authorization")
	worker := ""
	workers := 0
	for _, value := range auths {
		if workerScheme(value) {
			workers++
			worker = value
		}
	}
	if workers > 1 || workers == 1 && len(auths) != 1 {
		return ""
	}
	lease := ""
	if workers == 1 {
		_, got, ok := splitWorker(worker)
		if !ok {
			return ""
		}
		lease = got
		if len(legacy) == 1 && subtle.ConstantTimeCompare([]byte(lease), []byte(legacy[0])) != 1 {
			return ""
		}
	} else if len(legacy) == 1 {
		lease = legacy[0]
	}
	if len(lease) < minLease || len(lease) > maxLease || strings.ContainsAny(lease, " \t\r\n,") {
		return ""
	}
	return lease
}

func workerScheme(value string) bool {
	if len(value) < len(Scheme) || !strings.EqualFold(value[:len(Scheme)], Scheme) {
		return false
	}
	return len(value) == len(Scheme) || value[len(Scheme)] == ' '
}

func splitWorker(value string) (key, lease string, ok bool) {
	if !workerScheme(value) || len(value) > maxAuthorization {
		return "", "", false
	}
	rest := value[len(Scheme):]
	if rest == "" || rest[0] != ' ' {
		return "", "", false
	}
	key, lease, found := strings.Cut(rest[1:], " ")
	if !found || !validKey(key) || !validLease(lease) {
		return "", "", false
	}
	return key, lease, true
}

func validKey(key string) bool {
	return key != "" && len(key) <= maxKey && !strings.ContainsAny(key, " \t\r\n,")
}

func validLease(lease string) bool {
	return len(lease) >= minLease && len(lease) <= maxLease && !strings.ContainsAny(lease, " \t\r\n,")
}

func leaseFragment(value string) string {
	if !workerScheme(value) || len(value) > maxAuthorization {
		return ""
	}
	rest := value[len(Scheme):]
	if rest == "" || rest[0] != ' ' {
		return ""
	}
	_, lease, found := strings.Cut(rest[1:], " ")
	if !found || lease == "" || strings.ContainsAny(lease, " \t\r\n") {
		return ""
	}
	return lease
}
