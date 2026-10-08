// SPDX-License-Identifier: AGPL-3.0-only
// Self-contained. Install a reviewed copy outside the checkout before merging.
import { constants, openSync, closeSync, fstatSync, readSync, writeFileSync, fsyncSync, renameSync, unlinkSync, statSync, realpathSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const registryPaths = Object.freeze([
  'internal/authz/builtin_agent_exclusions.json',
  'internal/authz/permission_labels.json',
  'internal/auth/testdata/key_scope_ceiling.json',
  'internal/dsar/inventory.json',
])
const absent = Symbol('absent')
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value)
const text = value => typeof value === 'string' && value.length > 0 && value.length <= 1024
const permission = value => text(value) && /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$/.test(value)
const identifier = value => text(value) && /^[a-z][a-z0-9_]*$/.test(value)
const classes = new Set(['personal', 'metadata', 'secret', 'review'])
const license = 'SPDX-License-Identifier: AGPL-3.0-only'
export class RegistryInputError extends Error {}
export class RegistryConflictError extends Error {}
function require(value, message) { if (!value) throw new RegistryInputError(message) }
function fields(value, required, optional = []) {
  require(object(value) && required.every(key => Object.hasOwn(value, key)) &&
    Object.keys(value).every(key => [...required, ...optional].includes(key)), `Invalid fields; expected ${required.join(', ')}`)
}
function unique(rows, identity, label) {
  require(Array.isArray(rows) && rows.length <= 50_000, `Invalid list: ${label}`)
  const keys = new Set()
  for (const row of rows) {
    const key = identity(row)
    require(text(key) && !keys.has(key), `Invalid or duplicate key: ${label}: ${key}`)
    keys.add(key)
  }
}
function permissions(rows) {
  unique(rows, row => { require(permission(row), 'Invalid permission'); return row }, 'permissions')
}

export function validateRegistry(data, file) {
  require(registryPaths.includes(file), `Unsupported registry: ${file}`)
  if (file === registryPaths[0]) {
    fields(data, ['_license', 'permissions'])
    require(data._license === license, 'Invalid license')
    permissions(data.permissions)
  } else if (file === registryPaths[1]) {
    fields(data, ['resources', 'actions', 'special'])
    for (const [group, labels] of Object.entries(data)) {
      require(object(labels), `Invalid label map: ${group}`)
      for (const [key, label] of Object.entries(labels)) require((group === 'special' ? permission(key) : identifier(key)) && text(label), `Invalid label: ${key}`)
    }
  } else if (file === registryPaths[2]) {
    fields(data, ['_license', 'registry', 'cases'])
    require(data._license === license, 'Invalid license')
    unique(data.registry, row => {
      fields(row, ['key', 'grantable_at', 'agent_grantable'], ['owner_workstation_grantable'])
      require(permission(row.key) && typeof row.agent_grantable === 'boolean' &&
        (row.owner_workstation_grantable === undefined || typeof row.owner_workstation_grantable === 'boolean'), 'Invalid permission registry row')
      unique(row.grantable_at, value => { require(['workspace', 'project'].includes(value), 'Invalid grant location'); return value }, 'grantable_at')
      require(row.grantable_at.length > 0, 'Empty grant locations')
      return row.key
    }, 'registry')
    unique(data.cases, row => {
      fields(row, ['name', 'workspace', 'projects', 'want'], ['builtin_role', 'private_role', 'outside_ceiling', 'project_builtin_roles', 'rotation_want'])
      require(text(row.name), 'Invalid case name')
      if (row.workspace !== null) permissions(row.workspace)
      require(Array.isArray(row.projects) && row.projects.length <= 50_000, 'Invalid projects')
      row.projects.forEach(permissions)
      permissions(row.want)
      if (Object.hasOwn(row, 'rotation_want')) permissions(row.rotation_want)
      if (Object.hasOwn(row, 'private_role')) require(typeof row.private_role === 'boolean', 'Invalid private role')
      if (Object.hasOwn(row, 'outside_ceiling')) require(permission(row.outside_ceiling), 'Invalid outside ceiling')
      if (Object.hasOwn(row, 'builtin_role')) require(text(row.builtin_role), 'Invalid built-in role')
      if (Object.hasOwn(row, 'project_builtin_roles')) require(Array.isArray(row.project_builtin_roles) &&
        row.project_builtin_roles.length === row.projects.length && row.project_builtin_roles.every(text), 'Invalid project role bindings')
      return row.name
    }, 'cases')
  } else {
    fields(data, ['_license', 'version', 'tables'])
    require(data._license === license && data.version === 1, 'Invalid inventory header')
    unique(data.tables, row => {
      fields(row, ['table', 'classification', 'locator', 'columns'])
      require(identifier(row.table) && classes.has(row.classification), 'Invalid classified table')
      fields(row.locator, ['tenant_column'], ['person_column'])
      require(Object.values(row.locator).every(identifier), 'Invalid tenant/person locator')
      require(object(row.columns), 'Invalid columns')
      for (const [column, classification] of Object.entries(row.columns)) require(identifier(column) && classes.has(classification), `Invalid classification: ${row.table}.${column}`)
      return row.table
    }, 'tables')
  }
  return data
}

