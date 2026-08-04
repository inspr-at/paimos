// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package handlers

// Voice-intake workbench sessions (PAI-704). A session captures a spoken/typed
// idea as an append-only event log that is simultaneously the time-travel
// history, the SSE replay source, and the scrub timeline.
//
// Invariants (see docs/THREAT_MODEL.md):
//   INV-INTAKE-01 — sessions are owner-or-admin; non-owner access → 404.
//   INV-INTAKE-02 — transcript/spec/summary bodies live only in intake_events;
//                   they never reach stdout logs, audit lines, mutation_log,
//                   or ai_calls.
//
// These routes are a UI-internal surface: intentionally NOT part of
// /api/openapi.json (see openapi_coverage_test.go — documented→registered
// only). Session mutations are deliberately outside the undo/mutation_log
// scope: artifacts here are pre-issue drafts containing model output, which
// mutation_log payloads must never carry (docs/UNDO_SPEC.md); the intake
// timeline itself is the undo surface for this feature.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	intakeChunkMaxBytes      = 8 * 1024
	intakeTranscriptMaxBytes = 256 * 1024
	intakeSpecMaxBytes       = 48 * 1024
	intakeMaxEventsPerSess   = 2000
	intakeCheckpointMaxLabel = 120
)

// Artifact kinds whose latest snapshot at a given seq defines the session
// state. transcript_chunk/checkpoint/restore/language/status are excluded:
// they are inputs or markers, not materialized artifacts.
var intakeArtifactKinds = []string{"spec", "summaries", "ticket_preview", "project_match", "impacts"}

// intakeEventKindAllowed mirrors the intake_events.kind CHECK constraint.
func intakeEventKindAllowed(kind string) bool {
	switch kind {
	case "transcript_chunk", "spec", "summaries", "ticket_preview",
		"project_match", "impacts", "checkpoint", "restore", "language", "status":
		return true
	}
	return false
}

type intakeSession struct {
	ID                int64   `json:"id"`
	UserID            int64   `json:"user_id"`
	Status            string  `json:"status"`
	Language          string  `json:"language"`
	DetectedProjectID *int64  `json:"detected_project_id"`
	DetectedScore     int     `json:"detected_score"`
	PinnedProjectID   *int64  `json:"pinned_project_id"`
	CreatedIssueID    *int64  `json:"created_issue_id"`
	TranscriptBytes   int     `json:"transcript_bytes"`
	Rev               int64   `json:"rev"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	CompletedAt       *string `json:"completed_at"`
}

type intakeEventMeta struct {
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Label     string `json:"label,omitempty"`
	Bytes     int    `json:"bytes"`
	CreatedAt string `json:"created_at"`
}

type intakeCheckpoint struct {
	Seq       int64  `json:"seq"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

type intakeState struct {
	AtSeq      int64                      `json:"at_seq"`
	Transcript string                     `json:"transcript"`
	Artifacts  map[string]json.RawMessage `json:"artifacts"`
}

const intakeSessionCols = `id, user_id, status, language, detected_project_id, detected_score,
	pinned_project_id, created_issue_id, transcript_bytes, rev, created_at, updated_at, completed_at`

func scanIntakeSession(row interface{ Scan(...any) error }) (*intakeSession, error) {
	var s intakeSession
	var detected, pinned, created sql.NullInt64
	var completed sql.NullString
	err := row.Scan(&s.ID, &s.UserID, &s.Status, &s.Language, &detected, &s.DetectedScore,
		&pinned, &created, &s.TranscriptBytes, &s.Rev, &s.CreatedAt, &s.UpdatedAt, &completed)
	if err != nil {
		return nil, err
	}
	if detected.Valid {
		s.DetectedProjectID = &detected.Int64
	}
	if pinned.Valid {
		s.PinnedProjectID = &pinned.Int64
	}
	if created.Valid {
		s.CreatedIssueID = &created.Int64
	}
	if completed.Valid {
		s.CompletedAt = &completed.String
	}
	return &s, nil
}

func loadIntakeSession(ctx context.Context, id int64) (*intakeSession, error) {
	row := db.DB.QueryRowContext(ctx,
		`SELECT `+intakeSessionCols+` FROM intake_sessions WHERE id = ?`, id)
	return scanIntakeSession(row)
}

// requireIntakeSession resolves {id} and enforces INV-INTAKE-01: the session
// must exist AND belong to the caller (admins pass). Both "does not exist"
// and "not yours" answer 404 so the endpoint is not an existence oracle.
func requireIntakeSession(w http.ResponseWriter, r *http.Request) (*intakeSession, *models.User, bool) {
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "unauthenticated", http.StatusUnauthorized)
		return nil, nil, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		jsonError(w, "intake session not found", http.StatusNotFound)
		return nil, nil, false
	}
	s, err := loadIntakeSession(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "intake session not found", http.StatusNotFound)
		return nil, nil, false
	}
	if handleDBError(w, err, "intake session") {
		return nil, nil, false
	}
	if s.UserID != user.ID && !auth.IsAdmin(user) {
		jsonError(w, "intake session not found", http.StatusNotFound)
		return nil, nil, false
	}
	return s, user, true
}

