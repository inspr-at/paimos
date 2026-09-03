// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

func runAccountProbe(ctx context.Context, path string, maximum int, args ...string) ([]byte, error) {
	if maximum < 1 || maximum > 4096 {
		return nil, errors.New("account probe bound is invalid")
	}
	output := &boundedAccountOutput{maximum: maximum}
	command := exec.CommandContext(ctx, path, args...) // #nosec G204 -- pinned executable and adapter-owned fixed argv.
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.overflow {
		return nil, errors.New("account probe unavailable")
	}
	return output.Bytes(), nil
}

type boundedAccountOutput struct {
	bytes.Buffer
	maximum  int
	overflow bool
}

func (output *boundedAccountOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := output.maximum - output.Len()
	if remaining <= 0 {
		output.overflow = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		output.overflow = true
	}
	_, _ = output.Buffer.Write(value)
	return original, nil
}
