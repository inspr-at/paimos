// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1401 fix6. The risk: a Status names save whose answer is still being read
// when the signed-in person or workspace changes lands on the next person's card:
// the earlier workspace's names show (and mark the store read, so the router never
// reads again), the next person's drafts are overwritten, or the earlier save's end
// lets a second save run beside the next person's own.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'

type Body = { promise: Promise<unknown>; resolve: (value: unknown) => void }
const fixture = vi.hoisted(() => ({ session: {} as { identity: unknown; authenticationCurrent: () => boolean }, handler: (async () => ({})) as (path: string, init?: RequestInit) => Promise<unknown> }))
vi.mock('../src/stores/session', () => ({ useSession: () => fixture.session }))
vi.mock('../src/lib/api', () => ({ api: (path: string, init?: RequestInit) => fixture.handler(path, init) }))
import * as apiModule from '../src/lib/api'
import * as statusNamesLib from '../src/lib/statusNames'
import * as displayLanguageModule from '../src/lib/displayLanguage'
import * as identityScopeModule from '../src/lib/useIdentityScope'
import * as storeModule from '../src/stores/statusNames'

const who = (id: string) => ({ tenant: { id: `tenant-${id}`, name: id }, principal: { id: `person-${id}`, name: id, kind: 'person' } })
const later = (): Body => { let resolve!: (value: unknown) => void; const promise = new Promise<unknown>(done => { resolve = done }); return { promise, resolve } }
async function settle() { for (let i = 0; i < 8; i++) await Promise.resolve(); await Vue.nextTick() }
const typed = (value: string) => ({ target: { value } }) as unknown as Event

// The server per workspace: reads answer at once; each save answers with a body
// the test hands out, so a person change can fall between the answer and its body.
let servers: Record<string, unknown>, puts: { tenant: string; body: unknown }[], answers: { status: number; body: Body }[], pinia: Pinia
const scopes: Vue.EffectScope[] = []
beforeEach(() => {
  setActivePinia(pinia = createPinia())
  fixture.session = Vue.reactive({ identity: who('a'), authenticationCurrent: () => true })
  servers = { 'tenant-a': { revision: 5, names: {} }, 'tenant-b': { revision: 1, names: { in_progress: { en: 'Doing' } } } }
  puts = []; answers = []
  fixture.handler = async (path, init) => {
    expect(path).toBe('/settings/status-names')
    const tenant = (fixture.session.identity as ReturnType<typeof who>).tenant.id
    if (init?.method === 'PUT') {
      puts.push({ tenant, body: JSON.parse(String(init.body)) })
      const answer = answers.shift()!
      return { ok: answer.status < 300, status: answer.status, json: () => answer.body.promise }
    }
    const snapshot = structuredClone(servers[tenant])
    return { ok: true, status: 200, json: async () => snapshot }
  }
})
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); disposePinia(pinia); statusNamesLib.setStatusNames({}) })

