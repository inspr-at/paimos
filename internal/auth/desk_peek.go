// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"net/http"
	"time"
)

// Clear the retired AEON-1495 cookie on session changes. No route accepts it
// and no code issues a credential into it.
func (m *Module) clearDeskPeek(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "aeon_desk_peek", Value: "", Path: "/api/decision-desk/owner-summary", MaxAge: -1, Expires: time.Unix(0, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode})
}
func (m *Module) replaceSessionCookie(w http.ResponseWriter, token string) {
	m.setSessionCookie(w, token)
	m.clearDeskPeek(w)
}
