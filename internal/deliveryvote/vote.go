// SPDX-License-Identifier: AGPL-3.0-only

// Package deliveryvote records a person's rework mark on one harness session.
// Silence is not stored: a row is an exception. Score is an optional detail.
// Agents cannot read or write these routes. Objective signals are counts of
// ticket events, present only when those events were recorded.
package deliveryvote

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const (
	maxDepth    = 8
	maxScope    = 200
	maxSessions = 80
	maxComment  = 2000
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var allowedTags = map[string]struct{}{"quality": {}, "rework": {}, "taste": {}}

type module struct{ pool *pgxpool.Pool }

// New serves person rework marks on harness sessions.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool: pool} }

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{nodeId}/delivery-ratings", m.list)
	mux.HandleFunc("GET /api/harness-sessions/{sessionId}/delivery-rating", m.get)
	mux.HandleFunc("PUT /api/harness-sessions/{sessionId}/delivery-rating", m.put)
	mux.HandleFunc("DELETE /api/harness-sessions/{sessionId}/delivery-rating", m.clear)
}

// Signals are objective counts beside a rating. Zero means none were recorded.
type Signals struct {
	ReviewRounds int `json:"review_rounds"`
	CIFailures   int `json:"ci_failures"`
	Reverts      int `json:"reverts"`
}

