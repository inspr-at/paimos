// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"os"
	"strings"
)

// sessionIndexResult is the local ~/.aeon/sessions/index lookup.
// Rejected means an entry exists but must not be used and must not fall
// through to the server binding (stopped generation, symlink, or unreadable).
type sessionIndexResult int

const (
	sessionIndexAbsent sessionIndexResult = iota
	sessionIndexRejected
	sessionIndexBound
)

func hookSessionEnvSet() bool {
	return os.Getenv("AEON_SESSION_ID") != "" || os.Getenv("AEON_SESSION_FILE") != "" || os.Getenv("AEON_SESSION_STATE_DIR") != ""
}

func hookBindingStatus(harness string) string {
	_, label, result := lookupSessionIndex(vendorSessionRef(harness))
	if result != sessionIndexBound || label == "" {
		return "not bound: run harness run-heartbeat with --source-session"
	}
	return "bound: " + label
}

// recordSessionIndex publishes the live generation for hook session_id lookup.
// A failure is reported and ignored: heartbeat must keep running.
func recordSessionIndex(rt *runtime, o heartbeatOptions, session *heartbeatSession) {
	source := strings.ToLower(strings.TrimSpace(o.SourceSession))
	if session == nil || session.hold.dir == nil || !validUUID(source) || session.disk.Closed || session.disk.Terminal {
		return
	}
	if err := writeSessionIndex(source, session.hold.dir.Name()); err != nil && rt != nil {
		fmt.Fprintln(rt.stderr, "heartbeat: could not record the session index")
	}
}

// releaseSessionIndex drops every index entry that still points at this state
// directory. Stopping the generation is what removes the binding.
func releaseSessionIndex(session *heartbeatSession) {
	if session == nil || session.hold.dir == nil {
		return
	}
	removeSessionIndexForState(session.hold.dir.Name())
}
