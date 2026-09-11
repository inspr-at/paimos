// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package targetidentity owns the canonical, server-side deployment target
// identity bytes shared by release acceptance and bounded launch grants.
// Target refs are provenance digests; URLs and host fields never cross either
// public contract.
package targetidentity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	KindPharosOwner        = "pharos_owner"
	KindProjectEnvironment = "project_environment"
)

// Identity field order is part of the target_ref byte contract. Keep this
// structure discriminated and additive; callers must not construct it from a
// display label.
type Identity struct {
	Kind              string `json:"kind"`
	ProjectID         int64  `json:"project_id"`
	RegistrationID    int64  `json:"registration_id,omitempty"`
	DeliveryID        int64  `json:"delivery_id,omitempty"`
	AttemptID         int64  `json:"attempt_id,omitempty"`
	WorkflowSymbol    string `json:"workflow_symbol,omitempty"`
	EnvironmentSymbol string `json:"environment_symbol,omitempty"`
	EnvironmentID     int64  `json:"environment_id,omitempty"`
	APIKeyID          int64  `json:"api_key_id,omitempty"`
	UserID            int64  `json:"user_id,omitempty"`
	ReporterID        int64  `json:"reporter_id,omitempty"`
	AllowDeployment   int64  `json:"allow_deployment,omitempty"`
	URL               string `json:"url,omitempty"`
	HostAlias         string `json:"host_alias,omitempty"`
	HostIP            string `json:"host_ip,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

func Digest(identity Identity) string {
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func IsDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil && value == strings.ToLower(value)
}

// LoadProjectEnvironment resolves every identity-bearing database field. The
// returned identity may be hashed but must never be serialized to an external
// launch response because it contains host provenance.
func LoadProjectEnvironment(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, environmentID int64) (Identity, error) {
	var name, created, url, alias, ip, updated string
	err := q.QueryRowContext(ctx, `SELECT name, created_at, COALESCE(url,''), COALESCE(host_alias,''), COALESCE(host_ip,''), COALESCE(updated_at,'')
		FROM project_environments WHERE id=? AND project_id=?`, environmentID, projectID).
		Scan(&name, &created, &url, &alias, &ip, &updated)
	if err != nil {
		return Identity{}, err
	}
	name = strings.TrimSpace(name)
	if !validOpaqueRef(name) {
		return Identity{}, errors.New("invalid project environment identity")
	}
	return Identity{
		Kind: KindProjectEnvironment, ProjectID: projectID, EnvironmentID: environmentID,
		EnvironmentSymbol: name, CreatedAt: created, URL: strings.TrimSpace(url),
		HostAlias: strings.TrimSpace(alias), HostIP: strings.TrimSpace(ip), UpdatedAt: strings.TrimSpace(updated),
	}, nil
}

// IsSymbol reports whether a value is safe for the closed external-stage
// workflow/environment symbol grammar. Existing project-environment names are
// allowed to be broader; callers selecting a delegated target must opt in only
// to this subset.
func IsSymbol(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validOpaqueRef(value string) bool {
	if len(value) < 1 || len(value) > 160 ||
		value[0] < 'A' || (value[0] > 'Z' && value[0] < 'a') || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' ||
			character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}
