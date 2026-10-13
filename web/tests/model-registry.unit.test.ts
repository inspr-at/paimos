// SPDX-License-Identifier: AGPL-3.0-only
// Risks: a hand-added model is stored under a route the server refuses; the registry shows a
// level order, state or "taken on" the data does not support; a partial write is reported as saved.
import { beforeEach, describe, expect, it, vi } from 'vitest'
const http = vi.hoisted(() => ({ calls: [] as { method: string; path: string; body: unknown }[], signals: [] as (AbortSignal | undefined)[], handle: (_method: string, _path: string, _body: unknown): Response => new Response('{}') }))
vi.mock('../src/lib/api.ts', () => ({
  api: async (path: string, init: RequestInit = {}) => {
    const body = typeof init.body === 'string' ? JSON.parse(init.body) : undefined
    http.calls.push({ method: init.method ?? 'GET', path, body })
    http.signals.push(init.signal ?? undefined)
    return http.handle(init.method ?? 'GET', path, body)
  },
}))
import { StaleScopeError } from '../src/lib/identityScope'
import { buildEntries } from '../src/lib/modelsSimple'
import { LEVEL, manyRoutes } from './fixtures/registry-many'
import { RegistryError, addLine, getRefreshStatus, listProfiles, buildLines, changeOf, checkReport, cooldownLabel, draftOf, emptyDraft, familyOf, intervalLabel, lastChecked, lineWriteOf, profileSource, modelSlug, removalText, removeLine, restoreLine, routeOf, routesFor, shortDate, splitLevels, takenText, useText, validateDraft, checkNow, lifecycleSource, outdatedBatch, outdatedEntries, outdatedTray, outdatedUseText, outdatedWhy, readOutdated, readOutdatedBatch, refused, retireOutdated, undoOutdated, OUTDATED_LIMIT, OUTDATED_PAGES, type LineDraft, type LineUsage, type OutdatedDocument, type OutdatedVersion, type RegistryProfile, type WriteScope, buildRegistry, registryCount, pauseUntil, pauseOptions, untilText, pauseTag, pauseModel, undoPause, previewRemoval, atLevel, lifecycleText, olderWhy, pauseTargets, PAUSED_REASON, type RegistryModel } from '../src/lib/modelRegistry'

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })
let counter = 0
const profile = (over: Partial<RegistryProfile> & Pick<RegistryProfile, 'harness' | 'model' | 'effort'>): RegistryProfile => ({
  id: `id-${++counter}`, slug: `s${counter}`, version: '1', family: 'openai', tier: 'strong', enabled: true, created_at: '2026-09-20T12:00:00Z', source: 'auto', retire_at: null, retired: false, effort_level: null, ...over,
})
beforeEach(() => { http.calls.length = 0; http.signals.length = 0; http.handle = () => json({}) })

describe('registry lines', () => {
  const sol = (effort: string, level: number, extra: Partial<RegistryProfile> = {}) => profile({ harness: 'codex', model: 'gpt-6.1-sol', effort, effort_level: level, display_name: 'GPT Sol', model_version: '6.1', ...extra })
  it('groups profiles into one line per model, levels in the model\'s own order, retired profiles left out', () => {
    const lines = buildLines([sol('xhigh', 4), sol('low', 1), sol('high', 3), profile({ harness: 'codex', model: 'gpt-6.1-sol', effort: 'medium', effort_level: 2, retired: true }), sol('medium', 2)])
    expect(lines).toHaveLength(1)
    expect(lines[0]).toMatchObject({ name: 'GPT-6.1 Sol', displayName: 'GPT Sol', efforts: ['low', 'medium', 'high', 'xhigh'], route: 'openai', source: 'auto', isNew: false })
  })
  it('names a level whose effort level is unknown after those that have one, and a missing display name falls back to the model id', () => {
    const [line] = buildLines([profile({ harness: 'cursor', model: 'composer-2.5', effort: 'default', family: 'cursor' }), profile({ harness: 'cursor', model: 'composer-2.5', effort: 'fast', effort_level: 1, family: 'cursor' })])
    expect(line).toMatchObject({ name: 'composer-2.5', efforts: ['fast', 'default'] })
  })
  it('marks a discovered, disabled line New and shows a scheduled retirement, never both', () => {
    const lines = buildLines([profile({ harness: 'grok', model: 'grok-4.8-preview', effort: 'high', family: 'xai', enabled: false }), profile({ harness: 'codex', model: 'gpt-6-luna', effort: 'low', retire_at: '2026-10-31T12:00:00Z' })])
    expect(lines.find(line => line.model === 'grok-4.8-preview')).toMatchObject({ isNew: true, retireAt: null })
    expect(lines.find(line => line.model === 'gpt-6-luna')).toMatchObject({ isNew: false, retireAt: '2026-10-31T12:00:00Z' })
    expect(buildLines([profile({ harness: 'pi', model: 'openrouter/x/y', effort: 'off', enabled: false, source: 'manual', family: 'unknown' })])[0]!.isNew).toBe(false)
  })
  it('says which version a taken-on line replaced only when the data shows one', () => {
    const now = new Date('2026-10-09T10:00:00Z')
    const taken = [sol('high', 3, { version: '2026-10-03-accepted', created_at: '2026-10-03T12:00:00Z' }), profile({ harness: 'codex', model: 'gpt-6-sol', effort: 'high', display_name: 'GPT Sol', model_version: '6', retired: true })]
    expect(takenText(buildLines(taken)[0]!, now)).toBe('was 6, taken on 3 Oct')
    expect(takenText(buildLines([taken[0]!])[0]!, now)).toBe('taken on 3 Oct')
    expect(buildLines([sol('high', 3)])[0]!.took).toBeNull()
  })
  it('orders harnesses as the page does, newest version first and then by name', () => {
    const make = (harness: 'codex' | 'claude', model: string, name: string) => profile({ harness, model, effort: 'high', display_name: name, family: harness === 'claude' ? 'anthropic' : 'openai' })
    const lines = buildLines([make('claude', 'claude-opus-5-5', 'Opus'), make('codex', 'gpt-6-luna', 'Luna'), make('codex', 'gpt-6.1-sol', 'Sol'), make('codex', 'gpt-6-astra', 'Astra')])
    expect(lines.map(line => line.model)).toEqual(['gpt-6.1-sol', 'gpt-6-astra', 'gpt-6-luna', 'claude-opus-5-5'])
  })
  // AEON-1163. Risk: a source is guessed ("Auto-discovered") where the server named none.
  it('names a source only when the server does: legacy auto stays unknown, a hand-added line says so', () => {
    const line = buildLines([sol('high', 3)])[0]!
    expect(line.origin).toBeUndefined()
    expect(profileSource(line)).toBeNull()
    expect(profileSource({ ...line, origin: '' })).toBeNull()
    expect(profileSource({ ...line, source: 'manual' })).toBe('manual')
    for (const origin of ['shipped', 'provider', 'harness', 'manual'] as const) expect(profileSource({ ...line, origin })).toBe(origin)
  })
})

