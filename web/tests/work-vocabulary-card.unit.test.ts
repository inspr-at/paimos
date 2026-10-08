// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vocabulary from '../src/lib/workVocabulary'
import { flatten, mountView, settle as viewSettle, textOf, type RenderNode } from './webcore-view-harness'
const scopes: Vue.EffectScope[] = []
const apps: Vue.App[] = []
afterEach(() => { for (const s of scopes.splice(0)) s.stop(); for (const app of apps.splice(0)) app.unmount(); vi.unstubAllGlobals() })
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
async function settle() { for (let i = 0; i < 8; i++) await Promise.resolve(); await Vue.nextTick() }
const value = (revision: number) => ({ revision, leaf: { name: `Leaf ${revision}`, icon: '' }, levels: [] })
const response = (revision: number) => ({ ok: true, json: async () => value(revision) })
it('serializes a deferred Reload with Save and leaves the saved revision visible', async () => {
  vi.stubGlobal('navigator', { platform: 'Mac' })
  const reload = deferred<ReturnType<typeof response>>()
  const api = vi.fn().mockResolvedValueOnce(response(1)).mockImplementationOnce(() => reload.promise).mockResolvedValueOnce(response(2))
  const session = Vue.reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person' } } })
  const accept = vi.fn(<T,>(next: T) => next)
  const can = vi.fn((permission: string) => permission === 'settings.manage')
  const modules: Record<string, unknown> = { vue: Vue, '../../lib/api': { api }, '../../lib/authz': { can }, '../../stores/session': { useSession: () => session }, '../../stores/workVocabulary': { useWorkVocabulary: () => ({ accept }) }, '../../lib/workVocabulary': vocabulary, '../AppIcon.vue': {}, '../KeyCap.vue': {}, './SettingsCard.vue': {}, './IconChoice.vue': {} }
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

// AEON-996: levels read as they nest, with real icons. The risk is the card listing
// the leaf first and the preview naming only three levels, so every test below uses
// three configured levels (the old card hard-coded levels 1, 2 and the leaf).
const IconStub = { __esModule: true, default: { props: ['name'], setup: (p: { name: string }) => () => Vue.h('svg', { 'data-icon': p.name }) } }
const IconChoiceStub = { __esModule: true, default: { props: ['modelValue', 'label', 'fallback'], setup: (p: Record<string, string>) => () => Vue.h('i', { 'data-label': p.label, 'data-fallback': p.fallback, 'data-value': p.modelValue }) } }
const three = () => ({ revision: 3, leaf: { name: 'Arbeitsschritt', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }, { name: 'Geschichte', icon: '' }, { name: '', icon: '' }] })
const norm = (text: string) => text.replace(/\s+/g, ' ').trim()
const fire = (node: RenderNode, name: string, ...args: unknown[]) => (([] as Array<(...a: unknown[]) => unknown>).concat(node.props[name] as never)).forEach(handler => handler(...args))
async function mountCard(admin: boolean) {
  vi.stubGlobal('navigator', { platform: 'Mac' })
  const vocab = three()
  const api = vi.fn(async (_path: string, init?: RequestInit) => ({ ok: true, json: async () => init?.method === 'PUT' ? { ...JSON.parse(String(init.body)), revision: vocab.revision + 1 } : vocab }))
  const session = Vue.reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person' } } })
  const view = mountView('../src/components/settings/WorkVocabularyCard.vue', {
    '../../lib/api': { api }, '../../lib/authz': { can: () => admin }, '../../stores/session': { useSession: () => session },
    '../../stores/workVocabulary': { useWorkVocabulary: () => ({ value: vocab, accept: <T,>(next: T) => next }) },
    '../../lib/workVocabulary': vocabulary, '../AppIcon.vue': IconStub, './IconChoice.vue': IconChoiceStub,
  })
  apps.push(view.app); await viewSettle()
  const all = flatten(view.root)
  return { api, ...view, all, labels: all.filter(el => el.tag === 'label').map(textOf).map(norm), preview: all.find(el => el.props.class === 'preview')! }
}
it('lists the levels top-down, ending with the leaf, and previews the whole chain', async () => {
  const card = await mountCard(true)
  expect(card.labels).toEqual(['Top level', 'Level 2', 'Level 3', 'Leaf (work item)'])
  const inputs = card.all.filter(el => el.tag === 'input')
  expect(inputs.map(el => el.props.placeholder)).toEqual(['Epic', 'Story', 'Level 3', 'Ticket'])
  expect(inputs.map(el => el.props.value)).toEqual(['Vorhaben', 'Geschichte', '', 'Arbeitsschritt'])
  // Each picker knows its level's default icon, so a blank choice still shows one.
  const pickers = card.all.filter(el => el.tag === 'i')
  expect(pickers.map(el => el.props['data-label'])).toEqual(['Top level icon', 'Level 2 icon', 'Level 3 icon', 'Leaf icon'])
  expect(pickers.map(el => el.props['data-fallback'])).toEqual(['epic', 'layers', 'layers', 'ticket'])
  expect(pickers.map(el => el.props['data-value'])).toEqual(['tree', '', '', 'check'])
  expect(norm(textOf(card.preview))).toBe('Vorhaben / Geschichte / Level 3 / Arbeitsschritt')
  expect(flatten(card.preview).filter(el => el.tag === 'svg').map(el => el.props['data-icon'])).toEqual(['tree', 'layers', 'layers', 'check'])
})
it('lists the same order and labels read-only', async () => {
  const card = await mountCard(false)
  expect(card.labels).toEqual([])
  const rows = card.all.filter(el => el.tag === 'dt').map(dt => [norm(textOf(dt)), norm(textOf(dt.parent!.children[1]))])
  expect(rows).toEqual([['Top level', 'Vorhaben'], ['Level 2', 'Geschichte'], ['Level 3', 'Level 3'], ['Leaf (work item)', 'Arbeitsschritt']])
  expect(norm(textOf(card.preview))).toBe('Vorhaben / Geschichte / Level 3 / Arbeitsschritt')
})
it('Add level puts the new level directly above the leaf and saves levels[0] as the top level', async () => {
  const card = await mountCard(true)
  fire(card.all.find(el => el.tag === 'button' && textOf(el).includes('Add level'))!, 'onClick')
  await viewSettle()
  expect(flatten(card.root).filter(el => el.tag === 'label').map(textOf).map(norm)).toEqual(['Top level', 'Level 2', 'Level 3', 'Level 4', 'Leaf (work item)'])
  expect(norm(textOf(flatten(card.root).find(el => el.props.class === 'preview')!))).toBe('Vorhaben / Geschichte / Level 3 / Level 4 / Arbeitsschritt')
  fire(flatten(card.root).find(el => el.tag === 'button' && textOf(el).includes('Save names'))!, 'onClick')
  await viewSettle()
  const put = card.api.mock.calls.find(call => call[1]?.method === 'PUT')!
  expect(JSON.parse(String(put[1]!.body)).levels.map((level: { name: string }) => level.name)).toEqual(['Vorhaben', 'Geschichte', '', ''])
})
it('the icon picker shows every icon with its name and chooses one', async () => {
  const emitted: string[] = []
  const panel = { __esModule: true, default: { props: ['label'], setup: (p: { label: string }, { slots }: Vue.SetupContext) => () => Vue.h('div', { role: 'dialog', 'aria-label': p.label }, slots.default?.()) } }
  const view = mountView('../src/components/settings/IconChoice.vue', { '../../lib/workVocabulary': vocabulary, '../AppIcon.vue': IconStub, '../work/FloatingPanel.vue': panel },
    { modelValue: 'tree', label: 'Level 2 icon', fallback: 'layers', 'onUpdate:modelValue': (value: string) => emitted.push(value) })
  apps.push(view.app); await viewSettle()
  const trigger = view.find(el => el.props['aria-haspopup'] === 'listbox')
  const iconOf = (el: RenderNode) => flatten(el).find(child => child.tag === 'svg')!.props['data-icon']
  expect(trigger.props['aria-label']).toBe('Level 2 icon: Tree'); expect(iconOf(trigger)).toBe('tree')
  expect(flatten(view.root).filter(el => el.props.role === 'listbox')).toHaveLength(0)
  // ArrowDown opens it from the trigger, as a native select would.
  fire(trigger, 'onKeydown', { key: 'ArrowDown', preventDefault() {} }); await viewSettle()
  expect(flatten(view.root).find(el => el.props.role === 'dialog')!.props['aria-label']).toBe('Choose level 2 icon')
  const options = flatten(view.root).filter(el => el.props.role === 'option')
  expect(options.map(el => [norm(textOf(el)), iconOf(el)])).toEqual([['Default Layers', 'layers'], ['Ticket', 'ticket'], ['Epic', 'epic'], ['Task', 'task'], ['Layers', 'layers'], ['Tree', 'tree'], ['Folder', 'folder'], ['Check', 'check'], ['Box', 'box']])
  expect(options.filter(el => el.props['aria-selected']).map(el => norm(textOf(el)))).toEqual(['Tree'])
  const focus = vi.fn(); Object.assign(trigger, { focus })
  fire(options[2], 'onClick'); await viewSettle()
  expect(emitted).toEqual(['epic'])
  expect(flatten(view.root).filter(el => el.props.role === 'listbox')).toHaveLength(0)
  expect(focus).toHaveBeenCalledOnce()
})
