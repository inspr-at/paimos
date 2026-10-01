// SPDX-License-Identifier: AGPL-3.0-only
package main

// These words distinguish the one-time computer pairing from the per-session
// approval. Language is an explicit CLI choice, never inferred from credentials.
type attachWords struct {
	Unlinked, Linked, PairingHelp, Next, NextManual, LocalCheck string
}

func attachWording(language string) attachWords {
	if language == "de" {
		return attachWords{
			Unlinked:    "Computer gekoppelt · Diese Sitzung ist noch nicht verknüpft.",
			Linked:      "Computer gekoppelt · Diese Sitzung ist verknüpft.",
			PairingHelp: "Die Kopplung verbindet den Computer mit Aeon. Verknüpfe jede laufende Sitzung separat mit ihrem Ticket.",
			Next:        "Als Nächstes: Freigabe im Browserfenster, das sich nach der lokalen Prüfung öffnet. Touch ID folgt, wenn erforderlich.",
			NextManual:  "Als Nächstes: Öffne nach der lokalen Prüfung den angezeigten Link und erteile die Freigabe in Aeon. Touch ID folgt, wenn erforderlich.",
			LocalCheck:  "Lokale Prüfung",
		}
	}
	return attachWords{
		Unlinked:    "Computer paired · This session not yet linked.",
		Linked:      "Computer paired · This session linked.",
		PairingHelp: "Pairing connects the computer to Aeon. Link each running session separately to its ticket.",
		Next:        "Next: approve in the browser window that opens after the local check. Touch ID follows when required.",
		NextManual:  "Next: open the printed link after the local check and approve in Aeon. Touch ID follows when required.",
		LocalCheck:  "Local check",
	}
}