function source() {
  // Baseline mode runs the same assertions against the card as the gate reviewed it.
  const path = 'web/src/components/settings/StatusNamesCard.vue'
  return process.env.AEON_1401_FIX6_BASELINE === '1' ? execFileSync('git', ['show', `99c853857:${path}`], { encoding: 'utf8' }) : readFileSync(new URL('../src/components/settings/StatusNamesCard.vue', import.meta.url), 'utf8')
}
type Card = {
  drafts: statusNamesLib.StatusNameDrafts; problems: Vue.Ref<Map<string, { code: string }>>; busy: Vue.Ref<boolean>; footer: Vue.Ref<string>; failed: Vue.Ref<boolean>; focused: string
  edit: (key: statusNamesLib.StatusNameKey, locale: statusNamesLib.StatusLocale, event: Event) => void
  blurred: (key: statusNamesLib.StatusNameKey, locale: statusNamesLib.StatusLocale) => void
  commit: (field: string) => void
}
async function mount() {
  const { descriptor } = parse(source())
  const { content } = compileScript(descriptor, { id: 'status-names-card' })
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onBeforeUnmount: Vue.onScopeDispose }, '../../lib/api': apiModule, '../../lib/authz': { can: () => true }, '../../lib/displayLanguage': displayLanguageModule,
    '../../lib/statusNames': statusNamesLib, '../../lib/useIdentityScope': identityScopeModule, '../../stores/profile': { useProfile: () => ({ profile: null }) },
    '../../stores/session': { useSession: () => fixture.session }, '../../stores/statusNames': storeModule, '../AppIcon.vue': {}, '../work/StatusIcon.vue': {}, './SettingsCard.vue': {},
  }
  const exports: { default?: { setup: (props: object, ctx: object) => unknown } } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => { if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`); return modules[id] }, exports)
  const store = storeModule.useStatusNames()
  await store.load()
  const scope = Vue.effectScope(); scopes.push(scope)
  const card = scope.run(() => exports.default!.setup({}, { expose: () => {} }))! as Card
  await settle()
  expect(store.value).toEqual({ revision: 5, names: {} })
  return { card, store }
}
// Person A renames In progress; the server answers, and its body is still being read when B signs in.
async function saveThenSwitch(card: Card, store: ReturnType<typeof storeModule.useStatusNames>, status: number) {
  const body = later(); answers.push({ status, body })
  card.focused = 'in_progress.en'; card.edit('in_progress', 'en', typed('Building')); card.blurred('in_progress', 'en')
  await settle()
  expect(puts).toEqual([{ tenant: 'tenant-a', body: { revision: 5, names: { in_progress: { en: 'Building' } } } }])
  fixture.session.identity = who('b')
  await store.load(); await settle()
  expect(store.value).toEqual({ revision: 1, names: { in_progress: { en: 'Doing' } } })
  expect(card.drafts.in_progress).toEqual({ en: 'Doing', de: '' })
  return body
}

it('a save answered while the person changes leaves the next person’s names and drafts alone', async () => {
  const { card, store } = await mount()
  const body = await saveThenSwitch(card, store, 200)
  card.focused = 'qa.en'; card.edit('qa', 'en', typed('Check'))
  body.resolve({ revision: 6, names: { in_progress: { en: 'Building' } } }); await settle()
  expect(store.value).toEqual({ revision: 1, names: { in_progress: { en: 'Doing' } } })
  expect(statusNamesLib.statusName('in_progress')).toBe('Doing')
  expect(card.drafts.in_progress).toEqual({ en: 'Doing', de: '' })
  expect(card.drafts.qa).toEqual({ en: 'Check', de: '' })
  expect(card.footer.value).toBe('')
  expect(card.failed.value).toBe(false)
  expect(card.busy.value).toBe(false)
})

it('a conflict answered while the person changes keeps the next person’s refused draft', async () => {
  const { card, store } = await mount()
  const body = await saveThenSwitch(card, store, 409)
  card.focused = 'in_progress.en'; card.edit('in_progress', 'en', typed('Done')); card.blurred('in_progress', 'en')
  expect(card.problems.value.get('in_progress.en')?.code).toBe('taken')
  expect(card.footer.value).toBe('Not saved. Fix the marked name.')
  const reads = vi.spyOn(store, 'load')
  body.resolve({ error: 'status names changed; reload before saving' }); await settle()
  expect(reads).not.toHaveBeenCalled()
  expect(card.drafts.in_progress).toEqual({ en: 'Done', de: '' })
  expect(card.problems.value.get('in_progress.en')?.code).toBe('taken')
  expect(card.footer.value).toBe('Not saved. Fix the marked name.')
  expect(card.failed.value).toBe(true)
  expect(store.value).toEqual({ revision: 1, names: { in_progress: { en: 'Doing' } } })
})

it('the earlier person’s save ending never starts a second save beside the next person’s', async () => {
  const { card, store } = await mount()
  const first = await saveThenSwitch(card, store, 200), own = later()
  answers.push({ status: 200, body: own })
  card.focused = 'qa.en'; card.edit('qa', 'en', typed('Check')); card.blurred('qa', 'en'); await settle()
  // B edits again while B's save is out: it waits for that save.
  card.focused = 'done.en'; card.edit('done', 'en', typed('Shipped')); card.blurred('done', 'en'); await settle()
  expect(puts.slice(1)).toEqual([{ tenant: 'tenant-b', body: { revision: 1, names: { in_progress: { en: 'Doing' }, qa: { en: 'Check' } } } }])
  first.resolve({ revision: 6, names: { in_progress: { en: 'Building' } } }); await settle()
  expect(puts).toHaveLength(2)
  expect(card.busy.value).toBe(true)
  expect(card.footer.value).toBe('Saving…')
  // B's own answer ends the save; the waiting edit follows under the new revision.
  answers.push({ status: 200, body: later() })
  own.resolve({ revision: 2, names: { in_progress: { en: 'Doing' }, qa: { en: 'Check' } } }); await settle()
  expect(store.value).toEqual({ revision: 2, names: { in_progress: { en: 'Doing' }, qa: { en: 'Check' } } })
  expect(puts.slice(2)).toEqual([{ tenant: 'tenant-b', body: { revision: 2, names: { in_progress: { en: 'Doing' }, qa: { en: 'Check' }, done: { en: 'Shipped' } } } }])
})
