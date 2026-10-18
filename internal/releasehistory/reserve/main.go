// SPDX-License-Identifier: AGPL-3.0-only
// reserve allocates the next published sequence with a fresh coordinate.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

func main() {
	repo := flag.String("repo", ".", "full checkout with release tags")
	version := flag.String("version", "", "fresh UTC calendar coordinate")
	ticket := flag.String("ticket", "", "owning AEON release ticket")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "reserve: unexpected arguments")
		os.Exit(1)
	}
	name, err := releasehistory.ReserveFile(context.Background(), *repo, *version, *ticket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reserve:", err)
		os.Exit(1)
	}
	fmt.Println("reserved:", *version, name)
}