// Vote is one person's rework mark. Score is an optional detail; null means
// the mark has no score. Comment is the required reason.
type Vote struct {
	Score     *int      `json:"score"`
	Tags      []string  `json:"tags"`
	Comment   string    `json:"comment"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Rating is one visible session and the caller's mark. A missing mark means
// this person has not asked for rework. Votes counts exception rows. Average
// is the mean of the scores that were given. Harness, model and account are
// the session's current metadata. Each saved mark also keeps the snapshot
// copied at save time.
type Rating struct {
	SessionID    string  `json:"session_id"`
	TicketNodeID string  `json:"ticket_node_id"`
	Harness      string  `json:"harness"`
	Model        *string `json:"model"`
	AccountLabel *string `json:"account_label"`
	Mine         *Vote   `json:"mine"`
	Votes        int     `json:"votes"`
	Average      *string `json:"average"`
	Signals      Signals `json:"signals"`
}

// Page is the ratings for sessions under one ticket, epic or task.
type Page struct {
	NodeID   string   `json:"node_id"`
	Sessions []Rating `json:"sessions"`
}

type voteWrite struct {
	Score   *int     `json:"score"`
	Tags    []string `json:"tags"`
	Comment *string  `json:"comment"`
}

type sessionHead struct {
	id, ticket, harness string
	model, account      *string
}

type storedVote struct {
	voter, comment, harness string
	score                   *int
	tags                    []string
	updated                 time.Time
	model, account          *string
}

func (m *module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := person(w, r)
	if !ok {
		return
	}
	nodeID := r.PathValue("nodeId")
	if !uuidPattern.MatchString(nodeID) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var page Page
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ids, found, err := scope(r.Context(), tx, p.TenantID, nodeID)
		if err != nil || !found {
			return err
		}
		heads, err := sessionsIn(r.Context(), tx, p.TenantID, ids)
		if err != nil {
			return err
		}
		rows, err := assemble(r.Context(), tx, p, heads)
		if err != nil {
			return err
		}
		page = Page{NodeID: nodeID, Sessions: rows}
		return nil
	})
	write(w, err, page)
}

func (m *module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := person(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("sessionId")
	if !uuidPattern.MatchString(sessionID) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var rating Rating
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		head, err := oneSession(r.Context(), tx, p.TenantID, sessionID)
		if err != nil {
			return err
		}
		rows, err := assemble(r.Context(), tx, p, []sessionHead{head})
		if err != nil {
			return err
		}
		rating = rows[0]
		return nil
	})
	write(w, err, rating)
}

func (m *module) put(w http.ResponseWriter, r *http.Request) {
	p, ok := person(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("sessionId")
	if !uuidPattern.MatchString(sessionID) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body voteWrite
	if err := workorders.Decode(r, &body); err != nil {
		write(w, err, nil)
		return
	}
	tags, err := normalizeTags(body.Tags)
	if err != nil {
		write(w, err, nil)
		return
	}
	comment, err := normalizeComment(body.Comment)
	if err != nil {
		write(w, err, nil)
		return
	}
	score, err := normalizeScore(body.Score)
	if err != nil {
		write(w, err, nil)
		return
	}
	var rating Rating
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := activePerson(r.Context(), tx, p); err != nil {
			return err
		}
		head, err := oneSession(r.Context(), tx, p.TenantID, sessionID)
		if err != nil {
			return err
		}
		previous, err := myVote(r.Context(), tx, p, sessionID)
		if err != nil {
			return err
		}
		if err := upsert(r.Context(), tx, p, head, score, tags, comment); err != nil {
			return err
		}
		var before any
		if previous != nil {
			before = voteSnapshot(sessionID, previous.score, previous.tags, previous.comment, previous.harness, previous.model, previous.account)
		}
		if _, err := events.Append(r.Context(), tx, p, events.Change{
			NodeID: &head.ticket,
			Type:   "delivery.rated",
			Before: before,
			After:  voteSnapshot(sessionID, score, tags, comment, head.harness, head.model, head.account),
		}); err != nil {
			return err
		}
		rows, err := assemble(r.Context(), tx, p, []sessionHead{head})
		if err != nil {
			return err
		}
		rating = rows[0]
		return nil
	})
	write(w, err, rating)
}

func (m *module) clear(w http.ResponseWriter, r *http.Request) {
	p, ok := person(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("sessionId")
	if !uuidPattern.MatchString(sessionID) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var rating Rating
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := activePerson(r.Context(), tx, p); err != nil {
			return err
		}
		head, err := oneSession(r.Context(), tx, p.TenantID, sessionID)
		if err != nil {
			return err
		}
		previous, err := myVote(r.Context(), tx, p, sessionID)
		if err != nil {
			return err
		}
		if previous != nil {
			if _, err := tx.Exec(r.Context(), `DELETE FROM agent_delivery_votes
				WHERE tenant_id = $1 AND session_id = $2 AND voter_principal_id = $3`, p.TenantID, sessionID, p.ID); err != nil {
				return err
			}
			if _, err := events.Append(r.Context(), tx, p, events.Change{
				NodeID: &head.ticket,
				Type:   "delivery.rated",
				Before: voteSnapshot(sessionID, previous.score, previous.tags, previous.comment, previous.harness, previous.model, previous.account),
			}); err != nil {
				return err
			}
		}
		rows, err := assemble(r.Context(), tx, p, []sessionHead{head})
		if err != nil {
			return err
		}
		rating = rows[0]
		return nil
	})
	write(w, err, rating)
}

func person(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidPattern.MatchString(p.ID) || !uuidPattern.MatchString(p.TenantID) {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return tenant.Principal{}, false
	}
	if p.Kind != tenant.Person {
		httpapi.WriteError(w, http.StatusForbidden, "only a person can mark a delivery")
		return tenant.Principal{}, false
	}
	return p, true
}

func write(w http.ResponseWriter, err error, body any) {
	if err == nil {
		httpapi.WriteJSON(w, http.StatusOK, body)
		return
	}
	var we *workorders.Error
	if errors.As(err, &we) {
		httpapi.WriteError(w, we.Status, we.Message)
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42501" {
		httpapi.WriteError(w, http.StatusForbidden, "only a person can mark a delivery")
		return
	}
	if errors.As(err, &pg) && (pg.Code == "23514" || pg.Code == "22P02") {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid mark")
		return
	}
	httpapi.WriteError(w, http.StatusInternalServerError, "rework mark is unavailable")
}

func normalizeScore(score *int) (*int, error) {
	if score == nil {
		return nil, nil
	}
	if *score < 1 || *score > 5 {
		return nil, &workorders.Error{Status: http.StatusBadRequest, Message: "score must be from 1 to 5"}
	}
	return score, nil
}

func normalizeTags(tags []string) ([]string, error) {
	if tags == nil {
		return []string{}, nil
	}
	if len(tags) > len(allowedTags) {
		return nil, &workorders.Error{Status: http.StatusBadRequest, Message: "too many tags"}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if _, ok := allowedTags[tag]; !ok {
			return nil, &workorders.Error{Status: http.StatusBadRequest, Message: "unknown tag"}
		}
		if _, dup := seen[tag]; dup {
			return nil, &workorders.Error{Status: http.StatusBadRequest, Message: "duplicate tag"}
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeComment(comment *string) (string, error) {
	if comment == nil {
		return "", &workorders.Error{Status: http.StatusBadRequest, Message: "a reason is required"}
	}
	if utf8.RuneCountInString(*comment) > maxComment {
		return "", &workorders.Error{Status: http.StatusBadRequest, Message: "comment is too long"}
	}
	for _, r := range *comment {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return "", &workorders.Error{Status: http.StatusBadRequest, Message: "comment has unsupported characters"}
		}
	}
	trimmed := strings.TrimSpace(*comment)
	if trimmed == "" {
		return "", &workorders.Error{Status: http.StatusBadRequest, Message: "a reason is required"}
	}
	return trimmed, nil
}

func voteSnapshot(sessionID string, score *int, tags []string, comment, harness string, model, account *string) map[string]any {
	out := map[string]any{
		"session_id": sessionID,
		"score":      score,
		"tags":       tags,
		"comment":    comment,
		"harness":    harness,
	}
	if model != nil {
		out["model"] = *model
	}
	if account != nil {
		out["account_label"] = *account
	}
	return out
}

func activePerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM principals WHERE tenant_id = $1 AND id = $2 AND kind = 'person'`, p.TenantID, p.ID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return &workorders.Error{Status: http.StatusForbidden, Message: "only a person can mark a delivery"}
	}
	return err
}

