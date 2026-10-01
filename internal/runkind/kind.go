// SPDX-License-Identifier: AGPL-3.0-only
// Package runkind validates public execution labels shared by the CLI and server.
package runkind

import (
	"fmt"
	"regexp"
)

const Accepted = "codex, claude, pi, cursor, grok, media, terminal"

var generatorLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,119}$`)
var commandLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+ -]{0,119}$`)

func Valid(harness string) bool {
	switch harness {
	case "codex", "claude", "pi", "cursor", "grok", "media", "terminal":
		return true
	}
	return false
}

func Process(harness string) bool { return harness == "media" || harness == "terminal" }

func Validate(harness, generator, command string) error {
	if !Valid(harness) {
		return fmt.Errorf("--harness %q is not supported; use one of %s (media needs --generator; terminal needs --command)", harness, Accepted)
	}
	if harness == "media" {
		if !generatorLabel.MatchString(generator) {
			return fmt.Errorf("--generator must be 1–120 characters: letters, digits, ., _, :, /, +, -; start with a letter or digit")
		}
	} else if generator != "" {
		return fmt.Errorf("--generator requires --harness media")
	}
	if harness == "terminal" {
		if !commandLabel.MatchString(command) || command[len(command)-1] == ' ' {
			return fmt.Errorf("--command must be 1–120 characters: letters, digits, spaces, ., _, :, /, +, -; start with a letter or digit and omit trailing spaces")
		}
	} else if command != "" {
		return fmt.Errorf("--command requires --harness terminal")
	}
	return nil
}
