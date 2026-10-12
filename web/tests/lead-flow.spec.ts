// SPDX-License-Identifier: AGPL-3.0-only
// AEON-741: the approved lead flow. Lead band in every state, the line and the
// docked lead panel, Start lead and Pause, Queue on the ticket with its progress
// line, the Agents page leads list and the expert-only manual start.
import { expect, test, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { expectStableControls } from './helpers/stable'
import { HOST_A, mockLeadFlow } from './lead-flow-fixtures'
import { controlStability } from './control-stability'

const band = (page: Page) => page.locator('section.lead')
const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details' })
const mac = process.platform === 'darwin'
const submit = mac ? 'Meta+Enter' : 'Control+Enter'
async function theme(page: Page, value: 'light' | 'dark') { await page.evaluate(choice => { document.documentElement.dataset.theme = choice }, value) }
// mockLeadFlow installs the clock, including animation frames. Native layout
// observation is the barrier for responsive geometry, independent of that clock.
async function settleLayout(target: Locator) {
  await target.evaluate(element => new Promise<void>(resolve => {
    const observer = new ResizeObserver(() => { observer.disconnect(); resolve() })
    observer.observe(element)
  }))
}

test('AEON-1195: focus cycles retain the same lead DOM rows, order and controls during delayed refresh', async ({ page }, info) => {
  const { data } = await mockLeadFlow(page, { lead: 'paused', aeon: 'paused', queue: [] })
  // Equal states exercise the project tie breaker, independent of server order.
  data.projects.reverse()
  await page.goto('/agents')
  // AEON-1210: each lead heads its project's group in Sessions.
  const rows = page.locator('.sessions .lead-row')
  await expect(rows).toHaveCount(2)
  expect(await rows.evaluateAll(list => list.map(row => row.getAttribute('data-project')))).toEqual(['AEON', 'PHAROS'])
  await page.evaluate(() => {
    const section = document.querySelector('.agents-page .sessions .table')!
    const original = [...section.querySelectorAll('.lead-row')]
    const removed: Node[] = []
    const observer = new MutationObserver(records => { for (const record of records) removed.push(...record.removedNodes) })
    observer.observe(document.body, { childList: true, subtree: true })
    Object.assign(window, { focusEvidence: { section, original, removed, observer } })
  })
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 1100 })
    // The focus refresh starts after the nested responsive containers paint;
    // a viewport transition is a separate interaction from the held reread.
    await settleLayout(page.locator('.sessions .table'))
    const guard = await controlStability(page, { aeon: rows.nth(0), pharos: rows.nth(1) })
    for (const look of ['light', 'dark'] as const) {
      await theme(page, look)
      let release!: () => void, started!: () => void
      const held = new Promise<void>(resolve => { release = resolve })
      const arrived = new Promise<void>(resolve => { started = resolve })
      const barrier = async (route: import('@playwright/test').Route) => { started(); await held; await route.fallback() }
      await page.route('**/api/projects/*/lead', barrier)
      await guard.check(async () => {
        await page.evaluate(() => { window.dispatchEvent(new Event('blur')); window.dispatchEvent(new Event('focus')) })
        await arrived
        await expect(rows).toHaveCount(2)
        expect(await page.evaluate(() => {
          const evidence = (window as unknown as { focusEvidence: { section: Element; original: Element[]; removed: Node[] } }).focusEvidence
          return evidence.section.isConnected && evidence.original.every((row, index) => row === document.querySelectorAll('.sessions .lead-row')[index]) && !evidence.removed.some(node => node === evidence.section || evidence.original.some(row => node === row || node.contains(row)))
        })).toBe(true)
      })
      await page.locator('.agents-page .sessions').screenshot({ path: info.outputPath(`aeon-1195-focus-${width}-${look}.png`) })
      await guard.check(async () => {
        const responses = Promise.all(['p-aeon', 'p-pharos'].map(id => page.waitForResponse(response => new URL(response.url()).pathname === `/api/projects/${id}/lead`)))
        release()
        await Promise.all((await responses).map(response => response.finished()))
        await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
        await expect(rows).toHaveCount(2)
        expect(await rows.evaluateAll(list => list.map(row => row.getAttribute('data-project')))).toEqual(['AEON', 'PHAROS'])
      })
      await page.unroute('**/api/projects/*/lead', barrier)
      data.projects.reverse()
      await guard.check(async () => {
        await page.evaluate(async () => {
          // The dev server serves the real store; reread the same project set
          // in the opposite HTTP order without navigating or replacing the view.
          const { useProjects } = await import('/src/stores/projects.ts' /* @vite-ignore */)
          await useProjects().load(true)
        })
        expect(await rows.evaluateAll(list => list.map(row => row.getAttribute('data-project')))).toEqual(['AEON', 'PHAROS'])
      })
    }
    guard.done()
  }
  expect(await page.evaluate(() => {
    const evidence = (window as unknown as { focusEvidence: { section: Element; original: Element[]; removed: Node[]; observer: MutationObserver } }).focusEvidence
    evidence.observer.disconnect()
    return evidence.section.isConnected && evidence.original.every((row, index) => row === document.querySelectorAll('.sessions .lead-row')[index]) && !evidence.removed.some(node => node === evidence.section || evidence.original.some(row => node === row || node.contains(row)))
  })).toBe(true)
})

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
      { name: 'pick a fixed host', run: async () => { await sheet.getByRole('radio', { name: /build-7/ }).click(); await expect(sheet.locator('.choice-note')).toContainText('stays on build-7') } },
      { name: 'a removed host explains itself in the reserved slot', run: async () => { await sheet.getByRole('radio', { name: /build-6/ }).dispatchEvent('click'); await expect(sheet.locator('.choice-note')).toContainText('being removed') } },
    ] })
    await expect(sheet.getByRole('radio', { name: /build-7/ })).toHaveAttribute('aria-checked', 'true')
    await page.screenshot({ path: info.outputPath(`start-sheet-${width}-${look}.png`) })
    if (look === 'light') expect((await new AxeBuilder({ page }).include('dialog.lead-sheet').analyze()).violations).toEqual([])
    await page.keyboard.press(submit)
    await expect(sheet).toHaveCount(0)
    const put = state.calls.find(c => c.method === 'PUT' && c.path.endsWith('/lead-settings'))
    expect(put?.body).toEqual({ revision: 2, overrides: { allowed_host_ids: [HOST_A] } })
    expect(state.calls.find(c => c.method === 'POST' && c.path === '/api/projects/p-pharos/lead')?.body).toEqual({ expected_revision: 0 })
    await expect(band(page)).toContainText('Requested · waiting for a runtime')
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
  await panel(page).getByRole('tab', { name: 'Lead', exact: true }).click()
  await expect(panel(page).getByRole('heading', { name: 'Right now' })).toBeVisible()
  for (const section of ['Workers', 'Next up', 'Before every start', 'Recent']) await expect(panel(page).getByRole('heading', { name: section })).toBeVisible()
  await expect(panel(page).locator('.admission-caption')).toContainText('All passed at the last start')
  await expect(panel(page).locator('[data-check]')).toHaveText(['Dial', 'Harness limits', 'Account room', 'Host load'])
  await expect(panel(page).locator('.log')).toContainText('Merged PHAROS-15 and handed it to release')
  await page.screenshot({ path: info.outputPath('panel-working-1440.png') })
  expect((await new AxeBuilder({ page }).include('aside.session-panel').analyze()).violations).toEqual([])
  await panel(page).getByRole('button', { name: 'Pause…', exact: true }).click()
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

