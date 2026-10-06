// SPDX-License-Identifier: AGPL-3.0-only
// AEON-780: the Agents head is one line whose counts filter Sessions.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'

const lead = '5e000000-0000-4000-8000-000000000001'
const child = '5e000000-0000-4000-8000-000000000002'
const other = '5e000000-0000-4000-8000-000000000003'
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)
const count = (page: Page, filter: string) => page.locator(`.page-head .count[data-filter="${filter}"]`)

// A working lead whose worker has a problem, and one unrelated working session.
async function setup(page: Page) {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  data.sessions.splice(3)
  data.approvals.splice(0); data.messages.splice(0); data.targets.splice(0)
  Object.assign(data.sessions[0]!, { display_label: 'Release lead' })
  Object.assign(data.sessions[1]!, { parent_harness_session_id: lead, agent_principal_id: data.sessions[0]!.agent_principal_id, display_label: 'Failing worker', has_problem: true, stop_reason: 'worker failed: exit 2' })
  Object.assign(data.sessions[2]!, { display_label: 'Unrelated worker', activity: 'busy' })
  await mockAgents(page, data)
}

for (const width of [1440, 390]) {
  test(`one head line; a count filters Sessions, keeps ancestors as context and clears with × or Esc at ${width}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    await setup(page)
    await page.goto('/agents')
    const head = page.locator('.agents-page .page-head')
    const title = head.getByRole('heading', { level: 1, name: 'Agents' })
    const add = head.getByRole('button', { name: /^New: / })
    const more = head.getByRole('button', { name: 'More agent actions', exact: true })
    await expect(count(page, 'problem')).toContainText('1problem')
    // No workspace eyebrow and no "Live" in the head.
    await expect(head.locator('.eyebrow')).toHaveCount(0)
    await expect(head).not.toContainText('Live')
    await expect(count(page, 'working')).toContainText('2working')
    await expect(count(page, 'problem')).toHaveClass(/hot/)
    await expect(count(page, 'problem')).toHaveAttribute('data-tip', /^Show Failing worker: /)
    // The + is the page's one primary button, without the base glow.
    expect(await add.evaluate(el => getComputedStyle(el).boxShadow)).not.toMatch(/\b(24|30)px/)
    const titleBox = (await title.boundingBox())!, addBox = (await add.boundingBox())!, moreBox = (await more.boundingBox())!
    // Title and controls share line 1; on desktop the counts share it too.
    expect(Math.abs((titleBox.y + titleBox.height / 2) - (moreBox.y + moreBox.height / 2))).toBeLessThan(4)
    const countBox = (await count(page, 'working').boundingBox())!
    if (width > 720) {
      expect(Math.abs((countBox.y + countBox.height / 2) - (moreBox.y + moreBox.height / 2))).toBeLessThan(4)
      await expect(count(page, 'waiting')).toBeVisible()
    } else {
      expect(countBox.y).toBeGreaterThan(titleBox.y + titleBox.height - 1)
      expect(addBox.height).toBeGreaterThanOrEqual(44)
      expect(moreBox.width).toBeGreaterThanOrEqual(44)
      expect(countBox.height).toBeGreaterThanOrEqual(44)
      // Zeros hide on phones.
      await expect(count(page, 'waiting')).toBeHidden()
    }
    await expect(count(page, 'waiting')).toHaveAttribute('aria-disabled', 'true')
    const chip = page.locator('.sessions .filter-chip')
    await expectStableControls({
      controls: { title, add, more, working: count(page, 'working'), problem: count(page, 'problem') },
      interactions: [
        { name: 'filter to the problem', run: async () => {
          await count(page, 'problem').click()
          await expect(count(page, 'problem')).toHaveAttribute('aria-pressed', 'true')
          await expect(chip).toContainText('Problem')
          await expect(row(page, child)).toBeVisible()
          await expect(row(page, lead)).toHaveClass(/filter-context/)
          await expect(row(page, other)).toHaveCount(0)
        } },
        { name: 'Esc clears', run: async () => {
          await page.keyboard.press('Escape')
          await expect(chip).toHaveCount(0)
          await expect(row(page, other)).toBeVisible()
          await expect(row(page, lead)).not.toHaveClass(/filter-context/)
        } },
        { name: 'chip × clears and returns focus', run: async () => {
          await count(page, 'working').click()
          await expect(row(page, child)).toHaveCount(0)
          await chip.getByRole('button', { name: 'Show all sessions' }).click()
          await expect(chip).toHaveCount(0)
          await expect(count(page, 'working')).toBeFocused()
          await expect(row(page, child)).toBeVisible()
        } },
      ],
    })
    // A zero count does nothing.
    if (width > 720) {
      await count(page, 'waiting').click({ force: true })
      await expect(count(page, 'waiting')).toHaveAttribute('aria-pressed', 'false')
      await expect(chip).toHaveCount(0)
    }
    await page.screenshot({ path: testInfo.outputPath(`agents-head-${width}.png`) })
  })
}

test('"…" keeps five items; moved actions have their home on the page', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  const more = page.locator('.page-head').getByRole('button', { name: 'More agent actions', exact: true })
  await more.click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitem')).toHaveText([/^Model preferences/, /^Usage/, /^Decision Desk/, /^Agent keys/, /^Agent settings/])
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await expect(more).toBeFocused()
  // History sits in the Sessions head.
  await page.locator('.sessions .head-tools').getByRole('button', { name: /^Show history/ }).click()
  await expect(page.getByRole('heading', { level: 2, name: 'History' })).toBeVisible()
})
