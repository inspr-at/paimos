// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
)

// Hints never submit controls or content. The worker's existing claim, lease,
// ownership, expiry and receipt paths remain the only authority.
func (r *Remote) WatchHarness(ctx context.Context, session HarnessSession, wake func()) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
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
	watcher, ok := s.api.(interface {
		WatchHarness(context.Context, HarnessSession, func()) error
	})
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
	for ctx.Err() == nil {
		// Copy only the immutable generation and lease; live metadata belongs to
		// the heartbeat. Reconnect's initial wake covers missed queue changes.
		_ = watcher.WatchHarness(ctx, session, entry.wakeHarness)
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
