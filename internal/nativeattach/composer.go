// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type keyAction int

const (
	keyNone keyAction = iota
	keyDetach
	keySubmit
	keyInterrupt
)

// Composer keeps text-field shortcuts separate from deliberate control actions.
// Modified Enter needs a terminal that sends CSI-u or modifyOtherKeys. Esc then
// s is the portable submit fallback; Enter inside the field inserts a newline.
type Composer struct {
	Text     string
	Focused  bool
	platform string
	pending  []byte
	paste    bool
}

func SubmitLabel(platform string) string {
	if platform == "darwin" {
		return "⌘↵"
	}
	return "Ctrl+↵"
}

func (c *Composer) Escape() keyAction {
	if c.Focused {
		c.Focused = false
		return keyNone
	}
	return keyDetach
}

func (c *Composer) add(text string) {
	if len(c.Text)+len(text) <= nativecontract.InputUTF8Bytes {
		c.Text += text
	}
}

func (c *Composer) feed(input []byte, control bool) []keyAction {
	var actions []keyAction
	for _, ch := range input {
		if len(c.pending) != 0 || ch == 0x1b {
			c.pending = append(c.pending, ch)
			if len(c.pending) == 1 {
				continue
			}
			if len(c.pending) == 2 && ch != '[' {
				c.pending = nil
				continue
			}
			if len(c.pending) > 32 {
				c.pending = nil
				continue
			}
			if len(c.pending) > 2 && (ch >= 0x40 && ch <= 0x7e) {
				seq := string(c.pending)
				c.pending = nil
				switch seq {
				case "\x1b[200~":
					c.paste = true
				case "\x1b[201~":
					c.paste = false
				case "\x1b[13;9u", "\x1b[27;9;13~":
					if c.platform == "darwin" && control && c.Focused && !c.paste {
						actions = append(actions, keySubmit)
					}
				case "\x1b[13;5u", "\x1b[27;5;13~":
					if c.platform != "darwin" && control && c.Focused && !c.paste {
						actions = append(actions, keySubmit)
					}
				}
			}
			continue
		}
		if c.paste {
			if c.Focused && (ch >= 0x20 || ch == '\n' || ch == '\r' || ch == '\t') {
				if ch == '\r' {
					ch = '\n'
				}
				c.add(string([]byte{ch}))
			}
			continue
		}
		if ch == 3 || ch == 4 {
			actions = append(actions, keyDetach)
			continue
		}
		if !c.Focused {
			switch ch {
			case 'q':
				actions = append(actions, keyDetach)
			case 'a', '\r', '\n':
				if control {
					c.Focused = true
				}
			case 's':
				if control {
					actions = append(actions, keySubmit)
				}
			case 'i':
				actions = append(actions, keyInterrupt)
			}
			continue
		}
		switch ch {
		case 0x7f, 8:
			_, n := utf8.DecodeLastRuneInString(c.Text)
			if n > 0 {
				c.Text = c.Text[:len(c.Text)-n]
			}
		case '\r', '\n':
			c.add("\n")
		default:
			if ch >= 0x20 {
				c.add(string([]byte{ch}))
			}
		}
	}
	return actions
}

func (c *Composer) flushEscape() keyAction {
	if string(c.pending) == "\x1b" {
		c.pending = nil
		if !c.paste {
			return c.Escape()
		}
	}
	c.pending = nil
	return keyNone
}

// Display text can never execute terminal controls, OSC clipboard/device
// requests or bidi formatting. Only our own renderer writes escape sequences.
func terminalText(text string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(text, "�") {
		if r == '\n' {
			b.WriteRune(r)
		} else if r == '\t' {
			b.WriteString("    ")
		} else if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
