// SPDX-License-Identifier: AGPL-3.0-only
import { boundFixture } from '../fixture-lifecycle.mjs'

boundFixture()
process.on('SIGTERM', () => {})
process.on('SIGINT', () => {})
setInterval(() => {}, 1000)
