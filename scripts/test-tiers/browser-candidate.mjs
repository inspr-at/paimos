// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1485 experiment. No workflow/planner/runner imports this module.
import { posix } from 'node:path'
import { isDocsLike, validImpactPaths } from './core.mjs'

export const candidateID = 'impact-ci-import-closure-v1'
export const candidateBounds = Object.freeze({ files: 20_000, fileBytes: 2 * 1024 * 1024,
  totalBytes: 64 * 1024 * 1024, edges: 250_000, cases: 20_000, extraCases: 300 })
const modulePath = file => typeof file === 'string' && /^(?:src|tests|e2e)\/.+\.(?:ts|js|mjs|vue|json|css|html)$/.test(file) &&
  file.length <= 1024 && !file.split('/').some(part => ['', '.', '..'].includes(part)) && !/[\\\x00-\x1f\x7f]/.test(file)
const specPath = file => modulePath(file) && /^(?:tests|e2e)\/.+\.spec\.ts$/.test(file)
const suffixes = ['', '.ts', '.js', '.mjs', '.vue', '.json', '.css', '/index.ts', '/index.js']
const sorted = values => [...new Set(values)].sort()

// The broad rule is explicit, independent of the observed failing spec. These
// process-wide inputs cannot establish isolation between browser harnesses.
export const sharedBrowserInput = file => /^(?:src\/(?:styles|stores)\/|src\/(?:main\.ts|router\.ts|App\.vue)$|src\/lib\/(?:api|auth|session)\.ts$)/.test(file)

