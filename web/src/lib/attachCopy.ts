// SPDX-License-Identifier: AGPL-3.0-only
import { displayLanguage, type DisplayLanguage } from './displayLanguage.ts'
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

export type TitlePart = { text: string; kind: 'text' | 'harness' | 'account' | 'computer' | 'workspace' }

// AEON-1502: the pairing review in one sentence. English and German pairs; the
// app picks the language through displayLanguage().
export function pairingCopy(language: DisplayLanguage, product: string) {
  const de = language === 'de'
  const list = (names: readonly string[]) => names.length < 2 ? names[0] ?? '' : `${names.slice(0, -1).join(', ')} ${de ? 'und' : 'and'} ${names.at(-1)}`
  const t = (text: string): TitlePart => ({ text, kind: 'text' })
  const joined = (names: readonly string[]): TitlePart[] => names.flatMap((name, i) => [
    ...(i === 0 ? [] : [t(i === names.length - 1 ? (de ? ' und ' : ' and ') : ', ')]),
    { text: name, kind: 'harness' as const },
  ])
  // German needs "can" in the plural for several harnesses.
  const can = (names: readonly string[], en: string, deOne: string, deMany: string) => de ? `${list(names)} ${names.length === 1 ? deOne : deMany}` : `${list(names)} ${en}`
  const reasons: Record<string, string> = {
    'Connecting this computer…': 'Dieser Computer wird verbunden…',
    'Denying this request…': 'Die Anfrage wird abgelehnt…',
    'Wait for the current action to finish.': 'Auf den Abschluss der laufenden Aktion warten.',
    'Only a signed-in person who can manage accounts can connect or deny this computer.': 'Nur eine angemeldete Person, die Konten verwalten darf, kann diesen Computer verbinden oder ablehnen.',
    'Choose at least one harness.': 'Mindestens einen Harness auswählen.',
    'The computer name must match the computer you opened.': 'Der Name muss zum geöffneten Computer passen.',
    'Use the matching code, or review this as a new computer.': 'Den passenden Code verwenden oder als neuen Computer prüfen.',
  }
  return {
    list,
    addTitle: (harnesses: readonly { name: string; account: string }[], computer: string): TitlePart[] => {
      const one = harnesses.length === 1 && harnesses[0]!.account
        ? [{ text: harnesses[0]!.name, kind: 'harness' as const }, t(' '), { text: `(${harnesses[0]!.account})`, kind: 'account' as const }]
        : joined(harnesses.map(item => item.name))
      const host: TitlePart = { text: computer, kind: 'computer' }
      return de ? [...one, t(' zu '), host, t(' hinzufügen?')] : [t('Add '), ...one, t(' to '), host, t('?')]
    },
    pairTitle: (computer: string, workspace: string): TitlePart[] => {
      const host: TitlePart = { text: computer, kind: 'computer' }, space: TitlePart = { text: workspace, kind: 'workspace' }
      return de ? [host, t(' mit '), space, t(' koppeln?')] : [t('Pair '), host, t(' with '), space, t('?')]
    },
    facts: (verify: boolean, agents: boolean) => de
      ? [verify ? 'Prüfung an' : 'Prüfung aus', agents ? 'Agents nach dem Verbinden aktiv' : 'Agents nach dem Verbinden pausiert', 'in Details änderbar'].join(' · ')
      : [verify ? 'Verification on' : 'Verification off', agents ? 'Agents enabled after connecting' : 'Agents paused after connecting', 'change in Details'].join(' · '),
    role: de ? 'Admins können auf diesem Computer Läufe starten' : 'Admins can start runs on this computer',
    add: de ? 'Hinzufügen' : 'Add',
    adding: de ? 'Wird hinzugefügt…' : 'Adding…',
    pair: de ? 'Koppeln' : 'Pair',
    pairing: de ? 'Wird gekoppelt…' : 'Pairing…',
    deny: de ? 'Ablehnen' : 'Deny',
    otherCode: de ? 'Anderen Code verwenden' : 'Use a different code',
    newComputer: de ? 'Als neuen Computer prüfen' : 'Review as a new computer',
    notSignedIn: de ? 'Nicht angemeldet' : 'Not signed in',
    accounts: (count: number) => de ? `${count} Konten` : `${count} accounts`,
    details: 'Details',
    workspace: de ? 'Arbeitsbereich' : 'Workspace',
    folder: de ? 'Ordner' : 'Folder',
    account: (harness: string) => de ? `${harness}-Konto` : `${harness} account`,
    accountFor: (harness: string) => de ? `Konto für ${harness}` : `Account for ${harness}`,
    connectHarness: (harness: string) => de ? `${harness} verbinden` : `Connect ${harness}`,
    connectAccount: (label: string) => de ? `Konto ${label} verbinden` : `Connect account ${label}`,
    leaveOut: de ? 'Weglassen' : 'Leave out',
    verification: de ? 'Prüfung' : 'Verification',
    verifyLabel: de ? 'Ausgewählte Harnesses prüfen' : 'Verify selected harnesses',
    notSaid: de ? `Dieses ${product} hat nicht angegeben, welche Harnesses geprüft werden können.` : `This ${product} has not said which harnesses can be verified.`,
    unavailable: de ? 'Prüfung ist nicht verfügbar.' : 'Verification is unavailable.',
    canVerify: (names: readonly string[]) => can(names, 'can be verified.', 'kann geprüft werden.', 'können geprüft werden.'),
    cannotVerify: (names: readonly string[]) => can(names, 'can’t be verified.', 'kann nicht geprüft werden.', 'können nicht geprüft werden.'),
    willVerify: (names: readonly string[]) => can(names, 'will be verified.', 'wird geprüft.', 'werden geprüft.'),
    // After Add or Pair with harnesses that can't be verified: the choice is in Details.
    choice: (names: readonly string[]) => de
      ? `${can(names, '', 'kann nicht geprüft werden', 'können nicht geprüft werden')}. In Details die Prüfung ausschalten oder ${names.length === 1 ? 'den Harness' : 'sie'} weglassen.`
      : `${list(names)} can’t be verified. Turn verification off, or leave ${names.length === 1 ? 'it' : 'them'} out, in Details.`,
    limits: (terms: { policy?: string; allowance: number; unit: string; runs_per_account: number; max_parallel_runs: number; max_duration_seconds: number }, until: string) => {
      const allowance = terms.unit === 'requests'
        ? de ? `${terms.allowance} ${terms.allowance === 1 ? 'Anfrage' : 'Anfragen'} pro Harness` : `${terms.allowance} ${terms.allowance === 1 ? 'request' : 'requests'} per harness`
        : de ? `${terms.allowance} ${terms.unit} pro Harness` : `${terms.allowance} ${terms.unit} per harness`
      const runs = terms.runs_per_account === 1 && terms.max_parallel_runs === 1
        ? de ? '1 Lauf gleichzeitig' : '1 run at a time'
        : de ? `${terms.runs_per_account} Läufe, höchstens ${terms.max_parallel_runs} gleichzeitig` : `${terms.runs_per_account} runs, at most ${terms.max_parallel_runs} at a time`
      return [
        [terms.policy === 'read_only' ? (de ? 'Nur lesend' : 'Read-only') : '', allowance, runs, de ? `max. ${terms.max_duration_seconds} Sekunden` : `${terms.max_duration_seconds} seconds max`, de ? `bis ${until}` : `until ${until}`].filter(Boolean).join(' · '),
        de ? 'Keine Änderungen am Repository und keine privilegierten Aktionen' : 'No repository changes or privileged actions',
        de ? `${product}-Kontingent, getrennt vom Anbieter-Abo` : `${product} allowance, separate from the vendor subscription`,
      ]
    },
    limitsDiffer: de
      ? 'Diese Prüfgrenzen weichen von 1 Anfrage pro Harness, 1 Lauf gleichzeitig und höchstens 60 Sekunden ab. Vor dem Verbinden die Werte des Servers prüfen.'
      : 'These verification limits differ from 1 request per selected harness, 1 run at a time on that harness, and a 60 second maximum. Review the server values before connecting.',
    noTerms: de
      ? 'Der Server hat keine Prüfbedingungen mitgeschickt. Prüfung ausgeschaltet lassen oder den Code erneut nachschlagen.'
      : 'The server did not include verification terms. Leave verification off, or look the code up again.',
    after: de ? 'Nach dem Verbinden' : 'After connecting',
    allowAgents: de ? 'Agents dürfen diese Konten nutzen' : 'Let agents use these accounts',
    keepPaused: de ? 'Agents pausiert lassen' : 'Keep agents paused',
    footer: de
      ? 'Anmeldungen bei Anbietern und Projektdateien bleiben auf dem Computer; Verbindungsgeheimnisse bleiben aus dem Browser. Das Koppeln gibt keine Berechtigung zum Starten von Läufen; das entscheiden die Rollen pro Computer.'
      : 'Vendor sign-ins and project files stay on the computer; connection secrets stay out of the browser. Pairing grants no permission to start runs; roles on each computer decide.',
    where: de ? 'Wo die Konten arbeiten dürfen' : 'Where accounts may work',
    whereNowhere: de ? 'Noch nirgends · vor der ersten Nutzung in Einstellungen › Konten erlauben' : 'Nowhere yet · allow in Settings › Accounts before first use',
    // Only the first page of contexts was read when `partial`: the count is a lower bound.
    whereSome: (names: readonly string[], more: number, partial: boolean) => {
      const rest = more > 0 ? `${more}${partial ? '+' : ''} ${de ? 'weitere' : 'more'}` : partial ? (de ? 'weitere' : 'more') : ''
      const shown = rest ? [...names, rest] : names
      return de ? `${list(shown)} · in Einstellungen › Konten änderbar` : `${list(shown)} · change in Settings › Accounts`
    },
    whereRule: de ? 'Die Regel des Arbeitsbereichs entscheidet · Einstellungen › Konten' : 'The workspace rule decides · Settings › Accounts',
    whereFailed: de ? 'Regel nicht lesbar; sie gilt trotzdem · nach dem Verbinden in Einstellungen › Konten prüfen' : 'The workspace rule could not be read; it still applies · check Settings › Accounts after connecting',
    whereReading: de ? 'Regel des Arbeitsbereichs wird gelesen…' : 'Reading the workspace rule…',
    expiredAdd: (harnesses: readonly string[], computer: string) => de ? `Die Anfrage, ${list(harnesses)} zu ${computer} hinzuzufügen, ist abgelaufen.` : `The request to add ${list(harnesses)} to ${computer} expired.`,
    expiredPair: (computer: string, workspace: string) => de ? `Die Anfrage, ${computer} mit ${workspace} zu koppeln, ist abgelaufen.` : `The request to pair ${computer} with ${workspace} expired.`,
    deniedAdd: (harnesses: readonly string[], computer: string) => de ? `Das Hinzufügen von ${list(harnesses)} zu ${computer} wurde abgelehnt.` : `Adding ${list(harnesses)} to ${computer} was denied.`,
    deniedPair: (computer: string, workspace: string) => de ? `Das Koppeln von ${computer} mit ${workspace} wurde abgelehnt.` : `Pairing ${computer} with ${workspace} was denied.`,
    // The command sits between the two halves, set in mono.
    runAgain: (computer: string) => de ? { before: 'Zum erneuten Anfragen ', after: ` auf ${computer} ausführen.` } : { before: 'Run ', after: ` on ${computer} to ask again.` },
    newRequest: de ? 'Im Terminal dieses Computers eine neue Anfrage starten.' : 'Start a new request in the terminal on that computer.',
    reason: (text: string) => de ? reasons[text] ?? text : text,
  }
}

/** Today's add-harness command (agentPairing's harness fix); several harnesses ask agentd to discover them again. */
export function addHarnessCommand(harnesses: readonly string[]): string {
  return harnesses.length === 1 ? `aeon-agentd add-harness --harness ${harnesses[0]}` : 'aeon-agentd add-harness'
}
