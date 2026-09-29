// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

const lineageMarker = "[aeon doctrine lineage]\n"

func cutLineage(details string) (body, raw string, ok bool) {
	i := strings.LastIndex(details, lineageMarker)
	if i < 0 {
		return details, "", false
	}
	return strings.TrimSuffix(details[:i], "\n\n"), details[i+len(lineageMarker):], true
}

// contentFingerprint is the imported baseline. It covers every user-editable
// draft field, including the draft identity and source revision, so an API
// edit of either is a divergence even when edited_here stays false.
func contentFingerprint(r DraftRule) string {
	details, _, _ := cutLineage(r.Details)
	expires := ""
	if r.ExpiresAt != nil {
		expires = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	h := sha256.New()
	fmt.Fprintf(h, "text=%s\nwhy=%s\ndetails=%s\nstrength=%s\nenabled=%t\nexpires=%s\nroles=%s\nharnesses=%s\nreference=%s\nsource_identity=%s\nidentity=%s\nrevision=%s\nedited_here=%t\n",
		r.Text, r.Why, details, r.Strength, r.Enabled, expires,
		strings.Join(uniqueSorted(r.Roles), ","),
		strings.Join(uniqueSorted(r.Harnesses), ","),
		r.Source.Reference, r.Source.Identity, r.Identity, r.Source.Revision, r.Source.EditedHere)
	return hex.EncodeToString(h.Sum(nil))
}

// sourceRevision is the imported source revision stored on the draft. One file
// uses its raw SHA-256. Several files use a digest of their paths and raw
// hashes. That digest is fixed before the content baseline so the baseline can
// include the revision; the revision is not a digest of the lineage document
// that stores the baseline.
func sourceRevision(sources []SourceRef) string {
	if len(sources) == 0 {
		return ""
	}
	if len(sources) == 1 {
		return sources[0].FileSHA256
	}
	h := sha256.New()
	for _, src := range sources {
		fmt.Fprintf(h, "%s\n%s\n", src.Path, src.FileSHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseDraftLineage(details string) (draftLineage, bool) {
	_, raw, ok := cutLineage(details)
	if !ok {
		return draftLineage{}, false
	}
	var lineage draftLineage
	if json.Unmarshal([]byte(raw), &lineage) != nil || len(lineage.Sources) == 0 {
		return draftLineage{}, false
	}
	return lineage, true
}

func divergedFromBaseline(stored DraftRule) bool {
	_, raw, ok := cutLineage(stored.Details)
	if !ok {
		return true
	}
	var lineage draftLineage
	if json.Unmarshal([]byte(raw), &lineage) != nil || lineage.ContentSHA256 == "" {
		return true
	}
	return lineage.ContentSHA256 != contentFingerprint(stored)
}

func rejectEdited(stored DraftRule) error {
	if stored.Source.EditedHere {
		return fmt.Errorf("%w: existing identity was edited here; resolve it explicitly", ErrDraftConflict)
	}
	if divergedFromBaseline(stored) {
		return fmt.Errorf("%w: stored rule no longer matches the imported baseline for its source revision; the Aeon edit was kept", ErrDraftConflict)
	}
	return nil
}

func mergeImported(existing, imported []DraftRule) (merged []DraftRule, added, updated, unchanged int, err error) {
	merged = slices.Clone(existing)
	used := make([]bool, len(merged))
	index := map[string]int{}
	for i, rule := range merged {
		if _, ok := index[rule.Identity]; ok {
			return nil, 0, 0, 0, ErrDraftConflict
		}
		index[rule.Identity] = i
	}
	var pending []DraftRule
	for _, rule := range imported {
		i, ok := index[rule.Identity]
		if !ok {
			pending = append(pending, rule)
			continue
		}
		if used[i] {
			return nil, 0, 0, 0, fmt.Errorf("%w: duplicate imported identity", ErrDraftConflict)
		}
		used[i] = true
		if sameDraftRule(merged[i], rule) {
			unchanged++
			continue
		}
		if err = rejectEdited(merged[i]); err != nil {
			return nil, 0, 0, 0, err
		}
		merged[i] = rule
		updated++
	}
	assignment, err := assignLineage(pending, merged, used)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	for n, rule := range pending {
		j := assignment[n]
		if j < 0 {
			if _, exists := index[rule.Identity]; exists {
				return nil, 0, 0, 0, fmt.Errorf("%w: duplicate imported identity", ErrDraftConflict)
			}
			merged = append(merged, rule)
			index[rule.Identity] = len(merged) - 1
			used = append(used, true)
			added++
			continue
		}
		if err = rejectEdited(merged[j]); err != nil {
			return nil, 0, 0, 0, err
		}
		oldID := merged[j].Identity
		if other, exists := index[rule.Identity]; exists && other != j {
			return nil, 0, 0, 0, fmt.Errorf("%w: duplicate imported identity", ErrDraftConflict)
		}
		delete(index, oldID)
		merged[j] = rule
		index[rule.Identity] = j
		used[j] = true
		updated++
	}
	return merged, added, updated, unchanged, nil
}

type lineageEdge struct {
	in, ex int
	pos    bool
	sim    float64
}

func assignLineage(pending, existing []DraftRule, used []bool) ([]int, error) {
	out := make([]int, len(pending))
	for i := range out {
		out[i] = -1
	}
	if len(pending) == 0 {
		return out, nil
	}
	var edges []lineageEdge
	candIn := make([]int, len(pending))
	candEx := map[int]int{}
	for i, in := range pending {
		inLin, ok := parseDraftLineage(in.Details)
		if !ok {
			continue
		}
		for j, ex := range existing {
			if used[j] {
				continue
			}
			exLin, ok := parseDraftLineage(ex.Details)
			if !ok {
				continue
			}
			aligned, pos := lineageAligned(inLin, exLin)
			if !aligned {
				continue
			}
			edges = append(edges, lineageEdge{i, j, pos, textSimilarity(in.Text, ex.Text)})
			candIn[i]++
			candEx[j]++
		}
	}
	pairedIn := map[int]bool{}
	pairedEx := map[int]bool{}
	for i := range pending {
		if candIn[i] != 1 {
			continue
		}
		var edge lineageEdge
		for _, item := range edges {
			if item.in == i {
				edge = item
				break
			}
		}
		if candEx[edge.ex] != 1 {
			continue
		}
		out[i] = edge.ex
		pairedIn[i] = true
		pairedEx[edge.ex] = true
	}
	var rest []lineageEdge
	for _, edge := range edges {
		if pairedIn[edge.in] || pairedEx[edge.ex] {
			continue
		}
		rest = append(rest, edge)
	}
	slices.SortFunc(rest, func(a, b lineageEdge) int {
		if a.pos != b.pos {
			if a.pos {
				return -1
			}
			return 1
		}
		if a.sim != b.sim {
			if a.sim > b.sim {
				return -1
			}
			return 1
		}
		if a.in != b.in {
			return a.in - b.in
		}
		return a.ex - b.ex
	})
	for _, edge := range rest {
		if pairedIn[edge.in] || pairedEx[edge.ex] {
			continue
		}
		if !edge.pos && edge.sim < 0.5 {
			continue
		}
		if !lineageClear(edge, rest, pairedIn, pairedEx) {
			continue
		}
		out[edge.in] = edge.ex
		pairedIn[edge.in] = true
		pairedEx[edge.ex] = true
	}
	for _, edge := range edges {
		if pairedIn[edge.in] || pairedEx[edge.ex] {
			continue
		}
		return nil, fmt.Errorf("%w: unresolved replacement under %s; local proposal retained", ErrDraftConflict, headingOf(pending[edge.in]))
	}
	return out, nil
}

func lineageClear(edge lineageEdge, edges []lineageEdge, pairedIn, pairedEx map[int]bool) bool {
	for _, other := range edges {
		if other.in == edge.in && other.ex == edge.ex {
			continue
		}
		if other.in == edge.in && !pairedEx[other.ex] && !betterEdge(edge, other) {
			return false
		}
		if other.ex == edge.ex && !pairedIn[other.in] && !betterEdge(edge, other) {
			return false
		}
	}
	return true
}

func betterEdge(a, b lineageEdge) bool {
	if a.pos != b.pos {
		return a.pos
	}
	return a.sim >= b.sim+0.15
}

// headingsAlign compares the section path under the document title. A retitle
// keeps that path and must not look like a brand-new rule. The path below the
// title is empty when the rule sits directly under the title; those rules still
// align. A path with no separator is a section in a document that has no title
// and matches only when the whole path is unchanged.
func headingsAlign(left, right string) bool {
	if left == right {
		return true
	}
	lRest, lok := sectionBelowTitle(left)
	rRest, rok := sectionBelowTitle(right)
	return lok && rok && lRest == rRest
}

func sectionBelowTitle(path string) (string, bool) {
	_, rest, ok := strings.Cut(path, " / ")
	if !ok {
		return "", false
	}
	return rest, true
}

func lineageAligned(a, b draftLineage) (aligned, samePos bool) {
	for _, left := range a.Sources {
		for _, right := range b.Sources {
			if left.Path == "" || left.Path != right.Path || !headingsAlign(left.HeadingPath, right.HeadingPath) {
				continue
			}
			aligned = true
			if left.StartLine > 0 && left.StartLine == right.StartLine {
				samePos = true
			}
		}
	}
	return aligned, samePos
}

func headingOf(rule DraftRule) string {
	lineage, ok := parseDraftLineage(rule.Details)
	if !ok || len(lineage.Sources) == 0 || lineage.Sources[0].HeadingPath == "" {
		return "unknown heading"
	}
	return lineage.Sources[0].HeadingPath
}

func textSimilarity(a, b string) float64 {
	left := strings.Fields(normalizeText(a))
	right := strings.Fields(normalizeText(b))
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	counts := map[string]int{}
	for _, token := range left {
		counts[token]++
	}
	inter := 0
	for _, token := range right {
		if counts[token] > 0 {
			counts[token]--
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(left)+len(right))
}
