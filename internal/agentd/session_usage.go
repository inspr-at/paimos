// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

type sessionUsageAPI interface {
	ReportSessionUsage(context.Context, HarnessSession, sessionusage.UsageReport) error
}

func (r *Remote) ReportSessionUsage(ctx context.Context, s HarnessSession, report sessionusage.UsageReport) error {
	if s.ID == "" || s.ProjectID == "" || len(s.Lease) < 32 || len(s.Lease) > 256 {
		return errors.New("session usage requires registered worker binding")
	}
	return r.harnessWorker(ctx, s, "/usage", report, nil)
}

// sessionUsageReporter owns an in-memory, bounded normalized outbox. The exact
// pending receipt survives ambiguous HTTP failures; newer cumulative snapshots
// coalesce behind it. No stream, source identity, lease or payload is journaled.
// A new daemon never resumes a previous generation's capture.
type sessionUsageReporter struct {
	billing, accountID, plan string
	flushMu                  sync.Mutex
	mu                       sync.Mutex
	api                      sessionUsageAPI
	session                  HarnessSession
	latest                   map[string]sessionusage.UsageReport
	pending                  *sessionusage.UsageReport
	acked                    map[string]int64
	wake                     chan struct{}
	done                     chan struct{}
	cancel                   context.CancelFunc
	closed                   bool
	terminal                 error
	fenced                   func() bool
	archive                  func()
}

func newSessionUsageReporter(api sessionUsageAPI, session HarnessSession, fenced func() bool, archive func()) *sessionUsageReporter {
	r := &sessionUsageReporter{api: api, session: session, latest: map[string]sessionusage.UsageReport{}, acked: map[string]int64{},
		wake: make(chan struct{}, 1), done: make(chan struct{}), fenced: fenced, archive: archive}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		defer close(r.done)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			case <-timer.C:
			}
			// Stop and a wake can both be ready. Once finish has taken the
			// flush, the loop must not start another attempt.
			if ctx.Err() != nil {
				return
			}
			// The POST is not a child of the reporter cancel context. finish
			// cancels that context to stop the loop; cancelling this attempt
			// too drops a 410 the server already committed, and the post-join
			// flush then writes after archive.
			attempt, cancelAttempt := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = r.flush(attempt)
			cancelAttempt()
		}
	}()
	return r
}

func (r *sessionUsageReporter) submit(report sessionusage.UsageReport) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.terminal != nil {
		return
	}
	old, exists := r.latest[report.Model]
	if !exists && len(r.latest) >= 128 {
		return
	}
	// The parser supplies only counters/model/finality. Never forward arbitrary
	// adapter billing metadata, costs or vendor/source identifiers. Billing comes
	// only from the verified account probe or server-selected account settings.
	report = sessionusage.UsageReport{ServiceTier: report.ServiceTier, Model: report.Model, InputTokens: cloneCount(report.InputTokens),
		OutputTokens: cloneCount(report.OutputTokens), CachedInputTokens: cloneCount(report.CachedInputTokens),
		ReasoningTokens: cloneCount(report.ReasoningTokens), Provisional: report.Provisional, BillingMode: sessionusage.BillingMode(r.billing)}
	if r.accountID != "" {
		id := r.accountID
		report.AccountID = &id
	}
	if report.BillingMode == "subscription" && r.plan != "" {
		label := r.plan
		if len(label) <= 120 {
			report.SubscriptionLabel = &label
		}
	}
	before := old
	before.ReportID, before.Sequence = "", 0
	b, _ := json.Marshal(before)
	next, _ := json.Marshal(report)
	if exists && string(b) == string(next) {
		return
	}
	report.Sequence = old.Sequence + 1
	if report.Sequence > 1_000_000_000_000 {
		return
	}
	b, _ = json.Marshal(report)
	sum := sha256.Sum256(append([]byte("aeon-managed-usage-v1\x00"+r.session.ProjectID+"\x00"+r.session.ID+"\x00"), b...))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	report.ReportID = fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	r.latest[report.Model] = report
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func cloneCount(n *int64) *int64 {
	if n == nil {
		return nil
	}
	v := *n
	return &v
}

// flush has one owner: the background loop, then finish after joining it.
func (r *sessionUsageReporter) flush(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.fenced() {
			r.mu.Lock()
			r.terminal = ErrHarnessArchived
			r.mu.Unlock()
			return ErrHarnessArchived
		}
		r.mu.Lock()
		if r.terminal != nil {
			err := r.terminal
			r.mu.Unlock()
			return err
		}
		if r.pending == nil {
			keys := make([]string, 0, len(r.latest))
			for model := range r.latest {
				keys = append(keys, model)
			}
			slices.Sort(keys)
			for _, model := range keys {
				report := r.latest[model]
				if report.Sequence > r.acked[model] {
					r.pending = &report
					break
				}
			}
		}
		if r.pending == nil {
			r.mu.Unlock()
			return nil
		}
		report := *r.pending
		r.mu.Unlock()
		err := r.api.ReportSessionUsage(ctx, r.session, report)
		r.mu.Lock()
		if err == nil {
			r.acked[report.Model] = report.Sequence
			r.pending = nil
		}
		var status *client.StatusError
		if errors.Is(err, ErrHarnessArchived) || errors.As(err, &status) && status.Status >= 400 && status.Status < 500 && status.Status != http.StatusRequestTimeout && status.Status != http.StatusTooManyRequests {
			r.terminal = err
		}
		r.mu.Unlock()
		if errors.Is(err, ErrHarnessArchived) {
			r.archive()
		}
		if err != nil {
			return err
		}
	}
}

func (r *sessionUsageReporter) finish(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		err := r.flush(ctx)
		r.mu.Lock()
		terminal := r.terminal
		r.mu.Unlock()
		if err == nil || terminal != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
