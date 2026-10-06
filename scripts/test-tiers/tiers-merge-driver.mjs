// SPDX-License-Identifier: AGPL-3.0-only
// Self-contained: install this file outside the checkout for old-branch merges.
// Minimal copies of manifests.mjs identity, canonical writer and set merge.
// Keep behavior aligned with tiers-merge-driver.test.mjs parity fixtures.
import { readFileSync, writeFileSync, statSync } from 'node:fs'

const manifestPaths = ['scripts/ci/go-test-tiers.json', 'scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json']
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const absent = Symbol('absent')
class MergeConflictError extends Error {}
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value)
const fields = ['kind', 'package', 'file', 'name', 'occurrence']
const keys = value => Object.keys(value).sort((a, b) => {
  const rank = k => fields.includes(k) ? fields.indexOf(k) : fields.length
  return rank(a) - rank(b) || compare(a, b)
})
const stable = value => Array.isArray(value) ? value.map(stable) : object(value)
  ? Object.fromEntries(keys(value).map(k => [k, stable(value[k])])) : value
const equal = (a, b) => a === absent || b === absent ? a === b : JSON.stringify(stable(a)) === JSON.stringify(stable(b))

function testIdentity(row) {
  if (!object(row) || !['go', 'node', 'vitest', 'browser'].includes(row.kind) ||
      typeof row.name !== 'string' || !row.name ||
      typeof (row.package ?? row.file) !== 'string' || !(row.package ?? row.file) ||
      (row.kind === 'go' ? !row.package : !row.file) ||
      (row.occurrence !== undefined && (!Number.isSafeInteger(row.occurrence) || row.occurrence < 1))) {
    throw new Error('Invalid test row identity')
  }
  // A tuple avoids colon/# collisions in parameterized test titles. Occurrence
  // is part of native registration identity, not policy or a scheduling value.
  return JSON.stringify([row.kind, row.kind === 'go' ? row.package : row.file, row.name, row.occurrence ?? null])
}
const fieldIdentity = field => row => {
  if (!object(row) || typeof row[field] !== 'string' || !row[field]) throw new Error(`Invalid ${field} row identity`)
  return row[field]
}
const stringIdentity = row => {
  if (typeof row !== 'string' || !row) throw new Error('Expected nonempty string identity')
  return row
}

function listPolicy(path, shards) {
  const name = path.at(-1)
  if (!shards) {
    if (path.length === 1 && name === 'tests') return { identity: testIdentity }
    if (path.length === 1 && name === 'postGateCases') return { identity: stringIdentity }
    if (path.length === 1 && name === 'deleteCandidateGroups') return { identity: fieldIdentity('file') }
  } else {
    if (path.length === 1 && name === 'groups') return { identity: fieldIdentity('id'), ordered: true, recursive: true }
    if (path.length === 3 && path[0] === 'groups' && name === 'specs') return { identity: fieldIdentity('file') }
    if (path.length === 1 && ['configs', 'duplicateCurrentSpecs', 'exclusions'].includes(name)) return { identity: fieldIdentity('file') }
    if (path.length === 1 && name === 'ciInventory') return { identity: fieldIdentity('id') }
    if (path.length === 1 && name === 'integrationNotes') return { identity: stringIdentity, ordered: true }
  }
}

function rowCompare(a, b, identity) {
  if (object(a) && a.kind && object(b) && b.kind && a.name && b.name) {
    for (const field of ['kind', 'owner', 'name', 'occurrence']) {
      const value = row => field === 'owner' ? row.package ?? row.file : row[field] ?? 0
      const order = compare(value(a), value(b))
      if (order) return order
    }
    return 0
  }
  return compare(identity(a), identity(b))
}

function rowMap(rows, policy, label, rejectDuplicates) {
  if (!Array.isArray(rows)) throw new Error(`Expected row list: ${label}`)
  if (rows.length > 50_000) throw new Error(`Too many rows: ${label}`)
  const result = new Map()
  for (const row of rows) {
    const id = policy.identity(row)
    if (result.has(id) && (rejectDuplicates || !equal(result.get(id), row))) throw new Error(`Duplicate key: ${label}: ${id}`)
    result.set(id, row)
  }
  return result
}

function manifestType(path) {
  if (!manifestPaths.includes(path)) throw new Error(`Unsupported manifest: ${path}`)
  return path === manifestPaths[2]
}

function normalizeManifest(data, file, { rejectDuplicates = false } = {}) {
  const shards = manifestType(file)
  if (!object(data) || data.version !== 1 || !Array.isArray(data[shards ? 'groups' : 'tests'])) throw new Error(`Expected version 1 manifest: ${file}`)
  function visit(value, path = []) {
    if (path.length > 64) throw new Error('Manifest nesting exceeds 64 levels')
    const policy = listPolicy(path, shards)
    if (policy) {
      const rows = [...rowMap(value, policy, path.join('.'), rejectDuplicates).values()]
      if (!policy.ordered) rows.sort((a, b) => rowCompare(a, b, policy.identity))
      return rows.map(row => visit(row, [...path, policy.identity(row)]))
    }
    if (Array.isArray(value)) return value.map((row, i) => visit(row, [...path, String(i)]))
    if (object(value)) return Object.fromEntries(keys(value).map(k => [k, visit(value[k], [...path, k])]))
    return value
  }
  const result = visit(data)
  if (shards) {
    const seen = new Set()
    for (const group of result.groups) for (const spec of group.specs) {
      if (seen.has(spec.file)) throw new Error(`Duplicate spec across groups: ${spec.file}`)
      seen.add(spec.file)
    }
  }
  return result
}

