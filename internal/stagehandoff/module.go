// SPDX-License-Identifier: AGPL-3.0-only

// Package stagehandoff retains retired route and wire compatibility only.
// No launch, evidence, plugin dispatch or settlement can execute here.
package stagehandoff

import (
	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"time"
)

type Module struct{}

func New(_ *pgxpool.Pool, _ *plugins.Registry) httpapi.Module { return &Module{} }

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{projectId}/releases/{releaseId}/candidate-artifact", httpapi.RetiredFlow)
	mux.HandleFunc("PUT /api/projects/{projectId}/releases/{releaseId}/candidate-artifact", httpapi.RetiredFlow)
	mux.HandleFunc("POST /api/stage-handoffs", reportercontract.WithHeader(reportercontract.StageHandoffs, httpapi.RetiredFlow))
	mux.HandleFunc("GET /api/stage-handoffs/{handoffId}", reportercontract.WithHeader(reportercontract.StageHandoffs, httpapi.RetiredFlow))
	mux.HandleFunc("POST /api/stage-handoffs/{handoffId}/classic-batch-alias", reportercontract.WithHeader(reportercontract.BaselineBatches, httpapi.RetiredFlow))
	mux.HandleFunc("POST /api/projects/{projectId}/baseline-batches/batches/{batchId}/built-receipt", reportercontract.WithHeader(reportercontract.BaselineBatches, httpapi.RetiredFlow))
	mux.HandleFunc("POST /api/stage-handoffs/{handoffId}/evidence", reportercontract.WithHeader(reportercontract.StageEvidence, httpapi.RetiredFlow))
	mux.HandleFunc("POST /api/stage-handoffs/{handoffId}/launch/admit", reportercontract.WithHeader(reportercontract.StageLaunch, httpapi.RetiredFlow))
	mux.HandleFunc("POST /api/stage-handoffs/{handoffId}/launch/consume", reportercontract.WithHeader(reportercontract.StageLaunch, httpapi.RetiredFlow))
	mux.HandleFunc("POST /api/stage-handoffs/{handoffId}/result", reportercontract.WithHeader(reportercontract.StageResult, httpapi.RetiredFlow))
}

// Historic response types remain stable for strict external parsers.
type Handoff struct {
	ID                     string                 `json:"id"`
	ProjectNodeID          string                 `json:"project_node_id"`
	ReleaseNodeID          string                 `json:"release_node_id"`
	Stage                  string                 `json:"stage"`
	Operation              string                 `json:"operation"`
	PluginID               string                 `json:"plugin_id"`
	Attempt                int                    `json:"attempt"`
	AuthorityEpoch         int64                  `json:"authority_epoch"`
	SupersededBy           *string                `json:"superseded_by"`
	AuthorityOpen          bool                   `json:"authority_open"`
	JourneyRevision        int64                  `json:"journey_revision"`
	State                  string                 `json:"state"`
	ExpiresAt              time.Time              `json:"expires_at"`
	EvidenceCeiling        []string               `json:"evidence_ceiling"`
	PlanDigest             string                 `json:"plan_digest"`
	PredecessorDigest      string                 `json:"predecessor_digest"`
	ContextDigest          string                 `json:"context_digest"`
	PrerequisiteSealSHA256 string                 `json:"prerequisite_seal_sha256"`
	Target                 *deploytarget.Target   `json:"-"`
	TargetDigestSHA256     string                 `json:"-"`
	Result                 *Result                `json:"result,omitempty"`
	Admission              *HandoffAdmissionState `json:"admission,omitempty"`
}
type HandoffAdmissionState struct {
	AdmissionID           string     `json:"admission_id"`
	Epoch                 int64      `json:"epoch"`
	ExpiresAt             time.Time  `json:"expires_at"`
	ConsumedAt            *time.Time `json:"consumed_at,omitempty"`
	ConsumedByPrincipalID *string    `json:"consumed_by_principal_id,omitempty"`
}
type Artifact struct {
	VersionScheme        string `json:"version_scheme"`
	Version              string `json:"version"`
	ReleaseChannel       string `json:"release_channel"`
	ReleaseSequence      int64  `json:"release_sequence"`
	DigestSHA256         string `json:"digest_sha256"`
	CommitDigest         string `json:"commit_digest"`
	ManifestCoordinate   string `json:"manifest_coordinate"`
	ManifestDigestSHA256 string `json:"manifest_digest_sha256"`
}
type EvidenceWrite struct {
	Sequence           int64     `json:"sequence"`
	Kind               string    `json:"kind"`
	Outcome            string    `json:"outcome"`
	ObservedAt         time.Time `json:"observed_at"`
	AuthorityEpoch     int64     `json:"authority_epoch"`
	Workflow           *string   `json:"workflow,omitempty"`
	Environment        *string   `json:"environment,omitempty"`
	Artifact           *Artifact `json:"artifact,omitempty"`
	Authorized         *bool     `json:"authorized,omitempty"`
	CredentialReady    *bool     `json:"credential_ready,omitempty"`
	ReviewedPlanDigest string    `json:"reviewed_plan_digest,omitempty"`
	Host               string    `json:"host,omitempty"`
	AllHostEvalPassed  *bool     `json:"all_host_eval_passed,omitempty"`
	TargetBuildPassed  *bool     `json:"target_build_passed,omitempty"`
	BackupReady        *bool     `json:"backup_ready,omitempty"`
	BackupObservedAt   time.Time `json:"backup_observed_at,omitempty"`
	RestartRequired    *bool     `json:"restart_required,omitempty"`
	RunningKernel      string    `json:"running_kernel,omitempty"`
	ExpectedKernel     string    `json:"expected_kernel,omitempty"`
}
type Evidence struct {
	EvidenceWrite
	HandoffID  string    `json:"handoff_id"`
	ReceivedAt time.Time `json:"received_at"`
}
type ResultWrite struct {
	Outcome                string  `json:"outcome"`
	TerminalSequence       int64   `json:"terminal_sequence"`
	AuthorityEpoch         int64   `json:"authority_epoch"`
	PrerequisiteSealSHA256 string  `json:"prerequisite_seal_sha256"`
	BlockerCode            *string `json:"blocker_code,omitempty"`
}
type Result struct {
	ResultWrite
	HandoffID   string    `json:"handoff_id"`
	CompletedAt time.Time `json:"completed_at"`
}
