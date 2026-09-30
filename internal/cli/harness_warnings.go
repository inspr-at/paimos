// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
)

const heartbeatWarningInterval = 10 * time.Minute

func (rt *runtime) printEstimateWarnings(warnings []harness.EstimateWarning, at *map[string]time.Time, now time.Time) {
	if *at == nil {
		*at = map[string]time.Time{}
	}
	for _, warning := range warnings {
		code, hint := heartbeatText(warning.Code, 80), heartbeatText(warning.Hint, 1000)
		if code == "" || hint == "" {
			continue
		}
		if last, ok := (*at)[code]; ok && now.Sub(last) < heartbeatWarningInterval {
			continue
		}
		fmt.Fprintf(rt.stderr, "warning: %s: %s\n", code, hint)
		(*at)[code] = now
	}
}

// One-shot invocations share a value-free private warning receipt next to the
// worker lease. It carries no lease, local path, response or ticket content.
func (rt *runtime) printHeartbeatWarnings(out any, session, leaseFile string) {
	raw, err := json.Marshal(out)
	if err != nil {
		return
	}
	var response struct {
		Warnings []harness.EstimateWarning `json:"warnings"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Warnings) == 0 {
		return
	}
	hold, err := openHeartbeatHold(filepath.Join(filepath.Dir(leaseFile), ".heartbeat-warnings-"+session))
	if errors.Is(err, errHeartbeatBusy) {
		return
	}
	var disk heartbeatDisk
	if err == nil {
		defer hold.release()
		if raw, err := hold.readFile("state.json", 65536); err == nil {
			_ = json.Unmarshal(raw, &disk)
		}
	}
	rt.printEstimateWarnings(response.Warnings, &disk.WarningAt, time.Now())
	if err == nil {
		raw, _ := json.Marshal(disk)
		_ = hold.writeFile("state.json", raw)
	}
}