// appendIntakeEventTx bumps the session rev and inserts one event inside the
// caller's transaction. Returns the allocated per-session seq.
func appendIntakeEventTx(ctx context.Context, tx *sql.Tx, sessionID int64, kind, source, label, payloadJSON string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`UPDATE intake_sessions SET rev = rev + 1, updated_at = datetime('now') WHERE id = ?`, sessionID); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT rev FROM intake_sessions WHERE id = ?`, sessionID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO intake_events (session_id, seq, kind, source, label, payload_json)
		 VALUES (?, ?, ?, ?, ?, ?)`, sessionID, seq, kind, source, label, payloadJSON); err != nil {
		return 0, err
	}
	return seq, nil
}

func intakeEventCount(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sessionID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM intake_events WHERE session_id = ?`, sessionID).Scan(&n)
	return n, err
}

// CreateIntakeSession handles POST /api/intake/sessions.
func CreateIntakeSession(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	var body struct {
		Language string `json:"language"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty body = defaults
	}
	lang := strings.TrimSpace(body.Language)
	if lang == "" {
		lang = "en"
	}
	if lang != "en" && lang != "de" {
		jsonError(w, "language must be en or de", http.StatusBadRequest)
		return
	}
	res, err := db.DB.ExecContext(r.Context(),
		`INSERT INTO intake_sessions (user_id, language) VALUES (?, ?)`, user.ID, lang)
	if handleDBError(w, err, "intake session") {
		return
	}
	id, _ := res.LastInsertId()
	s, err := loadIntakeSession(r.Context(), id)
	if handleDBError(w, err, "intake session") {
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, s)
}

// ListIntakeSessions handles GET /api/intake/sessions — the caller's own
// sessions only (admins included: the list is a personal resume surface).
func ListIntakeSessions(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	rows, err := db.DB.QueryContext(r.Context(),
		`SELECT `+intakeSessionCols+` FROM intake_sessions
		 WHERE user_id = ? ORDER BY updated_at DESC LIMIT 20`, user.ID)
	if handleDBError(w, err, "intake sessions") {
		return
	}
	defer rows.Close()
	out := []*intakeSession{}
	for rows.Next() {
		s, err := scanIntakeSession(rows)
		if handleDBError(w, err, "intake sessions") {
			return
		}
		out = append(out, s)
	}
	if handleDBError(w, rows.Err(), "intake sessions") {
		return
	}
	jsonOK(w, out)
}

// GetIntakeSession handles GET /api/intake/sessions/{id} — session row plus
// head state and checkpoint list, enough for a client to hydrate in one call.
func GetIntakeSession(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	state, err := intakeStateAt(r.Context(), s.ID, s.Rev)
	if handleDBError(w, err, "intake state") {
		return
	}
	checkpoints, err := listIntakeCheckpoints(r.Context(), s.ID)
	if handleDBError(w, err, "intake checkpoints") {
		return
	}
	jsonOK(w, map[string]any{
		"session":     s,
		"state":       state,
		"checkpoints": checkpoints,
	})
}

