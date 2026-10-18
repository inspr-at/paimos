// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const (
	liveThreadLimit      = 256
	liveViewerLimit      = 256
	liveTenantThreads    = 64
	liveTenantViewers    = 64
	livePerThreadViewers = 4
	liveFrameLimit       = 64
	liveByteLimit        = 256 << 10
	liveRetention        = 2 * time.Minute
)

type liveFrame struct {
	sequence       int64
	session, epoch string
	raw            []byte
}
type liveThread struct {
	id                         string
	tenant                     string
	sequence                   int64
	sourceSession, sourceEpoch string
	sourceSequence             uint64
	frames                     []liveFrame
	bytes                      int
	touched                    time.Time
	viewers                    map[chan struct{}]bool
}

// Replay is RAM only, capped by both bytes and count. Viewers hold one wake
// hint, never their own text queue. Idle threads expire; restart/gaps fail
// explicitly with 409 and require reloading final history.
type liveRelay struct {
	mu      sync.Mutex
	threads map[string]*liveThread
	viewers int
	now     func() time.Time
}

func (b *liveRelay) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}
func (b *liveRelay) thread(key string, create bool) (*liveThread, error) {
	now := b.clock()
	for k, v := range b.threads {
		if len(v.viewers) == 0 && now.Sub(v.touched) >= liveRetention {
			delete(b.threads, k)
		}
	}
	if b.threads == nil {
		b.threads = map[string]*liveThread{}
	}
	v := b.threads[key]
	if v == nil && !create {
		return nil, nil
	}
	if v == nil {
		tenant, _, _ := strings.Cut(key, "/")
		count := 0
		for _, thread := range b.threads {
			if thread.tenant == tenant {
				count++
			}
		}
		if count >= liveTenantThreads && !b.evictIdle(tenant) {
			return nil, workorders.Fail(429, "live chat tenant thread limit reached")
		}
		if len(b.threads) >= liveThreadLimit {
			if !b.evictIdle("") {
				return nil, workorders.Fail(429, "live chat thread limit reached")
			}
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, err
		}
		v = &liveThread{id: hex.EncodeToString(id[:]), tenant: tenant, viewers: map[chan struct{}]bool{}}
		b.threads[key] = v
	}
	v.touched = now
	return v, nil
}

// Called with mu held. Active streams are never evicted; an idle eviction
// invalidates that relay ID so its previous cursors fail explicitly.
func (b *liveRelay) evictIdle(tenant string) bool {
	var oldest *liveThread
	var key string
	for k, thread := range b.threads {
		if len(thread.viewers) == 0 && (tenant == "" || thread.tenant == tenant) && (oldest == nil || thread.touched.Before(oldest.touched) || thread.touched.Equal(oldest.touched) && k < key) {
			oldest, key = thread, k
		}
	}
	if oldest == nil {
		return false
	}
	delete(b.threads, key)
	return true
}
func liveKey(tenant, thread string) string { return tenant + "/" + thread }

type liveCursor struct {
	Tenant, Person, Thread, Relay string
	Sequence                      int64
}

