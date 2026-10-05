// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// This is a read projection. In particular, it never returns arbitrary event
// snapshots (account events and other domains have their own disclosure rules).
type activityItem struct {
	EventID         int64      `json:"event_id"`
	NodeID          *string    `json:"node_id"`
	ProjectID       *string    `json:"project_id"`
	Key             string     `json:"key"`
	Title           string     `json:"title"`
	Actor           string     `json:"actor"`
	Type            string     `json:"type"`
	Rule            string     `json:"rule"`
	Reason          string     `json:"reason"`
	From            string     `json:"from"`
	To              string     `json:"to"`
	At              time.Time  `json:"at"`
	Revision        *time.Time `json:"revision"`
	Automatic       bool       `json:"automatic"`
	Undone          bool       `json:"undone"`
	ChangedSince    bool       `json:"changed_since"`
	Undoable        bool       `json:"undoable"`
	RequiresPreview bool       `json:"requires_preview"`
}

type activityPage struct {
	Items      []activityItem `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type activityQuery struct {
	view, rule, project, search string
	limit                       int
	at                          *time.Time
	id                          int64
}

func (q activityQuery) fingerprint() string {
	hash := sha256.Sum256([]byte(url.Values{"view": {q.view}, "rule": {q.rule}, "project": {q.project}, "q": {q.search}}.Encode()))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func (q activityQuery) cursor(item activityItem) string {
	return base64.RawURLEncoding.EncodeToString([]byte(item.At.Format(time.RFC3339Nano) + "|" + strconv.FormatInt(item.EventID, 10) + "|" + q.fingerprint()))
}

func parseActivityQuery(values url.Values) (activityQuery, error) {
	q := activityQuery{view: values.Get("view"), rule: values.Get("rule"), project: values.Get("project_id"), limit: 50}
	invalid := fmt.Errorf("invalid activity filters or cursor")
	if len(values.Get("q")) > 200 || !utf8.ValidString(values.Get("q")) {
		return q, invalid
	}
	q.search = strings.TrimSpace(values.Get("q"))
	if q.view == "" {
		q.view = "everything"
	}
	switch q.view {
	case "everything", "automatic", "people", "agents":
	default:
		return q, invalid
	}
	switch q.rule {
	case "", "new", "backlog", "blocked", "progress", "done", "publish", "accept", "work_parent":
	default:
		return q, invalid
	}
	if q.project != "" && !validUUID(q.project) {
		return q, invalid
	}
	if values.Has("limit") {
		n, err := strconv.Atoi(values.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			return q, invalid
		}
		q.limit = n
	}
	if values.Has("cursor") {
		encoded := values.Get("cursor")
		if encoded == "" || len(encoded) > 256 {
			return q, invalid
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		parts := strings.Split(string(decoded), "|")
		if err != nil || len(parts) != 3 || parts[2] != q.fingerprint() {
			return q, invalid
		}
		at, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return q, invalid
		}
		q.at = &at
		q.id, err = parseID(parts[1])
		if err != nil || q.id < 1 {
			return q, invalid
		}
	}
	return q, nil
}

func (m *module) activity(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if len(r.URL.RawQuery) > 2048 {
		writeError(w, 400, "invalid_request", "activity query is too long")
		return
	}
	q, err := parseActivityQuery(r.URL.Query())
	if err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	page, err := m.readActivity(ctx, p, q)
	if err != nil {
		if db.IsStatementTimeout(err) || ctx.Err() != nil {
			writeError(w, 503, "read_timeout", "Activity could not be loaded. Retry the same page.")
		} else if errors.Is(err, ErrForbidden) {
			writeError(w, 403, "forbidden", "Activity is not available with the current permissions.")
		} else {
			failure(w, err)
		}
		return
	}
	httpapi.WriteJSON(w, 200, page)
}

func (m *module) readActivity(ctx context.Context, p tenant.Principal, q activityQuery) (activityPage, error) {
	page := activityPage{Items: []activityItem{}}
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "events.read", authz.Scope{AnyProject: true}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return ErrForbidden
			}
			return err
		}
		// The existing events and nodes RLS policies are both applied. Quote
		// metadata stays on its own authorized routes, as on /events. Restrict
		// metadata extraction to automatic changes; arbitrary job metadata is
		// not a general workspace disclosure surface.
		query := `WITH activity AS NOT MATERIALIZED (
 SELECT e.id,e.at,e.node_id::text,n.project_id::text,coalesce(n.key,'') AS key,
 coalesce(n.title,replace(e.type,'_',' ')) AS title,
 coalesce(p.name,'') AS actor,e.actor_principal_id,e.type,
 e.type IN ('status_autopilot.changed','status_autopilot.derived') AS automatic,
 CASE WHEN e.type IN ('status_autopilot.changed','status_autopilot.derived') THEN coalesce(e.metadata->>'rule','') ELSE '' END AS rule,
 CASE WHEN e.type IN ('status_autopilot.changed','status_autopilot.derived') THEN coalesce(e.metadata->>'reason','') ELSE '' END AS reason,
 CASE WHEN e.type IN ('node.created','node.updated','node.deleted','status_autopilot.changed','status_autopilot.derived','status_autopilot.undone') THEN coalesce(e.before->>'state','') ELSE '' END AS from_state,
 CASE WHEN e.type IN ('node.created','node.updated','node.deleted','status_autopilot.changed','status_autopilot.derived','status_autopilot.undone') THEN coalesce(nullif(e.metadata->>'flag',''),e.after->>'state','') ELSE '' END AS to_state,
 CASE WHEN e.type IN ('status_autopilot.changed','status_autopilot.derived') THEN e.after->>'updated_at' END AS revision,
 n.updated_at AS current_revision,n.deleted_at IS NULL AND n.id IS NOT NULL AS live,
 n.state= e.after->>'state' AS same_state,p.kind AS actor_kind
 FROM events e LEFT JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
 LEFT JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
 WHERE e.tenant_id=$1 AND e.type NOT LIKE 'quote.%'
 ) SELECT id,at,node_id,project_id,key,title,actor,actor_principal_id,type,automatic,rule,reason,
 from_state,to_state,revision,current_revision,live,coalesce(same_state,false),
 EXISTS(SELECT 1 FROM events u WHERE u.tenant_id=$1 AND u.undo_of=activity.id)
 FROM activity WHERE ($2='everything' OR ($2='automatic' AND automatic) OR ($2='people' AND actor_kind='person') OR ($2='agents' AND actor_kind='agent'))
 AND ($3='' OR rule=$3) AND ($4::uuid IS NULL OR project_id::uuid=$4::uuid)
 AND ($5='' OR strpos(lower(key||' '||title||' '||actor||' '||type||' '||reason),lower($5))>0)`
		args := []any{p.TenantID, q.view, q.rule, nullable(q.project), q.search}
		if q.at != nil {
			query += ` AND (at,id)<($6::timestamptz,$7::bigint)`
			args = append(args, q.at, q.id)
		}
		args = append(args, q.limit+1)
		query += ` ORDER BY at DESC,id DESC LIMIT $` + strconv.Itoa(len(args))
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		type state struct {
			actor   string
			matches bool
		}
		states := []state{}
		for rows.Next() {
			var item activityItem
			var s state
			var revision *string
			var current *time.Time
			var live, same bool
			if err := rows.Scan(&item.EventID, &item.At, &item.NodeID, &item.ProjectID, &item.Key, &item.Title, &item.Actor, &s.actor, &item.Type, &item.Automatic, &item.Rule, &item.Reason, &item.From, &item.To, &revision, &current, &live, &same, &item.Undone); err != nil {
				rows.Close()
				return err
			}
			if revision != nil {
				if at, err := time.Parse(time.RFC3339Nano, *revision); err == nil {
					item.Revision = &at
					s.matches = live && same && current != nil && current.Equal(at)
				}
			}
			item.ChangedSince = item.Automatic && !s.matches
			item.RequiresPreview = item.Type == derivedStatusEvent
			if item.Automatic {
				item.Actor = "Status autopilot"
			}
			page.Items = append(page.Items, item)
			states = append(states, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > q.limit {
			page.Items = page.Items[:q.limit]
			cursor := q.cursor(page.Items[len(page.Items)-1])
			page.NextCursor = &cursor
		}
		// Read-time availability is advisory. The registered mutation remains
		// responsible for locks, revision and permission checks in its write.
		for i := range page.Items {
			item, s := &page.Items[i], states[i]
			if item.Type != "status_autopilot.changed" || !s.matches || item.Undone || m.undo[item.Type] == nil {
				continue
			}
			scope := authz.Scope{}
			if item.ProjectID != nil {
				scope.ProjectID = *item.ProjectID
			}
			item.Undoable = authz.RequireTx(ctx, tx, p, "events.undo", scope) == nil &&
				(s.actor == p.ID || authz.RequireTx(ctx, tx, p, "events.undo_other", scope) == nil) &&
				authz.RequireTx(ctx, tx, p, "nodes.write", scope) == nil
		}
		return nil
	})
	return page, err
}
