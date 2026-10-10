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

const emptyQueueInterval = 30 * time.Second

// PollScheduledOnce retains recovery and account health at the caller's five
// second cadence. Only an empty queue scan backs off; hints carry no authority.
// Paired callers must reconcile lifecycle and tombstones before calling it.
func (s *Supervisor) PollScheduledOnce(ctx context.Context) error {
	s.queuePollMu.Lock()
	defer s.queuePollMu.Unlock()
	now := s.capacityNow()
	dirty := s.queueDirty.Swap(false)
	scan := !now.Before(s.queueNext) || dirty
	nonempty := false
	err := s.poll(ctx, true, scan, &nonempty)
	if scan {
		delay := emptyQueueInterval
		if err != nil || nonempty {
			delay = 5 * time.Second
		}
		s.queueNext = now.Add(delay)
	}
	return err
}

func (s *Supervisor) QueueWake() <-chan struct{} { return s.queueWake }

func (s *Supervisor) wakeQueue() {
	s.queueDirty.Store(true)
	select {
	case s.queueWake <- struct{}{}:
	default:
	}
}

// RunQueueHints recovers missed hints with the safety scan and reconnect wake.
// An unsupported or unavailable stream never enables dispatch or suppresses it.
func (s *Supervisor) RunQueueHints(ctx context.Context) {
	watcher, ok := s.api.(interface {
		WatchQueue(context.Context, func()) error
	})
	if !ok {
		return
	}
	for ctx.Err() == nil {
		_ = watcher.WatchQueue(ctx, s.wakeQueue)
		timer := time.NewTimer(emptyQueueInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// WatchQueue consumes content-free frames using the daemon's existing run.read
// key. The context bounds connection, frame reads and total listener lifetime.
func (r *Remote) WatchQueue(ctx context.Context, wake func()) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Client.BaseURL+"/api/runs/queued/notifications", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Client.Token)
	req.Header.Set("Accept", "text/event-stream")
	hc := *r.Client.HTTP
	hc.Timeout = 0
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &client.StatusError{Status: res.StatusCode}
	}
	if strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]) != "text/event-stream" {
		return errors.New("invalid queue notification stream")
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	event, size := "", 0
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > 16<<10 {
			return errors.New("queue notification frame too large")
		}
		if line == "" {
			if event == "queue.wake" && ctx.Err() == nil {
				wake()
			}
			event, size = "", 0
		} else if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
