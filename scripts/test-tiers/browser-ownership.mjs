// SPDX-License-Identifier: AGPL-3.0-only
import { dirname, posix } from 'node:path'
import { boundedText } from './inputs.mjs'
import { graphIncomplete } from './core.mjs'

// Observations augment imports, never replace them. Navigation owns the
// application entry/router closure; URL-loaded HTML owns its script imports.
// Computed imports/navigation conservatively own all possible local inputs.
export function ownershipGraph(checkout, imports, read = file => boundedText(checkout, file), { maxEdges = 250_000 } = {}) {
  if (imports[graphIncomplete]) return imports
  if (Object.keys(imports).length > 20_000) return { [graphIncomplete]: 'ownership file count bound' }
  const graph = Object.fromEntries(Object.entries(imports).map(([file, deps]) => [file, [...deps]]))
  const broad = Object.keys(graph).filter(file => !/\.(?:spec|test)\.ts$/.test(file))
  let edges = Object.values(graph).reduce((sum, deps) => sum + deps.length, 0)
  const edge = (owner, target) => {
    if (!Object.hasOwn(graph, target)) throw new Error(`unmapped route/harness input: ${target}`)
    if (!graph[owner].includes(target)) {
      if (++edges > maxEdges) throw new Error('ownership edge count bound')
      graph[owner].push(target)
    }
  }
  try {
    for (const file of Object.keys(graph)) {
      const source = read(file)
      if (typeof source !== 'string') throw new Error(`unreadable ownership input: ${file}`)
      for (const match of source.matchAll(/(?:["'`])(\/(?:src|tests|e2e)\/[a-zA-Z0-9_./-]+\.(?:ts|js|mjs|vue|html))(?:[?"'`])/g)) {
        const target = match[1].slice(1)
        if (Object.hasOwn(graph, target)) edge(file, target)
        // Some tests fulfil synthetic harness URLs from inline HTML. Their
        // unmapped references own the entire local input set conservatively.
        else for (const possible of broad) if (possible !== file) edge(file, possible)
      }
      if (file.endsWith('.html')) for (const match of source.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["']/g)) {
        const target = match[1].split('?')[0]
        if (/^https?:/.test(target)) throw new Error(`external harness script: ${file}`)
        edge(file, target.startsWith('/') ? target.slice(1) : posix.normalize(posix.join(dirname(file), target)))
      }
      if (/\.goto\s*\(/.test(source)) {
        for (const entry of ['src/main.ts', 'src/router.ts']) edge(file, entry)
        // URL parameters and helper variables may resolve to any harness.
        for (const target of broad.filter(target => target.endsWith('.html'))) edge(file, target)
      }
      if (/\bimport\s*\(\s*(?!["'])|\bimport\.meta\.glob/.test(source)) for (const target of broad) if (target !== file) edge(file, target)
    }
    return graph
  } catch (error) { return { [graphIncomplete]: error.message } }
}
