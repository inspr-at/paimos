// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// GoFullRun is diagnostic execution data. Each active-layout shard row has a
// terminal result, including all invocations of split packages. New packages
// without rows use their go-package obligation (the runner's catch-all). A
// selected-only run or a missing/skipped/cancelled shard is never a full comparison.
// Run/job authentication and signed reusable baseline evidence remain B/E/G.
type GoFullRun struct {
	Schema            string        `json:"schema"`
	PlanID            string        `json:"plan_id"`
	CandidateCommit   string        `json:"candidate_commit"`
	EnvironmentDigest string        `json:"environment_digest"`
	RunID             int64         `json:"run_id"`
	Attempt           int64         `json:"attempt"`
	Layout            int           `json:"layout"`
	Results           []GoRowResult `json:"results"`
}

type GoRowResult struct {
	ObligationID string `json:"obligation_id"`
	Package      string `json:"package"`
	Result       string `json:"result"`
}

type GoShadowComparison struct {
	Schema              string        `json:"schema"`
	Authority           string        `json:"authority"`
	ImpactID            string        `json:"impact_id"`
	PlanID              string        `json:"plan_id"`
	RunID               int64         `json:"run_id"`
	Attempt             int64         `json:"attempt"`
	Layout              int           `json:"layout"`
	ResultsDigest       string        `json:"results_digest"`
	Complete            bool          `json:"complete"`
	Verdict             string        `json:"verdict"`
	Expected            int           `json:"expected"`
	Observed            int           `json:"observed"`
	Missing             []string      `json:"missing"`
	OmittedFailures     []GoRowResult `json:"omitted_failures"`
	OmittedFailureCount *int          `json:"omitted_failure_count"`
}

func DecodeGoFullRun(raw []byte) (GoFullRun, error) {
	var run GoFullRun
	if err := decodeStrict(raw, &run); err != nil {
		return GoFullRun{}, err
	}
	return run, nil
}

// CompareGoImpact needs complete results for the full active layout before
// reporting a count of zero. It detects failures the *hint* would omit, while
// execution remains full. Timing/static are always fresh and excluded from the
// package savings comparison; no result here satisfies a required context.
func CompareGoImpact(p Plan, impact GoImpactReport, run GoFullRun) (GoShadowComparison, error) {
	if err := ValidatePlan(p); err != nil {
		return GoShadowComparison{}, err
	}
	if impact.ID != goImpactID(impact) || impact.PlanID != p.ID || impact.Mode != "shadow" || impact.Authority != "diagnostic-only" || run.Schema != "aeon.ci.go-full-run.v1" || run.PlanID != p.ID || run.CandidateCommit != p.Candidate.Commit || run.EnvironmentDigest != p.EnvironmentDigest || run.RunID <= 0 || run.Attempt != 1 || (run.Layout != 7 && run.Layout != 4) {
		return GoShadowComparison{}, fmt.Errorf("shadow comparison plan/run binding mismatch")
	}
	if run.Layout == 4 && p.Binding.Event == "pull_request" {
		return GoShadowComparison{}, fmt.Errorf("PR full result must use hosted seven-shard layout")
	}
	live := map[string]bool{}
	selected := map[string]bool{}
	for _, pkg := range impact.Packages {
		live[pkg.Package] = pkg.CandidatePresent
		selected[pkg.Package] = pkg.Selected
	}
	expected := map[string]string{}
	rows := map[string]bool{}
	prefix := fmt.Sprintf("go/%d/", run.Layout)
	for _, u := range p.Obligations {
		if u.Kind == "go-row" && live[u.Package] && strings.HasPrefix(u.ID, prefix) {
			expected[u.ID] = u.Package
			rows[u.Package] = true
		}
	}
	for _, u := range p.Obligations {
		if u.Kind == "go-package" && live[u.Package] && !rows[u.Package] {
			expected[u.ID] = u.Package
		}
	}
	if len(expected) == 0 {
		return GoShadowComparison{}, fmt.Errorf("empty full-run comparison inventory")
	}
	comparison := GoShadowComparison{Schema: "aeon.ci.go-shadow-comparison.v1", Authority: "diagnostic-only", ImpactID: impact.ID, PlanID: p.ID, RunID: run.RunID, Attempt: run.Attempt, Layout: run.Layout, Expected: len(expected), Verdict: "incomplete", Missing: []string{}, OmittedFailures: []GoRowResult{}}
	seen := map[string]bool{}
	results := append([]GoRowResult(nil), run.Results...)
	sort.Slice(results, func(i, j int) bool { return results[i].ObligationID < results[j].ObligationID })
	for _, result := range results {
		if expected[result.ObligationID] != result.Package || result.Package == "" || seen[result.ObligationID] {
			return GoShadowComparison{}, fmt.Errorf("unexpected, duplicate or wrong-package full-run result")
		}
		seen[result.ObligationID] = true
		switch result.Result {
		case "success", "failure":
			comparison.Observed++
		case "skipped", "cancelled":
			comparison.Missing = append(comparison.Missing, result.ObligationID)
		default:
			return GoShadowComparison{}, fmt.Errorf("unknown full-run terminal result")
		}
		if result.Result == "failure" && !selected[result.Package] {
			comparison.OmittedFailures = append(comparison.OmittedFailures, result)
		}
	}
	for id := range expected {
		if !seen[id] {
			comparison.Missing = append(comparison.Missing, id)
		}
	}
	sort.Strings(comparison.Missing)
	run.Results = results
	comparison.ResultsDigest = digest("full-Go-run-results", run)
	comparison.Complete = len(comparison.Missing) == 0
	if comparison.Complete {
		count := len(comparison.OmittedFailures)
		comparison.OmittedFailureCount = &count
		comparison.Verdict = "zero-omitted-failures"
		if count != 0 {
			comparison.Verdict = "omitted-failures"
		}
	}
	return comparison, nil
}

