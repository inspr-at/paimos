// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1027: the folded "No lead" card is remembered per person and project.
// Risks: a fold made before the read overwrites the folds of other projects; one person's fold
// shows for another; a project that got a lead stays folded for the next time it has none.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope, type Ref } from 'vue'
const mocks = vi.hoisted(() => ({ api: vi.fn() }))
vi.mock('../src/lib/api', () => ({ api: mocks.api }))
import { LEAD_CARD_KEY, MAX_FOLDED, readCollapsed, useLeadCardFold, withCollapsed } from '../src/lib/leadCardFold'
import { setPreferenceOwner } from '../src/lib/preferences'

const flush = async () => { for (let i = 0; i < 8; i++) await new Promise<void>(resolve => setImmediate(resolve)) }
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const person = (id: string) => ({ tenant: { id: 't' }, principal: { id } })

// The server keeps one value per person and key, like /api/preferences/{key}.
let server: Map<string, unknown>, who: string, puts: { who: string; value: unknown }[], gate: ReturnType<typeof deferred<Response>> | undefined
const scopes: EffectScope[] = []
beforeEach(() => {
  server = new Map(); who = 'alice'; puts = []; gate = undefined
  mocks.api.mockReset()
  mocks.api.mockImplementation(async (path: string, init?: RequestInit) => {
    expect(path).toBe(`/preferences/${LEAD_CARD_KEY}`)
    const owner = who
    if (init?.method === 'PUT') { const { value } = JSON.parse(String(init.body)); server.set(owner, value); puts.push({ who: owner, value }); return json({ key: LEAD_CARD_KEY, value }) }
    const answer = json({ key: LEAD_CARD_KEY, value: server.get(owner) ?? null })
    return gate ? gate.promise.then(() => answer) : answer
  })
  setPreferenceOwner(null)
  setPreferenceOwner(person('alice'))
})
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); setPreferenceOwner(null); vi.unstubAllGlobals() })

function mount(project = 'p1', hasLead = false) {
  const projectId = ref(project), viewer = ref('t:alice'), lead = ref(hasLead)
  const scope = effectScope(); scopes.push(scope)
  const fold = scope.run(() => useLeadCardFold(projectId, viewer, lead))!
  return { fold, projectId, viewer, lead }
}
const as = (id: string, viewer: Ref<string>) => { who = id; setPreferenceOwner(person(id)); viewer.value = `t:${id}` }

describe('stored shape', () => {
  it('keeps ids only, once each, and at most MAX_FOLDED (the oldest fold goes first)', () => {
    expect(readCollapsed(null)).toEqual([])
    expect(readCollapsed({ collapsed: 'p1' })).toEqual([])
    expect(readCollapsed({ collapsed: ['p1', 7, '', 'p1', null, 'p2', 'x'.repeat(65)] })).toEqual(['p1', 'p2'])
    const many = Array.from({ length: MAX_FOLDED + 5 }, (_, i) => `p${i}`)
    expect(readCollapsed({ collapsed: many })).toEqual(many.slice(-MAX_FOLDED))
    expect(withCollapsed(['a', 'b'], 'c', true)).toEqual(['a', 'b', 'c'])
    expect(withCollapsed(['a', 'b'], 'a', true)).toEqual(['b', 'a'])
    expect(withCollapsed(['a', 'b'], 'a', false)).toEqual(['b'])
    expect(withCollapsed(many, 'new', true)).toHaveLength(MAX_FOLDED)
    expect(withCollapsed(many, 'new', true).at(-1)).toBe('new')
  })
})

