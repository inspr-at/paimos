// SPDX-License-Identifier: AGPL-3.0-only
import { displayLanguage } from './displayLanguage.ts'
// Pairing belongs to a computer; linking belongs to one running session.
// Copy follows the app language, independently of browser region preferences.
export function attachCopy(product: string, locale = typeof navigator === 'undefined' ? 'en' : navigator.language) {
  return displayLanguage(locale) === 'de' ? {
    computerPaired: 'Computer gekoppelt',
    sessionUnlinked: 'Diese Sitzung ist noch nicht verknüpft',
    sessionLinked: 'Diese Sitzung ist verknüpft',
    pairingHelp: `Die Kopplung verbindet den Computer mit ${product}. Verknüpfe jede laufende Sitzung separat mit ihrem Ticket.`,
    linkHelp: 'Die Kopplung deines Computers verknüpft noch keine Sitzung. Prüfe diese Sitzung und erlaube ihre Verknüpfung mit dem Ticket.',
    codeHelp: 'Starte aeon-agentd attach auf deinem gekoppelten Computer. Nach der lokalen Prüfung öffnet sich die Freigabe im Browser. Gib den Code hier ein, falls sich kein Fenster öffnet.',
    attachSession: 'Sitzung verknüpfen',
    attachTitle: 'Eine laufende Sitzung verknüpfen',
    watchTitle: 'Eine laufende Sitzung mitverfolgen',
    codeLabel: 'Verknüpfungscode',
    review: 'Sitzung prüfen',
    checking: 'Wird geprüft…',
    invalidCode: 'Gib den neunstelligen Code aus deinem Terminal ein.',
  } : {
    computerPaired: 'Computer paired',
    sessionUnlinked: 'This session not yet linked',
    sessionLinked: 'This session linked',
    pairingHelp: `Pairing connects the computer to ${product}. Link each running session separately to its ticket.`,
    linkHelp: 'Pairing your computer does not link a session. Review this session and allow it to link to the ticket.',
    codeHelp: 'Run aeon-agentd attach on your paired computer. After the local check, approval opens in your browser. Enter the code here if no window opens.',
    attachSession: 'Attach session',
    attachTitle: 'Attach a running session',
    watchTitle: 'Watch a running session',
    codeLabel: 'Attach code',
    review: 'Review session',
    checking: 'Checking…',
    invalidCode: 'Enter the nine-digit code from your terminal.',
  }
}