func scope(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]string, bool, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT k.slug FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.tenant_id = $1::uuid AND n.id = $2::uuid AND n.deleted_at IS NULL
			AND k.slug IN ('work', 'ticket', 'epic', 'task')
			AND ((SELECT aeon_visible_all()) OR n.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))`, tenantID, nodeID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, &workorders.Error{Status: http.StatusNotFound, Message: "not found"}
	}
	if err != nil {
		return nil, false, err
	}
	ids := []string{nodeID}
	type item struct {
		id    string
		depth int
	}
	frontier := []item{{nodeID, 0}}
	for len(frontier) > 0 && len(ids) < maxScope {
		parent := frontier[0]
		frontier = frontier[1:]
		if parent.depth >= maxDepth {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT c.id::text FROM nodes c
			WHERE c.tenant_id = $1::uuid AND c.parent_id = $2::uuid AND c.deleted_at IS NULL
				AND ((SELECT aeon_visible_all()) OR c.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
			ORDER BY c.position, c.id`, tenantID, parent.id)
		if err != nil {
			return nil, false, err
		}
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				rows.Close()
				return nil, false, err
			}
			if len(ids) == maxScope {
				break
			}
			ids = append(ids, child)
			frontier = append(frontier, item{child, parent.depth + 1})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, false, err
		}
	}
	return ids, true, nil
}