test('a ticket preview over any page offers Start lead and the sheet opens there', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockLeadFlow(page, { lead: 'none', queue: ['n-4'] })
  // The Projects page has no lead views of its own: only the shell can host the sheet.
  await page.goto('/?peek=PHAROS-14')
  const preview = page.locator('.ticket-peek-host')
  await expect(preview.locator('.tline')).toContainText('Queued, waiting for a lead.')
  const start = preview.locator('.tline').getByRole('button', { name: 'Start lead' })
  await start.click()
  const sheet = page.getByRole('dialog', { name: 'Start lead for PHAROS' })
  await expect(sheet).toBeVisible()
  await expect(sheet.getByRole('button', { name: /Start lead/ })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(sheet).toHaveCount(0)
  await expect(start).toBeFocused()
})

test('leaving the page that opened the docked lead panel closes it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockLeadFlow(page, { lead: 'working', aeon: 'none' })
  await page.goto('/agents')
  // The working lead's own session heads PHAROS; its queue chip opens the lead panel (AEON-1210).
  await page.locator('.sessions .lead-row[data-project="PHAROS"] .qchip.q-l1').click()
  await expect(panel(page)).toBeVisible()
  await page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Projects' }).click()
  await expect(page).toHaveURL(/\/$/)
  await expect(panel(page)).toHaveCount(0)
})

