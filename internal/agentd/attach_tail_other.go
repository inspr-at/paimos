// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package agentd

import "errors"

var errAttachTranscript = errors.New("pinned attach transcripts unsupported on this platform")

type attachTail struct{ id string }

func openAttachTail(string, int) (*attachTail, error) { return nil, errAttachTranscript }
func (*attachTail) close()                            {}
func (*attachTail) check() error                      { return errAttachTranscript }
func (*attachTail) startNow() error                   { return errAttachTranscript }
func (*attachTail) next() (string, error)             { return "", errAttachTranscript }
