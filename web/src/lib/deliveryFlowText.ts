// SPDX-License-Identifier: AGPL-3.0-only
// Words of Delivery › Flow lanes (AEON-994 draft 5, section 7: flow.* and lanes.*).
// English and German, impersonal; the person's profile locale picks one.
import type { DeliveryLanguage } from './delivery'
import type { Lane } from './deliveryFlow'

const EN = {
  legend: 'Legend', lgWork: 'working', lgWait: 'waiting for …', lgRework: 'doing it again', lgFut: 'expected', lgHand: 'hand-over', lgTgt: 'Arion target', lgInc: 'incident + recovery',
  live: 'Live · now {t}', viewing: 'Viewing {t}', at: 'At {t}', now: 'now', expected: 'expected',
  zoom: 'Zoom', fit: 'Fit all', zooms: { 15: '15 min', 30: '30 min', 60: '1 h' } as Record<number, string>,
  follow: 'Follow', following: 'Following', backNow: 'Back to now', followingNow: 'Following now',
  // {mod} {left} {right} {up} {down} are drawn as keys, never typed as symbols.
  hint: 'Drag the time handle to move the time · click a step for details · drag or scroll sideways to pan · {mod}+scroll zooms · keys: {left}{right} pan · Shift+{left}{right} time · {up}{down} lane · Enter select',
  overview: 'Whole run', overviewLabel: 'Overview: move the visible window', chart: 'flow chart', lanesLabel: 'Lanes: who worked and who waited',
  since: 'since', target: 'target', healthy: 'healthy again', more: 'more',
  kinds: { work: 'Working', wait: 'Waiting', rework: 'Doing it again', incident: 'Incident + recovery' },
  example: 'Until then, release 126 from 8 Oct is shown as an example.',
  actors: {
    expert: { you: 'Markus', lead: 'LEAD', ops: 'OPS', review: 'Reviewer', build: 'Builder', ci: 'CI & queue' },
    simple: { you: 'You', lead: 'LEAD (agent)', ops: 'OPS (agent)', review: 'Reviewer (agent)', build: 'Builder (agent)', ci: 'Checks (machines)' },
    short: { you: 'You', lead: 'LEAD', ops: 'OPS', review: 'Review', build: 'Build', ci: 'Checks' },
  } as Record<'expert' | 'simple' | 'short', Record<Lane, string>>,
}
export type FlowText = typeof EN

const DE: FlowText = {
  legend: 'Legende', lgWork: 'Arbeit', lgWait: 'wartet auf …', lgRework: 'noch einmal', lgFut: 'erwartet', lgHand: 'Übergabe', lgTgt: 'Arion-Ziel', lgInc: 'Störung + Behebung',
  live: 'Live · jetzt {t}', viewing: 'Ansicht {t}', at: 'Um {t}', now: 'jetzt', expected: 'erwartet',
  zoom: 'Zoom', fit: 'Alles', zooms: { 15: '15 min', 30: '30 min', 60: '1 h' },
  follow: 'Folgen', following: 'Folgt', backNow: 'Zurück zu jetzt', followingNow: 'Folgt jetzt',
  hint: 'Zeit-Griff ziehen verschiebt die Zeit · Schritt anklicken für Details · ziehen oder seitlich scrollen verschiebt · {mod}+Scrollen zoomt · Tasten: {left}{right} schieben · Umschalt+{left}{right} Zeit · {up}{down} Spur · Enter wählen',
  overview: 'Ganzer Lauf', overviewLabel: 'Übersicht: Ausschnitt verschieben', chart: 'Ablauf', lanesLabel: 'Spuren: wer gearbeitet und wer gewartet hat',
  since: 'seit', target: 'Ziel', healthy: 'wieder gesund', more: 'mehr',
  kinds: { work: 'Arbeit', wait: 'Warten', rework: 'Noch einmal', incident: 'Störung + Behebung' },
  example: 'Bis dahin dient Release 126 vom 8. Okt. als Beispiel.',
  actors: {
    expert: { you: 'Markus', lead: 'LEAD', ops: 'OPS', review: 'Reviewer', build: 'Builder', ci: 'CI & Queue' },
    simple: { you: 'Du', lead: 'LEAD (Agent)', ops: 'OPS (Agent)', review: 'Reviewer (Agent)', build: 'Builder (Agent)', ci: 'Checks (Maschinen)' },
    short: { you: 'Du', lead: 'LEAD', ops: 'OPS', review: 'Review', build: 'Build', ci: 'Checks' },
  },
}

export const flowText = (lang: DeliveryLanguage): FlowText => lang === 'de' ? DE : EN
export type HintPart = { text: string } | { key: 'mod' | 'left' | 'right' | 'up' | 'down' }
/** The hint as text and keys; keys are drawn, the modifier follows the platform. */
export function hintParts(hint: string): HintPart[] {
  return hint.split(/(\{(?:mod|left|right|up|down)\})/).filter(Boolean)
    .map(part => /^\{\w+\}$/.test(part) ? { key: part.slice(1, -1) as 'mod' } : { text: part })
}
