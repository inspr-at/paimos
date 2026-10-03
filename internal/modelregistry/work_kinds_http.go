// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type workKind struct {
	modelprefs.Kind
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}
type workKindPage struct {
	Items      []workKind `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}
type workKindWrite struct {
	Label     string `json:"label"`
	Hint      string `json:"hint"`
	ProjectID string `json:"project_id"`
}
type workKindPatch struct {
	Label    *string `json:"label"`
	Hint     *string `json:"hint"`
	Position *int    `json:"position"`
}

func kindSlug(label string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(label) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			hyphen = false
		} else if unicode.IsLetter(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
			hyphen = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "work-" + s
	}
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	return s
}
func kindScan(ctx context.Context, tx pgx.Tx, id string) (workKind, error) {
	var k workKind
	err := tx.QueryRow(ctx, `SELECT id::text,slug,label,hint,project_id::text,system,position,archived_at FROM work_kinds WHERE id=$1`, id).Scan(&k.ID, &k.Slug, &k.Label, &k.Hint, &k.ProjectID, &k.System, &k.Position, &k.ArchivedAt)
	return k, err
}
func (m *Module) listWorkKinds(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	q := r.URL.Query()
	limit := 100
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writePreferenceError(w, prefFail(400, "invalid_limit"))
			return
		}
	}
	cursor := q.Get("cursor")
	if cursor != "" && !uuidRE.MatchString(cursor) {
		writePreferenceError(w, prefFail(400, "invalid_cursor"))
		return
	}
	archived := false
	if q.Get("include_archived") != "" {
		archived, err = strconv.ParseBool(q.Get("include_archived"))
		if err != nil {
			writePreferenceError(w, prefFail(400, "invalid_include_archived"))
			return
		}
	}
	out := workKindPage{Items: []workKind{}}
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := authz.RequireTx(ctx, tx, p, "models.read", authz.Scope{}); err != nil {
			return err
		}
		if err := readableProject(ctx, tx, p, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,slug,label,hint,project_id::text,system,position,archived_at FROM work_kinds WHERE (project_id IS NULL OR project_id=$1::uuid) AND ($2 OR archived_at IS NULL) AND ($3::uuid IS NULL OR id>$3::uuid) ORDER BY id LIMIT $4`, optionalUUID(project), archived, optionalUUID(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k workKind
			if err := rows.Scan(&k.ID, &k.Slug, &k.Label, &k.Hint, &k.ProjectID, &k.System, &k.Position, &k.ArchivedAt); err != nil {
				return err
			}
			out.Items = append(out.Items, k)
		}
		if len(out.Items) > limit {
			next := out.Items[limit-1].ID
			out.NextCursor = &next
			out.Items = out.Items[:limit]
		}
		return rows.Err()
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) writeWorkKind(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	id := r.PathValue("kindId")
	if id != "" && !uuidRE.MatchString(id) {
		writePreferenceError(w, prefFail(400, "invalid_kind_id"))
		return
	}
	var in workKindWrite
	var patch workKindPatch
	var err error
	if id == "" {
		err = decodeJSON(w, r, &in)
		in.Label = strings.TrimSpace(in.Label)
		if err == nil && (in.Label == "" || !boundedText(in.Label, 60) || !boundedText(in.Hint, 120) || (in.ProjectID != "" && !uuidRE.MatchString(in.ProjectID))) {
			err = prefFail(422, "invalid_kind")
		}
	} else if r.Method == http.MethodPatch {
		err = decodeJSON(w, r, &patch)
		if err == nil && (patch.Label == nil && patch.Hint == nil && patch.Position == nil || patch.Label != nil && (strings.TrimSpace(*patch.Label) == "" || !boundedText(*patch.Label, 60)) || patch.Hint != nil && !boundedText(*patch.Hint, 120) || patch.Position != nil && (*patch.Position < -1000000 || *patch.Position > 1000000)) {
			err = prefFail(422, "invalid_kind")
		}
	}
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var out workKind
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		project := in.ProjectID
		var before *workKind
		if id != "" {
			k, err := kindScan(ctx, tx, id)
			if err != nil {
				return err
			}
			before = &k
			if k.ProjectID != nil {
				project = *k.ProjectID
			}
		}
		level := "default"
		if project != "" {
			level = "project"
		}
		if err := authorizePreference(ctx, tx, p, level, project, nil); err != nil {
			return err
		}
		if before != nil && before.System != nil {
			return prefFail(422, "system_kind")
		}
		ev := "work_kind.updated"
		if id == "" {
			// Bound active kinds across the tenant. Slug candidates are checked against
			// both default and project lists under the tenant fence; the DB guard is
			// still authoritative against concurrent non-API writers and restores.
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_kinds WHERE archived_at IS NULL`).Scan(&count); err != nil {
				return err
			}
			if count >= maxPreferenceKinds {
				return prefFail(413, "too_many_kinds")
			}
			base := kindSlug(in.Label)
			slug := ""
			for attempt := 0; attempt < 256; attempt++ {
				candidate := base
				if attempt > 0 {
					suffix := "-" + strconv.Itoa(attempt+1)
					n := 48 - len(suffix)
					if len(candidate) > n {
						candidate = candidate[:n]
					}
					candidate = strings.TrimRight(candidate, "-") + suffix
				}
				var taken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_kinds WHERE slug=$1 AND archived_at IS NULL AND ($2::uuid IS NULL OR project_id IS NULL OR project_id=$2::uuid))`, candidate, optionalUUID(project)).Scan(&taken); err != nil {
					return err
				}
				if !taken {
					slug = candidate
					break
				}
			}
			if slug == "" {
				return prefFail(409, "slug_taken")
			}
			if err := tx.QueryRow(ctx, `INSERT INTO work_kinds(tenant_id,slug,label,hint,project_id,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, p.TenantID, slug, in.Label, in.Hint, optionalUUID(project), p.ID).Scan(&id); err != nil {
				return err
			}
			ev = "work_kind.created"
		} else if r.Method == http.MethodPatch {
			label, hint, position := before.Label, before.Hint, before.Position
			if patch.Label != nil {
				label = strings.TrimSpace(*patch.Label)
			}
			if patch.Hint != nil {
				hint = *patch.Hint
			}
			if patch.Position != nil {
				position = *patch.Position
			}
			if _, err := tx.Exec(ctx, `UPDATE work_kinds SET label=$2,hint=$3,position=$4 WHERE id=$1`, id, label, hint, position); err != nil {
				return err
			}
		} else if r.Method == http.MethodDelete {
			if _, err := tx.Exec(ctx, `UPDATE work_kinds SET archived_at=coalesce(archived_at,now()) WHERE id=$1`, id); err != nil {
				return err
			}
			ev = "work_kind.archived"
		} else {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_kinds WHERE archived_at IS NULL`).Scan(&count); err != nil {
				return err
			}
			if before.ArchivedAt != nil && count >= maxPreferenceKinds {
				return prefFail(413, "too_many_kinds")
			}
			if _, err := tx.Exec(ctx, `UPDATE work_kinds SET archived_at=NULL WHERE id=$1`, id); err != nil {
				return err
			}
		}
		var err error
		out, err = kindScan(ctx, tx, id)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: ev, Before: before, After: out})
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	status := 200
	if r.PathValue("kindId") == "" {
		status = 201
	}
	httpapi.WriteJSON(w, status, out)
}