test('ticket header keeps every action inside the default side panel with wide system fonts', async ({ page }) => {
  await mockLeadFlow(page, { lead: 'working', queue: [] })
  // CI's Linux fonts (DejaVu Sans and Sans Mono) run wider than macOS's. Verdana
  // is wider still, and Menlo shares DejaVu Sans Mono's metrics, so the whole
  // bar, key and position included, is at least as wide here as on CI.
  const wide = '.panel-bar, .panel-bar * { font-family: Verdana, "DejaVu Sans", sans-serif !important; } .panel-bar .mono, .panel-bar .mono *, .panel-bar .key-chip, .panel-bar .key-chip *, .panel-bar kbd { font-family: Menlo, "DejaVu Sans Mono", monospace !important; }'
  // 1024 and 1440 open the dock at its default width (the CI stale-progress
  // spec lost Close at both); the dock's own width decides, not the viewport.
  for (const width of [1024, 1440]) for (const key of ['PHAROS-14', 'PHAROS-12']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.goto(`/p/PHAROS/${key}`)
    await page.addStyleTag({ content: wide })
    const drawer = page.getByRole('complementary', { name: 'Ticket details' })
    const bar = drawer.locator('.panel-bar-main')
    await expect(drawer.locator('.q-action')).toBeVisible()
    const frame = (await bar.boundingBox())!
    for (const name of ['Edit', 'More actions', 'Close ticket details']) {
      const box = (await drawer.getByRole('button', { name, exact: true }).boundingBox())!
      expect(box.x + box.width, `${width} ${key}: ${name} stays inside the header`).toBeLessThanOrEqual(frame.x + frame.width + 0.5)
      expect(box.x + box.width, `${width} ${key}: ${name} stays inside the viewport`).toBeLessThanOrEqual(width + 0.5)
    }
    expect(await bar.evaluate(el => el.scrollWidth - el.clientWidth), `${width} ${key}: the header does not overflow`).toBeLessThanOrEqual(0)
    // The fold gives up what More also offers first: the default desktop dock keeps Queue's word.
    if (width === 1440) await expect(drawer.locator('.q-action .q-word').first()).toBeVisible()
  }
})

