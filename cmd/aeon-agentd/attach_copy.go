// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"

	"github.com/inspr-at/paimos/internal/agentd"
)

// These words distinguish the one-time computer pairing from the per-session
// approval. Language is an explicit CLI choice, never inferred from credentials.
type attachWords struct {
	Unlinked, Linked, PairingHelp, Next, NextManual, LocalCheck string
}

func attachLocalizedFailure(err error, language string) error {
	var detail *agentd.AttachLocalError
	if language != "de" || !errors.As(err, &detail) {
		return err
	}
	hints := map[string]string{
		"attach_version_mismatch":   "Aeon und agentd verwenden inkompatible Versionen für die Verknüpfung. Aktualisiere Aeon und paimos-agentd, starte agentd neu und versuche es erneut; die Kopplungsschlüssel bleiben gültig.",
		"attach_pairing_revoked":    "Die Kopplung dieses Computers wird nicht mehr akzeptiert. Prüfe die gekoppelten Computer in Aeon; kopple den Computer bei widerrufener Kopplung erneut und versuche es noch einmal.",
		"attach_ticket_not_visible": "Das Ticket oder sein Projekt ist für den Eigentümer der Kopplung nicht verfügbar. Prüfe die Projektzuordnung und den Projektzugriff und versuche es erneut.",
		"attach_code_expired":       "Der Verknüpfungscode ist vor der Aktivierung abgelaufen. Starte attach erneut und genehmige den neuen Code in Aeon.",
		"attach_live_limit":         "Zu viele Verknüpfungen warten in Aeon auf Freigabe. Genehmige eine, lehne sie ab oder warte auf ihren Ablauf und versuche es erneut.",
	}
	if hint := hints[detail.Code]; hint != "" {
		return &agentd.AttachLocalError{Code: detail.Code, Hint: hint}
	}
	return err
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
