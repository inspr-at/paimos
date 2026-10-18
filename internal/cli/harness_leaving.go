// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) harnessLeavingAt() *Command {
	var at, reason, note string
	var off bool
	return &Command{Name: "leaving-at", Short: "Schedule all your running work to stop by a deadline", Use: "harness leaving-at [--at RFC3339 | --off]", addFlags: func(fs *flagSet) {
		fs.string(&at, "at", 0, "leaving deadline in RFC3339 format")
		fs.bool(&off, "off", 0, "cancel pending leave requests; paused agents stay paused")
		fs.string(&reason, "reason", 0, "reason for leaving")
		fs.string(&note, "note", 0, "optional handover note")
	}, run: func(args []string) error {
		if len(args) > 0 || off && at != "" || at == "" && (reason != "" || note != "") {
			return usagef("choose --at RFC3339 or --off, or no flags to read the deadline")
		}
		method := http.MethodGet
		var body any
		if off {
			method = http.MethodDelete
		} else if at != "" {
			deadline, err := time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return usagef("--at must be RFC3339")
			}
			method = http.MethodPut
			body = map[string]any{"deadline_at": deadline, "reason": reason, "note": note}
		}
		var out any
		if err := rt.harnessDo(method, "/api/me/leaving-at", "", body, &out); err != nil {
			return err
		}
		return rt.printHarness(out)
	}}
}

func (rt *runtime) harnessPauseDefault() *Command {
	var level string
	return &Command{Name: "pause-default", Short: "Read or set your default pause level in this workspace", Use: "harness pause-default [--level LEVEL]", addFlags: func(fs *flagSet) {
		fs.string(&level, "level", 0, "stop_now, pause_quickly, pause or wrap_up")
	}, run: func(args []string) error {
		if len(args) > 0 || level != "" && !harness.ValidPauseLevel(level) {
			return usagef("invalid default pause level")
		}
		method := http.MethodGet
		var body any
		if level != "" {
			method = http.MethodPut
			body = map[string]any{"default_level": level}
		}
		var out any
		if err := rt.harnessDo(method, "/api/me/agent-pause-settings", "", body, &out); err != nil {
			return err
		}
		return rt.printHarness(out)
	}}
}
