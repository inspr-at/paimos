// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
)

type Result struct {
	Status  string `json:"status"`
	Subject string `json:"-"`
}

type Provisioner interface {
	Name() string
	EnsureUser(ctx context.Context, email, displayName string) (Result, error)
}

// TenantScoped is implemented by process-configured adapters so one tenant
// cannot use credentials intended for another tenant's identity organization.
type TenantScoped interface{ TenantID() string }

// InviteSender resumes setup email after user creation succeeded but sending
// the invite code failed. Callers may use it only for a subject saved from
// their own create attempt, never for an arbitrary pre-existing account.
type InviteSender interface {
	SendInvite(ctx context.Context, subject string) error
}

// ProvisionError preserves only the ID of a user created in this attempt.
// Error deliberately contains no provider response or credential.
type ProvisionError struct{ Subject string }

func (*ProvisionError) Error() string { return "identity provisioning failed" }

type Config struct {
	Driver         string
	TenantID       string
	URL            string
	OrganizationID string
	TokenFile      string
}

// FromEnv reads the secret only from a file. No credential is returned on any
// error path, logged, or exposed through the Provisioner interface.
func FromEnv() (Provisioner, error) {
	c := Config{
		Driver:         strings.TrimSpace(os.Getenv("AEON_IDENTITY_PROVISIONER")),
		TenantID:       strings.TrimSpace(os.Getenv("AEON_IDENTITY_TENANT_ID")),
		URL:            strings.TrimSpace(os.Getenv("AEON_ZITADEL_URL")),
		OrganizationID: strings.TrimSpace(os.Getenv("AEON_ZITADEL_ORG_ID")),
		TokenFile:      strings.TrimSpace(os.Getenv("AEON_ZITADEL_TOKEN_FILE")),
	}
	if c.Driver == "" || c.Driver == "none" {
		return nil, nil
	}
	if c.Driver != "zitadel" {
		return nil, errors.New("unsupported identity provisioner")
	}
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, errors.New("AEON_ZITADEL_URL must be an HTTPS origin")
	}
	if c.TenantID == "" || c.OrganizationID == "" || c.TokenFile == "" {
		return nil, errors.New("Zitadel tenant, organization and token file are required")
	}
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, errors.New("cannot read AEON_ZITADEL_TOKEN_FILE")
	}
	value := strings.TrimSpace(string(token))
	if value == "" {
		return nil, errors.New("AEON_ZITADEL_TOKEN_FILE is empty")
	}
	return newZitadel(c.URL, c.OrganizationID, value, c.TenantID), nil
}
