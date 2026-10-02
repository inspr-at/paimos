// SPDX-License-Identifier: AGPL-3.0-only
// packnotes is the offline reserve/backfill step. Inputs are explicit snapshot
// exports, or explicitly selected historic ticket exports. It writes only the public
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
	"github.com/inspr-at/paimos/internal/releasehistory/codename"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "product notes:", err)
		os.Exit(1)
	}
}

func run() error {
	return runWithArgs(os.Args[1:])
}

func runWithArgs(args []string) error {
	flags := flag.NewFlagSet("packnotes", flag.ContinueOnError)
	repo := flags.String("repo", ".", "Aeon checkout")
	snapshots := flags.String("snapshots", "", "directory of frozen VERSION.json snapshot exports")
	historyFile := flags.String("history", "", "reviewed /api/releases export with tenant_id and project_node_id; frozen notes only")
	historic := flags.String("historic", "", "reviewed historic ticket export; freeze public fields for published Git membership")
	snapshot := flags.String("snapshot", "", "one snapshot export for the reserved version")
	reserve := flags.String("reserve", "", "explicitly freeze the preview for version.json's reserved coordinate")
	reuseFrom := flags.String("reuse-from", "", "failed coordinate whose exact original historic export is reused")
	tenant := flags.String("tenant", "", "expected source tenant UUID (required for exports)")
	project := flags.String("project", "", "expected source AEON project UUID (required for exports)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	inputs := 0
	for _, value := range []string{*snapshot, *snapshots, *historyFile, *historic} {
		if value != "" {
			inputs++
		}
	}
	if flags.NArg() != 0 || (*snapshot != "" && *reserve == "") || (*reserve != "" && *snapshot == "" && *historic == "") || inputs > 1 {
		return fmt.Errorf("select one of -snapshots DIR, -history FILE, -historic FILE [-reserve VERSION] or -snapshot FILE -reserve VERSION; exports require -tenant UUID -project UUID")
	}
	if inputs > 0 && (*tenant == "" || *project == "") {
		return fmt.Errorf("exports require -tenant UUID -project UUID")
	}
	if *reuseFrom != "" && (*historic == "" || *reserve == "") {
		return fmt.Errorf("-reuse-from requires -historic and -reserve")
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
	failedVersions := map[string]bool{}
	for _, rel := range history.Releases {
		if rel.State != releasehistory.StatePublished {
			failedVersions[rel.Version] = true
		}
	}
	// Tagged snapshots already have an immutable version binding. Import their
	// public fields only; historical tags and the raw files remain untouched.
	if err := bundle.AddHistory(history); err != nil {
		return err
	}
	if *historic != "" {
		raw, err := os.ReadFile(*historic)
		if err != nil {
			return err
		}
		if *reserve != "" {
			var export releasehistory.HistoricTicketExport
			if json.Unmarshal(raw, &export) != nil || export.Reservation == nil {
				return fmt.Errorf("historic export must bind a reservation")
			}
			reservation, err := releasehistory.ReadNoteReservation(*repo, *reserve, export.Reservation.Tickets)
			if err != nil {
				return err
			}
			add := func() error { return bundle.AddReserved(raw, reservation, *tenant, *project) }
			if *reuseFrom != "" {
				failed := false
				for _, r := range history.Releases {
					if r.Version == *reuseFrom && r.State != releasehistory.StatePublished {
						failed = true
					}
				}
				if !failed {
					return fmt.Errorf("-reuse-from must name an explicitly unpublished or withdrawn coordinate")
				}
				add = func() error { return bundle.AddRereserved(raw, reservation, *reuseFrom, *tenant, *project) }
			}
			if err := add(); err != nil {
				return err
			}
		} else {
			report, err := bundle.AddHistoric(history, raw, *tenant, *project)
			if err != nil {
				return err
			}
			fmt.Printf("historic notes: %d releases backfilled; %d ticket occurrences classified; %d under Other; %d hidden\n", report.Releases, report.Classified, report.Other, report.Hidden)
		}
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
		for _, rel := range exported.Releases {
			if rel.State == releasehistory.StatePublished && failedVersions[rel.Version] {
				return fmt.Errorf("history export cannot publish failed coordinate %s", rel.Version)
			}
		}
		if err := bundle.AddHistory(exported.History); err != nil {
			return err
		}
	}
	add := func(path, version string, reserve bool) error {
		if failedVersions[version] {
			return fmt.Errorf("cannot capture failed coordinate %s", version)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		notes, err := releasehistory.PublicNotesFromSnapshot(raw, version, *tenant, *project, reserve)
		if err != nil {
			return fmt.Errorf("snapshot %s rejected: %w", version, err)
		}
		if reserve {
			reservation, err := releasehistory.ReadNoteReservation(*repo, version, []string{})
			if err != nil {
				return err
			}
			notes.ReleaseChannel = reservation.Channel
			notes.ReleaseSequence = reservation.Sequence
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
	if *reserve != "" && *snapshot != "" {
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
	if *reserve != "" {
		// The reservation also carries its codename (AEON-430), presentation only.
		name, _, err := codename.StampFile(filepath.Join(*repo, "version.json"))
		if err != nil {
			return err
		}
		fmt.Printf("codename: %s\n", name)
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