describe('routes and families', () => {
  it('derives the provider the server compares with the model\'s own namespace', () => {
    expect([routeOf('codex', 'gpt-6'), routeOf('claude', 'x'), routeOf('grok', 'x'), routeOf('gemini', 'x'), routeOf('cursor', 'x')]).toEqual(['openai', 'anthropic', 'xai', 'google', 'cursor'])
    expect([routeOf('pi', 'openrouter/qwen/qwen3-coder'), routeOf('opencode', 'ollama/qwen3'), routeOf('pi', 'claude-opus')]).toEqual(['openrouter', 'ollama', 'unknown'])
    expect(routesFor('pi')).toEqual(['openrouter', 'anthropic', 'openai', 'ollama'])
    expect(routesFor('opencode')).toEqual(['openrouter', 'ollama'])
    expect(routesFor('gemini')).toEqual(['google'])
  })
  it('mirrors the server\'s family rule, so an unknown OpenRouter family is allowed for pi only through "unknown"', () => {
    expect([familyOf('pi', 'openrouter/qwen/qwen3-coder'), familyOf('pi', 'anthropic/claude-x'), familyOf('opencode', 'ollama/qwen3'), familyOf('pi', 'plain')]).toEqual(['unknown', 'anthropic', 'local', 'unknown'])
    expect([familyOf('gemini', 'gemini-3-flash'), familyOf('cursor', 'composer-2.5'), familyOf('cursor', 'gpt-6'), familyOf('cursor', 'other-1')]).toEqual(['google', 'cursor', 'openai', 'unknown'])
  })
})

describe('the inline form', () => {
  const draft = (over: Partial<LineDraft> = {}): LineDraft => ({ ...emptyDraft(), name: 'Qwen3 Coder', slug: 'qwen/qwen3-coder', efforts: ['off'], ...over })
  it('puts the route in front of a pi or OpenCode slug once, in lower case', () => {
    expect(modelSlug(draft())).toBe('openrouter/qwen/qwen3-coder')
    expect(modelSlug(draft({ slug: 'openrouter/qwen/qwen3-coder' }))).toBe('openrouter/qwen/qwen3-coder')
    expect(modelSlug(draft({ slug: 'OpenRouter/qwen/qwen3-coder' }))).toBe('openrouter/qwen/qwen3-coder')
    expect(modelSlug(draft({ harness: 'opencode', route: 'ollama', slug: 'qwen3' }))).toBe('ollama/qwen3')
    expect(modelSlug(draft({ harness: 'codex', route: 'openai', slug: ' gpt-7 ' }))).toBe('gpt-7')
  })
  it('accepts an OpenRouter slug with or without its prefix and a variant, and refuses what the server would', () => {
    expect(validateDraft(draft(), 'new')).toEqual({})
    expect(validateDraft(draft({ slug: 'qwen/qwen3-coder:free' }), 'new')).toEqual({})
    expect(validateDraft(draft({ slug: 'qwen' }), 'new').slug).toBe('OpenRouter slugs look like openrouter/vendor/model.')
    expect(validateDraft(draft({ slug: 'bad slug' }), 'new').slug).toBe('Letters, digits and . _ : / - only.')
    expect(validateDraft(draft({ slug: '' }), 'new').slug).toBe('The model slug is needed.')
    expect(validateDraft(draft({ slug: 'sk-live-abc/def' }), 'new').slug).toBe('That looks like a key, not a model slug.')
    expect(validateDraft(draft({ slug: `a/${'b'.repeat(130)}` }), 'new').slug).toBe('Use at most 128 characters.')
  })
  it('refuses a slug that belongs to another route instead of storing it under the wrong provider', () => {
    expect(validateDraft(draft({ route: 'anthropic', slug: 'openrouter/qwen/x' }), 'new').slug).toBe('This slug belongs to OpenRouter; choose that route.')
    expect(validateDraft(draft({ route: 'anthropic', slug: 'claude-haiku-5' }), 'new')).toEqual({})
    expect(validateDraft(draft({ route: 'openrouter', slug: 'anthropic/claude-haiku-5' }), 'new')).toEqual({})
  })
  it('needs a name and at least one level of valid, distinct, bounded names; a level still being typed counts', () => {
    expect(validateDraft(draft({ name: '  ', efforts: [] }), 'new')).toEqual({ name: 'Give it a name.', efforts: 'Add at least one level (e.g. off).' })
    expect(validateDraft(draft({ efforts: [], level: 'low, high' }), 'new')).toEqual({})
    expect(validateDraft(draft({ efforts: ['bad name'] }), 'new').efforts).toBe('Level names use letters, digits and . _ : - only.')
    expect(validateDraft(draft({ efforts: Array.from({ length: 17 }, (_, i) => `l${i}`) }), 'new').efforts).toBe('Use at most 16 levels.')
    expect(splitLevels('low, high  max low', ['off'])).toEqual(['off', 'low', 'high', 'max'])
  })
  it('does not check the slug of an auto-discovered entry, which cannot change', () => {
    expect(validateDraft(draft({ slug: '' }), 'auto')).toEqual({})
  })
  it('writes only what the person changed, keeping an auto entry\'s model and route', () => {
    const line = buildLines([profile({ harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', effort_level: 3, display_name: 'GPT Sol', model_version: '6.1', note: 'a' })])[0]!
    const same = lineWriteOf(draftOf(line), line)
    expect(same).toEqual({ display_name: 'GPT Sol', note: 'a', efforts: ['high'], route: 'openai' })
    expect(changeOf(line, same)).toBe(false)
    expect(changeOf(line, lineWriteOf({ ...draftOf(line), note: 'b' }, line))).toBe(true)
    expect(changeOf(line, lineWriteOf({ ...draftOf(line), efforts: ['high', 'max'] }, line))).toBe(true)
    // An auto entry's slug is never sent, whatever the form holds.
    expect(lineWriteOf({ ...draftOf(line), slug: 'gpt-9' }, line).model).toBeUndefined()
    const hand = buildLines([profile({ harness: 'pi', model: 'openrouter/qwen/x', effort: 'off', source: 'manual', family: 'unknown', display_name: 'Qwen' })])[0]!
    expect(lineWriteOf({ ...draftOf(hand), route: 'anthropic', slug: 'anthropic/claude-haiku-5' }, hand)).toMatchObject({ route: 'anthropic', model: 'anthropic/claude-haiku-5' })
  })
})

describe('writes', () => {
  const draft = (over: Partial<LineDraft> = {}): LineDraft => ({ ...emptyDraft(), name: 'Qwen3 Coder', slug: 'qwen/qwen3-coder', efforts: ['off'], note: 'trial', ...over })
  it('adds a pi OpenRouter model as one profile with the slug in model, the family unknown and the tier standard', async () => {
    http.handle = (_m, _p, body) => json({ ...(body as object), id: 'new', enabled: true, created_at: '2026-10-09T08:00:00Z', source: 'manual' }, 201)
    const result = await addLine(draft(), new Date('2026-10-09T08:00:00.123Z'))
    expect(result.failed).toBeNull()
    expect(http.calls).toHaveLength(1)
    expect(http.calls[0]).toEqual({ method: 'POST', path: '/models', body: {
      slug: 'hand-pi-openrouter-qwen-qwen3-coder-off', version: '20261009T080000.123', harness: 'pi', family: 'unknown', model: 'openrouter/qwen/qwen3-coder', effort: 'off', tier: 'standard',
      display_name: 'Qwen3 Coder', short_name: 'Qwen3 Coder', note: 'trial',
    } })
    expect(http.calls[0]!.body).toSatisfy((body: { slug: string }) => /^[a-z][a-z0-9_-]*$/.test(body.slug))
  })
  it('registers several levels in the person\'s order with one more write, and says so when that write fails', async () => {
    http.handle = (method, path, body) => method === 'POST' ? json({ ...(body as object), id: 'first', enabled: true, created_at: '2026-10-09T08:00:00Z', source: 'manual' }, 201) : path.includes('/lines/') ? json({ error: 'provider route does not match model namespace' }, 422) : json({})
    const result = await addLine(draft({ harness: 'opencode', route: 'ollama', slug: 'qwen3', efforts: [], level: 'low, high' }))
    expect(http.calls.map(call => `${call.method} ${call.path}`)).toEqual(['POST /models', 'PUT /models/lines/opencode/ollama%2Fqwen3'])
    expect(http.calls[1]!.body).toEqual({ display_name: 'Qwen3 Coder', note: 'trial', efforts: ['low', 'high'], route: 'ollama' })
    expect(result.failed).toBe('provider route does not match model namespace')
    expect(result.profiles.map(item => item.id)).toEqual(['first'])
  })
  it('never swallows a refused first write', async () => {
    http.handle = () => json({ error: 'invalid harness, family or tier' }, 400)
    await expect(addLine(draft())).rejects.toMatchObject({ status: 400, message: 'invalid harness, family or tier' })
    expect(http.calls).toHaveLength(1)
  })
  it('removes every level one by one and stops at the first refusal, saying how far it got', async () => {
    http.handle = (_m, path) => path.includes('c') ? json({ error: 'retire failed' }, 500) : json({ retired: true })
    const result = await removeLine(['a1', 'b2', 'c3', 'd4'])
    expect(result).toEqual({ done: ['a1', 'b2'], error: 'retire failed' })
    expect(http.calls.map(call => call.path)).toEqual(['/models/a1/retire', '/models/b2/retire', '/models/c3/retire'])
    expect(http.calls[0]!.body).toEqual({ reason: 'Removed in Settings › Models' })
  })
  it('restores levels and reports how many could not come back', async () => {
    http.handle = (_m, path) => path.includes('b2') ? json({ error: 'gone' }, 404) : json({ retired: false })
    expect(await restoreLine(['a1', 'b2', 'c3'])).toBe('1 of 3 levels could not be restored')
    expect(await restoreLine(['a1'])).toBeNull()
  })
  it('stops the next request when the captured person or workspace is gone', async () => {
    const ended = () => {
      const controller = new AbortController()
      let live = true
      const scope: WriteScope = { signal: controller.signal, live: () => live }
      return { scope, kill() { live = false; controller.abort() } }
    }
    const several: LineDraft = { ...emptyDraft(), name: 'Qwen3 Coder', slug: 'qwen3', harness: 'opencode', route: 'ollama', efforts: [], level: 'low, high', note: 'trial' }
    const adding = ended()
    http.handle = () => { adding.kill(); return json({ id: 'first', enabled: true, created_at: '2026-10-09T08:00:00Z', source: 'manual', harness: 'opencode', model: 'ollama/qwen3', effort: 'low' }, 201) }
    await expect(addLine(several, new Date('2026-10-09T08:00:00Z'), adding.scope)).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls).toHaveLength(1)
    expect(http.signals[0]).toBe(adding.scope.signal)
    http.calls.length = 0; http.signals.length = 0
    await expect(addLine(several, new Date('2026-10-09T08:00:00Z'), { live: () => false })).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls).toHaveLength(0)

    const retire = ended()
    http.handle = () => { retire.kill(); return json({ retired: true }) }
    await expect(removeLine(['a1', 'b2'], 'Removed in Settings › Models', retire.scope)).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls.map(call => call.path)).toEqual(['/models/a1/retire'])
    expect(http.signals[0]).toBe(retire.scope.signal)
    expect(http.calls[0]!.body).toEqual({ reason: 'Removed in Settings › Models' })
    http.calls.length = 0
    await expect(removeLine(['a1'], 'Removed in Settings › Models', { signal: AbortSignal.abort() })).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls).toHaveLength(0)

    const restore = ended()
    http.calls.length = 0; http.signals.length = 0
    http.handle = () => { restore.kill(); return json({ retired: false }) }
    await expect(restoreLine(['a1', 'b2'], restore.scope)).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls).toHaveLength(1)
    expect(http.signals[0]).toBe(restore.scope.signal)
    http.calls.length = 0
    await expect(restoreLine(['a1'], { live: () => false })).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls).toHaveLength(0)
  })
  it('carries the server\'s remaining wait from a 429, from the body or the Retry-After header', async () => {
    http.handle = () => json({ error: 'model refresh cooldown', retry_after: 120 }, 429, { 'Retry-After': '120' })
    await expect(checkNow()).rejects.toMatchObject({ status: 429, retryAfter: 120 })
    http.handle = () => json({ error: 'model refresh cooldown' }, 429, { 'Retry-After': '45' })
    await expect(checkNow()).rejects.toBeInstanceOf(RegistryError)
    http.handle = () => json({ error: 'model refresh cooldown' }, 429, { 'Retry-After': '45' })
    await expect(checkNow()).rejects.toMatchObject({ retryAfter: 45 })
    http.handle = () => json({ error: 'boom' }, 500, { 'Retry-After': '45' })
    await expect(checkNow()).rejects.toMatchObject({ status: 500, retryAfter: null })
  })
})

