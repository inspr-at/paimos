// SPDX-License-Identifier: AGPL-3.0-only
import { boundFixture } from './fixture-lifecycle.mjs'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

boundFixture({ parent: Number(process.argv[2]) })
process.on('SIGTERM', () => {})
process.on('SIGINT', () => {})
setInterval(() => {}, 1000)
// Publish the timeout barrier only after the ownership preload has reaped
// its ps child. Killing during that preload can leave an orphan group.
writeFileSync(join(process.env.AEON_STUB_STATE, 'hung'), 'ready')
writeFileSync(join(process.env.AEON_STUB_STATE, 'watching'), 'ready')
