// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Numbers › Building it (AEON-1493): the words of the approved design, English and German,
// impersonal. Placeholders in braces are filled with the window's own figures from the server.
import type { DeliveryLanguage } from './delivery'

export type BuildingKey = 'first_try' | 'rounds' | 'idle' | 'lead'
export type RoundOutcome = 'passed' | 'changes' | 'failed' | 'merge_forward' | 'none'

// In lineA and pp, '|' separates plain and bold runs: plain, bold, plain, bold …
interface MetricWords {
  name: string; learn: string; wants: string; say: string; sayOne?: string; target: string
  lineA: string; lineB: string; lineBOne?: string; pp: string; worst: string; worstOne?: string; unit: string; chartUnit: string
}

const EN = {
  title: 'Building it', sub: 'From the first build to a change that passes its checks: rounds, waits and how often it works the first time.',
  noData: 'No rounds recorded in this window yet.', err: 'Not loaded.', retry: 'Retry', onTarget: '{k} of {t} on target',
  cap: 'Building it · last {w} days · change against the {w} days before',
  more: 'Worst 5', open: 'Worst in this window: {n}', foot: 'From the event log', src: 'Event log: rounds, gate verdicts, merges',
  wpTitle: 'Worst in this window', wpSub: '{n} · last {w} days', close: 'Close', filters: 'Filters apply: type, priority, worked by',
  wpEmpty: 'No item in this window needed more.', moreRounds: '+{n} more', roundsWord: 'Rounds',
  kinds: { passed: 'passed', changes: 'changes', failed: 'failed', merge_forward: 'merge-forward', none: 'no verdict recorded' } as Record<RoundOutcome, string>,
  per: { day: 'Per day', week: 'Per week', month: 'Per month' },
  watched: 'watched', noTarget: 'No target yet', nothing: 'nothing recorded', noDataYet: 'no data yet', pts: 'pts', mean: 'mean',
  items: ['item', 'items'] as readonly [string, string], waits: ['wait', 'waits'] as readonly [string, string],
  metrics: {
    first_try: {
      name: 'First-try rate', unit: '%', chartUnit: '%',
      learn: 'Share of items merged after exactly one build round: no fix round and no merge-forward. Counted for every item merged in this window.',
      wants: 'Wanted: at least {t} of items merged after one round.', say: '{k} of {n} items passed on the first try.', sayOne: '{k} of 1 item passed on the first try.',
      target: 'Target ≥ {t}', lineA: '|{k} of {n}| merged after one round', lineB: '{n} items · {m} needed more rounds', lineBOne: '1 item · {m} needed more rounds',
      pp: '|{m} of {n}| needed more rounds', worst: '{v} rounds', worstOne: '1 round',
    },
    rounds: {
      name: 'Rounds per item', unit: 'rounds', chartUnit: 'rounds',
      learn: 'Every round until merged: the first build, each fix round after a gate verdict, and each merge-forward round that brings in the newest main. p50 {p50}: half of the items needed this many or fewer.',
      wants: 'Wanted: {t} rounds or fewer on the typical item.', say: 'Half the items needed {v} rounds or more.', sayOne: 'At least half the items merged after one round.',
      target: 'Target ≤ {t} rounds', lineA: 'p50 |{p50}| · mean |{mean}', lineB: '{n} items · {c} rounds', lineBOne: '1 item · {c} rounds',
      pp: 'p50 |{p50}| · mean |{mean}| · p90 |{p90}', worst: '{v} rounds', worstOne: '1 round',
    },
    idle: {
      name: 'Idle between rounds', unit: 'min', chartUnit: 'min',
      learn: 'From the end of a round (the gate verdict, or the builder reporting done) to the next step: the next dispatch, the merge enqueue or the merge. p50: half of the waits were shorter.',
      wants: 'Wanted: the next step within {t} on the typical item.', say: 'After a round, the next step waited {v} on the typical item.',
      target: 'Target p50 under {t}', lineA: 'p50 |{p50}| · p90 |{p90}', lineB: '{n} waits in {i} items', lineBOne: '{n} waits in 1 item',
      pp: 'p50 |{p50}| · p90 |{p90}', worst: 'idle {v}',
    },
    lead: {
      name: 'Lead time per item', unit: 'h', chartUnit: 'h',
      learn: 'From the start of the first build to the merge, in clock time: every round, every wait and the merge queue included. No target yet: watched.',
      wants: 'No target yet: watched.', say: 'From the first build to merged took about {v} on the typical item.',
      target: 'No target yet: watched', lineA: 'p50 |{p50}| · p90 |{p90}', lineB: '{n} items', lineBOne: '1 item',
      pp: 'p50 |{p50}| · p90 |{p90}', worst: 'lead {v}',
    },
  } as Record<BuildingKey, MetricWords>,
  readout: { first: '{l} · {v} · {k} of {n} {items}', rounds: '{l} · p50 {p50} · mean {mean} · {n} {items}', idle: '{l} · p50 {p50} · p90 {p90} · {n} {waits}', lead: '{l} · p50 {p50} · p90 {p90} · {n} {items}' },
  chartTarget: { first_try: 'Target ≥ {t}', rounds: 'Target ≤ {t}', idle: 'Target < {t}' } as Partial<Record<BuildingKey, string>>,
  sparkTarget: 'target {t}',
}

export type BuildingText = typeof EN