describe('answers that are not what the page expects', () => {
  it('is an error, not a crash, when the list or the refresh status has the wrong shape', async () => {
    http.handle = () => json({ revision: 3, profile: {} })
    await expect(listProfiles()).rejects.toMatchObject({ status: 502 })
    await expect(getRefreshStatus()).rejects.toMatchObject({ status: 502 })
    http.handle = () => json(Array.from({ length: 2049 }, () => ({})))
    await expect(listProfiles()).rejects.toMatchObject({ status: 502 })
    http.handle = () => json([])
    await expect(listProfiles()).resolves.toEqual([])
  })
})

describe('copy', () => {
  it('writes dates, times, intervals and waits the way the page shows them', () => {
    const now = new Date(2026, 9, 9, 12, 0)
    expect(shortDate(new Date(2026, 9, 3, 12).toISOString(), now)).toBe('3 Oct')
    expect(shortDate(new Date(2025, 11, 24, 12).toISOString(), now)).toBe('24 Dec 2025')
    expect(lastChecked(new Date(2026, 9, 9, 6, 12).toISOString(), now)).toBe('06:12')
    expect(lastChecked(new Date(2026, 9, 3, 6, 12).toISOString(), now)).toBe('3 Oct, 06:12')
    expect(lastChecked(null, now)).toBe('')
    expect([intervalLabel(360), intervalLabel(1440), intervalLabel(90)]).toEqual(['6 h', '24 h', '90 min'])
    expect([cooldownLabel(300), cooldownLabel(61), cooldownLabel(59), cooldownLabel(0)]).toEqual(['again in 5 min', 'again in 2 min', 'again in 1 min', 'again in 1 min'])
  })
  it('names each visible use by its own replacement and effort, and a null one is no qualified fallback', () => {
    const usage: LineUsage = {
      revision: 'shown', incomplete: false,
      replacement: { line: 'decoy', effort: 'max', harness: 'codex', model: 'gpt-decoy' },
      used_by: [
        { column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } },
        { column: 'backend', layer: 'workspace', replacement: { line: null, effort: null } },
        { column: 'concept', layer: 'workspace', replacement: { line: 'anthropic:opus', effort: null, harness: 'claude', model: 'claude-opus-5-5' } },
      ],
    }
    const text = removalText('GPT Sol 6.1', 'auto', usage, { backend: 'Backend build' }, 'me', pick => pick.model === 'gpt-6-astra' ? 'GPT Astra 6' : pick.model === 'claude-opus-5-5' ? 'Claude Opus 5.5' : pick.model ?? pick.line ?? '')
    expect(text).toBe('GPT Sol 6.1 is in use: Default · all work (for everyone) — GPT Astra 6 at xhigh takes over; Backend build (for everyone) — no qualified fallback; Concepts (for everyone) — Claude Opus 5.5 takes over, with no effort named. Auto-update won’t add it back.')
    expect(text).not.toContain('The next model in line')
    expect(text).not.toContain('gpt-decoy')
    expect(text).not.toContain('takes over there')
  })
  it('separates a partial or stale discovery from a new model and from a version taken on', () => {
    expect(checkReport({ sources: [{ state: 'stale' }] }, true)).toBe('Discovery was incomplete: some model lists could not be checked')
    expect(checkReport({ sources: [{ state: 'limited' }] }, true)).toBe('Discovery was partial: not every model could be read')
    expect(checkReport({ sources: [{ state: 'limited' }, { state: 'stale' }] }, true)).toBe('Discovery was partial: some lists were incomplete and some could not be checked')
    expect(checkReport({ added: 2 }, true)).toBe('2 profiles were added')
    expect(checkReport({ added: 1 }, true)).toBe('1 profile was added')
    expect(checkReport({ added: 2 }, true)).not.toContain('taken on')
    expect(checkReport({ added: 1 }, true)).not.toContain('newer version')
    expect(checkReport({ new_lines: ['openai:gpt-7'], added: 1 }, true)).toBe('1 new model found')
    expect(checkReport({ new_lines: ['openai:gpt-7'], added: 1, sources: [{ state: 'limited' }] }, true)).toBe('1 new model found. Discovery was partial: not every model could be read')
    expect(checkReport({ new_lines: ['a', 'b'], sources: [{ state: 'stale' }] }, true)).toBe('2 new models found. Discovery was incomplete: some model lists could not be checked')
    expect(checkReport({}, false)).toBe('Checked · vendor model lists are off, so nothing new could be found')
    expect(checkReport({ sources: [{ state: 'fresh' }] }, true)).toBe('Up to date · nothing new')
    expect(checkReport({ new_lines: ['openai:gpt-7'], added: 1 }, true)).not.toContain('taken on')
    expect(checkReport({ added: 2, sources: [{ state: 'stale' }] }, true)).not.toContain('Up to date')
    expect(checkReport({ added: 2, sources: [{ state: 'stale' }] }, true)).toBe('2 profiles were added. Discovery was incomplete: some model lists could not be checked')
  })
  it('does not call a profile addition accepted, and counts one accepted model once however many efforts it has', () => {
    expect(checkReport({ added: 4 }, true)).toBe('4 profiles were added')
    expect(checkReport({ added: 4 }, true)).not.toContain('taken on')
    expect(checkReport({ added: 4 }, true)).not.toContain('newer version')
    expect(checkReport({ added: 4 }, true)).not.toContain('in use')
    expect(checkReport({ added: 4, accepted_models: [] }, true)).toBe('4 profiles were added')
    expect(checkReport({ added: 4, accepted_models: ['openai:sol', 'openai:sol'] }, true)).toBe('A newer version already in use was taken on')
    expect(checkReport({ added: 4, accepted_models: ['openai:sol'] }, true)).not.toContain('4')
    expect(checkReport({ added: 4, accepted_models: ['openai:sol', 'openai:astra'] }, true)).toBe('2 newer versions already in use were taken on')
    expect(checkReport({ added: 4, accepted_models: ['openai:sol', 'openai:astra'] }, true)).not.toContain('4 newer')
  })
  it('names where a model is used, and who for', () => {
    const kinds = { backend: 'Backend build' }
    expect(useText({ column: 'other', layer: 'default', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('Default · all work (for everyone)')
    expect(useText({ column: 'backend', layer: 'person', person: 'me', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('Backend build (yours)')
    expect(useText({ column: 'docs', layer: 'person', person: 'someone', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('docs (another person)')
    expect(useText({ column: 'review:openai', layer: 'workspace', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('Reviews (for everyone)')
    // A situation is named as the outdated sheet names it (AEON-1163); the first build is the row itself.
    expect(useText({ column: 'other', layer: 'default', situation: 'review', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('Review · Default (for everyone)')
    expect(useText({ column: 'backend', layer: 'person', person: 'me', situation: 'small', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('First build · Backend build (yours)')
    expect(useText({ column: 'backend', layer: 'default', situation: 'first', replacement: { line: null, effort: null } }, kinds, 'me')).toBe('Backend build (for everyone)')
  })
})

// AEON-1164. Risks: the tray offers a version whose uses cannot move (no runnable successor),
// the sheet shows server codes or identities instead of words, and "nothing uses them" is said
// when uses are only hidden from this person.
describe('old versions', () => {
  const REVISION = 'a'.repeat(64)
  const old = (over: Partial<OutdatedVersion> & Pick<OutdatedVersion, 'version' | 'successor'>): OutdatedVersion => ({
    line: 'openai:gpt-sol', state: 'superseded', date: null, reason: 'Source reports a successor', source: 'agent-report:codex', uses: [], incomplete: false, ...over,
  })
  const doc = (versions: OutdatedVersion[], over: Partial<OutdatedDocument> = {}): OutdatedDocument => ({ versions, revision: REVISION, next_cursor: '', incomplete: false, ...over })
  const registry = [
    profile({ harness: 'codex', model: 'gpt-6-sol', effort: 'high', identity: 'openai:gpt-6-sol', display_name: 'GPT Sol', model_version: '6' }),
    profile({ harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', identity: 'openai:gpt-6.1-sol', display_name: 'GPT Sol', model_version: '6.1' }),
    profile({ harness: 'pi', model: 'anthropic/claude-opus-5', effort: 'xhigh', family: 'anthropic', identity: 'anthropic:claude-opus-5', display_name: 'Claude Opus', model_version: '5' }),
    profile({ harness: 'claude', model: 'claude-opus-5-5', effort: 'xhigh', family: 'anthropic', identity: 'anthropic:claude-opus-5-5', display_name: 'Claude Opus', model_version: '5.5' }),
    profile({ harness: 'grok', model: 'grok-4.5', effort: 'high', family: 'xai', identity: 'xai:grok-4.5', display_name: 'Grok', model_version: '4.5' }),
    profile({ harness: 'grok', model: 'grok-4.7', effort: 'high', family: 'xai', identity: 'xai:grok-4.7', display_name: 'Grok', model_version: '4.7', enabled: false }),
    profile({ harness: 'gemini', model: 'gemini-2.5-pro', effort: 'high', family: 'google', identity: 'google:gemini-2.5-pro', display_name: 'Gemini Pro', model_version: '2.5' }),
  ]
  const use = (label: string, who = 'Default', from = 'openai:gpt-6-sol', to: string | null = 'openai:gpt-6.1-sol') => ({ label, who, from, to })

  it('reads one bounded page and rejects an answer that is not the outdated list', async () => {
    http.handle = () => json(doc([old({ version: 'openai:gpt-6-sol', successor: 'openai:gpt-6.1-sol' })]))
    await expect(readOutdated()).resolves.toMatchObject({ revision: REVISION })
    expect(http.calls.at(-1)).toMatchObject({ method: 'GET', path: '/models/outdated?limit=50' })
    for (const wrong of [{}, { versions: [], revision: 'short' }, doc([{ version: 'x' } as OutdatedVersion]), doc([old({ version: 'x', successor: null, uses: [{ label: 1 } as never] })])]) {
      http.handle = () => json(wrong)
      await expect(readOutdated()).rejects.toMatchObject({ status: 502 })
    }
  })
  it('retires the versions at the revision read with them and undoes with the token alone', async () => {
    http.handle = () => json({ moved: [], removed: ['openai:gpt-6-sol'], undo_token: '7.abc' })
    await retireOutdated(['openai:gpt-6-sol'], REVISION)
    expect(http.calls.at(-1)).toEqual({ method: 'POST', path: '/models/outdated/retire', body: { versions: ['openai:gpt-6-sol'], revision: REVISION } })
    http.handle = () => json({ event_id: 8, restored: true })
    await undoOutdated('7.abc')
    expect(http.calls.at(-1)).toEqual({ method: 'POST', path: '/models/outdated/undo', body: { undo_token: '7.abc' } })
  })
  // AEON-1164 fix2. Risk: old versions without a usable successor fill the first page, so the tray stays hidden
  // although later pages hold versions that can move; or the walk mixes pages read at different revisions.
  it('reads on past pages whose versions cannot move, at the first page\'s revision, and stops at a full batch, a changed revision, an unreadable page or the page bound', async () => {
    const stuck = (prefix: string, n: number) => Array.from({ length: n }, (_, i) => old({ version: `${prefix}:m-${String(i).padStart(3, '0')}`, successor: `${prefix}:unregistered` }))
    const movable = (prefix: string, n: number) => Array.from({ length: n }, (_, i) => old({ version: `${prefix}:m-${String(i).padStart(3, '0')}`, successor: 'openai:gpt-6.1-sol' }))
    // The server's keyset page: versions after the cursor in identity order, the last one as the next cursor while more remain.
    const pager = (versions: OutdatedVersion[], answer: (page: number) => { revision?: string; status?: number } = () => ({})) => (_method: string, path: string) => {
      const url = new URL(path, 'http://registry'), after = url.searchParams.get('cursor') ?? '', limit = Number(url.searchParams.get('limit'))
      const { revision = REVISION, status } = answer(http.calls.length)
      if (status) return json({ error: 'unavailable' }, status)
      const rest = versions.filter(version => version.version > after), items = rest.slice(0, limit)
      return json(doc(items, { revision, next_cursor: rest.length > limit ? items.at(-1)!.version : '' }))
    }
    const walk = async () => readOutdatedBatch(await readOutdated(), registry)
    const reads = () => http.calls.map(call => call.path)

    // A first page of versions that cannot move, then one that can: the walk reaches it, bound to the first revision.
    http.handle = pager([...stuck('aaa', 50), old({ version: 'openai:gpt-6-sol', successor: 'openai:gpt-6.1-sol' })])
    let read = await walk()
    expect(reads()).toEqual(['/models/outdated?limit=50', '/models/outdated?limit=50&cursor=aaa%3Am-049'])
    expect(read).toMatchObject({ revision: REVISION, next_cursor: '' })
    expect(outdatedBatch(read, registry)).toMatchObject({ entries: [{ version: 'openai:gpt-6-sol' }], more: false })

    // A later page read at another revision is not mixed in; the cursor left over says more exist.
    http.calls.length = 0
    http.handle = pager([...stuck('aaa', 50), old({ version: 'openai:gpt-6-sol', successor: 'openai:gpt-6.1-sol' })], page => page > 1 ? { revision: 'b'.repeat(64) } : {})
    read = await walk()
    expect(read).toMatchObject({ revision: REVISION, next_cursor: 'aaa:m-049' })
    expect(read.versions).toHaveLength(50)
    expect(outdatedBatch(read, registry)).toEqual({ entries: [], more: true })

    // A later page that cannot be read ends the walk the same way, without failing the pages already read.
    http.calls.length = 0
    http.handle = pager([...stuck('aaa', 50), old({ version: 'openai:gpt-6-sol', successor: 'openai:gpt-6.1-sol' })], page => page > 1 ? { status: 503 } : {})
    read = await walk()
    expect(read).toMatchObject({ revision: REVISION, next_cursor: 'aaa:m-049' })
    expect(reads()).toHaveLength(2)

    // The walk is bounded: eleven pages reach every version a registry of 512 profiles can hold, and no more are read.
    http.calls.length = 0
    http.handle = pager(stuck('aaa', 600))
    read = await walk()
    expect(reads()).toHaveLength(OUTDATED_PAGES)
    expect(read.versions).toHaveLength(OUTDATED_PAGES * OUTDATED_LIMIT)
    expect(read.next_cursor).not.toBe('')

    // A full batch ends the walk, and the sheet never offers more than the server retires in one step.
    http.calls.length = 0
    http.handle = pager([...movable('bbb', 20), ...stuck('ccc', 30), ...movable('ddd', 50)])
    read = await walk()
    expect(reads()).toHaveLength(2)
    const batch = outdatedBatch(read, registry)
    expect(batch.entries).toHaveLength(OUTDATED_LIMIT)
    expect(batch.entries.at(-1)!.version).toBe('ddd:m-029')
    expect(batch.more).toBe(true)
  })
  it('tells a refusal from an answer that never came: only a 4xx means nothing was written', async () => {
    expect(refused(new RegistryError(409, 'stale outdated preview'))).toBe(true)
    expect(refused(new RegistryError(403, 'forbidden'))).toBe(true)
    expect(refused(new RegistryError(502, 'bad gateway'))).toBe(false)
    expect(refused(new RegistryError(504, 'gateway timeout'))).toBe(false)
    expect(refused(new TypeError('Failed to fetch'))).toBe(false)
    // A success whose body is not the retirement result is not a confirmed step either.
    http.handle = () => json({ ok: true })
    const lost = await retireOutdated(['openai:gpt-6-sol'], REVISION).catch((error: unknown) => error)
    expect(lost).toBeInstanceOf(RegistryError)
    expect(refused(lost)).toBe(false)
  })
  it('offers only versions whose uses can move: a registered successor, and a runnable one when something uses the old version', () => {
    const entries = outdatedEntries(doc([
      old({ version: 'openai:gpt-6-sol', successor: 'openai:gpt-6.1-sol', uses: [use('Default · other · first')] }),
      old({ version: 'xai:grok-4.5', successor: 'xai:grok-4.7', uses: [use('fallback · other · first', 'Default', 'xai:grok-4.5', 'xai:grok-4.7')] }),
      old({ version: 'google:gemini-2.5-pro', successor: 'google:gemini-3-pro' }),
      old({ version: 'anthropic:claude-opus-5', successor: 'anthropic:claude-opus-5-5', state: 'deprecated', date: '2026-09-01T12:00:00Z', source: 'https://api.anthropic.com/v1/models' }),
      old({ version: 'openai:gpt-6.1-sol', successor: null }),
    ]), registry, new Date('2026-10-11T12:00:00Z'))
    expect(entries.map(entry => entry.version)).toEqual(['openai:gpt-6-sol', 'anthropic:claude-opus-5'])
    expect(entries[0]).toMatchObject({ name: 'GPT-6 Sol', successorName: 'GPT-6.1 Sol', harness: 'codex', routes: ['Codex · gpt-6-sol'], source: 'harness', why: 'Its source names GPT-6.1 Sol as the next version' })
    expect(entries[1]).toMatchObject({ name: 'Opus 5', successorName: 'Opus 5.5', routes: ['pi · anthropic/claude-opus-5'], source: 'provider', why: 'Deprecated since 1 Sep' })
  })
  it('says why a version is old in words, with a badge only for a named source', () => {
    const now = new Date('2026-10-11T12:00:00Z')
    expect(outdatedWhy(old({ version: 'a:b', successor: 'a:c', state: 'retiring', date: '2026-11-04T12:00:00Z' }), 'C', now)).toBe('Retires on 4 Nov')
    expect(outdatedWhy(old({ version: 'a:b', successor: 'a:c', state: 'deprecated', date: '2027-01-15T12:00:00Z' }), 'C', now)).toBe('Deprecated from 15 Jan 2027')
    expect(outdatedWhy(old({ version: 'a:b', successor: 'a:c', state: 'vanished' }), 'C', now)).toBe('No longer in its source’s model list')
    expect(outdatedWhy(old({ version: 'a:b', successor: 'a:c', reason: 'Same model line has a newer sourced version', source: 'aeon_model_line/aeon_model_version_newer' }), 'C', now)).toBe('Newer version in the same line')
    expect(lifecycleSource('aeon_model_line/aeon_model_version_newer')).toBeNull()
    expect(lifecycleSource('offline-bootstrap')).toBe('shipped')
    expect(lifecycleSource('')).toBeNull()
  })
  it('names the two successors that take over most uses, counts the rest, and says nothing uses them only when nothing is hidden', () => {
    const entry = (name: string, successorName: string, uses: number, incomplete = false) => ({ version: name, name, successor: successorName, successorName, harness: null, route: '', routes: [], why: '', source: null, uses: Array.from({ length: uses }, () => use('Default · other · first')), incomplete, targets: Array.from({ length: uses }, () => successorName) })
    const tray = outdatedTray([entry('Grok 4.5', 'Grok 4.7', 1), entry('GPT Sol 6', 'GPT Sol 6.1', 3), entry('Opus 5', 'Opus 5.5', 2)], false, false)
    expect(tray).toEqual({ named: [{ from: 'GPT Sol 6', to: 'GPT Sol 6.1' }, { from: 'Opus 5', to: 'Opus 5.5' }], count: '3\u00a0old versions', unused: '' })
    expect(outdatedTray([entry('Grok 4.5', 'Grok 4.7', 0), entry('Opus 5', 'Opus 5.5', 0)], false, false).unused).toBe(', nothing uses them')
    expect(outdatedTray([entry('Grok 4.5', 'Grok 4.7', 0)], false, false)).toMatchObject({ count: '1\u00a0old version', unused: ', nothing uses it' })
    expect(outdatedTray([entry('Grok 4.5', 'Grok 4.7', 0, true)], false, false).unused).toBe(', nothing you can see uses them')
    expect(outdatedTray([entry('Grok 4.5', 'Grok 4.7', 0)], false, true).unused).toBe(', nothing you can see uses them')
    expect(outdatedTray([entry('Grok 4.5', 'Grok 4.7', 1)], true, false).count).toBe('1+\u00a0old versions')
  })
  it('writes each use in words: the row, the person, and what a lock or rule does, never a server code', () => {
    const kinds = { backend: 'Backend build', design: 'UI design' }, names = new Map([['22222222-2222-4222-8222-222222222222', 'Mia']])
    const me = '11111111-1111-4111-8111-111111111111', mia = '22222222-2222-4222-8222-222222222222', other = '33333333-3333-4333-8333-333333333333'
    const words = (label: string, who = 'Default') => outdatedUseText(use(label, who), kinds, names, me)
    expect(words('Default · other · first')).toEqual({ title: 'Default · all work', who: 'for everyone' })
    expect(words('Except for · backend · first', mia)).toEqual({ title: 'Backend build', who: 'Mia' })
    expect(words('Default · other · first', me)).toEqual({ title: 'Default · all work', who: 'yours' })
    expect(words('Default · other · first', other)).toEqual({ title: 'Default · all work', who: 'another person' })
    expect(words('fallback · design · first')).toEqual({ title: 'Fallback for UI design', who: 'for everyone' })
    expect(words('fallback · other · first')).toEqual({ title: 'Fallback for the default', who: 'for everyone' })
    expect(words('Default · other · fix')).toEqual({ title: 'Fix rounds · Default', who: 'for everyone · situation' })
    expect(words('fallback · backend · stuck')).toEqual({ title: 'Fallback for Stuck · Backend build', who: 'for everyone · situation' })
    expect(words('never · design')).toEqual({ title: 'Never for UI design', who: 'for everyone · the rule moves with it' })
    expect(words('lock · top · design', mia)).toEqual({ title: 'UI design', who: 'locked; the lock moves with it · set by Mia' })
    expect(words('lock · bottom · other', '')).toEqual({ title: 'Fallback for the default', who: 'last step; it moves with it' })
    expect(words('lock · not · backend', me)).toEqual({ title: 'Never for Backend build', who: 'the rule moves with it · set by you' })
    expect(words('fallback · build')).toEqual({ title: 'Older fallback list · build', who: 'for everyone' })
    expect(words('pinned · complex')).toEqual({ title: 'Pinned pick · complex', who: 'for everyone' })
    expect(words('something new · x')).toEqual({ title: 'something new · x', who: 'for everyone' })
  })
})

// AEON-1163 · W4: the registry grouped provider → line → version, routes as sub-rows.
// Risks: ~35 routes read as ~35 rows again; Cursor's per-level ids show as separate models; the registry, the picker,
// the tray and the sheet call one model by different names; a route or fact claims a source nobody reported;
// a pause or its Undo touches the wrong ids or reports a partial change as done.
describe('the grouped registry', () => {
  const NOW = new Date('2026-10-11T12:00:00Z')
  const many = (): RegistryProfile[] => manyRoutes().flatMap(route => route.efforts.map(effort => profile({
    harness: route.harness, model: route.model, family: route.family, provider: route.family, effort, effort_level: LEVEL[effort] ?? null,
    display_name: route.display_name, short_name: route.short_name, model_version: route.model_version, origin: route.origin, source: route.source ?? 'auto',
    enabled: route.enabled ?? true, note: route.note, identity: route.identity, lifecycle: route.lifecycle ?? null, pause: route.pause ?? null,
  })))
  const rowOf = (registry: ReturnType<typeof buildRegistry>, key: string) => registry.groups.flatMap(group => group.rows).find(row => row.key === key)!

  it('shows about 35 routes as at most 20 rows, providers in order, with the count above the list', () => {
    const profiles = many(), ids = new Set(profiles.map(item => `${item.harness}/${item.model}`))
    expect(ids.size).toBe(36)
    const registry = buildRegistry(profiles, NOW)
    expect(registry.rows).toBeLessThanOrEqual(20)
    expect(registry.groups.map(group => group.label)).toEqual(['OpenAI', 'Anthropic', 'xAI', 'Google', 'Cursor', 'Local', 'Unknown provider'])
    expect(registry.groups.find(group => group.provider === 'openai')!.rows.map(row => row.model.name)).toEqual(['GPT-6.1 Sol', 'GPT-6.1 Astra', 'GPT-6 Luna', 'GPT-6 Terra'])
    expect(registryCount(registry)).toBe(`${registry.models} models · ${registry.routes} routes · ${registry.rows} lines`)
    expect(registry.routes).toBeLessThan(ids.size)
  })
  it('folds Cursor\'s per-level ids into one route: Grok 4.7 runs through Grok and Cursor, its fast variant is its own line', () => {
    const registry = buildRegistry(many(), NOW)
    const grok = rowOf(registry, 'xai:grok')
    expect(grok.model).toMatchObject({ name: 'Grok 4.7', versionLabel: '4.7' })
    expect(grok.model.routes.map(route => [route.harness, route.id, route.efforts, route.models.length])).toEqual([
      ['grok', 'grok-4.7', ['low', 'medium', 'high', 'xhigh'], 1],
      ['cursor', 'grok-4.7-‹level›', ['low', 'medium', 'high', 'xhigh'], 4],
    ])
    expect(grok.older.map(version => version.name)).toEqual(['Grok 4.6', 'Grok 4.5'])
    expect(rowOf(registry, 'xai:grok-fast').model.routes).toMatchObject([{ harness: 'cursor', id: 'grok-4.7-‹level›-fast', models: ['grok-4.7-low-fast', 'grok-4.7-medium-fast', 'grok-4.7-high-fast', 'grok-4.7-xhigh-fast'] }])
  })
  it('is one model per version across harnesses: Opus 5.5 through Claude Code, Cursor and pi, the alias as one more route, Opus 5 folded', () => {
    const opus = rowOf(buildRegistry(many(), NOW), 'anthropic:opus')
    expect(opus.model).toMatchObject({ name: 'Opus 5.5', versionLabel: '5.5', source: 'provider', note: 'best for design and concepts' })
    expect(opus.model.routes.map(route => `${route.harness} ${route.id}`)).toEqual(['claude claude-opus-5-5', 'claude opus', 'cursor claude-opus-5.5', 'pi anthropic/claude-opus-5-5'])
    expect(opus.model.routes.find(route => route.harness === 'pi')).toMatchObject({ via: 'Anthropic', source: 'harness', hand: false })
    expect(opus.older).toHaveLength(1)
    expect(opus.older[0]).toMatchObject({ name: 'Opus 5', old: true })
    expect(olderWhy(opus.older[0]!, opus.model.name, NOW)).toBe('Deprecated from 15 Jan 2027')
    expect(opus.older[0]!.routes[0]!.efforts).toEqual(['high', 'xhigh'])
  })
  it('keeps the version that runs on the row: a newer one that is off waits on a row of its own; the newest that is old moves into the fold', () => {
    const registry = buildRegistry([
      profile({ harness: 'codex', model: 'gpt-6.2-sol', effort: 'high', display_name: 'GPT Sol', model_version: '6.2', enabled: false, origin: 'provider' }),
      profile({ harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', display_name: 'GPT Sol', model_version: '6.1' }),
      profile({ harness: 'codex', model: 'gpt-6-sol', effort: 'high', display_name: 'GPT Sol', model_version: '6' }),
    ], NOW)
    const rows = registry.groups[0]!.rows
    expect(rows.map(row => [row.model.name, row.model.isNew, row.older.map(version => version.name)])).toEqual([['GPT-6.2 Sol', true, []], ['GPT-6.1 Sol', false, ['GPT-6 Sol']]])
  })
  it('says where each fact came from, and "unknown" (null) when nobody reported it; only hand-added routes are editable', () => {
    const registry = buildRegistry(many(), NOW)
    const qwen = rowOf(registry, 'unknown:openrouter/qwen/qwen3-coder')
    expect(qwen.model.routes).toMatchObject([{ harness: 'pi', via: 'OpenRouter', source: 'manual', hand: true }])
    expect(registry.groups.flatMap(group => group.rows).filter(row => row.model.routes.some(route => route.hand)).map(row => row.key)).toEqual(['unknown:openrouter/qwen/qwen3-coder'])
    const legacy = buildRegistry([profile({ harness: 'codex', model: 'gpt-6-luna', effort: 'high' })], NOW).groups[0]!.rows[0]!.model
    expect(legacy.source).toBeNull()
    expect(legacy.routes[0]!.source).toBeNull()
    expect(legacy.lifecycle).toBeNull()
    expect(lifecycleText(legacy.lifecycle, NOW)).toBeNull()
    const luna = rowOf(registry, 'openai:luna').model
    expect(luna.retireAt).toBe('2026-10-31T12:00:00Z')
    expect(lifecycleText(luna.lifecycle, NOW)).toBe('Retiring 31 Oct')
    expect(lifecycleText(rowOf(registry, 'openai:sol').model.lifecycle, NOW)).toBe('Active')
  })
  it('says token budgets once and names a level as the sheet does', () => {
    const pro = rowOf(buildRegistry(many(), NOW), 'google:gemini-pro').model
    expect(pro.routes.map(route => [route.harness, route.efforts, route.budget])).toEqual([['gemini', ['1024', '8192', '24576', '32768'], true], ['opencode', ['default'], false]])
    expect(atLevel('Opus 5', 'xhigh')).toBe('Opus 5 · xhigh')
    expect(atLevel('Gemini 2.5 Pro', '32k')).toBe('Gemini 2.5 Pro · 32k budget')
    expect(atLevel('Opus 5', null)).toBe('Opus 5')
  })
  // Tick (W5 review, 2026-10-11): one naming function across picker, registry rows, outdated tray and sheet.
  it('calls a model by one name in the picker, the registry row, the tray and the sheet', () => {
    const profiles = [
      profile({ harness: 'codex', model: 'gpt-6-sol', effort: 'high', display_name: 'GPT Sol', short_name: 'Sol', model_version: '6', identity: 'openai:gpt-6-sol' }),
      profile({ harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', display_name: 'GPT Sol', short_name: 'Sol', model_version: '6.1', identity: 'openai:gpt-6.1-sol' }),
      profile({ harness: 'claude', model: 'claude-opus-5', effort: 'high', family: 'anthropic', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5', identity: 'anthropic:claude-opus-5' }),
      profile({ harness: 'claude', model: 'claude-opus-5-5', effort: 'high', family: 'anthropic', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5', identity: 'anthropic:claude-opus-5-5' }),
    ]
    const rows = buildRegistry(profiles, NOW).groups.flatMap(group => group.rows)
    const picker = buildEntries(profiles)
    const entries = outdatedEntries({ versions: [
      { version: 'openai:gpt-6-sol', line: 'openai:sol', state: 'superseded', date: null, successor: 'openai:gpt-6.1-sol', reason: '', source: '', uses: [], incomplete: false },
      { version: 'anthropic:claude-opus-5', line: 'anthropic:opus', state: 'superseded', date: null, successor: 'anthropic:claude-opus-5-5', reason: '', source: '', uses: [], incomplete: false },
    ], revision: 'a'.repeat(64), next_cursor: '', incomplete: false }, profiles, NOW)
    expect(rows.map(row => row.model.name)).toEqual(['GPT-6.1 Sol', 'Opus 5.5'])
    expect(picker.map(entry => entry.name)).toEqual(['GPT-6.1 Sol', 'Opus 5.5'])
    expect(entries.map(entry => [entry.name, entry.successorName])).toEqual([['GPT-6 Sol', 'GPT-6.1 Sol'], ['Opus 5', 'Opus 5.5']])
    expect(rows.map(row => row.older[0]!.name)).toEqual(['GPT-6 Sol', 'Opus 5'])
    expect(outdatedTray(entries, false, false).named).toEqual([{ from: 'GPT-6 Sol', to: 'GPT-6.1 Sol' }, { from: 'Opus 5', to: 'Opus 5.5' }])
  })
  it('names how long a pause lasts and what the menu offers', () => {
    const now = new Date(2026, 9, 11, 15, 30)
    expect(new Date(pauseUntil('tomorrow', now)!)).toEqual(new Date(2026, 9, 12, 4, 0))
    expect(Date.parse(pauseUntil('week', now)!) - now.getTime()).toBe(7 * 86_400_000)
    expect(pauseUntil('open', now)).toBeNull()
    expect(pauseOptions(false, now).map(option => option.label)).toEqual(['Until tomorrow, 04:00', 'For a week', 'Until I resume it'])
    expect(pauseOptions(true, now).map(option => option.choice)).toEqual(['resume', 'tomorrow', 'week', 'open'])
    expect(pauseOptions(false, now)[1]!.detail).toBe('Until 18 Oct')
    expect(untilText(new Date(2026, 9, 11, 22, 0).toISOString(), now)).toBe('until 22:00')
    expect(untilText(new Date(2026, 9, 12, 4, 0).toISOString(), now)).toBe('until tomorrow, 04:00')
    expect(untilText(new Date(2026, 9, 18, 15, 30).toISOString(), now)).toBe('until 18 Oct')
    expect(untilText(null, now)).toBe('until resumed')
  })
  it('tags a pause on the row: every route, one named route, or part of a route', () => {
    const at = '2026-10-10T12:00:00Z'
    const paused = (until: string | null) => ({ until, reason: PAUSED_REASON, by: null, at })
    const flash = buildRegistry(many(), NOW).groups.flatMap(group => group.rows).find(row => row.key === 'google:gemini-flash')!.model
    expect(pauseTag(flash, NOW)).toBe('Paused until 14 Oct')
    const opusProfiles = (pauses: Record<string, ReturnType<typeof paused> | null>) => buildRegistry(many().map(item => item.model in pauses ? { ...item, pause: pauses[item.model] } : item), NOW).groups.flatMap(group => group.rows).find(row => row.key === 'anthropic:opus')!.model
    expect(pauseTag(opusProfiles({ 'claude-opus-5.5': paused(null) }), NOW)).toBe('Cursor paused')
    expect(pauseTag(opusProfiles({ 'claude-opus-5.5': paused(null), 'anthropic/claude-opus-5-5': paused(null) }), NOW)).toBe('2 routes paused')
    expect(pauseTag(opusProfiles({ 'claude-opus-5-5': paused('2026-10-20T12:00:00Z'), opus: paused(null), 'claude-opus-5.5': paused(null), 'anthropic/claude-opus-5-5': paused('2026-10-15T12:00:00Z') }), NOW)).toBe('Paused until 15 Oct')
    const grok = buildRegistry(many().map(item => item.model === 'grok-4.7-low' ? { ...item, pause: paused(null) } : item), NOW).groups.flatMap(group => group.rows).find(row => row.key === 'xai:grok')!.model
    expect(pauseTag(grok, NOW)).toBe('Partly paused')
    expect(grok.routes[1]).toMatchObject({ harness: 'cursor', pause: null, partlyPaused: true })
  })
  it('pauses every route of a version, Cursor\'s Grok levels through one id, stops at the first refusal and undoes exactly what it changed', async () => {
    const grok = buildRegistry(many(), NOW).groups.flatMap(group => group.rows).find(row => row.key === 'xai:grok')!.model
    let refuse = ''
    http.handle = (_method, path) => path.includes(refuse) && refuse ? json({ error: 'model_pause_limit' }, 413) : json({ harness: 'x', model: 'y', pause: null })
    const done = await pauseModel(grok, grok.family, { until: '2026-10-12T02:00:00.000Z' })
    expect(done.error).toBeNull()
    expect(http.calls.map(call => `${call.method} ${call.path}`)).toEqual(['PUT /models/lines/grok/grok-4.7/pause', 'PUT /models/lines/cursor/grok-4.7-low/pause'])
    expect(http.calls[0]!.body).toEqual({ until: '2026-10-12T02:00:00.000Z', reason: PAUSED_REASON })
    // Every other route is written one by one; a refusal stops the rest and says how far it got.
    http.calls.length = 0; refuse = 'cursor/claude-opus-5.5'
    const opus: RegistryModel = buildRegistry(many(), NOW).groups.flatMap(group => group.rows).find(row => row.key === 'anthropic:opus')!.model
    const partial = await pauseModel(opus, opus.family, 'resume')
    expect(http.calls.map(call => `${call.method} ${call.path}`)).toEqual([
      'DELETE /models/lines/claude/claude-opus-5-5/pause', 'DELETE /models/lines/claude/opus/pause', 'DELETE /models/lines/cursor/claude-opus-5.5/pause',
    ])
    expect(partial).toMatchObject({ error: 'model_pause_limit' })
    expect(partial.done.map(step => step.model)).toEqual(['claude-opus-5-5', 'opus'])
    // Only Cursor's Grok ids are one group on the server (pause.go); any other per-level ids are each written.
    const route = { ...opus.routes[2]!, models: ['x-high', 'x-max'] }
    expect(pauseTargets(route, 'anthropic')).toEqual(['x-high', 'x-max'])
    expect(pauseTargets(route, 'xai')).toEqual(['x-high'])
    // Undo writes back what each step found: an earlier pause that still lies ahead, otherwise none.
    http.calls.length = 0; refuse = ''
    const flash = buildRegistry(many(), NOW).groups.flatMap(group => group.rows).find(row => row.key === 'google:gemini-flash')!.model
    const steps = (await pauseModel(flash, flash.family, { until: null })).done
    http.calls.length = 0
    expect(await undoPause(steps, NOW)).toBeNull()
    expect(http.calls).toEqual([{ method: 'PUT', path: '/models/lines/gemini/gemini-2.5-flash/pause', body: { until: '2026-10-14T12:00:00Z', reason: 'Paused in Settings › Models' } }])
    http.calls.length = 0
    expect(await undoPause(steps, new Date('2026-10-15T00:00:00Z'))).toBeNull()
    expect(http.calls).toEqual([{ method: 'DELETE', path: '/models/lines/gemini/gemini-2.5-flash/pause', body: undefined }])
    // A person or workspace gone mid-run stops the next request.
    http.calls.length = 0
    let live = true
    http.handle = () => { live = false; return json({ harness: 'x', model: 'y', pause: null }) }
    await expect(pauseModel(grok, grok.family, 'resume', { live: () => live })).rejects.toBeInstanceOf(StaleScopeError)
    expect(http.calls).toHaveLength(1)
  })
  // AEON-1163 risk: one read per route excludes only that route, so a sibling route removed in the same step is named as the fallback.
  it('reads one remove question for a whole version: every profile it removes goes with that one read', async () => {
    http.handle = () => json({ used_by: [], replacement: { line: null, effort: null }, revision: 'r1', incomplete: false })
    await previewRemoval('pi', 'anthropic/claude-opus-5-5', undefined, ['claude-high', 'pi high'])
    await previewRemoval('codex', 'gpt-6.1-sol')
    expect(http.calls.map(call => `${call.method} ${call.path}`)).toEqual([
      'GET /models/lines/pi/anthropic%2Fclaude-opus-5-5/usage?profile=claude-high&profile=pi%20high',
      'GET /models/lines/codex/gpt-6.1-sol/usage',
    ])
  })
})
