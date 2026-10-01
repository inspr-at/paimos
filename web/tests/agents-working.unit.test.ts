// SPDX-License-Identifier: AGPL-3.0-only
// AEON-499: the Agents working control. Running counts come from sessions,
// targets from the person's preference; row targets never exceed the total and
// the words never claim that anything starts agents automatically.
import { describe, expect, it } from 'vitest'
import { freeLine, stepCap, stepRow, workingPlan, type RunningSession } from '../src/lib/agentsWorking'

const sessions: RunningSession[] = [
  ...Array.from({ length: 5 }, () => ({ harness: 'codex', model: 'gpt-6.1-sol', area: 'backend' })),
  { harness: 'codex', model: 'gpt-6.1-sol', area: 'frontend' },
  { harness: 'claude', model: 'opus', area: 'design' },
  { harness: 'codex', model: 'gpt-6.1-sol', area: '' },
]

describe('agents working', () => {
  it('counts running sessions against the total, with honest words', () => {
    const plan = workingPlan({ pref: { cap: 10 }, sessions, harnesses: ['codex', 'claude', 'grok'], capacityKnown: false, roomNow: null })
    expect(plan.running).toBe(8)
    expect(plan.runningLine).toBe('2 slots free')
    expect(plan.capDots).toEqual([true, true, true, true, true, true, true, true, false, false])
    expect(plan.footLine).toBe('0 of 10 assigned · 10 flexible: any area takes them · no capacity readings yet, so the limits are yours, not the accounts’')
    expect(plan.footLine).not.toMatch(/autopilot|starts/i)
    expect(freeLine(8, 8)).toBe('every slot busy')
    expect(freeLine(6, 8)).toBe('2 over your target')
  })
  it('by area: every area, plus No area set when a ticket has none', () => {
    const plan = workingPlan({ pref: { cap: 10, area: { backend: 6, design: 1 } }, sessions, harnesses: [], capacityKnown: true, roomNow: 3 })
    expect(plan.rows.map(r => r.label)).toEqual(['Backend', 'Frontend', 'Full stack', 'Infrastructure', 'Design', 'Docs', 'No area set'])
    const backend = plan.rows[0]
    expect(backend).toMatchObject({ running: 5, target: 6, canDec: true, canInc: true })
    expect(backend.dots).toEqual([true, true, true, true, true, false])
    expect(plan.footLine).toBe('7 of 10 assigned · 3 flexible: any area takes them · the accounts have room for 3 more agents now')
  })
  it('by model: harnesses with accounts or work, in pool order, with their models', () => {
    const plan = workingPlan({ pref: { cap: 10, view: 'model', model: { codex: 8 } }, sessions, harnesses: ['grok', 'codex'], capacityKnown: true, roomNow: null })
    expect(plan.rows.map(r => [r.label, r.running, r.target])).toEqual([['Codex', 7, 8], ['Claude', 1, 0], ['Grok', 0, 0]])
    expect(plan.rows[0].sub).toBe('7 running · gpt-6.1-sol')
    expect(plan.rows[1].canDec).toBe(false)
  })
  it('row targets stay within the total; lowering the total trims the last targets', () => {
    let pref = stepRow({ cap: 2 }, 'area', 'backend', 1)
    pref = stepRow(pref, 'area', 'design', 1)
    expect(stepRow(pref, 'area', 'docs', 1)).toBe(pref)
    expect(workingPlan({ pref, sessions: [], harnesses: [], capacityKnown: true, roomNow: null }).rows.every(r => !r.canInc)).toBe(true)
    const lower = stepCap(pref, -1)
    expect(lower).toMatchObject({ cap: 1, area: { backend: 1, design: 0 } })
    expect(stepCap({ cap: 12 }, 1).cap).toBe(12)
    expect(stepCap({ cap: 1 }, -1).cap).toBe(1)
  })
})
