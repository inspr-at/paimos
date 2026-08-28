// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
	"github.com/inspr-at/paimos/backend/secretvault"
)

const (
	webhookDispatchInterval = 250 * time.Millisecond
	webhookLeaseDuration    = 15 * time.Second
	webhookMaxAttempts      = 8
)

type webhookWake struct {
	Event          string `json:"event"`
	Version        int    `json:"version"`
	Instance       string `json:"instance"`
	DeliveryID     string `json:"delivery_id"`
	Project        string `json:"project"`
	MessageID      string `json:"message_id"`
	Cursor         int64  `json:"cursor"`
	To             string `json:"to"`
	RequestedLevel string `json:"requested_level"`
	EffectiveLevel string `json:"effective_level"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	Content        string `json:"content"`
}

// webhookJob is one leased wake. The capability URL and the optional sender
// secret header are decrypted only for the outbound request and never copied
// into the delivery row, an error, or a log line.
type webhookJob struct {
	wake              webhookWake
	URL               string
	authHeaderName    string
	authHeaderValue   string
	authHeaderMissing bool
	attemptCount      int
}

// WebhookDispatcher owns server-side grok_bot_routine wake attempts.
type WebhookDispatcher struct {
	db       *sql.DB
	client   *http.Client
	instance string
	interval time.Duration
}

func NewWebhookDispatcher(database *sql.DB) *WebhookDispatcher {
	return &WebhookDispatcher{db: database, client: newWebhookHTTPClient(), instance: instanceName(), interval: webhookDispatchInterval}
}

// StartWebhookDispatcher starts the instance-local outbox worker. It is safe
// to run when no webhook targets exist.
func StartWebhookDispatcher(database *sql.DB) {
	dispatcher := NewWebhookDispatcher(database)
	go func() {
		_ = dispatcher.Run(context.Background())
	}()
}

func (d *WebhookDispatcher) Run(ctx context.Context) error {
	for {
		worked, err := d.DispatchOne(ctx)
		if err != nil && ctx.Err() == nil {
			// Errors are represented on the delivery row by DispatchOne. Keep the
			// process alive and avoid logging content, target URLs, or vendor output.
		}
		if worked {
			continue
		}
		timer := time.NewTimer(d.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (d *WebhookDispatcher) DispatchOne(ctx context.Context) (bool, error) {
	job, err := d.leaseWebhook(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if job.authHeaderMissing {
		// The adapter authenticates every wake with a sender secret and this
		// target version has none (registered before M157). Fail closed without
		// contacting the endpoint; the operator registers a new version with
		// the sender key and requeues.
		d.failWebhook(ctx, job.wake.DeliveryID, job.attemptCount, "target_secret_missing", false, 0)
		return true, errors.New("webhook target has no sender secret")
	}
	payload, err := json.Marshal(job.wake)
	if err != nil {
		d.failWebhook(ctx, job.wake.DeliveryID, job.attemptCount, "payload_invalid", false, 0)
		return true, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.URL, strings.NewReader(string(payload)))
	if err != nil {
		d.failWebhook(ctx, job.wake.DeliveryID, job.attemptCount, "target_invalid", false, 0)
		return true, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", job.wake.DeliveryID)
	if job.authHeaderName != "" {
		req.Header.Set(job.authHeaderName, job.authHeaderValue)
	}
	resp, err := d.client.Do(req) // #nosec G704 -- target is operator-registered, encrypted, allowlisted, and revalidated by the transport.
	if err != nil {
		d.failWebhook(ctx, job.wake.DeliveryID, job.attemptCount, "transport_error", true, 0)
		return true, err
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, d.completeWebhook(ctx, job.wake)
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly ||
		resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		retryAfter := boundedRetryAfter(resp.Header.Get("Retry-After"))
		d.failWebhook(ctx, job.wake.DeliveryID, job.attemptCount, "http_transient", true, retryAfter)
		return true, fmt.Errorf("webhook returned transient HTTP %d", resp.StatusCode)
	}
	d.failWebhook(ctx, job.wake.DeliveryID, job.attemptCount, "http_4xx", false, 0)
	return true, fmt.Errorf("webhook returned terminal HTTP %d", resp.StatusCode)
}

func (d *WebhookDispatcher) leaseWebhook(ctx context.Context) (*webhookJob, error) {
	serverAdapters := harnessplugin.Names(harnessplugin.ModeServer, harnessplugin.KindHTTPSWebhook)
	if len(serverAdapters) == 0 {
		return nil, sql.ErrNoRows
	}
	// A production transaction is BEGIN IMMEDIATE. Probe through the database
	// first so the common empty-queue poll remains a WAL reader instead of
	// taking SQLite's sole writer slot every 250 ms. A positive probe is only a
	// hint: the immediate transaction below re-runs the exact FIFO selector and
	// is still the sole authority for leasing work.
	if _, err := nextWebhookDeliveryID(ctx, d.db, d.instance, serverAdapters); err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	deliveryID, err := nextWebhookDeliveryID(ctx, tx, d.instance, serverAdapters)
	if err != nil {
		return nil, err
	}
	leaseUntil := time.Now().UTC().Add(webhookLeaseDuration).Format("2006-01-02T15:04:05.000Z")
	result, err := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='leased',attempt_count=attempt_count+1,
		lease_until=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE delivery_id=?
		AND ((state IN ('pending','retry') AND next_attempt_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 OR (state='leased' AND lease_until<=strftime('%Y-%m-%dT%H:%M:%fZ','now')))`, leaseUntil, deliveryID)
	if err != nil {
		return nil, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return nil, sql.ErrNoRows
	}
	var job webhookJob
	var cipher, secretCipher []byte
	var from, project, task, body, adapter string
	var hop int
	var maximumLevel, primaryID, chosenID string
	err = tx.QueryRowContext(ctx, `SELECT d.delivery_id,d.attempt_count,d.requested_level,
		COALESCE(d.primary_target_id,''),`+selectedDeliveryTargetSQL+`,
		t.maximum_level,t.adapter,t.target_ref_cipher,t.target_secret_cipher,am.id,am.message_id,am.context_id,am.task_id,am.from_address,am.to_address,am.hop_count,am.body
		FROM agent_message_deliveries d JOIN agent_messages am ON am.id=d.message_row_id
		JOIN agent_message_targets t ON t.id=`+selectedDeliveryTargetSQL+`
		WHERE d.delivery_id=?`, deliveryID).Scan(&job.wake.DeliveryID, &job.attemptCount, &job.wake.RequestedLevel,
		&primaryID, &chosenID, &maximumLevel, &adapter, &cipher, &secretCipher, &job.wake.Cursor, &job.wake.MessageID, &project, &task,
		&from, &job.wake.To, &hop, &body)
	if err != nil {
		return nil, err
	}
	plain, err := secretvault.Decrypt(targetSecretDomain, cipher)
	if err != nil {
		return nil, err
	}
	job.URL = string(plain)
	headerName, headerPrefix, secretRequired, err := harnessplugin.SecretHeader(adapter)
	if err != nil {
		return nil, err
	}
	if secretRequired {
		if len(secretCipher) == 0 {
			job.authHeaderMissing = true
		} else {
			secret, err := secretvault.Decrypt(targetSenderSecretDomain, secretCipher)
			if err != nil {
				return nil, err
			}
			job.authHeaderName = headerName
			job.authHeaderValue = headerPrefix + string(secret)
		}
	}
	job.wake.Event = "agent_message.available"
	job.wake.Version = 1
	job.wake.Instance = d.instance
	job.wake.Project = project
	job.wake.EffectiveLevel = "simple"
	if job.wake.RequestedLevel == "steer" {
		job.wake.FallbackReason = "unsupported"
	} else if primaryID == "" && chosenID != "" {
		job.wake.FallbackReason = "target_missing"
	} else if maximumLevel == "simple" && job.wake.RequestedLevel == "steer" {
		job.wake.FallbackReason = "policy_capped"
	}
	job.wake.Content = (FramedMessage{From: from, Project: project, Issue: task, Hop: hop, Body: body}).FullMessage()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

type webhookDeliveryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func nextWebhookDeliveryID(ctx context.Context, queryer webhookDeliveryQueryer, instance string, serverAdapters []string) (string, error) {
	var deliveryID string
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serverAdapters)), ",")
	query := `SELECT d.delivery_id FROM agent_message_deliveries d
		JOIN agent_messages am ON am.id=d.message_row_id
		JOIN agent_message_targets t ON t.id=` + selectedDeliveryTargetSQL + `
		WHERE d.instance=? AND t.instance=d.instance AND t.adapter IN (` + placeholders + `)
		AND ((d.state IN ('pending','retry') AND d.next_attempt_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 OR (d.state='leased' AND d.lease_until<=strftime('%Y-%m-%dT%H:%M:%fZ','now')))
		AND NOT EXISTS (
		 SELECT 1 FROM agent_message_deliveries older JOIN agent_messages older_message ON older_message.id=older.message_row_id
		 WHERE older.instance=d.instance AND older_message.to_address=am.to_address AND older_message.id<am.id
		 AND older.state NOT IN ('handed_off','dead'))
		ORDER BY am.to_address,am.id LIMIT 1`
	queryArgs := make([]any, 0, len(serverAdapters)+1)
	queryArgs = append(queryArgs, instance)
	for _, adapter := range serverAdapters {
		queryArgs = append(queryArgs, adapter)
	}
	if err := queryer.QueryRowContext(ctx, query, queryArgs...).Scan(&deliveryID); err != nil {
		return "", err
	}
	return deliveryID, nil
}

