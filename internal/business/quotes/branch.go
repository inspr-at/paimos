// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/plugins/fence"
)

type branchWrite struct {
	ExpectedQuoteRevision int64  `json:"expected_quote_revision"`
	ExpectedVersion       int    `json:"expected_version"`
	ExpectedContentSHA256 string `json:"expected_content_sha256"`
}

// branchDraft makes a new editable aggregate from a sealed document version.
// The source version and its issue/decision evidence remain unchanged.
func (m *Module) branchDraft(w http.ResponseWriter, r *http.Request) {
	p, e := caller(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	if !m.allow(r, p) {
		respond(w, 0, nil, denied())
		return
	}
	id, e := pathID(r)
	if e != nil {
		respond(w, 0, nil, e)
		return
	}
	var in branchWrite
	if e = decode(r, &in); e != nil {
		respond(w, 0, nil, e)
		return
	}
	if in.ExpectedQuoteRevision < 1 || in.ExpectedVersion < 1 || !shaRe.MatchString(in.ExpectedContentSHA256) {
		respond(w, 0, nil, bad("invalid branch precondition"))
		return
	}
	var out draftRow
	e = m.tx(r.Context(), p, fence.PermStepsApply, true, func(tx pgx.Tx) error {
		var err error
		out, err = branchQuoteDraft(r.Context(), tx, p, id, in.ExpectedQuoteRevision, in.ExpectedVersion, in.ExpectedContentSHA256)
		return err
	})
	if e == nil {
		w.Header().Set("ETag", fmt.Sprintf(`"qd-%d"`, out.DraftRevision))
	}
	respond(w, 200, out, e)
}
