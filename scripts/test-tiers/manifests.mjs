// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync, writeFileSync, statSync, openSync, closeSync, fsyncSync, renameSync, unlinkSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { resolve } from 'node:path'
import { tiers, key } from './core.mjs'

export const manifestPaths = ['scripts/ci/go-test-tiers.json', 'scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json']
export const fixCommand = 'node scripts/test-tiers/cli.mjs manifests --write'
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const absent = Symbol('absent')
class MergeConflictError extends Error {}
class ManifestInputError extends Error {}
const filesystemErrors = new Set([
  'ENOENT', 'EACCES', 'EISDIR', 'ENOSPC', 'EROFS', 'EPERM', 'EMFILE', 'ENFILE',
  'ENOTDIR', 'ELOOP', 'ENAMETOOLONG', 'EIO', 'EBADF', 'EEXIST', 'EINVAL', 'EBUSY',
  'EFBIG', 'ENOTEMPTY', 'EXDEV', 'ETXTBSY', 'ENODEV', 'ENXIO', 'EDQUOT', 'ENOSYS',
  'ENOTSUP', 'EOPNOTSUPP',
])
const testOwner = row => row.kind === 'go' ? row.package : row.file
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
      typeof testOwner(row) !== 'string' || !testOwner(row) ||
      (row.kind === 'go' && Object.hasOwn(row, 'occurrence')) ||
      (row.occurrence !== undefined && (!Number.isSafeInteger(row.occurrence) || row.occurrence < 1))) {
    throw new ManifestInputError('Invalid test row identity')
  }
  // A tuple avoids colon/# collisions in parameterized test titles. Occurrence
  // is part of native registration identity, not policy or a scheduling value.
  return JSON.stringify([row.kind, testOwner(row), row.name, row.occurrence ?? null])
}
const fieldIdentity = field => row => {
  if (!object(row) || typeof row[field] !== 'string' || !row[field]) throw new ManifestInputError(`Invalid ${field} row identity`)
  return row[field]
}
const stringIdentity = row => {
  if (typeof row !== 'string' || !row) throw new ManifestInputError('Expected nonempty string identity')
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
      const value = row => field === 'owner' ? testOwner(row) : row[field] ?? 0
      const order = compare(value(a), value(b))
      if (order) return order
    }
    return 0
  }
  return compare(identity(a), identity(b))
}

function rowMap(rows, policy, label, rejectDuplicates) {
  if (!Array.isArray(rows)) throw new ManifestInputError(`Expected row list: ${label}`)
  if (rows.length > 50_000) throw new ManifestInputError(`Too many rows: ${label}`)
  const result = new Map()
  for (const row of rows) {
    const id = policy.identity(row)
    if (result.has(id) && (rejectDuplicates || !equal(result.get(id), row))) throw new ManifestInputError(`Duplicate key: ${label}: ${id}`)
    result.set(id, row)
  }
  return result
}

function manifestType(path) {
  if (!manifestPaths.includes(path)) throw new ManifestInputError(`Unsupported manifest: ${path}`)
  return path === manifestPaths[2]
}

export function normalizeManifest(data, file, { rejectDuplicates = false } = {}) {
  const shards = manifestType(file)
  if (!object(data) || data.version !== 1 || !Array.isArray(data[shards ? 'groups' : 'tests'])) throw new ManifestInputError(`Expected version 1 manifest: ${file}`)
  function visit(value, path = []) {
    if (path.length > 64) throw new ManifestInputError('Manifest nesting exceeds 64 levels')
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
      if (seen.has(spec.file)) throw new ManifestInputError(`Duplicate spec across groups: ${spec.file}`)
      seen.add(spec.file)
    }
  }
  return result
}