test('ticket header steps down at every dock width so Close stays inside with a wide system font', async ({ page }) => {
  // AEON-780: at 1440 on CI's Linux fonts the labelled Queue pushed Close out of
  // the dock (work-queue.spec.ts:529). Same wide fonts as the test above.
  const wide = '.panel-bar, .panel-bar * { font-family: Verdana, "DejaVu Sans", sans-serif !important; } .panel-bar .mono, .panel-bar .mono *, .panel-bar .key-chip, .panel-bar .key-chip *, .panel-bar kbd { font-family: Menlo, "DejaVu Sans Mono", monospace !important; }'
  await mockLeadFlow(page, { lead: 'working', queue: [] })
  for (const key of ['PHAROS-14', 'PHAROS-12']) {
    await page.setViewportSize({ width: 1920, height: 1000 })
    await page.goto(`/p/PHAROS/${key}`)
    await page.addStyleTag({ content: wide })
    const drawer = page.getByRole('complementary', { name: 'Ticket details' })
    const bar = drawer.locator('.panel-bar-main')
    // A wide dock keeps the full header: the steps never hide what fits.
    await expect(drawer.locator('.q-action .q-word').first()).toBeVisible()
    await expect(drawer.locator('.q-action .q-key')).toBeVisible()
    await expect(drawer.locator('.position')).toBeVisible()
    await expect(drawer.getByRole('button', { name: 'Open as full page', exact: true })).toBeVisible()
    for (const width of [1920, 1600, 1440, 1366, 1280, 1180, 1024, 900, 800, 721]) {
      await page.setViewportSize({ width, height: 1000 })
      // Measure after the real layout observers, not the fixture's fake frames.
      await settleLayout(bar)
      const geometry = await bar.evaluate(el => {
        const frame = el.getBoundingClientRect()
        const buttons = [...el.querySelectorAll('button')].map(button => {
          const rect = button.getBoundingClientRect()
          return { name: button.getAttribute('aria-label') ?? button.textContent?.trim() ?? '', left: rect.left, right: rect.right, width: rect.width }
        }).filter(button => button.width > 0)
        return { left: frame.left, right: frame.right, fold: el.parentElement?.dataset.fold, overflow: el.scrollWidth - el.clientWidth, buttons }
      })
      expect(geometry.overflow, `${key} at ${width}: the header does not overflow (fold ${geometry.fold}, width ${geometry.right - geometry.left})`).toBeLessThanOrEqual(0)
      for (const name of ['Close ticket details', 'More actions', 'Edit']) expect(geometry.buttons.map(b => b.name), `${key} at ${width}: ${name} is shown`).toContain(name)
      for (const button of geometry.buttons) {
        expect(button.left, `${key} at ${width}: ${button.name} starts inside the header`).toBeGreaterThanOrEqual(geometry.left - 0.5)
        expect(button.right, `${key} at ${width}: ${button.name} ends inside the header`).toBeLessThanOrEqual(geometry.right + 0.5)
      }
      await expect(drawer.getByRole('button', { name: 'Close ticket details' })).toBeInViewport({ ratio: 1 })
    }
    // A person-sized dock keeps its width during a viewport resize. A later
    // font metric change must still fold its labels; observing only the bar's
    // width cannot see this growth.
    await page.setViewportSize({ width: 1440, height: 1000 })
    await settleLayout(bar)
    const before = (await bar.boundingBox())!
    const fonts = await page.addStyleTag({ content: '.project-page.ticket-dock { --panel-user-w: 560px; } .panel-bar .q-word { font-size: 40px !important; }' })
    try {
      await page.setViewportSize({ width: 1441, height: 1000 })
      await settleLayout(bar)
      const after = (await bar.boundingBox())!
      expect(Math.abs(after.width - before.width), 'the dock width stayed fixed').toBeLessThanOrEqual(0.5)
      expect(await bar.evaluate(el => el.scrollWidth - el.clientWidth), `${key}: late font metrics stay inside the fixed dock`).toBeLessThanOrEqual(0)
      await expect(drawer.getByRole('button', { name: 'Close ticket details' })).toBeInViewport({ ratio: 1 })
    } finally { await fonts.evaluate(el => el.remove()) }
  }
})

