// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Cursors are scoped keyset coordinates, never authority. Every page resolves
// current participant and project permission again inside the access fence.
type historyCursor struct {
	Tenant, Person, Thread string
	Kind                   string
	Position, Snapshot     int64
}

func encodeCursor(c historyCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeCursor(raw string, scope historyCursor) (historyCursor, error) {
	if len(raw) > 4096 {
		return scope, workorders.Fail(400, "invalid chat cursor")
	}
	bytes, err := base64.RawURLEncoding.DecodeString(raw)
	var c historyCursor
	if err != nil || json.Unmarshal(bytes, &c) != nil || c.Tenant != scope.Tenant || c.Person != scope.Person || c.Thread != scope.Thread || c.Kind != scope.Kind || c.Position < 0 || c.Snapshot < 0 {
		return scope, workorders.Fail(400, "invalid chat cursor")
	}
	return c, nil
}
func boundedLimit(r *http.Request, maximum, fallback int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if len(raw) > 3 || err != nil || value < 1 || value > maximum {
		return 0, workorders.Fail(400, "invalid chat limit")
	}
	return value, nil
}
func participant(r *http.Request, tx pgx.Tx, p tenant.Principal) (historyCursor, error) {
	if !workorders.UUID(r.PathValue("id")) {
		return historyCursor{}, unavailable()
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return historyCursor{}, err
	}
	return participantRead(r, tx, p)
}

// Live reads use a short snapshot without taking the access-change write
// fence. Mutations and durable history keep participant's existing fence.
func participantRead(r *http.Request, tx pgx.Tx, p tenant.Principal) (historyCursor, error) {
	c := historyCursor{Tenant: p.TenantID, Thread: r.PathValue("id")}
	if !workorders.UUID(c.Thread) {
		return c, unavailable()
	}
	person, err := personID(r.Context(), tx, p)
	if err != nil {
		return c, err
	}
	c.Person = person
	var project string
	if err = tx.QueryRow(r.Context(), `SELECT project_id::text FROM chat_threads WHERE id=$1 AND person_id=$2 AND archived_at IS NULL`, c.Thread, person).Scan(&project); err != nil {
		return c, err
	}
	if err = require(r.Context(), tx, p, "chat.read", project); err != nil {
		return c, err
	}
	_, err = tx.Exec(r.Context(), `SELECT set_config('aeon.chat_conversation_id',$1,true)`, c.Thread)
	return c, err
}

type historyMessage struct {
	ID           string  `json:"message_id"`
	Conversation string  `json:"conversation_id"`
	Sequence     string  `json:"read_seq"`
	Event        string  `json:"sent_event_position"`
	Body         string  `json:"body"`
	Payload      string  `json:"payload_mode"`
	ReplyTo      *string `json:"reply_to,omitempty"`
}
type historyItem struct {
	Message   historyMessage `json:"message"`
	ReadState string         `json:"person_read_state"`
	Receipt   any            `json:"receipt"`
}
type historyPage struct {
	Contract   string        `json:"contract"`
	Items      []historyItem `json:"items"`
	MoreBefore bool          `json:"has_more_before"`
	MoreAfter  bool          `json:"has_more_after"`
	Before     *string       `json:"before_cursor"`
	After      *string       `json:"after_cursor"`
	Snapshot   string        `json:"snapshot_cursor"`
	Epoch      string        `json:"migration_epoch"`
}

func (m *Module) listMessages(r *http.Request, tx pgx.Tx, p tenant.Principal, _ struct{}) (any, error) {
	limit, err := boundedLimit(r, 100, 50)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	before, after, around := q.Get("before"), q.Get("after"), q.Get("around_message")
	count := 0
	for _, value := range []string{before, after, around} {
		if value != "" {
			count++
		}
	}
	if count > 1 || around != "" && !workorders.UUID(around) {
		return nil, workorders.Fail(400, "invalid chat page selector")
	}
	c, err := participant(r, tx, p)
	if err != nil {
		return nil, err
	}
	c.Kind = "messages"
	if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(sent_event_id),0) FROM inbox_messages WHERE chat_thread_id=$1`, c.Thread).Scan(&c.Snapshot); err != nil {
		return nil, err
	}
	descending := after == ""
	if before != "" || after != "" {
		raw := before
		if after != "" {
			raw = after
		}
		c, err = decodeCursor(raw, c)
		if err != nil {
			return nil, err
		}
	}
	if around != "" {
		if err = tx.QueryRow(r.Context(), `SELECT sent_event_id FROM inbox_messages WHERE chat_thread_id=$1 AND id=$2`, c.Thread, around).Scan(&c.Position); err != nil {
			return nil, err
		}
		// A bounded centred window: half before, the selected message and later
		// messages. No offset into the whole history is evaluated.
		if err = tx.QueryRow(r.Context(), `SELECT coalesce(min(sent_event_id),0) FROM (SELECT sent_event_id FROM inbox_messages WHERE chat_thread_id=$1 AND sent_event_id<$2 ORDER BY sent_event_id DESC LIMIT $3) older`, c.Thread, c.Position, limit/2).Scan(&c.Position); err != nil {
			return nil, err
		}
		if c.Position > 0 {
			c.Position--
		}
		descending = false
	}
	sign, order := ">", "ASC"
	if descending {
		sign, order = "<", "DESC"
	}
	query := `SELECT id::text,sent_event_id,body,reply_to_id::text FROM inbox_messages WHERE chat_thread_id=$1 AND sent_event_id<=$2`
	if c.Position > 0 {
		query += ` AND sent_event_id` + sign + `$3`
	} else {
		query += ` AND $3::bigint=0`
	}
	query += ` ORDER BY sent_event_id ` + order + ` LIMIT $4`
	rows, err := tx.Query(r.Context(), query, c.Thread, c.Snapshot, c.Position, limit+1)
	if err != nil {
		return nil, err
	}
	items := []historyItem{}
	positions := []int64{}
	bytes := 1024
	for rows.Next() {
		item := historyItem{Message: historyMessage{Conversation: c.Thread, Payload: "inline"}, ReadState: "known_unread"}
		var event int64
		if err = rows.Scan(&item.Message.ID, &event, &item.Message.Body, &item.Message.ReplyTo); err != nil {
			rows.Close()
			return nil, err
		}
		item.Message.Sequence = strconv.FormatInt(event, 10)
		item.Message.Event = item.Message.Sequence
		raw, _ := json.Marshal(item)
		if len(items) == limit || bytes+len(raw) > 1<<20 {
			break
		}
		bytes += len(raw)
		items = append(items, item)
		positions = append(positions, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Read sequences use immutable event positions. Sparse positions remain
	// exact bitmap gaps; a high watermark never marks the gaps as seen.
	seen := map[int64][]byte{}
	// A page can lie beyond the first 64 read chunks. Fetch only the exact
	// chunks represented on this page, not a conversation-wide bitmap list.
	indices := []int64{}
	for _, event := range positions {
		if _, ok := seen[event/4096]; !ok {
			indices = append(indices, event/4096)
		}
	}
	if len(indices) > 0 {
		rows, err = tx.Query(r.Context(), `SELECT chunk_index,bitmap FROM chat_seen_chunks WHERE conversation_id=$1 AND person_id=$2 AND chunk_index=ANY($3::bigint[])`, c.Thread, c.Person, indices)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var index int64
			var bits []byte
			if err = rows.Scan(&index, &bits); err != nil {
				rows.Close()
				return nil, err
			}
			seen[index] = bits
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for i, event := range positions {
			bits := seen[event/4096]
			if len(bits) == 512 && bits[event%4096/8]&(1<<uint(event%8)) != 0 {
				items[i].ReadState = "seen"
			}
		}
	}
	if descending {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
			positions[i], positions[j] = positions[j], positions[i]
		}
	}
	out := historyPage{Contract: "chat-v1", Items: items, Snapshot: encodeCursor(c), Epoch: "0"}
	if len(positions) > 0 {
		low, high := positions[0], positions[len(positions)-1]
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE chat_thread_id=$1 AND sent_event_id<$2),EXISTS(SELECT 1 FROM inbox_messages WHERE chat_thread_id=$1 AND sent_event_id>$3 AND sent_event_id<=$4)`, c.Thread, low, high, c.Snapshot).Scan(&out.MoreBefore, &out.MoreAfter); err != nil {
			return nil, err
		}
		if out.MoreBefore {
			next := c
			next.Position = low
			token := encodeCursor(next)
			out.Before = &token
		}
		if out.MoreAfter {
			next := c
			next.Position = high
			token := encodeCursor(next)
			out.After = &token
		}
	}
	return out, nil
}

