// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const subscriptionBatch = 200
const subscriptionHeartbeat = 15 * time.Second

// Admission bounds connection creation as well as persistent listeners. The
// session advisory slots below enforce the key limit across server processes.
type subscriptionLimits struct {
	mu    sync.Mutex
	keys  map[string]int
	total int
}

func (l *subscriptionLimits) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= 64 || l.keys[key] >= 2 {
		return false
	}
	if l.keys == nil {
		l.keys = make(map[string]int)
	}
	l.keys[key]++
	l.total++
	return true
}

func (l *subscriptionLimits) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys[key]--
	l.total--
	if l.keys[key] == 0 {
		delete(l.keys, key)
	}
}

type subscriptionRequest struct {
	after  int64
	latest bool
	topics map[string]bool
}

func parseSubscription(r *http.Request) (subscriptionRequest, error) {
	out := subscriptionRequest{latest: true, topics: make(map[string]bool)}
	if len(r.URL.RawQuery) > 256 {
		return out, errors.New("query too large")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return out, err
	}
	for key, values := range query {
		if key != "after" && key != "topics" || len(values) != 1 {
			return out, errors.New("invalid query")
		}
	}
	cursor := "latest"
	if query.Has("after") {
		cursor = query.Get("after")
	}
	if values, present := r.Header["Last-Event-Id"]; present {
		if len(values) != 1 {
			return out, errors.New("invalid Last-Event-ID")
		}
		cursor = values[0]
		if cursor == "latest" {
			return out, errors.New("invalid Last-Event-ID")
		}
	}
	if len(cursor) == 0 || len(cursor) > 19 {
		return out, errors.New("invalid cursor")
	}
	if cursor != "latest" {
		out.after, err = parseID(cursor)
		if err != nil {
			return out, errors.New("invalid cursor")
		}
		out.latest = false
	}
	if !query.Has("topics") {
		for topic := range subscriptionTopics {
			out.topics[topic] = true
		}
	} else {
		raw := query.Get("topics")
		if len(raw) == 0 || len(raw) > 128 {
			return out, errors.New("invalid topics")
		}
		for _, topic := range strings.Split(raw, ",") {
			if !subscriptionTopics[topic] || out.topics[topic] {
				return out, errors.New("invalid topics")
			}
			out.topics[topic] = true
		}
	}
	return out, nil
}

