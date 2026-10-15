// SPDX-License-Identifier: AGPL-3.0-only
// Work at a glance (AEON-1358, design r4b §1 and §3.1–§3.2): the words of the header work bar and the column header
// figures, in English and German (impersonal). Placeholders in braces are filled with the view's own numbers.
// The app shows English until it is fully translated (displayLanguage); the German table is ready for that switch.
import type { DisplayLanguage } from './displayLanguage'

export interface WorkFiguresText {
  wholeProject: string; thisView: string; ofTickets: string; total: string; workByHours: string; about: string
  groups: { completed: string; doneNotLive: string; inProgress: string; open: string; done: string }
  withoutEstimate: string; allEstimated: string
  head: { hours: string; share: string; tickets: string }
  counting: string
  foot: { estimates: string; withoutEstimate: string; everyEstimated: string; closed: string; noClosed: string; hide: string; noReleases: string }
  failed: string; tooMany: string; slow: string; retry: string
  sortBy: string; shiftSort: string
  shown: string; inclHidden: string; allRows: string; leavesOnly: string; closedLeftOut: string; closedCount: string
  unassigned: string; withoutTokens: string; withoutModel: string; withoutCostUnit: string; withoutCost: string
  lines: {
    parents: string; leaves: string; keys: string
    running: string; nextFinish: string; lastFinish: string
    weighted: string; plain: string; readings: string
    high: string; medium: string; low: string; noPriority: string
    others: string; agent: string; person: string
    median: string; average: string; withoutEstimate: string; countAsZero: string
    measured: string; estimated: string; ticket: string; tickets: string
    range: string; today: string; medianAge: string; staleCreated: string; medianSinceUpdate: string; staleUpdated: string
    noEpic: string; noRelease: string; none: string; untagged: string; noCostUnit: string; noModel: string
  }
}

const EN: WorkFiguresText = {
  wholeProject: 'Whole project', thisView: 'This view', ofTickets: '{n} of {m} tickets', total: 'Total', workByHours: 'Work by hours', about: 'about',
  groups: { completed: 'Completed', doneNotLive: 'Done, not live', inProgress: 'In progress', open: 'Open', done: 'Done' },
  withoutEstimate: '{n} without estimate', allEstimated: 'All estimated',
  head: { hours: 'Hours', share: 'Share', tickets: 'Tickets' },
  counting: 'Counting…',
  foot: {
    estimates: 'Hours are agent estimates · leaves only', withoutEstimate: '{n} without estimate count as 0h', everyEstimated: 'Every ticket has an estimate',
    closed: '{n} closed not counted', noClosed: 'No closed tickets', hide: '{hide} doesn’t change these figures.',
    noReleases: 'This project doesn’t mark releases, so Done is its last step.',
  },
  failed: 'These figures could not be counted.', tooMany: 'Too many tickets to count here; narrow the view.', slow: 'Counting took too long.', retry: 'Retry',
  sortBy: 'Sort by {column}', shiftSort: 'Shift-click adds a secondary sort',
  shown: 'shown', inclHidden: 'incl. hidden rows', allRows: 'all rows', leavesOnly: 'leaves only', closedLeftOut: 'closed left out', closedCount: '{n} closed',
  unassigned: '{n} unassigned', withoutTokens: '{n} without tokens', withoutModel: '{n} without model', withoutCostUnit: '{n} without cost unit', withoutCost: '{n} without cost',
  lines: {
    parents: 'Parents', leaves: 'Leaves', keys: 'Keys',
    running: 'Running', nextFinish: 'Next finish', lastFinish: 'Last finish',
    weighted: 'Weighted by hours', plain: 'Plain average', readings: 'With a progress reading',
    high: 'High', medium: 'Medium', low: 'Low', noPriority: 'No priority',
    others: 'Others', agent: 'agent', person: 'person',
    median: 'Median', average: 'Average', withoutEstimate: 'Without estimate', countAsZero: 'count as 0h',
    measured: 'Measured', estimated: 'Estimated', ticket: 'ticket', tickets: 'tickets',
    range: 'Range', today: 'today', medianAge: 'Median age', staleCreated: 'Open, created 14d+ ago', medianSinceUpdate: 'Median since update', staleUpdated: 'Open, untouched 7d+',
    noEpic: 'No epic', noRelease: 'No release', none: 'None', untagged: 'Untagged', noCostUnit: 'No cost unit', noModel: 'No model',
  },
}

