// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type accountProbeKind uint8

const (
	accountProbeCodex accountProbeKind = iota + 1
	accountProbeClaude
)

func runAccountProbe(ctx context.Context, path string, maximum int, kind accountProbeKind) ([]byte, error) {
	if maximum < 1 || maximum > 4096 {
		return nil, errors.New("account probe bound is invalid")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("account probe executable is not pinned")
	}
	var args []string
	switch kind {
	case accountProbeCodex:
		args = []string{"login", "status"}
	case accountProbeClaude:
		args = []string{"auth", "status", "--json"}
	default:
		return nil, errors.New("account probe kind is invalid")
	}
	output := &boundedAccountOutput{maximum: maximum}
	command := exec.CommandContext(ctx, path, args...) // #nosec G204 G702 -- canonical executable and closed internal argv above.
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.overflow {
		return nil, errors.New("account probe unavailable")
	}
	return output.Bytes(), nil
}

// resolvePinnedExecutable turns either an explicit operator path or one exact
// PATH lookup into a canonical regular executable. Callers never persist the
// path or derive it from prompts, profile fields, or remote reporter input.
func resolvePinnedExecutable(configured, name, label string) (string, error) {
	path := configured
	if path != "" {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return "", fmt.Errorf("configured %s executable must be a clean absolute path", label)
		}
	} else {
		var err error
		path, err = exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%s executable is unavailable", label)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("%s executable path is invalid", label)
		}
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical || strings.ContainsAny(canonical, "\x00\r\n") {
		return "", fmt.Errorf("%s executable cannot be pinned", label)
	}
	info, err := os.Stat(canonical) // #nosec G703 -- canonical is a clean absolute path resolved from the local operator configuration or one fixed-name PATH lookup.
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("%s executable is not a regular executable", label)
	}
	return canonical, nil
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
