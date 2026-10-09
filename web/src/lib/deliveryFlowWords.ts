// SPDX-License-Identifier: AGPL-3.0-only
// Words for recorded flow steps (AEON-994 draft 5, package 6). Steps carry timing facts
// only — never titles, findings or logs — so a step's label is built from its step key,
// kind, round, outcome and wait reason, in the Expert and the Simple wording, EN and DE.
import type { DeliveryLanguage } from './delivery'
import type { StepOutcome, WaitReason, Words } from './deliveryFlow'

export type StepKey = 'a' | 'b' | 'c' | 'd' | 'e' | 'f' | 'g' | 'h' | 'i' | 'j' | 'k' | 'l'
  | 'copy_gate' | 'pin_gate' | 'build' | 'review' | 'ci' | 'queue' | 'merge_round' | 'hold' | 'mitigation' | 'switch' | 'live_check'
type Pair = readonly [en: string, de: string]
interface KeyWords { expert: Pair; simple: Pair }

const KEYS: Record<StepKey, KeyWords> = {
  a: { expert: ['a · Merge queue', 'a · Merge-Queue'], simple: ['Release change through the queue', 'Release-Änderung durch die Queue'] },
  b: { expert: ['b · Image check + tag', 'b · Image-Check + Tag'], simple: ['Build image checked', 'Build-Image geprüft'] },
  c: { expert: ['c · release.yml', 'c · release.yml'], simple: ['Release built', 'Release gebaut'] },
  d: { expert: ['d · Draft inspection', 'd · Entwurf prüfen'], simple: ['Release draft inspected', 'Release-Entwurf geprüft'] },
  e: { expert: ['e · agentd qualification', 'e · agentd-Qualifizierung'], simple: ['Agent app tested on a Mac', 'Agent-App auf einem Mac getestet'] },
  f: { expert: ['f · Publish', 'f · Veröffentlichen'], simple: ['Published', 'Veröffentlicht'] },
  g: { expert: ['g · Pin PR', 'g · Pin-PR'], simple: ['Server update prepared', 'Server-Update vorbereitet'] },
  h: { expert: ['h · Pin gate', 'h · Pin-Gate'], simple: ['Server update reviewed', 'Server-Update geprüft'] },
  i: { expert: ['i · Pin CI + merge', 'i · Pin-CI + Merge'], simple: ['Server config checked and merged', 'Server-Konfiguration geprüft und gemergt'] },
  j: { expert: ['j · DB snapshot', 'j · DB-Snapshot'], simple: ['Database backed up', 'Datenbank gesichert'] },
  k: { expert: ['k · Switch', 'k · Switch'], simple: ['Server switched', 'Server umgestellt'] },
  l: { expert: ['l · Live check', 'l · Live-Check'], simple: ['Checked live', 'Live geprüft'] },
  copy_gate: { expert: ['Copy gate', 'Copy-Gate'], simple: ['Release notes checked', 'Release-Notes geprüft'] },
  pin_gate: { expert: ['Pin gate', 'Pin-Gate'], simple: ['Server update reviewed', 'Server-Update geprüft'] },
  build: { expert: ['Build', 'Build'], simple: ['Being built', 'Wird gebaut'] },
  review: { expert: ['Review', 'Review'], simple: ['Reviewed', 'Review'] },
  ci: { expert: ['CI run', 'CI-Lauf'], simple: ['Checks', 'Checks'] },
  queue: { expert: ['Merge queue', 'Merge-Queue'], simple: ['Through the queue', 'Durch die Queue'] },
  merge_round: { expert: ['Merge round', 'Merge-Runde'], simple: ['Updated to the newest main', 'Auf den neuesten main gebracht'] },
  hold: { expert: ['Held', 'Angehalten'], simple: ['Held', 'Angehalten'] },
  mitigation: { expert: ['Mitigation', 'Gegenmaßnahme'], simple: ['Fix written', 'Fix geschrieben'] },
  switch: { expert: ['Switch', 'Switch'], simple: ['Server switched', 'Server umgestellt'] },
  live_check: { expert: ['Live check', 'Live-Check'], simple: ['Checked live', 'Live geprüft'] },
}
export const isStepKey = (key: string): key is StepKey => key in KEYS

const OUTCOME_EXPERT: Record<StepOutcome, Pair> = {
  ok: ['ok', 'ok'], changes: ['changes', 'Änderungen'], red: ['red', 'rot'], flaky: ['flake', 'Flake'], degraded: ['DEGRADED', 'DEGRADED'], green: ['green', 'grün'],
}
/** The outcome in plain words (moment panel, Simple). */
export const OUTCOME_PLAIN: Record<StepOutcome, Pair> = {
  ok: ['ended OK', 'endete OK'], changes: ['changes asked', 'Änderungen verlangt'], flaky: ['failed on a flaky test', 'an wackeligem Test gescheitert'],
  red: ['failed', 'gescheitert'], degraded: ['live but not working properly', 'live, aber gestört'], green: ['green', 'grün'],
}
const WAIT_EXPERT: Record<WaitReason, Pair> = {
  reviewer: ['reviewer', 'Reviewer'], queue: ['queue', 'Queue'], dependency: ['dependency', 'Abhängigkeit'], human_gate: ['approval', 'Freigabe'],
  rerun: ['re-run', 'Wiederholung'], release_train: ['release train', 'Release-Zug'],
}
/** What a wait is for, as the object of "Waiting for …" (in-flight table, Simple). */
export const WAIT_OBJECT: Record<WaitReason, Pair> = {
  reviewer: ['a reviewer', 'einen Reviewer'], queue: ['the merge queue', 'die Merge-Queue'], dependency: ['another change', 'eine andere Änderung'],
  human_gate: ['you', 'dich'], rerun: ['the re-run', 'die Wiederholung'], release_train: ['the release train', 'den Release-Zug'],
}