// AbandonIntakeSession handles DELETE /api/intake/sessions/{id}.
func AbandonIntakeSession(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	if s.Status == "active" {
		_, err := db.DB.ExecContext(r.Context(),
			`UPDATE intake_sessions SET status='abandoned', updated_at=datetime('now') WHERE id=?`, s.ID)
		if handleDBError(w, err, "intake session") {
			return
		}
		publishIntakeSessionState(r.Context(), s.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// IngestIntakeTranscript handles POST /api/intake/sessions/{id}/transcript.
// Capture must always work: this endpoint never fails because of AI state.
func IngestIntakeTranscript(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	if s.Status != "active" {
		jsonError(w, "intake session is not active", http.StatusConflict)
		return
	}
	var body struct {
		Text      string `json:"text"`
		ClientSeq *int64 `json:"client_seq"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	text := body.Text
	if strings.TrimSpace(text) == "" {
		jsonError(w, "text is required", http.StatusBadRequest)
		return
	}
	if len(text) > intakeChunkMaxBytes {
		jsonError(w, "transcript chunk exceeds 8 KiB", http.StatusRequestEntityTooLarge)
		return
	}
	if s.TranscriptBytes+len(text) > intakeTranscriptMaxBytes {
		jsonError(w, "session transcript is full — create the issue or start a new session", http.StatusConflict)
		return
	}

	ctx := r.Context()
	// Retry dedupe: a chunk with a client_seq we already stored answers with
	// the original seq and appends nothing.
	if body.ClientSeq != nil {
		var seq int64
		err := db.DB.QueryRowContext(ctx,
			`SELECT seq FROM intake_events
			 WHERE session_id = ? AND kind = 'transcript_chunk'
			   AND json_extract(payload_json, '$.client_seq') = ?
			 ORDER BY seq DESC LIMIT 1`, s.ID, *body.ClientSeq).Scan(&seq)
		if err == nil {
			jsonOK(w, map[string]any{"seq": seq, "transcript_bytes": s.TranscriptBytes, "deduped": true})
			return
		}
		if !errors.Is(err, sql.ErrNoRows) && handleDBError(w, err, "intake transcript") {
			return
		}
	}

	count, err := intakeEventCount(ctx, db.DB, s.ID)
	if handleDBError(w, err, "intake transcript") {
		return
	}
	if count >= intakeMaxEventsPerSess {
		jsonError(w, "session event limit reached — create the issue or start a new session", http.StatusConflict)
		return
	}

	payload := map[string]any{"text": text}
	if body.ClientSeq != nil {
		payload["client_seq"] = *body.ClientSeq
	}
	raw, _ := json.Marshal(payload)

	tx, err := db.DB.BeginTx(ctx, nil)
	if handleDBError(w, err, "intake transcript") {
		return
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(ctx, tx, s.ID, "transcript_chunk", "user", "", string(raw))
	if handleDBError(w, err, "intake transcript") {
		return
	}
	sep := ""
	if s.TranscriptBytes > 0 {
		sep = "\n"
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE intake_sessions
		 SET transcript = transcript || ? || ?, transcript_bytes = transcript_bytes + ?
		 WHERE id = ?`, sep, text, len(sep)+len(text), s.ID); err != nil {
		if handleDBError(w, err, "intake transcript") {
			return
		}
	}
	if handleDBError(w, tx.Commit(), "intake transcript") {
		return
	}
	publishIntakeEvent(ctx, s.ID, seq)
	jsonOK(w, map[string]any{"seq": seq, "transcript_bytes": s.TranscriptBytes + len(sep) + len(text)})
}

// PatchIntakeSession handles PATCH /api/intake/sessions/{id}:
// language toggle, project pin/unpin, and manual spec edits.
func PatchIntakeSession(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	if s.Status != "active" {
		jsonError(w, "intake session is not active", http.StatusConflict)
		return
	}
	var body struct {
		Language        *string `json:"language"`
		PinnedProjectID *int64  `json:"pinned_project_id"` // 0 clears the pin
		SpecMarkdown    *string `json:"spec_markdown"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	if body.Language != nil {
		lang := strings.TrimSpace(*body.Language)
		if lang != "en" && lang != "de" {
			jsonError(w, "language must be en or de", http.StatusBadRequest)
			return
		}
		if lang != s.Language {
			raw, _ := json.Marshal(map[string]string{"language": lang})
			if !appendIntakeEventHTTP(w, ctx, s.ID, "language", "user", "", string(raw)) {
				return
			}
			if _, err := db.DB.ExecContext(ctx,
				`UPDATE intake_sessions SET language=? WHERE id=?`, lang, s.ID); handleDBError(w, err, "intake session") {
				return
			}
		}
	}

	if body.PinnedProjectID != nil {
		pid := *body.PinnedProjectID
		if pid == 0 {
			if _, err := db.DB.ExecContext(ctx,
				`UPDATE intake_sessions SET pinned_project_id=NULL, updated_at=datetime('now') WHERE id=?`, s.ID); handleDBError(w, err, "intake session") {
				return
			}
		} else {
			// The pin must be a project the caller can view; reject in the
			// 404 shape so this cannot probe for project existence.
			if !auth.CanViewProject(r, pid) {
				jsonError(w, "project not found", http.StatusNotFound)
				return
			}
			if _, err := db.DB.ExecContext(ctx,
				`UPDATE intake_sessions SET pinned_project_id=?, updated_at=datetime('now') WHERE id=?`, pid, s.ID); handleDBError(w, err, "intake session") {
				return
			}
		}
	}

	if body.SpecMarkdown != nil {
		md := *body.SpecMarkdown
		if len(md) > intakeSpecMaxBytes {
			jsonError(w, "spec exceeds 48 KiB", http.StatusRequestEntityTooLarge)
			return
		}
		count, err := intakeEventCount(ctx, db.DB, s.ID)
		if handleDBError(w, err, "intake session") {
			return
		}
		if count >= intakeMaxEventsPerSess {
			jsonError(w, "session event limit reached — create the issue or start a new session", http.StatusConflict)
			return
		}
		raw, _ := json.Marshal(map[string]string{"markdown": md, "language": s.Language})
		if !appendIntakeEventHTTP(w, ctx, s.ID, "spec", "user", "", string(raw)) {
			return
		}
	}

	updated, err := loadIntakeSession(ctx, s.ID)
	if handleDBError(w, err, "intake session") {
		return
	}
	publishIntakeSessionState(ctx, s.ID)
	jsonOK(w, updated)
}

// appendIntakeEventHTTP appends a single event in its own tx and publishes it.
// Returns false after writing an error response.
func appendIntakeEventHTTP(w http.ResponseWriter, ctx context.Context, sessionID int64, kind, source, label, payload string) bool {
	tx, err := db.DB.BeginTx(ctx, nil)
	if handleDBError(w, err, "intake event") {
		return false
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(ctx, tx, sessionID, kind, source, label, payload)
	if handleDBError(w, err, "intake event") {
		return false
	}
	if handleDBError(w, tx.Commit(), "intake event") {
		return false
	}
	publishIntakeEvent(ctx, sessionID, seq)
	return true
}

// CreateIntakeCheckpoint handles POST /api/intake/sessions/{id}/checkpoints.
func CreateIntakeCheckpoint(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	if s.Status != "active" {
		jsonError(w, "intake session is not active", http.StatusConflict)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		jsonError(w, "label is required", http.StatusBadRequest)
		return
	}
	if len(label) > intakeCheckpointMaxLabel {
		jsonError(w, "label too long", http.StatusBadRequest)
		return
	}
	count, err := intakeEventCount(r.Context(), db.DB, s.ID)
	if handleDBError(w, err, "intake checkpoint") {
		return
	}
	if count >= intakeMaxEventsPerSess {
		jsonError(w, "session event limit reached", http.StatusConflict)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if handleDBError(w, err, "intake checkpoint") {
		return
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(r.Context(), tx, s.ID, "checkpoint", "user", label, "")
	if handleDBError(w, err, "intake checkpoint") {
		return
	}
	if handleDBError(w, tx.Commit(), "intake checkpoint") {
		return
	}
	publishIntakeEvent(r.Context(), s.ID, seq)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, intakeCheckpoint{Seq: seq, Label: label})
}

// ListIntakeEvents handles GET /api/intake/sessions/{id}/events — timeline
// metadata only (no payload bodies; scrubbing fetches state separately).
func ListIntakeEvents(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	since := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("since_seq")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			jsonError(w, "invalid since_seq", http.StatusBadRequest)
			return
		}
		since = n
	}
	var kindFilter []string
	if raw := strings.TrimSpace(r.URL.Query().Get("kinds")); raw != "" {
		for k := range strings.SplitSeq(raw, ",") {
			k = strings.TrimSpace(k)
			if !intakeEventKindAllowed(k) {
				jsonError(w, "invalid kind: "+k, http.StatusBadRequest)
				return
			}
			kindFilter = append(kindFilter, k)
		}
	}
	q := `SELECT seq, kind, source, label, LENGTH(payload_json), created_at
	      FROM intake_events WHERE session_id = ? AND seq > ?`
	args := []any{s.ID, since}
	if len(kindFilter) > 0 {
		// #nosec G202 G701 -- only literal "?," placeholders are concatenated;
		// every kind value is bound as a parameter and validated against the
		// closed intake event-kind set above.
		q += ` AND kind IN (` + strings.Repeat("?,", len(kindFilter)-1) + `?)`
		for _, k := range kindFilter {
			args = append(args, k)
		}
	}
	q += ` ORDER BY seq ASC`
	rows, err := db.DB.QueryContext(r.Context(), q, args...)
	if handleDBError(w, err, "intake events") {
		return
	}
	defer rows.Close()
	out := []intakeEventMeta{}
	for rows.Next() {
		var ev intakeEventMeta
		if err := rows.Scan(&ev.Seq, &ev.Kind, &ev.Source, &ev.Label, &ev.Bytes, &ev.CreatedAt); err != nil {
			if handleDBError(w, err, "intake events") {
				return
			}
		}
		out = append(out, ev)
	}
	if handleDBError(w, rows.Err(), "intake events") {
		return
	}
	jsonOK(w, out)
}

// GetIntakeState handles GET /api/intake/sessions/{id}/state?at_seq=N.
func GetIntakeState(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	at := s.Rev
	if raw := strings.TrimSpace(r.URL.Query().Get("at_seq")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > s.Rev {
			jsonError(w, "invalid at_seq", http.StatusBadRequest)
			return
		}
		at = n
	}
	state, err := intakeStateAt(r.Context(), s.ID, at)
	if handleDBError(w, err, "intake state") {
		return
	}
	jsonOK(w, state)
}

// RestoreIntakeSession handles POST /api/intake/sessions/{id}/restore.
// Restore is append-only: it appends a restore marker plus fresh copies of
// the as-of artifacts, and rematerializes the transcript. History before the
// restore point is never rewritten, so scrubbing forward still works.
func RestoreIntakeSession(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	if s.Status != "active" {
		jsonError(w, "intake session is not active", http.StatusConflict)
		return
	}
	var body struct {
		Seq int64 `json:"seq"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Seq < 1 || body.Seq > s.Rev {
		jsonError(w, "seq out of range", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	count, err := intakeEventCount(ctx, db.DB, s.ID)
	if handleDBError(w, err, "intake restore") {
		return
	}
	// A restore appends up to 1 marker + len(artifacts) snapshots.
	if count+1+len(intakeArtifactKinds) > intakeMaxEventsPerSess {
		jsonError(w, "session event limit reached", http.StatusConflict)
		return
	}

	state, err := intakeStateAt(ctx, s.ID, body.Seq)
	if handleDBError(w, err, "intake restore") {
		return
	}

	tx, err := db.DB.BeginTx(ctx, nil)
	if handleDBError(w, err, "intake restore") {
		return
	}
	defer tx.Rollback()

	marker, _ := json.Marshal(map[string]int64{"to_seq": body.Seq})
	firstSeq, err := appendIntakeEventTx(ctx, tx, s.ID, "restore", "user", "", string(marker))
	if handleDBError(w, err, "intake restore") {
		return
	}
	for _, kind := range intakeArtifactKinds {
		payload, ok := state.Artifacts[kind]
		if !ok || payload == nil {
			continue
		}
		if _, err := appendIntakeEventTx(ctx, tx, s.ID, kind, "system", "", string(payload)); err != nil {
			if handleDBError(w, err, "intake restore") {
				return
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE intake_sessions SET transcript=?, transcript_bytes=? WHERE id=?`,
		state.Transcript, len(state.Transcript), s.ID); err != nil {
		if handleDBError(w, err, "intake restore") {
			return
		}
	}
	if handleDBError(w, tx.Commit(), "intake restore") {
		return
	}
	publishIntakeEventsFrom(ctx, s.ID, firstSeq)
	updated, err := loadIntakeSession(ctx, s.ID)
	if handleDBError(w, err, "intake restore") {
		return
	}
	state2, err := intakeStateAt(ctx, s.ID, updated.Rev)
	if handleDBError(w, err, "intake restore") {
		return
	}
	jsonOK(w, map[string]any{"session": updated, "state": state2})
}

// intakeStateAt materializes the session state as of seq: the latest artifact
// snapshot per kind, and the transcript rebuilt from chunk events. The
// rebuild honors restore markers by following them recursively is NOT needed:
// restore appends full snapshots, so "latest at seq" already reflects them;
// only the transcript needs replaying, and restore rewrote the materialized
// transcript — for as-of views we rebuild from chunks up to the last restore
// marker's target, then chunks after it.
func intakeStateAt(ctx context.Context, sessionID, atSeq int64) (*intakeState, error) {
	state := &intakeState{AtSeq: atSeq, Artifacts: map[string]json.RawMessage{}}
	for _, kind := range intakeArtifactKinds {
		var payload string
		err := db.DB.QueryRowContext(ctx,
			`SELECT payload_json FROM intake_events
			 WHERE session_id = ? AND kind = ? AND seq <= ?
			 ORDER BY seq DESC LIMIT 1`, sessionID, kind, atSeq).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if payload != "" {
			state.Artifacts[kind] = json.RawMessage(payload)
		}
	}
	transcript, err := intakeTranscriptAt(ctx, sessionID, atSeq)
	if err != nil {
		return nil, err
	}
	state.Transcript = transcript
	return state, nil
}

// intakeTranscriptAt rebuilds the transcript as of atSeq. Restore markers
// re-base the effective chunk window: the transcript at any point is the
// chunks visible at the most recent restore target, plus chunks appended
// after that restore.
func intakeTranscriptAt(ctx context.Context, sessionID, atSeq int64) (string, error) {
	var markerSeq, toSeq sql.NullInt64
	var payload sql.NullString
	err := db.DB.QueryRowContext(ctx,
		`SELECT seq, payload_json, json_extract(payload_json, '$.to_seq')
		 FROM intake_events
		 WHERE session_id = ? AND kind = 'restore' AND seq <= ?
		 ORDER BY seq DESC LIMIT 1`, sessionID, atSeq).Scan(&markerSeq, &payload, &toSeq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	var base string
	lower := int64(0)
	if markerSeq.Valid && toSeq.Valid {
		b, err := intakeTranscriptAt(ctx, sessionID, toSeq.Int64)
		if err != nil {
			return "", err
		}
		base = b
		lower = markerSeq.Int64
	}

	rows, err := db.DB.QueryContext(ctx,
		`SELECT payload_json FROM intake_events
		 WHERE session_id = ? AND kind = 'transcript_chunk' AND seq > ? AND seq <= ?
		 ORDER BY seq ASC`, sessionID, lower, atSeq)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	parts := []string{}
	if base != "" {
		parts = append(parts, base)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return "", err
		}
		var chunk struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			continue
		}
		if chunk.Text != "" {
			parts = append(parts, chunk.Text)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(parts, "\n"), nil
}

func listIntakeCheckpoints(ctx context.Context, sessionID int64) ([]intakeCheckpoint, error) {
	rows, err := db.DB.QueryContext(ctx,
		`SELECT seq, label, created_at FROM intake_events
		 WHERE session_id = ? AND kind = 'checkpoint' ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []intakeCheckpoint{}
	for rows.Next() {
		var cp intakeCheckpoint
		if err := rows.Scan(&cp.Seq, &cp.Label, &cp.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}
