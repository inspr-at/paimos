// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

// Real SFC watchers and actions in an in-memory Vue host. Geometry remains
// the separate Playwright gate; these tests do not claim browser evidence.
const session = Vue.reactive({ identity: { principal: { id: 'person-a' }, tenant: { id: 'tenant-a' } } })
const requests: { path: string; options?: RequestInit; resolve: (response: Response) => void }[] = []
const api = vi.fn((path: string, options?: RequestInit) => new Promise<Response>(resolve => requests.push({ path, options, resolve })))
function component(file: string): Vue.Component {
  const { descriptor } = parse(readFileSync(new URL(`../src/components/${file}.vue`, import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: file, inlineTemplate: true })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    // Input updates are dispatched directly through the generated model
    // callback; this host has no DOM event system or checkbox geometry.
    if (id === 'vue') return { ...Vue, vModelText: { beforeMount: (el: Node, binding: { value: unknown }) => { el.props.value = binding.value }, beforeUpdate: (el: Node, binding: { value: unknown }) => { el.props.value = binding.value } }, vModelCheckbox: {} }
    if (id === '../../lib/api') return { api }
    if (id === '../../stores/session') return { useSession: () => session }
    if (id === '../../lib/brand') return { brand: Vue.ref({ short_name: 'Aeon' }) }
    if (id === './SettingsCard.vue') return Vue.defineComponent({ setup: (_, { slots }) => () => Vue.h('section', slots.default?.()) })
    throw new Error(`Unexpected dependency ${id}`)
  }, exports)
  return exports.default!
}
type Node = { tag: string; text: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string): Node => ({ tag, text: '', props: {}, children: [], parent: null })
function detach(child: Node) { const siblings = child.parent?.children; if (siblings) siblings.splice(siblings.indexOf(child), 1); child.parent = null }
const renderer = Vue.createRenderer<Node, Node>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: text => ({ ...node('#comment'), text }),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text; el.children = [] },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => { detach(child); child.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; if (index < 0) parent.children.push(child); else parent.children.splice(index, 0, child) },
})
const Parent = component('work/ParentBenefitGeneration'), Provider = component('settings/ModelProviderCard')
const apps: Vue.App[] = []
function mount(art: Vue.Component, props = {}) { const root = node('root'); const app = renderer.createApp({ render: () => Vue.h(art, props) }); app.mount(root); apps.push(app); return root }
function all(root: Node): Node[] { return [root, ...root.children.flatMap(all)] }
function text(root: Node): string { return root.text + root.children.map(text).join(' ') }
function find(root: Node, predicate: (n: Node) => boolean) { const found = all(root).find(predicate); expect(found).toBeDefined(); return found! }
async function answer(index: number, value: unknown, code = 200) { requests[index].resolve({ ok: code < 400, status: code, json: async () => value } as Response); await Vue.nextTick(); await Vue.nextTick(); await Vue.nextTick() }
const failed = (generation: string) => ({ is_parent: true, status: 'failed', generation, revision: '2026-10-04T08:00:00Z', generated: false, error: 'Fixture generation failed.' })
const provider = (model: string) => ({ enabled: true, base_url: 'http://localhost:11434/v1', chat_model: model, embedding_model: '', features: { parent_benefits: false, crm_note_rewrite: false, embeddings: false }, revision: 1, has_api_key: false })
beforeEach(() => { vi.useFakeTimers(); requests.length = 0; api.mockClear(); session.identity.principal.id = 'person-a'; session.identity.tenant.id = 'tenant-a' })
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.useRealTimers() })

it('retry captures its generation/revision and discards a result after navigation', async () => {
  const props = Vue.reactive({ nodeId: 'parent-a', editable: true })
  const root = mount(Parent, props)
  await answer(0, failed('attempt-a'))
  const retry = find(root, n => n.tag === 'button' && text(n) === 'Retry generation')
  ;(retry.props.onClick as () => void)()
  expect(requests[1].path).toBe('/nodes/parent-a/benefit-generation/retry')
  expect(JSON.parse(requests[1].options!.body as string)).toEqual({ expected_generation: 'attempt-a', expected_revision: '2026-10-04T08:00:00Z' })
  props.nodeId = 'parent-b'; await Vue.nextTick()
  await answer(2, { ...failed('attempt-b'), error: 'Parent B failed.' })
  await answer(1, { ...failed('attempt-a'), status: 'queued' }, 202)
  expect(text(root)).toContain('Parent B failed.')
  expect(text(root)).not.toContain('generation is queued')
})

it('tenant changes fence an old status read even for the same node id', async () => {
  const root = mount(Parent, { nodeId: 'shared-id', editable: true })
  const oldSignal = requests[0].options!.signal!
  session.identity.tenant.id = 'tenant-b'; await Vue.nextTick()
  expect(oldSignal.aborted).toBe(true)
  await answer(1, { ...failed('attempt-b'), error: 'Tenant B failed.' })
  await answer(0, { ...failed('attempt-a'), status: 'generated', generated: true })
  expect(text(root)).toContain('Tenant B failed.')
  expect(text(root)).not.toContain('Generated')
})

it('an old provider save cannot restore another person’s opt-in or drafts', async () => {
  const root = mount(Provider)
  await answer(0, provider('person-a-model'))
  const chat = find(root, n => n.props.id === 'chat-model')
  ;(chat.props['onUpdate:modelValue'] as (value: string) => void)('changed-a-model')
  const form = find(root, n => n.tag === 'form')
  ;(form.props.onSubmit as (event: { preventDefault: () => void }) => void)({ preventDefault() {} })
  expect(JSON.parse(requests[1].options!.body as string)).toMatchObject({ chat_model: 'changed-a-model', expected_revision: 1 })
  session.identity.principal.id = 'person-b'; await Vue.nextTick()
  await answer(2, provider('person-b-model'))
  await answer(1, { ...provider('changed-a-model'), revision: 2, features: { parent_benefits: true } })
  expect(find(root, n => n.props.id === 'chat-model').props.value).toBe('person-b-model')
  expect(text(root)).not.toContain('Saved.')
})
