// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1637 risk: on a phone, a lead panel showing Events cannot leave it. Every tap on Lead,
// Details or Chat shows "Something went wrong with that action" and the tab stays on Events
// (live r137, native context 503 feature_disabled). The tabs' long-press timer called setTimeout
// on another object: Safari and Firefox apply WebIDL's receiver check to Window's timers and
// throw, Chromium does not, so this spec applies Safari's rule and taps with a touch pointer.
import { test, expect, type Page } from '@playwright/test'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'
import { watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

test.use({ hasTouch: true })

const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details', exact: true })
const tab = (page: Page, name: string) => panel(page).getByRole('tab', { name: new RegExp(`^${name}`) })
const PANES: Record<string, string> = { Lead: '#session-panel-lead', Details: '#session-panel-overview', Chat: '#session-panel-messages', Events: '#session-panel-events' }

async function setup(page: Page) {
  await page.addInitScript(() => {
    for (const name of ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] as const) {
      const timer = window[name] as (...args: unknown[]) => unknown
      Object.defineProperty(window, name, { configurable: true, writable: true, value: function (this: unknown, ...args: unknown[]) {
        if (this !== undefined && this !== null && this !== window) throw new TypeError(`Can only call Window.${name} on instances of Window`)
        return timer.apply(window, args)
      } })
    }
  })
  const errors = watchErrors(page), world = await mockLeadFlow(page, { lead: 'working' })
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (['/api/models', '/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
    if (path === `/api/projects/p-pharos/harness-sessions/${LEAD_SESSION}/native-context`) return route.fulfill({ status: 503, json: { reason: 'feature_disabled', request_id: null, retryable: false } })
    return route.fallback()
  })
  return { errors, world }
}

async function leaveEvents(page: Page) {
  const leave = (name: string) => ({ name: `Events, then ${name}`, run: async () => {
    await tab(page, 'Events').tap()
    await expect(tab(page, 'Events')).toHaveAttribute('aria-selected', 'true')
    await tab(page, name).tap()
    await expect(tab(page, name)).toHaveAttribute('aria-selected', 'true')
    await expect(panel(page).locator(PANES[name]!)).toBeVisible()
    await expect(page.locator('.toast.error')).toHaveCount(0)
  } })
  await expectStableControls({
    controls: { tabs: panel(page).getByRole('tablist'), lead: tab(page, 'Lead'), details: tab(page, 'Details'), chat: tab(page, 'Chat'), events: tab(page, 'Events') },
    interactions: ['Lead', 'Details', 'Chat'].map(leave),
  })
}

// This regression needs real touch dispatch, not a viewport/theme matrix.
for (const width of [390]) for (const look of ['light'] as const) {
  test(`AEON-1637: ${width} ${look} a tap leaves Events for Lead, Details and Chat without an error, from a link and from the default`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
    const { errors, world } = await setup(page)

    // A link opens the panel on Events.
    await page.goto(`/agents/${LEAD_SESSION}?tab=events`)
    await expect(tab(page, 'Events')).toHaveAttribute('aria-selected', 'true')
    await expect(panel(page).locator(PANES.Events!)).toHaveText('Live events are not available for this session yet.')
    await page.screenshot({ path: info.outputPath(`AEON-1637/events-${width}-${look}.png`) })
    await leaveEvents(page)

    // Markus's panel: Chat is the default and Events the tab used last; it opens on Chat.
    world.data.preferences['view:lead-panel'] = { last: 'events', default: 'chat' }
    await page.goto(`/agents/${LEAD_SESSION}`)
    await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
    await leaveEvents(page)
    await page.screenshot({ path: info.outputPath(`AEON-1637/chat-after-events-${width}-${look}.png`) })

    // The hold that shares these touches still opens a tab's default menu, without switching.
    await tab(page, 'Events').tap()
    const touch = { pointerType: 'touch', pointerId: 7, isPrimary: true, button: 0, bubbles: true }
    await tab(page, 'Lead').dispatchEvent('pointerdown', touch)
    await expect(page.getByRole('menuitem', { name: 'Open here by default', exact: true })).toBeVisible()
    await tab(page, 'Lead').dispatchEvent('pointerup', touch)
    await tab(page, 'Lead').dispatchEvent('click', { detail: 1, bubbles: true })
    await expect(tab(page, 'Events')).toHaveAttribute('aria-selected', 'true')
    expect(errors).toEqual([])
  })
}
