// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package releaseacceptance

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/inspr-at/paimos/backend/mailer"
)

var (
	ErrInvalid      = errors.New("release_acceptance_invalid")
	ErrUnauthorized = errors.New("release_acceptance_unauthorized")
	ErrForbidden    = errors.New("release_acceptance_forbidden")
	ErrNotFound     = errors.New("release_acceptance_not_found")
	ErrConflict     = errors.New("release_acceptance_conflict")
	ErrStale        = errors.New("release_acceptance_stale")
	ErrUnavailable  = errors.New("release_acceptance_unavailable")
)

const (
	ModeCustomerOperated = "customer_operated"
	ModeAgencySupported  = "agency_supported"
	ModeAgencyOperated   = "agency_operated"

	StateBuilt             = "built"
	StateAcceptancePending = "acceptance_pending"
	StateAccepted          = "accepted"
	StatusPending          = "pending"
	StatusAccepted         = "accepted"
	PartyLinkedUser        = "linked_user"
	PartyManualEmail       = "manual_email"
	SourcePlatform         = "platform"
	SourceExternalEmail    = "external_email"
	SourceStandingPolicy   = "standing_policy"
	MailPlatform           = "platform_send"
	MailExternal           = "external_manual"
	MailPending            = "pending"
	MailSent               = "sent"
	MailFailed             = "failed"
	maxBodyBytes           = 64 << 10
	maxAttempts            = 8
	opaqueRefPattern       = `^[A-Za-z][A-Za-z0-9._:-]*$`
)

type Actor struct {
	Kind                string
	UserID              int64
	SessionCredentialID string
	APIKeyID            int64
	Impersonated        bool
}

type ProjectAuthority struct {
	ProjectID int64
	CanView   bool
	CanEdit   bool
	UserRole  string
}

type ArtifactIdentity struct {
	BatchID            int64
	BatchKey           string
	BaselineRef        string
	ContentDigest      string
	RevisionSeal       string
	ArtifactDigest     string
	ArtifactCoordinate string
	VersionScheme      string
	ReleaseChannel     string
	ReleaseSequence    int64
	Version            string
	Commit             string
}

type ArtifactSource interface {
	Load(ctx context.Context, tx *sql.Tx, projectID, batchID int64) (ArtifactIdentity, error)
}

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type Gap struct {
	GapRef    string `json:"gap_ref"`
	Statement string `json:"statement"`
}

type PartyInput struct {
	PartyRef    string   `json:"party_ref"`
	Kind        string   `json:"kind"`
	UserID      int64    `json:"user_id,omitempty"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
}

type Party struct {
	PartyRef    string   `json:"party_ref"`
	Kind        string   `json:"kind"`
	UserID      *int64   `json:"user_id,omitempty"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
}

type Confirmation struct {
	PartyRef    string `json:"party_ref"`
	Decision    string `json:"decision"`
	Source      string `json:"source"`
	ActorUserID int64  `json:"actor_user_id"`
	Attestation string `json:"attestation,omitempty"`
	ConfirmedAt string `json:"confirmed_at"`
}

type EmailEvidence struct {
	MessageRef         string   `json:"message_ref"`
	ReleaseRevision    int64    `json:"acceptance_revision"`
	RecipientPartyRefs []string `json:"recipient_party_refs"`
	State              string   `json:"state"`
	Source             string   `json:"source"`
	RecordedAt         string   `json:"recorded_at"`
	SentAt             *string  `json:"sent_at"`
	ActorUserID        int64    `json:"actor_user_id"`
	Attestation        string   `json:"attestation,omitempty"`
	BodySHA256         string   `json:"body_sha256"`
}

type Missing struct {
	Confirmations []string `json:"confirmations"`
	EmailCoverage []string `json:"email_coverage"`
	Preview       bool     `json:"preview"`
	Send          bool     `json:"send"`
}

