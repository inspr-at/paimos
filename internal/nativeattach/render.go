// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type tailRow struct{ key, text string }
type tail struct {
	rows  []tailRow
	bytes int
}

func (t *tail) put(key, text string, appendText bool) string {
	text = terminalText(text)
	if len(text) > nativecontract.ExportedFrameBytes {
		text = text[:nativecontract.ExportedFrameBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	if key != "" {
		for i := range t.rows {
			if t.rows[i].key == key {
				t.bytes -= len(t.rows[i].text)
				if appendText {
					text = t.rows[i].text + text
					if len(text) > nativecontract.ExportedFrameBytes {
						text = "[earlier message text omitted]\n" + text[len(text)-nativecontract.ExportedFrameBytes/2:]
					}
				}
				t.rows[i].text = text
				t.bytes += len(text)
				t.trim()
				return text
			}
		}
	}
	t.rows = append(t.rows, tailRow{key, text})
	t.bytes += len(text)
	t.trim()
	return text
}

func (t *tail) trim() {
	for len(t.rows) > nativecontract.LiveReplayFrames || t.bytes > nativecontract.LiveReplayBytes {
		t.bytes -= len(t.rows[0].text)
		t.rows[0] = tailRow{}
		t.rows = t.rows[1:]
	}
}

func (t *tail) event(e nativecontract.StreamEvent) string {
	var text, key string
	appendText := false
	switch e.Kind {
	case "assistant.delta":
		var p nativecontract.AssistantDelta
		_ = json.Unmarshal(e.Payload, &p)
		key, text, appendText = p.MessageID, p.Text, true
	case "assistant.final":
		var p nativecontract.AssistantFinal
		_ = json.Unmarshal(e.Payload, &p)
		key, text = p.MessageID, "[final] "+p.Text
	case "structured":
		var p nativecontract.StructuredFrame
		_ = json.Unmarshal(e.Payload, &p)
		text = "[" + p.Kind + "]"
		if p.Text != nil {
			text += " " + *p.Text
		}
	case "receipt":
		var p nativecontract.Receipt
		_ = json.Unmarshal(e.Payload, &p)
		text = receiptText(p)
	case "input.delivered":
		var p nativecontract.InputDelivered
		_ = json.Unmarshal(e.Payload, &p)
		text = "[input " + p.InputID + ": delivered, " + p.Level + "] " + p.Text
	case "tool.start":
		var p nativecontract.ToolStart
		_ = json.Unmarshal(e.Payload, &p)
		text = "[tool " + p.CallID + ": started] " + p.Name
	case "tool.result":
		var p nativecontract.ToolResult
		_ = json.Unmarshal(e.Payload, &p)
		text = "[tool " + p.CallID + ": result]"
		if p.ResultSummary != nil {
			text += " " + *p.ResultSummary
		}
	case "gap":
		text = "[live stream gap; attach again to reload authorized history]"
	case "end":
		var p nativecontract.End
		_ = json.Unmarshal(e.Payload, &p)
		text = "[stream ended: " + p.Reason + "]"
	case "snapshot":
		var p nativecontract.SessionView
		_ = json.Unmarshal(e.Payload, &p)
		text = "[session " + p.State + ", " + p.Mode + "]"
	case "input.not_confirmed", "input.generation_changed", "input.session_ended", "input.refused":
		var p nativecontract.InputFailure
		_ = json.Unmarshal(e.Payload, &p)
		text = "[input " + p.InputID + ": " + e.Kind + "; retryable=" + fmt.Sprint(p.Retryable) + "; no automatic retry]"
	default:
		text = "[" + e.Kind + "]"
	}
	t.put(key, text, appendText)
	return terminalText(text)
}

func receiptText(r nativecontract.Receipt) string {
	id := r.RequestID
	if r.InputID != nil {
		id = *r.InputID
	}
	text := "[" + r.Operation + " " + id + ": " + r.State + "]"
	if r.Reason != nil {
		text += " " + string(*r.Reason)
	}
	return text
}

func draw(t Terminal, s *Session, composer *Composer, tail *tail, feedback string) error {
	if !t.Interactive() {
		return nil
	}
	cols, rows, err := t.Size()
	if err != nil {
		return err
	}
	if cols < 1 || rows < 1 {
		return fmt.Errorf("invalid terminal size")
	}
	// A hostile or broken size never causes a proportional allocation.
	if cols > 512 {
		cols = 512
	}
	if rows > 200 {
		rows = 200
	}
	mode := "read-only"
	if s.control && !s.authorityLost {
		mode = "controller"
	}
	lines := []string{
		"session " + s.binding.OwnedSessionID + " · " + mode,
		"a input · " + SubmitLabel(composer.platform) + " submit (or Esc, s) · i interrupt · Esc/q detach",
		"input: " + strings.ReplaceAll(terminalText(composer.Text), "\n", " ↵ "),
		terminalText(feedback),
	}
	var content []string
	for _, row := range tail.rows {
		for _, line := range strings.Split(row.text, "\n") {
			// Limit wrapping to the visible screen rather than allocating an
			// entire untrusted transcript into lines on each keypress.
			for line != "" {
				segment, rest := splitColumns(line, cols)
				content = append(content, segment)
				if len(content) > rows {
					content = content[1:]
				}
				line = rest
			}
		}
	}
	available := rows - len(lines)
	if available < 0 {
		lines = lines[:rows]
		available = 0
	}
	if len(content) > available {
		content = content[len(content)-available:]
	}
	lines = append(lines, content...)
	var out strings.Builder
	out.WriteString("\x1b[H\x1b[2J")
	for i, line := range lines {
		if i != 0 {
			out.WriteString("\r\n")
		}
		visible, _ := splitColumns(line, cols)
		out.WriteString(visible)
	}
	_, err = fmt.Fprint(t, out.String())
	return err
}

func splitColumns(text string, cols int) (string, string) {
	n := 0
	for i := range text {
		if n == cols {
			return text[:i], text[i:]
		}
		n++
	}
	return text, ""
}
