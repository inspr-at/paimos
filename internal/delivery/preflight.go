// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

const preflightWorkflow = ".github/workflows/ci-preflight.yml"
const preflightGroups = 12

type preflightCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type localPreflight struct {
	Schema int              `json:"schema"`
	Kind   string           `json:"kind"`
	SHA    string           `json:"sha"`
	Base   string           `json:"base_sha"`
	Runner string           `json:"runner_class"`
	Checks []preflightCheck `json:"checks"`
	Status string           `json:"status"`
}
type browserPreflight struct {
	Schema  int    `json:"schema"`
	Kind    string `json:"kind"`
	SHA     string `json:"sha"`
	Run     int64  `json:"run_id"`
	Attempt int    `json:"run_attempt"`
	Runner  string `json:"runner_class"`
	Group   string `json:"group"`
	Status  string `json:"status"`
}
type preflightResult struct {
	Schema   int                `json:"schema"`
	SHA      string             `json:"sha"`
	Run      int64              `json:"run_id"`
	Attempt  int                `json:"run_attempt"`
	Local    localPreflight     `json:"local"`
	Browsers []browserPreflight `json:"browsers"`
	Status   string             `json:"status"`
}

// PreflightReason is shared by the builder admission path and the future
// required paimos/admission reporter. Evidence cannot satisfy any CI check.
func PreflightReason(raw []byte, sha string, run int64, attempt int) string {
	if len(raw) == 0 {
		return "preflight_missing"
	}
	if len(raw) > 32<<10 || !reviewgate.ValidSHA(sha) || run <= 0 || attempt <= 0 {
		return "preflight_invalid"
	}
	var result preflightResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return "preflight_invalid"
	}
	if result.SHA != sha || result.Local.SHA != sha {
		return "preflight_stale_sha"
	}
	if result.Schema != 1 || result.Run != run || result.Attempt != attempt || result.Local.Schema != 1 || result.Local.Kind != "local" ||
		result.Local.Runner != "mbp2606" || !reviewgate.ValidSHA(result.Local.Base) || len(result.Browsers) != preflightGroups {
		return "preflight_invalid"
	}
	names := []string{"static", "go-strict", "go-packages", "web-unit", "web-strict"}
	if len(result.Local.Checks) != len(names) {
		return "preflight_invalid"
	}
	localGreen, browserGreen := true, true
	for i, check := range result.Local.Checks {
		if check.ID != names[i] || check.Status != "passed" && check.Status != "failed" && check.Status != "not_run" {
			return "preflight_invalid"
		}
		localGreen = localGreen && check.Status == "passed"
	}
	for i, group := range result.Browsers {
		if group.SHA != sha {
			return "preflight_stale_sha"
		}
		if group.Schema != 1 || group.Kind != "browser" || group.Run != run || group.Attempt != attempt || group.Runner != "hosted" ||
			group.Group != fmt.Sprintf("browser-%d", i+1) || group.Status != "passed" && group.Status != "failed" {
			return "preflight_invalid"
		}
		browserGreen = browserGreen && group.Status == "passed"
	}
	wantLocal, wantAll := "failed", "failed"
	if localGreen {
		wantLocal = "passed"
	}
	if localGreen && browserGreen {
		wantAll = "passed"
	}
	if result.Local.Status != wantLocal || result.Status != wantAll {
		return "preflight_invalid"
	}
	if !localGreen {
		return "preflight_local_red"
	}
	if !browserGreen {
		return "preflight_browser_red"
	}
	return ""
}

func preflightJSON(raw []byte) ([]byte, error) {
	if len(raw) > 64<<10 {
		return nil, errRead
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) != 1 {
		return nil, errRead
	}
	file := reader.File[0]
	if file.Name != "preflight.json" || file.UncompressedSize64 > 32<<10 || !file.Mode().IsRegular() {
		return nil, errRead
	}
	stream, err := file.Open()
	if err != nil {
		return nil, errRead
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, (32<<10)+1))
	if err != nil || len(data) > 32<<10 {
		return nil, errRead
	}
	return data, nil
}