type ReleaseRecord struct {
	ID                 int64  `json:"id"`
	ProjectID          int64  `json:"project_id"`
	BatchID            int64  `json:"batch_id"`
	ReleaseRef         string `json:"release_ref"`
	BatchKey           string `json:"batch_key"`
	BaselineRef        string `json:"baseline_ref"`
	ContentDigest      string `json:"content_digest"`
	RevisionSeal       string `json:"revision_seal"`
	ArtifactDigest     string `json:"artifact_digest"`
	ArtifactCoordinate string `json:"artifact_coordinate"`
	VersionScheme      string `json:"version_scheme"`
	ReleaseChannel     string `json:"release_channel"`
	ReleaseSequence    int64  `json:"release_sequence"`
	Version            string `json:"version"`
	CommitSHA          string `json:"commit_sha"`
	State              string `json:"state"`
	Revision           int64  `json:"revision"`
	CreatedAt          string `json:"created_at"`
}

type CooperationDefaults struct {
	AgreementRef string `json:"agreement_ref"`
	Notes        string `json:"notes,omitempty"`
}

type Acceptance struct {
	ID                 int64               `json:"id"`
	Release            ReleaseRecord       `json:"release"`
	Revision           int64               `json:"revision"`
	Status             string              `json:"status"`
	OperatingMode      string              `json:"operating_mode"`
	OperatingModeLabel string              `json:"operating_mode_label"`
	AgreementRef       string              `json:"agreement_ref"`
	DisclosedGaps      []Gap               `json:"disclosed_gaps"`
	RequiredPartyRefs  []string            `json:"required_party_refs"`
	DeliveryPartyRef   string              `json:"delivery_party_ref"`
	OperatorPartyRef   string              `json:"operator_party_ref"`
	SupportPartyRef    *string             `json:"support_party_ref"`
	Parties            []Party             `json:"parties"`
	Confirmations      []Confirmation      `json:"confirmations"`
	EmailEvidence      []EmailEvidence     `json:"email_evidence"`
	PreviewSubject     string              `json:"preview_subject"`
	PreviewBody        string              `json:"preview_body"`
	PreviewRevision    int64               `json:"preview_revision"`
	Missing            Missing             `json:"missing"`
	Defaults           CooperationDefaults `json:"defaults"`
	OfferDisclaimer    string              `json:"offer_disclaimer"`
}

type StandingPolicy struct {
	ID             int64    `json:"id"`
	PolicyRef      string   `json:"policy_ref"`
	OperatingMode  string   `json:"operating_mode"`
	ContentDigest  string   `json:"content_digest"`
	RevisionSeal   string   `json:"revision_seal"`
	TargetRef      string   `json:"target_ref"`
	Parties        []string `json:"parties"`
	ModelRef       string   `json:"model_ref"`
	AgreementRef   string   `json:"agreement_ref"`
	Gaps           []Gap    `json:"gaps"`
	ReleaseChannel string   `json:"release_channel"`
	ArtifactDigest string   `json:"artifact_digest"`
	BoundedUse     string   `json:"bounded_use"`
	ExpiresAt      string   `json:"expires_at"`
	RevokedAt      *string  `json:"revoked_at,omitempty"`
	CreatedAt      string   `json:"created_at"`
}

type Service struct {
	DB        *sql.DB
	Artifacts ArtifactSource
	Mail      mailer.Mailer
	Clock     Clock
}

func NewService(database *sql.DB, artifacts ArtifactSource, mail mailer.Mailer, clock Clock) *Service {
	if artifacts == nil {
		artifacts = LiveArtifacts{}
	}
	if mail == nil {
		mail = mailer.Unconfigured{}
	}
	if clock == nil {
		clock = ClockFunc(func() time.Time { return time.Now().UTC() })
	}
	return &Service{DB: database, Artifacts: artifacts, Mail: mail, Clock: clock}
}

func ModeLabel(mode string) string {
	switch mode {
	case ModeAgencyOperated:
		return "provider-operated"
	case ModeAgencySupported:
		return "agency-supported"
	case ModeCustomerOperated:
		return "customer-operated"
	default:
		return mode
	}
}

const OfferDisclaimer = "Provider-operated is representable here. It is not an offer that Augmentoring will operate the service."
