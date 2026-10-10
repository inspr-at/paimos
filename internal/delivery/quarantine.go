// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

var queueBranch = regexp.MustCompile(`^gh-readonly-queue/[A-Za-z0-9_./-]+/pr-([1-9][0-9]*)-([0-9a-f]{40})$`)

type queueSuite struct {
	ID     int64  `json:"id"`
	Head   string `json:"head_sha"`
	Branch string `json:"head_branch"`
	App    struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

type queueRun struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Head       string     `json:"head_sha"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	Completed  *time.Time `json:"completed_at"`
	Suite      queueSuite `json:"check_suite"`
	App        struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

type queueCheckEvent struct {
	Suite queueSuite `json:"check_suite"`
	Run   queueRun   `json:"check_run"`
}

type queueCheckReader interface {
	QueueChecks(context.Context, queueSuite, *queueRun) (string, []queueRun, error)
}

type QueueFailure struct {
	Check      string    `json:"check"`
	Conclusion string    `json:"conclusion"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
}

type EnqueueAllowed struct {
	Allowed  bool           `json:"allowed"`
	Reason   string         `json:"reason"`
	Failures []QueueFailure `json:"failures"`
}

func queueFailureKind(conclusion string) string {
	switch conclusion {
	case "failure":
		return "required_failure"
	case "cancelled", "timed_out", "startup_failure", "stale":
		return "infra"
	}
	return ""
}

// quarantineEvent is called only after the existing HMAC/installation boundary
// and duplicate check, under the observation lock. Network reads precede the
// tenant-fenced write. A failed/partial read stays retryable, never acknowledged.
func (m *Module) quarantineEvent(ctx context.Context, name, id string, raw []byte) (bool, error) {
	if name != "check_run" && name != "check_suite" {
		return false, nil
	}
	var e queueCheckEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return true, fail(400, "invalid queue check")
	}
	suite := e.Suite
	var run *queueRun
	if name == "check_run" {
		suite = e.Run.Suite
		// check_run includes the head and provider even when its nested suite
		// is abbreviated. Never trust a caller-supplied workflow label.
		suite.Head = e.Run.Head
		suite.App.Slug = e.Run.App.Slug
		run = &e.Run
	}
	branch := strings.TrimPrefix(suite.Branch, "refs/heads/")
	if !strings.HasPrefix(branch, "gh-readonly-queue/") {
		return false, nil
	}
	match := queueBranch.FindStringSubmatch(branch)
	if len(branch) > 255 || match == nil || strings.Contains(branch, "..") || !reviewgate.ValidSHA(suite.Head) || suite.ID <= 0 {
		return true, fail(400, "invalid queue check subject")
	}
	n, err := strconv.ParseInt(match[1], 10, 32)
	if err != nil {
		return true, fail(400, "invalid queue pull request")
	}
	workflow := ""
	runs := []queueRun{}
	if suite.App.Slug == "github-actions" {
		reader, ok := m.github.(queueCheckReader)
		if !ok {
			return true, errRead
		}
		workflow, runs, err = reader.QueueChecks(ctx, suite, run)
		if err != nil {
			return true, err
		}
		if workflow == "" || len(workflow) > 200 || len(runs) > 2000 {
			return true, errRead
		}
	}
	at := m.now()
	service := db.AllProjects(ctx, "authenticated delivery queue checks")
	err = db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
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
		// Ignored providers have no required workflow and need no head binding.
		if len(runs) > 0 {
			queued, err := queuedHeadTx(ctx, tx, m.config.Repository, n, suite.Head, match[2])
			if err != nil {
				return err
			}
			settings, err := settingsTx(ctx, tx, queued.Project)
			if err != nil {
				return err
			}
			failed := false
			for _, c := range runs {
				kind := queueFailureKind(c.Conclusion)
				if settings.RequiredWorkflow == nil || workflow != *settings.RequiredWorkflow || settings.RequiredChecks == nil || !slices.Contains(*settings.RequiredChecks, c.Name) || kind == "" || c.Status != "completed" {
					continue
				}
				if c.ID <= 0 || len(c.Name) == 0 || len(c.Name) > 200 || c.Head != suite.Head || c.Completed == nil || c.Completed.IsZero() {
					return errRead
				}
				tag, err := tx.Exec(ctx, `INSERT INTO delivery_queue_failures(tenant_id,repository,pull_request,head_sha,check_name,workflow,conclusion,kind,check_run_id,at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,check_run_id) DO NOTHING`, m.config.TenantID, queued.Repository, n, queued.Head, c.Name, workflow, c.Conclusion, kind, c.ID, c.Completed)
				if err != nil {
					return err
				}
				failed = failed || kind == "required_failure" && tag.RowsAffected() == 1
			}
			current, err := load(ctx, tx, queued.ID)
			if err != nil {
				return err
			}
			// A late completion is still historical evidence, but cannot change
			// the PR head now on screen. All writes precede event-counter locks.
			if failed && current != nil && current.Head == queued.Head {
				o := current.Observation
				o.QueueFailure, o.Queued, o.At, o.Settings = true, false, at, settings
				os = append(os, o)
			}
		}
		_, err := recordTx(ctx, tx, m.config.TenantID, record{ID: id, Event: name, Action: "completed", Repository: m.config.Repository, PR: &n, Head: suite.Head, Hash: digest(raw), At: at, Observations: os})
		return err
	})
	return true, err
}

