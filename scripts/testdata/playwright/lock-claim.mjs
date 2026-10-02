// SPDX-License-Identifier: AGPL-3.0-only
import { writeFileSync } from 'node:fs'
import { acquireLock } from '../../playwright-lock.mjs'

const [path, ready] = process.argv.slice(2)
try {
  const lock = await acquireLock(path, { graceMs: 50 })
  writeFileSync(`${path}.${lock.token}.groups`, '')
  const timer = setInterval(() => {}, 1000)
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(signal, () => {
    try { lock.release({ journal: true }) }
    finally { clearInterval(timer) }
  })
  writeFileSync(ready, JSON.stringify({ token: lock.token }))
} catch (error) { console.error(error.message); process.exitCode = 1 }
