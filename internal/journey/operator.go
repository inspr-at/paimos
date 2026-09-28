// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/operatoractor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

const operatorUsage = "usage: aeon journey mark-disposable --tenant SLUG --project KEY | aeon journey seed --tenant SLUG --project KEY --to-stage build|candidate|deploy [--brief 1|2|3] [--production --confirm-project KEY]"

// OperatorResult is the value-free result of a host-only journey command.
type OperatorResult struct {
	ProjectKey    string  `json:"project_key"`
	Disposable    bool    `json:"disposable"`
	Stage         string  `json:"stage"`
	ReleaseID     *string `json:"release_id"`
	TargetReached bool    `json:"target_reached"`
	PendingAction string  `json:"pending_action,omitempty"`
	Already       bool    `json:"already"`
}

// RunOperator implements the host CLI's `aeon journey` subcommands. The
// coordinator wires it into cmd/aeon; no HTTP route exposes these operations.
// Production requires an explicit flag and matching project confirmation.
func RunOperator(ctx context.Context, pool *pgxpool.Pool, args []string, stdout io.Writer) error {
	opts, err := parseOperator(args)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, productionContextKey{}, opts.production)
	var out OperatorResult
	if opts.command == "mark-disposable" {
		out, err = markDisposable(ctx, pool, opts.slug, opts.project, opts.production)
	} else {
		out, err = seedDisposableWithBrief(ctx, pool, opts.slug, opts.project, opts.toStage, opts.brief, opts.production)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(out)
}

type operatorOptions struct {
	command, slug, project, toStage, brief string
	production                             bool
}

// ValidateOperator checks the host CLI before it opens a database connection.
func ValidateOperator(args []string) error {
	_, err := parseOperator(args)
	return err
}

func parseOperator(args []string) (operatorOptions, error) {
	if os.Getenv("AEON_ENV") != "dev" && os.Getenv("AEON_ENV") != "prod" {
		return operatorOptions{}, errors.New("journey operator commands require AEON_ENV=dev or AEON_ENV=prod with --production and --confirm-project")
	}
	if len(args) == 0 || args[0] != "mark-disposable" && args[0] != "seed" {
		return operatorOptions{}, errors.New(operatorUsage)
	}
	fs := flag.NewFlagSet("aeon journey "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	slug := fs.String("tenant", "", "tenant slug")
	project := fs.String("project", "", "project node key")
	production := fs.Bool("production", false, "allow production operator command")
	confirm := fs.String("confirm-project", "", "exact project node key confirmation")
	var toStage, brief *string
	if args[0] == "seed" {
		toStage = fs.String("to-stage", "", "target stage")
		brief = fs.String("brief", "", "fixed disposable intake brief: 1, 2 or 3")
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *slug == "" || *project == "" ||
		args[0] == "seed" && ((*toStage != "build" && *toStage != "candidate" && *toStage != "deploy") || (*brief != "" && *brief != "1" && *brief != "2" && *brief != "3")) {
		return operatorOptions{}, errors.New(operatorUsage)
	}
	if os.Getenv("AEON_ENV") == "prod" && (!*production || *confirm == "") {
		return operatorOptions{}, errors.New("production journey command requires --production and --confirm-project matching --project")
	}
	if *confirm != "" && *confirm != *project {
		return operatorOptions{}, errors.New("--confirm-project must exactly match --project")
	}
	if os.Getenv("AEON_ENV") == "dev" && (*production || *confirm != "") {
		return operatorOptions{}, errors.New("production flags require AEON_ENV=prod")
	}
	stage := ""
	briefNumber := ""
	if toStage != nil {
		stage = *toStage
		briefNumber = *brief
	}
	return operatorOptions{args[0], *slug, *project, stage, briefNumber, *production}, nil
}

type productionContextKey struct{}

func operatorContext(ctx context.Context, production bool) (context.Context, error) {
	if os.Getenv("AEON_ENV") != "dev" && !(os.Getenv("AEON_ENV") == "prod" && production) {
		return nil, errors.New("journey operator commands require AEON_ENV=dev or confirmed production host CLI")
	}
	return db.AllProjects(ctx, "disposable journey operator command"), nil
}

func projectByKey(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("project node key is required")
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.key=$1 AND n.deleted_at IS NULL AND k.slug='project'`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("project %q not found", key)
	}
	return id, err
}

// MarkDisposable records an explicit, irreversible disposable marker in dev. A live
// or previously released project cannot be marked. A replay makes no changes.
func MarkDisposable(ctx context.Context, pool *pgxpool.Pool, slug, key string) (OperatorResult, error) {
	return markDisposable(ctx, pool, slug, key, false)
}

func markDisposable(ctx context.Context, pool *pgxpool.Pool, slug, key string, production bool) (OperatorResult, error) {
	ctx, err := operatorContext(ctx, production)
	if err != nil {
		return OperatorResult{}, err
	}
	if pool == nil {
		return OperatorResult{}, errors.New("database pool is required")
	}
	var out OperatorResult
	err = db.InTransaction(ctx, pool, func(ctx context.Context) error {
		tid, err := tenantbootstrap.ResolveSlug(ctx, pool, slug)
		if err != nil {
			return err
		}
		return db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
			id, err := projectByKey(ctx, tx, key)
			if err != nil {
				return err
			}
			var already bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_disposable_projects WHERE project_node_id=$1::uuid)`, id).Scan(&already); err != nil {
				return err
			}
			var live bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases WHERE project_node_id=$1::uuid AND state IN ('released','superseded','deploying','access'))`, id).Scan(&live); err != nil {
				return err
			}
			if live {
				return errors.New("cannot mark a deployed or released project disposable")
			}
			if !already {
				actorID, err := operatoractor.EnsureWithProduction(ctx, tx, tid, production)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO journey_disposable_projects(tenant_id,project_node_id,marked_by_principal_id) VALUES($1::uuid,$2::uuid,$3::uuid)`, tid, id, actorID); err != nil {
					return err
				}
				p := tenant.Principal{ID: actorID, TenantID: tid, Kind: tenant.Agent}
				if _, err := writeEvent(ctx, tx, p, id, "journey.disposable_marked", nil, map[string]any{"project_node_id": id, "disposable": true}); err != nil {
					return err
				}
			}
			out = OperatorResult{ProjectKey: key, Disposable: true, Already: already}
			return nil
		})
	})
	return out, err
}

