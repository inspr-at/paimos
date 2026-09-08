// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/externalstage"
)

type LiveArtifacts struct{}

func (LiveArtifacts) Load(ctx context.Context, tx *sql.Tx, projectID, batchID int64) (ArtifactIdentity, error) {
	var identity ArtifactIdentity
	var deliveryID, attemptID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,batch_key,baseline_ref,content_digest,revision_seal,delivery_id,attempt_id
		FROM baseline_batch_batches WHERE id=? AND project_id=?`, batchID, projectID).Scan(
		&identity.BatchID, &identity.BatchKey, &identity.BaselineRef, &identity.ContentDigest, &identity.RevisionSeal,
		&deliveryID, &attemptID)
	if err == sql.ErrNoRows {
		return ArtifactIdentity{}, fmt.Errorf("%w: batch", ErrNotFound)
	}
	if err != nil {
		return ArtifactIdentity{}, err
	}
	if !deliveryID.Valid || !attemptID.Valid {
		return ArtifactIdentity{}, fmt.Errorf("%w: built receipt artifact is missing", ErrConflict)
	}
	got, err := externalstage.LoadExplicitBuiltArtifact(ctx, tx, deliveryID.Int64, attemptID.Int64)
	if err != nil {
		return ArtifactIdentity{}, fmt.Errorf("%w: built receipt artifact is missing", ErrConflict)
	}
	if !got.Complete() {
		return ArtifactIdentity{}, fmt.Errorf("%w: built receipt artifact is incomplete", ErrConflict)
	}
	identity.ArtifactDigest = "sha256:" + hex.EncodeToString(got.Digest)
	identity.ArtifactCoordinate = got.Coordinate
	identity.VersionScheme = got.Scheme
	identity.ReleaseChannel = got.Channel
	identity.ReleaseSequence = got.Sequence
	identity.Version = got.Version
	identity.Commit = got.Commit
	return identity, nil
}

func requireHuman(actor Actor) error {
	if actor.Kind != string(auth.PrincipalSession) || actor.UserID <= 0 || actor.SessionCredentialID == "" || actor.Impersonated || actor.APIKeyID != 0 {
		return fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return nil
}

func (s *Service) currentAuthority(ctx context.Context, tx *sql.Tx, actor Actor, projectID int64, write bool) (ProjectAuthority, error) {
	if projectID <= 0 {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrInvalid)
	}
	if err := requireHuman(actor); err != nil {
		return ProjectAuthority{}, err
	}
	if err := requireLiveSession(ctx, tx, actor); err != nil {
		return ProjectAuthority{}, err
	}
	var status, userStatus, userRole string
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
	authz := ProjectAuthority{ProjectID: projectID, UserRole: userRole}
	isAdmin := userRole == "admin" || userRole == "super_admin"
	if isAdmin {
		authz.CanView, authz.CanEdit = true, true
	} else {
		var level sql.NullString
		_ = tx.QueryRowContext(ctx, `SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`, actor.UserID, projectID).Scan(&level)
		switch {
		case userRole == "external":
			if level.String == "viewer" || level.String == "editor" {
				authz.CanView = true
			}
			if level.String == "editor" {
				authz.CanEdit = true
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
	return authz, nil
}

func requireLiveSession(ctx context.Context, tx *sql.Tx, actor Actor) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions
		WHERE credential_id=? AND COALESCE(acting_as_user_id,user_id)=? AND expires_at>datetime('now')`,
		actor.SessionCredentialID, actor.UserID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return nil
}

func validOpaqueRef(v string) bool {
	if len(v) < 1 || len(v) > 160 {
		return false
	}
	if v[0] < 'A' || (v[0] > 'Z' && v[0] < 'a') || v[0] > 'z' {
		return false
	}
	for i := 1; i < len(v); i++ {
		c := v[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validEmail(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || !headerSafe(v) {
		return false
	}
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Address == "" || !headerSafe(addr.Address) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(addr.Address), v)
}

func headerSafe(v string) bool {
	if strings.ContainsAny(v, "\r\n\x00") {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func parsePolicyExpiry(raw string, now time.Time) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !headerSafe(raw) {
		return "", fmt.Errorf("%w: expires_at", ErrInvalid)
	}
	var parsed time.Time
	var err error
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err = time.Parse(layout, raw)
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("%w: expires_at must be RFC3339", ErrInvalid)
	}
	parsed = parsed.UTC()
	if !parsed.After(now.UTC()) {
		return "", fmt.Errorf("%w: standing policy already expired", ErrInvalid)
	}
	return parsed.Format(time.RFC3339), nil
}
