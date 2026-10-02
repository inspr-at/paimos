// SPDX-License-Identifier: AGPL-3.0-only
import { spawn } from 'node:child_process'

// A cold recovery cannot prove ownership of helpers whose leader has exited.
// The test creates this group directly and cleans up only these known children.
const leader = spawn(process.execPath, ['--input-type=module', '-e', `
  import { spawn } from 'node:child_process'
  import { processStart } from ${JSON.stringify(new URL('../../playwright-processes.mjs', import.meta.url).href)}
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })
  console.log(JSON.stringify({ pid: process.pid, started: processStart(process.pid), helper: child.pid }))
  process.exit(0)
`], { detached: true, stdio: ['ignore', 'pipe', 'inherit'] })
leader.stdout.pipe(process.stdout)
leader.once('exit', code => { process.exitCode = code })
