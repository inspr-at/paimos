// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// canEditProjectTx is the transaction-bound form of auth.CanEditProject.
// Security-sensitive cross-scope mutations use it so the permission decision
// and the protected reads/writes share one SQLite snapshot. Unlike the general
// request helper, it also proves that the destination project is live.
func canEditProjectTx(ctx context.Context, tx *sql.Tx, user *models.User, projectID int64) (bool, error) {
	if user == nil || user.Status != "active" || projectID <= 0 {
		return false, nil
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=?`, projectID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if status == "deleted" {
		return false, nil
	}
	if auth.IsAdmin(user) {
		return true, nil
	}
	var level string
	err := tx.QueryRowContext(ctx,
		`SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`,
		user.ID, projectID).Scan(&level)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		return level == string(auth.AccessEditor), nil
	}
	return user.Role == "member", nil
}

type knowledgeScope struct {
	projectID sql.NullInt64
	userID    sql.NullInt64
	typ       string
}

func (s knowledgeScope) isKnowledge() bool { return IsKnowledgeIssueType(s.typ) }

func loadKnowledgeScopeTx(ctx context.Context, tx *sql.Tx, issueID int64) (knowledgeScope, error) {
	var scope knowledgeScope
	err := tx.QueryRowContext(ctx, `
		SELECT project_id, user_id, type
		  FROM issues
		 WHERE id=? AND deleted_at IS NULL`, issueID,
	).Scan(&scope.projectID, &scope.userID, &scope.typ)
	return scope, err
}

func loadKnowledgeScope(issueID int64) (knowledgeScope, error) {
	var scope knowledgeScope
	err := db.DB.QueryRow(`
		SELECT project_id, user_id, type
		  FROM issues
		 WHERE id=? AND deleted_at IS NULL`, issueID,
	).Scan(&scope.projectID, &scope.userID, &scope.typ)
	return scope, err
}

func canViewKnowledgeEndpoint(r *http.Request, scope knowledgeScope) bool {
	user := auth.GetUser(r)
	if user == nil || user.Status != "active" {
		return false
	}
	if scope.projectID.Valid {
		return auth.CanViewProject(r, scope.projectID.Int64)
	}
	if scope.userID.Valid {
		return scope.isKnowledge() && scope.userID.Int64 == user.ID
	}
	return true // instance knowledge and legacy project-less issues are readable
}

// canEditKnowledgeEndpointTx authorizes relation mutation against an issue.
// Ordinary project issues require project edit rights. User knowledge belongs
// only to that user; instance knowledge is admin-owned. Legacy project-less
// non-knowledge rows (notably sprints) retain their existing authenticated
// relation behavior.
func canEditKnowledgeEndpointTx(ctx context.Context, tx *sql.Tx, user *models.User, scope knowledgeScope) (bool, error) {
	if user == nil || user.Status != "active" {
		return false, nil
	}
	if scope.projectID.Valid {
		return canEditProjectTx(ctx, tx, user, scope.projectID.Int64)
	}
	if scope.userID.Valid {
		return scope.isKnowledge() && scope.userID.Int64 == user.ID, nil
	}
	if scope.isKnowledge() {
		return auth.IsAdmin(user), nil
	}
	return true, nil
}

// sameKnowledgeScope requires every relation touching knowledge to remain in
// one exact owner scope. This permits project knowledge↔ordinary issue links
// inside the same project, while rejecting project, user, and instance hops.
func sameKnowledgeScope(left, right knowledgeScope) bool {
	if !left.isKnowledge() && !right.isKnowledge() {
		return true
	}
	if left.projectID.Valid || right.projectID.Valid {
		return left.projectID.Valid && right.projectID.Valid && left.projectID.Int64 == right.projectID.Int64
	}
	if left.userID.Valid || right.userID.Valid {
		return left.userID.Valid && right.userID.Valid && left.userID.Int64 == right.userID.Int64
	}
	return true // both are instance-owned
}
