// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  allowanceWindowLabel, authorFamilyFor, chooseStep, displayText, emptyChoice, emptyTouch, fetchAccountCatalog, fillDefaults, isAccountCatalog, presentCascade, publicModelId, sessionReport, workRoleFor,
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

  it('accepts percent readings and structured first-run waits without inventing headroom', () => {
    const waiting = account({ available: false, remaining_fraction: null, windows: [{ ...window(5), unit: 'percent', provisional: true }], wait: { code: 'reading', run_now_allowed: false } })
    expect(isAccountCatalog(catalog([waiting]))).toBe(true)
    const view = presented(catalog([waiting])).view
    expect(view.status.label).toBe('Waiting for the first run’s reading')
    expect(view.status.detail).toContain('queue')
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
    expect(authorFamilyFor({ fields: { author_family: 'local' } })).toBe('local')
    expect(authorFamilyFor({ fields: { author_family: 'google' } })).toBe('google')
    expect(authorFamilyFor({ fields: { author_family: 'opencode' } })).toBe('')
  })

  it('offers pi registry model ids and drops a profile that is not a visible choice', () => {
    expect(displayText('anthropic/claude-opus-5')).toBeNull()
    expect(displayText('anthropic/claude-sonnet-5')).toBeNull()
    expect(publicModelId('anthropic/claude-opus-5')).toBe('anthropic/claude-opus-5')
    expect(publicModelId('anthropic/claude-sonnet-5')).toBe('anthropic/claude-sonnet-5')
    expect(publicModelId('gpt-6-sol')).toBe('gpt-6-sol')
    expect(publicModelId('/Users/hidden/model')).toBeNull()
    expect(publicModelId('.. /secret')).toBeNull()
    expect(publicModelId('sk-live-token')).toBeNull()
    expect(displayText('CODEX_HOME=/secret')).toBeNull()
    const pi = account({
      models: [
        { model: 'anthropic/claude-opus-5', family: 'anthropic', efforts: [{ effort: 'xhigh', model_profile_id: profileA, version: '2' }] },
        { model: 'anthropic/claude-sonnet-5', family: 'anthropic', efforts: [{ effort: 'high', model_profile_id: profileB, version: '2' }] },
        { model: 'sk-live-token', family: 'anthropic', efforts: [{ effort: 'high', model_profile_id: profileC, version: '2' }] },
        { model: '/Users/hidden/model', family: 'openai', efforts: [{ effort: 'low', model_profile_id: 'b1000000-0000-4000-8000-000000000004', version: '2' }] },
      ],
      default_model_profile_id: profileA,
    })
    const offered = presented(catalog([pi]))
    expect(offered.view.models.map(item => item.label)).toEqual(['anthropic/claude-opus-5', 'anthropic/claude-sonnet-5'])
    expect(offered.view.profileId).toBe(profileA)
    expect(offered.view.requested.model).toBe('anthropic/claude-opus-5')
    const hidden = 'b1000000-0000-4000-8000-000000000099'
    const removed = presentCascade(context(catalog([pi])), {
      hostId: 'workstation', harness: 'codex', accountId: accountA, modelKey: 'anthropic\tanthropic/claude-opus-5', profileId: hidden,
    }, { host: true, harness: true, account: true, model: true, effort: true })
    expect(removed.profileId).toBe('')
    expect(removed.efforts.some(item => item.value === hidden)).toBe(false)
    const malformed = presented(catalog([account({
      models: [{ model: 'sk-live-token', family: 'openai', efforts: [{ effort: 'high', model_profile_id: profileA, version: '1' }] }],
      default_model_profile_id: profileA,
    })]))
    expect(malformed.view.models).toEqual([])
    expect(malformed.view.profileId).toBe('')
    expect(malformed.filled.profileId).toBe('')
  })

  it('treats provisional windows as unknown and does not rank unmeasured zero as the greatest allowance', () => {
    const knownId = 'ac000000-0000-4000-8000-000000000011'
    const freshId = 'ac000000-0000-4000-8000-000000000012'
    const measured = window(5, 30, 100)
    const unmeasured = { ...window(5, 100, 100), provisional: true, used: 0, remaining: 100, pace_remaining: 100 }
    const nested: AgentAccountCatalog = {
      as_of: new Date(NOW).toISOString(),
      role: 'build',
      hosts: [{
        daemon_id: 'daemon-a',
        label: 'Studio',
        harnesses: [{
          harness: 'pi',
          default_account_id: knownId,
          accounts: [
            account({
              id: knownId, label: 'Measured subscription', plan: 'Pro', remaining_fraction: 0.3, windows: [measured],
              models: [{ model: 'anthropic/claude-sonnet-5', family: 'anthropic', efforts: [{ effort: 'high', model_profile_id: profileB, version: '2' }] }],
              default_model_profile_id: profileB,
            }),
            account({
              id: freshId, label: 'Fresh subscription', plan: 'Pro', remaining_fraction: null, windows: [unmeasured],
              models: [{ model: 'anthropic/claude-opus-5', family: 'anthropic', efforts: [{ effort: 'xhigh', model_profile_id: profileA, version: '2' }] }],
              default_model_profile_id: profileA,
            }),
          ],
        }],
      }],
    }
    const known = presented(nested)
    expect(known.filled.accountId).toBe(knownId)
    expect(known.view.notes.account).toContain('most allowance left')
    expect(known.view.notes.account).toContain('30%')
    expect(known.view.accounts.find(item => item.value === freshId)?.label).toContain('allowance unknown')
    expect(known.view.accounts.find(item => item.value === freshId)?.label).not.toContain('100%')
    const mixed = account({
      remaining_fraction: null,
      windows: [measured, unmeasured],
    })
    expect(presented(catalog([mixed])).view.notes.account).toContain('Allowance is unknown.')
    expect(presented(catalog([mixed])).view.notes.account).not.toMatch(/100%|30%/)
    const inflated = structuredClone(nested)
    inflated.hosts[0].harnesses[0].default_account_id = null
    inflated.hosts[0].harnesses[0].accounts[1].remaining_fraction = 1
    const unranked = presented(inflated)
    expect(unranked.filled.accountId).toBe('')
    expect(unranked.view.notes.account).not.toContain('most allowance left')
    expect(unranked.view.accounts.find(item => item.value === freshId)?.label).toContain('allowance unknown')
    expect(unranked.view.accounts.find(item => item.value === freshId)?.label).not.toContain('100%')
    const onlyFresh = presented(catalog([account({ remaining_fraction: 1, windows: [unmeasured] })]))
    expect(onlyFresh.view.notes.account).toContain('Allowance is unknown.')
    expect(onlyFresh.view.notes.account).not.toContain('most allowance left')
    expect(onlyFresh.view.notes.account).not.toContain('100%')
    const unread = account({ id: accountA, label: 'Unread', remaining_fraction: null, windows: [{ ...window(5), provisional: true }] })
    const blocked = account({ id: accountB, available: false, remaining_fraction: null, unavailable_reasons: ['allowance'] })
    const onlyRunnable: AgentAccountCatalog = {
      as_of: new Date(NOW).toISOString(),
      role: 'build',
      hosts: [{
        daemon_id: 'workstation',
        label: 'Work Mac',
        harnesses: [
          { harness: 'codex', default_account_id: null, accounts: [blocked] },
          { harness: 'claude', default_account_id: accountA, accounts: [unread] },
        ],
      }],
    }
    const picked = presented(onlyRunnable)
    expect(picked.filled.harness).toBe('claude')
    expect(picked.filled.accountId).toBe(accountA)
    const several = structuredClone(onlyRunnable)
    several.hosts[0].harnesses[0].default_account_id = accountB
    several.hosts[0].harnesses[0].accounts[0].available = true
    several.hosts[0].harnesses[0].accounts[0].remaining_fraction = null
    const unselected = presented(several)
    expect(unselected.filled.harness).toBe('')
    expect(unselected.filled.accountId).toBe('')
  })

  it('labels the requested snapshot and reports a different or unknown session separately', () => {
    const requested = { host: 'Studio', harness: 'Pi', account: 'Measured subscription · Pro', model: 'anthropic/claude-sonnet-5', thinking: 'High' }
    expect(sessionReport({ model: 'anthropic/claude-sonnet-5', account_label: 'Measured subscription', reasoning_effort: 'high' }, requested).differs).toBe(false)
    const different = sessionReport({ model: 'anthropic/claude-opus-5', account_label: 'Fresh subscription', reasoning_effort: 'xhigh' }, requested)
    expect(different).toMatchObject({ model: 'anthropic/claude-opus-5', account: 'Fresh subscription', thinking: 'Extra high', differs: true })
    const unknown = sessionReport({ model: null, account_label: 'CODEX_HOME=/secret', reasoning_effort: null }, requested)
    expect(unknown).toMatchObject({ model: 'Unknown', account: 'Unknown', thinking: 'Unknown', modelKnown: false, accountKnown: false, differs: false })
  })
})