// Separators have their own lines, including at the first/last row. JSON has
// no trailing commas: an insertion needs a row and a separator, but never a
// changed neighbouring row or a policy-changing sentinel. Empty lists retain
// the same opening/closing lines. All fields within a row have stable order.
export function formatManifest(data, file, options) {
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

export function readManifest(path) {
  if (statSync(path).size > 32 * 1024 * 1024) throw new ManifestInputError(`Manifest exceeds 32 MiB: ${path}`)
  return readFileSync(path, 'utf8')
}

export function checkManifests(root) {
  const failures = []
  for (const file of manifestPaths) {
    try {
      const source = readManifest(resolve(root, file))
      if (source !== formatManifest(JSON.parse(source), file)) failures.push(file)
    } catch (error) { failures.push(`${file}: ${error.message}`) }
  }
  if (failures.length) throw new ManifestInputError(`Noncanonical CI manifests:\n${failures.join('\n')}\nFix: ${fixCommand}`)
}

export function writeManifests(root) {
  // Validate every input before writing any of the three files.
  const outputs = manifestPaths.map(file => [resolve(root, file), formatManifest(JSON.parse(readManifest(resolve(root, file))), file)])
  for (const [path, source] of outputs) atomicWrite(path, source)
}

export function classifyManifest(manifest, discovered, { tier, only } = {}) {
  if (!tiers.includes(tier)) throw new ManifestInputError('classify requires --tier ESSENTIAL|GATED-FULL|NIGHTLY')
  if (only !== undefined && (typeof only !== 'string' || !only || only.length > 1024)) throw new ManifestInputError('Expected nonempty --only pattern (at most 1024 characters)')
  // --only is a literal identity substring, not an unbounded regular expression.
  const declared = rowMap(manifest.tests, { identity: testIdentity }, 'tests', true)
  const found = rowMap(discovered, { identity: testIdentity }, 'inventory', true)
  const added = []
  for (const [id, row] of found) if (!declared.has(id) && (only === undefined || key(row).includes(only))) {
    const { leaf, line, id: nativeId, active, file, ...entry } = row
    added.push({ ...entry, ...(row.kind === 'go' ? {} : { file }), tier })
  }
  return { manifest: { ...manifest, tests: [...manifest.tests, ...added] }, added }
}

export function mergeManifests(base, ours, theirs, file) {
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
      let order = [...merged.keys()]
      if (policy.ordered) {
        // Compare surviving BASE identities only: additions still append in
        // identity order, and deletions are handled by the keyed row merge.
        const sequences = maps.map(map => [...map.keys()].filter(id => maps[0].has(id) && merged.has(id)))
        const [baseOrder, oursOrder, theirsOrder] = sequences
        const same = (a, b) => JSON.stringify(a) === JSON.stringify(b)
        let retained = baseOrder
        if (same(oursOrder, theirsOrder) || same(baseOrder, theirsOrder)) retained = oursOrder
        else if (same(baseOrder, oursOrder)) retained = theirsOrder
        else return conflict([...path, '<order>'])
        order = [...retained, ...[...merged.keys()].filter(id => !maps[0].has(id)).sort(compare)]
      }
      return order.map(id => merged.get(id))
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
    if (!(error instanceof ManifestInputError)) throw error
    // Individually valid sides can introduce the same spec in two groups.
    // That disagreement is a merge conflict, rather than an input parse error.
    throw new MergeConflictError(`Manifest merge conflict: ${file}: ${error.message}`)
  }
}

// All fallible writes target a unique sibling. Rename is the commit point;
// nothing that could report failure runs after OURS has been replaced.
function atomicWrite(path, source) {
  const temporary = `${path}.${randomUUID()}.tmp`
  let descriptor, created = false
  try {
    descriptor = openSync(temporary, 'wx', statSync(path).mode & 0o777)
    created = true
    writeFileSync(descriptor, source)
    fsyncSync(descriptor)
    closeSync(descriptor)
    descriptor = undefined
    renameSync(temporary, path)
  } catch (error) {
    if (descriptor !== undefined) { try { closeSync(descriptor) } catch {} }
    if (created) { try { unlinkSync(temporary) } catch {} }
    throw error
  }
}

export function mergeDriver(basePath, oursPath, theirsPath, file) {
  const inputs = [basePath, oursPath, theirsPath].map(path => JSON.parse(readManifest(path)))
  let output, disagreement
  try {
    output = formatManifest(mergeManifests(...inputs, file), file, { rejectDuplicates: true })
  } catch (error) {
    if (!(error instanceof MergeConflictError)) throw error
    disagreement = error
    // Preserve all three complete versions, even when the semantic conflict
    // (e.g. duplicate specs across groups) would merge cleanly as plain text.
    const [base, ours, theirs] = inputs.map(data => formatManifest(data, file, { rejectDuplicates: true }))
    output = `<<<<<<< ours
${ours}||||||| base
${base}=======
${theirs}>>>>>>> theirs
`
  }
  atomicWrite(oursPath, output)
  // Exit 1 only after conflict markers are safely written. Any write failure
  // propagates instead and leaves the original OURS bytes untouched.
  if (disagreement) throw disagreement
  return 0
}

export function mergeDriverMain(args) {
  try {
    if (args.length !== 4) throw new ManifestInputError('Usage: node tiers-merge-driver.mjs BASE OURS THEIRS PATH')
    return mergeDriver(...args)
  } catch (error) {
    if (!(error instanceof MergeConflictError) && !(error instanceof ManifestInputError) &&
        !(error instanceof SyntaxError) && !filesystemErrors.has(error.code)) throw error
    console.error(error.message)
    return error instanceof MergeConflictError ? 1 : 2
  }
}

export function manifestsMain(args, root) {
  if (args.length === 1 && args[0] === '--write') { writeManifests(root); return 0 }
  if (args.length === 1 && args[0] === '--check') { checkManifests(root); return 0 }
  if (args[0] === 'merge-driver') return mergeDriverMain(args.slice(1))
  throw new ManifestInputError('Usage: cli.mjs manifests --write|--check | manifests merge-driver BASE OURS THEIRS PATH')
}
