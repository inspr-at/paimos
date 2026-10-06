// SPDX-License-Identifier: AGPL-3.0-only
// AEON-791: Agent names and Work vocabulary share one revision and one store.
// Risks: a read that an Agent names save overtook puts the old word back on every
// screen, and an edited draft silently overwrites names another admin saved.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'

type Names = { singular: string; plural: string }
type Vocabulary = { revision: number; leaf: { name: string; icon: string }; levels: { name: string; icon: string }[]; lead?: Names }
type Handler = (path: string, init?: RequestInit) => Promise<Response>
const http = vi.hoisted(() => ({ handler: (async () => new Response('{}')) as Handler }))
vi.mock('../src/lib/api', () => {
  class APIError extends Error { constructor(readonly status: number, message: string) { super(message) } }
  return { api: (path: string, init?: RequestInit) => http.handler(path, init), APIError }
})
vi.mock('../src/stores/session', async () => {
  const { reactive } = await import('vue')
  const session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'admin' } } })
  return { useSession: () => session }
})
import * as apiModule from '../src/lib/api'
import * as sessionModule from '../src/stores/session'
import * as storeModule from '../src/stores/workVocabulary'
import * as lead from '../src/lib/lead'
import * as vocabularyLib from '../src/lib/workVocabulary'

const DIRIGENT = { singular: 'Dirigent', plural: 'Dirigenten' }, SUPERVISOR = { singular: 'Supervisor', plural: 'Supervisors' }
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(r => { resolve = r }); return { promise, resolve } }
async function settle() { for (let i = 0; i < 8; i++) await Promise.resolve(); await Vue.nextTick() }
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

// The server as in internal/nodes/vocabulary.go: revision check, a write without lead keeps the names.
let server: Vocabulary, puts: Record<string, unknown>[], hold: ReturnType<typeof deferred> | undefined
const scopes: Vue.EffectScope[] = []
beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('navigator', { platform: 'Mac' })
  server = { revision: 4, leaf: { name: 'Arbeitsschritt', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }] }
  puts = []; hold = undefined
  http.handler = async (path, init) => {
    expect(path).toBe('/settings/work-vocabulary')
    if (init?.method === 'PUT') {
      const body = JSON.parse(String(init.body))
      puts.push(body)
      if (body.revision !== server.revision) return json(409, { error: 'work vocabulary changed; reload before saving' })
      const names = 'lead' in body ? (body.lead.singular || body.lead.plural ? body.lead : undefined) : server.lead
      server = { revision: server.revision + 1, leaf: body.leaf, levels: body.levels, ...(names ? { lead: names } : {}) }
      return json(200, server)
    }
    // The read answers with what the server held when it was asked.
    const snapshot = structuredClone(server), gate = hold
    hold = undefined
    if (gate) await gate.promise
    return json(200, snapshot)
  }
})
afterEach(() => { for (const s of scopes.splice(0)) s.stop(); vi.unstubAllGlobals(); lead.setLeadWords(null) })

const modules: Record<string, unknown> = {
  vue: Vue, '../../lib/api': apiModule, '../../lib/authz': { can: () => true }, '../../lib/lead': lead, '../../lib/workVocabulary': vocabularyLib,
  '../../stores/session': sessionModule, '../../stores/workVocabulary': storeModule, '../AppIcon.vue': {}, '../KeyCap.vue': {}, './SettingsCard.vue': {},
}
function setup<T>(file: string): T {
  const { descriptor } = parse(readFileSync(new URL(`../src/components/settings/${file}`, import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: file })
  const exports: { default?: { setup: (props: object, ctx: object) => unknown } } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => { if (!(id in modules)) throw new Error(id); return modules[id] }, exports)
  return exports.default!.setup({}, { expose: () => {} }) as T
}
type NamesCard = { draft: Vue.Ref<Names>; save: () => Promise<void>; message: Vue.Ref<string>; error: Vue.Ref<boolean> }
type WorkCard = { draft: Vue.Ref<Vocabulary>; load: () => Promise<void>; save: () => Promise<void> }
async function mount() {
  const scope = Vue.effectScope(); scopes.push(scope)
  const cards = scope.run(() => ({ names: setup<NamesCard>('AgentNamesCard.vue'), work: setup<WorkCard>('WorkVocabularyCard.vue') }))!
  await settle()
  const store = storeModule.useWorkVocabulary()
  expect(store.value.revision).toBe(4)
  return { ...cards, store }
}

it('a Work vocabulary read overtaken by an Agent names save never brings the old word back', async () => {
  const { names, work, store } = await mount()
  hold = deferred()
  const gate = hold
  const reading = work.load()
  names.draft.value = { ...SUPERVISOR }
  await names.save()
  expect(names.message.value).toBe('Agent names saved.')
  expect(server.revision).toBe(5)
  gate.resolve(); await reading; await settle()
  // The revision-4 answer arrives last and is dropped everywhere.
  expect(store.value).toMatchObject({ revision: 5, lead: SUPERVISOR })
  expect(lead.LEAD_WORDS.S).toBe('Supervisor')
  expect(work.draft.value.revision).toBe(5)
  expect(names.draft.value).toEqual(SUPERVISOR)
  expect(names.message.value).toBe('Agent names saved.')
})

it('an edited draft conflicts with names another admin saved instead of overwriting them', async () => {
  const { names, work, store } = await mount()
  names.draft.value = { ...DIRIGENT }
  server = { ...server, revision: 5, lead: { ...SUPERVISOR } }
  await work.load(); await settle()
  expect(store.value).toMatchObject({ revision: 5, lead: SUPERVISOR })
  expect(names.draft.value).toEqual(DIRIGENT)
  await names.save()
  // The draft goes back under the revision it was edited on, so the server refuses it.
  expect(puts).toEqual([expect.objectContaining({ revision: 4, lead: DIRIGENT })])
  expect(names.error.value).toBe(true)
  expect(names.message.value).toBe('work vocabulary changed; reload before saving')
  expect(server).toMatchObject({ revision: 5, lead: SUPERVISOR })
  expect(lead.LEAD_WORDS.S).toBe('Supervisor')
  expect(names.draft.value).toEqual(DIRIGENT)
})

it('an edited draft follows a work-level save that left the names alone', async () => {
  const { names, work } = await mount()
  names.draft.value = { ...DIRIGENT }
  work.draft.value.leaf.name = 'Schritt'
  await work.save(); await settle()
  expect(puts[0]).not.toHaveProperty('lead')
  await names.save()
  expect(names.message.value).toBe('Agent names saved.')
  expect(puts[1]).toEqual({ revision: 5, leaf: { name: 'Schritt', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }], lead: DIRIGENT })
  expect(server).toMatchObject({ revision: 6, leaf: { name: 'Schritt' }, lead: DIRIGENT })
})