describe('the fold of the "No lead" card', () => {
  it('starts open, folds one project and remembers it for that person and project only', async () => {
    const { fold, projectId } = mount()
    await flush()
    expect(fold.ready.value).toBe(true)
    expect(fold.collapsed.value).toBe(false)
    fold.toggle(); await flush()
    expect(fold.collapsed.value).toBe(true)
    expect(puts).toEqual([{ who: 'alice', value: { collapsed: ['p1'] } }])
    projectId.value = 'p2'
    expect(fold.collapsed.value).toBe(false)
    fold.toggle(); await flush()
    expect(puts.at(-1)).toEqual({ who: 'alice', value: { collapsed: ['p1', 'p2'] } })
    projectId.value = 'p1'
    expect(fold.collapsed.value).toBe(true)
    // A reload reads it back from the server.
    for (const scope of scopes.splice(0)) scope.stop()
    setPreferenceOwner(null); setPreferenceOwner(person('alice'))
    const again = mount('p2'); await flush()
    expect(again.fold.collapsed.value).toBe(true)
  })

  it('unfolding removes only that project from the stored list', async () => {
    server.set('alice', { collapsed: ['p1', 'p2'] })
    const { fold } = mount('p1'); await flush()
    expect(fold.collapsed.value).toBe(true)
    fold.toggle(); await flush()
    expect(fold.collapsed.value).toBe(false)
    expect(puts).toEqual([{ who: 'alice', value: { collapsed: ['p2'] } }])
  })

  it('shows nothing until the preference is read, and a fold made then cannot overwrite the others', async () => {
    server.set('alice', { collapsed: ['p2', 'p3'] })
    gate = deferred<Response>()
    const { fold } = mount('p1'); await flush()
    expect(fold.ready.value).toBe(false)
    fold.toggle(); await flush()
    expect(fold.collapsed.value).toBe(false)
    expect(puts).toEqual([])
    gate.resolve(new Response()); await flush()
    expect(fold.ready.value).toBe(true)
    fold.toggle(); await flush()
    expect(puts).toEqual([{ who: 'alice', value: { collapsed: ['p2', 'p3', 'p1'] } }])
  })

  it("another person never sees this person's fold, and gets their own once their preference is read", async () => {
    server.set('alice', { collapsed: ['p1'] })
    const { fold, viewer } = mount('p1'); await flush()
    expect(fold.collapsed.value).toBe(true)
    gate = deferred<Response>()
    as('bob', viewer)
    expect(fold.ready.value).toBe(false)
    expect(fold.collapsed.value).toBe(false)
    gate.resolve(new Response()); await flush()
    expect(fold.ready.value).toBe(true)
    expect(fold.collapsed.value).toBe(false)
    fold.toggle(); await flush()
    expect(puts).toEqual([{ who: 'bob', value: { collapsed: ['p1'] } }])
    expect(server.get('alice')).toEqual({ collapsed: ['p1'] })
  })

  it('a project that gets a lead forgets its fold, and the card opens the next time it has none', async () => {
    server.set('alice', { collapsed: ['p1', 'p2'] })
    const { fold, lead } = mount('p1'); await flush()
    expect(fold.collapsed.value).toBe(true)
    expect(puts).toEqual([])
    lead.value = true; await flush()
    expect(fold.collapsed.value).toBe(false)
    expect(puts).toEqual([{ who: 'alice', value: { collapsed: ['p2'] } }])
    lead.value = false; await flush()
    expect(fold.collapsed.value).toBe(false)
    expect(puts).toHaveLength(1)
  })

  it('moves only for a person’s own toggle, never for a stored fold, another project or reduced motion', async () => {
    server.set('alice', { collapsed: ['p1'] })
    const { fold, projectId } = mount('p1'); await flush()
    expect(fold.moving.value).toBe(false)
    fold.toggle(); await flush()
    expect(fold.moving.value).toBe(true)
    projectId.value = 'p2'
    expect(fold.moving.value).toBe(false)
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('prefers-reduced-motion') }))
    fold.toggle(); await flush()
    expect(fold.collapsed.value).toBe(true)
    expect(fold.moving.value).toBe(false)
  })

  it('the transition end settles only the fold’s own height transition', async () => {
    const { fold } = mount(); await flush()
    fold.toggle(); await flush()
    const host = {}, other = {}
    fold.settle({ target: other, currentTarget: host, propertyName: 'grid-template-rows' } as unknown as TransitionEvent)
    fold.settle({ target: host, currentTarget: host, propertyName: 'opacity' } as unknown as TransitionEvent)
    expect(fold.moving.value).toBe(true)
    fold.settle({ target: host, currentTarget: host, propertyName: 'grid-template-rows' } as unknown as TransitionEvent)
    expect(fold.moving.value).toBe(false)
  })

  it('signed out: nothing folds, nothing is written and nothing waits to move', async () => {
    setPreferenceOwner(null)
    const { fold } = mount(); await flush()
    fold.toggle(); await flush()
    expect(fold.collapsed.value).toBe(false)
    expect(fold.moving.value).toBe(false)
    expect(puts).toEqual([])
  })
})
