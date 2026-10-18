// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"fmt"
	"strings"
	"time"
)

// words follow the conversation's language; the conversation itself stays
// as sent. German labels are the ones START uses for the same sections.
type words struct {
	entryTitle, handedOver, summary, facts, openPoints, transcript, workBody string
	person, start                                                            string
}

var copies = map[string]words{
	"en": {
		entryTitle: "From the START conversation",
		handedOver: "Handed over from START on %s, %s.",
		summary:    "Summary", facts: "Facts", openPoints: "Open points",
		transcript: "The conversation is attached to the project as %s (%d messages).",
		workBody:   "Open point from the START conversation on %s.",
		person:     "Person", start: "START",
	},
	"de": {
		entryTitle: "Aus dem START-Gespräch",
		handedOver: "Aus START auf %s übergeben, %s.",
		summary:    "Aktuelle Einschätzung", facts: "Was wir konkret gesehen haben", openPoints: "Was noch offen ist",
		transcript: "Der Gesprächsverlauf hängt am Projekt: %s (%d Nachrichten).",
		workBody:   "Offener Punkt aus dem START-Gespräch auf %s.",
		person:     "Person", start: "START",
	},
}

func copyFor(language string) words {
	if w, ok := copies[language]; ok {
		return w
	}
	return copies["en"]
}

const stamp = "2006-01-02 15:04 UTC"

// markdownText renders conversation text as text: characters that could
// start Markdown structure, emphasis or a link are backslash-escaped, so a
// pasted [link](…) or heading stays literally what was said.
func markdownText(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		digits := indent
		for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
			digits++
		}
		for j, r := range line {
			lead := j == indent && strings.ContainsRune("#-+=", r)
			ordered := j == digits && digits > indent && (r == '.' || r == ')')
			if lead || ordered || strings.ContainsRune("\\`*_[]<>!|~&", r) {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func knowledgeBody(p Payload, handedOver time.Time) string {
	w := copyFor(p.Language)
	var b strings.Builder
	fmt.Fprintf(&b, w.handedOver, markdownText(p.Source.Host), handedOver.UTC().Format(stamp))
	section := func(title string, lines []string, list bool) {
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n\n## " + title + "\n\n")
		for i, line := range lines {
			if list {
				b.WriteString("- " + markdownText(line))
				if i < len(lines)-1 {
					b.WriteString("\n")
				}
				continue
			}
			b.WriteString(markdownText(line))
		}
	}
	if p.Summary != "" {
		section(w.summary, []string{p.Summary}, false)
	}
	section(w.facts, p.Facts, true)
	section(w.openPoints, p.OpenPoints, true)
	if len(p.Transcript) > 0 {
		b.WriteString("\n\n" + fmt.Sprintf(w.transcript, markdownText(transcriptName(p)), len(p.Transcript)))
	}
	return b.String()
}

func knowledgeMetadata(p Payload, handedOver time.Time) map[string]any {
	return map[string]any{
		"source":         map[string]any{"product": p.Source.Product, "host": p.Source.Host, "session_id": p.Source.SessionID},
		"language":       p.Language,
		"handed_over_at": handedOver.UTC().Format(time.RFC3339),
		"messages":       len(p.Transcript),
	}
}

func transcriptName(p Payload) string {
	return "start-conversation-" + strings.NewReplacer(":", "-", "[", "", "]", "").Replace(p.Source.Host) + ".txt"
}

// transcriptText is a plain-text file; it is served only as a download.
func transcriptText(p Payload) string {
	w := copyFor(p.Language)
	var b strings.Builder
	fmt.Fprintf(&b, "START · %s · %s\n", p.Source.Host, p.Source.SessionID)
	for _, turn := range p.Transcript {
		who := w.person
		if turn.Role == "assistant" {
			who = w.start
		}
		b.WriteString("\n— " + who)
		if turn.At != nil {
			b.WriteString(" · " + turn.At.UTC().Format(stamp))
		}
		b.WriteString("\n" + strings.ReplaceAll(turn.Text, "\r\n", "\n") + "\n")
	}
	return b.String()
}
