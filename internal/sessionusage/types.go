// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import "errors"

const (
	maxToken = 1_000_000_000_000
	maxLine  = 64 * 1024
	maxInput = 1 << 20
	maxUsage = 128

	accountingVendor = "vendor_cumulative"
	accountingDelta  = "delta_sum"

	statusKnown       = "known"
	statusUnknown     = "unknown"
	statusProvisional = "provisional"
)

// ErrMalformed means a usage record is not valid vendor JSON.
var ErrMalformed = errors.New("malformed usage record")

// ErrAmbiguous means the capture can be read as more than one usage total.
var ErrAmbiguous = errors.New("ambiguous usage record")

// ErrRejected means the counters are negative, decreasing, overflowing, or
// otherwise unsafe to report.
var ErrRejected = errors.New("rejected usage record")

// UsageError is a command-line usage mistake.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Prior is the last cumulative snapshot already stored for a model.
// A null counter is unknown. Sequence 0 is allowed only when every counter
// is null.
type Prior struct {
	Model             string
	Sequence          int64
	InputTokens       *int64
	OutputTokens      *int64
	CachedInputTokens *int64
}

// Options selects the vendor and the reporter metadata that vendor logs do
// not carry. Account and subscription fields are taken only from Options.
type Options struct {
	Source            string
	Model             string
	BillingMode       string
	SubscriptionLabel string
	AccountID         string
	AccountLabel      string
	Priors            []Prior
	NewReportID       func() (string, error)
}

// UsageReport is the US1 session-usage request body.
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

// Observation records how the counters were measured. It is not posted.
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

// Result is the parser stdout document.
type Result struct {
	Reports      []UsageReport `json:"reports"`
	Observations []Observation `json:"observations"`
}

type snapshot struct {
	input       int64
	output      int64
	cached      int64
	cachedKnown bool
}

type usageRecord struct {
	accounting string
	model      string
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
