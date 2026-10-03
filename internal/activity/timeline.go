// SPDX-License-Identifier: AGPL-3.0-only

package activity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Author uses a null ID when the classic record has no mapped principal.
type Author struct {
	ID   *string `json:"id"`
	Name string  `json:"name"`
	// HasAvatar says a picture exists; clients request it only then.
	HasAvatar bool `json:"has_avatar"`
	// Automatic is true when the tenant System principal wrote the event.
	// Clients show that actor as Aeon (automatic). A person named System is not.
	Automatic bool `json:"automatic,omitempty"`
	// Job and Reason are the background job recorded on the event.
	Job    string `json:"job,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// avatarExists is the SQL test for a principal's profile picture.
const avatarExists = `EXISTS (SELECT 1 FROM personal_profiles avatar WHERE avatar.tenant_id=$1 AND avatar.principal_id=coalesce(target.id,p.id) AND avatar.avatar_hashes <> '{}'::jsonb)`

func hasAvatar(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM personal_profiles WHERE tenant_id=$1 AND principal_id=$2 AND avatar_hashes <> '{}'::jsonb)`, tenantID, principalID).Scan(&has)
	return has, err
}

type FieldChange struct {
	Field string  `json:"field"`
	From  *string `json:"from"`
	To    *string `json:"to"`
}

type Item struct {
	AutomaticChange *statusautopilot.Change `json:"automatic_change,omitempty"`
	ID              string                  `json:"id"`
	At              time.Time               `json:"at"`
	Type            string                  `json:"type"`
	Author          Author                  `json:"author"`
	BodyMarkdown    *string                 `json:"body_markdown,omitempty"`
	Changes         []FieldChange           `json:"changes,omitempty"`
}

