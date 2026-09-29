// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

type setupPrompt struct {
	in   *bufio.Reader
	out  io.Writer
	json bool
}

func (p setupPrompt) read(label, explicitFlag string) (string, error) {
	if p.json {
		return "", fmt.Errorf("%s is required with --json", explicitFlag)
	}
	if _, err := fmt.Fprint(p.out, label); err != nil {
		return "", err
	}
	line, err := p.in.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("interactive confirmation unavailable; provide %s explicitly", explicitFlag)
	}
	if len(line) > 4096 {
		return "", errors.New("setup answer too long")
	}
	return strings.TrimSpace(line), nil
}

func (p setupPrompt) confirm(label, explicitFlag string) (bool, error) {
	answer, err := p.read(label+" [y/N] ", explicitFlag)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), err
}

func (p setupPrompt) selectHarnesses(candidates []agentsetup.Candidate) ([]agentsetup.Candidate, error) {
	if p.json {
		return nil, errors.New("--harness is required with --json")
	}
	if len(candidates) == 0 {
		return nil, errors.New("no signed-in supported harness found; use the vendor's normal login, then rerun pair (or --harness NAME for its diagnostic)")
	}
	var selected []agentsetup.Candidate
	for _, candidate := range candidates {
		yes, err := p.confirm(fmt.Sprintf("Pair %s (%s)?", candidate.Harness, candidate.Label), "--harness")
		if err != nil {
			return nil, err
		}
		if yes {
			selected = append(selected, candidate)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no harness selected; no pairing requested")
	}
	return selected, nil
}
