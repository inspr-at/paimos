// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
)

func readOpenRouterKey(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", errors.New("hidden OpenRouter key input requires a terminal; use --openrouter-env-file with a private local file")
	}
	if _, err := fmt.Fprint(out, "OpenRouter API key (hidden, stored only on this computer): "); err != nil {
		return "", err
	}
	raw, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", errors.New("hidden OpenRouter key input unavailable")
	}
	defer clear(raw)
	if len(raw) < 8 || len(raw) > 1024 {
		return "", errors.New("invalid OpenRouter key")
	}
	return string(raw), nil
}