func sessionsIn(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) ([]sessionHead, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.ticket_node_id::text, s.harness, s.model, s.account_label
		FROM harness_sessions s
		JOIN nodes tn ON tn.tenant_id = s.tenant_id AND tn.id = s.ticket_node_id AND tn.deleted_at IS NULL
		WHERE s.tenant_id = $1::uuid
			AND s.ticket_node_id = ANY($2::uuid[])
			AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
		ORDER BY coalesce(s.stopped_at, s.heartbeat_at, s.created_at) DESC, s.id DESC
		LIMIT $3`, tenantID, ids, maxSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionHead
	for rows.Next() {
		var head sessionHead
		if err := rows.Scan(&head.id, &head.ticket, &head.harness, &head.model, &head.account); err != nil {
			return nil, err
		}
		out = append(out, head)
	}
	return out, rows.Err()
}

func oneSession(ctx context.Context, tx pgx.Tx, tenantID, sessionID string) (sessionHead, error) {
	var head sessionHead
	var ticket *string
	err := tx.QueryRow(ctx, `
		SELECT s.id::text, s.ticket_node_id::text, s.harness, s.model, s.account_label
		FROM harness_sessions s
		WHERE s.tenant_id = $1::uuid AND s.id = $2::uuid
			AND ((SELECT aeon_visible_all()) OR s.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))`, tenantID, sessionID).Scan(&head.id, &ticket, &head.harness, &head.model, &head.account)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionHead{}, &workorders.Error{Status: http.StatusNotFound, Message: "not found"}
	}
	if err != nil {
		return sessionHead{}, err
	}
	if ticket == nil || *ticket == "" {
		return sessionHead{}, &workorders.Error{Status: http.StatusConflict, Message: "this session is not attached to a ticket"}
	}
	var visible bool
	err = tx.QueryRow(ctx, `SELECT true FROM nodes n
		WHERE n.tenant_id = $1::uuid AND n.id = $2::uuid AND n.deleted_at IS NULL
			AND ((SELECT aeon_visible_all()) OR n.project_id = ANY ((SELECT aeon_visible_projects())::uuid[]))`, tenantID, *ticket).Scan(&visible)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionHead{}, &workorders.Error{Status: http.StatusNotFound, Message: "not found"}
	}
	if err != nil {
		return sessionHead{}, err
	}
	head.ticket = *ticket
	return head, nil
}

func myVote(ctx context.Context, tx pgx.Tx, p tenant.Principal, sessionID string) (*storedVote, error) {
	var vote storedVote
	err := tx.QueryRow(ctx, `SELECT score, tags, comment, harness, model, account_label FROM agent_delivery_votes
		WHERE tenant_id = $1 AND session_id = $2 AND voter_principal_id = $3`, p.TenantID, sessionID, p.ID).Scan(
		&vote.score, &vote.tags, &vote.comment, &vote.harness, &vote.model, &vote.account)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if vote.tags == nil {
		vote.tags = []string{}
	}
	return &vote, nil
}

func upsert(ctx context.Context, tx pgx.Tx, p tenant.Principal, head sessionHead, score *int, tags []string, comment string) error {
	_, err := tx.Exec(ctx, `INSERT INTO agent_delivery_votes
		(tenant_id, session_id, ticket_node_id, voter_principal_id, score, tags, comment, harness, model, account_label)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (tenant_id, session_id, voter_principal_id) DO UPDATE SET
			ticket_node_id = EXCLUDED.ticket_node_id,
			score = EXCLUDED.score,
			tags = EXCLUDED.tags,
			comment = EXCLUDED.comment,
			harness = EXCLUDED.harness,
			model = EXCLUDED.model,
			account_label = EXCLUDED.account_label`,
		p.TenantID, head.id, head.ticket, p.ID, score, tags, comment, head.harness, head.model, head.account)
	return err
}

func assemble(ctx context.Context, tx pgx.Tx, p tenant.Principal, heads []sessionHead) ([]Rating, error) {
	if heads == nil {
		heads = []sessionHead{}
	}
	out := make([]Rating, len(heads))
	if len(heads) == 0 {
		return out, nil
	}
	ids := make([]string, len(heads))
	for i, head := range heads {
		ids[i] = head.id
		out[i] = Rating{
			SessionID: head.id, TicketNodeID: head.ticket, Harness: head.harness,
			Model: head.model, AccountLabel: head.account,
			Signals: Signals{},
		}
	}
	votes, err := loadVotes(ctx, tx, p.TenantID, ids)
	if err != nil {
		return nil, err
	}
	signals, err := countTicketEventSignals(ctx, tx, p.TenantID, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		rows := votes[out[i].SessionID]
		sum, scored := 0, 0
		for _, vote := range rows {
			if vote.score != nil {
				sum += *vote.score
				scored++
			}
			if vote.voter == p.ID {
				tags := vote.tags
				if tags == nil {
					tags = []string{}
				}
				out[i].Mine = &Vote{Score: vote.score, Tags: tags, Comment: vote.comment, UpdatedAt: vote.updated.UTC()}
			}
		}
		out[i].Votes = len(rows)
		out[i].Average = FormatAverage(sum, scored)
		if signal, ok := signals[out[i].SessionID]; ok {
			out[i].Signals = signal
		}
	}
	return out, nil
}

func loadVotes(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) (map[string][]storedVote, error) {
	rows, err := tx.Query(ctx, `SELECT session_id::text, voter_principal_id::text, score, tags, comment, updated_at
		FROM agent_delivery_votes WHERE tenant_id = $1 AND session_id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]storedVote{}
	for rows.Next() {
		var sessionID string
		var vote storedVote
		if err := rows.Scan(&sessionID, &vote.voter, &vote.score, &vote.tags, &vote.comment, &vote.updated); err != nil {
			return nil, err
		}
		if vote.tags == nil {
			vote.tags = []string{}
		}
		out[sessionID] = append(out[sessionID], vote)
	}
	return out, rows.Err()
}