type Page struct {
	Items      []Item  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type cursor struct {
	Tenant    string    `json:"tenant"`
	Node      string    `json:"node"`
	Watermark int64     `json:"watermark"`
	At        time.Time `json:"at"`
	ID        int64     `json:"id"`
}

func decodeCursor(raw, tenantID, node string) (*cursor, error) {
	if len(raw) > 1024 {
		return nil, errInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil || c.Tenant != tenantID || c.Node != node || c.Watermark < 1 || c.ID < 1 || c.ID > c.Watermark || c.At.IsZero() {
		return nil, errInvalid
	}
	return &c, nil
}

type record map[string]any
type activityEvent struct {
	id               int64
	at               time.Time
	typ, actor       string
	job, reason      string
	kindFrom, kindTo string
	before, after    record
}

func decodeRecord(raw []byte) (record, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v record
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	err := d.Decode(&v)
	return v, err
}

func (m *module) read(ctx context.Context, p tenant.Principal, node string, limit int, c *cursor) (Page, error) {
	page := Page{Items: []Item{}}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var found string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, p.TenantID, node).Scan(&found); err != nil {
			return err
		}
		// The node index bounds work to this ticket, not the tenant's event log.
		// A single statement captures a consistent event set; the cursor then
		// fences later inserts, including backdated import rows and comment edits.
		watermark := int64(0)
		if c != nil {
			watermark = c.Watermark
		}
		rows, err := tx.Query(ctx, `SELECT id,at,type,actor_principal_id::text,before,after,metadata FROM events
		 WHERE tenant_id=$1 AND node_id=$2 AND ($3::bigint=0 OR id<=$3)
		 AND type IN ('import.comment','import.history','import.node_created','node.created','node.updated','node.moved','node.kind_changed','comment.created','comment.updated','comment.deleted','status_autopilot.changed','status_autopilot.undone','status_autopilot.skipped')
		 ORDER BY id`, p.TenantID, node, watermark)
		if err != nil {
			return err
		}
		var evs []activityEvent
		for rows.Next() {
			var e activityEvent
			var before, after, meta []byte
			if err := rows.Scan(&e.id, &e.at, &e.typ, &e.actor, &before, &after, &meta); err != nil {
				rows.Close()
				return err
			}
			e.job, e.reason = jobReason(meta)
			e.kindFrom, e.kindTo = kindChange(meta)
			e.before, err = decodeRecord(before)
			if err == nil {
				e.after, err = decodeRecord(after)
			}
			if err != nil {
				rows.Close()
				return err
			}
			evs = append(evs, e)
			if c == nil && e.id > watermark {
				watermark = e.id
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		people, err := readPeople(ctx, tx, p.TenantID, evs)
		if err != nil {
			return err
		}
		items := project(evs, people)
		automatic, err := statusautopilot.ChangesTx(ctx, tx, p, node, false, 0)
		if err != nil {
			return err
		}
		automaticByID := map[int64]statusautopilot.Change{}
		for _, change := range automatic {
			automaticByID[change.EventID] = change
		}
		for i := range items {
			id, _ := strconv.ParseInt(items[i].ID, 10, 64)
			if change, ok := automaticByID[id]; ok {
				items[i].AutomaticChange = &change
			}
		}
		for _, item := range items {
			id, _ := strconv.ParseInt(item.ID, 10, 64)
			if c != nil && (item.At.After(c.At) || (item.At.Equal(c.At) && id >= c.ID)) {
				continue
			}
			if len(page.Items) == limit {
				last := page.Items[limit-1]
				lastID, _ := strconv.ParseInt(last.ID, 10, 64)
				b, _ := json.Marshal(cursor{Tenant: p.TenantID, Node: node, Watermark: watermark, At: last.At, ID: lastID})
				next := base64.RawURLEncoding.EncodeToString(b)
				page.NextCursor = &next
				break
			}
			page.Items = append(page.Items, item)
		}
		return nil
	})
	return page, err
}

// Fetch only referenced principals. Classic identity subjects include the
// source instance, so equal numeric user IDs from different sources cannot mix.
func readPeople(ctx context.Context, tx pgx.Tx, tenantID string, evs []activityEvent) (map[string]Author, error) {
	ids, subjects, aliases := []string{}, []string{}, []string{}
	for _, e := range evs {
		ids = append(ids, e.actor)
		source := sourceOf(e)
		for _, r := range []record{e.before, e.after, object(e.after["record"]), object(object(e.after["record"])["snapshot"]), object(object(e.after["fields"])["classic"])} {
			for _, key := range []string{"author_id", "changed_by", "created_by", "assignee_id"} {
				if v := scalar(r[key]); v != nil && source != "" {
					subjects = append(subjects, source+":"+*v)
				}
			}
			for _, key := range []string{"author", "author_name", "changed_by_name", "created_by_name", "agent_name"} {
				if name := textValue(r[key]); name != "" && source != "" {
					aliases = append(aliases, source+":"+name)
				}
			}
			for _, key := range []string{"assignee", "assignee_id", "created_by"} {
				if v := scalar(object(r["fields"])[key]); v != nil {
					ids = append(ids, *v)
				}
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,coalesce(target.id,p.id)::text,coalesce(target.name,p.name),CASE WHEN i.issuer='paimos-classic' THEN i.subject ELSE '' END,`+avatarExists+`,(p.kind='agent' AND p.name='System' AND p.roles @> ARRAY['system']::text[])
	 FROM principals p LEFT JOIN identities i ON i.id=p.identity_id
 LEFT JOIN principals target ON target.tenant_id=p.tenant_id AND target.id=p.linked_to
	 WHERE p.tenant_id=$1 AND (p.id::text=ANY($2::text[]) OR (i.issuer='paimos-classic' AND i.subject=ANY($3::text[])))
 UNION ALL
 SELECT p.id::text,coalesce(target.id,p.id)::text,coalesce(target.name,p.name),'username:'||aliases.alias,`+avatarExists+`,(p.kind='agent' AND p.name='System' AND p.roles @> ARRAY['system']::text[]) FROM principals p JOIN (
   SELECT min(after->'principal'->>'id') AS principal_id,
     (after->'classic'->>'source_id')||':'||(after->'classic'->>'username') AS alias
   FROM events WHERE tenant_id=$1 AND type IN ('import.user_created','import.user_updated')
   GROUP BY (after->'classic'->>'source_id')||':'||(after->'classic'->>'username')
   HAVING count(DISTINCT after->'principal'->>'id')=1
 ) aliases ON aliases.principal_id=p.id::text
 LEFT JOIN principals target ON target.tenant_id=p.tenant_id AND target.id=p.linked_to
 WHERE p.tenant_id=$1 AND aliases.alias=ANY($4::text[])`, tenantID, ids, subjects, aliases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := map[string]Author{}
	for rows.Next() {
		var id, canonical, name, subject string
		var avatar, automatic bool
		if err := rows.Scan(&id, &canonical, &name, &subject, &avatar, &automatic); err != nil {
			return nil, err
		}
		a := Author{ID: &canonical, Name: name, HasAvatar: avatar, Automatic: automatic}
		people[id] = a
		if subject != "" {
			people[subject] = a
		}
	}
	return people, rows.Err()
}

func (e activityEvent) author(people map[string]Author) Author {
	a := people[e.actor]
	a.Job, a.Reason = e.job, e.reason
	return a
}

func jobReason(raw []byte) (string, string) {
	if len(raw) == 0 {
		return "", ""
	}
	var doc struct {
		Job    string `json:"job"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", ""
	}
	return strings.TrimSpace(doc.Job), strings.TrimSpace(doc.Reason)
}

func kindChange(raw []byte) (string, string) {
	if len(raw) == 0 {
		return "", ""
	}
	var doc struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", ""
	}
	return strings.TrimSpace(doc.From), strings.TrimSpace(doc.To)
}

func sourceOf(e activityEvent) string {
	if ref, ok := e.after["classic_ref"].(string); ok {
		source, _, found := strings.Cut(ref, ":"+e.typ+":")
		if found {
			return source
		}
	}
	return textValue(object(object(e.after["fields"])["classic"])["source_id"])
}

func project(evs []activityEvent, people map[string]Author) []Item {
	items := []Item{}
	comments := map[string]Item{}
	importIDs := map[string]string{}
	history := []activityEvent{}
	// Input is event-ID order, so the latest revision wins independently of
	// its timestamp. Timeline order is applied only after projecting comments.
	for _, e := range evs {
		id := strconv.FormatInt(e.id, 10)
		item := Item{ID: id, At: e.at, Author: e.author(people)}
		switch e.typ {
		case "comment.created":
			body := textValue(e.after["body_markdown"])
			item.Type = "comment"
			item.BodyMarkdown = &body
			comments[id] = item
		case "comment.updated", "comment.deleted":
			key := textValue(e.after["comment_id"])
			if e.typ == "comment.deleted" {
				delete(comments, key)
			} else if old, ok := comments[key]; ok {
				body := textValue(e.after["body_markdown"])
				old.BodyMarkdown = &body
				comments[key] = old
			}
		case "import.comment":
			r := object(e.after["record"])
			source := sourceOf(e)
			key := source + ":" + textValue(r["id"])
			if textValue(r["id"]) == "" {
				key = id
			}
			item.Type = "comment"
			item.Author = classicAuthor(r, source, people, "author_id", "author", "author_name")
			body := textValue(r["body"])
			item.BodyMarkdown = &body
			if original, ok := importIDs[key]; ok {
				old := comments[original]
				item.ID = old.ID
				item.At = old.At
			} else {
				importIDs[key] = id
			}
			comments[item.ID] = item
		case "import.history":
			history = append(history, e)
		case "node.created", "import.node_created":
			item.Type = "created"
			if e.typ == "import.node_created" {
				r := object(object(e.after["fields"])["classic"])
				item.Author = classicAuthor(r, sourceOf(e), people, "created_by", "created_by_name")
				if id := textValue(object(e.after["fields"])["created_by"]); id != "" {
					if author, ok := people[id]; ok {
						item.Author = author
					}
				}
				if at, err := time.Parse(time.RFC3339Nano, textValue(e.after["created_at"])); err == nil {
					item.At = at
				}
			}
			items = append(items, item)
		case "node.kind_changed":
			if e.kindFrom == "" || e.kindTo == "" || e.kindFrom == e.kindTo {
				break
			}
			from, to := e.kindFrom, e.kindTo
			item.Type = "change"
			item.Changes = []FieldChange{{Field: "kind", From: &from, To: &to}}
			items = append(items, item)
		case "status_autopilot.changed", "status_autopilot.skipped":
			item.Type = "change"
			item.Changes = diff(nativeFields(e.before, people), nativeFields(e.after, people))
			items = append(items, item)
		case "node.updated", "node.moved", "status_autopilot.undone":
			item.Type = "change"
			item.Changes = diff(nativeFields(e.before, people), nativeFields(e.after, people))
			if len(item.Changes) > 0 {
				items = append(items, item)
			}
		}
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].at.Equal(history[j].at) {
			return history[i].id < history[j].id
		}
		return history[i].at.Before(history[j].at)
	})
	previous := map[string]record{}
	for _, e := range history {
		r := object(e.after["record"])
		snapshot := object(r["snapshot"])
		if snapshot == nil {
			continue
		}
		source := sourceOf(e)
		fields := classicFields(snapshot, source, people)
		if prev, ok := previous[source]; ok {
			changes := diff(prev, fields)
			if len(changes) > 0 {
				items = append(items, Item{ID: strconv.FormatInt(e.id, 10), At: e.at, Type: "change", Author: classicAuthor(r, source, people, "changed_by", "changed_by_name", "agent_name"), Changes: changes})
			}
		}
		previous[source] = fields
	}
	for _, item := range comments {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].At.Equal(items[j].At) {
			a, _ := strconv.ParseInt(items[i].ID, 10, 64)
			b, _ := strconv.ParseInt(items[j].ID, 10, 64)
			return a > b
		}
		return items[i].At.After(items[j].At)
	})
	return items
}

