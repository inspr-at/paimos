// SPDX-License-Identifier: AGPL-3.0-only
package assistants

import (
	"regexp"
	"strings"
)

// Structured human_input is the primary action gate. Free-text channels reuse
// these same bounded recognizers. Mentioning credentials or requesting normal
// merge/deploy approval is allowed; arbitrary natural language is not classified.
var forbiddenHumanAsk = regexp.MustCompile(`(?i)(?:\b(?:(?:give|send|provide|paste|share|supply)[ \t]+(?:me|us)|(?:gib|sende|schick|teile)[ \t]+(?:mir|uns))\b[^\n.!?]{0,120}\b(?:secrets?|tokens?|api[ _-]?keys?|passwords?|passwörter|passwort|zugangsdaten|schlüssel)\b|\b(?:secrets?|tokens?|api[ _-]?keys?|passwords?|passwörter|passwort|zugangsdaten|schlüssel)\b[^\n.!?]{0,80}\b(?:from you|from the user|from the human|von dir|vom menschen)\b|\b(?:please|you must|you need to|can you|could you|bitte|du musst)\b[^\n.!?]{0,80}\b(?:manually|manual steps?|run this|run the command|execute this|manuell|ausführen)\b)`)

const HumanInputProhibited = "assistant_human_input_prohibited: Refused: agents must use existing authorized tools; asking a person for secrets, tokens or manual steps is prohibited."

// PolicyRefusal is a recorded denial. Transaction owners retain the receipt,
// commit without publishing the refused content, then return the channel error.
type PolicyRefusal struct{ Reason string }

func (r *PolicyRefusal) Error() string { return r.Reason }

// HumanInputRefusal is shared by actions, messages, Desk questions and routine
// results. Callers validate channel size bounds before passing text here.
func HumanInputRefusal(humanInput string, text ...string) string {
	if humanInput != "none" || forbiddenHumanAsk.MatchString(strings.Join(text, "\n")) {
		return HumanInputProhibited
	}
	return ""
}
