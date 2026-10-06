// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vocabulary from '../src/lib/workVocabulary'
const scopes: Vue.EffectScope[] = []
afterEach(() => { for (const s of scopes.splice(0)) s.stop(); vi.unstubAllGlobals() })
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
async function settle() { for (let i = 0; i < 8; i++) await Promise.resolve(); await Vue.nextTick() }
const value = (revision: number) => ({ revision, leaf: { name: `Leaf ${revision}`, icon: '' }, levels: [] })
const response = (revision: number) => ({ ok: true, json: async () => value(revision) })
it('serializes a deferred Reload with Save and leaves the saved revision visible', async () => {
  vi.stubGlobal('navigator', { platform: 'Mac' })
  const reload = deferred<ReturnType<typeof response>>()
  const api = vi.fn().mockResolvedValueOnce(response(1)).mockImplementationOnce(() => reload.promise).mockResolvedValueOnce(response(2))
  const session = Vue.reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person' } } })
  const accept = vi.fn()
  const can = vi.fn((permission: string) => permission === 'settings.manage')
  const modules: Record<string, unknown> = { vue: Vue, '../../lib/api': { api }, '../../lib/authz': { can }, '../../stores/session': { useSession: () => session }, '../../stores/workVocabulary': { useWorkVocabulary: () => ({ accept }) }, '../../lib/workVocabulary': vocabulary, '../AppIcon.vue': {}, '../KeyCap.vue': {}, './SettingsCard.vue': {} }
  const { descriptor } = parse(readFileSync(new URL('../src/components/settings/WorkVocabularyCard.vue', import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: 'vocabulary-test' })
  const exports: { default?: { setup: (props: object, ctx: object) => unknown } } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => { if (!(id in modules)) throw new Error(id); return modules[id] }, exports)
  const scope = Vue.effectScope(); scopes.push(scope)
  const card = scope.run(() => exports.default!.setup({}, { expose: () => {} })) as { load: () => Promise<void>; save: () => Promise<void>; busy: Vue.Ref<boolean>; draft: Vue.Ref<ReturnType<typeof value>>; message: Vue.Ref<string> }
  await settle()
  const pending = card.load()
  // A submit while the read is pending must send no PUT.
  await card.save()
  expect(api).toHaveBeenCalledTimes(2)
  expect(card.busy.value).toBe(true)
  reload.resolve(response(1)); await pending
  await card.save()
  expect(card.draft.value.revision).toBe(2)
  expect(card.message.value).toBe('Workspace names saved.')
  expect(accept).toHaveBeenCalledTimes(3)
  expect(accept).toHaveBeenLastCalledWith(value(2))
  expect(can).toHaveBeenCalledWith('settings.manage')
})
