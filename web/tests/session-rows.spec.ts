// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const shotDir = resolve('..', '.agent-shots')

async function setup(page: Page, theme: 'light' | 'dark') {
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    me: me.id,
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: {
      'p-pharos': { key: 'PHAROS', title: 'Pharos' },
      'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning across every environment in the studio fleet' },
      'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    },
  })
  const lead = data.sessions[0]!
  Object.assign(lead, { brief: 'Keep fleet membership consistent while the connector lands' })
  const quiet = data.sessions[4]!
  Object.assign(quiet, { display_label: 'x'.repeat(96) })
  await mockAgents(page, data)
  return { lead, quiet }
}

for (const pass of [1, 2]) for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) for (const detail of [false, true]) {
  test(`session rows ${theme} ${width} detail ${detail ? 'open' : 'closed'} pass ${pass}`, async ({ page }) => {
    const errors = watchErrors(page)
    const { lead, quiet } = await setup(page, theme)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(detail ? `/agents/${lead.id}` : '/agents')
    const row = page.locator(`[data-row="s:${lead.id}"]`)
    await expect(row.locator('.result')).toHaveText('Keep fleet membership consistent while the connector lands')
    await expect(row.locator('.session-name')).toHaveText('camy')
    await expect(row.locator('.role')).toHaveText('Lead')
    await expect(row.locator('.harness-badge')).toHaveAttribute('data-harness', 'claude')
    const badge = await row.locator('.harness-badge').boundingBox()
    expect(badge!.width).toBeGreaterThanOrEqual(12)
    expect(badge!.width).toBeLessThanOrEqual(14)
    expect(badge!.height).toBeLessThanOrEqual(14)
    await expect(row.locator('.exec-model')).toContainText('claude-fable-high')
    await expect(row.locator('.exec-model')).toHaveText('claude-fable-high')
    await expect(row.locator('.exec-account')).toContainText('Claude Max')
    await expect(row.locator('.provider-mark')).toHaveAttribute('data-provider', 'anthropic')
    await expect(row.locator('.result')).not.toContainText('heartbeat')
    const quietRow = page.locator(`[data-row="s:${quiet.id}"]`)
    await expect(quietRow.locator('.exec-model')).toHaveCount(0)
    await expect(quietRow.locator('.exec-account')).not.toContainText('unknown')
    await expect(quietRow.locator('.provider-mark')).toHaveAttribute('data-provider', 'unknown')
    const full = 'x'.repeat(96)
    const result = quietRow.locator('.result')
    await expect(result).toHaveAttribute('title', full)
    const resultBox = (await result.boundingBox())!
    expect(resultBox.x).toBeGreaterThanOrEqual(0)
    expect(resultBox.x + resultBox.width).toBeLessThanOrEqual(width + 1)
    // Phones wrap a long result to two lines and clamp it there (AEON-304); either way it is cut, never spilled.
    if (width === 390) expect(await result.evaluate(el => el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1)).toBe(true)
    await quietRow.locator('.agent-link').focus()
    await expect(quietRow.locator('.agent-link')).toBeFocused()
    // Focusing a row opens nothing else: no pacing editor, no pool menu.
    await expect(page.locator('.cap .ed, .cap .menu')).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
    const border = await row.evaluate(el => getComputedStyle(el).borderLeftWidth)
    expect(border).toBe('0px')
    mkdirSync(shotDir, { recursive: true })
    await page.screenshot({ path: resolve(shotDir, `sr1-pass${pass}-${theme}-${width}-${detail ? 'open' : 'closed'}.png`), fullPage: true })
    expect(errors).toEqual([])
  })
}