func (m *module) subscribe(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	keyID := p.KeyID
	explicit := false
	for _, scope := range p.Scopes {
		explicit = explicit || strings.ReplaceAll(scope, ":", ".") == "events.subscribe"
	}
	if p.Kind != tenant.Agent || !validUUID(keyID) || !explicit {
		writeError(w, 403, "forbidden", "explicit events.subscribe agent key required")
		return
	}
	request, err := parseSubscription(r)
	if err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	ctx := tenant.WithPrincipal(r.Context(), p)
	if err := m.subscriptionAuthorize(ctx); err != nil {
		subscriptionFailure(w, err)
		return
	}
	key := p.TenantID + ":" + keyID
	if !m.subscriptions.acquire(key) {
		subscriptionLimit(w)
		return
	}
	defer m.subscriptions.release(key)
	rc := http.NewResponseController(w)
	// A stream without enforceable write deadlines cannot safely bound slow
	// consumers. Standard net/http and the production wrappers support this.
	if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		writeError(w, 503, "stream_unavailable", "streaming deadlines unavailable")
		return
	}
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, m.pool.Config().ConnConfig.Copy())
	if err == nil {
		admitted := false
		for slot := 0; slot < 2; slot++ {
			err = conn.QueryRow(connectCtx, `SELECT pg_try_advisory_lock(hashtextextended($1,885))`, fmt.Sprintf("%s:%d", key, slot)).Scan(&admitted)
			if err == nil && admitted {
				break
			}
			if err != nil {
				break
			}
		}
		if err == nil && !admitted {
			conn.Close(connectCtx)
			subscriptionLimit(w)
			cancel()
			return
		}
		if err == nil {
			_, err = conn.Exec(connectCtx, "LISTEN aeon_events")
		}
	}
	cancel()
	if conn != nil {
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = conn.Close(closeCtx)
		}()
	}
	if err != nil {
		subscriptionFailure(w, err)
		return
	}
	// LISTEN precedes the boundary read, closing the replay/live handoff gap.
	newest, err := m.subscriptionNewest(ctx, p)
	if err != nil {
		subscriptionFailure(w, err)
		return
	}
	after := request.after
	if request.latest {
		after = newest
	}
	if after > newest {
		writeError(w, 400, "invalid_cursor", "cursor exceeds tenant log")
		return
	}
	items, through, err := m.subscriptionRead(ctx, p, after, newest, request.topics)
	if err != nil {
		subscriptionFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flush := func(payload string) error {
		if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, payload); err != nil {
			return err
		}
		return rc.Flush()
	}
	checkpoint := func(name string, cursor int64) error {
		return flush(fmt.Sprintf("id: %d\nevent: %s\ndata: {\"after\":%d}\n\n", cursor, name, cursor))
	}
	if err := checkpoint("stream.ready", after); err != nil {
		return
	}
	heartbeat := time.Now().Add(subscriptionHeartbeat)
	for {
		for _, item := range items {
			if ctx.Err() != nil {
				return
			}
			data, err := json.Marshal(item)
			if err != nil || flush(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", item.ID, item.Type, data)) != nil {
				return
			}
		}
		if through > after {
			if err := checkpoint("stream.cursor", through); err != nil {
				return
			}
			after = through
		}
		if through >= newest {
			wait := m.subscriptionWait
			if wait == nil {
				wait = waitSubscription
			}
			if err := wait(ctx, conn, heartbeat); err != nil {
				if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
					return
				}
				// Revalidate even an idle connection before sending its ping.
				if err := m.subscriptionAuthorize(ctx); err != nil {
					return
				}
				if err := flush("event: stream.ping\ndata: {}\n\n"); err != nil {
					return
				}
				heartbeat = time.Now().Add(subscriptionHeartbeat)
			}
		}
		newest, err = m.subscriptionNewest(ctx, p)
		if err != nil {
			return
		}
		items, through, err = m.subscriptionRead(ctx, p, after, newest, request.topics)
		if err != nil {
			return
		} // Resume only from the last processed frame.
	}
}

func waitSubscription(ctx context.Context, conn *pgx.Conn, deadline time.Time) error {
	for {
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		waitCtx, stop := context.WithDeadline(ctx, deadline)
		notification, err := conn.WaitForNotification(waitCtx)
		stop()
		if err != nil {
			return err
		}
		// Other tenants neither trigger reads nor postpone the heartbeat.
		var hint struct {
			TenantID string `json:"tenant_id"`
		}
		p, _ := tenant.PrincipalFrom(ctx)
		if json.Unmarshal([]byte(notification.Payload), &hint) == nil && hint.TenantID == p.TenantID {
			return nil
		}
	}
}

func subscriptionLimit(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "15")
	writeError(w, 429, "connection_limit", "subscription connection limit reached")
}

func subscriptionFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, authz.ErrForbidden) || errors.Is(err, db.ErrKeyAuthorityChanged) {
		writeError(w, 403, "forbidden", "subscription authority denied")
	} else {
		writeError(w, 503, "stream_unavailable", "subscription temporarily unavailable")
	}
}

// Change hints deliberately omit snapshots, actor and resource identities.
type subscriptionHint struct {
	ID    int64     `json:"id"`
	Type  string    `json:"type"`
	Topic string    `json:"topic"`
	At    time.Time `json:"at"`
}

