// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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

// Use the inbox hook's source precedence, but only attach an active session in
// the target project that belongs to callerID. A configured source never falls
// through to another generation. A corrupt source is ignored so an ordinary
// tell still sends.
func (rt *runtime) ambientSenderSession(ctx context.Context, projectID, callerID string) (string, error) {
	id, err := inboxHookSession()
	if errors.Is(err, errSessionFileUnavailable) || errors.Is(err, errInvalidSessionFile) || errors.Is(err, errInvalidAeonSessionID) {
		fmt.Fprintln(rt.stderr, "note: ambient sender session source is unusable; sending without it")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if id == "" && os.Getenv("AEON_SESSION_ID") == "" && os.Getenv("AEON_SESSION_FILE") == "" && os.Getenv("AEON_SESSION_STATE_DIR") == "" {
		if ref := ambientVendorSessionRef(); ref != "" {
			id, err = rt.lookupVendorSession(ctx, ref)
		}
	}
	if err != nil || id == "" {
		return "", err
	}
	c, err := rt.api()
	if err != nil {
		return "", err
	}
	var session struct {
		ProjectID        string     `json:"project_id"`
		AgentPrincipalID string     `json:"agent_principal_id"`
		StoppedAt        *time.Time `json:"stopped_at"`
		ArchivedAt       *time.Time `json:"archived_at"`
	}
	err = c.DoWithHeaders(ctx, http.MethodGet, "/api/projects/"+url.PathEscape(projectID)+"/harness-sessions/"+url.PathEscape(id)+"/lookup", nil, &session, nil)
	if err != nil {
		var status *client.StatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			fmt.Fprintln(rt.stderr, "note: ambient sender session unavailable in the target project; sending without it")
			return "", nil
		}
		if errors.As(err, &status) && status.Status == http.StatusForbidden {
			fmt.Fprintln(rt.stderr, "note: ambient sender session is not readable; sending without it")
			return "", nil
		}
		return "", err
	}
	if !strings.EqualFold(session.ProjectID, projectID) {
		fmt.Fprintln(rt.stderr, "note: ambient sender session belongs to another project; sending without it")
		return "", nil
	}
	if !strings.EqualFold(session.AgentPrincipalID, callerID) {
		fmt.Fprintln(rt.stderr, "note: ambient sender session belongs to another agent; sending without it")
		return "", nil
	}
	if session.StoppedAt != nil || session.ArchivedAt != nil {
		fmt.Fprintln(rt.stderr, "note: ambient sender session has ended; sending without it")
		return "", nil
	}
	return id, nil
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
