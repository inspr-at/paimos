// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1171: what needs the person in Accounts, Computers and Capacity. Risks: a
// matrix answer without the new facts (or a malformed one) invents items; a
// project list beyond the bound is shown; the footer leads to a part without items.
import { describe, expect, it } from 'vitest'
import { accountsPartNeeds, HOLDING_FIRST, matrixNeedFacts, needCount, needsPart, NO_MATRIX_FACTS, partCounts, workContextLink, type MatrixNeedFacts } from '../src/lib/accountsNeeds'
import type { OverviewAccount } from '../src/lib/accountsOverview'
import type { CapacityWindow } from '../src/lib/capacity'

const NOW = Date.parse('2026-10-11T10:00:00Z')
const window = (remaining: number): CapacityWindow => ({
  reading: { window_kind: 'weekly', bucket: '', window_minutes: 10080, used_percent: 100 - remaining, resets_at: '2026-10-13T10:00:00Z', plan: 'Pro', source: 'harness', read_at: '2026-10-11T09:55:00Z' },
  starts_at: '2026-10-06T10:00:00Z', allowance: 100, remaining_percent: remaining, freshness: 'fresh', usage_today_known: true,
} as unknown as CapacityWindow)
const account = (id: string, vendor: string, remaining: number): OverviewAccount => ({ id, vendor, harness: vendor.toLowerCase(), identity: `${id}@example.com`, records: [], rows: [], signins: [], windows: [window(remaining)] })
const thresholds = { early_percent: 10, urgent_percent: 3 }
const input = (facts: MatrixNeedFacts = NO_MATRIX_FACTS, chooses = true) => ({ accounts: [account('a', 'Codex', 9), account('b', 'Claude', 40)], thresholds, now: NOW, facts, chooses })

describe('matrix facts', () => {
  it('count nothing while the server does not report them, and nothing for a malformed answer', () => {
    expect(matrixNeedFacts({ accounts: [{ id: 'a', label: 'Main', harness: 'codex' }] })).toEqual(NO_MATRIX_FACTS)
    expect(matrixNeedFacts(null)).toEqual(NO_MATRIX_FACTS)
    expect(matrixNeedFacts({ accounts: 'x', holding_projects: { count: 'many', first: 'no' } })).toEqual(NO_MATRIX_FACTS)
    expect(matrixNeedFacts({ accounts: [{ id: 'a', awaiting: 'yes' }, { awaiting: true }] }).awaiting).toEqual([])
  })
  it('read awaiting accounts and at most the bound of projects waiting in Unassigned', () => {
    const first = Array.from({ length: HOLDING_FIRST + 5 }, (_, i) => ({ project_id: `p${i}`, key: `K${i}`, name: `Project ${i}` }))
    const facts = matrixNeedFacts({ accounts: [{ id: 'a', label: 'Spare', harness: 'codex', awaiting: true }, { id: 'b', label: 'Main', harness: 'codex', awaiting: false }], holding_projects: { count: 31, first } })
    expect(facts.awaiting).toEqual([{ id: 'a', label: 'Spare', harness: 'codex' }])
    expect(facts.holding.first).toHaveLength(HOLDING_FIRST)
    expect(facts.holding.count).toBe(31)
    // A count below the named projects is not believed.
    expect(matrixNeedFacts({ holding_projects: { count: 0, first: first.slice(0, 2) } }).holding.count).toBe(2)
  })
})

describe('the Accounts part', () => {
  it('lists a low quota by the workspace thresholds, and nothing else when the matrix reports nothing', () => {
    const items = accountsPartNeeds(input())
    expect(items.map(i => i.id)).toEqual(['quota:a'])
    expect(items[0]).toMatchObject({ name: 'Codex quota is low', detail: '9% left · Weekly', count: 1, action: { label: 'Details' } })
    expect(accountsPartNeeds({ ...input(), thresholds: { early_percent: 5, urgent_percent: 3 } })).toEqual([])
  })
  it('lists a new account that asks first and one row for projects in Unassigned', () => {
    const one = accountsPartNeeds(input({ awaiting: [{ id: 'n', label: 'Spare', harness: 'codex' }], holding: { count: 1, first: [{ project_id: 'p', key: 'PHAROS', name: 'Pharos' }] } }))
    expect(one.map(i => [i.id, i.name, i.action?.label])).toEqual([
      ['quota:a', 'Codex quota is low', 'Details'],
      ['new:n', 'New account found: Spare', 'Choose where'],
      ['holding', 'Pharos waits in Unassigned', 'Choose its context'],
    ])
    const many = accountsPartNeeds(input({ awaiting: [], holding: { count: 3, first: [{ project_id: 'p', key: 'PHAROS', name: 'Pharos' }] } }))
    expect(many.at(-1)).toMatchObject({ name: '3 projects wait in Unassigned', count: 1, action: { label: 'Choose their contexts' } })
    // Someone who may not choose reads the item; nothing claims to fix it.
    expect(accountsPartNeeds(input({ awaiting: [{ id: 'n', label: '', harness: 'codex' }], holding: { count: 0, first: [] } }, false))[1]).toMatchObject({ name: 'New account found: an account', action: { label: 'Details', fixesProblem: false } })
  })
})

describe('counts and where the footer leads', () => {
  it('count each part, calm rows as zero', () => {
    expect(needCount([{ count: 1 }, { count: 0 }, { count: 2 }])).toBe(3)
    expect(partCounts({ accounts: [{ count: 1 }], computers: [{ count: 0 }], capacity: [] })).toEqual({ accounts: 1, computers: 0, capacity: 0 })
  })
  it('lead to the open part when it has items, else the first part that has some', () => {
    expect(needsPart({ accounts: 1, computers: 2, capacity: 0 }, 'computers')).toBe('computers')
    expect(needsPart({ accounts: 0, computers: 2, capacity: 1 }, 'accounts')).toBe('computers')
    expect(needsPart({ accounts: 0, computers: 0, capacity: 1 }, 'personal')).toBe('capacity')
    expect(needsPart({}, 'accounts')).toBeNull()
  })
  it('open a waiting project at its Work context card', () => {
    expect(workContextLink({ key: 'PRJ 17' })).toBe('/p/PRJ%2017/settings#project-work-context')
  })
})
