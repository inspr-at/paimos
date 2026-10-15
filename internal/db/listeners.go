// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NotificationListener is a cursor-free wake source. Consumers subscribe before
// replay and rescan durable state on IsResync; hints never grant data access.
type NotificationListener interface {
	WaitForNotification(context.Context) (*pgconn.Notification, error)
	Close(context.Context) error
}

// There is one session per channel per main pool, independent of tenant and
// client count. The fixed allowlist also bounds accidental channel creation.
func listenerName(channel string) string {
	switch channel {
	case "aeon_events":
		return "aeon-events-listener"
	case "aeon_run_queue":
		return "aeon-agentruns-listener"
	case "aeon_routine_deliveries":
		return "aeon-inbox-routine-listener"
	case "aeon_routine_intents":
		return "aeon-routineexec-listener"
	}
	return ""
}

// dedicatedConfig replaces only our default name, preserving an operator's URL
// application_name (including an explicitly empty name).
func dedicatedConfig(pool *pgxpool.Pool, name string) *pgx.ConnConfig {
	cfg := pool.Config().ConnConfig.Copy()
	if m := monitor(pool); m != nil && m.defaultApplicationName {
		cfg.RuntimeParams["application_name"] = name
	}
	return cfg
}

type notificationHub struct {
	mu          sync.Mutex
	pool        *pgxpool.Pool
	channel     string
	subs        map[*notificationSubscription]struct{}
	cancel      context.CancelFunc
	ready, done chan struct{}
	initialErr  error
	closing     bool
}
type notificationSubscription struct {
	hub    *notificationHub
	tenant string
	wake   chan *pgconn.Notification
	done   chan struct{}
	once   sync.Once
	stopMu sync.Mutex
	stop   func() bool
}

// SubscribeNotifications returns only once LISTEN is established. An empty
// tenant is for server workers that already scan and authorize all tenants.
func SubscribeNotifications(ctx context.Context, pool *pgxpool.Pool, channel, tenantID string) (NotificationListener, error) {
	m := monitor(pool)
	if m == nil || listenerName(channel) == "" {
		return nil, errors.New("notification pool or channel not configured")
	}
	for ctx.Err() == nil {
		m.listenerMu.Lock()
		if m.listenerShutdown.Load() {
			m.listenerMu.Unlock()
			return nil, context.Canceled
		}
		if m.listeners == nil {
			m.listeners = make(map[string]*notificationHub)
		}
		h := m.listeners[channel]
		if h != nil && h.closing {
			done := h.done
			m.listenerMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
			}
			continue
		}
		if h == nil {
			runCtx, cancel := context.WithCancel(context.Background())
			h = &notificationHub{pool: pool, channel: channel, subs: make(map[*notificationSubscription]struct{}), cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
			m.listeners[channel] = h
			go h.run(runCtx, m)
		}
		h.mu.Lock()
		if len(h.subs) >= 4096 {
			h.mu.Unlock()
			m.listenerMu.Unlock()
			return nil, errors.New("notification subscriber limit")
		}
		s := &notificationSubscription{hub: h, tenant: tenantID, wake: make(chan *pgconn.Notification, 16), done: make(chan struct{})}
		h.subs[s] = struct{}{}
		h.mu.Unlock()
		m.listenerMu.Unlock()
		// The callback and explicit Close may race; once owns unsubscription.
		s.stopMu.Lock()
		s.stop = context.AfterFunc(ctx, func() { _ = s.Close(context.Background()) })
		s.stopMu.Unlock()
		select {
		case <-ctx.Done():
			_ = s.Close(context.Background())
			return nil, ctx.Err()
		case <-h.ready:
			if h.initialErr != nil {
				_ = s.Close(context.Background())
				return nil, h.initialErr
			}
			return s, nil
		}
	}
	return nil, ctx.Err()
}

