// SPDX-License-Identifier: AGPL-3.0-only

package outcomes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves the outcome list and the coordinator record route.
type Module struct {
	pool *pgxpool.Pool
}

// New binds the module to pool.
func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool} }

// Mount registers GET and POST /api/outcomes.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/outcomes", m.list)
	mux.HandleFunc("POST /api/outcomes", m.record)
}

type outcome struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	TicketNodeID     string          `json:"ticket_node_id"`
	TicketKey        string          `json:"ticket_key"`
	ProjectID        string          `json:"project_id"`
	SessionID        *string         `json:"session_id"`
	RulesVersion     *string         `json:"rules_version"`
	ReleaseNodeID    *string         `json:"release_node_id"`
	ReleaseKey       *string         `json:"release_key"`
	ReleaseTitle     *string         `json:"release_title"`
	Source           string          `json:"source"`
	Payload          json.RawMessage `json:"payload"`
	ActorPrincipalID string          `json:"actor_principal_id"`
	RecordedAt       time.Time       `json:"recorded_at"`
	IdempotencyKey   string          `json:"idempotency_key"`
}

type recordBody struct {
	Kind         string          `json:"kind"`
	Ticket       string          `json:"ticket"`
	SessionID    *string         `json:"session_id"`
	RulesVersion *string         `json:"rules_version"`
	Payload      json.RawMessage `json:"payload"`
}

const outcomeSelect = `
SELECT o.id::text, o.kind, o.ticket_node_id::text, t.key, o.project_id::text,
       o.session_id::text, o.rules_version, o.release_node_id::text, rel.key, rel.title,
       o.source, o.payload, o.actor_principal_id::text, o.recorded_at, o.idempotency_key
FROM outcome_events o
JOIN nodes t ON t.tenant_id = o.tenant_id AND t.id = o.ticket_node_id
LEFT JOIN nodes rel ON rel.tenant_id = o.tenant_id AND rel.id = o.release_node_id
`

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ctx := authz.BindPool(r.Context(), m.pool)
	// Any project grant may open the list. Row-level security only checks node
	// visibility, so each returned project is authorized on its own below.
	if err := authz.Require(ctx, "outcome.read", authz.Scope{AnyProject: true}); err != nil {
		writeErr(w, err)
		return
	}
	query := r.URL.Query()
	ticket := strings.TrimSpace(query.Get("ticket_node_id"))
	session := strings.TrimSpace(query.Get("session_id"))
	_, rulesSet := query["rules_version"]
	rules, err := cleanRules(query.Get("rules_version"), rulesSet)
	if err != nil {
		writeErr(w, err)
		return
	}
	if session != "" && !validUUID(session) {
		writeErr(w, invalid("session_id must be a UUID"))
		return
	}
	limit, err := limitOf(query.Get("limit"), query.Has("limit"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if ticket == "" && session == "" && rules == "" {
		writeErr(w, invalid("filter by a ticket, session or rules version"))
		return
	}
	var items []outcome
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		check, err := authz.ProjectsTx(r.Context(), tx, p)
		if err != nil {
			return err
		}
		ticketID := ""
		if ticket != "" {
			node, err := resolveTicket(r.Context(), tx, ticket)
			if err != nil {
				return err
			}
			if !check("outcome.read", node.projectID) {
				items = []outcome{}
				return nil
			}
			ticketID = node.id
		}
		where := []string{"TRUE"}
		args := []any{}
		if ticketID != "" {
			args = append(args, ticketID)
			where = append(where, "o.ticket_node_id = $"+itoa(len(args))+"::uuid")
		}
		if session != "" {
			args = append(args, strings.ToLower(session))
			where = append(where, "o.session_id = $"+itoa(len(args))+"::uuid")
		}
		if rules != "" {
			args = append(args, rules)
			where = append(where, "o.rules_version = $"+itoa(len(args)))
		}
		if !check("outcome.read", "") {
			allowed, err := readableOutcomeProjects(r.Context(), tx, where, args, check)
			if err != nil {
				return err
			}
			if len(allowed) == 0 {
				items = []outcome{}
				return nil
			}
			args = append(args, allowed)
			where = append(where, "o.project_id = ANY($"+itoa(len(args))+"::uuid[])")
		}
		args = append(args, limit)
		rows, err := tx.Query(r.Context(), outcomeSelect+`WHERE `+strings.Join(where, " AND ")+
			` ORDER BY o.recorded_at DESC, o.id DESC LIMIT $`+itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items = []outcome{}
		for rows.Next() {
			item, err := scanOutcome(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"outcomes": items})
}

// readableOutcomeProjects keeps projects in this filter where the caller holds
// outcome.read. A workspace grant is decided by the caller before this query.
func readableOutcomeProjects(ctx context.Context, tx pgx.Tx, where []string, args []any, check authz.ProjectCheck) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT o.project_id::text FROM outcome_events o WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if check("outcome.read", id) {
			allowed = append(allowed, id)
		}
	}
	return allowed, rows.Err()
}