function stable(value) {
  if (Array.isArray(value)) return value.map(stable)
  if (object(value)) return Object.fromEntries(Object.keys(value).sort(compare).map(key => [key, stable(value[key])]))
  return value
}
const equal = (a, b) => a === absent || b === absent ? a === b : JSON.stringify(stable(a)) === JSON.stringify(stable(b))
const identity = path => path === 'permissions' ? row => row :
  path === 'registry' ? row => row.key : path === 'cases' ? row => row.name : path === 'tables' ? row => row.table : undefined

export function normalizeRegistry(data, file) {
  validateRegistry(data, file)
  const result = stable(data)
  for (const field of ['permissions', 'registry', 'cases', 'tables']) if (Object.hasOwn(result, field)) {
    const id = identity(field)
    result[field].sort((a, b) => compare(id(a), id(b)))
  }
  return result
}
export function formatRegistry(data, file) {
  function render(value, path = [], depth = 0) {
    // Keep the existing parity-fixture convention: one permission per line,
    // and compact grant/expectation arrays. Their order remains authored.
    if (file === registryPaths[2] && ((path[0] === 'registry' && path.length === 2) ||
        (path[0] === 'cases' && Array.isArray(value) && path.length > 1))) return JSON.stringify(value)
    const indent = '  '.repeat(depth), child = indent + '  '
    if (Array.isArray(value)) return value.length
      ? `[\n${value.map((row, i) => child + render(row, [...path, String(i)], depth + 1)).join(',\n')}\n${indent}]` : '[]'
    if (object(value)) {
      const rows = Object.entries(value).map(([key, row]) => `${child}${JSON.stringify(key)}: ${render(row, [...path, key], depth + 1)}`)
      return rows.length ? `{\n${rows.join(',\n')}\n${indent}}` : '{}'
    }
    return JSON.stringify(value)
  }
  return render(normalizeRegistry(data, file)) + '\n'
}

export function mergeRegistries(base, ours, theirs, file) {
  const inputs = [base, ours, theirs].map(value => normalizeRegistry(value, file))
  function merge(b, o, t, path = '') {
    if (equal(o, t)) return o
    if (equal(b, o)) return t
    if (equal(b, t)) return o
    if (o !== absent && t !== absent) {
      const id = identity(path)
      if (id) {
        const maps = [b === absent ? [] : b, o, t].map(rows => new Map(rows.map(row => [id(row), row])))
        const keys = [...new Set(maps.flatMap(map => [...map.keys()]))].sort(compare)
        return keys.flatMap(key => {
          const sides = maps.map(map => map.has(key) ? map.get(key) : absent)
          const row = merge(...sides, `${path}.${key}`)
          return row === absent ? [] : [row]
        })
      }
      // Permission and test-case records are atomic: combining separate edits
      // could manufacture a wider grant or a fixture neither author reviewed.
      // Privacy tables merge only their independently keyed columns;
      // classification and locator together form an indivisible safety header.
      if (file === registryPaths[3] && /^tables\.[^.]+$/.test(path)) {
        const header = value => value === absent ? absent : {
          table: value.table, classification: value.classification, locator: value.locator,
        }
        const mergedHeader = merge(...[b, o, t].map(header), `${path}.<header>`)
        const columns = merge(...[b, o, t].map(value => value === absent ? absent : value.columns), `${path}.columns`)
        return { ...mergedHeader, columns }
      }
      const recursive = path === '' || (file === registryPaths[1] && ['resources', 'actions', 'special'].includes(path)) ||
        (file === registryPaths[3] && /^tables\.[^.]+\.columns$/.test(path))
      if (recursive && (b === absent || object(b)) && object(o) && object(t)) {
        const keys = [...new Set([b === absent ? [] : Object.keys(b), Object.keys(o), Object.keys(t)].flat())].sort(compare)
        return Object.fromEntries(keys.flatMap(key => {
          const sides = [b, o, t].map(value => value !== absent && Object.hasOwn(value, key) ? value[key] : absent)
          const value = merge(...sides, path ? `${path}.${key}` : key)
          return value === absent ? [] : [[key, value]]
        }))
      }
    }
    throw new RegistryConflictError(`Registry merge conflict: ${file}: ${path || '<root>'}`)
  }
  return normalizeRegistry(merge(...inputs), file)
}

