// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/inspr-at/paimos/backend/auth"
)

func (s *Service) currentAuthority(ctx context.Context, tx *sql.Tx, actor Actor, projectID int64, write bool) (ProjectAuthority, error) {
	if projectID <= 0 {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrInvalid)
	}
	if actor.Kind != string(auth.PrincipalSession) || actor.UserID <= 0 || actor.SessionCredentialID == "" || actor.Impersonated || actor.APIKeyID != 0 {
		if write {
			return ProjectAuthority{}, fmt.Errorf("%w: human session required", ErrForbidden)
		}
		if actor.UserID <= 0 {
			return ProjectAuthority{}, fmt.Errorf("%w: authenticated actor required", ErrUnauthorized)
		}
	}
	var status string
	var userStatus, userRole string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=?`, projectID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return ProjectAuthority{}, fmt.Errorf("%w: project", ErrNotFound)
		}
		return ProjectAuthority{}, err
	}
	if status == "deleted" {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrNotFound)
	}
	if err := tx.QueryRowContext(ctx, `SELECT status,role FROM users WHERE id=?`, actor.UserID).Scan(&userStatus, &userRole); err != nil {
		return ProjectAuthority{}, fmt.Errorf("%w: actor", ErrUnauthorized)
	}
	if userStatus != "active" {
		return ProjectAuthority{}, fmt.Errorf("%w: actor", ErrUnauthorized)
	}
	authz := ProjectAuthority{ProjectID: projectID, Status: status}
	isAdmin := userRole == "admin" || userRole == "super_admin"
	if isAdmin {
		authz.CanView, authz.CanEdit = true, true
	} else {
		var level sql.NullString
		_ = tx.QueryRowContext(ctx, `SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`, actor.UserID, projectID).Scan(&level)
		switch {
		case userRole == "external":
			if level.String == "viewer" {
				authz.CanView = true
			}
			if level.String == "editor" {
				authz.CanView, authz.CanEdit = true, true
			}
		case userRole == "member":
			if !level.Valid || level.String == "editor" {
				authz.CanView, authz.CanEdit = true, true
			} else if level.String == "viewer" {
				authz.CanView = true
			}
		}
	}
	if !authz.CanView {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrNotFound)
	}
	if write && !authz.CanEdit {
		return ProjectAuthority{}, fmt.Errorf("%w: edit", ErrForbidden)
	}
	if write && status != "active" {
		return ProjectAuthority{}, fmt.Errorf("%w: project is not accepting new delivery work", ErrStale)
	}
	return authz, nil
}

// principal rebuilds the acting human's session principal for the authorities
// this service composes with. It grants nothing: every one of them
// reauthorizes the credential against the database before acting on it.
func (a Actor) principal() (auth.Principal, error) {
	if err := requireHuman(a); err != nil {
		return auth.Principal{}, err
	}
	p, err := auth.NewSessionPrincipal(a.SessionCredentialID, a.UserID, a.UserID, false)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return p, nil
}

func requireHuman(actor Actor) error {
	if actor.Kind != string(auth.PrincipalSession) || actor.UserID <= 0 || actor.SessionCredentialID == "" || actor.Impersonated || actor.APIKeyID != 0 {
		return fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return nil
}
