// SPDX-License-Identifier: AGPL-3.0-only

// Package workerlease carries the private generation proof alongside the agent key.
package workerlease

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

const Header = "Proxy-Authorization"
const LegacyHeader = "X-Aeon-Worker-Lease"

// Value formats the lease without occupying Authorization's agent-key slot.
func Value(lease string) string { return "Bearer " + lease }

// Read accepts the legacy header for release 137. Ambiguous or malformed proof
// fails closed, including when a valid legacy value accompanies a bad new one.
// The deprecation warning contains only fixed text, never request data.
func Read(r *http.Request) string {
	legacy := r.Header.Values(LegacyHeader)
	if len(legacy) != 0 {
		slog.WarnContext(r.Context(), "X-Aeon-Worker-Lease is deprecated; use Proxy-Authorization: Bearer (release 137 transition)")
	}
	current := r.Header.Values(Header)
	if len(legacy) > 1 || len(current) > 1 || len(legacy) == 1 && len(legacy[0]) > 4096 {
		return ""
	}
	lease := ""
	if len(current) == 1 {
		if len(current[0]) > 4103 {
			return ""
		}
		scheme, value, ok := strings.Cut(current[0], " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return ""
		}
		lease = value
		if len(legacy) == 1 && subtle.ConstantTimeCompare([]byte(lease), []byte(legacy[0])) != 1 {
			return ""
		}
	} else if len(legacy) == 1 {
		lease = legacy[0]
	}
	if len(lease) < 32 || len(lease) > 4096 || strings.ContainsAny(lease, " \t\r\n,") {
		return ""
	}
	return lease
}