// Separators have their own lines, including at the first/last row. JSON has
// no trailing commas: an insertion needs a row and a separator, but never a
// changed neighbouring row or a policy-changing sentinel. Empty lists retain
// the same opening/closing lines. All fields within a row have stable order.
function formatManifest(data, file, options) {
  const normalized = normalizeManifest(data, file, options), shards = manifestType(file)
  function render(value, path = [], depth = 0) {
    const indent = '  '.repeat(depth), child = `${indent}  `
    if (Array.isArray(value)) {
      const policy = listPolicy(path, shards)
      const rows = value.map((row, i) => child + (policy && !policy.recursive
        ? JSON.stringify(row) : render(row, [...path, policy ? policy.identity(row) : String(i)], depth + 1)))
      return `[\n${rows.length ? rows.join(`\n${child},\n`) + '\n' : ''}${indent}]`
    }
    if (object(value)) {
      const compact = path.join('.') === 'timingWeights.owners'
      const rows = Object.entries(value).map(([k, v]) => `${child}${JSON.stringify(k)}: ${compact ? JSON.stringify(v) : render(v, [...path, k], depth + 1)}`)
      return rows.length ? `{\n${rows.join(`\n${child},\n`)}\n${indent}}` : '{}'
    }
    return JSON.stringify(value)
  }
  return render(normalized) + '\n'
}

function readManifest(path) {
  if (statSync(path).size > 32 * 1024 * 1024) throw new Error(`Manifest exceeds 32 MiB: ${path}`)
  return readFileSync(path, 'utf8')
}

function mergeManifests(base, ours, theirs, file) {
  const shards = manifestType(file)
  // Old layouts are welcome; duplicate identities (even identical rows) are
  // ambiguous merge input and must be repaired explicitly before retrying.
  const inputs = [base, ours, theirs].map(data => normalizeManifest(data, file, { rejectDuplicates: true }))
  function conflict(path) { throw new MergeConflictError(`Manifest merge conflict: ${file}: ${path.join('.') || '<root>'}`) }
  function merge(b, o, t, path = [], atomic = false) {
    if (equal(o, t)) return o
    if (equal(b, o)) return t
    if (equal(b, t)) return o
    if (o === absent || t === absent || atomic) return conflict(path)
    const policy = listPolicy(path, shards)
    if (policy) {
      const maps = [b === absent ? [] : b, o, t].map(rows => rowMap(rows, policy, path.join('.'), true))
      const ids = new Set(maps.flatMap(map => [...map.keys()]))
      const merged = new Map()
      for (const id of [...ids].sort(compare)) {
        const row = merge(...maps.map(map => map.has(id) ? map.get(id) : absent), [...path, id], !policy.recursive)
        if (row !== absent) merged.set(id, row)
      }
      // Preserve existing group/history order. Concurrent new groups/notes
      // append by identity rather than by whichever side happened to be OURS.
      const order = policy.ordered ? [...maps[0].keys(), ...[...ids].filter(id => !maps[0].has(id)).sort(compare)] : [...merged.keys()]
      return order.filter(id => merged.has(id)).map(id => merged.get(id))
    }
    if ((b === absent || object(b)) && object(o) && object(t)) {
      const names = new Set([b === absent ? [] : Object.keys(b), Object.keys(o), Object.keys(t)].flat())
      const values = []
      for (const name of [...names].sort(compare)) {
        const v = merge(...[b, o, t].map(value => value !== absent && Object.hasOwn(value, name) ? value[name] : absent), [...path, name], path.join('.') === 'timingWeights.owners')
        if (v !== absent) values.push([name, v])
      }
      return Object.fromEntries(values)
    }
    return conflict(path)
  }
  const merged = merge(...inputs)
  try { return normalizeManifest(merged, file, { rejectDuplicates: true }) }
  catch (error) {
    // Individually valid sides can introduce the same spec in two groups.
    // That disagreement is a merge conflict, rather than an input parse error.
    throw new MergeConflictError(`Manifest merge conflict: ${file}: ${error.message}`)
  }
}

function mergeDriver(basePath, oursPath, theirsPath, file) {
  const inputs = [basePath, oursPath, theirsPath].map(path => JSON.parse(readManifest(path)))
  const output = formatManifest(mergeManifests(...inputs, file), file, { rejectDuplicates: true })
  // Failures leave OURS untouched; Git marks the path unmerged and stderr
  // identifies the conflicting key. Never write a partially merged manifest.
  writeFileSync(oursPath, output)
  return 0
}

function mergeDriverMain(args) {
  try {
    if (args.length !== 4) throw new Error('Usage: node tiers-merge-driver.mjs BASE OURS THEIRS PATH')
    return mergeDriver(...args)
  } catch (error) {
    console.error(error.message)
    return error instanceof MergeConflictError ? 1 : 2
  }
}

process.exitCode = mergeDriverMain(process.argv.slice(2))
