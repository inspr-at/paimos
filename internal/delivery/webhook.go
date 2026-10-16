// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var deliveryID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
var queueRef = regexp.MustCompile(`^refs/heads/gh-readonly-queue/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$`)

type envelope struct {
	Action       string `json:"action"`
	Ref          string `json:"ref"`
	After        string `json:"after"`
	SHA          string `json:"sha"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		Name    string `json:"full_name"`
		Default string `json:"default_branch"`
	} `json:"repository"`
	Pull     crossreview.GitHubPull `json:"pull_request"`
	CheckRun struct {
		Head  string `json:"head_sha"`
		Pulls []struct {
			Number int64 `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite struct {
		Head  string `json:"head_sha"`
		Pulls []struct {
			Number int64 `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	Group struct {
		Head string `json:"head_sha"`
		Base string `json:"base_sha"`
		Ref  string `json:"head_ref"`
	} `json:"merge_group"`
}

// uniqueJSON follows the authenticated-event boundary: duplicate keys, deep
// nesting and trailing values cannot ambiguously change an authenticated field.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("JSON nesting limit")
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate JSON key")
			}
			seen[key] = true
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	} else if delim == '[' {
		for d.More() {
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	} else {
		return fmt.Errorf("invalid JSON")
	}
	_, err = d.Token()
	return err
}
func authenticate(name, id, signature string, raw, secret []byte) (envelope, error) {
	var e envelope
	if len(secret) < 32 || len(raw) == 0 || len(raw) > 2<<20 || !deliveryID.MatchString(id) || !slices.Contains([]string{"pull_request", "check_run", "check_suite", "merge_group", "push", "status", "workflow_run"}, name) {
		return e, fmt.Errorf("invalid envelope")
	}
	want, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	if err != nil || len(signature) != 71 || !strings.HasPrefix(signature, "sha256=") || !hmac.Equal(want, mac.Sum(nil)) {
		return e, fmt.Errorf("invalid signature")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err = uniqueJSON(dec, 0); err != nil {
		return e, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return e, fmt.Errorf("trailing JSON")
	}
	err = json.Unmarshal(raw, &e)
	return e, err
}
func validAction(name, action string) bool {
	switch name {
	case "pull_request":
		return slices.Contains([]string{"opened", "synchronize", "reopened", "edited", "closed", "enqueued", "dequeued"}, action)
	case "check_run", "check_suite":
		return action == "completed"
	case "merge_group":
		return action == "checks_requested" || action == "destroyed"
	case "status", "push":
		return action == ""
	case "workflow_run":
		return action == "requested" || action == "in_progress" || action == "completed"
	}
	return false
}

// projectWebhook consumes a receipt that has already passed authentication and
// installation binding. Its ledger remains separate from durable checkpoints.
func (m *Module) projectWebhook(ctx context.Context, name, id string, raw []byte, e envelope) (webhookOutcome, error) {
	// A workflow run is a flow fact, not a delivery observation. The audit
	// handler records the live checks after this acceptance.
	if name == "workflow_run" {
		return webhookNothingToRecord, nil
	}
	// The publisher's own aeon/admission status is an echo of a write already
	// committed. It must not take the observation lock or read GitHub, and it
	// must not invalidate the acknowledged revision.
	if name == "status" && admissionStatusEcho(raw) {
		return webhookNothingToRecord, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ctx, release, err := m.tryObservationLock(ctx)
	if err != nil {
		return webhookFailed, err
	}
	defer release()
	dup, err := m.duplicate(ctx, id)
	if err != nil {
		return webhookFailed, err
	}
	if dup {
		return webhookRecorded, nil
	}
	if handled, queueErr := m.quarantineEvent(ctx, name, id, raw); handled {
		if queueErr != nil {
			return webhookFailed, queueErr
		}
		return webhookRecorded, nil
	}
	if name == "push" && (e.Repository.Default == "" || e.Ref != "refs/heads/"+e.Repository.Default) {
		return webhookNothingToRecord, nil
	}
	var pulls []Pull
	var head string
	switch name {
	case "pull_request":
		if e.Pull.Number < 1 || !reviewgate.ValidSHA(e.Pull.Head.SHA) || !reviewgate.ValidSHA(e.Pull.Base.SHA) {
			return webhookFailed, webhookClass("invalid_pull")
		}
		p, er := m.github.Pull(ctx, e.Pull.Number)
		err = er
		if err == nil {
			pulls = []Pull{p}
		}
		head = e.Pull.Head.SHA
	case "check_run", "check_suite", "status":
		head = e.SHA
		ns := []int64{}
		if name == "check_run" {
			head = e.CheckRun.Head
			for _, p := range e.CheckRun.Pulls {
				ns = append(ns, p.Number)
			}
		}
		if name == "check_suite" {
			head = e.CheckSuite.Head
			for _, p := range e.CheckSuite.Pulls {
				ns = append(ns, p.Number)
			}
		}
		if !reviewgate.ValidSHA(head) || len(ns) > 100 {
			return webhookFailed, webhookClass("invalid_check_subjects")
		}
		err = db.InTenant(db.AllProjects(ctx, "delivery check subjects"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT pull_request FROM delivery_items WHERE repository=$1 AND head_sha=$2 AND pull_request IS NOT NULL ORDER BY pull_request LIMIT 101`, m.config.Repository, head)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var n int64
				if err = rows.Scan(&n); err != nil {
					return err
				}
				if !slices.Contains(ns, n) {
					ns = append(ns, n)
				}
			}
			if len(ns) > 100 {
				return errRead
			}
			return rows.Err()
		})
		if err == nil {
			for _, n := range ns {
				p, er := m.github.Pull(ctx, n)
				if er != nil {
					err = er
					break
				}
				if p.Head == head {
					pulls = append(pulls, p)
				}
			}
		}
	case "merge_group":
		head = e.Group.Head
		if !reviewgate.ValidSHA(head) || !reviewgate.ValidSHA(e.Group.Base) || !queueRef.MatchString(e.Group.Ref) || strings.Contains(e.Group.Ref, "..") {
			return webhookFailed, webhookClass("invalid_group")
		}
		if e.Action == "checks_requested" {
			// Persist a fail-closed publication even when membership resolution
			// fails. Receipt retries can fill the immutable member snapshot.
			err = m.webhookProjectionTx(db.AllProjects(ctx, "admission unknown group"), func(tx pgx.Tx) error {
				if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
					return err
				}
				return stageAdmissionGroupTx(ctx, tx, m.config.TenantID, m.config.Repository, head, nil, true, false)
			})
			if err != nil {
				return webhookFailed, err
			}
			resolver := m.github.Group
			if m.admissionGitHub != nil {
				if g, ok := m.github.(interface {
					AdmissionGroup(context.Context, string, string, string) ([]Pull, error)
				}); ok {
					resolver = g.AdmissionGroup
				}
			}
			pulls, err = resolver(ctx, head, e.Group.Base, e.Group.Ref)
		} else {
			err = db.InTenant(db.AllProjects(ctx, "delivery queue destroy subjects"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
				rows, err := tx.Query(ctx, `SELECT observation FROM delivery_items WHERE observation->>'queue_head'=$1 ORDER BY id LIMIT 101`, head)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var b []byte
					if err = rows.Scan(&b); err != nil {
						return err
					}
					var o Observation
					if err = json.Unmarshal(b, &o); err != nil {
						return err
					}
					if o.PR != nil {
						pulls = append(pulls, Pull{Number: *o.PR, Branch: o.Branch, Head: o.Head, Base: o.Base, Target: o.Target, Open: o.Open, Checks: o.Checks})
					}
				}
				if len(pulls) > 100 {
					return errRead
				}
				return rows.Err()
			})
		}
	case "push":
		head = e.After
		if !reviewgate.ValidSHA(head) {
			return webhookFailed, webhookClass("invalid_push")
		}
	}
	if err != nil || len(pulls) > 100 {
		if err == nil {
			err = webhookClass("observation_limit")
		}
		return webhookFailed, err
	}
	at := m.now()
	service := db.AllProjects(ctx, "authenticated delivery webhook")
	err = m.webhookProjectionTx(service, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_github_events WHERE delivery_id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		os := []Observation{}
		for _, p := range pulls {
			o, err := m.observationTx(ctx, tx, p, at)
			if err != nil {
				return err
			}
			if name == "pull_request" && e.Pull.Head.SHA == o.Head {
				switch e.Action {
				case "enqueued":
					o.Queued = p.Queued
				case "dequeued":
					o.Queued = false
					o.QueueHead = ""
				}
			}
			if name == "merge_group" {
				o.Queued = e.Action == "checks_requested"
				if o.Queued {
					o.QueueHead = head
				} else {
					o.QueueHead = ""
				}
			}
			os = append(os, o)
		}
		rec := record{ID: id, Event: name, Action: e.Action, Repository: m.config.Repository, Head: head, Hash: digest(raw), At: at, Observations: os, Fanout: name == "pull_request", AdmissionGroupResolved: m.admissionGitHub != nil}
		if len(pulls) == 1 {
			n := pulls[0].Number
			rec.PR = &n
		}
		_, err := recordTx(ctx, tx, m.config.TenantID, rec)
		return err
	})
	if err != nil {
		return webhookFailed, err
	}
	return webhookRecorded, nil
}

// Retry only a definite lock-timeout rollback, never an ambiguous commit or a
// GitHub read. Each attempt re-enters RLS and takes the tenant fence before
// reading current platform/admission facts. The observation session stays held
// across retries, preserving snapshot order. Three attempts and the enclosing
// projection deadline bound the wait; the normal lock_timeout stays in force.
func (m *Module) webhookProjectionTx(ctx context.Context, write func(pgx.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := db.InTenant(ctx, m.pool, m.config.TenantID, write)
		var postgres *pgconn.PgError
		if !errors.As(err, &postgres) || postgres.Code != "55P03" || attempt == 2 {
			return err
		}
		backoff := time.Duration(50+rand.Int64N(100)) * time.Millisecond * time.Duration(1<<attempt)
		delay := time.NewTimer(backoff)
		select {
		case <-delay.C:
		case <-ctx.Done():
			delay.Stop()
			return ctx.Err()
		}
	}
}

func admissionStatusEcho(raw []byte) bool {
	var body struct {
		Context string `json:"context"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Context == AdmissionContext
}

func (m *Module) recordMetricEventTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE delivery_metric_sources SET last_event_at=greatest(last_event_at,$2) WHERE repository=$1`, m.config.Repository, m.now())
	return err
}
