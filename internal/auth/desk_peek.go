// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"slices"
	"time"
)

// AEON-1495: the owner's Waiting on you across instances. A sibling instance's
// page reads this instance's owner summary in the owner's browser, with the
// owner's own session here. The session cookie stays SameSite=Lax, so a
// cross-site read cannot carry it. The workspace owner (only) therefore also
// holds a copy scoped to the one read-only summary path, SameSite=None. It is
// accepted for that exact GET route only, only from a configured sibling
// origin and never next to an Authorization header; any other route, method
// or origin ignores it. No new secret exists: it is the same session, revoked
// with it.
const (
	deskPeekCookieName = "aeon_desk_peek"
	// DeskPeekPattern is the only route that accepts the peek cookie.
	DeskPeekPattern = "GET /api/decision-desk/owner-summary"
	deskPeekPath    = "/api/decision-desk/owner-summary"
)

// SetDeskPeekOrigins names the sibling origins (deployment configuration).
// Without any, the peek cookie is never issued nor accepted.
func (m *Module) SetDeskPeekOrigins(origins []string) {
	m.deskPeekOrigins = slices.Clone(origins)
}

// deskPeekRequest reports whether r may authenticate with the peek cookie.
func (m *Module) deskPeekRequest(r *http.Request) bool {
	if r.Method != http.MethodGet || r.Pattern != DeskPeekPattern || r.Header.Get("Authorization") != "" {
		return false
	}
	origins := r.Header.Values("Origin")
	return len(origins) == 1 && slices.Contains(m.deskPeekOrigins, origins[0])
}

// deskPeekOnly reports a sibling read that carried no session cookie: a stale
// peek copy never clears the session cookie of this instance.
func (m *Module) deskPeekOnly(r *http.Request) bool {
	_, err := r.Cookie(sessionCookieName)
	return err != nil && m.deskPeekRequest(r)
}

// IssueDeskPeek copies the request's browser session into the peek cookie when
// owner is true and siblings are configured; otherwise it clears one. Callers
// pass owner only after a live workspace-owner check in the same request.
// The cookie's path is only the summary route, so a browser never sends it to
// discovery. Clear it anyway: Set-Cookie matches on that path, not on the
// request path, and a non-owner must not keep an older identity's copy.
func (m *Module) IssueDeskPeek(w http.ResponseWriter, r *http.Request, owner bool) {
	session, err := r.Cookie(sessionCookieName)
	if owner && err == nil && len(m.deskPeekOrigins) > 0 {
		http.SetCookie(w, &http.Cookie{
			Name: deskPeekCookieName, Value: session.Value, Path: deskPeekPath,
			MaxAge: int(sessionTTL.Seconds()), Expires: time.Now().Add(sessionTTL),
			// SameSite=None requires Secure; browsers treat localhost as secure.
			HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
		})
		return
	}
	m.clearDeskPeek(w)
}

// replaceSessionCookie installs a new browser session and drops the summary
// copy. Login does not receive that copy (its path is the summary route), so
// the previous identity's copy would otherwise keep working after the swap.
func (m *Module) replaceSessionCookie(w http.ResponseWriter, token string) {
	m.setSessionCookie(w, token)
	m.clearDeskPeek(w)
}

func (m *Module) clearDeskPeek(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: deskPeekCookieName, Value: "", Path: deskPeekPath, MaxAge: -1, Expires: time.Unix(0, 0),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
}