// SeedDisposable advances existing, real project/release work through journey
// actions in one transaction. It may waive the build gate on an explicitly
// disposable project, but never fabricates a requirement agreement, completed
// ticket, candidate review, deployment decision or Pharos evidence. Missing
// work prerequisites roll back all transitions. A pending human gate commits
// valid preparation and returns the next action; rerun after the UI decision.
func SeedDisposable(ctx context.Context, pool *pgxpool.Pool, slug, key, target string) (OperatorResult, error) {
	return seedDisposable(ctx, pool, slug, key, target, false)
}

func seedDisposable(ctx context.Context, pool *pgxpool.Pool, slug, key, target string, production bool) (OperatorResult, error) {
	return seedDisposableWithBrief(ctx, pool, slug, key, target, "", production)
}

// SeedDisposableBrief completes one fixed intake on a disposable project and
// advances to the requested stage without deciding candidate or deploy gates.
func SeedDisposableBrief(ctx context.Context, pool *pgxpool.Pool, slug, key, target, brief string) (OperatorResult, error) {
	return seedDisposableWithBrief(ctx, pool, slug, key, target, brief, false)
}

func seedDisposableWithBrief(ctx context.Context, pool *pgxpool.Pool, slug, key, target, brief string, production bool) (OperatorResult, error) {
	ctx, err := operatorContext(ctx, production)
	if err != nil {
		return OperatorResult{}, err
	}
	if pool == nil || target != "build" && target != "candidate" && target != "deploy" {
		return OperatorResult{}, errors.New("database pool and build|candidate|deploy target are required")
	}
	if brief != "" {
		if _, ok := disposableBriefs[brief]; !ok {
			return OperatorResult{}, errors.New("brief must be 1, 2 or 3")
		}
		ctx = context.WithValue(ctx, briefContextKey{}, brief)
	}
	ctx = events.WithOperatorAnnotation(ctx, events.OperatorAnnotation{Production: production, Brief: brief})
	var out OperatorResult
	err = db.InTransaction(ctx, pool, func(ctx context.Context) error {
		tid, err := tenantbootstrap.ResolveSlug(ctx, pool, slug)
		if err != nil {
			return err
		}
		var id, actorID string
		if err := db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
			var err error
			id, err = projectByKey(ctx, tx, key)
			if err != nil {
				return err
			}
			var disposable bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_disposable_projects WHERE project_node_id=$1::uuid)`, id).Scan(&disposable); err != nil {
				return err
			}
			if !disposable {
				return errors.New("project is not disposable")
			}
			if brief != "" {
				if err := checkBriefSeed(ctx, tx, id, brief); err != nil {
					return err
				}
			}
			actorID, err = operatoractor.EnsureWithProduction(ctx, tx, tid, production)
			return err
		}); err != nil {
			return err
		}
		p := tenant.Principal{ID: actorID, TenantID: tid, Kind: tenant.Agent}
		m := &Module{pool: pool, inTenant: db.InTenant}
		changed := false
		if brief != "" {
			changed, err = prepareBriefSeed(ctx, pool, m, p, id, brief, target)
			if err != nil {
				return err
			}
		}
		for step := 0; step <= 3; step++ {
			view, err := m.read(ctx, p, id)
			if err != nil {
				return err
			}
			state, err := releaseState(ctx, pool, tid, view.CurrentReleaseID)
			if err != nil {
				return err
			}
			if view.NextAction.Key == "approve_candidate" && pendingSeedGate(view.NextAction.Key, target) {
				out = OperatorResult{ProjectKey: key, Disposable: true, Stage: view.Stage, ReleaseID: view.CurrentReleaseID, PendingAction: view.NextAction.Key, Already: !changed}
				return nil
			}
			reached := targetReached(state, view.Stage, target)
			if reached && target == "deploy" {
				reached, err = deployGatesLive(ctx, pool, tid, id)
				if err != nil {
					return err
				}
			}
			if reached {
				out = OperatorResult{ProjectKey: key, Disposable: true, Stage: view.Stage, ReleaseID: view.CurrentReleaseID, TargetReached: true, Already: !changed}
				return nil
			}
			if pendingSeedGate(view.NextAction.Key, target) {
				out = OperatorResult{ProjectKey: key, Disposable: true, Stage: view.Stage, ReleaseID: view.CurrentReleaseID, PendingAction: view.NextAction.Key, Already: !changed}
				return nil
			}
			action := seedNextAction(view, target)
			if action == "" {
				return fmt.Errorf("cannot seed to %s: %s (%s)", target, view.NextAction.Key, view.NextAction.Reason)
			}
			if step == 3 {
				return errors.New("journey seed exceeded three actions")
			}
			in := actionWrite{Action: action, ExpectedRevision: view.Revision, IdempotencyKey: fmt.Sprintf("operator-seed:%s:%d", action, view.Revision), ReleaseID: view.CurrentReleaseID}
			if _, err := m.actWithMode(ctx, p, id, in, true); err != nil {
				return fmt.Errorf("seed %s: %w", action, err)
			}
			changed = true
		}
		return errors.New("journey seed exceeded three actions")
	})
	return out, err
}

func pendingSeedGate(action, target string) bool {
	return action == "approve_candidate" && (target == "candidate" || target == "deploy") ||
		action == "approve_deploy" && target == "deploy"
}

func releaseState(ctx context.Context, pool *pgxpool.Pool, tid string, id *string) (string, error) {
	if id == nil {
		return "", nil
	}
	var state string
	err := db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, *id).Scan(&state)
	})
	return state, err
}

func deployGatesLive(ctx context.Context, pool *pgxpool.Pool, tid, projectID string) (bool, error) {
	var live bool
	err := db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
		f, err := loadFacts(ctx, tx, projectID, false)
		if err != nil {
			return err
		}
		live = f.CandidateGateID != "" && f.DeployGateID != "" &&
			f.GateLiveByID[f.CandidateGateID] && f.GateLiveByID[f.DeployGateID]
		return nil
	})
	return live, err
}

func targetReached(state, stage, target string) bool {
	switch target {
	case "build":
		return stage == "build" || state == "building" || state == "candidate" || state == "deploying" || state == "access" || state == "released" || state == "superseded"
	case "candidate":
		return state == "candidate" || state == "deploying" || state == "access" || state == "released" || state == "superseded"
	case "deploy":
		return state == "deploying" || state == "access" || state == "released" || state == "superseded"
	}
	return false
}

func seedNextAction(v Journey, target string) string {
	switch v.NextAction.Key {
	case "open_first_release":
		return "open_first_release"
	case "start_build":
		return "start_build"
	case "mark_candidate":
		if target != "build" {
			return "mark_candidate"
		}
	}
	return ""
}
