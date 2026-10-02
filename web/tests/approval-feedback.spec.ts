// SPDX-License-Identifier: AGPL-3.0-only
// AEON-505: approve and deny confirm on the card once the server answered, fold into
// Decided with a count tick, move focus on, and never jump the page.
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentMockOptions, type AgentWorld } from './agents-fixtures'
import { COLLAPSE_MS, SUCCESS_MS } from '../src/components/agents/approvalSettle'

const world: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
async function setup(page: Page, options: AgentMockOptions & { only?: string; emptyHistory?: boolean } = {}) {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  if (options.emptyHistory) data.approvals = data.approvals.filter(a => !a.decision && Date.parse(a.expires_at) > Date.now())
  // One request and nothing else waiting: deciding it empties Needs you.
  if (options.only) {
    data.approvals = data.approvals.filter(a => a.decision || Date.parse(a.expires_at) <= Date.now() || a.scope === options.only)
    data.messages = data.messages.filter(m => !m.is_action_request)
  }
  const calls = await mockAgents(page, data, options)
  return { data, calls }
}
async function openAgents(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(queue(page).locator('[data-row^="a:"]').first()).toBeVisible()
}
// The answer waits until the test lets it through, so "before the server" is observable.
async function holdDecisions(page: Page) {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/approvals/*/decision', async route => { await gate; await route.fallback() })
  return release
}
const queue = (page: Page) => page.getByRole('region', { name: 'Needs you' })
const card = (page: Page, id: string) => queue(page).locator(`[data-row="a:${id}"]`)
const settled = (page: Page) => queue(page).locator('.item.settled')
const decidedCount = (page: Page) => queue(page).locator('.history-toggle .mono')
const live = (page: Page) => page.locator('.agents-page > [aria-live="polite"]')
const idOf = (data: Awaited<ReturnType<typeof setup>>['data'], scope: string) => data.approvals.find(a => a.scope === scope && !a.decision)!.id

test.describe('with motion', () => {
  test.use({ reducedMotion: 'no-preference' })

  test('approve shows success only after the server, then folds into Decided and moves focus on', async ({ page }) => {
    const { data, calls } = await setup(page)
    const release = await holdDecisions(page)
    await openAgents(page)
    const claim = idOf(data, 'run.claim'), read = idOf(data, 'nodes.read')
    await expect(decidedCount(page)).toHaveText('4')
    await expect(queue(page).locator('.count-badge')).toHaveText('4')
    const item = card(page, claim)
    const who = (await item.locator('.who-name').textContent())!.trim()
    await item.getByRole('button', { name: 'Approve', exact: true }).click()
    await item.getByLabel('Reason (optional)').fill('Fine for this run.')
    await item.getByRole('button', { name: 'Approve permission' }).click()
    // In flight: saving, and no success yet.
    await expect(item.getByRole('button', { name: 'Saving…' })).toBeDisabled()
    await expect.poll(() => calls.filter(c => c.path.endsWith('/decision')).length).toBe(0)
    await expect(settled(page)).toHaveCount(0)
    // Hold the short collapse phase so hosted scheduling cannot skip it.
    await page.clock.install({ time: new Date() })
    await page.clock.pauseAt(new Date(Date.now() + 100))
    release()
    const done = settled(page)
    await expect(done).toHaveClass(/approved/)
    await expect(done.locator('.settled-line')).toHaveText(`Approved·${who} may claim a run and start work`)
    await expect(done.locator('.settled-reason')).toHaveText('“Fine for this run.”')
    await expect(done.locator('.settled-mark svg')).toBeVisible()
    await expect(live(page)).toHaveText(`Approved. ${who} may claim a run and start work. Reason: Fine for this run.`)
    // The next request takes focus and the cursor; Decided has not taken it yet.
    await expect(card(page, read)).toBeFocused()
    await expect(card(page, read)).toHaveClass(/active/)
    await expect(decidedCount(page)).toHaveText('4')
    await expect(queue(page).locator('.count-badge')).toHaveText('4')
    // The fold: Decided ticks, the card goes, the count follows.
    await page.clock.runFor(SUCCESS_MS)
    await expect(done).toHaveClass(/collapsing/)
    await expect(decidedCount(page)).toHaveText('5')
    await expect(decidedCount(page)).toHaveClass(/tick/)
    await page.clock.runFor(COLLAPSE_MS)
    await expect(done).toHaveCount(0)
    await expect(queue(page).locator('[data-row^="a:"]')).toHaveCount(2)
    await expect(queue(page).locator('.count-badge')).toHaveText('3')
    await expect(page.locator('.toast')).toHaveCount(0)
    expect(calls.find(c => c.path.endsWith('/decision'))?.body).toEqual({ decision: 'approved', reason: 'Fine for this run.' })
    await queue(page).getByRole('button', { name: /^Decided/ }).click()
    await expect(queue(page).locator('.past')).toHaveCount(5)
  })

  test('deny shows a neutral outcome with the reason', async ({ page }) => {
    const { data } = await setup(page)
    await openAgents(page)
    const control = idOf(data, 'harness.control')
    const item = card(page, control)
    const who = (await item.locator('.who-name').textContent())!.trim()
    await item.getByRole('button', { name: 'Deny', exact: true }).click()
    await item.getByLabel('Why not? The agent sees this.').fill('Use the staging account instead.')
    await page.keyboard.press('Enter')
    const done = settled(page)
    await expect(done).toHaveClass(/denied/)
    await expect(done.locator('.settled-line')).toHaveText(`Denied·${who} may not interrupt or stop agent sessions`)
    await expect(done.locator('.settled-reason')).toHaveText('“Use the staging account instead.”')
    // Neutral: ink, not the ok hue and not the danger hue.
    const ink = await page.evaluate(() => { const probe = document.createElement('span'); probe.style.color = 'var(--ink)'; document.body.append(probe); const value = getComputedStyle(probe).color; probe.remove(); return value })
    await expect(done.locator('.settled-word')).toHaveCSS('color', ink)
    await expect(live(page)).toHaveText(`Denied. ${who} may not interrupt or stop agent sessions. Reason: Use the staging account instead.`)
    await expect(card(page, idOf(data, 'run.claim'))).toBeFocused()
    await expect(done).toHaveCount(0)
    await expect(decidedCount(page)).toHaveText('5')
  })

  for (const mode of ['approve', 'deny'] as const) test(`first ${mode} with empty history never shifts content down and keeps scroll and focus`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 640 })
    const { data } = await setup(page, { emptyHistory: true })
    const release = await holdDecisions(page)
    await openAgents(page)
    const item = card(page, idOf(data, mode === 'approve' ? 'run.claim' : 'harness.control'))
    await item.getByRole('button', { name: mode === 'approve' ? 'Approve' : 'Deny', exact: true }).click()
    const main = page.locator('#main')
    await main.evaluate(el => { el.scrollTop = 80 })
    await expect.poll(() => main.evaluate(el => el.scrollTop)).toBe(80)
    await page.keyboard.press('Enter')
    await expect(item.getByRole('button', { name: 'Saving…' })).toBeDisabled()
    const before = await page.evaluate(() => {
      const section = document.querySelector<HTMLElement>('.agents-page .main-col > .queue')!
      const next = section.nextElementSibling as HTMLElement
      const main = document.getElementById('main')!
      const probe = { samples: [] as { top: number; scroll: number }[], active: true }
      ;(window as unknown as { approvalLayoutProbe: typeof probe }).approvalLayoutProbe = probe
      const sample = () => {
        probe.samples.push({ top: next.getBoundingClientRect().top + main.scrollTop, scroll: main.scrollTop })
        if (probe.active) requestAnimationFrame(sample)
      }
      sample()
      return probe.samples[0]!.top
    })
    release()
    await expect(settled(page)).toBeVisible()
    await expect(card(page, idOf(data, mode === 'approve' ? 'nodes.read' : 'run.claim'))).toBeFocused()
    await expect(decidedCount(page)).toHaveText('0')
    await expect(settled(page)).toHaveCount(0)
    await expect(decidedCount(page)).toHaveText('1')
    await expect(card(page, idOf(data, mode === 'approve' ? 'nodes.read' : 'run.claim'))).toBeFocused()
    const samples = await page.evaluate(() => {
      const probe = (window as unknown as { approvalLayoutProbe: { samples: { top: number; scroll: number }[]; active: boolean } }).approvalLayoutProbe
      probe.active = false
      return probe.samples
    })
    // Confirmation only shrinks the card: introducing the first footer must never
    // push what follows down, even for a single frame before the card fits itself.
    expect(Math.max(...samples.map(sample => sample.top)) - before).toBeLessThanOrEqual(1.5)
    expect(samples.every(sample => sample.scroll === 80)).toBe(true)
  })

  for (const emptyHistory of [false, true]) test(`deciding the last request folds Needs you away without a jump and focuses Decided (${emptyHistory ? 'empty' : 'existing'} history)`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 640 })
    await setup(page, { only: 'nodes.read', emptyHistory })
    await openAgents(page)
    await expect(queue(page).locator('.items > li')).toHaveCount(1)
    const id = (await queue(page).locator('[data-row^="a:"]').getAttribute('data-row'))!.slice(2)
    const item = card(page, id)
    await item.getByRole('button', { name: 'Approve', exact: true }).click()
    // Scrolled into the page (#main scrolls), with the request still in view.
    const main = page.locator('#main')
    await main.evaluate(el => { el.scrollTop = 80 })
    await expect.poll(() => main.evaluate(el => el.scrollTop)).toBe(80)
    const before = await page.evaluate(() => {
      const section = document.querySelector<HTMLElement>('.agents-page .main-col > .queue')!
      const next = section.nextElementSibling as HTMLElement
      const tops: number[] = []
      const w = window as unknown as { tops: number[]; sampling: boolean }
      w.tops = tops; w.sampling = true
      const main = document.getElementById('main')!
      const sample = () => { tops.push(next.getBoundingClientRect().top + main.scrollTop); if (w.sampling) requestAnimationFrame(sample) }
      requestAnimationFrame(sample)
      return { height: section.getBoundingClientRect().height, gap: parseFloat(getComputedStyle(section.parentElement!).rowGap) }
    })
    await item.getByRole('button', { name: 'Approve permission' }).click()
    await expect(settled(page)).toBeVisible()
    await expect(queue(page)).toHaveCount(0)
    const decided = page.getByRole('region', { name: 'Decided requests' }).getByRole('button', { name: /^Decided/ })
    await expect(decided).toBeFocused()
    await expect(decided.locator('.mono')).toHaveText(emptyHistory ? '1' : '5')
    const tops = await page.evaluate(async () => {
      await new Promise(resolve => setTimeout(resolve, 150))
      const w = window as unknown as { tops: number[]; sampling: boolean }
      w.sampling = false
      return w.tops
    })
    expect(await main.evaluate(el => el.scrollTop)).toBe(80)
    // What followed Needs you rose by exactly its height and gap, over many frames,
    // with no single-frame jump and no snap when the card was removed.
    const moved = tops[0] - tops[tops.length - 1]
    expect(Math.abs(moved - (before.height + before.gap))).toBeLessThanOrEqual(1.5)
    expect(Math.max(...tops) - tops[0]).toBeLessThanOrEqual(1.5)
    const steps = tops.slice(1).map((top, index) => tops[index] - top)
    expect(steps.filter(step => step > 0.5).length).toBeGreaterThanOrEqual(6)
    expect(Math.max(...steps)).toBeLessThan(moved * 0.5)
  })
})

test('a failed decision returns the card to its actionable state with the error, never a success', async ({ page }) => {
  const { data } = await setup(page, { failDecision: true })
  await openAgents(page)
  const item = card(page, idOf(data, 'harness.control'))
  await item.getByRole('button', { name: 'Approve', exact: true }).click()
  await item.getByLabel('Reason (optional)').fill('Fine for this run.')
  await item.getByRole('button', { name: 'Approve permission' }).click()
  await expect(item.getByRole('alert')).toHaveText('The request expired while you were deciding')
  await expect(settled(page)).toHaveCount(0)
  await expect(item.getByRole('button', { name: 'Approve permission' })).toBeEnabled()
  await expect(item.getByLabel('Reason (optional)')).toHaveValue('Fine for this run.')
  await expect(item.getByLabel('Reason (optional)')).toBeFocused()
  await expect(live(page)).toHaveText('')
  await expect(decidedCount(page)).toHaveText('4')
})

test('reduced motion: the success state shows, then is replaced without a fold', async ({ page }) => {
  const { data } = await setup(page)
  await openAgents(page)
  // Record every class the decided card ever has.
  await page.evaluate(() => {
    const seen = new Set<string>()
    ;(window as unknown as { seen: Set<string> }).seen = seen
    new MutationObserver(() => document.querySelectorAll('.item.settled').forEach(el => { seen.add(el.className); seen.add(`transition:${getComputedStyle(el).transitionDuration}`) }))
      .observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['class', 'style'] })
  })
  const item = card(page, idOf(data, 'nodes.read'))
  await item.getByRole('button', { name: 'Approve', exact: true }).click()
  await item.getByRole('button', { name: 'Approve permission' }).click()
  const done = settled(page)
  await expect(done).toBeVisible()
  await expect(done.locator('.settled-reason')).toHaveCount(0)
  await expect(decidedCount(page)).toHaveText('4')
  await expect(done).toHaveCount(0)
  await expect(decidedCount(page)).toHaveText('5')
  const seen = await page.evaluate(() => [...(window as unknown as { seen: Set<string> }).seen])
  expect(seen.some(entry => entry.includes('success'))).toBe(true)
  expect(seen.some(entry => entry.includes('collapsing'))).toBe(false)
  expect(seen.filter(entry => entry.startsWith('transition:'))).toEqual(['transition:0s'])
  await expect(decidedCount(page)).toHaveCSS('animation-name', 'none')
})

// APPROVAL_FEEDBACK_SHOTS=<dir>: the approved and denied states in light and dark, wide and phone.
test('approval feedback screenshots', async ({ page }) => {
  const dir = process.env.APPROVAL_FEEDBACK_SHOTS
  test.skip(!dir, 'Opt-in visual evidence')
  test.setTimeout(120_000)
  mkdirSync(dir!, { recursive: true })
  // A frozen clock holds the success state while the shots are taken.
  await page.clock.install()
  const { data } = await setup(page)
  await openAgents(page)
  const shoot = async (name: string) => {
    for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme: theme })
      await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
      await queue(page).screenshot({ animations: 'disabled', path: `${dir}/${name}-${theme}-${width}.png` })
    }
  }
  const claim = card(page, idOf(data, 'run.claim'))
  await claim.getByRole('button', { name: 'Approve', exact: true }).click()
  await claim.getByLabel('Reason (optional)').fill('Fine for this run.')
  await shoot('before')
  await claim.getByRole('button', { name: 'Approve permission' }).click()
  await expect(settled(page)).toBeVisible()
  await shoot('approved')
  await page.clock.runFor(2000)
  await expect(settled(page)).toHaveCount(0)
  const control = card(page, idOf(data, 'harness.control'))
  await control.getByRole('button', { name: 'Deny', exact: true }).click()
  await control.getByLabel('Why not? The agent sees this.').fill('Use the staging account instead.')
  await control.getByRole('button', { name: 'Deny permission' }).click()
  await expect(settled(page)).toBeVisible()
  await shoot('denied')
  await page.clock.runFor(2000)
  await expect(settled(page)).toHaveCount(0)
  await shoot('after')
})
