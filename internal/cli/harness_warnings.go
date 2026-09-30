// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// One-shot invocations share a value-free private warning receipt under
// ~/.aeon, independent of whether the worker lease came from a file or stdin.
// Run-heartbeat keeps its receipts in its existing private state directory.
func (rt *runtime) printHeartbeatWarnings(out any, session, _ string) {
	home, _ := os.UserHomeDir()
	rt.printHeartbeatWarningsHome(out, session, home)
}

func (rt *runtime) printHeartbeatWarningsHome(out any, session, home string) {
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
	hold, err := openHeartbeatWarningHold(home, session)
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

func openHeartbeatWarningHold(home, session string) (heartbeatHold, error) {
	if !filepath.IsAbs(home) || !validUUID(session) {
		return heartbeatHold{}, errHeartbeatState
	}
	dir, err := openPrivateHeartbeatDir(filepath.Join(home, ".aeon"))
	if err != nil {
		return heartbeatHold{}, err
	}
	defer dir.Close()
	return openHeartbeatHold(filepath.Join(dir.Name(), ".heartbeat-warnings-"+session))
}
