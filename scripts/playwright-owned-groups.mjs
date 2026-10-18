// SPDX-License-Identifier: AGPL-3.0-only
// Playwright starts browsers in detached process groups. Record those groups
// synchronously at spawn without changing Playwright's own shutdown behavior.
import childProcess from 'node:child_process'
import { appendFileSync, closeSync, constants, openSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { processStart, recordSpawnedGroup } from './playwright-processes.mjs'

export function recordDetachedChild(child, options, append, identityOptions) {
  if (!child.pid || !options?.detached) return child
  return recordSpawnedGroup(child, append, identityOptions)
}

const path = process.env.AEON_PW_GROUP_LOG
if (path) {
  const append = entry => {
    // Never recreate a journal already removed by crash recovery.
    const fd = openSync(path, constants.O_WRONLY | constants.O_APPEND)
    try { appendFileSync(fd, `${JSON.stringify(entry)}\n`) } finally { closeSync(fd) }
  }
  const owner = Number(process.env.AEON_PW_OWNER)
  // The supervisor directly spawns its detached root. Descendants are already
  // recorded by their parent's spawn hook and must not register as roots.
  if (process.ppid === owner) recordSpawnedGroup({ pid: process.pid }, append)
  // Check after publishing the root: a recovery that saw an empty journal
  // cannot race this preload into starting work after the supervisor's death.
  if (owner) {
    try {
      process.kill(owner, 0)
      if (processStart(owner) !== process.env.AEON_PW_OWNER_STARTED) process.exit(1)
    } catch { process.exit(1) }
  }
  const record = (child, options) => recordDetachedChild(child, options, append)
  const spawn = childProcess.spawn, fork = childProcess.fork
  childProcess.spawn = function (command, args, options) {
    return record(spawn.call(this, command, args, options), Array.isArray(args) ? options : args)
  }
  childProcess.fork = function (modulePath, args, options) {
    return record(fork.call(this, modulePath, args, options), Array.isArray(args) ? options : args)
  }
  syncBuiltinESMExports()
}
