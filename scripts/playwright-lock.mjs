// SPDX-License-Identifier: AGPL-3.0-only
import { randomUUID } from 'node:crypto'
import { closeSync, fstatSync, mkdirSync, openSync, readFileSync, readdirSync, renameSync, rmdirSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import { suiteLockPath } from './playwright-global-setup.mjs'
import { processStart, trackGroups, validStart } from './playwright-processes.mjs'

const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
export const idleLimitMs = 10 * 60_000
export const holdLimitMs = 15 * 60_000
const sameFile = (a, b) => a.dev === b.dev && a.ino === b.ino
function ownedFile(path, fd, token) {
  try { return sameFile(fstatSync(fd), statSync(path)) && JSON.parse(readFileSync(path, 'utf8')).token === token }
  catch (error) { if (error.code !== 'ENOENT') throw error; return false }
}
function removeOwned(path, fd, token) {
  if (ownedFile(path, fd, token)) unlinkSync(path)
}
function createFile(path, token = randomUUID(), fields = {}, started = processStart(process.pid)) {
  const fd = openSync(path, 'wx', 0o600)
  try {
    if (!validStart(started)) throw new Error('Cannot establish browser supervisor start time')
    writeFileSync(fd, JSON.stringify({ ...fields, pid: process.pid, token, started }))
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
function guard(path, started) {
  const directory = `${path}.guard`, token = randomUUID()
  const prepared = `${directory}.${token}.tmp`
  // Publish a nonempty directory in one atomic rename. This closes both the
  // incomplete-record crash window and the stale-reaper/new-owner unlink race.
  mkdirSync(prepared, { mode: 0o700 })
  let claim, published = false
  try {
    claim = createFile(`${prepared}/${token}`, token, {}, started)
    for (let attempt = 0; attempt < 3; attempt++) {
      try { renameSync(prepared, directory); published = true; break }
      catch (error) {
        if (!['EEXIST', 'ENOTEMPTY', 'ENOTDIR'].includes(error.code)) throw error
        if (!reclaimGuard(directory)) break
      }
    }
    if (!published) throw Object.assign(new Error(`Another Aeon browser suite is acquiring or recovering ${path}; recovery guard retained at ${directory}. Retry after it finishes.`), { code: 'LOCK_BUSY' })
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
function ownerDead(owner, { kill = process.kill } = {}) {
  if (!validOwner(owner)) return false
  try { kill(owner.pid, 0); return false }
  catch (error) { return error.code === 'ESRCH' }
}
function assertGone(state) {
  if (state.rows.length || state.unverified.length || state.issues.length) throw new Error('Stale browser lock retained: process identity or cleanup could not be proven')
}

async function recover(path, fd, owner, graceMs, interrupted, { now = Date.now, wait = delay, groups = trackGroups } = {}) {
  const log = `${path}.${owner.token}.groups`, tracker = groups(log)
  // Bad rows retain the lock, but must not prevent verified sibling cleanup.
  let errors = tracker.signal('SIGTERM')
  const deadline = now() + graceMs
  while (tracker.snapshot().rows.length && now() < deadline && !interrupted()) await wait(50)
  errors.push(...tracker.signal('SIGKILL'))
  const killDeadline = now() + 2000
  while (tracker.snapshot().rows.length && now() < killDeadline) await wait(50)
  assertGone(tracker.snapshot())
  if (errors.length) throw new AggregateError(errors, 'Stale browser lock retained after incomplete signalling')
  // The exclusive guard spans reaping, removing the old inode and acquiring
  // its replacement, so a competing starter cannot observe a free lane early.
  if (!sameFile(fstatSync(fd), statSync(path)) || JSON.parse(readFileSync(path, 'utf8')).token !== owner.token) throw new Error('Browser lock changed during recovery; refusing replacement')
  unlinkSync(log)
  removeOwned(path, fd, owner.token)
}

const timestamp = value => typeof value === 'number' && Number.isFinite(value) && value >= 0
export function holderLine(owner) {
  const date = value => timestamp(value) ? new Date(value).toISOString() : 'unknown'
  // Records from older supervisors have no progress clock. They remain live
  // until they exit; elapsed ownership alone is never evidence of a hang.
  const summary = typeof owner?.command === 'string' ? owner.command.replaceAll(/[\r\n\x00-\x1f\x7f]/g, ' ').slice(0, 160) : 'unknown'
  return `holder pid=${owner?.pid ?? 'unknown'} command=${summary} since=${date(owner?.acquired_at)} last_progress=${date(owner?.progress_at)}`
}
function verifiedLive(owner, { start = processStart, kill = process.kill } = {}) {
  return validOwner(owner) && !ownerDead(owner, { kill }) && start(owner.pid) === owner.started
}
function hung(owner, now) { return timestamp(owner?.progress_at) && now - owner.progress_at > idleLimitMs }

async function reapHung(owner, graceMs, interrupted, options) {
  const { now, wait, kill = process.kill } = options
  // The supervisor may share its caller's shell group. Signal its verified
  // PID, never -owner.pid: only the journal proves ownership of child groups.
  // Its TERM handler forwards to those groups; crash recovery below also
  // cleans detached browsers, with a fresh identity check before each signal.
  for (const [signal, budget] of [['SIGTERM', graceMs], ['SIGKILL', 2000]]) {
    if (!verifiedLive(owner, options)) break
    try { kill(owner.pid, signal) } catch (error) { if (error.code !== 'ESRCH') throw error }
    const deadline = now() + budget
    while (verifiedLive(owner, options) && now() < deadline && !interrupted()) await wait(50)
  }
  if (!ownerDead(owner, options)) throw new Error('Hung browser lock retained: supervisor death could not be proven')
}

function queueRows(directory) {
  let entries
  try { entries = readdirSync(directory) } catch (error) { if (error.code === 'ENOENT') return []; throw error }
  if (entries.length > 1024) throw new Error('Browser lock waiter queue exceeds 1024 entries')
  return entries.map(token => {
    if (!/^[a-f0-9-]{36}$/.test(token)) throw new Error('Browser lock waiter queue has an unknown entry')
    const path = `${directory}/${token}`
    let owner
    // A cancelled waiter removes only its own leaf, without waiting for the
    // guard. It may disappear between the directory snapshot and this read.
    try { owner = JSON.parse(readFileSync(path, 'utf8')) }
    catch (error) { if (error.code === 'ENOENT') return null; throw error }
    if (!validOwner(owner) || owner.token !== token || !Number.isSafeInteger(owner.order) || owner.order < 1) throw new Error('Browser lock waiter identity is incomplete')
    if (reaperGone(owner)) { unlinkSync(path); return null }
    return owner
  }).filter(Boolean).sort((a, b) => a.order - b.order || a.token.localeCompare(b.token))
}
function dequeue(directory, claim) {
  if (claim) { try { removeOwned(`${directory}/${claim.token}`, claim.fd, claim.token) } finally { closeSync(claim.fd) } }
}
function pruneQueue(directory) {
  // Only under the guard: otherwise rmdir could race another waiter's publish.
  try { rmdirSync(directory) } catch (error) { if (!['ENOENT', 'ENOTEMPTY', 'EEXIST'].includes(error.code)) throw error }
}

// wait:false retains the nonblocking primitive for probes. Browser runners use
// wait:true; every acquisition joins the FIFO, including a batch's next chunk.
export async function acquireLock(path = suiteLockPath, { graceMs = 5000, wait: queued = false, retryMs = 1000, now = Date.now, pause = delay, log = console.error, command = 'browser suite', ...identity } = {}) {
  const handlers = new Map()
  let lock, interrupt, claim
  const directory = `${path}.waiters`, recovery = { now, wait: pause, ...identity }
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
    for (;;) {
      checkInterrupted()
      let unguard, holder
      try {
        unguard = guard(path)
        const rows = queueRows(directory)
        if (queued && !claim) {
          mkdirSync(directory, { recursive: true, mode: 0o700 })
          const token = randomUUID()
          const prepared = `${path}.waiter.${token}.tmp`
          claim = createFile(prepared, token, { order: (rows.at(-1)?.order ?? 0) + 1 })
          try { renameSync(prepared, `${directory}/${token}`) }
          catch (error) { removeOwned(prepared, claim.fd, token); throw error }
          rows.push({ token: claim.token })
        }
        const turn = !rows.length || rows[0].token === claim?.token
        let fd
        try { fd = openSync(path, 'r') } catch (error) { if (error.code !== 'ENOENT') throw error }
        if (fd !== undefined) {
          try {
            try { holder = JSON.parse(readFileSync(fd, 'utf8')) } catch { /* fail closed */ }
            if (turn && ownerDead(holder, identity)) await recover(path, fd, holder, graceMs, () => interrupt, recovery)
            else if (turn && hung(holder, now()) && verifiedLive(holder, identity)) {
              // Progress updates take this same guard and replace the record
              // atomically. The idle decision cannot race a reporter update.
              const idle = Math.floor((now() - holder.progress_at) / 1000)
              await reapHung(holder, graceMs, () => interrupt, recovery)
              await recover(path, fd, holder, graceMs, () => interrupt, recovery)
              log(`hung-holder reaped ${holder.pid} idle=${idle}s`)
            } else if (!queued) throw new Error(`Another Aeon browser suite holds ${path}; ${holderLine(holder)}. Live, unknown or reused ownership is never signalled.`)
          } finally { closeSync(fd) }
        }
        checkInterrupted()
        if (turn) {
          try {
            lock = createFile(path, undefined, { acquired_at: now(), progress_at: now(), command })
            dequeue(directory, claim); claim = undefined
            pruneQueue(directory)
            break
          }
          catch (error) { if (error.code !== 'EEXIST') throw error }
        }
      } catch (error) {
        if (!queued || error.code !== 'LOCK_BUSY') throw error
        try { holder = JSON.parse(readFileSync(path, 'utf8')) } catch { /* diagnostic only */ }
      } finally { unguard?.() }
      if (!queued) throw new Error(`Another Aeon browser suite is queued for ${path}; ${holderLine(holder)}`)
      log(`Waiting for Aeon browser lock; ${holderLine(holder)}`)
      await pause(retryMs)
    }
  } finally {
    try { dequeue(directory, claim) }
    finally { for (const [signal, handler] of handlers) process.off(signal, handler) }
  }
  let closed = false
  const close = () => { if (!closed) { closeSync(lock.fd); closed = true } }
  const release = journal => {
    if (closed) return
    // A busy claim must not close the descriptor before an async retry. The
    // synchronous public primitive below still closes on failure as before.
    const releaseGuard = guard(path, lock.started)
    try {
      if (journal && ownedFile(path, lock.fd, lock.token)) unlinkSync(`${path}.${lock.token}.groups`)
      removeOwned(path, lock.fd, lock.token)
      pruneQueue(directory)
    } finally { try { releaseGuard() } finally { close() } }
  }
  return {
    token: lock.token,
    started: lock.started,
    get fd() { return lock.fd },
    progress() {
      if (closed) return false
      let unguard, next
      const temporary = `${path}.${lock.token}.tmp`
      try {
        try { unguard = guard(path, lock.started) } catch (error) { if (error.code === 'LOCK_BUSY') return false; throw error }
        if (!ownedFile(path, lock.fd, lock.token)) throw new Error('Browser lock ownership changed during progress update')
        const record = { ...JSON.parse(readFileSync(path, 'utf8')), progress_at: now() }
        next = openSync(temporary, 'wx', 0o600)
        writeFileSync(next, JSON.stringify(record))
        renameSync(temporary, path)
        closeSync(lock.fd); lock.fd = next; next = undefined
        return true
      } finally {
        if (next !== undefined) { closeSync(next); unlinkSync(temporary) }
        unguard?.()
      }
    },
    close,
    release({ journal = false } = {}) {
      try { release(journal) } finally { close() }
    },
    async releaseWhenReady({ journal = false } = {}) {
      const deadline = now() + 2000
      try {
        for (;;) {
          try { release(journal); return }
          catch (error) { if (error.code !== 'LOCK_BUSY' || now() >= deadline) throw error }
          await pause(25)
        }
      } finally { close() }
    },
  }
}
