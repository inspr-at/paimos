// SPDX-License-Identifier: AGPL-3.0-only
import { randomUUID } from 'node:crypto'
import { closeSync, fstatSync, mkdirSync, openSync, readFileSync, readdirSync, renameSync, rmdirSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import { suiteLockPath } from './playwright-global-setup.mjs'
import { processStart, trackGroups, validStart } from './playwright-processes.mjs'

const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
const sameFile = (a, b) => a.dev === b.dev && a.ino === b.ino
function ownedFile(path, fd, token) {
  try { return sameFile(fstatSync(fd), statSync(path)) && JSON.parse(readFileSync(path, 'utf8')).token === token }
  catch (error) { if (error.code !== 'ENOENT') throw error; return false }
}
function removeOwned(path, fd, token) {
  if (ownedFile(path, fd, token)) unlinkSync(path)
}
function createFile(path, token = randomUUID()) {
  const fd = openSync(path, 'wx', 0o600)
  try {
    const started = processStart(process.pid)
    if (!validStart(started)) throw new Error('Cannot establish browser supervisor start time')
    writeFileSync(fd, JSON.stringify({ pid: process.pid, token, started }))
    return { fd, token, started }
  } catch (error) { unlinkSync(path); closeSync(fd); throw error }
}
function validOwner(owner) {
  return owner && Number.isSafeInteger(owner.pid) && owner.pid > 1 && typeof owner.token === 'string' && /^[a-f0-9-]{36}$/.test(owner.token) && validStart(owner.started)
}
function reaperGone(owner) {
  if (!validOwner(owner)) return false
  try { process.kill(owner.pid, 0) }
  catch (error) { return error.code === 'ESRCH' }
  const started = processStart(owner.pid)
  // A failed ps is not proof of death. PID reuse is proof only when both
  // recorded and current identities are valid and different.
  return validStart(started) && started !== owner.started
}
function removeClaim(directory, claim) {
  // Each claim has a unique leaf. After another reaper removes this directory
  // and a starter publishes its replacement, neither unlink nor rmdir can
  // remove that replacement's different, already-present owner leaf.
  removeOwned(`${directory}/${claim.token}`, claim.fd, claim.token)
  try { rmdirSync(directory) }
  catch (error) { if (!['ENOENT', 'ENOTEMPTY', 'EEXIST'].includes(error.code)) throw error }
}
function reclaimGuard(directory) {
  let fd
  try {
    const info = statSync(directory)
    if (!info.isDirectory()) {
      // Compatibility with claims left by the old file-based protocol. New
      // claims are directories, so a late unlink cannot remove a replacement.
      fd = openSync(directory, 'r')
      const owner = JSON.parse(readFileSync(fd, 'utf8'))
      if (!reaperGone(owner)) return false
      try { removeOwned(directory, fd, owner.token) }
      catch (error) { if (!['EISDIR', 'EPERM'].includes(error.code)) throw error }
      return true
    }
    const entries = readdirSync(directory)
    if (entries.length !== 1 || !/^[a-f0-9-]{36}$/.test(entries[0])) return false
    fd = openSync(`${directory}/${entries[0]}`, 'r')
    const owner = JSON.parse(readFileSync(fd, 'utf8'))
    if (owner.token !== entries[0] || !reaperGone(owner)) return false
    removeClaim(directory, { fd, token: owner.token })
    return true
  } catch (error) {
    if (error.code === 'ENOENT') return true
    // Malformed claims remain for inspection. Never infer a dead owner from
    // a read failure or an incomplete identity record.
    if (error instanceof SyntaxError) return false
    throw error
  } finally { if (fd !== undefined) closeSync(fd) }
}
function guard(path) {
  const directory = `${path}.guard`, token = randomUUID()
  const prepared = `${directory}.${token}.tmp`
  // Publish a nonempty directory in one atomic rename. This closes both the
  // incomplete-record crash window and the stale-reaper/new-owner unlink race.
  mkdirSync(prepared, { mode: 0o700 })
  let claim, published = false
  try {
    claim = createFile(`${prepared}/${token}`, token)
    for (let attempt = 0; attempt < 3; attempt++) {
      try { renameSync(prepared, directory); published = true; break }
      catch (error) {
        if (!['EEXIST', 'ENOTEMPTY', 'ENOTDIR'].includes(error.code)) throw error
        if (!reclaimGuard(directory)) break
      }
    }
    if (!published) throw new Error(`Another Aeon browser suite is acquiring or recovering ${path}; recovery guard retained at ${directory}. Retry after it finishes.`)
    return () => { try { removeClaim(directory, claim) } finally { closeSync(claim.fd) } }
  } finally {
    if (!published) {
      try {
        if (claim) removeClaim(prepared, claim)
        else rmdirSync(prepared)
      } finally { if (claim) closeSync(claim.fd) }
    }
  }
}
function ownerDead(owner) {
  if (!validOwner(owner)) return false
  try { process.kill(owner.pid, 0); return false }
  catch (error) { return error.code === 'ESRCH' }
}
function assertGone(state) {
  if (state.rows.length || state.unknown.length || state.unverified.length || state.issues.length) throw new Error('Stale browser lock retained: process identity or cleanup could not be proven')
}

async function recover(path, fd, owner, graceMs, interrupted) {
  const log = `${path}.${owner.token}.groups`, tracker = trackGroups(log)
  // Bad rows retain the lock, but must not prevent verified sibling cleanup.
  let errors = tracker.signal('SIGTERM')
  const deadline = Date.now() + graceMs
  while (tracker.snapshot().rows.length && Date.now() < deadline && !interrupted()) await delay(50)
  errors.push(...tracker.signal('SIGKILL'))
  const killDeadline = Date.now() + 2000
  while (tracker.snapshot().rows.length && Date.now() < killDeadline) await delay(50)
  assertGone(tracker.snapshot())
  if (errors.length) throw new AggregateError(errors, 'Stale browser lock retained after incomplete signalling')
  // The exclusive guard spans reaping, removing the old inode and acquiring
  // its replacement, so a competing starter cannot observe a free lane early.
  if (!sameFile(fstatSync(fd), statSync(path)) || JSON.parse(readFileSync(path, 'utf8')).token !== owner.token) throw new Error('Browser lock changed during recovery; refusing replacement')
  unlinkSync(log)
  removeOwned(path, fd, owner.token)
}

export async function acquireLock(path = suiteLockPath, { graceMs = 5000 } = {}) {
  const handlers = new Map()
  let lock, interrupt, unguard
  const checkInterrupted = () => {
    if (interrupt) throw Object.assign(new Error(`Browser lock recovery interrupted by ${interrupt}; guard ${path}.guard released`), { signal: interrupt })
  }
  // Recovery can await while holding its claim, so handle termination before
  // creating that claim. SIGKILL is handled by the next owner's guard recovery.
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    const handler = () => { interrupt ??= signal }
    handlers.set(signal, handler)
    process.on(signal, handler)
  }
  try {
    unguard = guard(path)
    try { lock = createFile(path) }
    catch (error) {
      if (error.code !== 'EEXIST') throw error
      const fd = openSync(path, 'r')
      try {
        let owner
        try { owner = JSON.parse(readFileSync(fd, 'utf8')) } catch { /* fail closed */ }
        if (!owner || !ownerDead(owner)) throw new Error(`Another Aeon browser suite holds ${path} (PID ${owner?.pid ?? 'unknown'}). Live, unknown or reused ownership is never signalled.`)
        await recover(path, fd, owner, graceMs, () => interrupt)
      } finally { closeSync(fd) }
      checkInterrupted()
      lock = createFile(path)
    }
  } finally {
    try { unguard?.() }
    finally { for (const [signal, handler] of handlers) process.off(signal, handler) }
  }
  let closed = false
  return {
    token: lock.token,
    started: lock.started,
    fd: lock.fd,
    close() { if (!closed) { closeSync(lock.fd); closed = true } },
    release({ journal = false } = {}) {
      if (closed) return
      let releaseGuard
      try {
        releaseGuard = guard(path)
        if (journal && ownedFile(path, lock.fd, lock.token)) unlinkSync(`${path}.${lock.token}.groups`)
        removeOwned(path, lock.fd, lock.token)
      } finally { try { releaseGuard?.() } finally { this.close() } }
    },
  }
}
