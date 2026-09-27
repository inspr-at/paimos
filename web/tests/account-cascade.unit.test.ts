// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  allowanceWindowLabel, authorFamilyFor, chooseStep, emptyChoice, emptyTouch, fetchAccountCatalog, fillDefaults, isAccountCatalog, presentCascade, workRoleFor,
  type AgentAccountCatalog, type CatalogAccount, type CascadeChoice, type CascadeTouch,
} from '../src/lib/accountCascade'

const NOW = Date.parse('2026-09-27T12:00:00Z')
const profileA = 'b1000000-0000-4000-8000-000000000001'
const profileB = 'b1000000-0000-4000-8000-000000000002'
const profileC = 'b1000000-0000-4000-8000-000000000003'
const accountA = 'ac000000-0000-4000-8000-000000000001'
const accountB = 'ac000000-0000-4000-8000-000000000002'
const agentA = 'a0000000-0000-4000-8000-000000000001'
const agentB = 'a0000000-0000-4000-8000-000000000002'

function window(hours: number, remaining = 50, allowance = 100) {
  return {
    id: `w-${hours}`, starts_at: new Date(NOW - hours * 3_600_000 / 2).toISOString(), ends_at: new Date(NOW + hours * 3_600_000 / 2).toISOString(),
    unit: 'requests' as const, allowance, used: allowance - remaining, reserved: 0, pace_model: 'unrestricted' as const, burst_ratio: 0,
    provisional: false, remaining, pace_remaining: remaining,
  }
}
function account(overrides: Partial<CatalogAccount> = {}): CatalogAccount {
  return {
    id: accountA, label: 'Workspace', plan: 'Pro', registered_by_principal_id: agentA, state: 'available',
    last_probe_at: new Date(NOW).toISOString(), last_probe_ok: true, available: true, unavailable_reasons: [],
    remaining_fraction: 0.5, windows: [window(5)], models: [{ model: 'builder', family: 'openai', efforts: [{ effort: 'high', model_profile_id: profileA, version: '1' }] }],
    default_model_profile_id: profileA, ...overrides,
  }
}
function catalog(accounts: CatalogAccount[] = [account()], extraHosts: AgentAccountCatalog['hosts'] = []): AgentAccountCatalog {
  const ranked = accounts.filter(item => item.available && item.remaining_fraction != null)
    .sort((a, b) => (b.remaining_fraction ?? 0) - (a.remaining_fraction ?? 0) || (a.id < b.id ? -1 : 1))
  return {
    as_of: new Date(NOW).toISOString(), role: 'build',
    hosts: [{ daemon_id: 'workstation', label: 'Work Mac', harnesses: [{ harness: 'codex', accounts, default_account_id: ranked[0]?.id ?? null }] }, ...extraHosts],
  }
}
const context = (value: AgentAccountCatalog | null, role: 'build' | 'review-gate' = 'build') => ({ catalog: value, role: { role, source: 'ticket' as const }, authorFamily: '' as const })
function presented(value: AgentAccountCatalog | null, choice?: CascadeChoice, touch?: CascadeTouch) {
  const filled = fillDefaults(value, choice ?? emptyChoice(), touch ?? emptyTouch())
  return { filled, view: presentCascade(context(value), filled, touch ?? emptyTouch()) }
}

