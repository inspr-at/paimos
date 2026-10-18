// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

// projectAliasPattern preserves the CLI's strings.EqualFold semantics, including
// simple Unicode folds, without depending on the database's locale. Every rune
// is escaped; caller input cannot become a regex operator.
func projectAliasPattern(ref string) string {
	var pattern strings.Builder
	pattern.WriteByte('^')
	for _, r := range ref {
		if unicode.SimpleFold(r) == r {
			pattern.WriteString(regexp.QuoteMeta(string(r)))
			continue
		}
		pattern.WriteString("(?:")
		pattern.WriteString(regexp.QuoteMeta(string(r)))
		for fold := unicode.SimpleFold(r); fold != r; fold = unicode.SimpleFold(fold) {
			pattern.WriteByte('|')
			pattern.WriteString(regexp.QuoteMeta(string(fold)))
		}
		pattern.WriteByte(')')
	}
	pattern.WriteByte('$')
	return pattern.String()
}

// This read deliberately bypasses rich node/project lists and their descendant
// aggregates. RLS applies current visibility before LIMIT, so hidden matches
// neither resolve nor cause ambiguity. An identity confers no write authority.
func (m *Module) handleLookupProject(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	// A UTF-8 byte may use three URL bytes; cap decoding before parsing values.
	if len(r.URL.RawQuery) > len("ref=")+3*1024 {
		writeErr(w, badRequest("project lookup query exceeds its byte limit"))
		return
	}
	refs := r.URL.Query()["ref"]
	if len(refs) != 1 || len(refs[0]) > 1024 || !utf8.ValidString(refs[0]) || strings.ContainsRune(refs[0], 0) {
		writeErr(w, badRequest("one project ref of at most 1024 UTF-8 bytes is required"))
		return
	}
	ref := strings.TrimSpace(refs[0])
	if ref == "" {
		writeErr(w, badRequest("project ref is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items := make([]nodePreview, 0, 2)
	err := db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT n.id::text,n.key,n.title,n.state
		FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
		AND k.slug='project' AND n.deleted_at IS NULL
		AND (n.key=$1
		 OR (jsonb_typeof(n.fields->'project_key')='string' AND n.fields->>'project_key'=$1)
		 OR (strpos(n.key,'-')>0 AND split_part(n.key,'-',1)=$1)
		 OR (jsonb_typeof(n.fields->'classic'->'key')='string' AND n.fields->'classic'->>'key' ~ $2))
		LIMIT 2`, ref, projectAliasPattern(ref))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item nodePreview
			if err := rows.Scan(&item.ID, &item.Key, &item.Title, &item.State); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			writeError(w, http.StatusRequestTimeout, "project lookup canceled")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusServiceUnavailable, "project lookup timed out")
			return
		}
		writeErr(w, dbErr("lookup project", err))
		return
	}
	switch len(items) {
	case 0:
		writeErr(w, notFound(fmt.Sprintf("project key %q not found", ref)))
	case 1:
		writeJSON(w, http.StatusOK, items[0])
	default:
		writeErr(w, conflict(fmt.Sprintf("project key %q is ambiguous", ref)))
	}
}
