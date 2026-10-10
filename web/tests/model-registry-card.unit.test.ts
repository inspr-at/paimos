// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1012: the registry card binds an edit to the editor that read the line.
// Risk: a usage read of a closed editor lands after the same line is opened again,
// swaps the new editor's fields for the older ones, and Undo then writes those older
// values over a change another admin made in between.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'

type Handler = (path: string, init: RequestInit) => Promise<unknown>
const http = vi.hoisted(() => ({ handler: (async () => ({})) as Handler }))
vi.mock('../src/lib/api', () => ({ api: (path: string, init: RequestInit = {}) => http.handler(path, init) }))
import * as registry from '../src/lib/modelRegistry'
import * as accountUse from '../src/lib/accountUse'
import * as identityScope from '../src/lib/identityScope'

type Profile = registry.RegistryProfile
type Toasted = { message: string; options?: { tone?: string; action?: { label: string; run: () => void } } }

// Plain answers, so the only awaits in these tests are the ones the card itself makes.
const answer = (body: unknown, status = 200) => ({ ok: status < 400, status, headers: new Headers(), json: async () => structuredClone(body) })
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(r => { resolve = r }); return { promise, resolve } }
async function settle() { for (let i = 0; i < 60; i++) await Promise.resolve(); await Vue.nextTick(); for (let i = 0; i < 60; i++) await Promise.resolve() }

const LEVELS = ['low', 'medium', 'high']
const sol = (note: string): Profile[] => LEVELS.map((effort, index) => ({
  id: `sol-${effort}`, slug: `sol-${effort}`, version: '6.1', harness: 'codex', family: 'openai', model: 'gpt-6.1-sol', effort, tier: 'strong', enabled: true, created_at: '2026-09-20T12:00:00Z',
  display_name: 'GPT Sol', model_version: '6.1', note, source: 'auto', retired: false, retire_at: null, effort_level: index + 1,
}))

// The server as the card sees it: a revision per write, a usage read that answers with what the server held when it was asked.
let server: { revision: number; profiles: Profile[]; held: ReturnType<typeof deferred>[]; reads: number; delivered: number; puts: Record<string, unknown>[]; lists: number }
const revision = () => `r${server.revision}`
const toasts: Toasted[] = []
const scopes: Vue.EffectScope[] = []
beforeEach(() => {
  server = { revision: 1, profiles: sol('strongest for building'), held: [], reads: 0, delivered: 0, puts: [], lists: 0 }
  toasts.length = 0
  http.handler = async (path, init) => {
    const method = init.method ?? 'GET'
    if (path === '/models' && method === 'GET') { server.lists++; return answer(server.profiles) }
    if (path === '/models/refresh' && method === 'GET') return answer({ settings: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: true, interval_minutes: 360 }, last_run_at: null })
    if (path === '/models/lines/codex/gpt-6.1-sol/usage') {
      const read = server.reads++
      const snapshot = { used_by: [], incomplete: false, replacement: { line: null, effort: null }, revision: revision(), line_profiles: structuredClone(server.profiles) }
      const gate = server.held[read]
      if (gate) await gate.promise
      server.delivered++
      return answer(snapshot)
    }
    if (path === '/models/lines/codex/gpt-6.1-sol' && method === 'PUT') {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>
      server.puts.push(body)
      if (body.revision !== revision()) return answer({ error: 'stale registry revision' }, 409)
      for (const profile of server.profiles) { profile.display_name = String(body.display_name); profile.note = String(body.note) }
      server.revision++
      return answer({ profiles: server.profiles, revision: revision() })
    }
    throw new Error(`unexpected ${method} ${path}`)
  }
})
afterEach(() => { for (const s of scopes.splice(0)) s.stop() })

