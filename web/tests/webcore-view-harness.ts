// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

export type RenderNode = { tag: string; text: string; props: Record<string, unknown>; children: RenderNode[]; parent: RenderNode | null }
const node = (tag: string, text = ''): RenderNode => ({ tag, text, props: {}, children: [], parent: null })
function detach(child: RenderNode) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = null }
const renderer = Vue.createRenderer<RenderNode, RenderNode>({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text; el.children = [] },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => { detach(child); child.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child) },
})
export const textOf = (el: RenderNode): string => el.text + el.children.map(textOf).join(' ')
export const flatten = (el: RenderNode): RenderNode[] => [el, ...el.children.flatMap(flatten)]
export const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); await Vue.nextTick() }
export function mountView(path: string, modules: Record<string, unknown>, props: Record<string, unknown> = {}) {
  const { descriptor } = parse(readFileSync(new URL(path, import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: 'webcore-test', inlineTemplate: true, templateOptions: { compilerOptions: { hoistStatic: false } } })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  const stub = { __esModule: true, default: { setup: (_props: unknown, { slots }: Vue.SetupContext) => () => Vue.h('section', {}, slots.default?.()) } }
  const model = { created: (el: RenderNode, binding: Vue.DirectiveBinding) => { el.props.value = binding.value }, beforeUpdate: (el: RenderNode, binding: Vue.DirectiveBinding) => { el.props.value = binding.value } }
  const radio = { created: (el: RenderNode, binding: Vue.DirectiveBinding) => { el.props.checked = el.props.value === binding.value }, beforeUpdate: (el: RenderNode, binding: Vue.DirectiveBinding) => { el.props.checked = el.props.value === binding.value } }
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === 'vue') return { ...Vue, vModelText: model, vModelSelect: model, vModelCheckbox: model, vModelRadio: radio }
    if (id in modules) return modules[id]
    if (id.endsWith('.vue')) return stub
    throw new Error(`Unmocked dependency ${id}`)
  }, exports)
  const app = renderer.createApp(exports.default!, props)
  app.component('RouterLink', { setup: (_props, { slots }) => () => Vue.h('a', {}, slots.default?.()) })
  const root = node('root'); app.mount(root)
  return { root, app, find: (predicate: (el: RenderNode) => boolean) => flatten(root).find(predicate)! }
}
