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

// One-shot invocations share a value-free warning receipt under ~/.aeon.
// That directory is often already 0755 for config, so the receipt itself
// lives in a child this process creates at 0700. The lease may come from a
// file or stdin. Run-heartbeat keeps its receipts in its private state directory.
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
	// A receipt written at ~/.aeon/.heartbeat-warnings-SESSION still throttles
	// until the new directory has its own timestamps. Only stamps inside the
	// ten-minute window are copied, so an upgrade does not repeat them.
	now := time.Now()
	if len(disk.WarningAt) == 0 {
		if migrated := legacyHeartbeatWarnings(home, session, now); len(migrated) > 0 {
			disk.WarningAt = migrated
		}
	}
	rt.printEstimateWarnings(response.Warnings, &disk.WarningAt, now)
	if err == nil {
		raw, _ := json.Marshal(disk)
		_ = hold.writeFile("state.json", raw)
	}
}

func openHeartbeatWarningHold(home, session string) (heartbeatHold, error) {
	if !filepath.IsAbs(home) || !validUUID(session) {
		return heartbeatHold{}, errHeartbeatState
	}
	root := filepath.Join(home, ".aeon")
	if err := acceptHeartbeatConfigDir(root); err != nil {
		return heartbeatHold{}, err
	}
	receipts, err := openPrivateHeartbeatDir(filepath.Join(root, "heartbeat-warnings"))
	if err != nil {
		return heartbeatHold{}, err
	}
	defer receipts.Close()
	return openHeartbeatHold(filepath.Join(receipts.Name(), session))
}

// legacyHeartbeatWarnings reads a receipt from before warnings moved under
// heartbeat-warnings/. The directory is opened with the same ownership and
// final-symlink checks as live state. Stamps outside the throttle window,
// or in the future, are dropped.
func legacyHeartbeatWarnings(home, session string, now time.Time) map[string]time.Time {
	if !filepath.IsAbs(home) || !validUUID(session) {
		return nil
	}
	// Missing receipts stay missing. openHeartbeatHold would create the
	// directory, and that would put a legacy path back into ~/.aeon.
	root := filepath.Join(home, ".aeon")
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil
	}
	path := filepath.Join(root, ".heartbeat-warnings-"+session)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil
	}
	hold, err := openHeartbeatHold(path)
	if err != nil {
		return nil
	}
	defer hold.release()
	raw, err := hold.readFile("state.json", 65536)
	if err != nil {
		return nil
	}
	var disk heartbeatDisk
	if json.Unmarshal(raw, &disk) != nil {
		return nil
	}
	return recentWarningTimes(disk.WarningAt, now)
}

func recentWarningTimes(at map[string]time.Time, now time.Time) map[string]time.Time {
	if len(at) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(at))
	for code, last := range at {
		code = heartbeatText(code, 80)
		if code == "" || last.IsZero() || last.After(now) || now.Sub(last) >= heartbeatWarningInterval {
			continue
		}
		out[code] = last.UTC()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