func (d *WebhookDispatcher) completeWebhook(ctx context.Context, wake webhookWake) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var projectID, agentID int64
	var address string
	if err := tx.QueryRowContext(ctx, `SELECT pa.project_id,am.to_agent_id,am.to_address
		FROM agent_message_deliveries d JOIN agent_messages am ON am.id=d.message_row_id
		JOIN project_agents pa ON pa.id=am.to_agent_id WHERE d.delivery_id=? AND d.instance=?`,
		wake.DeliveryID, d.instance).Scan(&projectID, &agentID, &address); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='handed_off',effective_level='simple',
		fallback_reason=?,handed_off_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),lease_until=NULL,last_error_code='',
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE delivery_id=? AND state='leased'`,
		wake.FallbackReason, wake.DeliveryID)
	if err != nil {
		return err
	}
	updated, _ := result.RowsAffected()
	if updated != 1 {
		return coded("agent_message_delivery_raced", "webhook delivery lease changed before completion")
	}
	if _, err := ackInboxTx(ctx, tx, projectID, address, agentID, wake.Cursor); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *WebhookDispatcher) failWebhook(ctx context.Context, deliveryID string, attempt int, code string, retry bool, retryAfter time.Duration) {
	state := "blocked"
	if retry {
		state = "retry"
	}
	if attempt >= webhookMaxAttempts {
		state = "dead"
	}
	next := time.Now().UTC()
	if state == "retry" {
		delay := retryAfter
		if delay <= 0 {
			delay = webhookBackoff(deliveryID, attempt)
		}
		next = next.Add(delay)
	}
	_, _ = d.db.ExecContext(ctx, `UPDATE agent_message_deliveries SET state=?,lease_until=NULL,last_error_code=?,next_attempt_at=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE delivery_id=? AND state='leased'`,
		state, code, next.Format("2006-01-02T15:04:05.000Z"), deliveryID)
}

func webhookBackoff(deliveryID string, attempt int) time.Duration {
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 5 {
		shift = 5
	}
	base := time.Second * time.Duration(1<<shift)
	digest := sha256.Sum256([]byte(deliveryID + ":" + strconv.Itoa(attempt)))
	jitter := time.Duration(binary.BigEndian.Uint16(digest[:2])%1000) * base / 4000
	if base+jitter > time.Minute {
		return time.Minute
	}
	return base + jitter
}

func boundedRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		if seconds > 60 {
			seconds = 60
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := time.Until(when)
	if delay < 0 {
		return 0
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func newWebhookHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12}, // #nosec G402 -- modern minimum is pinned.
		ResponseHeaderTimeout: 10 * time.Second,
		DisableCompression:    true,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !harnessplugin.WebhookHostAllowed(host) {
			return nil, errors.New("webhook destination denied")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("webhook DNS failed")
		}
		for _, candidate := range addresses {
			if !harnessplugin.WebhookIPAllowed(candidate.IP) {
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
		}
		return nil, errors.New("webhook destination has no allowed reachable address")
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}
