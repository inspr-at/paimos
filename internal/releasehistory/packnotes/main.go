// SPDX-License-Identifier: AGPL-3.0-only
// packnotes is the offline reserve/backfill step. Inputs are explicit snapshot
// exports, never PR bodies, TSVs or live ticket fields. It writes only the public
// projection and leaves existing version entries immutable.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "product notes:", err)
		os.Exit(1)
	}
}

func run() error {
	repo := flag.String("repo", ".", "Aeon checkout")
	snapshots := flag.String("snapshots", "", "directory of frozen VERSION.json snapshot exports")
	historyFile := flag.String("history", "", "reviewed /api/releases export with tenant_id and project_node_id; frozen notes only")
	snapshot := flag.String("snapshot", "", "one snapshot export for the reserved version")
	reserve := flag.String("reserve", "", "explicitly freeze the preview for version.json's reserved coordinate")
	tenant := flag.String("tenant", "", "expected source tenant UUID (required for exports)")
	project := flag.String("project", "", "expected source AEON project UUID (required for exports)")
	flag.Parse()
	inputs := 0
	for _, value := range []string{*snapshot, *snapshots, *historyFile} {
		if value != "" {
			inputs++
		}
	}
	if flag.NArg() != 0 || ((*snapshot == "") != (*reserve == "")) || inputs > 1 {
		return fmt.Errorf("use -snapshots DIR or -snapshot FILE -reserve VERSION; exports require -tenant UUID -project UUID")
	}
	if (*snapshots != "" || *snapshot != "" || *historyFile != "") && (*tenant == "" || *project == "") {
		return fmt.Errorf("exports require -tenant UUID -project UUID")
	}
	path := filepath.Join(*repo, releasehistory.ProductNotesPath)
	bundle := releasehistory.EmptyProductNotes()
	if raw, err := os.ReadFile(path); err == nil {
		bundle, err = releasehistory.ReadProductNotes(raw)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Tag import uses the canonical name. A saved /api/releases export may name
	// inspr-at/paimos; AddHistory accepts either alias of this product.
	history, err := releasehistory.Build(context.Background(), releasehistory.Options{Repo: *repo, Repository: "inspr-at/aeon"})
	if err != nil {
		return err
	}
	if history.Product != bundle.Product {
		return fmt.Errorf("this exporter is only for PAIMOS AEON")
	}
	// Tagged snapshots already have an immutable version binding. Import their
	// public fields only; historical tags and the raw files remain untouched.
	if err := bundle.AddHistory(history); err != nil {
		return err
	}
	if *historyFile != "" {
		raw, err := os.ReadFile(*historyFile)
		if err != nil {
			return err
		}
		var exported struct {
			releasehistory.History
			TenantID  string `json:"tenant_id"`
			ProjectID string `json:"project_node_id"`
		}
		if len(raw) > 16<<20 || json.Unmarshal(raw, &exported) != nil {
			return fmt.Errorf("invalid release history export")
		}
		if err := releasehistory.HistoryBindingError(exported.TenantID, exported.ProjectID, *tenant, *project); err != nil {
			return err
		}
		if err := bundle.AddHistory(exported.History); err != nil {
			return err
		}
	}
	add := func(path, version string, reserve bool) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		notes, err := releasehistory.PublicNotesFromSnapshot(raw, version, *tenant, *project, reserve)
		if err != nil {
			return fmt.Errorf("snapshot %s rejected: %w", version, err)
		}
		return bundle.Add(version, notes)
	}
	if *snapshots != "" {
		entries, err := os.ReadDir(*snapshots)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			version := strings.TrimSuffix(entry.Name(), ".json")
			if !releasehistory.ValidVersion(version) {
				return fmt.Errorf("snapshot filenames must be VERSION.json")
			}
			if err := add(filepath.Join(*snapshots, entry.Name()), version, false); err != nil {
				return err
			}
		}
	}
	if *reserve != "" {
		raw, err := os.ReadFile(filepath.Join(*repo, "version.json"))
		if err != nil {
			return err
		}
		var head struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &head) != nil || head.Version != *reserve {
			return fmt.Errorf("reserve must match version.json")
		}
		if err := add(*snapshot, *reserve, true); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if _, err := releasehistory.ReadProductNotes(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Do not leave a truncated manifest if generation is interrupted.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".product-notes-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // only this invocation's temporary output
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	missing, unclassified := 0, 0
	for _, rel := range history.Releases {
		if _, ok := bundle.Releases[rel.Version]; !ok {
			missing++
		}
	}
	for _, notes := range bundle.Releases {
		for _, item := range notes.Items {
			if item.Group == "" {
				unclassified++
			}
		}
	}
	fmt.Printf("product notes: %d captures; %d history versions without authoritative snapshots; %d historical notes without captured groups\n", len(bundle.Releases), missing, unclassified)
	return nil
}
