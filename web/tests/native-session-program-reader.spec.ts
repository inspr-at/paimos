// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1309: retain the panel authority and stability regressions after balancing the spec files.
import { expect, test } from '@playwright/test'
import { WORKER, setup, refresh } from './native-session-program-fixtures'

test('AEON-1309: negative control reproduces reader drift with the anchor disabled', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  const { state, worker } = await setup(page)
  // Disable only this fix in the browser's test module. No throwaway source
  // commit, production fixture, other worktree or model process is involved.
  await page.route('**/src/lib/readerAnchor.ts', async route => {
    const response = await route.fetch(), original = await response.text()
    const disabled = original.replace(/onBeforeUpdate\(anchor.capture\);?/, '').replace(/onUpdated\(anchor.restore\);?/, '')
    expect(disabled).not.toBe(original)
    await route.fulfill({ response, body: disabled })
  })
  await page.goto(`/agents/${WORKER}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('tab', { name: 'Details', exact: true })).toHaveAttribute('aria-selected', 'true')
  await panel.locator('#session-panel-overview').evaluate(el => { (el as HTMLElement).style.overflowAnchor = 'none' })
  const facts = panel.locator('.block.first')
  await facts.evaluate(el => { const scroller = el.closest<HTMLElement>('#session-panel-overview')!; scroller.scrollTop += el.getBoundingClientRect().top - scroller.getBoundingClientRect().top })
  const before = await facts.evaluate(el => el.getBoundingClientRect().top)
  state.note = 'Neue Erkenntnisse zur Sitzungssteuerung und zur Wiederherstellung sichern die aktuelle Prozessidentität.'
  worker.heartbeat_at = '2026-10-06T10:00:04Z'; state.native.revision++
  await refresh(page)
  await expect(panel.locator('.now-step')).toContainText('Neue Erkenntnisse')
  const after = await facts.evaluate(el => el.getBoundingClientRect().top)
  expect(Math.abs(after - before), 'the positive control must fail without reader anchoring').toBeGreaterThan(.5)
})