// JSON.parse silently discards duplicate object keys. Scan bounded tokens first
// to reject that ambiguity (including escaped keys), then parse valid JSON.
export function parseRegistry(source) {
  require(Buffer.byteLength(source) <= 4 * 1024 * 1024, 'Registry exceeds 4 MiB')
  const token = /"(?:[^"\\]|\\.)*"|[{}\[\],:]|true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/gy
  const stack = []
  let offset = 0, count = 0
  while (offset < source.length) {
    while (/\s/.test(source[offset] ?? '') && offset < source.length) offset++
    if (offset === source.length) break
    token.lastIndex = offset
    const match = token.exec(source)
    require(match && ++count <= 500_000, 'Invalid or excessive JSON tokens')
    offset = token.lastIndex
    const value = match[0]
    if (value === '{' || value === '[') {
      stack.push(value === '{' ? new Set() : null)
      require(stack.length <= 64, 'Registry nesting exceeds 64 levels')
    } else if (value === '}' || value === ']') stack.pop()
    else if (value.startsWith('"')) {
      let next = offset
      while (next < source.length && /\s/.test(source[next])) next++
      if (source[next] === ':') {
        const key = JSON.parse(value), seen = stack.at(-1)
        require(seen instanceof Set && !seen.has(key), `Duplicate JSON key: ${key}`)
        seen.add(key)
      }
    }
  }
  return JSON.parse(source)
}

export function readRegistry(path) {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  try {
    const before = fstatSync(descriptor)
    require(before.isFile() && before.size <= 4 * 1024 * 1024, 'Registry must be a regular file of at most 4 MiB')
    const bytes = Buffer.alloc(before.size + 1)
    let size = 0, n
    while (size < bytes.length && (n = readSync(descriptor, bytes, size, bytes.length - size, null))) size += n
    const after = fstatSync(descriptor)
    require(size === before.size && after.size === before.size && after.mtimeMs === before.mtimeMs && after.ctimeMs === before.ctimeMs, 'Registry changed during read')
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, size))
  } finally { closeSync(descriptor) }
}

export function atomicWrite(path, source) {
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

export function mergeDriverMain(args) {
  try {
    require(args.length === 4, 'Usage: node registries.mjs BASE OURS THEIRS PATH')
    const [basePath, oursPath, theirsPath, file] = args
    const inputs = [basePath, oursPath, theirsPath].map(path => parseRegistry(readRegistry(path)))
    inputs.forEach(value => validateRegistry(value, file))
    let output, conflict
    try { output = formatRegistry(mergeRegistries(...inputs, file), file) }
    catch (error) {
      if (!(error instanceof RegistryConflictError)) throw error
      conflict = error
      const [base, ours, theirs] = inputs.map(value => formatRegistry(value, file))
      output = `<<<<<<< ours\n${ours}||||||| base\n${base}=======\n${theirs}>>>>>>> theirs\n`
    }
    atomicWrite(oursPath, output)
    if (conflict) throw conflict
    return 0
  } catch (error) {
    console.error(error.message)
    return error instanceof RegistryConflictError ? 1 : 2
  }
}
if (process.argv[1] && process.argv[1] !== '-' && realpathSync(resolve(process.argv[1])) === fileURLToPath(import.meta.url)) process.exitCode = mergeDriverMain(process.argv.slice(2))
