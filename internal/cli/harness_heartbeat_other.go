//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"os"
)

func readOwnerStamp(int) (ownerStamp, error) {
	return ownerStamp{}, errOwnerGone
}

func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("name sources require a unix host")
}

func openHeartbeatHold(string) (heartbeatHold, error) {
	return heartbeatHold{}, errors.New("heartbeat state requires a unix host")
}

func openPrivateHeartbeatDir(string) (*os.File, error) {
	return nil, errors.New("heartbeat state requires a unix host")
}

func acceptHeartbeatConfigDir(string) error {
	return errors.New("heartbeat state requires a unix host")
}

func (h *heartbeatHold) release() {
	if h == nil {
		return
	}
	if h.lock != nil {
		_ = h.lock.Close()
		h.lock = nil
	}
	if h.dir != nil {
		_ = h.dir.Close()
		h.dir = nil
	}
}

func (h *heartbeatHold) readFile(string, int) ([]byte, error) {
	return nil, errors.New("heartbeat state requires a unix host")
}

func (h *heartbeatHold) writeFile(string, []byte) error {
	return errors.New("heartbeat state requires a unix host")
}

func (h *heartbeatHold) remove(string) error {
	return errors.New("heartbeat state requires a unix host")
}
