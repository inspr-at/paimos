// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

// The timestamp and delivery ID are authenticated along with the exact bytes.
// Receivers verify v1=hex(HMAC-SHA256(webhook_secret,
// timestamp + "." + Idempotency-Key + "." + body)), enforce their replay
// window, and persist the idempotency key before acknowledging a delivery.
func assistantWebhookRequest(ctx context.Context, work *DeliveryWork, body []byte, now time.Time) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, work.TargetRef, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(work.TargetSecret))
	_, _ = mac.Write([]byte(timestamp + "." + work.ID + "."))
	_, _ = mac.Write(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", work.ID)
	req.Header.Set("X-Aeon-Webhook-Timestamp", timestamp)
	req.Header.Set("X-Aeon-Webhook-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	if work.Adapter == "grok_bot_routine" {
		// Preserve the live Grok receiver contract during migration.
		req.Header.Set("Authorization", "Bearer "+work.TargetSecret)
	}
	return req, nil
}