func classicAuthor(r record, source string, people map[string]Author, idKey string, nameKeys ...string) Author {
	if id := scalar(r[idKey]); id != nil {
		if a, ok := people[source+":"+*id]; ok {
			return a
		}
	}
	for _, key := range nameKeys {
		if name := textValue(r[key]); name != "" {
			if author, ok := people["username:"+source+":"+name]; ok {
				return author
			}
			return Author{Name: name}
		}
	}
	return Author{Name: "Unknown"}
}

// tagLabel is the ticket's labels as one string, names in stored order.
// A missing list and an empty list are the same. Objects contribute their name.
func tagLabel(v any) any {
	names := tagNames(v)
	if len(names) == 0 {
		return nil
	}
	return strings.Join(names, ", ")
}

func tagNames(v any) []string {
	switch value := v.(type) {
	case string:
		if name := strings.TrimSpace(value); name != "" {
			return []string{name}
		}
	case map[string]any:
		if name := strings.TrimSpace(textValue(value["name"])); name != "" {
			return []string{name}
		}
	case []any:
		var names []string
		for _, item := range value {
			names = append(names, tagNames(item)...)
		}
		return names
	}
	return nil
}

func nativeFields(r record, people map[string]Author) record {
	f := object(r["fields"])
	return record{"human_check": r["human_check"], "status": r["state"], "priority": f["priority"], "assignee": personName(f["assignee"], "", people), "title": r["title"], "parent": r["parent_id"], "tags": tagLabel(f["tags"])}
}

