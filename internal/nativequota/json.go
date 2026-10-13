// SPDX-License-Identifier: AGPL-3.0-only

package nativequota

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

var regexpScope = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Bound bytes, depth, token count and object keys before typed decoding. Reject
// duplicate keys so a conflicting scope/percentage cannot be last-value-wins.
func boundJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > nativecontract.NativeFrameBeforeDecodeBytes {
		return ErrBounds
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	tokens := 0
	var scan func(int) error
	scan = func(depth int) error {
		if depth > nativecontract.NativeFrameDecodeDepth || tokens >= 8192 {
			return ErrBounds
		}
		tokens++
		t, err := d.Token()
		if err != nil {
			return ErrProtocol
		}
		switch t {
		case json.Delim('{'):
			keys := make(map[string]bool)
			for d.More() {
				if len(keys) >= 256 {
					return ErrBounds
				}
				key, err := d.Token()
				name, ok := key.(string)
				folded := strings.ToLower(name)
				if err != nil || !ok || keys[folded] {
					return ErrProtocol
				}
				keys[folded] = true
				// encoding/json's case-insensitive struct field matching must
				// not enlarge a pinned vendor codec's accepted field names.
				if canonical, known := quotaFields[folded]; known && canonical != name {
					return ErrProtocol
				}
				if err := scan(depth + 1); err != nil {
					return err
				}
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return ErrProtocol
			}
		case json.Delim('['):
			for d.More() {
				if err := scan(depth + 1); err != nil {
					return err
				}
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return ErrProtocol
			}
		}
		return nil
	}
	if err := scan(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}

var quotaFields = map[string]string{
	"type": "type", "session_id": "session_id", "rate_limit_info": "rate_limit_info",
	"status": "status", "ratelimittype": "rateLimitType", "utilization": "utilization", "resetsat": "resetsAt",
	"unifiedwindows": "unifiedWindows", "method": "method", "params": "params", "threadid": "threadId",
	"ratelimits": "rateLimits", "ratelimitsbylimitid": "rateLimitsByLimitId", "ordinaryusageallowed": "ordinaryUsageAllowed",
	"limitid": "limitId", "primary": "primary", "secondary": "secondary", "usedpercent": "usedPercent", "windowdurationmins": "windowDurationMins",
}