type preflightRun struct {
	ID         int64  `json:"id"`
	Attempt    int    `json:"run_attempt"`
	Event      string `json:"event"`
	Branch     string `json:"head_branch"`
	Head       string `json:"head_sha"`
	Title      string `json:"display_title"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func lookupPreflight(get func(string, any) error, archive func(int64) ([]byte, error), sha string) (string, error) {
	if !reviewgate.ValidSHA(sha) {
		return "preflight_invalid", nil
	}
	var latest preflightRun
	total, seen := -1, map[int64]bool{}
	for page := 1; page <= 10; page++ {
		var response struct {
			Total int            `json:"total_count"`
			Runs  []preflightRun `json:"workflow_runs"`
		}
		if err := get("/actions/workflows/ci-preflight.yml/runs?event=workflow_dispatch&branch=main&per_page=100&page="+strconv.Itoa(page), &response); err != nil {
			return "", err
		}
		if response.Total < 0 || response.Total > 1000 || len(response.Runs) > 100 || total != -1 && total != response.Total {
			return "", errRead
		}
		total = response.Total
		for _, run := range response.Runs {
			if run.ID <= 0 || seen[run.ID] || run.Attempt < 1 || run.Path != preflightWorkflow || run.Event != "workflow_dispatch" || run.Branch != "main" || !reviewgate.ValidSHA(run.Head) {
				return "", errRead
			}
			seen[run.ID] = true
			if run.Title == "preflight:"+sha && run.ID > latest.ID {
				latest = run
			}
		}
		if len(response.Runs) < 100 {
			break
		}
	}
	if len(seen) != total {
		return "", errRead
	}
	if latest.ID == 0 {
		return "preflight_missing", nil
	}
	if latest.Status != "completed" {
		return "preflight_pending", nil
	}
	// Never fall back to an older green run or previous successful attempt.
	if latest.Conclusion != "success" {
		return "preflight_browser_red", nil
	}
	var artifacts struct {
		Total int `json:"total_count"`
		Items []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Size    int64  `json:"size_in_bytes"`
			Expired bool   `json:"expired"`
			Run     struct {
				ID   int64  `json:"id"`
				Head string `json:"head_sha"`
			} `json:"workflow_run"`
		} `json:"artifacts"`
	}
	if err := get(fmt.Sprintf("/actions/runs/%d/artifacts?per_page=100", latest.ID), &artifacts); err != nil {
		return "", err
	}
	if artifacts.Total < 0 || artifacts.Total > 100 || artifacts.Total != len(artifacts.Items) {
		return "", errRead
	}
	id := int64(0)
	for _, item := range artifacts.Items {
		if item.Name != fmt.Sprintf("aeon-preflight-%s-%d", sha, latest.Attempt) {
			continue
		}
		if id != 0 || item.ID <= 0 || item.Expired || item.Size <= 0 || item.Size > 64<<10 || item.Run.ID != latest.ID || item.Run.Head != latest.Head {
			return "preflight_invalid", nil
		}
		id = item.ID
	}
	if id == 0 {
		return "preflight_missing", nil
	}
	zipBytes, err := archive(id)
	if err != nil {
		return "", err
	}
	raw, err := preflightJSON(zipBytes)
	if err != nil {
		return "", err
	}
	return PreflightReason(raw, sha, latest.ID, latest.Attempt), nil
}

// LookupPreflight admits only repository-authenticated workflow/artifact reads.
// Callers install this reviewed implementation outside candidate workspaces.
func LookupPreflight(get func(string, any) error, archive func(int64) ([]byte, error), sha string) (string, error) {
	return lookupPreflight(get, archive, sha)
}

// Preflight performs a bounded fail-closed lookup with existing read-only App
// authority. The server never accepts a caller-supplied green assertion.
func (g AppReader) Preflight(ctx context.Context, head string) (string, error) {
	if g.App == nil {
		return "preflight_lookup_failed", errRead
	}
	reason := "preflight_lookup_failed"
	err := g.App.ReadArtifactInstallation(ctx, func(get func(string, any) error, archive func(int64) ([]byte, error)) error {
		var err error
		reason, err = lookupPreflight(get, archive, head)
		return err
	})
	if err != nil {
		return "preflight_lookup_failed", err
	}
	return reason, nil
}
