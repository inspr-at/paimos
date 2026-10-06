// SPDX-License-Identifier: AGPL-3.0-only
// AEON-741: the approved lead flow. Lead band in every state, the line and the
// docked lead panel, Start lead and Pause, Queue on the ticket with its progress
// line, the Agents page leads list and the expert-only manual start.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { expectStableControls } from './helpers/stable'
import { HOST_A, mockLeadFlow } from './lead-flow-fixtures'

const band = (page: Page) => page.locator('section.lead')
const panel = (page: Page) => page.getByRole('complementary', { name: 'PHAROS lead' })
const mac = process.platform === 'darwin'
const submit = mac ? 'Meta+Enter' : 'Control+Enter'
async function theme(page: Page, value: 'light' | 'dark') { await page.evaluate(choice => { document.documentElement.dataset.theme = choice }, value) }

for (const width of [1440, 390]) for (const look of ['light', 'dark'] as const) {
  test(`no lead: the band offers Start lead and the sheet requests one at ${width} ${look}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const { state } = await mockLeadFlow(page, { lead: 'none' })
    await page.goto('/p/PHAROS')
    await theme(page, look)
    await expect(band(page).getByRole('heading', { name: 'No lead in PHAROS' })).toBeVisible()
    await expect(band(page)).toContainText('1 queued work item waits for one')
    await expect(band(page).locator('.empty-lead li')).toHaveCount(3)
    await page.screenshot({ path: info.outputPath(`band-none-${width}-${look}.png`) })
    await band(page).getByRole('button', { name: 'Start lead' }).click()
    const sheet = page.getByRole('dialog', { name: 'Start lead for PHAROS' })
    await expect(sheet).toBeVisible()
    await expect(sheet.getByRole('button', { name: /Start lead/ })).toBeFocused()
    await expect(sheet.locator('[data-row="model"]')).toContainText('Today Claude Opus 5.5, high thinking')
    await expect(sheet.locator('[data-row="limits"]')).toContainText('up to 5 agents at once')
    const start = sheet.locator('[data-act="start"]'), cancel = sheet.locator('[data-act="cancel"]'), change = sheet.locator('[data-act="host"]')
    await expectStableControls({ controls: { start, cancel, change }, interactions: [
      { name: 'reveal hosts', run: async () => { await change.click(); await expect(sheet.getByRole('radiogroup', { name: 'Host' })).toBeVisible() } },
      { name: 'pick a fixed host', run: async () => { await sheet.getByRole('radio', { name: /mbp2607/ }).click(); await expect(sheet.locator('.choice-note')).toContainText('stays on mbp2607') } },
      { name: 'a removed host explains itself in the reserved slot', run: async () => { await sheet.getByRole('radio', { name: /mbp2606/ }).dispatchEvent('click'); await expect(sheet.locator('.choice-note')).toContainText('being removed') } },
    ] })
    await expect(sheet.getByRole('radio', { name: /mbp2607/ })).toHaveAttribute('aria-checked', 'true')
    await page.screenshot({ path: info.outputPath(`start-sheet-${width}-${look}.png`) })
    if (look === 'light') expect((await new AxeBuilder({ page }).include('dialog.lead-sheet').analyze()).violations).toEqual([])
    await page.keyboard.press(submit)
    await expect(sheet).toHaveCount(0)
    const put = state.calls.find(c => c.method === 'PUT' && c.path.endsWith('/lead-settings'))
    expect(put?.body).toEqual({ revision: 2, overrides: { allowed_host_ids: [HOST_A] } })
    expect(state.calls.find(c => c.method === 'POST' && c.path === '/api/projects/p-pharos/lead')?.body).toEqual({ expected_revision: 0 })
    await expect(band(page)).toContainText('Requested · waiting for its session')
    await expect(band(page).getByRole('button', { name: 'Cancel start' })).toBeVisible()
  })
}

test('every lead state keeps the primary action in one place and says what it needs', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  for (const [lead, reason, label, text] of [
    ['working', '', 'Pause…', 'Fixing three review findings'],
    ['waiting_for_room', 'dial_full', 'Open dial', 'Waiting for room · the dial is full'],
    ['waiting_for_room', 'host_unavailable', 'Check computers', 'host load can’t be read'],
    ['paused', 'process_stopped', 'Resume', 'Paused · handover saved'],
    ['cannot_start', 'project_archived', '', 'This project is archived'],
  ] as const) {
    await page.unrouteAll({ behavior: 'wait' })
    await mockLeadFlow(page, { lead, reason, processActive: lead === 'paused' ? false : undefined })
    await page.goto('/p/PHAROS')
    await expect(band(page)).toContainText(text)
    if (label) await expect(band(page).locator('[data-act="main"]')).toHaveText(label)
    else await expect(band(page).locator('[data-act="main"]')).toHaveCount(0)
    await page.screenshot({ path: info.outputPath(`band-${lead}-${reason || 'plain'}.png`) })
  }
})

test('working lead: the line opens the docked panel, questions go to the Decision Desk, Pause is revision-bound', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { state } = await mockLeadFlow(page, { lead: 'working', question: true })
  await page.goto('/p/PHAROS')
  const line = band(page).getByRole('group', { name: 'Where the work is' })
  await expect(line.locator('[data-station="queued"] .st-label b')).toHaveText('1')
  await expect(line.locator('[data-station="working"]')).toContainText('PHAROS-11')
  await expect(line.locator('[data-station="gate"]')).toContainText('PHAROS-13')
  await expect(line.locator('[data-station="merged"] .st-label b')).toHaveText('1')
  await expect(band(page).getByRole('link', { name: 'Answer' })).toHaveAttribute('href', /\/decision-desk\?needs=q(:|%3A)qqqqqqqq/)
  await page.screenshot({ path: info.outputPath('band-working-1440.png') })
  const pause = band(page).locator('[data-act="main"]'), name = band(page).getByRole('button', { name: /PHAROS lead: open details/ })
  await expectStableControls({ controls: { pause, name }, interactions: [
    { name: 'open the panel from a station', run: async () => { await line.locator('[data-station="queued"] .st-label').click(); await expect(panel(page)).toBeVisible() } },
    { name: 'close it with Esc', run: async () => { await page.keyboard.press('Escape'); await expect(panel(page)).toHaveCount(0) } },
  ] })
  await name.click()
  await expect(panel(page).getByRole('heading', { name: 'Right now' })).toBeVisible()
  for (const section of ['Workers', 'Next up', 'Before every start', 'Recent']) await expect(panel(page).getByRole('heading', { name: section })).toBeVisible()
  await expect(panel(page).locator('[data-check="dial"]')).toContainText('3 of 5 running')
  await expect(panel(page).locator('.log')).toContainText('Merged PHAROS-15 and handed it to release')
  await page.screenshot({ path: info.outputPath('panel-working-1440.png') })
  expect((await new AxeBuilder({ page }).include('aside.lead-panel').analyze()).violations).toEqual([])
  await panel(page).locator('[data-act="pause"]').click()
  const sheet = page.getByRole('dialog', { name: 'Pause the PHAROS lead' })
  await expect(sheet.getByRole('button', { name: /^Pause/ })).toBeFocused()
  await page.keyboard.press(submit)
  await expect(sheet).toHaveCount(0)
  expect(state.calls.find(c => c.path === '/api/projects/p-pharos/lead/pause')?.body).toEqual({ expected_revision: 4, generation: 2 })
  await expect(band(page)).toContainText('Pausing · handing over')
  // A live process keeps its slot: Resume waits for its confirmed stop.
  await expect(band(page).locator('[data-act="main"]')).toHaveAttribute('aria-disabled', 'true')
})

test('ticket: Queue keeps its place through Queue and Queued, and the line shows where it stands', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { state } = await mockLeadFlow(page, { lead: 'working', queue: [] })
  await page.goto('/p/PHAROS/PHAROS-14')
  const drawer = page.getByRole('complementary', { name: 'Ticket details' })
  const queue = drawer.locator('.q-btn'), edit = drawer.getByRole('button', { name: 'Edit', exact: true })
  await expect(drawer.locator('.tline')).toContainText('Not queued.')
  await expectStableControls({ controls: { queue, edit }, interactions: [
    { name: 'Q queues outside fields', run: async () => { await queue.focus(); await page.keyboard.press('q'); await expect(queue).toHaveAttribute('aria-pressed', 'true') } },
    { name: 'the progress line appears in the reserved slot', run: async () => { await expect(drawer.locator('.tl-step.now')).toHaveAttribute('data-step', 'queued') } },
    { name: 'Q again unqueues', run: async () => { await queue.click(); await expect(queue).toHaveAttribute('aria-pressed', 'false') } },
  ] })
  expect(state.calls.filter(c => c.path === '/api/queue' && c.method === 'POST')).toHaveLength(1)
  await page.screenshot({ path: info.outputPath('ticket-queue-1440.png') })
})

test('ticket: queued without a lead offers Start lead; a parent queues its open leaves; manual start stays expert-only', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { state } = await mockLeadFlow(page, { lead: 'none', queue: ['n-4'] })
  await page.goto('/p/PHAROS/PHAROS-14')
  const drawer = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(drawer.locator('.tline')).toContainText('Queued, waiting for a lead.')
  await expect(drawer.getByRole('button', { name: /Start now on/ })).toHaveCount(0)
  await drawer.locator('.tline').getByRole('button', { name: 'Start lead' }).click()
  await expect(page.getByRole('dialog', { name: 'Start lead for PHAROS' })).toBeVisible()
  await page.keyboard.press('Escape')
  await page.goto('/p/PHAROS/PHAROS-12')
  const parent = page.getByRole('complementary', { name: 'Ticket details' })
  const queue = parent.getByRole('button', { name: 'Queue the open work items below PHAROS-12' })
  await expect(queue).toContainText('Queue 1')
  await expect(parent.locator('.tline')).toContainText('1 open work item below.')
  await queue.click()
  await expect(page.getByText('PHAROS-12: 1 queued', { exact: true })).toBeVisible()
  expect(state.calls.find(c => c.path === '/api/queue/n-2/snapshots')?.body).toEqual({ expected_revision: expect.any(String) })
})

for (const look of ['light', 'dark'] as const) test(`Agents page: one line per lead; + offers Start lead only while a project has none (${look})`, async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockLeadFlow(page, { lead: 'working', aeon: 'none', queue: ['n-4', 'n-a1'] })
  await page.goto('/agents')
  await theme(page, look)
  const leads = page.locator('section.zone[aria-labelledby="leads-title"]')
  await expect(leads.locator('.lead-row')).toHaveCount(2)
  await expect(leads.locator('[data-project="PHAROS"]')).toContainText('PHAROS lead')
  await expect(leads.locator('[data-project="AEON"]')).toContainText('No lead in AEON')
  await page.screenshot({ path: info.outputPath(`agents-leads-${look}.png`) })
  await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /^Start lead…/ })).toContainText('AEON has none. One per project.')
  await expect(page.getByRole('menuitem', { name: /Start agent manually/ })).toHaveCount(0)
  await page.getByRole('menuitem', { name: /^Start lead…/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Start lead for AEON' })
  await expect(sheet.locator('[data-row="project"]')).toContainText('The only project without a lead.')
  await page.keyboard.press('Escape')
  await leads.locator('[data-project="PHAROS"]').click()
  await expect(panel(page)).toBeVisible()
})

test('Agents page: the expert setting reveals Start agent manually', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockLeadFlow(page, { lead: 'working', aeon: 'working', expert: true })
  await page.goto('/agents')
  await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /^Start lead…/ })).toHaveCount(0)
  await expect(page.getByRole('menuitem', { name: /Start agent manually/ })).toBeVisible()
})

for (const look of ['light', 'dark'] as const) test(`phone: band, full-height panel and pinned sheet bar at 390 (${look})`, async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockLeadFlow(page, { lead: 'working', question: true, longText: true })
  await page.goto('/p/PHAROS')
  await theme(page, look)
  await expect(band(page)).toContainText('Working')
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
  await page.screenshot({ path: info.outputPath(`phone-band-${look}.png`) })
  await band(page).getByRole('button', { name: /PHAROS lead: open details/ }).click()
  const box = await panel(page).boundingBox()
  expect(box?.width).toBe(390); expect(box?.y).toBe(0)
  const foot = await panel(page).locator('[data-act="pause"]').boundingBox()
  expect(foot!.y + foot!.height).toBeGreaterThan(844 - 80)
  await page.screenshot({ path: info.outputPath(`phone-panel-${look}.png`) })
  await panel(page).locator('[data-act="pause"]').click()
  const sheet = page.getByRole('dialog', { name: 'Pause the PHAROS lead' })
  const action = await sheet.locator('[data-act="pause"]').boundingBox()
  expect(action!.y + action!.height).toBeGreaterThan(844 - 80)
  expect(action!.height).toBeGreaterThanOrEqual(44)
  await page.screenshot({ path: info.outputPath(`phone-pause-${look}.png`) })
})
