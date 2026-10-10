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
import { RegistryError, addLine, getRefreshStatus, listProfiles, buildLines, changeOf, checkReport, cooldownLabel, draftOf, emptyDraft, familyOf, intervalLabel, lastChecked, lineWriteOf, metaParts, modelSlug, removalText, removeLine, restoreLine, routeOf, routesFor, shortDate, splitLevels, takenText, useText, validateDraft, checkNow, type LineDraft, type LineUsage, type RegistryProfile, type WriteScope } from '../src/lib/modelRegistry'

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
    expect(lines[0]).toMatchObject({ name: 'GPT Sol 6.1', displayName: 'GPT Sol', efforts: ['low', 'medium', 'high', 'xhigh'], route: 'openai', source: 'auto', isNew: false })
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
  it('describes a line as harness, route (when it adds something), id, source and what was taken on', () => {
    const [codex, cursor] = [buildLines([sol('high', 3)])[0]!, buildLines([profile({ harness: 'cursor', model: 'composer-2.5', effort: 'default', family: 'cursor' })])[0]!]
    expect(metaParts(codex)).toEqual(['Codex', 'OpenAI', 'gpt-6.1-sol', 'Auto-discovered'])
    expect(metaParts(cursor)).toEqual(['Cursor', 'composer-2.5', 'Auto-discovered'])
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
  })
})
