// SPDX-License-Identifier: AGPL-3.0-only
import { boundFixture } from './fixture-lifecycle.mjs'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

boundFixture({ parent: Number(process.argv[2]) })
process.on('SIGTERM', () => {})
process.on('SIGINT', () => {})
setInterval(() => {}, 1000)
writeFileSync(join(process.env.AEON_STUB_STATE, 'watching'), 'ready')
