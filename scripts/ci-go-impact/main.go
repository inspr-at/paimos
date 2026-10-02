// SPDX-License-Identifier: AGPL-3.0-only

// ci-go-impact consumes immutable Git objects and bounded diagnostic artifacts.
// It never runs go list/tests, fetches refs, publishes checks or changes CI work.
// Install reviewed binaries outside candidate workspaces for controller use.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/inspr-at/paimos/internal/ciproof"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func read(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("diagnostic input unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil || len(b) > 8<<20 {
		return nil, fmt.Errorf("diagnostic input limit")
	}
	return b, nil
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "shadow" {
		return fmt.Errorf("usage: ci-go-impact shadow --mirror ABSOLUTE_BARE_REPO --git ABSOLUTE_GIT --plan FILE [--analysis-context FILE --base-metadata FILE --candidate-metadata FILE --results FILE --record FILE]")
	}
	f := flag.NewFlagSet("shadow", flag.ContinueOnError)
	mirror := f.String("mirror", "", "controller-owned absolute bare mirror")
	git := f.String("git", "", "reviewed absolute Git executable")
	planFile := f.String("plan", "", "full foundation/authority plan JSON")
	analysisFile := f.String("analysis-context", "", "installed analysis image context JSON (diagnostic input)")
	baseFile := f.String("base-metadata", "", "base go list output from isolated metadata stage")
	candidateFile := f.String("candidate-metadata", "", "candidate go list output from isolated metadata stage")
	resultsFile := f.String("results", "", "full active-layout terminal result JSON")
	recordFile := f.String("record", "", "optional private controller-owned JSONL output")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || *planFile == "" {
		return fmt.Errorf("full plan required; no positional arguments")
	}
	r, err := ciproof.OpenRepository(ctx, *mirror, *git)
	if err != nil {
		return err
	}
	raw, err := read(*planFile)
	if err != nil {
		return err
	}
	var p ciproof.Plan
	if err := ciproof.Decode("plan", raw, &p); err != nil {
		return err
	}
	if err := ciproof.VerifyPlan(ctx, r, p); err != nil {
		return err
	}
	var base, candidate *ciproof.GoMetadata
	if *analysisFile != "" {
		raw, err := read(*analysisFile)
		if err != nil {
			return err
		}
		var c ciproof.GoAnalysisContext
		if err := ciproof.DecodeControllerInput(raw, &c); err != nil {
			return err
		}
		if *baseFile != "" {
			raw, err := read(*baseFile)
			if err != nil {
				return err
			}
			base, _ = ciproof.DecodeGoMetadata(p.Base, c, raw)
		}
		if *candidateFile != "" {
			raw, err := read(*candidateFile)
			if err != nil {
				return err
			}
			candidate, _ = ciproof.DecodeGoMetadata(p.Candidate, c, raw)
		}
	} else if *baseFile != "" || *candidateFile != "" {
		return fmt.Errorf("metadata context required")
	}
	var full *ciproof.GoFullRun
	if *resultsFile != "" {
		raw, err := read(*resultsFile)
		if err != nil {
			return err
		}
		value, err := ciproof.DecodeGoFullRun(raw)
		if err != nil {
			return err
		}
		full = &value
	}
	var impact ciproof.GoImpactReport
	var comparison *ciproof.GoShadowComparison
	if *recordFile != "" {
		impact, comparison, err = ciproof.RecordGoImpact(ctx, r, *recordFile, p, base, candidate, full)
	} else {
		impact, err = ciproof.AnalyzeGoImpact(ctx, r, p, base, candidate)
		if err == nil && full != nil {
			var value ciproof.GoShadowComparison
			value, err = ciproof.CompareGoImpact(p, impact, *full)
			comparison = &value
		}
	}
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Impact     ciproof.GoImpactReport      `json:"impact"`
		Comparison *ciproof.GoShadowComparison `json:"comparison"`
	}{impact, comparison}); err != nil {
		return err
	}
	if comparison != nil && (!comparison.Complete || comparison.Verdict != "zero-omitted-failures") {
		return fmt.Errorf("full shadow comparison failed: %s", comparison.Verdict)
	}
	return nil
}
