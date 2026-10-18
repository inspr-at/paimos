// SPDX-License-Identifier: AGPL-3.0-only
package outcomes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// This is a bounded live evidence view, with no inferred episode membership.
type ticketMeasurement struct {
	TicketID    string   `json:"ticket_node_id"`
	Scope       string   `json:"scope"`
	Completions int      `json:"completions"`
	Reviews     int      `json:"review_rounds"`
	Fixes       int      `json:"fix_rounds"`
	Commits     int      `json:"commits"`
	Added       *int64   `json:"lines_added"`
	Deleted     *int64   `json:"lines_deleted"`
	Files       *int64   `json:"file_changes"`
	Gaps        []string `json:"gaps"`
	Truncated   bool     `json:"truncated"`
}

type diffCommit struct {
	SHA     string `json:"sha"`
	Added   *int64 `json:"lines_added"`
	Deleted *int64 `json:"lines_deleted"`
	Files   *int64 `json:"files_changed"`
}

func sameDiff(a, b diffCommit) bool {
	for _, pair := range [][2]*int64{{a.Added, b.Added}, {a.Deleted, b.Deleted}, {a.Files, b.Files}} {
		if (pair[0] == nil) != (pair[1] == nil) || (pair[0] != nil && *pair[0] != *pair[1]) {
			return false
		}
	}
	return true
}

func (m *Module) measurement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if len(r.URL.RawQuery) > 512 {
		writeErr(w, invalid("measurement query too large"))
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ticket_node_id"))
	if ref == "" || len(ref) > 128 {
		writeErr(w, invalid("ticket_node_id is required and at most 128 bytes"))
		return
	}
	ctx, cancel := context.WithTimeout(authz.BindPool(r.Context(), m.pool), 10*time.Second)
	defer cancel()
	out := ticketMeasurement{Scope: "lifetime_events_and_current_bindings", Gaps: []string{}}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		n, err := resolveTicket(ctx, tx, ref)
		if err != nil {
			return err
		}
		for _, perm := range []string{"outcome.read", "harness.read"} {
			if err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: n.projectID}); err != nil {
				return err
			}
		}
		out.TicketID = n.id
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		// Close all rows before invoking callbacks that may use this transaction.
		rows, err := tx.Query(ctx, `SELECT kind,project_id::text FROM outcome_events WHERE ticket_node_id=$1 ORDER BY recorded_at DESC,id DESC LIMIT 10001`, n.id)
		if err != nil {
			return err
		}
		type event struct{ kind, project string }
		events := []event{}
		for rows.Next() {
			var e event
			if err = rows.Scan(&e.kind, &e.project); err != nil {
				rows.Close()
				return err
			}
			events = append(events, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(events) > 10000 {
			events = events[:10000]
			out.Truncated = true
			out.Gaps = append(out.Gaps, "outcome_limit")
		}
		if !check("outcome.read", "") {
			out.Gaps = append(out.Gaps, "project_scoped_events")
		}
		for _, e := range events {
			if !check("outcome.read", e.project) {
				if len(out.Gaps) == 0 || out.Gaps[len(out.Gaps)-1] != "outcome_project_unreadable" {
					out.Gaps = append(out.Gaps, "outcome_project_unreadable")
				}
				continue
			}
			switch e.kind {
			case "ticket_done":
				out.Completions++
			case "review_verdict":
				out.Reviews++
			case "fix_round":
				out.Fixes++
			}
		}
		// A restricted reader cannot prove that RLS retained every source row.
		// Return explicit missing diff evidence instead of a falsely complete sum.
		var all bool
		if err := tx.QueryRow(ctx, `SELECT aeon_visible_all()`).Scan(&all); err != nil {
			return err
		}
		if !all || !check("harness.read", "") {
			out.Gaps = append(out.Gaps, "diff_visibility_incomplete")
			return nil
		}
		rows, err = tx.Query(ctx, `SELECT commits FROM harness_sessions WHERE ticket_node_id=$1 ORDER BY created_at,id LIMIT 201`, n.id)
		if err != nil {
			return err
		}
		seen := map[string]diffCommit{}
		diffKnown := true
		identityIncomplete := false
		sessions := 0
		for rows.Next() {
			sessions++
			if sessions > 200 {
				out.Truncated = true
				out.Gaps = append(out.Gaps, "session_limit")
				diffKnown = false
				break
			}
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			// Database JSON is independently bounded before allocating a slice.
			if len(raw) > 65536 {
				diffKnown = false
				out.Truncated = true
				continue
			}
			var commits []diffCommit
			if len(raw) > 0 && json.Unmarshal(raw, &commits) != nil {
				diffKnown = false
				continue
			}
			if len(commits) >= 20 {
				out.Truncated = true
				commits = commits[:20]
			}
			for _, c := range commits {
				c.SHA = strings.ToLower(c.SHA)
				// Retained abbreviations cannot establish commit identity: even
				// a matching prefix may name a different full commit. Keep the
				// report accepted, but certify neither its count nor its diff.
				if len(c.SHA) != 40 || strings.Trim(c.SHA, "0123456789abcdef") != "" {
					diffKnown = false
					identityIncomplete = true
					continue
				}
				if old, ok := seen[c.SHA]; ok {
					if !sameDiff(old, c) {
						diffKnown = false
					}
					continue
				}
				if len(seen) >= 10000 {
					out.Truncated = true
					diffKnown = false
					break
				}
				seen[c.SHA] = c
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		out.Commits = len(seen)
		if identityIncomplete {
			out.Gaps = append(out.Gaps, "commit_identity_incomplete")
		}
		if len(seen) == 0 {
			out.Gaps = append(out.Gaps, "no_commit_diff")
			return nil
		}
		var a, d, f int64
		for _, c := range seen {
			if c.Added == nil || c.Deleted == nil || c.Files == nil {
				diffKnown = false
				continue
			}
			valid := true
			for _, v := range []*int64{c.Added, c.Deleted, c.Files} {
				if *v < 0 || *v > 1000000000 {
					valid = false
				}
			}
			if !valid {
				diffKnown = false
				continue
			}
			a += *c.Added
			d += *c.Deleted
			f += *c.Files
		}
		if out.Truncated {
			out.Gaps = append(out.Gaps, "retained_evidence_limit")
			diffKnown = false
		}
		if diffKnown {
			out.Added = &a
			out.Deleted = &d
			out.Files = &f
		} else {
			out.Gaps = append(out.Gaps, "diff_incomplete")
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}
