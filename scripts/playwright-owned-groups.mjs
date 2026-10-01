// SPDX-License-Identifier: AGPL-3.0-only
// Playwright starts browsers in detached process groups. Record those groups
// synchronously at spawn without changing Playwright's own shutdown behavior.
import childProcess from 'node:child_process'
import { appendFileSync, closeSync, constants, openSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { processStart, processTable, validStart } from './playwright-processes.mjs'

const path = process.env.AEON_PW_GROUP_LOG
if (path) {
  const append = entry => {
    // Never recreate a journal already removed by crash recovery.
    const fd = openSync(path, constants.O_WRONLY | constants.O_APPEND)
    try { appendFileSync(fd, `${JSON.stringify(entry)}\n`) } finally { closeSync(fd) }
  }
  const root = processTable().find(row => row.pid === process.pid && row.group === process.pid)
  if (root) append({ pid: root.pid, started: root.started })
  // A root whose supervisor died before recording it must not begin a suite.
  const owner = Number(process.env.AEON_PW_OWNER)
  if (owner) {
    try {
      process.kill(owner, 0)
      if (processStart(owner) !== process.env.AEON_PW_OWNER_STARTED) process.exit(1)
    } catch { process.exit(1) }
  }
  const record = (child, options) => {
    if (child.pid && options?.detached) {
      const started = processStart(child.pid)
      if (!validStart(started)) {
        try { process.kill(child.pid, 0) } catch (error) { if (error.code === 'ESRCH') return child }
      }
      append({ pid: child.pid, started })
    }
    return child
  }
  const spawn = childProcess.spawn, fork = childProcess.fork
  childProcess.spawn = function (command, args, options) {
    return record(spawn.call(this, command, args, options), Array.isArray(args) ? options : args)
  }
  childProcess.fork = function (modulePath, args, options) {
    return record(fork.call(this, modulePath, args, options), Array.isArray(args) ? options : args)
  }
  syncBuiltinESMExports()
}
