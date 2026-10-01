// SPDX-License-Identifier: AGPL-3.0-only
// Playwright starts browsers in detached process groups. Record those groups
// synchronously at spawn without changing Playwright's own shutdown behavior.
import childProcess from 'node:child_process'
import { appendFileSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'

const path = process.env.AEON_PW_GROUP_LOG
if (path) {
  const record = (child, options) => {
    if (child.pid && options?.detached) {
      const identity = childProcess.spawnSync('ps', ['-p', String(child.pid), '-o', 'lstart='], { encoding: 'utf8' })
      appendFileSync(path, `${JSON.stringify({ pid: child.pid, started: identity.stdout.trim().replaceAll(/\s+/g, ' ') })}\n`)
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