describe('account catalog contract', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('accepts the nested catalog and rejects the old flat launch catalog', () => {
    expect(isAccountCatalog(catalog())).toBe(true)
    expect(isAccountCatalog({ hosts: [{ id: 'mac', harnesses: [{ harness: 'codex', installed: true }] }], models: [] })).toBe(false)
  })

  it('fails closed when the catalog is missing, forbidden, or the wrong shape', async () => {
    expect(await fetchAccountCatalog('review-gate', '')).toMatchObject({ catalog: null, gap: 'family' })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'Permission denied' }), { status: 403 })))
    expect(await fetchAccountCatalog('build')).toMatchObject({ gap: 'forbidden', message: 'Permission denied' })
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 404 })))
    expect((await fetchAccountCatalog('scout')).gap).toBe('failed')
    vi.stubGlobal('fetch', vi.fn(async (path: string) => {
      expect(String(path)).toContain('/api/agent-accounts/catalog?role=build-hard')
      return new Response(JSON.stringify({ hosts: [{ id: 'legacy', harnesses: [] }] }), { status: 200 })
    }))
    expect((await fetchAccountCatalog('build-hard')).gap).toBe('failed')
    vi.stubGlobal('fetch', vi.fn(async (path: string) => {
      expect(String(path)).toContain('role=review-gate')
      expect(String(path)).toContain('author_family=anthropic')
      return new Response(JSON.stringify(catalog()), { status: 200 })
    }))
    expect((await fetchAccountCatalog('review-gate', 'anthropic')).gap).toBeNull()
  })

  it('names allowance windows from their span and does not call a 6-hour window 5-hour', () => {
    expect(allowanceWindowLabel(window(5))).toBe('5-hour')
    expect(allowanceWindowLabel(window(6))).toBe('6h window')
    expect(allowanceWindowLabel(window(24))).toBe('Daily')
    expect(allowanceWindowLabel(window(24 * 7))).toBe('Weekly')
    expect(allowanceWindowLabel(window(24 * 30))).toBe('Monthly')
    expect(allowanceWindowLabel({ starts_at: 'nope', ends_at: 'nope' })).toBe('Window timing unknown')
  })

  it('defaults to the greatest remaining fraction and the routed profile, with a UUID tie', () => {
    const low = account({ id: accountB, label: 'Spare', plan: '', remaining_fraction: 0.2, registered_by_principal_id: agentB, default_model_profile_id: profileB, models: [{ model: 'spare', family: 'openai', efforts: [{ effort: 'low', model_profile_id: profileB, version: '1' }] }] })
    const high = account({ remaining_fraction: 0.8 })
    const { filled, view } = presented(catalog([low, high]))
    expect(filled.accountId).toBe(accountA)
    expect(filled.profileId).toBe(profileA)
    expect(view.agentId).toBe(agentA)
    expect(view.notes.account).toContain('most allowance left')
    expect(view.requested.model).toBe('builder')
    const tieLow = account({ id: accountA, remaining_fraction: 0.4 })
    const tieHigh = account({ id: accountB, remaining_fraction: 0.4, label: 'Later' })
    const tied = catalog([tieHigh, tieLow])
    tied.hosts[0].harnesses[0].default_account_id = accountA
    expect(presented(tied).filled.accountId).toBe(accountA)
  })

  it('does not invent a model when the role has no route or the grant is empty', () => {
    const granted = account({
      default_model_profile_id: null,
      models: [
        { model: 'builder', family: 'openai', efforts: [{ effort: 'high', model_profile_id: profileA, version: '1' }, { effort: 'low', model_profile_id: profileC, version: '1' }] },
        { model: 'spare', family: 'openai', efforts: [{ effort: 'medium', model_profile_id: profileB, version: '1' }] },
      ],
    })
    const open = presented(catalog([granted]))
    expect(open.filled.modelKey).toBe('')
    expect(open.filled.profileId).toBe('')
    expect(open.view.models.map(item => item.label)).toEqual(['builder', 'spare'])
    expect(open.view.notes.model).toContain('no routed model')
    const blocked = presented(catalog([account({ models: [], default_model_profile_id: null, available: false, unavailable_reasons: ['models'] })]))
    expect(blocked.view.models).toEqual([])
    expect(blocked.view.profileId).toBe('')
    expect(blocked.view.notes.model).toContain('No granted model')
    expect(blocked.view.status.label).toBe('No model granted')
  })

  it('resets downstream choices and keeps an explicit account from falling back', () => {
    const other = account({ id: accountB, label: 'Spare', plan: 'Plus', remaining_fraction: 0.2, default_model_profile_id: profileB, models: [{ model: 'spare', family: 'openai', efforts: [{ effort: 'low', model_profile_id: profileB, version: '4' }] }] })
    const value = catalog([account(), other])
    const initial = fillDefaults(value, emptyChoice(), emptyTouch())
    const picked = chooseStep(initial, emptyTouch(), 'account', accountB)
    const next = fillDefaults(value, picked.choice, picked.touch)
    expect(next.accountId).toBe(accountB)
    expect(next.profileId).toBe(profileB)
    expect(presentCascade(context(value), next, picked.touch).requested.model).toBe('spare')
    const cleared = chooseStep(next, picked.touch, 'host', '')
    expect(fillDefaults(value, cleared.choice, cleared.touch)).toMatchObject({ hostId: '', harness: '', accountId: '', modelKey: '', profileId: '' })
  })

  it('states unavailable reasons and hides secret-shaped labels', () => {
    const stale = account({ label: 'CODEX_HOME=/secret', plan: 'sk-live', available: false, unavailable_reasons: ['probe', 'allowance'], remaining_fraction: null })
    const view = presented(catalog([stale])).view
    expect(view.accounts[0].label).toContain('Unlabeled account')
    expect(view.accounts[0].label).not.toMatch(/secret|sk-live/i)
    expect(view.status.label).toBe('Sign-in probe is stale')
    expect(view.status.detail).toContain('no allowance headroom')
    expect(view.status.detail).toContain('does not switch to another')
    expect(allowanceWindowLabel(window(5))).not.toContain('secret')
  })

  it('reads the ticket work type and does not guess an author family', () => {
    expect(workRoleFor({ fields: { work_role: 'scout' } })).toEqual({ role: 'scout', source: 'ticket' })
    expect(workRoleFor({ fields: { work_role: 'nope' } })).toEqual({ role: 'build', source: 'default' })
    expect(workRoleFor(null)).toEqual({ role: 'build', source: 'default' })
    expect(authorFamilyFor({ fields: { author_family: 'xai' } })).toBe('xai')
    expect(authorFamilyFor({ fields: { author_family: 'local' } })).toBe('')
  })
})