func (m *module) subscriptionRead(ctx context.Context, p tenant.Principal, after, newest int64, topics map[string]bool) ([]subscriptionHint, int64, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	ctx = db.WithReadStatementTimeout(ctx, 10*time.Second)
	// History's event-family RLS intentionally hides internal policy/delivery
	// events from project readers. This metadata-only projection instead checks
	// the exact topic permission and every referenced node below. The allowlist
	// contains no private inbox, quote, account, pairing or credential events.
	readCtx := db.AllProjects(ctx, "agent subscription: allowlisted value-free hints with explicit current node and topic checks")
	through := newest
	var items []subscriptionHint
	err := db.InTenant(readCtx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "events.subscribe", authz.Scope{}); err != nil {
			return err
		}
		workspaceNodesErr := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{})
		if workspaceNodesErr != nil && !errors.Is(workspaceNodesErr, authz.ErrForbidden) {
			return workspaceNodesErr
		}
		projects, err := authz.GrantedProjectIDsTx(ctx, tx, p, "nodes.read")
		if err != nil {
			return err
		}
		if len(projects) > 2000 {
			return errors.New("subscription project set exceeds bound")
		}
		types := make([]string, 0, len(subscriptionTypes))
		for typ, policy := range subscriptionTypes {
			if topics[policy.topic] {
				types = append(types, typ)
			}
		}
		// Tenant RLS remains active. Current nodes.read grants (including key
		// scope and creator ceiling) replace only event-family visibility. Every
		// referenced node must exist and belong to the permitted node set.
		rows, err := tx.Query(ctx, `SELECT e.id,e.type,e.at,coalesce(n.project_id,CASE WHEN k.slug='project' THEN n.id END)::text,
            left(coalesce(e.after->>'scope',e.before->>'scope',''),37),cardinality(e.node_refs)>100
            FROM events e LEFT JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
            LEFT JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
            WHERE e.tenant_id=$1 AND e.id>$2 AND e.id<=$3 AND e.type=ANY($4::text[])
            AND (e.node_id IS NULL OR n.id IS NOT NULL AND ($5 OR n.project_id=ANY($6::uuid[]) OR k.slug='project' AND n.id=ANY($6::uuid[])))
            AND CASE WHEN cardinality(e.node_refs)>100 THEN true ELSE NOT EXISTS (SELECT 1 FROM unnest(e.node_refs) ref(id)
                LEFT JOIN nodes rn ON rn.tenant_id=e.tenant_id AND rn.id=ref.id
                LEFT JOIN node_kinds rk ON rk.tenant_id=rn.tenant_id AND rk.id=rn.kind_id
                WHERE rn.id IS NULL OR NOT ($5 OR coalesce(rn.project_id=ANY($6::uuid[]) OR rk.slug='project' AND rn.id=ANY($6::uuid[]),false))) END
            ORDER BY e.id LIMIT 201`, p.TenantID, after, newest, types, workspaceNodesErr == nil, projects)
		if err != nil {
			return err
		}
		type candidate struct {
			hint      subscriptionHint
			project   *string
			scope     string
			oversized bool
		}
		candidates := []candidate{}
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.hint.ID, &c.hint.Type, &c.hint.At, &c.project, &c.scope, &c.oversized); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(candidates) > subscriptionBatch {
			candidates = candidates[:subscriptionBatch]
			through = candidates[len(candidates)-1].hint.ID
		}
		cache := map[string]bool{}
		for _, c := range candidates {
			if c.oversized {
				return errors.New("subscription event references exceed bound")
			}
			policy := subscriptionTypes[c.hint.Type]
			project := ""
			if c.project != nil {
				project = *c.project
			}
			if policy.topic == "preferences" {
				// The preference editor is person-only. An agent can read the
				// effective routing through models.resolve, but never another
				// person's preference board or raw snapshots.
				if !validUUID(c.scope) {
					continue
				}
				var person *string
				var scopedProject *string
				err := tx.QueryRow(ctx, `SELECT person_id::text,project_id::text FROM model_pref_scopes WHERE tenant_id=$1 AND id=$2::uuid`, p.TenantID, c.scope).Scan(&person, &scopedProject)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				if person != nil {
					if p.KeyCreatorID == "" {
						continue
					}
					var own bool
					if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals a JOIN principals b ON b.tenant_id=a.tenant_id AND coalesce(b.linked_to,b.id)=coalesce(a.linked_to,a.id) WHERE a.tenant_id=$1 AND a.id=$2::uuid AND b.id=$3::uuid AND a.kind='person' AND a.status='active' AND b.status='active')`, p.TenantID, p.KeyCreatorID, *person).Scan(&own); err != nil {
						return err
					}
					if !own {
						continue
					}
				}
				if scopedProject != nil {
					project = *scopedProject
				}
			}
			cacheKey := policy.permission + ":" + project
			allowed, cached := cache[cacheKey]
			if !cached {
				err := authz.RequireTx(ctx, tx, p, policy.permission, authz.Scope{ProjectID: project})
				if err != nil && !errors.Is(err, authz.ErrForbidden) {
					return err
				}
				allowed = err == nil
				cache[cacheKey] = allowed
			}
			if !allowed {
				continue
			}
			if policy.topic == "preferences" && project != "" {
				if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
					if errors.Is(err, authz.ErrForbidden) {
						continue
					}
					return err
				}
			}
			c.hint.Topic = policy.topic
			items = append(items, c.hint)
		}
		return nil
	})
	if err != nil {
		return nil, after, err
	}
	if topics["plan"] {
		// This service read selects only the authenticated key creator's plan
		// hints. NoProjects keeps every project invisible; no other event type
		// or preference value crosses this ownership-specific exception.
		planCtx := db.NoProjects(ctx, "agent subscription: own canonical person's value-free plan hints")
		err = db.InTenant(planCtx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			if err := authz.RequireTx(planCtx, tx, p, "events.subscribe", authz.Scope{}); err != nil {
				return err
			}
			if err := authz.RequireTx(planCtx, tx, p, "agents.plan.read", authz.Scope{}); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					return nil
				}
				return err
			}
			if p.KeyCreatorID == "" {
				return nil
			}
			rows, err := tx.Query(planCtx, `SELECT e.id,e.type,e.at FROM events e
                JOIN principals owner ON owner.tenant_id=e.tenant_id AND owner.id=$4::uuid AND owner.kind='person' AND owner.status='active'
                JOIN principals canonical ON canonical.tenant_id=owner.tenant_id AND canonical.id=coalesce(owner.linked_to,owner.id) AND canonical.kind='person' AND canonical.status='active'
                WHERE e.tenant_id=$1 AND e.id>$2 AND e.id<=$3 AND e.node_id IS NULL AND e.type='agents_plan.changed'
                AND e.after->>'principal_id'=canonical.id::text ORDER BY e.id LIMIT 201`, p.TenantID, after, newest, p.KeyCreatorID)
			if err != nil {
				return err
			}
			defer rows.Close()
			plans := []subscriptionHint{}
			for rows.Next() {
				var h subscriptionHint
				if err := rows.Scan(&h.ID, &h.Type, &h.At); err != nil {
					return err
				}
				h.Topic = "plan"
				plans = append(plans, h)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if len(plans) > subscriptionBatch {
				plans = plans[:subscriptionBatch]
				through = min(through, plans[len(plans)-1].ID)
			}
			items = append(items, plans...)
			return nil
		})
	}
	if err != nil {
		return nil, after, err
	}
	filtered := items[:0]
	for _, h := range items {
		if h.ID <= through {
			filtered = append(filtered, h)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	// Each read can contribute 200 hints. Cap the combined page without losing
	// any by advancing only through its last emitted hint.
	if len(filtered) > subscriptionBatch {
		filtered = filtered[:subscriptionBatch]
		through = filtered[len(filtered)-1].ID
	}
	return filtered, through, nil
}

// Each database operation has its own bound; the stream itself is long-lived.
func (m *module) subscriptionAuthorize(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return authz.Require(authz.BindPool(db.WithReadStatementTimeout(bounded, 10*time.Second), m.pool), "events.subscribe", authz.Scope{})
}

func (m *module) subscriptionNewest(ctx context.Context, p tenant.Principal) (int64, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.newest(db.WithReadStatementTimeout(bounded, 10*time.Second), p)
}
