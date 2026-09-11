// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func requireNamedWorker(worker WorkerSelection) error {
	if strings.TrimSpace(worker.WorkerName) == "" {
		return fmt.Errorf("%w: agent execution requires named worker, account, profile and workspace", ErrInvalid)
	}
	return nil
}

func requireAgentWorkerBinding(worker WorkerSelection) error {
	if worker.RuntimeID == "" || worker.RuntimeGeneration == "" || worker.AccountLabel == "" ||
		worker.ProfileID == "" || worker.ProfileVersion == "" || worker.WorkspaceHandle == "" {
		return fmt.Errorf("%w: select runtime, account, profile and workspace first", ErrInvalid)
	}
	return nil
}

func knownAccountSchema(version int) bool {
	return version == 0 || version == lifecycleintents.RuntimeSchemaV1 ||
		version == lifecycleintents.AccountChoiceSchemaV2 || version == lifecycleintents.AccountScopeSchemaV3 ||
		version == lifecycleintents.AccountLifecycleSchemaV4
}

func workerMatchesRegistration(worker WorkerSelection, reg lifecycleintents.Registration) error {
	if !knownAccountSchema(reg.SchemaVersion) {
		return fmt.Errorf("%w: unknown runtime account schema", ErrInvalid)
	}
	if reg.SchemaVersion == lifecycleintents.AccountScopeSchemaV3 || reg.SchemaVersion == lifecycleintents.AccountLifecycleSchemaV4 {
		if len(reg.AccountScopes) == 0 {
			return fmt.Errorf("%w: missing account scopes", ErrInvalid)
		}
		seen := map[string]bool{}
		for _, scope := range reg.AccountScopes {
			if seen[scope.AccountLabel] {
				return fmt.Errorf("%w: ambiguous account scopes", ErrInvalid)
			}
			seen[scope.AccountLabel] = true
		}
	}
	if !reg.MatchScopeAtRevision(worker.AccountLabel, worker.AccountKey, worker.ProfileID, worker.ProfileVersion, true, worker.AttachmentRevision) {
		return fmt.Errorf("%w: account class, key and profile are not an advertised scope", ErrInvalid)
	}
	for _, workspace := range reg.Workspaces {
		if workspace.Handle == worker.WorkspaceHandle {
			return nil
		}
	}
	return fmt.Errorf("%w: workspace is not advertised on this runtime", ErrInvalid)
}

func (s *Service) loadLiveRegistration(ctx context.Context, tx *sql.Tx, projectID int64, worker WorkerSelection) (lifecycleintents.Registration, error) {
	var body string
	err := tx.QueryRowContext(ctx, `SELECT registration_json FROM lifecycle_runtimes
		WHERE id=? AND project_id=? AND generation=? AND expires_at>?`,
		worker.RuntimeID, projectID, worker.RuntimeGeneration, s.stamp()).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return lifecycleintents.Registration{}, fmt.Errorf("%w: runtime is not a current advertised generation", ErrInvalid)
	}
	if err != nil {
		return lifecycleintents.Registration{}, err
	}
	var reg lifecycleintents.Registration
	if json.Unmarshal([]byte(body), &reg) != nil {
		return lifecycleintents.Registration{}, fmt.Errorf("%w: runtime advertisement", ErrInvalid)
	}
	return reg, nil
}

func (s *Service) validateAgentWorker(ctx context.Context, tx *sql.Tx, projectID int64, worker WorkerSelection) error {
	if err := requireAgentWorkerBinding(worker); err != nil {
		return err
	}
	reg, err := s.loadLiveRegistration(ctx, tx, projectID, worker)
	if err != nil {
		return err
	}
	return workerMatchesRegistration(worker, reg)
}
