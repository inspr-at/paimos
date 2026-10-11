// SPDX-License-Identifier: AGPL-3.0-only
import { createHash, randomUUID } from 'node:crypto'
import { mkdirSync, readFileSync, writeFileSync, renameSync, unlinkSync, rmdirSync, lstatSync } from 'node:fs'
import { homedir } from 'node:os'
import { resolve, dirname } from 'node:path'

// Deliberately independent of XDG_CACHE_HOME: ci-static gives each invocation
// a scratch directory, but native discovery should be shared across worktrees.
export const listingCacheDirectory = resolve(homedir(), '.cache/aeon/test-tier-listings-v1')
const digest = value => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const maxBytes = 32 * 1024 * 1024
const heldLocks = new Set()
const tierPolicies = new Set(['scripts/ci/go-test-tiers.json', 'scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json'])
const source = /(?:\.(?:go|mjs|cjs|js|ts|tsx|vue|json|yaml|yml|sql|css|html|md|txt|mod|sum|work|lock|c|h|s|cc|cpp|sh|svg|png|webp|jpg|jpeg|gif|ico|woff|woff2|avif|csv|toml|tmpl)|(?:^|\/)go\.work\.sum)$/
const safe = file => source.test(file) && !/(?:^|\/)(?:node_modules|\.git|\.env[^/]*|(?:secrets?|credentials?)(?:\.[^/]*)?)(?:\/|$)/i.test(file)

