// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/inspr-at/paimos/backend/internal/knowledge857"
)

func main() {
	var source, clone, report string
	flag.StringVar(&source, "source-backup-dir", "", "absolute path to the exact locked backup trio")
	flag.StringVar(&clone, "clone-dir", "", "absolute path for a new repaired clone")
	flag.StringVar(&report, "report", "", "absolute path for a new content-free JSON report")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	_, err := knowledge857.Run(context.Background(), knowledge857.Options{SourceBackupDir: source, CloneDir: clone, ReportPath: report, Policy: knowledge857.ProductionPolicy})
	if err != nil {
		fmt.Fprintf(os.Stderr, "PAI-857 repair refused: %v\nreport: %s\n", err, report)
		os.Exit(1)
	}
	fmt.Printf("PAI-857 repaired clone is clean; report: %s\n", report)
}
