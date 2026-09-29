// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

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
		ID      string `json:"id"`
		Harness string `json:"harness"`
	}
	if err := rt.doCtx(ctx, http.MethodGet, harnessPath(projectID, session), nil, &status); err != nil {
		return err
	}
	harness := strings.TrimSuffix(adapter, "_resume")
	if !strings.EqualFold(status.ID, session) || status.Harness != harness {
		return usagef("adapter does not match the selected harness session")
	}
	deliver := rt.messagingDeliverer
	if deliver == nil {
		deliver = deliverLocalMessaging
	}
	initialBackoff := min(interval, 30*time.Second)
	backoff := initialBackoff
	for {
		q := url.Values{"session": {session}, "limit": {strconv.Itoa(limit)}, "wait_ms": {"0"}}
		if follow {
			q.Set("wait_ms", "25000")
		}
		if after != "" {
			q.Set("after", after)
		}
		var page inbox.Page
		if err := rt.doCtx(ctx, http.MethodGet, "/api/inbox/messages?"+q.Encode(), nil, &page); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		retry := false
		for _, msg := range page.Items {
			if !validUUID(msg.ID) || (msg.RecipientSessionID != nil && !strings.EqualFold(*msg.RecipientSessionID, session)) {
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
			if err := rt.doCtx(ctx, http.MethodPost, "/api/inbox/messages/"+url.PathEscape(msg.ID)+"/ack", nil, nil); err != nil {
				return err
			}
			after = strconv.FormatInt(msg.SentEventID, 10)
			backoff = initialBackoff
		}
		if !follow {
			if len(page.Items) == 0 {
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
