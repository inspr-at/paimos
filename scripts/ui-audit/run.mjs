// SPDX-License-Identifier: AGPL-3.0-only
// Run from web with `npm run audit:ui` (offline; uses web/tests mocked APIs).
// AUDIT_FILTER=<state-name-fragment>, AUDIT_WIDTHS=390 and AUDIT_THEMES=light
// limit a local diagnostic run. The full
// run visits each route and important UI state at five widths in both themes.
// Findings and screenshots stay in this worktree under web/test-results/.
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const web = resolve(root, 'web')
const playwright = resolve(web, 'node_modules/.bin/playwright')
const result = spawnSync(playwright, ['test', '-c', 'playwright.ui.config.ts', '--project=audit', '--workers=1', '--reporter=line'], {
  cwd: web, stdio: 'inherit', env: { ...process.env, UI_AUDIT_AGGREGATE: '1', PLAYWRIGHT_PORT: process.env.PLAYWRIGHT_PORT ?? '5187' },
})
if (result.error) { console.error(result.error.message); process.exitCode = 1 }
else process.exitCode = result.status ?? 1
