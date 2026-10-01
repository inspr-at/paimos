// SPDX-License-Identifier: AGPL-3.0-only
import { randomUUID } from 'node:crypto'
import { closeSync, fstatSync, openSync, readFileSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
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
function createFile(path) {
  const fd = openSync(path, 'wx', 0o600), token = randomUUID()
  try {
    const started = processStart(process.pid)
    if (!validStart(started)) throw new Error('Cannot establish browser supervisor start time')
    writeFileSync(fd, JSON.stringify({ pid: process.pid, token, started }))
    return { fd, token, started }
  } catch (error) { unlinkSync(path); closeSync(fd); throw error }
}
function guard(path) {
  // All acquisition/recovery/release transactions use the same exclusive
  // claim. If recovery itself is killed, the claim remains and fails closed.
  // It is never stolen while a reaper could still be signalling processes.
  let claim
  try { claim = createFile(`${path}.guard`) }
  catch (error) {
    if (error.code === 'EEXIST') throw new Error(`Another Aeon browser suite is acquiring or recovering ${path}; recovery guard retained. Retry after it finishes.`)
    throw error
  }
  return () => { try { removeOwned(`${path}.guard`, claim.fd, claim.token) } finally { closeSync(claim.fd) } }
}
function ownerDead(owner) {
  if (!Number.isSafeInteger(owner.pid) || owner.pid <= 1 || typeof owner.token !== 'string' || !/^[a-f0-9-]{36}$/.test(owner.token) || !validStart(owner.started)) return false
  try { process.kill(owner.pid, 0); return false }
  catch (error) { return error.code === 'ESRCH' }
}
function assertGone(state) {
  if (state.rows.length || state.unknown.length || state.unverified.length || state.issues.length) throw new Error('Stale browser lock retained: process identity or cleanup could not be proven')
}

async function recover(path, fd, owner, graceMs) {
  const log = `${path}.${owner.token}.groups`, tracker = trackGroups(log)
  // Missing/malformed journals and changed identities always fail closed.
  const initial = tracker.snapshot()
  if (initial.unknown.length || initial.unverified.length || initial.issues.length) assertGone(initial)
  let errors = tracker.signal('SIGTERM')
  const deadline = Date.now() + graceMs
  while (tracker.snapshot().rows.length && Date.now() < deadline) await delay(50)
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
  const unguard = guard(path)
  let lock
  try {
    try { lock = createFile(path) }
    catch (error) {
      if (error.code !== 'EEXIST') throw error
      const fd = openSync(path, 'r')
      try {
        let owner
        try { owner = JSON.parse(readFileSync(fd, 'utf8')) } catch { /* fail closed */ }
        if (!owner || !ownerDead(owner)) throw new Error(`Another Aeon browser suite holds ${path} (PID ${owner?.pid ?? 'unknown'}). Live, unknown or reused ownership is never signalled.`)
        await recover(path, fd, owner, graceMs)
      } finally { closeSync(fd) }
      lock = createFile(path)
    }
  } finally { unguard() }
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