type seenChunk struct {
	Index  string `json:"chunk_index"`
	Bitmap string `json:"bitmap_base64"`
}
type seenMarker struct {
	Contract     string      `json:"contract"`
	Conversation string      `json:"conversation_id"`
	Prefix       string      `json:"contiguous_position"`
	Chunks       []seenChunk `json:"seen_chunks"`
	Next         *string     `json:"next_cursor"`
	Revision     string      `json:"revision"`
	Epoch        string      `json:"migration_epoch"`
	notify       string
}

func (s seenMarker) afterCommit(m *Module) {
	if s.notify != "" {
		_, _ = m.live.publish(s.notify, "", "", 0, map[string]string{"type": "read_marker"})
	}
}

type chunkRow struct {
	index int64
	bits  []byte
}

func readChunks(ctx context.Context, tx pgx.Tx, c historyCursor, start int64, limit int) ([]chunkRow, error) {
	rows, err := tx.Query(ctx, `SELECT chunk_index,bitmap FROM chat_seen_chunks WHERE conversation_id=$1 AND person_id=$2 AND chunk_index>=$3 ORDER BY chunk_index LIMIT $4`, c.Thread, c.Person, start, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chunkRow{}
	for rows.Next() {
		var chunk chunkRow
		if err = rows.Scan(&chunk.index, &chunk.bits); err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, rows.Err()
}
func marker(ctx context.Context, tx pgx.Tx, c historyCursor, start int64, limit int) (seenMarker, error) {
	c.Kind = "seen"
	out := seenMarker{Contract: "chat-v1", Conversation: c.Thread, Prefix: "0", Chunks: []seenChunk{}, Epoch: "0"}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM chat_read_state WHERE conversation_id=$1 AND person_id=$2`, c.Thread, c.Person).Scan(&revision); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	out.Revision = strconv.FormatInt(revision, 10)
	rows, err := readChunks(ctx, tx, c, start, limit+1)
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		next := c
		next.Position = rows[limit].index
		token := encodeCursor(next)
		out.Next = &token
		rows = rows[:limit]
	}
	for _, chunk := range rows {
		out.Chunks = append(out.Chunks, seenChunk{strconv.FormatInt(chunk.index, 10), base64.StdEncoding.EncodeToString(chunk.bits)})
	}
	return out, nil
}
func (m *Module) getSeen(r *http.Request, tx pgx.Tx, p tenant.Principal, _ struct{}) (any, error) {
	limit, err := boundedLimit(r, 64, 64)
	if err != nil {
		return nil, err
	}
	c, err := participant(r, tx, p)
	if err != nil {
		return nil, err
	}
	c.Kind = "seen"
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err = decodeCursor(raw, c)
		if err != nil {
			return nil, err
		}
	}
	return marker(r.Context(), tx, c, c.Position, limit)
}

type seenUnion struct {
	IDs   []string `json:"visible_message_ids"`
	Epoch string   `json:"migration_epoch"`
}

func (m *Module) unionSeen(r *http.Request, tx pgx.Tx, p tenant.Principal, in seenUnion) (any, error) {
	if len(in.IDs) < 1 || len(in.IDs) > 100 {
		return nil, workorders.Fail(400, "invalid visible message IDs")
	}
	ids := map[string]bool{}
	for _, id := range in.IDs {
		if !workorders.UUID(id) || ids[id] {
			return nil, workorders.Fail(400, "invalid visible message IDs")
		}
		ids[id] = true
	}
	if in.Epoch != "0" {
		return nil, workorders.Fail(409, "stale migration epoch")
	}
	c, err := participant(r, tx, p)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT sent_event_id FROM inbox_messages WHERE chat_thread_id=$1 AND id=ANY($2::uuid[]) ORDER BY sent_event_id`, c.Thread, in.IDs)
	if err != nil {
		return nil, err
	}
	events := []int64{}
	for rows.Next() {
		var event int64
		if err = rows.Scan(&event); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(events) != len(in.IDs) {
		return nil, unavailable()
	}
	groups := map[int64][]int64{}
	for _, event := range events {
		groups[event/4096] = append(groups[event/4096], event%4096)
	}
	indices := []int64{}
	for index := range groups {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	// The participant function holds the tenant access-change fence and checked
	// RequireTx in this final write transaction. Chunk locks follow sorted order.
	changed := false
	for _, index := range indices {
		chunks, err := readChunks(r.Context(), tx, c, index, 1)
		if err != nil {
			return nil, err
		}
		bits := make([]byte, 512)
		if len(chunks) > 0 && chunks[0].index == index {
			copy(bits, chunks[0].bits)
		}
		for _, bit := range groups[index] {
			bits[bit/8] |= 1 << uint(bit%8)
		}
		tag, err := tx.Exec(r.Context(), `INSERT INTO chat_seen_chunks(tenant_id,conversation_id,person_id,chunk_index,bitmap) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,conversation_id,person_id,chunk_index) DO UPDATE SET bitmap=EXCLUDED.bitmap,revision=chat_seen_chunks.revision+1 WHERE chat_seen_chunks.bitmap<>EXCLUDED.bitmap`, p.TenantID, c.Thread, c.Person, index, bits)
		if err != nil {
			return nil, err
		}
		changed = changed || tag.RowsAffected() > 0
	}
	if changed {
		if _, err := tx.Exec(r.Context(), `INSERT INTO chat_read_state(tenant_id,conversation_id,person_id) VALUES($1,$2,$3) ON CONFLICT(tenant_id,conversation_id,person_id) DO UPDATE SET revision=chat_read_state.revision+1`, p.TenantID, c.Thread, c.Person); err != nil {
			return nil, err
		}
	}
	out, err := marker(r.Context(), tx, c, 0, 64)
	if changed {
		out.notify = liveKey(p.TenantID, c.Thread)
	}
	return out, err
}
