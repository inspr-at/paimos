// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const llmsTextLimit = 200 * 1024

func renderLlms(slug string, doc portalDocument, releases []publicRelease) string {
	if !doc.ReleaseHistory {
		releases = nil
	}
	page, catalog, history, historyJSON := portalFileLinks(slug)
	var b strings.Builder
	title := "Product portal"
	if doc.Product != nil {
		if line := publicLine(doc.Product.Title, 300); line != "" {
			title = line
		}
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	switch {
	case doc.Product == nil:
		b.WriteString("> Nothing published yet.\n\n")
	default:
		if summary := publicLine(doc.Product.Summary, 4000); summary != "" {
			fmt.Fprintf(&b, "> %s\n\n", summary)
		}
		writeLlmsCatalog(&b, page, doc.Catalog)
		writeLlmsWishes(&b, page, doc.Wishes)
		writeLlmsComparison(&b, page, doc.Comparison)
		writeLlmsPace(&b, doc.Pace)
		writeLlmsReleases(&b, history, releases)
	}
	body := b.String()
	footer := fmt.Sprintf("## Machine-readable\n\n- [Catalog JSON](%s)\n", catalog)
	if doc.ReleaseHistory {
		footer += fmt.Sprintf("- [Release history](%s)\n- [Release history JSON](%s)\n", history, historyJSON)
	}
	if len(body)+len(footer) > llmsTextLimit {
		room := llmsTextLimit - len(footer)
		if room < 1 {
			room = 1
		}
		body = trimUTF8(body, room)
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
	}
	return body + footer
}

func portalFileLinks(slug string) (page, catalog, history, historyJSON string) {
	page = "/portal/" + slug
	catalog = page + "/catalog.json"
	history = page + "/releases"
	historyJSON = "/api/public/portal/" + slug + "/releases"
	return page, catalog, history, historyJSON
}

func writeLlmsCatalog(b *strings.Builder, page string, items []portalFeature) {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		label := linkLabel(publicLine(item.Title, 300))
		if label == "" {
			continue
		}
		line := fmt.Sprintf("- [%s](%s)", label, page)
		if summary := strings.TrimRight(publicLine(item.Summary, 4000), "."); summary != "" {
			line += ": " + summary
		}
		switch {
		case item.Status == "live" && item.LiveSince != "":
			line += ". Live since " + item.LiveSince + "."
		default:
			if status := catalogStatusLabel(item.Status); status != "" {
				line += ". " + status + "."
			}
		}
		if item.Status == "declined" {
			if reason := publicLine(item.DeclineReason, 500); reason != "" {
				line += " " + reason
			}
		}
		lines = append(lines, line)
	}
	writeLlmsSection(b, "Catalog", lines)
}

func writeLlmsWishes(b *strings.Builder, page string, items []portalWish) {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		label := linkLabel(publicLine(item.Title, 300))
		if label == "" {
			continue
		}
		line := fmt.Sprintf("- [%s](%s)", label, page)
		if summary := strings.TrimRight(publicLine(item.Summary, 4000), "."); summary != "" {
			line += ": " + summary
		}
		line += ". " + votesPhrase(item.Votes)
		lines = append(lines, line)
	}
	writeLlmsSection(b, "Wishes", lines)
}

func writeLlmsComparison(b *strings.Builder, page string, rows []portalComparisonRow) {
	lines := []string{}
	for _, row := range rows {
		aspect := linkLabel(publicLine(row.Aspect, 120))
		if aspect == "" {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Stance == "" || cell.Stance == "unknown" {
				continue
			}
			who := publicLine(cell.Competitor, 80)
			stance := comparisonStance(cell.Stance)
			if who == "" || stance == "" {
				continue
			}
			line := fmt.Sprintf("- [%s](%s): %s, %s.", aspect, page, who, stance)
			if quote := publicLine(cell.Quote, 400); quote != "" {
				line += " " + quote
			}
			lines = append(lines, line)
		}
	}
	writeLlmsSection(b, "Comparison", lines)
}

func writeLlmsPace(b *strings.Builder, pace *portalPace) {
	if pace == nil {
		return
	}
	lines := []string{}
	if pace.Releases30d != nil {
		lines = append(lines, fmt.Sprintf("- %d releases in 30 days.", *pace.Releases30d))
	}
	if pace.MedianReleaseGapDays != nil {
		lines = append(lines, fmt.Sprintf("- %d days between releases.", *pace.MedianReleaseGapDays))
	}
	if pace.WishToLiveMedianDays != nil {
		lines = append(lines, fmt.Sprintf("- %d days from a wish to live.", *pace.WishToLiveMedianDays))
	}
	writeLlmsSection(b, "Pace", lines)
}

func writeLlmsReleases(b *strings.Builder, history string, releases []publicRelease) {
	lines := make([]string, 0, len(releases))
	for _, rel := range releases {
		label := linkLabel(rel.Version)
		if label == "" {
			if at, err := time.Parse(time.RFC3339, rel.ReleasedAt); err == nil {
				label = at.UTC().Format("2006-01-02")
			}
		}
		if label == "" {
			continue
		}
		line := fmt.Sprintf("- [%s](%s)", label, history)
		parts := []string{}
		for _, note := range rel.Notes {
			pill := strings.TrimRight(strings.TrimSpace(publicLine(note.PillEN, 80)), ".")
			benefit := strings.TrimSpace(publicLine(note.BenefitEN, 200))
			bit := pill
			if benefit != "" {
				if bit != "" {
					bit += ". " + benefit
				} else {
					bit = benefit
				}
			}
			if bit != "" {
				parts = append(parts, bit)
			}
		}
		if len(parts) > 0 {
			if blurb := publicLine(strings.Join(parts, " "), 600); blurb != "" {
				line += ": " + blurb
			}
		}
		lines = append(lines, line)
	}
	writeLlmsSection(b, "Releases", lines)
}

func writeLlmsSection(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func linkLabel(s string) string {
	return strings.TrimSpace(strings.NewReplacer("[", "", "]", "").Replace(s))
}

func catalogStatusLabel(status string) string {
	switch status {
	case "idea":
		return "Idea"
	case "reviewed":
		return "Reviewed"
	case "planned":
		return "Planned"
	case "in_progress":
		return "In progress"
	case "live":
		return "Live"
	case "declined":
		return "Declined"
	default:
		return ""
	}
}

func comparisonStance(stance string) string {
	switch stance {
	case "yes":
		return "Yes"
	case "no":
		return "No"
	case "partial":
		return "Partly"
	default:
		return ""
	}
}

func votesPhrase(n int) string {
	if n == 1 {
		return "1 vote."
	}
	return fmt.Sprintf("%d votes.", n)
}

func trimUTF8(s string, max int) string {
	if max < 0 {
		max = 0
	}
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i+1]
	}
	return cut
}
