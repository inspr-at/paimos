// SPDX-License-Identifier: AGPL-3.0-only
// Run from web with `npm run audit:ui` (offline; uses web/tests mocked APIs).
// AUDIT_FILTER=<state-name-fragment>, AUDIT_WIDTHS=390 and AUDIT_THEMES=light
// limit a diagnostic run. The full
// run visits each route and important UI state at five widths in both themes.
// Findings and screenshots stay in this worktree under web/test-results/.
import { runPlaywright } from '../playwright-safe.mjs'
import { readdirSync } from 'node:fs'

try {
  const widths = process.env.AUDIT_WIDTHS ? process.env.AUDIT_WIDTHS.split(',').map(Number) : undefined
  const themes = process.env.AUDIT_THEMES ? process.env.AUDIT_THEMES.split(',') : undefined
  const specs = readdirSync('tests').filter(file => {
    const match = /^ui-audit-(light|dark)-(\d+)\.spec\.ts$/.exec(file)
    if (!match && file !== 'ui-audit.spec.ts') return false
    const theme = match?.[1] ?? 'light', width = Number(match?.[2] ?? 390)
    return (!widths || widths.includes(width)) && (!themes || themes.includes(theme))
  }).sort().map(file => `tests/${file}`)
  if (!specs.length) throw new Error('AUDIT_WIDTHS/AUDIT_THEMES matched no audit combinations')
  const result = await runPlaywright(['-c', 'playwright.ui.config.ts', ...specs, '--workers=1', '--reporter=line'], {
    env: { ...process.env, PLAYWRIGHT_PORT: process.env.PLAYWRIGHT_PORT ?? '5187' },
  })
  process.exitCode = result.code
} catch (error) { console.error(error.message); process.exitCode = 1 }