const session = Vue.reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'admin', kind: 'person' } }, authenticationCurrent: () => true })
const modules: Record<string, unknown> = {
  vue: { ...Vue, onBeforeUnmount: () => {} },
  '../../AppIcon.vue': {}, '../../agents/HarnessMark.vue': {}, './ModelRegistryEditor.vue': {},
  '../../../lib/authz': { can: () => true, onAccessChange: () => () => {} },
  '../../../lib/identityScope': identityScope,
  '../../../lib/toast': { toast: (message: string, options?: Toasted['options']) => { toasts.push({ message, options }) } },
  '../../../lib/workKinds': { listWorkKinds: async () => ({ items: [] }) },
  '../../../stores/session': { useSession: () => session },
  '../../../lib/modelRegistry': registry,
  '../../../lib/accountUse': accountUse,
}
interface Card {
  lines: Vue.ComputedRef<registry.RegistryLine[]>; draft: Vue.Ref<registry.LineDraft>; editing: Vue.Ref<string | null>; saving: Vue.Ref<boolean>
  openEditor(key: string, trigger: HTMLElement | null): void; closeEditor(restoreFocus: boolean): void; save(): void
}
function setup(): Card {
  const file = 'ModelRegistryCard.vue'
  const { descriptor } = parse(readFileSync(new URL(`../src/components/settings/models/${file}`, import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: file })
  const exports: { default?: { setup: (props: object, ctx: object) => unknown } } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => { if (!(id in modules)) throw new Error(id); return modules[id] }, exports)
  return exports.default!.setup({}, { expose: () => {} }) as Card
}
async function mount() {
  const scope = Vue.effectScope(); scopes.push(scope)
  const card = scope.run(setup)!
  await settle()
  expect(card.lines.value).toHaveLength(1)
  return { card, key: card.lines.value[0]!.key }
}
/** Another admin changes the note after the card loaded its list. */
function changeElsewhere(note: string) {
  for (const profile of server.profiles) profile.note = note
  server.revision++
}
const savedToast = () => toasts.filter(item => item.message === 'Saved').at(-1)!
const SOL = { display_name: 'GPT Sol', efforts: LEVELS, route: 'openai' }

it('an answer from a closed editor never reaches the reopened one: Undo restores the values that editor was read with', async () => {
  const { card, key } = await mount()
  const slow = deferred()
  server.held[0] = slow
  card.openEditor(key, null)
  await settle()
  expect(server.reads).toBe(1)
  expect(server.delivered).toBe(0)

  card.closeEditor(false)
  changeElsewhere('changed elsewhere')
  card.openEditor(key, null)
  await settle()
  expect(server.reads).toBe(2)
  expect(card.draft.value.note).toBe('changed elsewhere')

  // The first answer, read before the change, arrives last.
  slow.resolve()
  await settle()
  expect(server.delivered).toBe(2)
  expect(card.draft.value.note).toBe('changed elsewhere')

  card.draft.value = { ...card.draft.value, name: 'GPT Sol renamed' }
  card.save()
  await settle()
  expect(server.puts[0]).toEqual({ ...SOL, display_name: 'GPT Sol renamed', note: 'changed elsewhere', revision: 'r2' })
  const saved = savedToast()
  expect(saved.options?.action?.label).toBe('Undo')

  saved.options!.action!.run()
  await settle()
  expect(server.puts[1]).toEqual({ ...SOL, note: 'changed elsewhere', revision: 'r3' })
  expect(server.profiles.every(profile => profile.note === 'changed elsewhere' && profile.display_name === 'GPT Sol')).toBe(true)
})

it('an earlier answer that arrives first does not stand in for the reopened editor\'s own read', async () => {
  const { card, key } = await mount()
  const first = deferred(), second = deferred()
  server.held[0] = first
  server.held[1] = second
  card.openEditor(key, null)
  await settle()
  card.closeEditor(false)
  changeElsewhere('changed elsewhere')
  card.openEditor(key, null)
  await settle()
  expect(server.reads).toBe(2)

  first.resolve()
  await settle()
  expect(server.delivered).toBe(1)
  expect(card.draft.value.note).toBe('strongest for building')

  second.resolve()
  await settle()
  expect(server.delivered).toBe(2)
  expect(card.draft.value.note).toBe('changed elsewhere')
  card.draft.value = { ...card.draft.value, name: 'GPT Sol renamed' }
  card.save()
  await settle()
  expect(server.puts).toEqual([{ ...SOL, display_name: 'GPT Sol renamed', note: 'changed elsewhere', revision: 'r2' }])
  savedToast().options!.action!.run()
  await settle()
  expect(server.puts[1]).toEqual({ ...SOL, note: 'changed elsewhere', revision: 'r3' })
})

it('a save that waited for a read and lost its editor writes nothing and does not call it a conflict', async () => {
  const { card, key } = await mount()
  const slow = deferred()
  server.held[0] = slow
  card.openEditor(key, null)
  await settle()
  card.draft.value = { ...card.draft.value, name: 'GPT Sol renamed' }
  card.save()
  await settle()
  expect(card.saving.value).toBe(true)

  card.closeEditor(false)
  const listsBefore = server.lists
  slow.resolve()
  await settle()
  expect(server.delivered).toBe(1)
  expect(server.puts).toHaveLength(0)
  expect(card.saving.value).toBe(false)
  expect(toasts).toEqual([])
  expect(server.lists).toBe(listsBefore)
})
