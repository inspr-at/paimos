// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1439 risk: a data refresh re-runs the session panel's opening choice, so
// the "Opened on …" line is blanked and written again and the tab a person
// picked jumps back to the opening tab. The refreshes here drop the selected
// session from one list read and bring it back in the next, then re-read the
// identity, the lead, the questions and the list once more.
import { test, expect, type Page } from '@playwright/test'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'
import { watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const WORKER_SESSION = 'aaaaaaaa-0000-4000-8000-000000000002'
const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details', exact: true })
const tab = (page: Page, name: string) => panel(page).getByRole('tab', { name: new RegExp(`^${name}`) })
const openingLine = (page: Page) => panel(page).locator('p.sr-only').filter({ hasText: /^Opened on / })
const openingWrites = (page: Page) => page.evaluate(() => (window as unknown as { openingWrites: string[] }).openingWrites)

async function setup(page: Page) {
  // Every write of the opening line from the first render on: each new line,
  // each text change inside it and each removal.
  await page.addInitScript(() => {
    const writes: string[] = [], seen = new WeakSet<Element>()
    Object.assign(window, { openingWrites: writes })
    const lines = (node: Node) => node instanceof Element
      ? [...(node.matches('p') ? [node] : []), ...node.querySelectorAll('p')].filter(p => p.classList.contains('sr-only') && /^Opened on /.test(p.textContent ?? ''))
      : []
    new MutationObserver(records => {
      for (const record of records) {
        if (record.type === 'characterData') {
          const line = record.target.parentElement
          if (line && lines(line).length) writes.push(`changed: ${line.textContent}`)
        }
        for (const node of record.addedNodes) for (const line of lines(node)) if (!seen.has(line)) { seen.add(line); writes.push(`added: ${line.textContent}`) }
        for (const node of record.removedNodes) for (const line of lines(node)) writes.push(`removed: ${line.textContent}`)
      }
    }).observe(document, { subtree: true, childList: true, characterData: true })
  })
  const world = await mockLeadFlow(page, { lead: 'working' })
  // Markus's report: the lead panel opened on Chat, his default.
  world.data.preferences['agents:lead-panel'] = { pinned: 'chat' }
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (['/api/models', '/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
    return route.fallback()
  })
  return world
}

async function refreshSessions(page: Page) {
  await page.evaluate(async () => { await (await import('/src/stores/agents.ts' /* @vite-ignore */)).useAgents().refreshSessions() })
}

for (const width of [1440, 390]) for (const look of ['light', 'dark'] as const) {
  test(`AEON-1439: ${width} ${look} refreshes keep the chosen tab and write the opening line once`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
    const errors = watchErrors(page), world = await setup(page)
    await page.goto(`/agents/${LEAD_SESSION}`)
    await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
    await expect(openingLine(page)).toHaveText('Opened on Chat: your default')
    await tab(page, 'Details').click()
    await expect(tab(page, 'Details')).toHaveAttribute('aria-selected', 'true')
    await expect(panel(page).locator('#session-panel-overview')).toBeVisible()

    const controls = { header: panel(page).locator('.pane-bar'), tabs: panel(page).getByRole('tablist'), details: tab(page, 'Details'), close: panel(page).getByRole('button', { name: 'Close session details', exact: true }) }
    await expectStableControls({ controls, scrollAreas: { panel: panel(page) }, interactions: [
      { name: 'one list read drops the selected session, the next brings it back', run: async () => {
        const index = world.data.live.findIndex(row => row.session_id === LEAD_SESSION)
        const [lead] = world.data.live.splice(index, 1)
        await refreshSessions(page)
        // The drop reached the panel: without its session it has no tabs.
        await expect(panel(page).getByRole('tablist')).toHaveCount(0)
        world.data.live.splice(index, 0, lead!)
        await refreshSessions(page)
        await expect(tab(page, 'Details')).toHaveAttribute('aria-selected', 'true')
      } },
      { name: 'identity, lead, question and list re-reads', run: async () => {
        // Each read is awaited, so an opening re-run by the restore above has
        // finished its own reads before the tab is checked again.
        await page.evaluate(async () => {
          await (await import('/src/stores/session.ts' /* @vite-ignore */)).useSession().refresh()
          await (await import('/src/stores/projectLeads.ts' /* @vite-ignore */)).useProjectLeads().load('p-pharos')
          await (await import('/src/stores/agentProjection.ts' /* @vite-ignore */)).useAgentProjection().refresh()
          await (await import('/src/stores/agents.ts' /* @vite-ignore */)).useAgents().refreshSessions()
          await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
        })
        await expect(tab(page, 'Details')).toHaveAttribute('aria-selected', 'true')
        await expect(panel(page).locator('#session-panel-overview')).toBeVisible()
      } },
    ] })
    await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'false')
    expect(await openingWrites(page)).toEqual(['added: Opened on Chat: your default'])
    await page.screenshot({ path: info.outputPath(`AEON-1439/details-after-refreshes-${width}-${look}.png`) })

    if (width === 1440) {
      // Another session and the lead again are real openings: each picks its documented tab.
      await page.locator(`[data-row="s:${WORKER_SESSION}"] .agent-link`).click()
      await expect(panel(page)).toContainText('PHAROS-11')
      await expect(tab(page, 'Details')).toHaveAttribute('aria-selected', 'true')
      await page.locator(`[data-row="s:${LEAD_SESSION}"] .agent-link`).click()
      await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
      await expect(openingLine(page)).toHaveText('Opened on Chat: your default')
    }
    expect(errors).toEqual([])
  })
}
