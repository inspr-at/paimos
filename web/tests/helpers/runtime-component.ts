// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

// Node-mode Vite emits SSR functions. Compile the production client component
// so lifecycle, watches, event handlers and rendering run in a small Vue host.
export function clientComponent(source: URL, imports: Record<string, unknown>, renderTemplate = true): Vue.Component {
  const { descriptor } = parse(readFileSync(source, 'utf8'))
  const { content } = compileScript(descriptor, { id: source.pathname, inlineTemplate: renderTemplate,
    templateOptions: { compilerOptions: { hoistStatic: false } } })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === 'vue') return Vue
    if (Object.hasOwn(imports, id)) return imports[id]
    throw new Error(`Unexpected component dependency: ${id}`)
  }, exports)
  if (!renderTemplate) exports.default!.render = () => null
  return exports.default!
}

export type RuntimeNode = { tag: string; text: string; props: Record<string, unknown>; children: RuntimeNode[]; parent: RuntimeNode | null }
const node = (tag: string, text = ''): RuntimeNode => ({ tag, text, props: {}, children: [], parent: null })
function detach(child: RuntimeNode) {
  const siblings = child.parent?.children
  if (siblings) siblings.splice(siblings.indexOf(child), 1)
  child.parent = null
}
const renderer = Vue.createRenderer<RuntimeNode, RuntimeNode>({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (el, text) => { el.text = text },
  setElementText: (el, text) => { el.text = text; el.children = [] },
  parentNode: child => child.parent,
  nextSibling: child => child.parent?.children[child.parent.children.indexOf(child) + 1] ?? null,
  patchProp: (el, key, _previous, value) => { el.props[key] = value },
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(child)
    else parent.children.splice(index, 0, child)
  },
  remove: detach,
})

export function mountRuntime(component: Vue.Component, props: Record<string, unknown> = {}) {
  const root = node('root'), app = renderer.createApp(component, props)
  app.component('RouterLink', { props: ['to'], setup: (props, { slots }) => () => Vue.h('a', { href: props.to }, slots.default?.()) })
  const instance = app.mount(root)
  return { root, instance, unmount: () => app.unmount() }
}
export const runtimeText = (root: RuntimeNode): string => root.text + root.children.map(runtimeText).join('')
export function runtimeNodes(root: RuntimeNode, predicate: (node: RuntimeNode) => boolean): RuntimeNode[] {
  return [...(predicate(root) ? [root] : []), ...root.children.flatMap(child => runtimeNodes(child, predicate))]
}
