// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import "errors"

const (
	maxToken          = 1_000_000_000_000
	maxLine           = 64 * 1024
	maxInput          = 1 << 20
	maxUsage          = 128
	accountingVendor  = "vendor_cumulative"
	accountingDelta   = "delta_sum"
	statusKnown       = "known"
	statusUnknown     = "unknown"
	statusProvisional = "provisional"
)

// ErrMalformed means a record violates its JSON/counter schema.
var ErrMalformed = errors.New("malformed usage record")

// ErrAmbiguous means attribution or accounting has competing interpretations.
var ErrAmbiguous = errors.New("ambiguous usage record")

// ErrRejected means a capture is unsafe or unsupported to report.
var ErrRejected = errors.New("rejected usage record")

type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Options binds one source session to one Aeon session. FromStart explicitly
// asserts a lossless capture beginning at zero usage for that entire session.
// Every Parse receives the complete prefix, including when Previous is supplied.
// Never use a transcript tail, a resumed pre-existing thread, or multiple source
// sessions. Previous verifies append-only continuity, not the truth of the initial
// assertion. The reporter must serialize capture and persist reports before POST.
type Options struct {
	Source          string
	SessionID       string
	SourceSessionID string
	FromStart       bool
	Previous        *Checkpoint
	// Model asserts one fixed exact model for the whole capture. Required for
	// thread-cumulative records and any delta that omits its actual model.
	Model string
	// Final seals the entire session. Known tokens alone do not imply finality.
	Final             bool
	BillingMode       string
	SubscriptionLabel string
	AccountID         string
	AccountLabel      string
}

// Checkpoint is local continuity evidence, never an endpoint request. It contains
// only hashes and a byte count; source text and source IDs are not exported.
type Checkpoint struct {
	Binding string `json:"binding"`
	Bytes   int    `json:"bytes"`
	Digest  string `json:"digest"`
	Final   bool   `json:"final"`
}

// UsageReport matches the US1 per-session/model cumulative request body.
type UsageReport struct {
	ReportID          string  `json:"report_id"`
	Model             string  `json:"model"`
	Sequence          int64   `json:"sequence"`
	InputTokens       *int64  `json:"input_tokens"`
	OutputTokens      *int64  `json:"output_tokens"`
	CachedInputTokens *int64  `json:"cached_input_tokens"`
	Provisional       bool    `json:"provisional"`
	AccountID         *string `json:"account_id"`
	AccountLabel      *string `json:"account_label"`
	BillingMode       string  `json:"billing_mode"`
	SubscriptionLabel *string `json:"subscription_label"`
}

// Observation describes measurement independently of session finality; not POSTed.
type Observation struct {
	Source       string `json:"source"`
	Model        string `json:"model"`
	ModelStatus  string `json:"model_status"`
	InputStatus  string `json:"input_status"`
	OutputStatus string `json:"output_status"`
	CachedStatus string `json:"cached_status"`
	Measurement  string `json:"measurement"`
	Accounting   string `json:"accounting"`
	Provisional  bool   `json:"provisional"`
}

type Result struct {
	Reports      []UsageReport `json:"reports"`
	Observations []Observation `json:"observations"`
	Checkpoint   Checkpoint    `json:"checkpoint"`
}

type snapshot struct {
	input       int64
	output      int64
	cached      int64
	inputKnown  bool
	cachedKnown bool
}

type usageRecord struct {
	accounting string
	model      string
	id         string
	cumulative *snapshot
	delta      *snapshot
}

type counter struct {
	set   bool
	known bool
	value int64
}

func (c counter) pointer() *int64 {
	if !c.set || !c.known {
		return nil
	}
	v := c.value
	return &v
}
func (c counter) status() string {
	if c.set && c.known {
		return statusKnown
	}
	return statusUnknown
}
