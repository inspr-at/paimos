// SPDX-License-Identifier: AGPL-3.0-only
// One-time migration oracle. The historical aggregate is never a new fixture.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { dirname } from 'node:path'
import { apiRoot, bundleOpenAPI } from './generate.mjs'
import { splitOpenAPI } from './openapi-source.mjs'

try {
  const base = process.argv[2]
  if (process.argv.length !== 3 || !/^[a-f0-9]{40}$/.test(base ?? '')) throw new Error('Usage: node api/prove.mjs <40-character pre-fragment commit>')
  const before = execFileSync('git', ['show', `${base}:api/openapi.yaml`], { cwd: dirname(apiRoot), timeout: 10_000, maxBuffer: 16 * 1024 * 1024 })
  const after = Buffer.from(bundleOpenAPI())
  if (!before.equals(after)) throw new Error('OpenAPI migration changed contract bytes')
  const spec = splitOpenAPI(after.toString())
  console.log(JSON.stringify({ base, identical: true, sha256: createHash('sha256').update(after).digest('hex'), bytes: after.length,
    paths: spec.paths.size, components: [...spec.components.values()].reduce((n, entries) => n + entries.size, 0) }))
} catch (error) { console.error(error.message); process.exitCode = 1 }
