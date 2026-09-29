// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	defaultLimit = 50
	maxLimit     = 200
	maxWaitMS    = 30000
)

func (m *module) listen(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	after, waitMS, limit, err := parseListenQuery(r)
	if err != nil {
		failure(w, err)
		return
	}
	sessionID, err := sessionQuery(r)
	if err != nil {
		failure(w, err)
		return
	}
	exact, err := exactSessionQuery(r, sessionID)
	if err != nil {
		failure(w, err)
		return
	}
	ctx := r.Context()
	// A one-shot pull is what a turn-boundary hook does; a wait is a long poll.
	via := SeenHook
	if waitMS > 0 {
		via = SeenLongPoll
	}
	if waitMS == 0 {
		page, err := m.page(ctx, p, after, limit, sessionID, exact, via)
		if err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	// LISTEN is session state, not a tenant query, and must not hold a pool slot.
	conn, err := listenConn(ctx, m.pool)
	if err != nil {
		failure(w, err)
		return
	}
	defer closeListen(conn)
	if _, err = conn.Exec(ctx, "LISTEN aeon_events"); err != nil {
		failure(w, err)
		return
	}
	page, err := m.page(ctx, p, after, limit, sessionID, exact, via)
	if err != nil {
		failure(w, err)
		return
	}
	deadline := time.Now().Add(time.Duration(waitMS) * time.Millisecond)
	for len(page.Items) == 0 && time.Now().Before(deadline) {
		waitErr := waitTenantNotify(ctx, conn, p.TenantID, deadline)
		if ctx.Err() != nil {
			return
		}
		if waitErr != nil && !errors.Is(waitErr, errWaitTimeout) {
			failure(w, waitErr)
			return
		}
		page, err = m.page(ctx, p, after, limit, sessionID, exact, via)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failure(w, err)
			return
		}
		if errors.Is(waitErr, errWaitTimeout) {
			break
		}
	}
	writeJSON(w, http.StatusOK, page)
}

func parseListenQuery(r *http.Request) (after int64, waitMS int, limit int, err error) {
	q := r.URL.Query()
	limit = defaultLimit
	if q.Has("after") {
		after, err = parseNonNeg(q.Get("after"))
		if err != nil {
			return 0, 0, 0, badRequest("invalid after")
		}
	}
	if q.Has("wait_ms") {
		var n int64
		n, err = parseNonNeg(q.Get("wait_ms"))
		if err != nil || n > maxWaitMS {
			return 0, 0, 0, badRequest("invalid wait_ms")
		}
		waitMS = int(n)
	}
	if q.Has("limit") {
		var n int64
		n, err = parseNonNeg(q.Get("limit"))
		if err != nil || n < 1 || n > maxLimit {
			return 0, 0, 0, badRequest("invalid limit")
		}
		limit = int(n)
	}
	return after, waitMS, limit, nil
}

func (m *module) page(ctx context.Context, p tenant.Principal, after int64, limit int, session *string, exact bool, via string) (Page, error) {
	items, err := m.pendingVia(ctx, p, after, limit+1, session, exact, via)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items, NextAfter: after}
	if len(items) > limit {
		page.Items = items[:limit]
	}
	if len(page.Items) == 0 {
		page.Items = []Message{}
	} else {
		page.NextAfter = page.Items[len(page.Items)-1].SentEventID
	}
	return page, nil
}

func (m *module) pending(ctx context.Context, p tenant.Principal, after int64, limit int, sessions ...*string) ([]Message, error) {
	var sessionID *string
	if len(sessions) > 0 {
		sessionID = sessions[0]
	}
	return m.pendingVia(ctx, p, after, limit, sessionID, false, "")
}

// pendingVia reads the caller's unacked messages. With a session it records
// that generation as listening through via (AEON-280); every returned message
// is stamped as handed over to its recipient. exact leaves out unbound
// principal-wide rows, so a session hook never pages through a broadcast backlog.
func (m *module) pendingVia(ctx context.Context, p tenant.Principal, after int64, limit int, sessionID *string, exact bool, via string) ([]Message, error) {
	var items []Message
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		items = nil
		if _, err := messageSession(ctx, tx, sessionID, p.ID, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+messageCols+`
			FROM inbox_messages
			WHERE recipient_principal_id = $1::uuid
			  AND ((recipient_session_id IS NULL AND NOT $5) OR recipient_session_id=$4::uuid)
              AND sent_event_id > $2
			  AND acked_at IS NULL
			  AND (expires_at IS NULL OR expires_at > clock_timestamp())
			ORDER BY sent_event_id
			LIMIT $3`, p.ID, after, limit, sessionID, exact)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			msg, err := scanMessage(rows)
			if err != nil {
				return err
			}
			if sessionID != nil {
				msg.SenderLabel = msg.frozenSenderLabel
			} else {
				msg.RecipientSessionID = nil
				msg.SenderSessionID = nil
				msg.SenderLabel = ""
			}
			items = append(items, msg)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ID
		}
		if err := MarkFetched(ctx, tx, p, via, ids...); err != nil {
			return err
		}
		if sessionID != nil {
			return MarkSessionSeen(ctx, tx, *sessionID, via)
		}
		return nil
	})
	if items == nil {
		items = []Message{}
	}
	return items, err
}

func listenConn(ctx context.Context, pool *pgxpool.Pool) (*pgx.Conn, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return pgx.ConnectConfig(connectCtx, pool.Config().ConnConfig.Copy())
}

func closeListen(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

var errWaitTimeout = errors.New("inbox wait elapsed")

func waitTenantNotify(ctx context.Context, conn *pgx.Conn, tenantID string, deadline time.Time) error {
	for {
		if !time.Now().Before(deadline) {
			return errWaitTimeout
		}
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errWaitTimeout
			}
			return err
		}
		if notificationTenant(n.Payload) == tenantID {
			return nil
		}
	}
}

// exactSessionQuery reads exact_session (AEON-280): true returns only rows bound
// to the named session. It needs session; omitted or false keeps the default.
func exactSessionQuery(r *http.Request, session *string) (bool, error) {
	q := r.URL.Query()
	if !q.Has("exact_session") {
		return false, nil
	}
	exact, err := strconv.ParseBool(q.Get("exact_session"))
	if err != nil {
		return false, badRequest("invalid exact_session")
	}
	if exact && session == nil {
		return false, badRequest("exact_session requires session")
	}
	return exact, nil
}
