// SPDX-License-Identifier: AGPL-3.0-only
// exporthistoric reads through the configured agent client. Raw node responses
// never go to stdout; only release-note fields go to the ignored local export.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

type node struct {
	ID       string                     `json:"id"`
	Key      string                     `json:"key"`
	KindID   string                     `json:"kind_id"`
	ParentID *string                    `json:"parent_id"`
	Fields   map[string]json.RawMessage `json:"fields"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "historic export:", err)
		os.Exit(1)
	}
}

func run() error {
	repo := flag.String("repo", ".", "Aeon checkout with release tags")
	client := flag.String("client", "paimos", "configured PPM agent client executable (or wrapper)")
	tenant := flag.String("tenant", "", "expected PPM tenant UUID")
	project := flag.String("project", "", "expected AEON project UUID")
	out := flag.String("out", "", "new ignored local JSON file; never overwritten")
	flag.Parse()
	if flag.NArg() != 0 || *out == "" || releasehistory.HistoryBindingError(*tenant, *project, *tenant, *project) != nil {
		return fmt.Errorf("require -out FILE -tenant UUID -project UUID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("output must be inside the checkout")
	}
	if err := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "--quiet", "--", rel).Run(); err != nil {
		return fmt.Errorf("output must be git-ignored (use tmp/)")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return fmt.Errorf("output already exists or cannot be checked")
	}
	read := func(api string, into any) error {
		raw, err := exec.CommandContext(ctx, *client, "curl", api).Output()
		// Client stderr and raw API errors may contain private data. Never echo them.
		if err != nil {
			return fmt.Errorf("configured client could not read %s", api)
		}
		if json.Unmarshal(raw, into) != nil {
			return fmt.Errorf("invalid API response for %s", api)
		}
		return nil
	}
	var me struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	}
	if err := read("/api/me", &me); err != nil {
		return err
	}
	if me.Tenant.ID != *tenant {
		return fmt.Errorf("configured client is not in the selected tenant")
	}
	var kinds struct {
		Items []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := read("/api/kinds", &kinds); err != nil {
		return err
	}
	kind := map[string]string{}
	for _, item := range kinds.Items {
		kind[item.ID] = item.Slug
	}
	nodes := map[string]node{}
	getNode := func(id string) (node, error) {
		if n, ok := nodes[id]; ok {
			return n, nil
		}
		var n node
		err := read("/api/nodes/"+id, &n)
		if err == nil && n.ID != id {
			return n, fmt.Errorf("node identity mismatch")
		}
		if err == nil {
			nodes[id] = n
		}
		return n, err
	}
	p, err := getNode(*project)
	if err != nil {
		return err
	}
	if kind[p.KindID] != "project" {
		return fmt.Errorf("selected source is not a project")
	}
	history, err := releasehistory.Build(ctx, releasehistory.Options{Repo: root, Repository: "inspr-at/aeon"})
	if err != nil {
		return err
	}
	export := releasehistory.HistoricTicketExport{Schema: releasehistory.HistoricNotesSchema, TenantID: *tenant, ProjectID: *project, CapturedAt: time.Now().UTC().Truncate(time.Second), Tickets: []releasehistory.HistoricTicket{}}
	for _, key := range releasehistory.HistoricTicketKeys(history) {
		var n node
		if err := read("/api/node-keys/"+key, &n); err != nil {
			return err
		}
		if n.Key != key || kind[n.KindID] == "" {
			return fmt.Errorf("ticket identity/kind mismatch for %s", key)
		}
		parent := n
		seen := map[string]bool{}
		for parent.ID != *project {
			if parent.ParentID == nil || seen[parent.ID] || kind[parent.KindID] == "project" {
				return fmt.Errorf("%s is outside the selected project", key)
			}
			seen[parent.ID] = true
			parent, err = getNode(*parent.ParentID)
			if err != nil {
				return err
			}
		}
		fields := map[string]json.RawMessage{}
		for _, name := range []string{"pill_en", "pill_de", "benefit_en", "benefit_de", "type", "tags", "hide_from_release_notes"} {
			if value, ok := n.Fields[name]; ok {
				fields[name] = value
			}
		}
		raw, _ := json.Marshal(fields)
		export.Tickets = append(export.Tickets, releasehistory.HistoricTicket{Key: key, Kind: kind[n.KindID], Fields: raw})
	}
	raw, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Printf("historic export: %d distinct tickets; local file %s (review before packnotes -historic)\n", len(export.Tickets), rel)
	return nil
}