func (s *notificationSubscription) Close(ctx context.Context) error {
	s.once.Do(func() {
		s.stopMu.Lock()
		if s.stop != nil {
			s.stop()
		}
		s.stopMu.Unlock()
		h := s.hub
		m := monitor(h.pool)
		m.listenerMu.Lock()
		h.mu.Lock()
		delete(h.subs, s)
		close(s.done)
		if len(h.subs) == 0 {
			h.closing = true
			h.cancel()
		}
		h.mu.Unlock()
		m.listenerMu.Unlock()
	})
	// Wait for the final backend to close, before admitting a replacement hub.
	s.hub.mu.Lock()
	last := s.hub.closing
	s.hub.mu.Unlock()
	if last {
		select {
		case <-s.hub.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *notificationSubscription) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, context.Canceled
	case <-s.hub.done:
		return nil, context.Canceled
	case n := <-s.wake:
		return n, nil
	}
}

// IsResync marks local mailbox overflow or a connection gap. No event cursor is
// advanced: replay consumers reread from their last delivered cursor, and wake
// endpoints reauthorize and send a fresh wake without exposing event metadata.
func IsResync(n *pgconn.Notification) bool {
	var hint struct {
		Resync bool `json:"resync"`
	}
	return n != nil && json.Unmarshal([]byte(n.Payload), &hint) == nil && hint.Resync
}
func (h *notificationHub) publish(n *pgconn.Notification) {
	var hint struct {
		Tenant string `json:"tenant_id"`
	}
	resync := n == nil
	if !resync && (json.Unmarshal([]byte(n.Payload), &hint) != nil || hint.Tenant == "") {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !resync && s.tenant != "" && s.tenant != hint.Tenant {
			continue
		}
		notice := n
		if resync {
			notice = h.resync(s.tenant)
		}
		select {
		case s.wake <- notice:
		default:
			// Bounded draining, never wait on a subscriber. Any dropped event ID is
			// replaced with an explicit durable rescan, rather than another lossy hint.
			for range cap(s.wake) {
				select {
				case <-s.wake:
				default:
				}
			}
			s.wake <- h.resync(s.tenant)
		}
	}
}
func (h *notificationHub) resync(tenant string) *pgconn.Notification {
	payload, _ := json.Marshal(map[string]any{"tenant_id": tenant, "resync": true})
	return &pgconn.Notification{Channel: h.channel, Payload: string(payload)}
}
func (h *notificationHub) run(ctx context.Context, m *PoolMonitor) {
	defer func() {
		m.listenerMu.Lock()
		delete(m.listeners, h.channel)
		close(h.done)
		m.listenerMu.Unlock()
	}()
	first := true
	for ctx.Err() == nil {
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := pgx.ConnectConfig(op, dedicatedConfig(h.pool, listenerName(h.channel)))
		if err == nil {
			_, err = conn.Exec(op, "LISTEN "+h.channel)
		}
		cancel()
		reconnect := !first
		if first {
			h.initialErr = err
			close(h.ready)
			first = false
		}
		if err == nil {
			if reconnect {
				h.publish(nil)
			} // LISTEN precedes the reconnect rescan.
			lifetime, stop := context.WithTimeout(ctx, ListenerMaxLifetime)
			for lifetime.Err() == nil {
				n, waitErr := conn.WaitForNotification(lifetime)
				if waitErr != nil {
					break
				}
				h.publish(n)
			}
			stop()
		}
		if conn != nil {
			closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Close(closeCtx)
			stop()
		}
		if ctx.Err() != nil {
			return
		}
		h.publish(nil) // Also recover work immediately on disconnect.
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if first {
		h.initialErr = ctx.Err()
		close(h.ready)
	}
}

// CloseNotificationConnections drains all sessions at server/test shutdown and
// refuses late background subscriptions. It must precede closing the main pool.
func CloseNotificationConnections(pool *pgxpool.Pool) {
	m := monitor(pool)
	if m == nil {
		return
	}
	m.listenerMu.Lock()
	m.listenerShutdown.Store(true)
	hubs := make([]*notificationHub, 0, len(m.listeners))
	for _, h := range m.listeners {
		h.mu.Lock()
		h.closing = true
		h.cancel()
		h.mu.Unlock()
		hubs = append(hubs, h)
	}
	m.listenerMu.Unlock()
	for _, h := range hubs {
		<-h.done
	}
	m.admission.mu.Lock()
	m.admission.closeLocked()
	m.admission.mu.Unlock()
}
