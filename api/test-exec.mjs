// SPDX-License-Identifier: AGPL-3.0-only
// go test -exec "node /absolute/checkout/api/test-exec.mjs" ./...
// Prepares the contract for runners that test a fresh committed checkout.
import { spawnSync } from 'node:child_process'
import { generateOpenAPI } from './generate.mjs'

try {
  if (!process.argv[2]) throw new Error('Expected Go test executable')
  generateOpenAPI()
  const result = spawnSync(process.argv[2], process.argv.slice(3), { stdio: 'inherit' })
  if (result.error) throw result.error
  if (result.signal) process.kill(process.pid, result.signal)
  else process.exitCode = result.status ?? 1
} catch (error) { console.error(error.message); process.exitCode = 1 }