func classicFields(r record, source string, people map[string]Author) record {
	return record{"status": r["status"], "priority": r["priority"], "assignee": personName(r["assignee_id"], source, people), "title": r["title"], "parent": r["parent_id"]}
}

func personName(v any, source string, people map[string]Author) any {
	id := scalar(v)
	if id == nil {
		return nil
	}
	key := *id
	if source != "" {
		key = source + ":" + key
	}
	if p, ok := people[key]; ok {
		return p.Name
	}
	return *id
}

func diff(before, after record) []FieldChange {
	var changes []FieldChange
	for _, field := range []string{"status", "priority", "assignee", "title", "parent", "tags", "human_check"} {
		a, b := scalar(before[field]), scalar(after[field])
		if (a == nil && b == nil) || (a != nil && b != nil && *a == *b) {
			continue
		}
		changes = append(changes, FieldChange{Field: field, From: a, To: b})
	}
	return changes
}

func object(v any) record {
	switch m := v.(type) {
	case map[string]any:
		return record(m)
	case record:
		return m
	}
	return nil
}
func scalar(v any) *string {
	var s string
	switch value := v.(type) {
	case string:
		s = value
	case json.Number:
		s = value.String()
	default:
		return nil
	}
	if s == "" {
		return nil
	}
	return &s
}
func textValue(v any) string {
	if s := scalar(v); s != nil {
		return *s
	}
	return ""
}