func liveToken(c liveCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func parseLiveToken(token string, scope liveCursor) (liveCursor, error) {
	if len(token) > 4096 {
		return scope, workorders.Fail(400, "invalid live chat cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	var c liveCursor
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Tenant != scope.Tenant || c.Person != scope.Person || c.Thread != scope.Thread || c.Sequence < 0 || len(c.Relay) != 32 {
		return scope, workorders.Fail(400, "invalid live chat cursor")
	}
	return c, nil
}

func (b *liveRelay) subscribe(c liveCursor, token string) (liveCursor, <-chan struct{}, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, err := b.thread(liveKey(c.Tenant, c.Thread), true)
	if err != nil {
		return c, nil, nil, err
	}
	if token != "" {
		previous, err := parseLiveToken(token, c)
		if err != nil {
			return c, nil, nil, err
		}
		if previous.Relay != v.id || previous.Sequence > v.sequence || len(v.frames) > 0 && previous.Sequence < v.frames[0].sequence-1 {
			return c, nil, nil, workorders.Fail(409, "live chat cursor expired; reload final history")
		}
		c = previous
	} else {
		c.Relay = v.id
		c.Sequence = v.sequence
	}
	tenantViewers := 0
	for _, thread := range b.threads {
		if thread.tenant == c.Tenant {
			tenantViewers += len(thread.viewers)
		}
	}
	if b.viewers >= liveViewerLimit || tenantViewers >= liveTenantViewers || len(v.viewers) >= livePerThreadViewers {
		return c, nil, nil, workorders.Fail(429, "live chat viewer limit reached")
	}
	wake := make(chan struct{}, 1)
	v.viewers[wake] = true
	b.viewers++
	wake <- struct{}{}
	return c, wake, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if v.viewers[wake] {
			delete(v.viewers, wake)
			b.viewers--
			v.touched = b.clock()
		}
	}, nil
}
func (b *liveRelay) next(c liveCursor) (liveFrame, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.threads[liveKey(c.Tenant, c.Thread)]
	if v == nil || v.id != c.Relay || len(v.frames) > 0 && c.Sequence < v.frames[0].sequence-1 {
		return liveFrame{}, false, workorders.Fail(409, "live chat cursor expired; reload final history")
	}
	for _, f := range v.frames {
		if f.sequence > c.Sequence {
			return f, true, nil
		}
	}
	return liveFrame{}, false, nil
}
func (b *liveRelay) publish(key, session, epoch string, source uint64, payload any) (bool, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	if len(raw) > 128<<10 {
		return false, workorders.Fail(400, "live chat frame too large")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Durable-state notifications carry no interim content and only wake an
	// existing replay buffer. They must not consume capacity without viewers.
	v, err := b.thread(key, session != "" || source != 0)
	if err != nil {
		return false, err
	}
	if v == nil {
		return false, nil
	}
	if source > 0 {
		if v.sourceSession == session && v.sourceEpoch == epoch && source <= v.sourceSequence {
			return false, nil
		}
		v.sourceSession = session
		v.sourceEpoch = epoch
		v.sourceSequence = source
	}
	v.sequence++
	v.frames = append(v.frames, liveFrame{v.sequence, session, epoch, raw})
	v.bytes += len(raw)
	for len(v.frames) > liveFrameLimit || v.bytes > liveByteLimit {
		v.bytes -= len(v.frames[0].raw)
		v.frames[0] = liveFrame{}
		v.frames = v.frames[1:]
	}
	for wake := range v.viewers {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return true, nil
}

type livePublish struct {
	WorkerBindingRequest
	Event agentd.ChatSessionEvent `json:"event"`
}

func (m *Module) publishLive(r *http.Request, tx pgx.Tx, p tenant.Principal, in livePublish) (any, error) {
	e := in.Event
	if e.Sequence == 0 || e.Binding.TenantID != p.TenantID || e.Binding.PrincipalID != p.ID || e.Binding.SessionID != in.SessionID || e.Update == nil || e.Capabilities != nil || !agentd.ValidChatUpdate(*e.Update) {
		return nil, workorders.Fail(400, "invalid normalized chat event")
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	if _, err := AuthorizeWorkerTx(r.Context(), tx, r, p, in.WorkerBindingRequest); err != nil {
		return nil, err
	}
	accepted, err := m.live.publish(liveKey(p.TenantID, in.ConversationID), in.SessionID, in.BindingEpoch, e.Sequence, struct {
		Type    string             `json:"type"`
		Update  *agentd.ChatUpdate `json:"update"`
		Dropped uint64             `json:"dropped_events"`
		Source  uint64             `json:"source_sequence"`
	}{"update", e.Update, e.DroppedEvents, e.Sequence})
	return map[string]any{"contract": "chat-live-v1", "accepted": accepted}, err
}

var errLiveBindingChanged = errors.New("live chat binding changed")

// Check every frame/heartbeat in a bounded, read-only snapshot. A snapshot
// started after a committed revocation sees it. Release the transaction and
// admission slot before socket I/O so slow viewers retain no DB resources.
func (m *Module) liveAuthorized(r *http.Request, p tenant.Principal, f *liveFrame, send func() error) error {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	select {
	case m.liveChecks <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	current := true
	err := db.InTenantReadSnapshot(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		c, err := participantRead(r.WithContext(ctx), tx, p)
		if err != nil {
			return err
		}
		if f != nil && f.session != "" {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_threads t JOIN chat_session_bindings b ON b.tenant_id=t.tenant_id AND b.role_id=t.role_id JOIN harness_sessions s ON s.tenant_id=b.tenant_id AND s.id=b.session_id WHERE t.id=$1 AND b.session_id=$2 AND b.binding_epoch=$3 AND b.valid_to IS NULL AND s.stopped_at IS NULL AND s.archived_at IS NULL AND s.phase NOT IN ('stopping','stopped') AND coalesce(s.heartbeat_at,s.created_at)>clock_timestamp()-interval '2 minutes')`, c.Thread, f.session, f.epoch).Scan(&current)
			if err != nil {
				return err
			}
		}
		return nil
	})
	<-m.liveChecks
	if err != nil {
		return err
	}
	if !current {
		return errLiveBindingChanged
	}
	if send != nil {
		return send()
	}
	return nil
}
func (m *Module) streamLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !m.enabled {
		httpapi.WriteError(w, 404, "not found")
		return
	}
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		httpapi.WriteError(w, 401, "unauthorized")
		return
	}
	token := r.URL.Query().Get("after")
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		if token != "" && token != header {
			httpapi.WriteError(w, 400, "conflicting live chat cursors")
			return
		}
		token = header
	}
	if len(token) > 4096 {
		httpapi.WriteError(w, 400, "invalid live chat cursor")
		return
	}
	if err := m.liveAuthorized(r, p, nil, nil); err != nil {
		liveError(w, err)
		return
	}
	c, wake, unsubscribe, err := m.live.subscribe(liveCursor{Tenant: p.TenantID, Person: p.ID, Thread: r.PathValue("id")}, token)
	if err != nil {
		liveError(w, err)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	send := func(kind string, cursor liveCursor, raw []byte) error {
		if err := controller.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", liveToken(cursor), kind, raw); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err = m.liveAuthorized(r, p, nil, func() error { return send("ready", c, []byte(`{"contract":"chat-live-v1"}`)) }); err != nil {
		return
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	end := time.NewTimer(5 * time.Minute)
	defer end.Stop()
	for {
		for {
			select {
			case <-r.Context().Done():
				return
			case <-end.C:
				return
			default:
			}
			f, exists, nextErr := m.live.next(c)
			if nextErr != nil {
				_ = m.liveAuthorized(r, p, nil, func() error { return send("resync", c, []byte(`{"reason":"replay_gap"}`)) })
				return
			}
			if !exists {
				break
			}
			next := c
			next.Sequence = f.sequence
			if err = m.liveAuthorized(r, p, &f, func() error { return send("chat", next, f.raw) }); err != nil {
				if errors.Is(err, errLiveBindingChanged) {
					_ = send("resync", next, []byte(`{"reason":"binding_changed"}`))
				}
				return
			}
			c = next
		}
		select {
		case <-r.Context().Done():
			return
		case <-end.C:
			return
		case <-wake:
		case <-tick.C:
			if err = m.liveAuthorized(r, p, nil, func() error { return send("keepalive", c, []byte(`null`)) }); err != nil {
				return
			}
		}
	}
}
func liveError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, authz.ErrForbidden) {
		err = unavailable()
	}
	workorders.WriteError(w, err)
}
