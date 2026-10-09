// SPDX-License-Identifier: AGPL-3.0-only
// Words of Delivery › Flow (AEON-994 draft 5, section 7: flow.* and lanes.*), packages 5
// and 6. English and German, impersonal; the person's profile locale picks one.
import type { DeliveryLanguage } from './delivery'
import type { Lane } from './deliveryFlow'

/** The arrow reads as the word "to" (a to l, start to end); the cross as "times" (2x speed). */
export const TO = '→'
export const TIMES = '×'

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
  modes: { live: 'Live', replay: 'Replay', compare: 'Compare' }, modesLabel: 'Flow mode', moment: 'At this moment', nowLive: 'Now {t} · live',
  play: 'Play', pause: 'Pause', speed: 'Speed', rmNote: 'Reduced motion: drag the time handle to step through.',
  pickRun: 'Run to replay', pickPair: 'What to race', final: 'final', soFarRun: 'so far', vsTarget: '{a} vs. Arion target',
  inRun: '{t} · {d} in', targetReached: 'target reached',
  loading: 'Loading the flow', errT: 'The flow could not be loaded.', errB: 'No old runs are shown in place of new ones.', retry: 'Retry',
  nothingLive: 'Nothing is moving right now.', nothingLiveB: 'Replay shows the recorded runs.', noRuns: 'No run recorded yet.',
  noRelease: `No release with the steps a ${TO} l recorded yet.`, truncated: 'More runs exist than are shown here.',
  tableLabel: 'Runs in flight', run: 'Run',
  thSimple: ['Now', 'Who is on it', 'Waiting for', 'Done', 'Expected'], thExpert: ['Item', 'Step', 'Actor', 'Waiting for', '%', 'Step ETA · p90', 'ETA · p90'],
  expected_: { release: 'healthy ~{t}', change: 'merged ~{t}' }, noEstimate: 'no reliable estimate yet', etaNone: 'none: {r}', thenYou: 'then you: {g}', thenGate: 'then {g}',
  verdicts: { on: 'On target', close: 'Close', far: 'Far off' }, timesTarget: `about {k}${TIMES} the target`, alreadyTimes: `already {k}${TIMES} the target`,
  went: 'Where the time went', working: 'Working', waiting: 'Waiting', again: 'Doing it again', incidentW: 'Incident + recovery',
  soFar: 'so far', alongside: 'alongside', handovers: 'hand-overs', recovered: 'healthy again after {n} recovery steps', recovering: 'recovery running',
  idleHead: 'Idle', idle: 'idle', nothingYou: 'nothing waiting on you', waitsOnYou: 'waiting on you', of: 'of', usually: 'usually', arion: 'Arion',
  clickSeg: 'Click a step for its details.', noInc: 'No incident at this moment.', incOn: 'Incident active', soFarMin: '{m} min so far', ends: 'ends {t}', liveAfter: 'live after +{d}',
  terms: { started: 'Start', took: 'Took', outcome: 'Outcome', round: 'Round', waitsFor: 'Waits for', step: 'Step', actor: 'Actor', startEnd: `Start ${TO} end`, duration: 'Duration', source: 'Source' },
  sources: { github_app: 'GitHub App', paimos: 'PAIMOS', ops_rollout: 'OPS rollout record', arion: 'Arion target (arion.md § 4)' } as Record<string, string>,
  kindWords: { work: 'work', wait: 'wait', rework: 'rework' } as Record<string, string>, incidentWord: 'incident', sideWord: 'alongside',
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
  modes: { live: 'Live', replay: 'Wiedergabe', compare: 'Vergleich' }, modesLabel: 'Ablauf-Modus', moment: 'In diesem Moment', nowLive: 'Jetzt {t} · live',
  play: 'Abspielen', pause: 'Pause', speed: 'Tempo', rmNote: 'Weniger Bewegung: den Zeit-Griff ziehen, um schrittweise durchzugehen.',
  pickRun: 'Wiederzugebender Lauf', pickPair: 'Was antritt', final: 'final', soFarRun: 'bis jetzt', vsTarget: '{a} gegen Arion-Ziel',
  inRun: '{t} · {d} seit Start', targetReached: 'Ziel erreicht',
  loading: 'Ablauf wird geladen', errT: 'Der Ablauf konnte nicht geladen werden.', errB: 'Alte Läufe werden nicht anstelle neuer gezeigt.', retry: 'Erneut versuchen',
  nothingLive: 'Gerade läuft nichts.', nothingLiveB: 'Unter Wiedergabe stehen die aufgezeichneten Läufe.', noRuns: 'Noch kein Lauf aufgezeichnet.',
  noRelease: `Noch kein Release mit den Schritten a ${TO} l aufgezeichnet.`, truncated: 'Es gibt mehr Läufe, als hier zu sehen sind.',
  tableLabel: 'Laufende Einträge', run: 'Lauf',
  thSimple: ['Jetzt', 'Wer dran ist', 'Wartet auf', 'Erledigt', 'Erwartet'], thExpert: ['Eintrag', 'Schritt', 'Akteur', 'Wartet auf', '%', 'Schritt-ETA · p90', 'ETA · p90'],
  expected_: { release: 'gesund ~{t}', change: 'gemergt ~{t}' }, noEstimate: 'noch keine verlässliche Schätzung', etaNone: 'keine: {r}', thenYou: 'dann dich: {g}', thenGate: 'dann {g}',
  verdicts: { on: 'Im Ziel', close: 'Knapp dran', far: 'Weit weg' }, timesTarget: `etwa {k}${TIMES} das Ziel`, alreadyTimes: `schon {k}${TIMES} das Ziel`,
  went: 'Wohin die Zeit ging', working: 'Arbeit', waiting: 'Warten', again: 'Noch einmal', incidentW: 'Störung + Behebung',
  soFar: 'bisher', alongside: 'nebenher', handovers: 'Übergaben', recovered: 'nach {n} Behebungsschritten wieder gesund', recovering: 'Behebung läuft',
  idleHead: 'Frei', idle: 'frei', nothingYou: 'nichts wartet auf dich', waitsOnYou: 'wartet auf dich', of: 'von', usually: 'üblich', arion: 'Arion',
  clickSeg: 'Einen Schritt anklicken für Details.', noInc: 'Gerade keine Störung.', incOn: 'Störung aktiv', soFarMin: '{m} min bisher', ends: 'Ende {t}', liveAfter: 'live nach +{d}',
  terms: { started: 'Start', took: 'Dauer', outcome: 'Ergebnis', round: 'Runde', waitsFor: 'Wartet auf', step: 'Schritt', actor: 'Akteur', startEnd: `Start ${TO} Ende`, duration: 'Dauer', source: 'Quelle' },
  sources: { github_app: 'GitHub-App', paimos: 'PAIMOS', ops_rollout: 'OPS-Rollout-Eintrag', arion: 'Arion-Ziel (arion.md § 4)' },
  kindWords: { work: 'Arbeit', wait: 'Warten', rework: 'Nacharbeit' }, incidentWord: 'Störung', sideWord: 'nebenher',
  actors: {
    expert: { you: 'Markus', lead: 'LEAD', ops: 'OPS', review: 'Reviewer', build: 'Builder', ci: 'CI & Queue' },
    simple: { you: 'Du', lead: 'LEAD (Agent)', ops: 'OPS (Agent)', review: 'Reviewer (Agent)', build: 'Builder (Agent)', ci: 'Checks (Maschinen)' },
    short: { you: 'Du', lead: 'LEAD', ops: 'OPS', review: 'Review', build: 'Build', ci: 'Checks' },
  },
}

export const flowText = (lang: DeliveryLanguage): FlowText => lang === 'de' ? DE : EN
/** "{t}" style placeholders. */
export const put = (template: string, values: Record<string, string | number>) => template.replace(/\{(\w+)\}/g, (all, key: string) => key in values ? String(values[key]) : all)
export type HintPart = { text: string } | { key: 'mod' | 'left' | 'right' | 'up' | 'down' }
/** The hint as text and keys; keys are drawn, the modifier follows the platform. */
export function hintParts(hint: string): HintPart[] {
  return hint.split(/(\{(?:mod|left|right|up|down)\})/).filter(Boolean)
    .map(part => /^\{\w+\}$/.test(part) ? { key: part.slice(1, -1) as 'mod' } : { text: part })
}
