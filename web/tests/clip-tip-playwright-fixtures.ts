// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page, type Locator } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockPairing, pairingView } from './agent-pairing-fixtures'
import { clipSession, longAgent, longComputer, longName } from './clip-tip-fixtures'

export const shots = 'test-results/aeon-632a-clip'
export const fixShots = `${shots}/tooltip-fix`
export const overflowShots = `${shots}/overflow-fix4`
export const touchShots = `${shots}/touch-fix5`
export const tableShots = `${shots}/table-fix6`
export const overflowText = Array.from({ length: 20 }, (_, index) => `${index + 1}. ${longName}`).join('\n')
export async function noOverflow(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
  const offenders = overflow > 1 ? await page.evaluate(() => [...document.querySelectorAll('main *')]
    .filter(el => el.getBoundingClientRect().right > innerWidth + 1).slice(0, 10)
    .map(el => ({ tag: el.tagName, class: el.className, width: el.getBoundingClientRect().width, right: el.getBoundingClientRect().right }))) : []
  expect(overflow, JSON.stringify(offenders)).toBeLessThanOrEqual(1)
}
export async function keyboardTip(page: Page, control: Locator, text: string) {
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
  await page.keyboard.press('ArrowRight')
  await control.focus()
  await expect(page.locator('.tooltip')).toHaveText(text)
}
export async function scrollPage(page: Page, top: number) {
  // The scroll event is asynchronous. Cross its dispatch before asserting so
  // an old, still-visible tooltip cannot satisfy the disclosure assertion.
  await page.evaluate(top => new Promise<void>(resolve => {
    window.addEventListener('scroll', () => requestAnimationFrame(() => resolve()), { once: true })
    window.scrollTo(0, top)
  }), top)
  await expect.poll(() => page.evaluate(() => scrollY)).toBe(top)
}
export async function pauseForGesture(page: Page) {
  // The protocol call crosses processes while the clock is still running.
  // Pause ahead of that transit; every gesture's tested delay starts afterward.
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1000))
}
export async function setupHarness(page: Page, theme: string, query = '') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.nodes.find(node => node.id === 'n-epic')!.title = longName
  data.nodes.push({ ...data.nodes.find(node => node.id === 'n-epic')!, id: 'short-epic', key: 'PHAROS-99', title: 'Kurz' })
  await mockWork(page, data)
  await mockPairing(page)
  await page.route('**/api/harness-sessions?*', route => route.fulfill({ json: { items: [{ ...clipSession, heartbeat_at: new Date().toISOString() }], next_cursor: null } }))
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [
    pairingView({ computer_id: '33333333-3333-4333-8333-333333333333', computer_name: longComputer, computer_state: 'connected', state: 'redeemed', setup_state: 'connected', connectivity: 'online', local_processes: 'drained', local_cleanup: 'confirmed' }),
    pairingView({ computer_id: '33333333-3333-4333-8333-333333333334', request_id: '11111111-1111-4111-8111-111111111112', computer_name: longComputer, computer_state: 'revoked', state: 'revoked', local_processes: 'drained', local_cleanup: 'confirmed' }),
  ] } }))
  await page.goto(`/tests/clip-tip-harness.html${query}`)
  await expect(page.getByRole('heading', { name: 'Ganze Namen' })).toBeVisible()
  await expect(page.locator('.agent-name')).toHaveText(longAgent)
  await expect(page.locator('.computer .name')).toHaveText(longComputer)
  await page.evaluate(() => document.fonts.ready)
}
