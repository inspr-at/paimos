// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"fmt"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/attachwatch"
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
	if hint := attachGermanRefusalHints[detail.Code]; hint != "" {
		return &agentd.AttachLocalError{Code: detail.Code, Hint: hint}
	}
	return err
}

var attachGermanRefusalHints = map[string]string{
	"attach_version_mismatch":       "Aeon und agentd verwenden inkompatible Versionen für die Verknüpfung. Aktualisiere Aeon und paimos-agentd, starte agentd neu und versuche es erneut; die Kopplungsschlüssel bleiben gültig.",
	"attach_pairing_revoked":        "Die Kopplung dieses Computers wird nicht mehr akzeptiert. Prüfe die gekoppelten Computer in Aeon; kopple den Computer bei widerrufener Kopplung erneut und versuche es noch einmal.",
	"attach_ticket_not_visible":     "Das Ticket oder sein Projekt ist für den Eigentümer der Kopplung nicht verfügbar. Prüfe die Projektzuordnung und den Projektzugriff und versuche es erneut.",
	"attach_code_expired":           "Der Verknüpfungscode ist vor der Aktivierung abgelaufen. Starte attach erneut und genehmige den neuen Code in Aeon.",
	"attach_live_limit":             "Zu viele Verknüpfungen warten in Aeon auf Freigabe. Genehmige eine, lehne sie ab oder warte auf ihren Ablauf und versuche es erneut.",
	"attach_draining":               "Computer oder Harness werden getrennt. Bei einem Harness-Drain kann aeon-agentd add-harness bereits während der alten Trennung verwendet werden. Bei einer Computer-Trennung warte auf den Abschluss laufender Arbeit und kopple den Computer erneut. Starte attach mit neuer Freigabe erneut.",
	"attach_enrollment_unavailable": "Für diesen Harness besteht keine verbundene Registrierung. Prüfe die Registrierungen in Aeon, füge den Harness bei Bedarf mit aeon-agentd add-harness erneut hinzu und starte attach erneut.",
	"attach_poll_key_unknown":       "agentd registriert sich beim nächsten attach-Versuch automatisch erneut. Starte attach in wenigen Sekunden erneut und genehmige den neuen Code in Aeon; ein Daemon-Neustart ist nicht erforderlich.",
	"attach_registration_lost":      "Aeon akzeptiert die Verknüpfungsregistrierung dieses Daemons nicht mehr, etwa nach einem Serverneustart. Starte agentd neu, sobald laufende Arbeit es erlaubt, und genehmige einen neuen attach-Versuch. Erneutes Koppeln ist nicht erforderlich.",
	"attach_pairing_unavailable":    "Computer oder Berechtigungen sind nicht verfügbar. Prüfe Computer, Registrierungen, Berechtigungen des Laufzeitschlüssels und Kontoberechtigungen in Aeon; starte danach agentd und attach erneut.",
	"attach_scope_changed":          "Projekt, Ticket oder Harness-Registrierung wurden geändert. Prüfe Projektzuordnung, Projektzugriff und verbundene Harness-Registrierungen in Aeon und starte attach erneut.",
	"attach_computer_limit":         fmt.Sprintf("Dieser Computer hat bereits %d offene oder aktive Verknüpfungen. Beende einen attach-Helfer, lehne eine offene Anfrage in Aeon ab oder warte auf ihren Ablauf und starte attach erneut.", attachwatch.ComputerMax),
	"attach_attempt_limit":          fmt.Sprintf("Zu viele Verknüpfungsversuche haben Aeon erreicht. Warte %g Minuten und starte attach erneut.", attachwatch.AttemptWindow.Minutes()),
	"attach_registration_limit":     "Aeons Kapazität für Daemon-Registrierungen ist erschöpft. Bitte den Administrator, die Kapazität zu prüfen; starte danach agentd und attach erneut.",
	"attach_poll_limit":             "Verknüpfungsabfragen waren zu schnell oder nicht in Reihenfolge. Aktualisiere paimos-agentd, starte ihn neu, sobald laufende Arbeit es erlaubt, und starte attach erneut.",
	"attach_rate_limit":             "Aeon begrenzt Verknüpfungsanfragen. Warte vor dem nächsten Versuch; bitte bei weiteren Ablehnungen den Administrator, die Begrenzungen zu prüfen.",
	"attach_snapshot_changed":       "Prozess oder Freigabezuordnung wurden geändert. Prüfe Prozess, freigegebenen Ordner und attach-Argumente und starte attach mit neuer Freigabe erneut.",
	"attach_consent_required":       "Lokale Bestätigung oder Freigabezuordnung wurden abgelehnt. Aktualisiere Aeon und paimos-agentd und starte agentd neu, sobald laufende Arbeit es erlaubt. Starte attach erneut und bestätige im Browser sowie bei Aufforderung mit Touch ID.",
	"attach_ended":                  "Die Verknüpfung wurde beendet oder bereits verwendet. Starte attach erneut und genehmige die neue Anfrage in Aeon.",
	"attach_invalid_request":        "Aeon hat das Anfrageformat abgelehnt. Prüfe attach-Argumente und freigegebenen Ordner; aktualisiere bei Bedarf Aeon und paimos-agentd und starte attach erneut.",
	"attach_local_unavailable":      "Der lokale attach-Start konnte nicht abgeschlossen werden. Prüfe aeon-agentd status und das agentd-Protokoll; starte agentd neu, sobald laufende Arbeit es erlaubt, und starte attach erneut.",
	"attach_pairing_mismatch":       "Instanzadresse oder Arbeitsordner stimmen nicht mit der genehmigten Kopplung überein. Prüfe aeon-agentd status und das agentd-Protokoll; verwende die ursprünglich genehmigte Konfiguration oder kopple den Computer für die gewünschte Instanz und den Arbeitsordner erneut. Starte danach agentd und attach erneut.",
	"attach_offline":                "Der Austausch mit Aeon konnte nicht abgeschlossen werden. Prüfe die Verbindung und aeon-agentd status und starte attach erneut. War agentd beim Start offline, starte ihn neu, sobald laufende Arbeit es erlaubt.",
	"attach_server_unavailable":     "Aeon konnte die Anfrage nicht verarbeiten. Prüfe die Erreichbarkeit und bitte bei anhaltenden Problemen den Administrator, die Serverprotokolle zu prüfen; starte danach attach erneut.",
	"attach_unknown":                "Aeon hat die Verknüpfung aus unbekanntem Grund abgelehnt. Prüfe aeon-agentd status und bitte den Administrator, die Anfrage in den Serverprotokollen zu prüfen; starte danach attach erneut.",
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