const DE: WorkFiguresText = {
  wholeProject: 'Ganzes Projekt', thisView: 'Diese Ansicht', ofTickets: '{n} von {m} Tickets', total: 'Gesamt', workByHours: 'Arbeit in Stunden', about: 'etwa',
  groups: { completed: 'Abgeschlossen', doneNotLive: 'Erledigt, nicht live', inProgress: 'In Arbeit', open: 'Offen', done: 'Erledigt' },
  withoutEstimate: '{n} ohne Schätzung', allEstimated: 'Alle geschätzt',
  head: { hours: 'Stunden', share: 'Anteil', tickets: 'Tickets' },
  counting: 'Wird gezählt…',
  foot: {
    estimates: 'Stunden sind Agenten-Schätzungen · nur Blätter', withoutEstimate: '{n} ohne Schätzung zählen als 0h', everyEstimated: 'Jedes Ticket hat eine Schätzung',
    closed: '{n} geschlossene nicht gezählt', noClosed: 'Keine geschlossenen Tickets', hide: '{hide} ändert diese Zahlen nicht.',
    noReleases: 'Dieses Projekt markiert keine Releases, daher ist Erledigt der letzte Schritt.',
  },
  failed: 'Diese Zahlen konnten nicht gezählt werden.', tooMany: 'Zu viele Tickets zum Zählen; bitte die Ansicht eingrenzen.', slow: 'Das Zählen hat zu lange gedauert.', retry: 'Erneut versuchen',
  sortBy: 'Nach {column} sortieren', shiftSort: 'Umschalt-Klick fügt eine zweite Sortierung hinzu',
  shown: 'angezeigt', inclHidden: 'inkl. ausgeblendeter Zeilen', allRows: 'alle Zeilen', leavesOnly: 'nur Blätter', closedLeftOut: 'geschlossene nicht gezählt', closedCount: '{n} geschlossen',
  unassigned: '{n} ohne Zuständige', withoutTokens: '{n} ohne Tokens', withoutModel: '{n} ohne Modell', withoutCostUnit: '{n} ohne Kostenstelle', withoutCost: '{n} ohne Kosten',
  lines: {
    parents: 'Eltern', leaves: 'Blätter', keys: 'Schlüssel',
    running: 'Laufend', nextFinish: 'Nächstes Ende', lastFinish: 'Letztes Ende',
    weighted: 'Nach Stunden gewichtet', plain: 'Einfacher Durchschnitt', readings: 'Mit Fortschrittsangabe',
    high: 'Hoch', medium: 'Mittel', low: 'Niedrig', noPriority: 'Keine Priorität',
    others: 'Andere', agent: 'Agent', person: 'Person',
    median: 'Median', average: 'Durchschnitt', withoutEstimate: 'Ohne Schätzung', countAsZero: 'zählen als 0h',
    measured: 'Gemessen', estimated: 'Geschätzt', ticket: 'Ticket', tickets: 'Tickets',
    range: 'Zeitraum', today: 'heute', medianAge: 'Medianes Alter', staleCreated: 'Offen, vor 14T+ erstellt', medianSinceUpdate: 'Median seit Änderung', staleUpdated: 'Offen, 7T+ unverändert',
    noEpic: 'Kein Epic', noRelease: 'Kein Release', none: 'Keine', untagged: 'Ohne Label', noCostUnit: 'Keine Kostenstelle', noModel: 'Kein Modell',
  },
}

export function workFiguresText(language: DisplayLanguage = 'en'): WorkFiguresText {
  return language === 'de' ? DE : EN
}

// Fills {name} placeholders; unknown names stay as they are.
export function fill(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (match, name: string) => name in values ? String(values[name]) : match)
}