const L = (pair: Pair, lang: DeliveryLanguage) => lang === 'de' ? pair[1] : pair[0]
const both = (make: (lang: DeliveryLanguage) => string): Words => ({ en: make('en'), de: make('de') })

export interface StepWordsInput {
  key: string; kind: 'work' | 'wait' | 'rework' | 'recovery'; round: number
  outcome: StepOutcome | null; waitReason: WaitReason | null; waitsFor: string | null
  model: string | null; open: boolean; person: boolean
}
/** Expert and Simple labels of one recorded step. */
export function stepWords(input: StepWordsInput): { expert: Words; simple: Words } {
  const words = isStepKey(input.key) ? KEYS[input.key] : { expert: [input.key, input.key] as Pair, simple: [input.key, input.key] as Pair }
  const name = (level: 'expert' | 'simple', lang: DeliveryLanguage) => L(words[level], lang)
  const round = input.round > 1 ? input.round : 0
  if (input.kind === 'wait') {
    const reason = input.waitReason
    return {
      expert: both(lang => {
        const what = reason === 'dependency' || reason === 'human_gate' ? input.waitsFor ?? L(WAIT_EXPERT[reason], lang) : reason ? L(WAIT_EXPERT[reason], lang) : name('expert', lang)
        return `${lang === 'de' ? 'Warten' : 'Wait'} · ${what}`
      }),
      simple: both(lang => {
        const de = lang === 'de', what = input.waitsFor
        switch (reason) {
          case 'reviewer': return de ? 'Wartet auf einen Reviewer' : 'Waiting for a reviewer'
          case 'queue': return de ? 'Wartet in der Merge-Queue' : 'Waiting in the merge queue'
          case 'dependency': return what ? (de ? `Wartet, bis ${what} drin ist` : `Waiting for ${what} to land first`) : (de ? 'Wartet auf eine andere Änderung' : 'Waiting for another change')
          case 'human_gate': return input.person ? (de ? `Wartet auf dich${what ? `: ${what}` : ''}` : `Waiting for you${what ? `: ${what}` : ''}`) : (de ? `Wartet auf eine Freigabe${what ? `: ${what}` : ''}` : `Waiting for approval${what ? `: ${what}` : ''}`)
          case 'rerun': return de ? 'Wartet auf die Wiederholung' : 'Waiting for the re-run'
          case 'release_train': return de ? 'Wartet auf den Release-Zug' : 'Waiting for the release train'
          default: return de ? `Wartet: ${name('simple', lang)}` : `Waiting: ${name('simple', lang)}`
        }
      }),
    }
  }
  if (input.kind === 'rework') {
    return {
      expert: both(lang => input.key === 'build' ? (lang === 'de' ? 'Fix-Runde' : 'Fix round') : `${lang === 'de' ? 'Nacharbeit' : 'Rework'} · ${name('expert', lang)}`),
      simple: both(lang => input.key === 'build' ? (lang === 'de' ? 'Wird korrigiert' : 'Fixing it') : `${lang === 'de' ? 'Noch einmal' : 'Doing it again'}: ${name('simple', lang)}`),
    }
  }
  const outcome = input.outcome
  return {
    expert: both(lang => `${name('expert', lang)}${round ? ` r${round}` : ''}${outcome ? ` · ${L(OUTCOME_EXPERT[outcome], lang)}` : ''}${input.model ? ` · ${input.model}` : ''}`),
    simple: both(lang => {
      const de = lang === 'de', base = name('simple', lang)
      if (input.key === 'ci' || input.key === 'queue') {
        if (input.open) return input.key === 'ci' ? (de ? 'Checks laufen' : 'Checks running') : base
        if (outcome === 'flaky') return de ? 'Checks an wackeligem Test gescheitert' : 'Checks failed on a flaky test'
        if (outcome === 'red') return de ? `${base}: rot` : `${base}: failed`
        if (outcome === 'green' || outcome === 'ok') return input.key === 'ci' ? (de ? 'Checks: grün' : 'Checks: green') : base
      }
      if (input.key === 'review' || input.key === 'copy_gate' || input.key === 'pin_gate' || input.key === 'h') {
        if (outcome === 'changes') return `${base}: ${de ? 'Änderungen verlangt' : 'changes asked'}`
        if (outcome === 'ok' || outcome === 'green') return `${base}: OK`
      }
      if (outcome === 'degraded') return de ? `${base}: live, aber gestört` : `${base}: live but not working properly`
      return round ? `${base} · ${de ? 'Runde' : 'round'} ${round}` : base
    }),
  }
}

/** Words of an incident: the OPS summary for Experts, plain words for Simple. */
export function incidentWords(severity: 'degraded' | 'down', summary: string): { expert: Words; simple: Words } {
  const word = severity === 'down' ? 'DOWN' : 'DEGRADED'
  const text = summary.trim()
  const expert = !text ? word : text.toUpperCase().startsWith(word) ? text : `${word} · ${text}`
  return {
    expert: { en: expert, de: expert },
    simple: severity === 'down' ? { en: 'Not reachable', de: 'Nicht erreichbar' } : { en: 'Live, but not working properly', de: 'Live, aber gestört' },
  }
}
