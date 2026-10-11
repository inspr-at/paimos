// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
)

const harnessSafetyPoll = 30 * time.Second
const harnessFallbackPoll = 2 * time.Second
const harnessStreamIdle = 45 * time.Second

// Hints never submit controls or content. The worker's existing claim, lease,
// ownership, expiry and receipt paths remain the only authority.
func (r *Remote) WatchHarness(ctx context.Context, session HarnessSession, wake func()) error {
	// Let the server recycle its bounded listener. Its 15-second pings also
	// bound silent transport failures without repeatedly reopening healthy SSE.
	ctx, cancel := context.WithTimeout(ctx, db.ListenerMaxLifetime+time.Minute)
	defer cancel()
	connectTimer := time.AfterFunc(10*time.Second, cancel)
	defer connectTimer.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Client.BaseURL+harnessPath(session)+"/notifications", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Client.Token)
	req.Header.Set("X-Aeon-Worker-Lease", session.Lease)
	req.Header.Set("Accept", "text/event-stream")
	hc := *r.Client.HTTP
	hc.Timeout = 0 // bounded above by the stream context, including connection
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := hc.Do(req)
	connectTimer.Stop()
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &client.StatusError{Status: res.StatusCode}
	}
	if strings.Split(res.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		return errors.New("invalid harness notification stream")
	}
	idleTimer := time.AfterFunc(harnessStreamIdle, cancel)
	defer idleTimer.Stop()
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	event := ""
	frameBytes := 0
	for scanner.Scan() {
		line := scanner.Text()
		frameBytes += len(line) + 1
		if frameBytes > 16<<10 {
			return errors.New("harness notification frame too large")
		}
		if line == "" {
			if event == "harness.wake" || event == "stream.ping" {
				idleTimer.Reset(harnessStreamIdle)
			}
			if event == "harness.wake" && ctx.Err() == nil {
				wake()
			}
			event, frameBytes = "", 0
		} else if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

type harnessWatcher interface {
	WatchHarness(context.Context, HarnessSession, func()) error
}

func (entry *owned) wakeHarness() {
	entry.mu.Lock()
	hints := entry.harnessWake
	entry.mu.Unlock()
	select {
	case hints <- struct{}{}:
	default:
	}
}

func (s *Supervisor) watchHarness(entry *owned) {
	watcher, ok := s.api.(harnessWatcher)
	if !ok {
		return
	}
	entry.mu.Lock()
	session, done := entry.harness, entry.monitorDone
	entry.mu.Unlock()
	ctx, cancel := context.WithCancel(s.lifetime)
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	s.watchHarnessLoop(ctx, entry, session, watcher, time.Now, waitHarnessReconnect, jitterHarnessReconnect)
}

func jitterHarnessReconnect(backoff time.Duration) time.Duration {
	return backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
}

func waitHarnessReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Supervisor) watchHarnessLoop(ctx context.Context, entry *owned, session HarnessSession, watcher harnessWatcher, now func() time.Time, wait func(context.Context, time.Duration) bool, jitter func(time.Duration) time.Duration) {
	backoff := time.Second
	for ctx.Err() == nil {
		// Copy only the immutable generation and lease; live metadata belongs to
		// the heartbeat. Reconnect's initial wake covers missed queue changes.
		started := now()
		connected := false
		active := true
		_ = watcher.WatchHarness(ctx, session, func() {
			entry.mu.Lock()
			if !active || ctx.Err() != nil {
				entry.mu.Unlock()
				return
			}
			connected = true
			entry.harnessConnected = true
			entry.mu.Unlock()
			entry.wakeHarness()
		})
		entry.mu.Lock()
		active = false
		wasConnected := connected
		entry.harnessConnected = false
		entry.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		// Reconcile immediately on disconnect, then keep the two-second poll
		// independent of reconnect backoff. Hints never confer authority.
		entry.wakeHarness()
		if wasConnected && now().Sub(started) >= harnessStreamIdle {
			backoff = time.Second
		}
		if !wait(ctx, jitter(backoff)) {
			return
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}