for (const look of ['light', 'dark'] as const) test(`Agents page: one line per lead; + offers Start lead only while a project has none (${look})`, async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockLeadFlow(page, { lead: 'working', aeon: 'none', queue: ['n-4', 'n-a1'] })
  await page.goto('/agents')
  await theme(page, look)
  // AEON-1210: Sessions heads PHAROS with its lead row and AEON, which has queued work and no lead, with one slim line.
  const leads = page.locator('.agents-page .sessions')
  await expect(leads.locator('.lead-row[data-project="PHAROS"]')).toContainText('Claude lead')
  await expect(leads.locator('.lead-row[data-project="PHAROS"] .role')).toHaveText('Lead')
  await expect(leads.locator('.row.nolead[data-project="AEON"]')).toContainText('1 queued · no lead')
  await page.screenshot({ path: info.outputPath(`agents-leads-${look}.png`) })
  await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /^Start lead…/ })).toContainText('AEON has none. One per project.')
  await expect(page.getByRole('menuitem', { name: /Start agent manually/ })).toHaveCount(0)
  await page.getByRole('menuitem', { name: /^Start lead…/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Start lead for AEON' })
  await expect(sheet.locator('[data-row="project"]')).toContainText('The only project without a lead.')
  await page.keyboard.press('Escape')
  await leads.locator('.lead-row[data-project="PHAROS"] .qchip.q-l1').click()
  await expect(panel(page)).toBeVisible()
})

