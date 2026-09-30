// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import "errors"

// Server telemetry is not proof that a local child stopped. Require the local
// terminal record and the same workspace/branch before a capacity continuation.
func (s *Supervisor) validateCapacityHandoff(run Run, branch string) error {
	s.mu.Lock()
	previous := s.runs[run.RetryOfRunID]
	s.mu.Unlock()
	if previous == nil || run.RetryOfRunID == "" {
		return errors.New("capacity handoff needs the previous local attempt")
	}
	previous.mu.Lock()
	defer previous.mu.Unlock()
	r := previous.record
	if r.WorkOrderID != run.WorkOrderID || r.TenantID != s.tenantID || r.PrincipalID != s.principalID || r.Workspace != s.workspace || r.LaunchBranch == "" || branch != r.LaunchBranch || r.State != "failed" || !noLocalProcess(r) {
		return errors.New("capacity handoff waits for the original workspace, branch and stopped process")
	}
	return nil
}
