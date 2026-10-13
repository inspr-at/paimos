// SPDX-License-Identifier: AGPL-3.0-only

package nativequota

// ProbeFailureBody matches the existing measurement-only account probe ingress.
// Its available field reflects the independent identity fact, never quota.
// The server preserves its ordinary sign-in probe when measurement_only=true.
type ProbeFailureBody struct {
	DaemonID         string             `json:"daemon_id"`
	DaemonGeneration string             `json:"daemon_generation"`
	Available        bool               `json:"available"`
	MeasurementOnly  bool               `json:"measurement_only"`
	Readiness        MeasurementFailure `json:"readiness"`
}

type MeasurementFailure struct {
	BindingRevision int64  `json:"binding_revision"`
	Result          string `json:"result"`
}

// ProbeFailure prepares only an honest failed check. I2 sends it with the
// existing paired/account.probe proof; this package has no HTTP client. Foreign
// revisions cannot report even failures against the current account.
func (c *Collector) ProbeFailure(binding Binding) (ProbeFailureBody, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.binding.check(binding); err != nil {
		return ProbeFailureBody{}, err
	}
	result := c.failure
	if c.qualification == nil {
		result = "unsupported"
	} else if !c.failurePending {
		return ProbeFailureBody{}, ErrNoReadings
	}
	if result == "transport" {
		result = "protocol"
	}
	if result == "" {
		return ProbeFailureBody{}, ErrNoReadings
	}
	return ProbeFailureBody{DaemonID: c.binding.Runtime.DaemonID, DaemonGeneration: c.binding.Runtime.DaemonGeneration,
		Available: c.readiness.SignIn.State == "ready", MeasurementOnly: true,
		Readiness: MeasurementFailure{BindingRevision: c.binding.AccountBindingRevision, Result: result}}, nil
}