const DE: BuildingText = {
  title: 'Bauen', sub: 'Vom ersten Build bis zur Änderung, die ihre Checks besteht: Runden, Wartezeiten und wie oft es beim ersten Mal klappt.',
  noData: 'In diesem Zeitraum noch keine Runden erfasst.', err: 'Nicht geladen.', retry: 'Erneut versuchen', onTarget: '{k} von {t} im Ziel',
  cap: 'Bauen · letzte {w} Tage · Änderung gegenüber den {w} Tagen davor',
  more: 'Schlechteste 5', open: 'Am schlechtesten in diesem Zeitraum: {n}', foot: 'Aus dem Ereignisprotokoll', src: 'Ereignisprotokoll: Runden, Prüfurteile, Merges',
  wpTitle: 'Am schlechtesten in diesem Zeitraum', wpSub: '{n} · letzte {w} Tage', close: 'Schließen', filters: 'Filter gelten: Typ, Priorität, Bearbeitet von',
  wpEmpty: 'Keine Änderung in diesem Zeitraum brauchte mehr.', moreRounds: '+{n} weitere', roundsWord: 'Runden',
  kinds: { passed: 'bestanden', changes: 'Änderungen verlangt', failed: 'gescheitert', merge_forward: 'main nachgezogen', none: 'kein Urteil erfasst' },
  per: { day: 'Pro Tag', week: 'Pro Woche', month: 'Pro Monat' },
  watched: 'beobachtet', noTarget: 'Noch kein Ziel', nothing: 'nichts erfasst', noDataYet: 'noch keine Daten', pts: 'Pp.', mean: 'Mittel',
  items: ['Änderung', 'Änderungen'], waits: ['Wartezeit', 'Wartezeiten'],
  metrics: {
    first_try: {
      name: 'Erfolg im ersten Anlauf', unit: '%', chartUnit: '%',
      learn: 'Anteil der Änderungen, die nach genau einer Build-Runde gemergt wurden: keine Fix-Runde und kein Nachziehen von main. Gezählt für jede Änderung, die in diesem Zeitraum gemergt wurde.',
      wants: 'Gewollt: mindestens {t} der Änderungen nach einer Runde gemergt.', say: '{k} von {n} Änderungen klappten beim ersten Versuch.', sayOne: '{k} von 1 Änderung klappte beim ersten Versuch.',
      target: 'Ziel ≥ {t}', lineA: '|{k} von {n}| nach einer Runde gemergt', lineB: '{n} Änderungen · {m} brauchten mehr Runden', lineBOne: '1 Änderung · {m} brauchte mehr Runden',
      pp: '|{m} von {n}| brauchten mehr Runden', worst: '{v} Runden', worstOne: '1 Runde',
    },
    rounds: {
      name: 'Runden pro Änderung', unit: 'Runden', chartUnit: 'Runden',
      learn: 'Jede Runde bis zum Merge: der erste Build, jede Fix-Runde nach einem Prüfurteil und jede Runde, die den neuesten main nachzieht. p50 {p50}: Die Hälfte der Änderungen brauchte so viele oder weniger.',
      wants: 'Gewollt: {t} Runden oder weniger bei der typischen Änderung.', say: 'Die Hälfte der Änderungen brauchte {v} Runden oder mehr.', sayOne: 'Mindestens die Hälfte der Änderungen wurde nach einer Runde gemergt.',
      target: 'Ziel ≤ {t} Runden', lineA: 'p50 |{p50}| · Mittel |{mean}', lineB: '{n} Änderungen · {c} Runden', lineBOne: '1 Änderung · {c} Runden',
      pp: 'p50 |{p50}| · Mittel |{mean}| · p90 |{p90}', worst: '{v} Runden', worstOne: '1 Runde',
    },
    idle: {
      name: 'Leerlauf zwischen Runden', unit: 'min', chartUnit: 'min',
      learn: 'Vom Ende einer Runde (dem Prüfurteil oder der Fertigmeldung des Builders) bis zum nächsten Schritt: der nächste Auftrag, das Einreihen zum Merge oder der Merge. p50: Die Hälfte der Wartezeiten war kürzer.',
      wants: 'Gewollt: der nächste Schritt innerhalb von {t} bei der typischen Änderung.', say: 'Nach einer Runde wartete der nächste Schritt bei der typischen Änderung {v}.',
      target: 'Ziel p50 unter {t}', lineA: 'p50 |{p50}| · p90 |{p90}', lineB: '{n} Wartezeiten in {i} Änderungen', lineBOne: '{n} Wartezeiten in 1 Änderung',
      pp: 'p50 |{p50}| · p90 |{p90}', worst: 'Leerlauf {v}',
    },
    lead: {
      name: 'Durchlaufzeit pro Änderung', unit: 'h', chartUnit: 'h',
      learn: 'Vom Start des ersten Builds bis zum Merge, in Uhrzeit: jede Runde, jede Wartezeit und die Merge-Queue inklusive. Noch kein Ziel: wird beobachtet.',
      wants: 'Noch kein Ziel: wird beobachtet.', say: 'Vom ersten Build bis zum Merge dauerte es bei der typischen Änderung etwa {v}.',
      target: 'Noch kein Ziel: beobachtet', lineA: 'p50 |{p50}| · p90 |{p90}', lineB: '{n} Änderungen', lineBOne: '1 Änderung',
      pp: 'p50 |{p50}| · p90 |{p90}', worst: 'Durchlauf {v}',
    },
  },
  readout: { first: '{l} · {v} · {k} von {n} {items}', rounds: '{l} · p50 {p50} · Mittel {mean} · {n} {items}', idle: '{l} · p50 {p50} · p90 {p90} · {n} {waits}', lead: '{l} · p50 {p50} · p90 {p90} · {n} {items}' },
  chartTarget: { first_try: 'Ziel ≥ {t}', rounds: 'Ziel ≤ {t}', idle: 'Ziel < {t}' },
  sparkTarget: 'Ziel {t}',
}

export const buildingText = (lang: DeliveryLanguage): BuildingText => lang === 'de' ? DE : EN
