// SPDX-License-Identifier: AGPL-3.0-only
// Independent conversion proof: do not use the formatter/normalizer under test.
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../../', import.meta.url))
const files = ['scripts/ci/go-test-tiers.json', 'scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json']
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
function comparable(value, path, shards) {
  if (Array.isArray(value)) {
    const rows = value.map(row => comparable(row, [...path, '*'], shards))
    const unordered = shards
      ? ['configs', 'ciInventory', 'duplicateCurrentSpecs', 'exclusions', 'groups.*.specs']
      : ['tests', 'postGateCases', 'deleteCandidateGroups']
    return unordered.includes(path.join('.')) ? rows.sort((a, b) => compare(JSON.stringify(a), JSON.stringify(b))) : rows
  }
  if (value !== null && typeof value === 'object') return Object.fromEntries(Object.keys(value).sort(compare).map(k => [k, comparable(value[k], [...path, k], shards)]))
  return value
}
function git(args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024, timeout: 10_000 })
  if (result.error || result.status !== 0) throw new Error(`git ${args[0]} failed: ${result.error?.message ?? result.stderr}`)
  return result.stdout.trim()
}
export function proveConversion(before, after, file) {
  if (!files.includes(file)) throw new Error(`Unsupported proof manifest: ${file}`)
  const shards = file === files[2]
  assert.deepEqual(comparable(after, [], shards), comparable(before, [], shards), `${file}: conversion changed data`)
  return { file, rows: shards ? after.groups.reduce((n, g) => n + g.specs.length, 0) : after.tests.length, deepEqualExceptRowOrder: true, tiersWeightsAndMetadataUnchanged: true }
}
export function proveManifests(base = git(['rev-parse', 'HEAD'])) {
  if (!/^[a-f0-9]{40}$/.test(base)) throw new Error('Proof base must be a full local commit SHA')
  return { base, files: files.map((file, i) => {
    const before = JSON.parse(git(['show', `${base}:${file}`]))
    const after = JSON.parse(readFileSync(resolve(root, file), 'utf8'))
    // Set-like list order alone may differ. Duplicates are NOT discarded, and
    // group order, flags, history arrays and every scalar remain significant.
    return proveConversion(before, after, file)
  }) }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length > 3) throw new Error('Usage: node scripts/test-tiers/prove-manifests.mjs [FULL_BASE_SHA]')
    console.log(JSON.stringify(proveManifests(process.argv[2])))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
