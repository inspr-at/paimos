// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1135 (Markus 2026-10-10: "we will not ship sample data at all"): Delivery › Flow
// showed a built-in example run as if it were live. Example, sample, demo and mock data
// live in web/tests only. This reads every file in web/src and fails on a file named like
// such data, or on a relative import of one or of anything under web/tests. The one
// approved exception keeps its reason; an entry that no longer matches anything fails too.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = new URL('../src/', import.meta.url).pathname
export const SAMPLE = /(example|sample|demo|mock|fixture|dummy)/i
// Deliberate exceptions, each with its reason.
const ALLOW: { file: string; why: string }[] = [
  { file: 'lib/quotes/sampleDocument.ts', why: 'the quote layout preview shows an invented quote, not data (Markus 2026-10-10)' },
]

function files(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return name === 'vendor' ? [] : files(path)
    return /\.(vue|ts|js)$/.test(name) ? [path] : []
  })
}
const allowed = (file: string) => ALLOW.some(entry => entry.file === file)
/** Relative module paths a source imports, statically or dynamically. Packages are not ours. */
export function relativeImports(source: string): string[] {
  const out: string[] = []
  for (const m of source.matchAll(/(?:\bfrom\s*|\bimport\s*\(\s*|\bimport\s+)(['"])(\.{1,2}\/[^'"]+)\1/g)) out.push(m[2]!)
  return out
}
/** Why one source file breaks the rule, if it does. `file` is relative to web/src. */
export function violations(file: string, source: string): string[] {
  const out: string[] = []
  const name = file.split('/').pop()!
  if (SAMPLE.test(name) && !allowed(file)) out.push(`${file}: a sample-data file in web/src`)
  for (const spec of relativeImports(source)) {
    const target = join(file, '..', spec)
    if (target.startsWith('../tests/')) out.push(`${file}: imports ${spec} from web/tests`)
    else if (SAMPLE.test(target.split('/').pop()!) && !allowed(`${target.replace(/\.(ts|js)$/, '')}.ts`)) out.push(`${file}: imports sample data ${spec}`)
  }
  return out
}

test('web/src has no example, sample, demo or mock data and imports none', () => {
  const found = files(root).flatMap(path => violations(relative(root, path), readFileSync(path, 'utf8')))
  assert.deepEqual(found, [], 'sample data belongs in web/tests; without real data show an empty state that says when data appears')
  for (const entry of ALLOW) assert.ok(files(root).some(path => relative(root, path) === entry.file), `stale allow-list entry ${entry.file}`)
})

test('the guard catches what it is meant to catch', () => {
  assert.deepEqual(violations('lib/useDeliveryFlow.ts', "import { exampleLive } from './deliveryFlowExample'"), ["lib/useDeliveryFlow.ts: imports sample data ./deliveryFlowExample"])
  assert.deepEqual(violations('components/x/A.vue', "const m = await import('../../lib/demoRuns')"), ['components/x/A.vue: imports sample data ../../lib/demoRuns'])
  assert.deepEqual(violations('lib/a.ts', "import { RUN } from '../../tests/delivery-flow-fixtures'"), ['lib/a.ts: imports ../../tests/delivery-flow-fixtures from web/tests'])
  assert.deepEqual(violations('lib/mockRuns.ts', ''), ['lib/mockRuns.ts: a sample-data file in web/src'])
  // Packages, shared contract data, the approved quote preview and ordinary modules pass.
  assert.deepEqual(violations('lib/access.ts', "import data from '../../../internal/authz/permission_data.mjs'"), [])
  assert.deepEqual(violations('lib/graph.ts', "import type { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'"), [])
  assert.deepEqual(violations('components/settings/profiles/P.vue', "import { sampleDocument } from '../../../lib/quotes/sampleDocument'"), [])
  assert.deepEqual(violations('lib/useDeliveryFlow.ts', "import { liveData } from './deliveryFlowData'"), [])
})
