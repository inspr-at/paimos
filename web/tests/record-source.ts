// SPDX-License-Identifier: AGPL-3.0-only
// Exercise the actual SFC setup code with deterministic IO, without a browser.
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import { execFileSync } from 'node:child_process'

export function deferred<T = void>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
export async function flush() { for (let i = 0; i < 12; i++) await Promise.resolve(); await Vue.nextTick() }
// Imports are replaced at the boundary; every tested action/watcher is from the
// source file, not a second implementation. No sleeps or elapsed-time assertions.
function sourceText(path: string) {
  const baseline = process.env.AEON_RECORD_BASELINE
  return baseline ? execFileSync('git', ['show', `${baseline === '1' ? 'HEAD' : baseline}:web/src/${path}`], { encoding: 'utf8', cwd: new URL('../../', import.meta.url) }) : readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
}
export function sourceModule<T>(path: string, modules: Record<string, unknown>): T {
  const { outputText } = ts.transpileModule(sourceText(path), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === 'vue') return Vue
    if (!(id in modules)) throw new Error(`Missing test dependency: ${id}`)
    return modules[id]
  }, exports)
  return exports as T
}
export function setupSource(path: string, props: Record<string, unknown>, modules: Record<string, unknown>) {
  const { descriptor } = parse(sourceText(path))
  const { content } = compileScript(descriptor, { id: path })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: { setup: Function } } = {}
  const scope = Vue.effectScope()
  const importedVue = { ...Vue, onMounted: () => {}, onBeforeUnmount: Vue.onScopeDispose, useId: () => 'record-test', useModel: () => Vue.ref('') }
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === 'vue') return importedVue
    if (id.endsWith('.vue')) return { __esModule: true, default: {} }
    if (id.endsWith('.css')) return {}
    if (!(id in modules)) throw new Error(`Missing test dependency: ${id}`)
    return modules[id]
  }, exports)
  const emitted: unknown[][] = []
  const state = scope.run(() => exports.default!.setup(props, { expose: () => {}, emit: (...args: unknown[]) => emitted.push(args) }))
  return { state, emitted, stop: () => scope.stop() }
}