// RecordGoImpact reconstructs the selection before writing one bounded private
// JSONL diagnostic record. The file cannot be passed to the receipt ledger or
// used as a certificate. Unlike a status badge, it preserves incomplete and
// failed comparisons explicitly. Directory ownership belongs to the controller.
func RecordGoImpact(ctx context.Context, r *Repository, file string, p Plan, base, candidate *GoMetadata, full *GoFullRun) (GoImpactReport, *GoShadowComparison, error) {
	impact, err := AnalyzeGoImpact(ctx, r, p, base, candidate)
	if err != nil {
		return GoImpactReport{}, nil, err
	}
	var comparison *GoShadowComparison
	if full != nil {
		value, err := CompareGoImpact(p, impact, *full)
		if err != nil {
			return GoImpactReport{}, nil, err
		}
		comparison = &value
	}
	record := struct {
		Schema     string              `json:"schema"`
		Impact     GoImpactReport      `json:"impact"`
		Comparison *GoShadowComparison `json:"comparison"`
	}{"aeon.ci.go-shadow-record.v1", impact, comparison}
	b, err := json.Marshal(record)
	if err != nil {
		return GoImpactReport{}, nil, err
	}
	if len(b) > 16<<20 {
		return GoImpactReport{}, nil, fmt.Errorf("Go shadow record limit")
	}
	fd, err := unix.Open(file, unix.O_CREAT|unix.O_RDWR|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return GoImpactReport{}, nil, err
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return GoImpactReport{}, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return GoImpactReport{}, nil, fmt.Errorf("private regular Go shadow record file required")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return GoImpactReport{}, nil, err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	if info.Size() > 0 {
		if _, err := f.Seek(-1, io.SeekEnd); err != nil {
			return GoImpactReport{}, nil, err
		}
		var last [1]byte
		if _, err := io.ReadFull(f, last[:]); err != nil || last[0] != '\n' {
			return GoImpactReport{}, nil, fmt.Errorf("truncated Go shadow records")
		}
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return GoImpactReport{}, nil, err
	}
	if err := f.Sync(); err != nil {
		return GoImpactReport{}, nil, err
	}
	return impact, comparison, nil
}