// Pure historical-tree adapter: imports, HTML/Vue script/style sources, CSS
// imports, inline Vite URLs and route navigation. Unsupported syntax is kept as
// uncertainty, never silently treated as an absent dependency.
export function candidateGraph(files, bounds = candidateBounds) {
  const unavailable = reason => ({ complete: false, reason })
  if (!(files instanceof Map) || !files.size || files.size > bounds.files) return unavailable('source file count bound or missing tree')
  let bytes = 0
  for (const [file, source] of files) {
    if (!modulePath(file) || typeof source !== 'string' || Buffer.byteLength(source) >= bounds.fileBytes)
      return unavailable('invalid source or per-file byte bound')
    bytes += Buffer.byteLength(source)
    if (bytes >= bounds.totalBytes) return unavailable('source total byte bound')
  }
  const dependencies = new Map([...files.keys()].map(file => [file, new Set()]))
  const issues = new Map(), navigation = new Set()
  let edges = 0
  const issue = (file, reason) => {
    if (!issues.has(file)) issues.set(file, new Set())
    issues.get(file).add(reason)
  }
  const edge = (file, target) => {
    if (!dependencies.get(file).has(target)) {
      if (++edges > bounds.edges) throw new Error('ownership edge count bound')
      dependencies.get(file).add(target)
    }
  }
  const reference = (file, imported) => {
    imported = imported.split(/[?#]/)[0]
    if (!imported.startsWith('.') && !imported.startsWith('@/') && !/^\/(?:src|tests|e2e)\//.test(imported)) return
    const base = imported.startsWith('@/') ? 'src/' + imported.slice(2) : imported.startsWith('/') ? imported.slice(1) : posix.join(posix.dirname(file), imported)
    const target = suffixes.map(suffix => base + suffix).find(candidate => dependencies.has(candidate))
    if (target) edge(file, target)
    else issue(file, `unresolved local reference: ${imported}`)
  }
  try {
    const harnesses = [...files.keys()].filter(file => file.endsWith('.html'))
    for (const [file, source] of files) {
      for (const match of source.matchAll(/(?:\b(?:import|export)\s+(?:[\s\S]*?\s+from\s*)?|\bimport\s*\()\s*['"]([^'"]+)['"]/g)) reference(file, match[1])
      for (const match of source.matchAll(/<(?:script|style)\b[^>]*\bsrc=["']([^"']+)["']/g)) reference(file, match[1])
      for (const match of source.matchAll(/@import\s+(?:url\(\s*)?["']([^"']+)["']/g)) reference(file, match[1])
      for (const match of source.matchAll(/["'`](\/(?:src|tests|e2e)\/[a-zA-Z0-9_./-]+\.(?:ts|js|mjs|vue|json|css|html))(?:[?"'`])/g)) reference(file, match[1])
      if (/\bimport\s*\(\s*(?!["'\s])|\bimport\.meta\.glob|\brequire\s*\(/.test(source)) issue(file, 'computed module ownership')
      if (/\.goto\s*\(/.test(source)) {
        navigation.add(file)
        for (const entry of ['src/main.ts', 'src/router.ts', ...harnesses]) {
          if (dependencies.has(entry)) edge(file, entry)
          else issue(file, `missing navigation entry: ${entry}`)
        }
      }
    }
    const reverse = new Map([...files.keys()].map(file => [file, []]))
    for (const [owner, targets] of dependencies) for (const target of targets) reverse.get(target).push(owner)
    return { complete: true, dependencies, reverse, issues, navigation, edges }
  } catch (error) { return unavailable(error.message) }
}

function closure(seeds, edges) {
  const visited = new Set(seeds), queue = [...seeds]
  for (let at = 0; at < queue.length; at++) for (const target of edges.get(queue[at]) ?? []) {
    if (!visited.has(target)) { visited.add(target); queue.push(target) }
  }
  return visited
}

// An explicit research opt-in is required in addition to the existing switch.
// A full fallback means the *catalogue* here. S1's literal gated subset missed
// NIGHTLY regressions; presenting that subset as safe would repeat the defect.
// The cost of this escalation is reported, and it never qualifies narrowing.
export function candidateSelection({ paths, tests, graph, impact = 'off', event = 'pull_request', experiment = false,
  bounds = candidateBounds } = {}) {
  if (!Array.isArray(tests) || !tests.length || tests.length > bounds.cases || tests.some(row =>
    row.kind === 'browser' && (!specPath(row.file) || !['ESSENTIAL', 'GATED-FULL', 'NIGHTLY'].includes(row.tier))))
    throw new Error('invalid or oversized browser catalogue')
  const browser = tests.filter(row => row.kind === 'browser' && row.active !== false)
  const gated = browser.filter(row => row.tier !== 'NIGHTLY')
  const result = (selected, scope, reasons, mappedSpecs = [], shared = []) => ({ selector: candidateID, scope, reasons,
    selected, mappedSpecs, sharedInputs: shared, narrowed: scope === 'affected' })
  if (impact !== 'on' || experiment !== true) return result(gated, 'disabled', ['research candidate disabled or CI_IMPACT not on'])
  const full = (reasons, mappedSpecs = [], shared = []) => result(browser, 'catalogue', reasons, mappedSpecs, shared)
  if (event !== 'pull_request') return full(['non-PR event'])
  if (!validImpactPaths(paths) || !paths.length) return full(['missing, empty or invalid diff'])
  if (graph?.complete !== true) return full([`incomplete ownership graph: ${graph?.reason ?? 'unavailable'}`])
  const changed = paths.filter(file => /^web\/(?:src|tests|e2e)\//.test(file)).map(file => file.slice(4))
  const known = changed.filter(file => graph.dependencies.has(file))
  const impacted = closure(known, graph.reverse)
  // A newly added spec cannot be in the trusted base graph. Its explicit diff
  // path is still an owner; this never grants trust to its new import edges.
  const directSpecs = new Set(changed.filter(specPath))
  const mappedSpecs = sorted(browser.filter(row => impacted.has(row.file) || directSpecs.has(row.file)).map(row => row.file))
  const shared = sorted(changed.filter(sharedBrowserInput))
  const reasons = []
  if (paths.some(file => !isDocsLike(file) && !/^web\/(?:src|tests|e2e)\/.+\.(?:ts|js|mjs|vue|html)$/.test(file))) reasons.push('shared, CSS, data or unknown diff input')
  if (known.length !== changed.length) reasons.push('new, deleted, renamed or unmapped input')
  if (shared.length) reasons.push('shared-module fan-out requires full catalogue')
  if (!changed.length) reasons.push('no mapped browser input')
  // An unsupported import in any selected spec's forward closure invalidates
  // a narrow claim. Mapping diagnostics are still useful for miss analysis.
  const forward = closure(mappedSpecs, graph.dependencies)
  if ([...forward].some(file => graph.issues.has(file))) reasons.push('unresolved or computed ownership in selected closure')
  // An unresolved importer outside that closure may hide another consumer.
  if (graph.issues.size) reasons.push('unresolved ownership elsewhere in trusted graph')
  if (browser.some(row => !graph.dependencies.has(row.file))) reasons.push('catalogue spec absent from trusted base graph')
  const browserInputs = closure([...graph.dependencies.keys()].filter(specPath), graph.dependencies)
  if (changed.some(file => !browserInputs.has(file) && !directSpecs.has(file))) reasons.push('changed input has no browser owner')
  if (reasons.length) return full(reasons, mappedSpecs, shared)
  const owners = new Set(mappedSpecs)
  const selected = browser.filter(row => row.tier === 'ESSENTIAL' || owners.has(row.file))
  if (selected.length - browser.filter(row => row.tier === 'ESSENTIAL').length > bounds.extraCases)
    return full(['affected fan-out exceeds 300 additional cases'], mappedSpecs, shared)
  return result(selected, 'affected', ['essential plus import, inline harness and navigation closure'], mappedSpecs)
}