test('Start lead: the project holds still while its request is on the way, and the sheet still finishes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { state } = await mockLeadFlow(page, { lead: 'none', aeon: 'none' })
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/projects/*/lead', async route => { if (route.request().method() === 'POST') await held; await route.fallback() })
  await page.goto('/agents')
  await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
  await page.getByRole('menuitem', { name: /^Start lead…/ }).click()
  const sheet = page.getByRole('dialog', { name: /^Start lead for / }), start = sheet.locator('[data-act="start"]')
  const choices = sheet.getByRole('radiogroup', { name: 'Project' }).getByRole('radio')
  await expect(choices).toHaveCount(2)
  await expect(start).toHaveAttribute('aria-disabled', 'false')
  const heading = await sheet.getByRole('heading').textContent()
  await start.click()
  await expect(start).toHaveAttribute('aria-disabled', 'true')
  await sheet.locator('.project-choices [aria-checked="false"]').dispatchEvent('click')
  await expect(sheet.getByRole('heading')).toHaveText(heading!)
  release()
  await expect(sheet).toHaveCount(0)
  const posts = state.calls.filter(c => c.method === 'POST' && /\/lead$/.test(c.path))
  expect(posts.map(c => c.path)).toEqual([`/api/projects/${heading!.includes('AEON') ? 'p-aeon' : 'p-pharos'}/lead`])
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
  const head = await panel(page).getByRole('button', { name: 'Pause…', exact: true }).boundingBox()
  expect(head!.y).toBeLessThan(80)
  expect(head!.height).toBeGreaterThanOrEqual(44)
  await page.screenshot({ path: info.outputPath(`phone-panel-${look}.png`) })
  await panel(page).getByRole('button', { name: 'Pause…', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Pause the PHAROS lead' })
  const action = await sheet.locator('[data-act="pause"]').boundingBox()
  expect(action!.y + action!.height).toBeGreaterThan(844 - 80)
  expect(action!.height).toBeGreaterThanOrEqual(44)
  await page.screenshot({ path: info.outputPath(`phone-pause-${look}.png`) })
})

// AEON-1256: a running lead's card collapses to its header row; the choice is the person's and survives a reload.
for (const width of [1440, 400]) test(`AEON-1256: the lead card collapses to its header row and stays collapsed after a reload at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: width < 600 ? 844 : 1000 })
  const { data } = await mockLeadFlow(page, { lead: 'working', question: true })
  await page.goto('/p/PHAROS')
  const toggle = band(page).locator('[data-act="collapse"]'), head = band(page).locator('.lead-top')
  const pause = band(page).locator('[data-act="main"]'), menu = band(page).locator('[data-act="lead-menu"]'), name = band(page).getByRole('button', { name: /PHAROS lead: open details/ })
  const below = [band(page).getByRole('group', { name: 'Where the work is' }), band(page).locator('.lead-now').first(), band(page).locator('.asks'), band(page).locator('.lead-foot')]
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(toggle).toHaveAccessibleName('Collapse lead panel')
  await expect(toggle.locator('svg')).toHaveCount(1)
  await expect(toggle).toHaveText('')
  for (const part of below) await expect(part).toBeVisible()
  await expectStableControls({ controls: { collapse: toggle, pause, menu, name }, interactions: [
    { name: 'collapse with the pointer', run: async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-expanded', 'false') } },
    { name: 'expand with Enter', run: async () => { await toggle.focus(); await page.keyboard.press('Enter'); await expect(toggle).toHaveAttribute('aria-expanded', 'true') } },
    { name: 'collapse with Space', run: async () => { await page.keyboard.press('Space'); await expect(toggle).toHaveAttribute('aria-expanded', 'false') } },
  ] })
  // Only the header row stays: name, status, Pause… and the menu; nothing below it.
  await expect(toggle).toHaveAccessibleName('Expand lead panel')
  for (const part of below) await expect(part).toBeHidden()
  await expect(band(page).locator('#lead-body')).toHaveAttribute('inert', '')
  await expect(band(page).locator('.lead-state')).toContainText('Working')
  const card = (await band(page).boundingBox())!, row = (await head.boundingBox())!
  expect(card.y + card.height - (row.y + row.height), 'the card ends just below its header row').toBeLessThanOrEqual(20)
  // The collapse control sits left of Pause… and the menu, without overlapping either.
  const c = (await toggle.boundingBox())!, p = (await pause.boundingBox())!, m = (await menu.boundingBox())!
  expect(c.x + c.width, 'collapse before Pause…').toBeLessThanOrEqual(p.x + 0.5)
  expect(p.x + p.width, 'Pause… before the menu').toBeLessThanOrEqual(m.x + 0.5)
  if (width < 600) for (const [label, rect] of [['collapse', c], ['pause', p]] as const) expect(Math.min(rect.width, rect.height), `${label} is a 44 px target`).toBeGreaterThanOrEqual(44)
  await expect.poll(() => data.preferences['ui.project.lead-panel']).toEqual({ collapsed: true })
  await page.reload()
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  for (const part of below) await expect(part).toBeHidden()
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  for (const part of below) await expect(part).toBeVisible()
  await expect.poll(() => data.preferences['ui.project.lead-panel']).toEqual({ collapsed: false })
})

// AEON-1256: the sub-label under a station count (“PHAROS-13”, “+5”) always takes its line, so the card never jumps when it appears.
for (const width of [1440, 400]) test(`AEON-1256: the stations row has one height with and without sub-labels at ${width}`, async ({ page, context }) => {
  const lineOf = async (tab: Page, options: Parameters<typeof mockLeadFlow>[1]) => {
    await tab.setViewportSize({ width, height: width < 600 ? 844 : 1000 })
    await mockLeadFlow(tab, { lead: 'paused', ...options })
    await tab.goto('/p/PHAROS')
    const line = band(tab).getByRole('group', { name: 'Where the work is' })
    await expect(line).toBeVisible()
    return line
  }
  const withKeys = await lineOf(page, {})
  await expect(withKeys.locator('[data-station="gate"] .st-keys')).toContainText('PHAROS-13')
  const bare = await lineOf(await context.newPage(), { queue: [], decisions: false })
  await expect(bare.locator('[data-station="queued"] .st-label b')).toHaveText('0')
  for (const key of await bare.locator('.st-keys').all()) await expect(key).toHaveText('')
  const a = (await withKeys.boundingBox())!, b = (await bare.boundingBox())!
  expect(Math.abs(a.height - b.height), `stations row ${a.height} px with sub-labels, ${b.height} px without`).toBeLessThanOrEqual(0.5)
})
