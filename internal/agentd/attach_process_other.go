// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package agentd

import "errors"

func attachPeerPID(int) (int, error) { return 0, errors.New("attach peer checks unsupported") }
func observeAttachProcess(int) (attachObservation, error) {
	return attachObservation{}, errors.New("attach process checks unsupported")
}

func observeAttachProcessIdentity(pid int) (attachObservation, error) {
	return observeAttachProcess(pid)
}
