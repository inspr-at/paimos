// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/client"
)

// vendorSessionRef is the harness-native session id for one adapter family.
// Codex uses CODEX_SESSION_ID when that variable is present, even if it is too
// short to record, and falls back to CODEX_THREAD_ID only when it is unset.
func vendorSessionRef(harness string) string {
	switch harness {
	case "claude":
		return normalizeVendorRef(os.Getenv("CLAUDE_CODE_SESSION_ID"))
	case "codex":
		if strings.TrimSpace(os.Getenv("CODEX_SESSION_ID")) != "" {
			return normalizeVendorRef(os.Getenv("CODEX_SESSION_ID"))
		}
		return normalizeVendorRef(os.Getenv("CODEX_THREAD_ID"))
	default:
		return ""
	}
}

// ambientVendorSessionRef is the vendor id for a tell running inside a harness
// that has no --harness flag. Claude wins when both families are set.
func ambientVendorSessionRef() string {
	if ref := vendorSessionRef("claude"); ref != "" {
		return ref
	}
	return vendorSessionRef("codex")
}

func normalizeVendorRef(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 16 || len(raw) > 4096 || strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	return raw
}

func attachVendorSessionRef(body map[string]any, harness, ref, lease string) {
	vendor := vendorSessionRef(harness)
	if vendor == "" || vendor == ref || vendor == lease {
		return
	}
	body["vendor_session_ref"] = vendor
}

// lookupVendorSession posts the vendor reference. A 404 is an empty result so
// the caller can no-op. The reference is not included in the returned error.
func (rt *runtime) lookupVendorSession(ctx context.Context, ref string) (string, error) {
	c, err := rt.api()
	if err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	err = c.DoWithHeaders(ctx, http.MethodPost, "/api/inbox/session-binding", map[string]string{"harness_session_ref": ref}, &out, nil)
	if err != nil {
		var status *client.StatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	if !validUUID(out.SessionID) {
		return "", errors.New("invalid session binding")
	}
	return strings.ToLower(out.SessionID), nil
}
