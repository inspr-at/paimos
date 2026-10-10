// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func greenPreflight(sha string) preflightResult {
	local := localPreflight{Schema: 1, Kind: "local", SHA: sha, Base: strings.Repeat("a", 40), Runner: "build-6", Status: "passed"}
	for _, id := range []string{"static", "go-strict", "go-packages", "web-unit", "web-strict"} {
		local.Checks = append(local.Checks, preflightCheck{id, "passed"})
	}
	out := preflightResult{Schema: 1, SHA: sha, Run: 42, Attempt: 2, Local: local, Status: "passed"}
	for i := 1; i <= 12; i++ {
		out.Browsers = append(out.Browsers, browserPreflight{Schema: 1, Kind: "browser", SHA: sha, Run: 42, Attempt: 2, Runner: "hosted", Group: fmt.Sprintf("browser-%d", i), Status: "passed"})
	}
	return out
}
func preflightArchive(t *testing.T, result preflightResult) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	stream, err := writer.Create("preflight.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(stream).Encode(result); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func TestPreflightAdmissionMissingRedStaleAndLookupFailure(t *testing.T) {
	// Risk: missing/red/stale evidence, a previous run attempt, or incomplete
	// GitHub lookup authorizes opening/updating a PR or pushing a new head.
	head := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, reason string
		change       func(*preflightResult)
	}{
		{"green", "", func(*preflightResult) {}},
		{"another declared runner", "", func(r *preflightResult) { r.Local.Runner = "build-linux" }},
		{"missing declared runner", "preflight_invalid", func(r *preflightResult) { r.Local.Runner = "" }},
		{"oversized declared runner", "preflight_invalid", func(r *preflightResult) { r.Local.Runner = strings.Repeat("r", 129) }},
		{"malformed declared runner", "preflight_invalid", func(r *preflightResult) { r.Local.Runner = "build mac" }},
		{"newline declared runner", "preflight_invalid", func(r *preflightResult) { r.Local.Runner = "build-mac\n" }},
		{"local red", "preflight_local_red", func(r *preflightResult) {
			r.Local.Checks[0].Status = "failed"
			r.Local.Status = "failed"
			r.Status = "failed"
		}},
		{"browser red", "preflight_browser_red", func(r *preflightResult) { r.Browsers[0].Status = "failed"; r.Status = "failed" }},
		{"stale SHA", "preflight_stale_sha", func(r *preflightResult) { r.Local.SHA = strings.Repeat("c", 40) }},
		{"stale attempt", "preflight_invalid", func(r *preflightResult) { r.Browsers[0].Attempt = 1 }},
		{"partial groups", "preflight_invalid", func(r *preflightResult) { r.Browsers = r.Browsers[1:] }},
		{"wrong runner", "preflight_invalid", func(r *preflightResult) { r.Browsers[0].Runner = "build-6" }},
		{"contradictory green", "preflight_invalid", func(r *preflightResult) { r.Local.Checks[0].Status = "failed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := greenPreflight(head)
			tc.change(&result)
			raw, _ := json.Marshal(result)
			if reason := PreflightReason(raw, head, 42, 2); reason != tc.reason {
				t.Fatalf("reason %q; want %q", reason, tc.reason)
			}
		})
	}
	if reason := PreflightReason(nil, head, 42, 2); reason != "preflight_missing" {
		t.Fatal(reason)
	}
	result := greenPreflight(head)
	missing, red, partial, expired, wrongHead, lookupFailure := false, false, false, false, false, false
	get := func(path string, out any) error {
		if lookupFailure {
			return errors.New("read refused")
		}
		var response any
		switch {
		case strings.Contains(path, "/workflows/ci-preflight.yml/runs?"):
			runs := []preflightRun{{ID: 42, Attempt: 2, Event: "workflow_dispatch", Branch: "main", Head: strings.Repeat("a", 40), Title: "preflight:" + head, Path: preflightWorkflow, Status: "completed", Conclusion: "success"}}
			if red {
				runs[0].Conclusion = "failure"
			}
			if missing {
				runs = nil
			}
			total := len(runs)
			if partial {
				total++
			}
			response = map[string]any{"total_count": total, "workflow_runs": runs}
		case path == "/actions/runs/42/artifacts?per_page=100":
			workflowHead := strings.Repeat("a", 40)
			if wrongHead {
				workflowHead = head
			}
			response = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 77, "name": "aeon-preflight-" + head + "-2", "size_in_bytes": 2000, "expired": expired, "workflow_run": map[string]any{"id": 42, "head_sha": workflowHead}}}}
		default:
			t.Fatalf("unbound lookup: %s", path)
		}
		raw, _ := json.Marshal(response)
		return json.Unmarshal(raw, out)
	}
	archive := func(id int64) ([]byte, error) {
		if id != 77 {
			t.Fatal("wrong artifact")
		}
		return preflightArchive(t, result), nil
	}
	for _, tc := range []struct {
		name, reason string
		configure    func()
		wantErr      bool
	}{
		{"green", "", func() {}, false},
		{"missing", "preflight_missing", func() { missing = true }, false},
		{"red", "preflight_browser_red", func() { red = true }, false},
		{"expired", "preflight_invalid", func() { expired = true }, false},
		{"wrong artifact head", "preflight_invalid", func() { wrongHead = true }, false},
		{"partial lookup", "", func() { partial = true }, true},
		{"lookup failure", "", func() { lookupFailure = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing, red, partial, expired, wrongHead, lookupFailure = false, false, false, false, false, false
			tc.configure()
			reason, err := lookupPreflight(get, archive, head)
			if (err != nil) != tc.wantErr || reason != tc.reason {
				t.Fatalf("got %q %v; want %q error=%v", reason, err, tc.reason, tc.wantErr)
			}
		})
	}
}

func TestDeliveryShippingRefusesPreflightWithoutGrantingActions(t *testing.T) {
	// The authorized final transaction records the actual builder-visible deny,
	// even when all review and CI facts for this same head are already green.
	f, source, reader := shipFixture(t)
	enableShipping(t, f)
	for i, reason := range []string{"preflight_missing", "preflight_local_red", "preflight_browser_red", "preflight_stale_sha", "preflight_lookup_failed", ""} {
		reader.preflightReason = reason
		reader.preflightErr = nil
		if reason == "preflight_lookup_failed" {
			reader.preflightErr = errors.New("lookup failed")
		}
		var out ShipDecision
		f.call(t, f.person, "POST", shipPath(f)+"/claim", ShipInput{Request: requestID(700 + i), Source: source.ID, Head: strings.Repeat("b", 40)}, 200, &out)
		if reason == "" {
			if out.Action != "enqueue" || !out.Claimed {
				t.Fatalf("green not admitted: %+v", out)
			}
		} else if out.Reason != reason || out.Claimed || out.Action != "wait" {
			t.Fatalf("preflight denial bypassed: %+v", out)
		}
		if out.Execute {
			t.Fatal("shadow gained execution authority")
		}
	}
}
