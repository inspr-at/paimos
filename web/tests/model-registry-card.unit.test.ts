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
    if (path.startsWith('/models/outdated?') && method === 'GET') return answer({ versions: [], revision: '0'.repeat(64), next_cursor: '', incomplete: false })
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
  '../../AppIcon.vue': {}, '../../agents/HarnessMark.vue': {}, './ModelRegistryEditor.vue': {}, './ModelOutdatedSheet.vue': {},
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

// AEON-1164 fix2: the outdated tray and the review sheet.
// Risks: old versions that cannot move fill the first page of the outdated list, so later versions that can
// never reach the tray; and a removal whose answer is lost after it committed is reported as "Nothing was
// removed" while the sheet keeps offering versions that are gone.
interface OldCard {
  lines: Vue.ComputedRef<registry.RegistryLine[]>; entries: Vue.ComputedRef<registry.OutdatedEntry[]>; tray: Vue.ComputedRef<registry.OutdatedTray | null>
  more: Vue.ComputedRef<boolean>; retireOld(versions: string[]): void
}
const OLD_REVISION = 'a'.repeat(64)
const gpt6 = (): Profile[] => LEVELS.map((effort, index) => ({
  id: `old-${effort}`, slug: `old-${effort}`, version: '6', harness: 'codex', family: 'openai', model: 'gpt-6-sol', effort, tier: 'strong', enabled: true, created_at: '2026-08-20T12:00:00Z',
  display_name: 'GPT Sol', model_version: '6', source: 'auto', retired: false, retire_at: null, effort_level: index + 1, identity: 'openai:gpt-6-sol',
}))
const version = (id: string, successor: string): registry.OutdatedVersion => ({ version: id, line: 'openai:gpt-sol', state: 'superseded', date: null, successor, reason: 'Source reports a successor', source: 'agent-report:codex', uses: [], incomplete: false })
/** The outdated list as the server pages it, and its one-step retire; `lose` commits a retire and then loses its answer. */
function oldServer(versions: registry.OutdatedVersion[], lose: ((committed: () => void) => unknown) | null = null) {
  const state = { versions, revision: OLD_REVISION, reads: [] as string[], retires: [] as unknown[] }
  server.profiles = [...sol('strongest for building').map(profile => ({ ...profile, identity: 'openai:gpt-6.1-sol' })), ...gpt6()]
  const base = http.handler
  http.handler = async (path, init) => {
    const method = init.method ?? 'GET'
    if (path.startsWith('/models/outdated?') && method === 'GET') {
      state.reads.push(path)
      const url = new URL(path, 'http://registry'), after = url.searchParams.get('cursor') ?? '', limit = Number(url.searchParams.get('limit'))
      const rest = state.versions.filter(item => item.version > after), items = rest.slice(0, limit)
      return answer({ versions: items, revision: state.revision, next_cursor: rest.length > limit ? items.at(-1)!.version : '', incomplete: false })
    }
    if (path === '/models/outdated/retire' && method === 'POST') {
      const body = JSON.parse(String(init.body)) as { versions: string[]; revision: string }
      state.retires.push(body)
      if (body.revision !== state.revision) return answer({ error: 'stale outdated preview' }, 409)
      const commit = () => {
        for (const profile of server.profiles) if (body.versions.includes(profile.identity ?? '')) profile.retired = true
        state.versions = state.versions.filter(item => !body.versions.includes(item.version))
        state.revision = 'b'.repeat(64)
      }
      if (lose) return lose(commit)
      commit()
      return answer({ moved: [], removed: body.versions, undo_token: `9.${'c'.repeat(64)}` })
    }
    return base(path, init)
  }
  return state
}
async function mountOld(): Promise<OldCard> {
  const scope = Vue.effectScope(); scopes.push(scope)
  const card = scope.run(setup)! as unknown as OldCard
  await settle()
  return card
}
// Fifty versions sorted before the one that can move, each naming a successor the registry does not hold.
const unmovable = () => Array.from({ length: 50 }, (_, i) => version(`aaa:old-${String(i).padStart(2, '0')}`, 'aaa:unregistered'))

it('versions that can move still reach the tray when the first page holds only versions that cannot, and Remove sends the first page\'s revision', async () => {
  const old = oldServer([...unmovable(), version('openai:gpt-6-sol', 'openai:gpt-6.1-sol')])
  const card = await mountOld()
  expect(old.reads).toEqual(['/models/outdated?limit=50', '/models/outdated?limit=50&cursor=aaa%3Aold-49'])
  expect(card.entries.value.map(entry => entry.version)).toEqual(['openai:gpt-6-sol'])
  expect(card.tray.value).toMatchObject({ named: [{ from: 'GPT Sol 6', to: 'GPT Sol 6.1' }], count: '1 old version' })
  expect(card.more.value).toBe(false)

  card.retireOld(['openai:gpt-6-sol'])
  await settle()
  expect(old.retires).toEqual([{ versions: ['openai:gpt-6-sol'], revision: OLD_REVISION }])
  expect(toasts.map(item => item.message)).toEqual(['Removed 1 old version · 0 uses moved'])
  expect(card.tray.value).toBeNull()
})

it('a removal whose answer is lost after it committed is not called "nothing removed": it says the outcome is open and shows the list read again', async () => {
  const lost: [string, (committed: () => void) => unknown][] = [
    ['a lost answer', committed => { committed(); throw new TypeError('Failed to fetch') }],
    ['a gateway timeout', committed => { committed(); return answer({ error: 'gateway timeout' }, 504) }],
  ]
  for (const [how, lose] of lost) {
    toasts.length = 0
    const old = oldServer([version('openai:gpt-6-sol', 'openai:gpt-6.1-sol')], lose)
    const card = await mountOld()
    expect(card.entries.value.map(entry => entry.version), how).toEqual(['openai:gpt-6-sol'])
    const reads = old.reads.length

    card.retireOld(['openai:gpt-6-sol'])
    await settle()
    expect(old.retires, how).toHaveLength(1)
    expect(toasts.map(item => item.message), how).toEqual(['The registry didn’t confirm the removal, so it may have happened. The list was refreshed; check it before removing again.'])
    expect(toasts[0]!.options?.tone, how).toBe('error')
    // The card shows what the server holds now: the version is gone from the sheet, the tray and the list.
    expect(old.reads.length, how).toBeGreaterThan(reads)
    expect(card.entries.value, how).toEqual([])
    expect(card.tray.value, how).toBeNull()
    expect(card.lines.value.map(line => line.model), how).toEqual(['gpt-6.1-sol'])
  }
})

it('a refusal the server answered still says nothing was removed', async () => {
  const old = oldServer([version('openai:gpt-6-sol', 'openai:gpt-6.1-sol')], () => answer({ error: 'invalid or duplicate version' }, 400))
  const card = await mountOld()
  card.retireOld(['openai:gpt-6-sol'])
  await settle()
  expect(old.retires).toHaveLength(1)
  expect(toasts.map(item => item.message)).toEqual(['Nothing was removed. Try again.'])
  expect(card.entries.value.map(entry => entry.version)).toEqual(['openai:gpt-6-sol'])
})
