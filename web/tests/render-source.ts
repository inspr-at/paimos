// SPDX-License-Identifier: AGPL-3.0-only
// Render the production client SFC into a small Vue host. IO and child surfaces
// are explicit boundaries; conditionals, bindings, events and watchers execute.
import * as Vue from 'vue'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { sourceText } from './record-source.ts'

export interface RenderNode { tag: string; text: string; props: Record<string, unknown>; children: RenderNode[]; parent: RenderNode | null }
const node = (tag: string, text = ''): RenderNode => ({ tag, text, props: {}, children: [], parent: null })
function detach(child: RenderNode) {
  const siblings = child.parent?.children
  if (siblings) { const index = siblings.indexOf(child); if (index >= 0) siblings.splice(index, 1) }
  child.parent = null
}
const host = Vue.createRenderer<RenderNode, RenderNode>({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: text => node('#comment', text),
  setText: (element, text) => { element.text = text },
  setElementText: (element, text) => { element.text = text; for (const child of [...element.children]) detach(child) },
  parentNode: element => element.parent,
  nextSibling: element => element.parent?.children[element.parent.children.indexOf(element) + 1] ?? null,
  patchProp: (element, key, _previous, value) => { element.props[key] = value },
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(child)
    else parent.children.splice(index, 0, child)
  },
  remove: detach,
})

const boundary = Vue.defineComponent({
  inheritAttrs: false,
  setup(_props, { slots, attrs, expose }) {
    // Focus/geometry stay in the retained browser owners. These child methods
    // are surface boundaries; the tested parent still dispatches its own IO.
    expose({ close() {}, focusFirst() {}, show() {} })
    return () => Vue.h('surface', attrs, [...(slots.trigger?.({ phone: false }) ?? []), ...(slots.default?.() ?? [])])
  },
})

export function renderSource(path: string, props: Record<string, unknown>, modules: Record<string, unknown>) {
  const { descriptor } = parse(sourceText(path))
  const { content } = compileScript(descriptor, { id: path, inlineTemplate: true,
    templateOptions: { compilerOptions: { hoistStatic: false } } })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === 'vue') {
      // A virtual host has no native input events. Preserve the compiled
      // model-update callback and render its value; real typing stays in the
      // retained browser case. This adapter owns no application state.
      const modelValue = (element: RenderNode, binding: { value: unknown }) => { element.props.value = binding.value }
      return { ...Vue, vModelText: { created: modelValue, beforeUpdate: modelValue } }
    }
    if (id in modules) return modules[id]
    if (id.endsWith('.vue')) return { __esModule: true, default: boundary }
    if (id.endsWith('.css')) return {}
    throw new Error(`Missing render dependency: ${id}`)
  }, exports)
  const root = node('root')
  const app = host.createApp({ render: () => Vue.h(exports.default!, props) })
  app.mount(root)
  return { root, stop: () => app.unmount() }
}

export function renderText(root: RenderNode): string {
  return root.tag === '#comment' ? '' : root.text + root.children.map(renderText).join('')
}
export function renderNodes(root: RenderNode, predicate: (node: RenderNode) => boolean): RenderNode[] {
  return [...(predicate(root) ? [root] : []), ...root.children.flatMap(child => renderNodes(child, predicate))]
}