// Recover the exact queued PR head from normalized history, even after a
// dequeue, group destruction or synchronize replaced the current projection.
// Never map a synthetic group SHA to the current PR head by number alone.
func queuedHeadTx(ctx context.Context, tx pgx.Tx, repository string, pr int64, group, suffix string) (Observation, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT o FROM delivery_github_events e CROSS JOIN LATERAL jsonb_array_elements(e.observations) o
 WHERE e.repository=$1 AND o->>'pull_request'=$2 AND o->>'queued'='true'
 AND (o->>'queue_head'=$3 OR (coalesce(o->>'queue_head','')='' AND o->>'head_sha'=$4))
 ORDER BY (o->>'queue_head'=$3) DESC,e.sequence DESC LIMIT 1`, repository, strconv.FormatInt(pr, 10), group, suffix).Scan(&raw)
	var o Observation
	if err == nil {
		err = json.Unmarshal(raw, &o)
		if err == nil && (!reviewgate.ValidSHA(o.Head) || o.PR == nil || *o.PR != pr || o.Repository != repository) {
			err = errRead
		}
	}
	return o, err
}

func (m *Module) enqueueAllowed(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	repo, head := q.Get("repository"), q.Get("head")
	n, err := strconv.ParseInt(q.Get("pr"), 10, 32)
	if err != nil || n < 1 || len(repo) > 255 || !reviewgate.ValidRepository(repo) || !reviewgate.ValidSHA(head) || len(q["repository"]) != 1 || len(q["pr"]) != 1 || len(q["head"]) != 1 {
		respondError(w, fail(400, "invalid enqueue subject"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := EnqueueAllowed{Allowed: true, Reason: fmt.Sprintf("head %s has no recorded required merge queue failure", head[:7]), Failures: []QueueFailure{}}
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		current, err := load(ctx, tx, stableID(p.TenantID, repo, subject(&n, nil)))
		if err != nil {
			return err
		}
		if current == nil {
			return pgx.ErrNoRows
		}
		if err := authz.RequireTx(ctx, tx, p, "delivery.read", scope(current.Project)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT check_name,conclusion,kind,at FROM delivery_queue_failures WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 ORDER BY at,check_run_id LIMIT 101`, repo, n, head)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f QueueFailure
			if err := rows.Scan(&f.Check, &f.Conclusion, &f.Kind, &f.At); err != nil {
				return err
			}
			out.Failures = append(out.Failures, f)
			if f.Kind == "required_failure" && out.Allowed {
				out.Allowed = false
				out.Reason = fmt.Sprintf("head %s failed required check %s in the merge queue; push a fix to queue again", head[:7], f.Check)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Failures) > 100 {
			return fail(422, "queue failure history exceeds 100 rows")
		}
		if out.Allowed && len(out.Failures) > 0 {
			out.Reason = fmt.Sprintf("head %s has only merge queue infrastructure failures; enqueue is allowed after a rerun", head[:7])
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
