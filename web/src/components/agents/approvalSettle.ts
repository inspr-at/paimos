// SPDX-License-Identifier: AGPL-3.0-only
// A decided permission request answers on its own card before it leaves (AEON-505).
// pending → confirming (the call is out) → success (the server said yes, about 1.2 s)
// → collapsing (the card folds its height away and Decided takes it) → collapsed.
// Nothing shows success before the server answered: a failed call drops the card back
// to pending, actionable, and the caller shows the error. With reduced motion the
// success state is replaced without a fold.
import { computed, reactive } from 'vue'
import type { Approval } from '../../lib/agents'
import { scopeLabel } from '../../lib/agentState'

export type Decision = 'approved' | 'denied'
export type SettlePhase = 'pending' | 'confirming' | 'success' | 'collapsing' | 'collapsed'
export interface Settling {
  approval: Approval
  phase: 'confirming' | 'success' | 'collapsing'
  // From the server's answer; the reason is the one it accepted with it.
  decision?: Decision
  reason?: string
  // The card's height when the answer came, so the success state takes its place exactly.
  height?: number
}
export const SUCCESS_MS = 1200
export const COLLAPSE_MS = 280

// "Harbor Clerk may change tickets" / "Harbor Clerk may not change tickets".
export function settledLine(decision: Decision, agent: string, scope: string) {
  const label = scopeLabel(scope)
  const what = `${label.charAt(0).toLowerCase()}${label.slice(1)}`
  return `${agent} ${decision === 'approved' ? 'may' : 'may not'} ${what}`
}
export const settledWord = (decision: Decision) => decision === 'approved' ? 'Approved' : 'Denied'
// What a screen reader hears, once, politely.
export function settledAnnouncement(decision: Decision, agent: string, scope: string, reason?: string) {
  return `${settledWord(decision)}. ${settledLine(decision, agent, scope)}.${reason ? ` Reason: ${reason}` : ''}`
}

export interface SettleOptions {
  decide: (approval: Approval, decision: Decision, reason: string) => Promise<Pick<Approval, 'decision'>>
  reducedMotion?: () => boolean
  measure?: (id: string) => number | undefined
  // The server confirmed: the card is about to show its success state.
  onConfirmed?: (entry: Settling) => void
  onSettled?: (entry: Settling) => void
}

export function createApprovalSettle(options: SettleOptions) {
  const entries = reactive(new Map<string, Settling>())
  const done = reactive(new Set<string>())
  const timers = new Set<ReturnType<typeof setTimeout>>()
  const later = (ms: number, run: () => void) => {
    const timer = setTimeout(() => { timers.delete(timer); run() }, ms)
    timers.add(timer)
  }
  const phase = (id: string): SettlePhase => entries.get(id)?.phase ?? (done.has(id) ? 'collapsed' : 'pending')
  // Still on its card: Decided and the history do not count it yet.
  const holding = (id: string) => ['confirming', 'success'].includes(phase(id))

  async function submit(approval: Approval, decision: Decision, reason: string) {
    if (entries.has(approval.id)) return
    entries.set(approval.id, { approval, phase: 'confirming' })
    let answer: Pick<Approval, 'decision'>
    try { answer = await options.decide(approval, decision, reason) }
    catch (error) { entries.delete(approval.id); throw error }
    const entry: Settling = { approval, phase: 'success', decision: answer.decision ?? decision, reason: reason || undefined, height: options.measure?.(approval.id) }
    options.onConfirmed?.(entry)
    entries.set(approval.id, entry)
    later(SUCCESS_MS, () => collapse(approval.id))
  }
  function collapse(id: string) {
    const entry = entries.get(id)
    if (entry?.phase !== 'success') return
    if (options.reducedMotion?.()) { finish(id); return }
    entries.set(id, { ...entry, phase: 'collapsing' })
    later(COLLAPSE_MS, () => finish(id))
  }
  function finish(id: string) {
    const entry = entries.get(id)
    if (!entry) return
    entries.delete(id)
    done.add(id)
    options.onSettled?.(entry)
  }
  function stop() { for (const timer of timers) clearTimeout(timer); timers.clear() }

  return { entries, phase, holding, submit, stop, active: computed(() => entries.size > 0) }
}