func (m *Module) record(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	key, err := cleanIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	body, err := readRecord(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	kind := strings.TrimSpace(body.Kind)
	if !manualKinds[kind] {
		writeErr(w, invalid("kind must be review_verdict, fix_round, ci_result or revert"))
		return
	}
	ticket := strings.TrimSpace(body.Ticket)
	if ticket == "" {
		writeErr(w, invalid("ticket is required"))
		return
	}
	session, err := optionalUUID("session_id", body.SessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	rulesPresent := body.RulesVersion != nil
	rulesValue := ""
	if rulesPresent {
		rulesValue = *body.RulesVersion
	}
	rules, err := cleanRules(rulesValue, rulesPresent)
	if err != nil {
		writeErr(w, err)
		return
	}
	payload, err := canonicalPayload(kind, body.Payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx := authz.BindPool(r.Context(), m.pool)
	var item outcome
	created := false
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		node, err := resolveTicket(r.Context(), tx, ticket)
		if err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "outcome.write", authz.Scope{ProjectID: node.projectID}); err != nil {
			return err
		}
		if session != "" {
			var sessionProject string
			err := tx.QueryRow(r.Context(), `SELECT project_id::text FROM harness_sessions WHERE id=$1::uuid`, session).Scan(&sessionProject)
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("session not found")
			}
			if err != nil {
				return err
			}
			if sessionProject != node.projectID {
				return invalid("session is in another project")
			}
		}
		digest := requestDigest(kind, node.id, session, rules, payload)
		var id string
		err = tx.QueryRow(r.Context(), `
			INSERT INTO outcome_events (
				tenant_id, kind, project_id, ticket_node_id, session_id, rules_version,
				idempotency_key, actor_principal_id, source, payload, request_digest
			) VALUES (
				NULLIF(current_setting('aeon.tenant_id', true), '')::uuid,
				$1, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, ''), $6, $7::uuid, 'recorded', $8::jsonb, $9
			)
			ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
			RETURNING id::text`,
			kind, node.projectID, node.id, nilUUID(session), rules, key, p.ID, string(payload), digest).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var existing []byte
			err = tx.QueryRow(r.Context(), `SELECT id::text, request_digest FROM outcome_events WHERE idempotency_key=$1`, key).Scan(&id, &existing)
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("outcome conflict did not return the stored row")
			}
			if err != nil {
				return err
			}
			if !bytes.Equal(existing, digest) {
				return &fail{status: http.StatusConflict, msg: "idempotency key already used"}
			}
		} else if err != nil {
			return err
		} else {
			created = true
		}
		item, err = scanOutcome(tx.QueryRow(r.Context(), outcomeSelect+`WHERE o.id=$1::uuid`, id))
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, item)
}

type ticketRef struct {
	id, projectID, key string
}

func resolveTicket(ctx context.Context, tx pgx.Tx, ref string) (ticketRef, error) {
	rows, err := tx.Query(ctx, `
		SELECT n.id::text, coalesce(n.project_id::text, ''), n.key, k.slug
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.deleted_at IS NULL AND (n.id::text = $1 OR n.key = $1)
		LIMIT 2`, ref)
	if err != nil {
		return ticketRef{}, err
	}
	defer rows.Close()
	var found []ticketRef
	var slug string
	for rows.Next() {
		var item ticketRef
		var kind string
		if err := rows.Scan(&item.id, &item.projectID, &item.key, &kind); err != nil {
			return ticketRef{}, err
		}
		slug = kind
		found = append(found, item)
	}
	if err := rows.Err(); err != nil {
		return ticketRef{}, err
	}
	if len(found) == 0 {
		return ticketRef{}, &fail{status: http.StatusNotFound, msg: "not found"}
	}
	if len(found) > 1 {
		return ticketRef{}, invalid("ticket is ambiguous")
	}
	if slug != "ticket" {
		return ticketRef{}, invalid("outcomes are recorded on tickets")
	}
	if found[0].projectID == "" {
		return ticketRef{}, invalid("ticket has no project")
	}
	return found[0], nil
}

func readRecord(w http.ResponseWriter, r *http.Request) (recordBody, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body recordBody
	if err := dec.Decode(&body); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			return recordBody{}, invalid("request is too large")
		}
		if strings.Contains(err.Error(), "unknown field") {
			return recordBody{}, invalid("unknown field")
		}
		return recordBody{}, invalid("invalid outcome")
	}
	if err := oneValue(dec); err != nil {
		return recordBody{}, invalid("invalid outcome")
	}
	return body, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOutcome(row rowScanner) (outcome, error) {
	var item outcome
	var payload []byte
	var session, rules, releaseID, releaseKey, releaseTitle *string
	err := row.Scan(
		&item.ID, &item.Kind, &item.TicketNodeID, &item.TicketKey, &item.ProjectID,
		&session, &rules, &releaseID, &releaseKey, &releaseTitle,
		&item.Source, &payload, &item.ActorPrincipalID, &item.RecordedAt, &item.IdempotencyKey,
	)
	if err != nil {
		return outcome{}, err
	}
	item.SessionID = session
	item.RulesVersion = blankToNil(rules)
	item.ReleaseNodeID = releaseID
	item.ReleaseKey = blankToNil(releaseKey)
	item.ReleaseTitle = blankToNil(releaseTitle)
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	item.Payload = payload
	return item, nil
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return tenant.Principal{}, false
	}
	return p, true
}

func writeErr(w http.ResponseWriter, err error) {
	var f *fail
	if errors.As(err, &f) {
		httpapi.WriteError(w, f.status, f.msg)
		return
	}
	if errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, http.StatusForbidden, "permission denied")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	slog.Error("outcomes", "err", err)
	httpapi.WriteError(w, http.StatusInternalServerError, "internal error")
}

func optionalUUID(field string, value *string) (string, error) {
	if value == nil {
		return "", nil
	}
	text := strings.ToLower(strings.TrimSpace(*value))
	if text == "" {
		return "", invalid(field + " must be a UUID")
	}
	if !validUUID(text) {
		return "", invalid(field + " must be a UUID")
	}
	return text, nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}

func nilUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func blankToNil(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func limitOf(raw string, present bool) (int, error) {
	if !present || strings.TrimSpace(raw) == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLimit {
		return 0, invalid("limit must be from 1 to 100")
	}
	return n, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
