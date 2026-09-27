// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSubmitsOneReport(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "body.json")
	var stdout, stderr bytes.Buffer
	err := Run([]string{
		"--source", "cursor", "--model", "composer-2.5",
		"--submit", "/usr/bin/tee", "--submit-arg", dest,
	}, strings.NewReader(`{"type":"result","usage":{"inputTokens":4,"outputTokens":1,"cacheReadTokens":1,"cacheWriteTokens":0}}`), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var doc Result
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Reports) != 1 || doc.Reports[0].Model != "composer-2.5" || *doc.Reports[0].InputTokens != 4 {
		t.Fatalf("stdout: %s", stdout.String())
	}
	if !uuidRE.MatchString(doc.Reports[0].ReportID) {
		t.Fatalf("report id: %s", doc.Reports[0].ReportID)
	}
	submitted, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var posted UsageReport
	if err := json.Unmarshal(submitted, &posted); err != nil {
		t.Fatal(err)
	}
	if posted.ReportID != doc.Reports[0].ReportID || *posted.CachedInputTokens != 1 {
		t.Fatalf("submitted: %s", submitted)
	}
}

func TestRunPriorFile(t *testing.T) {
	dir := t.TempDir()
	prior := filepath.Join(dir, "prior.json")
	body := `[{"model":"gpt-6-luna","sequence":2,"input_tokens":10,"output_tokens":1,"cached_input_tokens":0}]`
	if err := os.WriteFile(prior, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run([]string{"--source", "codex", "--prior-file", prior}, strings.NewReader(`{"type":"turn.completed","usage":{"input_tokens":3,"cached_input_tokens":1,"output_tokens":1}}`), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var doc Result
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Reports[0].Sequence != 3 || *doc.Reports[0].InputTokens != 13 || *doc.Reports[0].CachedInputTokens != 1 {
		t.Fatalf("report: %+v", doc.Reports[0])
	}
}

func TestRunUsageError(t *testing.T) {
	err := Run(nil, strings.NewReader(""), ioDiscard{}, ioDiscard{})
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatal(err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
