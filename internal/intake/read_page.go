// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type historyPosition struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

type turnPosition struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Ordinal  int    `json:"ordinal"`
}

type historyCursor struct {
	Version     int              `json:"v"`
	Tenant      string           `json:"tenant"`
	Project     string           `json:"project"`
	Node        string           `json:"node"`
	Source      *historyPosition `json:"source,omitempty"`
	Turn        *turnPosition    `json:"turn,omitempty"`
	Draft       *historyPosition `json:"draft,omitempty"`
	SourcesDone bool             `json:"sources_done"`
	TurnsDone   bool             `json:"turns_done"`
	DraftsDone  bool             `json:"drafts_done"`
}

type historyPage struct {
	limit  int
	cursor historyCursor
}

func historyBounds(r *http.Request, tenantID, projectID, nodeID string) (*historyPage, error) {
	q := r.URL.Query()
	page := &historyPage{limit: maxSnapshotRows, cursor: historyCursor{Version: 1, Tenant: tenantID, Project: projectID, Node: nodeID}}
	for _, name := range []string{"limit", "after", "node_id"} {
		if len(q[name]) > 1 {
			return nil, fail(http.StatusBadRequest, "repeated history selector")
		}
	}
	if q.Has("limit") {
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > maxSnapshotRows {
			return nil, fail(http.StatusBadRequest, "invalid history limit")
		}
		page.limit = limit
	}
	if raw := q.Get("after"); q.Has("after") {
		if raw == "" || len(raw) > 2048 {
			return nil, fail(http.StatusBadRequest, "invalid history cursor")
		}
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &page.cursor) != nil {
			return nil, fail(http.StatusBadRequest, "invalid history cursor")
		}
		c := page.cursor
		validPosition := func(p *historyPosition) bool {
			return p == nil || (uuidPattern.MatchString(p.ID) && !p.At.IsZero())
		}
		if c.Version != 1 || c.Tenant != tenantID || c.Project != projectID || c.Node != nodeID ||
			!validPosition(c.Source) || !validPosition(c.Draft) ||
			(c.Turn != nil && (!uuidPattern.MatchString(c.Turn.ID) || !uuidPattern.MatchString(c.Turn.SourceID) || c.Turn.Ordinal < 0 || c.Turn.Ordinal > 2147483647)) ||
			(!c.SourcesDone && c.Source == nil) || (!c.TurnsDone && c.Turn == nil) || (!c.DraftsDone && c.Draft == nil) {
			return nil, fail(http.StatusBadRequest, "invalid history cursor")
		}
	}
	if nodeID != "" {
		page.cursor.SourcesDone = true
		page.cursor.TurnsDone = true
	}
	return page, nil
}

func (p *historyPage) next() string {
	if p.cursor.SourcesDone && p.cursor.TurnsDone && p.cursor.DraftsDone {
		return ""
	}
	b, _ := json.Marshal(p.cursor)
	return base64.RawURLEncoding.EncodeToString(b)
}

func loadSnapshotPage(ctx context.Context, tx pgx.Tx, projectID, nodeID string, page *historyPage) (snapshot, error) {
	out := snapshot{Sources: []sourceView{}, Turns: []turnView{}, Drafts: []draftView{}}
	if !page.cursor.SourcesDone {
		q := `SELECT id::text, project_node_id::text, kind, label, locator, file_id::text,
			content_sha256, idempotency_key, created_at FROM intake_sources WHERE project_node_id=$1::uuid`
		args := []any{projectID}
		if pos := page.cursor.Source; pos != nil {
			q += ` AND (created_at,id)>($2::timestamptz,$3::uuid)`
			args = append(args, pos.At, pos.ID)
		}
		args = append(args, page.limit+1)
		rows, err := tx.Query(ctx, q+` ORDER BY created_at,id LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return snapshot{}, err
		}
		page.cursor.SourcesDone = true
		for rows.Next() {
			if len(out.Sources) == page.limit {
				page.cursor.SourcesDone = false
				break
			}
			view, err := scanSource(rows)
			if err != nil {
				rows.Close()
				return snapshot{}, err
			}
			out.Sources = append(out.Sources, view)
			page.cursor.Source = &historyPosition{ID: view.ID, At: view.CreatedAt}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return snapshot{}, err
		}
	}
	if !page.cursor.TurnsDone {
		q := `SELECT id::text, source_id::text, ordinal, speaker, speaker_principal_id::text, body, idempotency_key, created_at
			FROM intake_transcript_turns WHERE project_node_id=$1::uuid`
		args := []any{projectID}
		if pos := page.cursor.Turn; pos != nil {
			q += ` AND (source_id,ordinal,id)>($2::uuid,$3::integer,$4::uuid)`
			args = append(args, pos.SourceID, pos.Ordinal, pos.ID)
		}
		args = append(args, page.limit+1)
		rows, err := tx.Query(ctx, q+` ORDER BY source_id,ordinal,id LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return snapshot{}, err
		}
		page.cursor.TurnsDone = true
		for rows.Next() {
			if len(out.Turns) == page.limit {
				page.cursor.TurnsDone = false
				break
			}
			view, err := scanTurn(rows)
			if err != nil {
				rows.Close()
				return snapshot{}, err
			}
			out.Turns = append(out.Turns, view)
			page.cursor.Turn = &turnPosition{ID: view.ID, SourceID: view.SourceID, Ordinal: view.Ordinal}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return snapshot{}, err
		}
	}
	if !page.cursor.DraftsDone {
		var nodes []string
		if nodeID != "" {
			nodes = []string{nodeID}
		}
		drafts, err := loadDraftsFiltered(ctx, tx, projectID, "", "", nodes, page)
		if err != nil {
			return snapshot{}, err
		}
		for _, row := range drafts {
			view, err := presentDraft(ctx, tx, projectID, row)
			if err != nil {
				return snapshot{}, err
			}
			out.Drafts = append(out.Drafts, view)
		}
	}
	return out, nil
}
