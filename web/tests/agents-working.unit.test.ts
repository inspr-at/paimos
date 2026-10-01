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
  it('without a chosen total nothing stands in for it: no number, no free slots, no assigned share', () => {
    const plan = workingPlan({ pref: null, sessions, harnesses: [], capacityKnown: true, roomNow: null })
    expect(plan).toMatchObject({ unset: true, cap: null, from: 8, runningLine: 'no target set yet', flexible: 0, footLine: '' })
    expect(plan.capDots).toEqual(Array(8).fill(true))
    // A first step starts from what runs now; a row target fixes that total.
    expect(stepCap(null, 1, plan.from).cap).toBe(9)
    expect(stepCap(null, -1, plan.from).cap).toBe(7)
    expect(stepRow(null, 'area', 'docs', 1, plan.from)).toEqual({ cap: 8, area: { docs: 1 } })
  })
  // AEON-499 review: zero to three running once showed "Target: 4" and four slots nobody chose.
  for (const n of [0, 1, 2, 3]) {
    it(`with ${n} running and no total, no target is invented`, () => {
      const running = sessions.slice(0, n)
      const plan = workingPlan({ pref: null, sessions: running, harnesses: ['codex'], capacityKnown: false, roomNow: null })
      expect(plan.cap).toBeNull()
      expect(plan.running).toBe(n)
      expect(plan.capDots).toEqual(Array(n).fill(true))
      expect(plan.capDots.filter(on => !on)).toHaveLength(0)
      expect(plan.flexible).toBe(0)
      expect(plan.footLine).not.toMatch(/assigned|flexible|\b4\b/)
      expect(plan.footLine).toBe('No capacity readings yet, so the limits are yours, not the accounts’')
      expect(plan.runningLine).toBe('no target set yet')
      // − lowers below what runs now only when that leaves at least one; + always starts one above it.
      expect(plan.canFewer).toBe(n > 1)
      expect(plan.canMore).toBe(true)
      expect(stepCap(null, 1, plan.from).cap).toBe(n + 1)
      expect(plan.rows.every(r => r.target === 0 && r.canInc)).toBe(true)
      expect(plan.full).toBe(false)
    })
  }
  it('a stored total, once loaded, is the number', () => {
    const plan = workingPlan({ pref: { cap: 6 }, sessions: sessions.slice(0, 2), harnesses: [], capacityKnown: true, roomNow: null })
    expect(plan).toMatchObject({ unset: false, cap: 6, runningLine: '4 slots free', footLine: '0 of 6 assigned · 6 flexible: any area takes them' })
    expect(plan.capDots).toEqual([true, true, false, false, false, false])
  })
  it('by area: only areas with work or a target, the rest as quiet choices', () => {
    const plan = workingPlan({ pref: { cap: 10, area: { backend: 6, design: 1 } }, sessions, harnesses: [], capacityKnown: true, roomNow: 3 })
    expect(plan.rows.map(r => r.label)).toEqual(['Backend', 'Frontend', 'Design', 'No area set'])
    expect(plan.spare.map(a => a.label)).toEqual(['Full stack', 'Infrastructure', 'Docs'])
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
    let pref = stepRow({ cap: 2 }, 'area', 'backend', 1, 0)
    pref = stepRow(pref, 'area', 'design', 1, 0)
    expect(stepRow(pref, 'area', 'docs', 1, 0)).toBe(pref)
    const full = workingPlan({ pref, sessions: [], harnesses: [], capacityKnown: true, roomNow: null })
    expect(full.rows.every(r => !r.canInc)).toBe(true)
    expect(full.full).toBe(true)
    const lower = stepCap(pref, -1, 0)
    expect(lower).toMatchObject({ cap: 1, area: { backend: 1, design: 0 } })
    expect(stepCap({ cap: 12 }, 1, 0).cap).toBe(12)
    expect(stepCap({ cap: 1 }, -1, 0).cap).toBe(1)
  })
})
