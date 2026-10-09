// SPDX-License-Identifier: AGPL-3.0-only
import { test as base } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { browserIdentity } from './browser-impact.mjs'
import { captureBrowser } from './browser-coverage.mjs'
import { boundedText } from './inputs.mjs'
export * from '@playwright/test'

export const test = base.extend({
  aeonBrowserCoverage: [async ({ browser }, use, testInfo) => {
    const capture = captureBrowser(browser)
    try { await use() } finally {
      const observation = await capture.finish()
      const expected = JSON.parse(boundedText(fileURLToPath(new URL('../../', import.meta.url)), process.env.AEON_BROWSER_IMPACT_CASES) ?? '[]')
      const file = relative(process.cwd(), testInfo.file).replaceAll('\\', '/')
      const title = testInfo.titlePath.join(' › ')
      const matches = expected.filter(row => row.file === file && row.project === testInfo.project.name &&
        (title === row.name || title.endsWith(` › ${row.name}`)) && row.line === testInfo.line)
      if (matches.length === 1) {
        const row = matches[0]
        const directory = process.env.AEON_BROWSER_IMPACT_DIRECTORY
        mkdirSync(directory, { recursive: true })
        const record = { ...row, ...observation, status: testInfo.status }
        const name = createHash('sha256').update(browserIdentity(row)).digest('hex')
        writeFileSync(resolve(directory, `${name}.json`), JSON.stringify(record) + '\n')
      }
    }
  }, { auto: true }],
})
export default test
