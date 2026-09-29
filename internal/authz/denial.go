// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/httpapi"
)

// denial reports only the authenticated caller's own missing layer. It never
// includes a target identifier or confirms that a requested resource exists.
type denial struct {
	reason string
	scope  string
}

func (d *denial) Error() string { return ErrForbidden.Error() }
func (d *denial) Unwrap() error { return ErrForbidden }

// WriteForbidden preserves the existing envelope and adds safe diagnostics
// only for decisions made by Require. Unknown/unauthenticated denials stay generic.
func WriteForbidden(w http.ResponseWriter, err error) {
	body := map[string]any{"error": "permission denied", "code": "forbidden", "reason": "This action needs a permission you do not hold"}
	var d *denial
	if errors.As(err, &d) {
		body["reason_code"] = d.reason
		switch d.reason {
		case "missing_key_scope":
			body["scope"] = d.scope
			body["reason"] = "The key needs scope " + d.scope
		case "missing_project_access":
			body["reason"] = "Your access does not cover this project"
		case "missing_role_permission":
			body["reason"] = "Your role does not allow this action"
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusForbidden, body)
}
