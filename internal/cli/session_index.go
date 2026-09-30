// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// sessionIndexResult is the local ~/.aeon/sessions/index lookup.
// Rejected means an entry exists but must not be used: stopped generation,
// symlink, dead or reused owner, or unreadable. A UUID miss is absent.
type sessionIndexResult int

const (
	sessionIndexAbsent sessionIndexResult = iota
	sessionIndexRejected
	sessionIndexBound
)

// errSessionIndexConflict refuses a second live state directory for one source UUID.
var errSessionIndexConflict = errors.New("session index conflict")

type indexConflict struct{ source string }

func (e *indexConflict) Error() string {
	return "session index already binds " + e.source + " to another live state directory"
}

func (e *indexConflict) Unwrap() error { return errSessionIndexConflict }

func sessionIndexConflictError(source string) error {
	return &indexConflict{source: source}
}

func hookSessionEnvSet() bool {
	return os.Getenv("AEON_SESSION_ID") != "" || os.Getenv("AEON_SESSION_FILE") != "" || os.Getenv("AEON_SESSION_STATE_DIR") != ""
}

func hookBindingStatus(harness string) string {
	if hookSessionEnvSet() {
		return explicitHookBindingStatus(harness)
	}
	return indexBindingStatus(harness)
}

func indexBindingStatus(harness string) string {
	_, label, result := lookupSessionIndex(vendorSessionRef(harness))
	if result != sessionIndexBound || label == "" {
		return "not bound: run harness run-heartbeat with --source-session"
	}
	return "bound: " + label
}

// explicitHookBindingStatus follows inboxHookSession: an explicit env binding
// wins, including when it is invalid or missing, and a disagreement with the
// index is reported instead of hidden.
func explicitHookBindingStatus(harness string) string {
	id, err := inboxHookSession()
	if err != nil {
		return "not bound: explicit session binding is invalid"
	}
	if id == "" {
		return "not bound: explicit session binding is unavailable"
	}
	indexedID, indexedLabel, result := lookupSessionIndex(vendorSessionRef(harness))
	same := result == sessionIndexBound && strings.EqualFold(indexedID, id)
	status := "bound: " + explicitBindingLabel(id, indexedLabel, same)
	if result == sessionIndexBound && indexedID != "" && !same {
		status += "\nconflict: session index binds a different generation"
	}
	return status
}

func explicitBindingLabel(id, indexedLabel string, indexSame bool) string {
	if indexSame && indexedLabel != "" {
		return indexedLabel
	}
	if os.Getenv("AEON_SESSION_ID") == "" && os.Getenv("AEON_SESSION_FILE") == "" {
		if label := stateDirSentLabel(os.Getenv("AEON_SESSION_STATE_DIR")); label != "" {
			return label
		}
	}
	return id
}

// recordSessionIndex publishes the live generation for hook session_id lookup.
// A conflicting live binding refuses startup. Any other index failure is
// reported and ignored so heartbeat can keep running. An owner accepted
// without a verified start is left unpublished; lookup would reject it.
func recordSessionIndex(rt *runtime, o heartbeatOptions, session *heartbeatSession) error {
	source := strings.ToLower(strings.TrimSpace(o.SourceSession))
	if session == nil || session.hold.dir == nil || !validUUID(source) || session.disk.Closed || session.disk.Terminal {
		return nil
	}
	if session.disk.OwnerPID <= 0 || session.disk.OwnerStart == "" {
		return nil
	}
	err := writeSessionIndex(source, session.hold.dir.Name(), session.disk.OwnerPID, session.disk.OwnerStart)
	if err == nil {
		return nil
	}
	if errors.Is(err, errSessionIndexConflict) {
		if rt != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: %s\n", err.Error())
		}
		if rt == nil {
			return err
		}
		return rt.abandonHeartbeat(o, session, err)
	}
	if rt != nil {
		fmt.Fprintln(rt.stderr, "heartbeat: could not record the session index")
	}
	return nil
}

// releaseSessionIndex drops every index entry that still points at this state
// directory. Shutdown removes the binding even when /stop did not persist.
func releaseSessionIndex(session *heartbeatSession) {
	if session == nil || session.hold.dir == nil {
		return
	}
	removeSessionIndexForState(session.hold.dir.Name())
}
