// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package agentd

import (
	"errors"
	"github.com/inspr-at/paimos/internal/agentactivity"
)

var errAttachTranscript = errors.New("pinned attach transcripts unsupported on this platform")

type attachTail struct {
	id              string
	activityEnabled bool
	activity        *agentactivity.Activity
}

func openAttachTail(string, int) (*attachTail, error) { return nil, errAttachTranscript }
func (*attachTail) close()                            {}
func (*attachTail) check() error                      { return errAttachTranscript }
func (*attachTail) startNow() error                   { return errAttachTranscript }
func (*attachTail) next() (string, error)             { return "", errAttachTranscript }