// Index blob identities are content hashes, independent of checkout paths and
// mtimes. Hash only dirty/untracked inputs; an unchanged tree needs no source
// reads. Include untracked tests and deletions, and never open secret files.
export function inputSnapshot(base, run) {
  const git = args => run('git', args, { cwd: base })
  const algorithm = git(['rev-parse', '--show-object-format']).trim()
  const dirty = new Set(git(['diff-files', '--no-ext-diff', '--name-only', '-z']).split('\0'))
  const rows = new Map()
  for (const entry of git(['ls-files', '--stage', '-z']).split('\0').filter(Boolean)) {
    const match = /^(\d+) ([a-f0-9]+) (\d)\t([\s\S]+)$/.exec(entry)
    if (!match || match[3] !== '0') throw new Error('Test discovery requires an index without conflicts')
    const [, mode, hash, , file] = match
    if (safe(file)) rows.set(file, { mode, hash })
  }
  // ci-static generates these untracked configs below the web Go package.
  // Parallel typechecking is not a source edit; tracked inputs remain keyed.
  for (const file of git(['ls-files', '--others', '--exclude-standard', '-z']).split('\0')
    .filter(file => safe(file) && !/^web\/\.ci-static-typecheck-[^/]+\//.test(file))) {
    rows.set(file, { mode: '100644' }); dirty.add(file)
  }
  for (const [file, row] of rows) {
    if (!dirty.has(file) && row.mode !== '120000') continue
    let stat
    try { stat = lstatSync(resolve(base, file)) }
    catch (error) { if (error.code === 'ENOENT') { rows.delete(file); continue }; throw error }
    // Do not follow a source symlink to an untracked input or a credential.
    if (!stat.isFile()) throw new Error(`Test discovery input must be a regular file: ${file}`)
    if (stat.size > maxBytes) throw new Error(`Test discovery input exceeds ${maxBytes} bytes: ${file}`)
    const body = readFileSync(resolve(base, file))
    row.hash = createHash(algorithm).update(`blob ${body.length}\0`).update(body).digest('hex')
    row.mode = stat.mode & 0o111 ? '100755' : '100644'
  }
  return [...rows].sort(([a], [b]) => a.localeCompare(b)).map(([file, row]) => [file, row.hash, row.mode])
}

export function listingKey(snapshot, scope, identity) {
  let inputs = scope.startsWith('go-') ? snapshot.filter(([file]) => file.endsWith('.go') || /^go\.(?:mod|sum|work|work\.sum)$/.test(file) || file.startsWith('scripts/test-tiers/')) : snapshot.filter(([file]) => !tierPolicies.has(file))
  if (scope === 'go-base') inputs = inputs.filter(([file]) => !file.endsWith('_test.go'))
  return digest([1, scope, identity, inputs])
}

export function goTestPackageKeys(snapshot, identity) {
  const packages = new Map()
  for (const row of snapshot) if (row[0].endsWith('_test.go') && !row[0].includes('/testdata/')) {
    const owner = dirname(row[0]).replaceAll('\\', '/')
    if (!packages.has(owner)) packages.set(owner, [])
    packages.get(owner).push(row)
  }
  return Object.fromEntries([...packages].map(([owner, rows]) => [owner, digest([1, 'go-test-source', identity, owner, rows])]))
}

export function cachedGoSource(snapshot, identity, { collectAll, collectPackage }, options = {}) {
  const stamp = listingKey(snapshot, 'go-source', identity)
  return cachedListing(stamp, () => {
    const baseStamp = listingKey(snapshot, 'go-base', identity)
    const current = goTestPackageKeys(snapshot, { identity, baseStamp })
    const base = cachedListing(baseStamp, () => ({ inventory: collectAll(), packages: current }), options)
    let value = structuredClone(base.inventory)
    const owners = new Set([...Object.keys(base.packages), ...Object.keys(current)])
    // A new test-only package can change go list's package set. Refresh the
    // base conservatively rather than silently omitting its active graph.
    if ([...owners].some(owner => !Object.hasOwn(base.packages, owner))) {
      const refreshed = cachedListing(baseStamp, () => ({ inventory: collectAll(), packages: current }), { ...options, fresh: true })
      value = refreshed.inventory
    }
    else for (const owner of owners) if (base.packages[owner] !== current[owner]) {
      const stamp = current[owner] ?? digest([1, 'go-empty-package', identity, baseStamp, owner])
      const updated = cachedListing(stamp, () => collectPackage(owner), options)
      value.tests = [...(value.tests ?? []).filter(row => row.package !== owner), ...updated.tests]
      const packageName = owner === '.' ? identity.module : owner
      if (updated.imports !== undefined) value.imports[packageName] = updated.imports
      else delete value.imports[packageName]
    }
    return value
  }, options)
}

export function cacheEnabled(env = process.env) {
  // Hosted and self-hosted CI retain native discovery, including its failures.
  // A developer cache must never become evidence for a trusted CI gate.
  return env.AEON_TEST_TIER_CACHE !== '0' && env.GITHUB_ACTIONS !== 'true'
}

function readCache(directory, key) {
  try {
    const file = resolve(directory, `${key}.json`)
    const stat = lstatSync(file)
    if (!stat.isFile() || stat.size > maxBytes) return undefined
    const entry = JSON.parse(readFileSync(file, 'utf8'))
    if (entry.version === 1 && entry.key === key && entry.checksum === digest(entry.value)) return entry.value
  } catch (error) {
    if (!['ENOENT', 'ENOTDIR'].includes(error.code) && !(error instanceof SyntaxError)) throw error
  }
}

function writeCache(directory, key, value) {
  const body = JSON.stringify({ version: 1, key, checksum: digest(value), value })
  if (Buffer.byteLength(body) > maxBytes) throw new Error('Test listing cache exceeds its size bound')
  const temporary = resolve(directory, `${key}.${process.pid}.${randomUUID()}.tmp`)
  try {
    writeFileSync(temporary, body, { flag: 'wx', mode: 0o600 })
    renameSync(temporary, resolve(directory, `${key}.json`))
  } finally {
    try { unlinkSync(temporary) } catch (error) { if (error.code !== 'ENOENT') throw error }
  }
}

export function withListingLock(directory, action, { timeout = 20 * 60_000, onWait = () => {} } = {}) {
  directory = resolve(directory)
  if (heldLocks.has(directory)) return action()
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  const lock = resolve(directory, 'listing.lock'), owner = resolve(lock, 'owner.json')
  const begin = performance.now(), pause = new Int32Array(new SharedArrayBuffer(4))
  for (;;) {
    try { mkdirSync(lock); break }
    catch (error) { if (error.code !== 'EEXIST') throw error }
    try {
      const { pid } = JSON.parse(readFileSync(owner, 'utf8'))
      if (Number.isSafeInteger(pid) && pid > 0) {
        try { process.kill(pid, 0) }
        catch (error) {
          if (error.code === 'ESRCH') {
            const recovery = resolve(directory, 'recovery.lock')
            let recovering = false
            try { mkdirSync(recovery); recovering = true }
            catch (error) { if (error.code !== 'EEXIST') throw error }
            if (recovering) {
              try {
                // Re-read under the recovery lock; a different waiter may
                // already have installed a new live owner in this pathname.
                if (JSON.parse(readFileSync(owner, 'utf8')).pid === pid) {
                  const stale = `${lock}.${randomUUID()}.stale`
                  renameSync(lock, stale)
                  unlinkSync(resolve(stale, 'owner.json')); rmdirSync(stale)
                }
              } finally { rmdirSync(recovery) }
              continue
            }
          }
          if (!['EPERM', 'ESRCH'].includes(error.code)) throw error
        }
      }
    } catch (error) { if (error.code !== 'ENOENT') throw error }
    onWait()
    if (performance.now() - begin >= timeout) throw new Error('Timed out waiting for shared test listing lock')
    Atomics.wait(pause, 0, 0, 100)
  }
  try {
    writeFileSync(owner, JSON.stringify({ pid: process.pid }), { flag: 'wx', mode: 0o600 })
    heldLocks.add(directory)
    return action()
  } finally {
    heldLocks.delete(directory)
    unlinkSync(owner); rmdirSync(lock)
  }
}

export function cachedListing(key, compute, { directory = listingCacheDirectory, enabled = true, fresh = false, lockOptions } = {}) {
  if (!enabled) return compute()
  if (!fresh) {
    const hit = readCache(directory, key)
    if (hit !== undefined) return hit
  }
  return withListingLock(directory, () => {
    if (!fresh) {
      const hit = readCache(directory, key)
      if (hit !== undefined) return hit
    }
    const value = compute()
    writeCache(directory, key, value)
    return value
  }, lockOptions)
}

// A test edit invalidates its owner, while an ordinary source edit invalidates
// owners that import it. Dependency test files do not belong to a Go binary.
// Only module/tooling inputs are global. Package subtrees retain non-Go inputs
// for embed, cgo and assembly, without making web/docs or tier policy edits
// invalidate unrelated binaries after classification registers a test.
export function goPackageKeys(snapshot, inventory, identity) {
  const go = new Map(), assets = new Map(), common = []
  for (const row of snapshot) {
    const [file] = row
    if (tierPolicies.has(file)) continue
    if (/^go\.(?:mod|sum|work|work\.sum)$/.test(file) ||
      (!file.includes('/') && file.endsWith('.go') && !file.endsWith('_test.go')) ||
      file.startsWith('scripts/test-tiers/') || file.startsWith('scripts/test-tier-go/')) {
      common.push(row); continue
    }
    let owner = dirname(file).replaceAll('\\', '/')
    if (file.endsWith('.go')) {
      if (!go.has(owner)) go.set(owner, [])
      go.get(owner).push(row)
    } else for (;;) {
      // Embed patterns can reach any directory below the package. Index each
      // ancestor once; sibling paths such as a/ and ab/ never share assets.
      if (!assets.has(owner)) assets.set(owner, [])
      assets.get(owner).push(row)
      if (owner === '.') break
      owner = dirname(owner).replaceAll('\\', '/')
    }
  }
  const commonKey = digest(common)
  return Object.fromEntries(Object.keys(inventory.imports).sort().map(owner => {
    const dependencies = new Set()
    const visit = pkg => {
      if (dependencies.has(pkg)) return
      dependencies.add(pkg)
      for (const dependency of inventory.imports[pkg] ?? []) visit(dependency)
    }
    visit(owner)
    const inputs = [...dependencies].sort().flatMap(pkg => {
      const directory = pkg === identity.module ? '.' : pkg
      return [...(go.get(directory) ?? []).filter(([file]) => pkg === owner || !file.endsWith('_test.go')), ...(assets.get(directory) ?? [])]
    })
    return [owner, digest([2, 'go-native', identity, owner, commonKey, inputs])]
  }))
}

export function cachedGoList(keys, list, { directory = listingCacheDirectory, enabled = true, fresh = false } = {}) {
  const owners = Object.keys(keys)
  if (!enabled) return list(owners)
  return withListingLock(directory, () => {
    const results = new Map(), missing = []
    for (const owner of owners) {
      const hit = fresh ? undefined : readCache(directory, keys[owner])
      if (Array.isArray(hit) && hit.every(name => typeof name === 'string')) results.set(owner, hit)
      else missing.push(owner)
    }
    if (missing.length) {
      const listed = list(missing)
      for (const owner of missing) {
        if (!Array.isArray(listed[owner])) throw new Error(`Incomplete Go listing: ${owner}`)
      }
      for (const owner of missing) {
        writeCache(directory, keys[owner], listed[owner])
        results.set(owner, listed[owner])
      }
    }
    return Object.fromEntries(owners.map(owner => [owner, results.get(owner)]))
  })
}
