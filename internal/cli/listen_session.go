// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/inbox"
)

// Session delivery never claims a principal-wide target: that target may belong
// to a different generation. The operator supplies the exact local reference.
func (rt *runtime) listenSessionDelivery(projectID, project, session, adapter, refFile, after string, limit int, follow bool, interval time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	adapter = canonicalMessagingAdapter(adapter)
	ref := ""
	if adapter != "codex" && adapter != "claude_resume" {
		return usagef("session delivery supports codex or claude_resume; managed sessions use agentd drain")
	}
	if refFile == "" || refFile == "-" {
		return usagef("--session --deliver requires --target-ref-file for the exact local harness generation")
	}
	var err error
	ref, err = rt.readMessagingPrivateFile(refFile, false)
	if err != nil {
		return err
	}
	var status struct {
		ID         string     `json:"id"`
		Harness    string     `json:"harness"`
		Management string     `json:"management_mode"`
		Phase      string     `json:"phase"`
		StoppedAt  *time.Time `json:"stopped_at"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	if err := rt.doMessagingPoll(ctx, http.MethodGet, harnessPath(projectID, session), &status, follow, interval); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	harness := strings.TrimSuffix(adapter, "_resume")
	if !strings.EqualFold(status.ID, session) || status.Harness != harness {
		return usagef("adapter does not match the selected harness session")
	}
	if status.Management == "managed" {
		return usagef("managed sessions use agentd drain; the idle companion is for unmanaged sessions")
	}
	if status.Phase == "stopped" || status.StoppedAt != nil || status.ArchivedAt != nil {
		return usagef("the selected session has ended")
	}
	deliver := rt.messagingDeliverer
	if deliver == nil {
		deliver = deliverLocalMessaging
	}
	initialBackoff := min(interval, 30*time.Second)
	backoff := initialBackoff
	for {
		q := url.Values{"session": {session}, "exact_session": {"true"}, "limit": {strconv.Itoa(limit)}, "wait_ms": {"0"}}
		if follow {
			q.Set("wait_ms", "25000")
		}
		if after != "" {
			q.Set("after", after)
		}
		var page inbox.Page
		if err := rt.doMessagingPoll(ctx, http.MethodGet, "/api/inbox/messages?"+q.Encode(), &page, follow, interval); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		retry := false
		delivered := false
		for _, msg := range page.Items {
			if msg.RecipientSessionID == nil || !strings.EqualFold(*msg.RecipientSessionID, session) {
				after = strconv.FormatInt(msg.SentEventID, 10)
				continue
			}
			if !validUUID(msg.ID) {
				return fmt.Errorf("invalid session inbox response")
			}
			// Plain session messages carry no transport-level request; queue as simple.
			compat := inbox.CompatMessage{ID: msg.ID, SenderPrincipalID: msg.SenderPrincipalID, Body: sessionMessageFrame(msg), Level: "simple", CreatedAt: msg.CreatedAt}
			result, err := deliver(ctx, adapter, inbox.DeliveryWork{ID: msg.ID, ProjectKey: project, TargetRef: ref, MaximumLevel: "simple", Message: &compat})
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if !follow {
					return &exitError{code: 4, msg: "session delivery unavailable; message left pending"}
				}
				retry = true
				break
			}
			if result.EffectiveLevel != "simple" {
				return fmt.Errorf("invalid session delivery result")
			}
			// This endpoint invokes ConfirmSessionMessage, advancing delivery and receipt.
			if err := rt.doMessagingPoll(ctx, http.MethodPost, "/api/inbox/messages/"+url.PathEscape(msg.ID)+"/ack", nil, follow, interval); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			delivered = true
			after = strconv.FormatInt(msg.SentEventID, 10)
			backoff = initialBackoff
		}
		if !follow {
			if !delivered {
				return &exitError{code: 3}
			}
			return nil
		}
		if retry {
			if !waitMessagingRetry(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}
}

func waitMessagingRetry(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Retry only reads and idempotent acknowledgements. In particular, retry an ack
// in place after handoff, without reprinting or redelivering the message.
func (rt *runtime) doMessagingPoll(ctx context.Context, method, path string, dest any, follow bool, interval time.Duration) error {
	c, err := rt.api()
	if err != nil {
		return err
	}
	backoff := min(interval, 30*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.Do(ctx, method, path, nil, dest)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !follow || !transientMessagingError(err) {
			return rt.fail(err, c.Token)
		}
		if !waitMessagingRetry(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func transientMessagingError(err error) bool {
	var status *client.StatusError
	if errors.As(err, &status) {
		return status.Status == http.StatusRequestTimeout || status.Status == http.StatusTooManyRequests || status.Status >= 500
	}
	// url.Error itself implements net.Error even for permanent URL/TLS errors.
	var request *url.Error
	if errors.As(err, &request) {
		err = request.Err
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
