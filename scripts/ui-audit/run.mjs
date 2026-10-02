// SPDX-License-Identifier: AGPL-3.0-only
// Run from web with `npm run audit:ui` (offline; uses web/tests mocked APIs).
// AUDIT_FILTER=<state-name-fragment>, AUDIT_WIDTHS=390 and AUDIT_THEMES=light
// limit a local diagnostic run. The full
// run visits each route and important UI state at five widths in both themes.
// Findings and screenshots stay in this worktree under web/test-results/.
import { runPlaywright } from '../playwright-safe.mjs'

try {
  const result = await runPlaywright(['-c', 'playwright.ui.config.ts', 'tests/ui-audit.spec.ts', '--workers=1', '--reporter=line'], {
    env: { ...process.env, PLAYWRIGHT_PORT: process.env.PLAYWRIGHT_PORT ?? '5187' },
  })
  process.exitCode = result.code
} catch (error) { console.error(error.message); process.exitCode = 1 }
