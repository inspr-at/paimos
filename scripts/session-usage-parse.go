// SPDX-License-Identifier: AGPL-3.0-only

// Command session-usage-parse normalizes Codex or Cursor usage records.
// It reads stdin and an optional local checkpoint; no CLI or network calls.
// Persist stdout before posting its reports through the coordinator transport.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

func main() {
	if err := sessionusage.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var usage *sessionusage.UsageError
		if errors.As(err, &usage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
