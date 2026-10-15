// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

type benefitFlags struct{ PillEN, PillDE, BenefitEN, BenefitDE, Hide, NoReleaseNeeded string }

func (b *benefitFlags) flags(fs *flagSet) {
	fs.string(&b.PillEN, "pill-en", 0, "English benefit pill (2–4 words)")
	fs.string(&b.PillDE, "pill-de", 0, "German benefit pill (2–4 words)")
	fs.string(&b.BenefitEN, "benefit-en", 0, "English user benefit (one or two sentences)")
	fs.string(&b.BenefitDE, "benefit-de", 0, "German user benefit (one or two neutral sentences)")
	fs.string(&b.Hide, "hide-from-release-notes", 0, "true or false; hidden work is exempt from the release-text entry check")
	fs.string(&b.NoReleaseNeeded, "no-release-needed", 0, "true or false; content work needs no software release and is omitted from release notes")
}
func (b benefitFlags) changed() bool {
	return b.PillEN+b.PillDE+b.BenefitEN+b.BenefitDE+b.Hide+b.NoReleaseNeeded != ""
}
func (b benefitFlags) apply(fields map[string]any) (bool, error) {
	for key, value := range map[string]string{"pill_en": b.PillEN, "pill_de": b.PillDE, "benefit_en": b.BenefitEN, "benefit_de": b.BenefitDE} {
		if value != "" {
			fields[key] = value
		}
	}
	if b.Hide != "" {
		if b.Hide != "true" && b.Hide != "false" {
			return false, usagef("--hide-from-release-notes must be true or false")
		}
		value, _ := strconv.ParseBool(b.Hide)
		fields["hide_from_release_notes"] = value
	}
	if b.NoReleaseNeeded != "" {
		if b.NoReleaseNeeded != "true" && b.NoReleaseNeeded != "false" {
			return false, usagef("--no-release-needed must be true or false")
		}
		value, _ := strconv.ParseBool(b.NoReleaseNeeded)
		fields["no_release_needed"] = value
	}
	return b.changed(), nil
}

// One-time, read-only audit of visible open work. hide_closed uses the server's
// tenant-defined status categories; walkNodes enforces cursor and page bounds.
func (rt *runtime) reportInvalidPills(project string) error {
	proj, err := rt.projectNode(project)
	if err != nil {
		return err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return err
	}
	nodes, err := rt.walkNodes(url.Values{"parent_id": {proj.ID}, "include_descendants": {"true"}, "hide_closed": {"true"}}, nil)
	if err != nil {
		return err
	}
	type invalidPill struct {
		ID     string   `json:"id"`
		Key    string   `json:"issue_key"`
		State  string   `json:"status"`
		PillEN string   `json:"pill_en"`
		PillDE string   `json:"pill_de"`
		Issues []string `json:"issues"`
	}
	items := []invalidPill{}
	scanned := 0
	for _, node := range nodes {
		slug := kinds.slug(node.KindID)
		if slug != "work" && slug != "ticket" {
			continue
		}
		scanned++
		if issues := ticketbenefits.PillIssues(node.Fields); len(issues) > 0 {
			fields := fieldMap(node.Fields)
			items = append(items, invalidPill{node.ID, node.Key, node.State, fieldString(fields, "pill_en"), fieldString(fields, "pill_de"), issues})
		}
	}
	if rt.jsonOut {
		return rt.printJSON(map[string]any{"items": items, "scanned": scanned, "total": len(items)})
	}
	for _, item := range items {
		fmt.Fprintf(rt.stdout, "%s (%s): %s\n  pill_en: %q\n  pill_de: %q\n", item.Key, item.State, strings.Join(item.Issues, "; "), item.PillEN, item.PillDE)
	}
	fmt.Fprintf(rt.stdout, "%d open tickets scanned; %d with invalid pills.\n", scanned, len(items))
	return nil
}
