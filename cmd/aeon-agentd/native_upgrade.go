// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/inspr-at/paimos/internal/nativedrain"
)

// prepareNativeUpgrade is the I2 supervisor wiring seam. It only prepares and
// reports readiness; applying a release/restarting the service stays with its
// coordinator. The serving loop and native registry must remain alive on error.
func prepareNativeUpgrade(ctx context.Context, drain *nativedrain.Coordinator, out io.Writer) error {
	status, err := drain.Begin()
	if writeErr := writeNativeUpgradeStatus(out, status); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return err
	}
	status, err = drain.Drain(ctx)
	if writeErr := writeNativeUpgradeStatus(out, status); writeErr != nil {
		return writeErr
	}
	return err
}

func writeNativeUpgradeStatus(out io.Writer, status nativedrain.Status) error {
	if _, err := fmt.Fprintf(out, "native upgrade: %s; reason=%s; admission_closed=%t; pending_launches=%d; flush_confirmed=%t\n",
		status.State, status.Reason, status.AdmissionClosed, status.PendingLaunches, status.FlushConfirmed); err != nil {
		return err
	}
	for _, session := range status.Sessions {
		if _, err := fmt.Fprintf(out, "native session %s: %s; reason=%s\n", session.OwnedSessionID, session.State, session.Reason); err != nil {
			return err
		}
	}
	return nil
}
