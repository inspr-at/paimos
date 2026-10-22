// SPDX-License-Identifier: AGPL-3.0-only
// Risk: saving a PNG, a missing reference, or a rewritten reference can turn a
// broken comparison into a green case. This synthetic document is only a
// counterexample for the comparator; it cannot supply a product reference.
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { test, expect } from '@playwright/test'

test('visual comparison rejects a changed control and never creates a missing reference', async ({ page }, info) => {
  expect(info.config.updateSnapshots).toBe('none')
  await page.setContent('<style>body { margin: 0; background: white; } button { width: 160px; height: 44px; background: white; color: black; border: 1px solid black; }</style><button>Review change</button>')
  const reference = info.snapshotPath('synthetic-reference.png')
  mkdirSync(dirname(reference), { recursive: true })
  writeFileSync(reference, await page.screenshot({ path: info.outputPath('synthetic-reference.png') }))
  await expect(page).toHaveScreenshot('synthetic-reference.png', { maxDiffPixels: 0 })
  await page.getByRole('button').evaluate(element => { element.style.background = 'rgb(220, 0, 0)' })
  await expect(expect(page).toHaveScreenshot('synthetic-reference.png', { maxDiffPixels: 0, timeout: 1500 })).rejects.toThrow(/Screenshot comparison failed|pixels.*different/)
  await expect(expect(page).toHaveScreenshot('missing-reference.png', { timeout: 1500 })).rejects.toThrow(/snapshot.*doesn.t exist|snapshot.*not exist/i)
})
