package externalstage

import "net/http"

const AdminTerminalEvidencePath = "/api/agent-mode/deliveries/{deliveryKey}/external-stage-handoffs/{handoffID}/terminal-evidence"

func init() {
	Routes = append(Routes, Route{
		OperationID: "readExternalStageTerminalEvidence",
		Method:      http.MethodGet,
		Path:        AdminTerminalEvidencePath,
		Audience:    "internal",
	})
}

// TerminalEvidence is an owner-authorized, value-free projection of one
// immutable terminal report. It deliberately excludes report bodies,
// evidence payloads, credentials, and handoff secrets.
type TerminalEvidence struct {
	HandoffID            string        `json:"handoff_id"`
	DeliveryKey          string        `json:"delivery_key"`
	IssueKey             string        `json:"issue_key"`
	AttemptNumber        int64         `json:"attempt_number"`
	PlanRevision         int64         `json:"plan_revision"`
	StageKey             string        `json:"stage_key"`
	ExecutionNumber      int64         `json:"execution_number"`
	AuthorityEpoch       int64         `json:"authority_epoch"`
	CredentialEpoch      int64         `json:"credential_epoch"`
	ReporterClass        ReporterClass `json:"reporter_class"`
	ReporterRole         ReporterRole  `json:"reporter_role"`
	DependencyKey        string        `json:"dependency_key,omitempty"`
	TerminalStatus       HandoffState  `json:"terminal_status"`
	TerminalSequence     int64         `json:"terminal_sequence"`
	TerminalAt           string        `json:"terminal_at"`
	TerminalReportDigest string        `json:"terminal_report_digest"`
}
