// SPDX-License-Identifier: AGPL-3.0-only

// Command stamp writes the reserved release's codename into version.json
// (AEON-430). Run it with the reservation, after release_sequence is set:
//
//	go run ./internal/releasehistory/codename/stamp -repo .
//
// It is idempotent and refuses a codename that differs from the one the
// sequence gives. packnotes -reserve runs the same step.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inspr-at/paimos/internal/releasehistory/codename"
)

func main() {
	repo := flag.String("repo", ".", "Aeon checkout")
	flag.Parse()
	name, changed, err := codename.StampFile(filepath.Join(*repo, "version.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "codename:", err)
		os.Exit(1)
	}
	state := "already in version.json"
	if changed {
		state = "written to version.json"
	}
	fmt.Printf("codename: %s (%s)\n", name, state)
}
