// SPDX-License-Identifier: AGPL-3.0-only

// Package decisiondesk projects existing sources; it never owns their decisions.
package decisiondesk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NearExpiry is the proposed warning window for the existing phone worker's
// 30-second scan. Scheduler integration must retain quiet hours and escalation.
const NearExpiry = 15 * time.Minute

var ErrPersonStepCoverage = errors.New("desk person-step coverage exceeds 1000 candidate tickets")

var ErrCoverage = errors.New("desk project coverage exceeds 1000 projects")

type Item struct {
	PersonStep *workorders.PersonStep `json:"person_step,omitempty"`
	ID         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	ProjectID  string                 `json:"project_id,omitempty"`
	Revision   int64                  `json:"revision"`
	Title      string                 `json:"title"`
	CreatedAt  time.Time              `json:"created_at"`
	ExpiresAt  *time.Time             `json:"expires_at,omitempty"`
	Held       bool                   `json:"held"`
	CanDecide  bool                   `json:"can_decide"`
	Href       string                 `json:"href"`
	Source     string                 `json:"source"`
	Bucket     int                    `json:"-"`
	OrderAt    time.Time              `json:"-"`
}

// PushEligible is evaluated again just before dispatch. Answered questions,
// grace revisions, settled requests and expired approvals never enter Items.
func (i Item) PushEligible(now time.Time) bool {
	if i.ExpiresAt != nil && !i.ExpiresAt.After(now) {
		return false
	}
	switch i.Kind {
	case "question", "action_request":
		return i.Held
	case "approval":
		return i.Held || i.ExpiresAt != nil && !i.ExpiresAt.After(now.Add(NearExpiry))
	}
	// Doctrine uses its existing server toast claims. It has no verified held
	// work adapter, so it cannot also produce a desk push for that proposal.
	return false
}

type Counts struct {
	Open    int            `json:"open"`
	Held    int            `json:"held"`
	Chores  int            `json:"chores"`
	Lessons map[string]int `json:"lessons,omitempty"`
}

type Page struct {
	Items      []Item    `json:"items"`
	Counts     Counts    `json:"counts"`
	HasMore    bool      `json:"has_more"`
	NextCursor string    `json:"next_cursor,omitempty"`
	AsOf       time.Time `json:"as_of"`
}

type cursor struct {
	Bucket int       `json:"bucket"`
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
}

type Module struct {
	pool           *pgxpool.Pool
	routineRuntime recurrences.ExecutionRuntimeReader
}

func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool} }

func (m *Module) WithRoutineExecutionRuntime(reader recurrences.ExecutionRuntimeReader) *Module {
	m.routineRuntime = reader
	return m
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/decision-desk/projection", m.projection)
}

func (m *Module) projection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
		return
	}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || r.Header.Get("Authorization") != "" {
		httpapi.WriteError(w, 403, "a signed-in person is required")
		return
	}
	limit := 50
	if r.URL.Query().Has("limit") {
		n, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || n < 1 || n > 100 {
			httpapi.WriteError(w, 400, "invalid limit")
			return
		}
		limit = n
	}
	after, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpapi.WriteError(w, 400, "invalid cursor")
		return
	}
	page, err := m.Read(r.Context(), p, limit, after)
	if errors.Is(err, ErrCoverage) || errors.Is(err, ErrPersonStepCoverage) || errors.Is(err, recurrences.ErrConsentCoverage) || errors.Is(err, knowledge.ErrDeskLessonCoverage) {
		httpapi.WriteError(w, 422, err.Error())
		return
	}
	if err != nil {
		httpapi.WriteError(w, 500, "decision desk could not be read")
		return
	}
	httpapi.WriteJSON(w, 200, page)
}

func decodeCursor(raw string) (*cursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 512 {
		return nil, errors.New("cursor too large")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Bucket < 0 || c.Bucket > 2 || c.At.IsZero() || !validID(c.ID) || !validKind(c.Kind) {
		return nil, errors.New("invalid cursor")
	}
	return &c, nil
}

func validID(id string) bool {
	var uuid pgtype.UUID
	return len(id) == 36 && uuid.Scan(id) == nil && uuid.Valid
}

func validKind(kind string) bool {
	return kind == "question" || kind == "approval" || kind == "action_request" || kind == "doctrine" || kind == "key_trim" || kind == "stepup" || kind == "tier_request" || kind == "account_matrix" || kind == "routine_consent" || kind == "lesson"
}

func (m *Module) Read(ctx context.Context, p tenant.Principal, limit int, after *cursor) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	page := Page{Items: []Item{}}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || limit < 1 || limit > 100 {
		return page, errors.New("invalid desk reader")
	}
	// Source-specific permissions below are authoritative, including callers
	// whose questions.read does not include generic nodes.read.
	err := db.InTenantReadSnapshot(db.AllProjects(ctx, "decision desk: every source filtered by current recipient permissions"), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		var err error
		page, err = ReadTx(ctx, tx, p, limit, after, m.routineRuntime)
		return err
	})
	return page, err
}
