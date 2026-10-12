// SPDX-License-Identifier: AGPL-3.0-only
// Coordinator: PLAYWRIGHT_PORT=5825 npm test -- agents-hierarchy.spec.ts
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const lead = '5e000000-0000-4000-8000-000000000001'
const child = '5e000000-0000-4000-8000-000000000002'
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)
async function signal(page: Page, name: string) {
  await page.evaluate(name => window.dispatchEvent(new CustomEvent('test:agents-signal', { detail: name })), name)
}
async function setup(page: Page) {
  await mockWork(page, fixtures(), { admin: true })
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      closed = false
      receive = (event: Event) => {
        const kind = (event as CustomEvent<string>).detail
        if (kind === 'disconnect') this.onerror?.(new Event('error'))
        else if (kind === 'reconnect') this.onopen?.(new Event('open'))
        else this.dispatchEvent(new Event(kind))
      }
      constructor() {
        super()
        window.addEventListener('test:agents-signal', this.receive)
        setTimeout(() => { if (!this.closed) this.onopen?.(new Event('open')) }, 0)
      }
      close() { this.closed = true; window.removeEventListener('test:agents-signal', this.receive) }
    }
    Object.assign(window, { EventSource: Stream })
  })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
    'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'n-1': { key: 'PHAROS-11', title: 'Coordinator' }, 'n-2': { key: 'PHAROS-12', title: 'Hierarchy worker' },
  } })
  data.sessions.splice(2)
  data.approvals.splice(0)
  data.messages.splice(0)
  data.targets.splice(0)
  Object.assign(data.sessions[0]!, { display_label: 'Release lead', agent: { id: 'shared', name: 'aeon-coordinator' } })
  Object.assign(data.sessions[1]!, { parent_harness_session_id: lead, agent_principal_id: data.sessions[0]!.agent_principal_id, display_label: 'AC4 hierarchy', agent: { id: 'shared', name: 'aeon-coordinator' } })
  const calls = await mockAgents(page, data)
  return { data, calls }
}

test('workers nest by session ID, display their label, harness and ticket, and expand with the keyboard', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  await expect(row(page, lead)).toContainText('1 worker')
  await expect(row(page, child)).toHaveAttribute('data-depth', '1')
  await expect(row(page, child)).toHaveAttribute('data-parent', lead)
  await expect(row(page, child).locator('.ava')).toHaveAttribute('aria-label', /AC4 hierarchy:/)
  await expect(row(page, child).locator('.live-bot')).toHaveAttribute('data-harness', 'codex')
  await expect(row(page, child).locator('.key-inline')).toHaveText('PHAROS-12 ·')
  // The fold button is reached from its row: ← and → fold and unfold; Enter and Space work on the button.
  const toggle = row(page, lead).getByRole('button', { name: 'Fold Release lead’s workers: 1 worker, 1 working' })
  await toggle.focus()
  await page.keyboard.press('Enter')
  await expect(row(page, child)).toHaveCount(0)
  await page.keyboard.press('Space')
  await expect(row(page, child)).toBeVisible()
  // From the row itself, ← folds the open lead and → unfolds it again.
  await row(page, lead).focus()
  await page.keyboard.press('ArrowLeft')
  await expect(row(page, child)).toHaveCount(0)
  await expect(row(page, lead)).toHaveAttribute('aria-expanded', 'false')
  await page.keyboard.press('ArrowRight')
  await expect(row(page, child)).toBeVisible()
  await expect(row(page, lead)).toHaveAttribute('aria-expanded', 'true')
  // A fresh list says nothing about its age; only a delay shows in the head (AEON-780 replaced the last-updated stamp).
  await expect(page.locator('.page-head .freshness')).toHaveCount(0)
})

test('new workers update through the live channel; a stopped worker keeps its place for 10 minutes, then folds into recoverable history', async ({ page }) => {
  const { data } = await setup(page)
  const worker = data.sessions.pop()!
  await page.clock.install()
  await page.goto('/agents')
  await expect(row(page, lead)).toBeVisible()
  data.sessions.push(worker)
  await signal(page, 'harness.registered')
  await page.clock.runFor(500)
  await expect(row(page, child)).toBeVisible({ timeout: 3000 })
  Object.assign(worker, { phase: 'stopped', stopped_at: new Date().toISOString(), stop_reason: 'process_exited' })
  await signal(page, 'harness.stopped')
  await page.clock.runFor(500)
  // AEON-1211: it keeps its place and end state, and says when it moves; then, with the pointer away, it folds away.
  await expect(row(page, child)).toHaveClass(/stopped/)
  await expect(row(page, child).locator('.l2t')).toContainText('moves to Ended at')
  await page.mouse.move(0, 0)
  await page.clock.runFor(10 * 60_000 + 2_000)
  await expect(row(page, child)).toHaveCount(0)
  await row(page, lead).getByRole('button', { name: 'Show stopped workers of Release lead' }).click()
  await expect(row(page, child)).toBeVisible()
  await expect(row(page, child)).toHaveClass(/stopped/)
})

test('returning to a visible tab refreshes immediately and failed reads keep the last success visible', async ({ page }) => {
  const { data } = await setup(page)
  // The last good read lands in the first minute (a whole minute just after the fixtures' clock);
  // the next read fails ten minutes later.
  const lastGood = Math.ceil(Date.now() / 60_000) * 60_000, failedAt = lastGood + 10 * 60_000
  await page.clock.install({ time: lastGood })
  await page.goto('/agents')
  await expect(row(page, child).locator('.ava')).toHaveAttribute('aria-label', /AC4 hierarchy:/)
  await page.route('**/api/harness-sessions?*', route => route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } }))
  await page.clock.pauseAt(failedAt)
  const delayed = page.locator('.page-head .freshness')
  await expect(delayed).toContainText('Update delayed')
  // The failed read keeps the last good list and dates it: the head's tip and the footer (AEON-785)
  // name the last success, never the failed read.
  await expect(row(page, child).locator('.ava')).toHaveAttribute('aria-label', /AC4 hierarchy:/)
  const [good, failed] = await page.evaluate(times => times.map(at => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })), [lastGood, failedAt])
  expect(good).not.toBe(failed)
  const footer = page.locator('.app-footer .sum')
  await expect(footer).toHaveAttribute('data-conn', 'off')
  await expect(footer).toHaveAttribute('aria-label', `Live updates paused, retrying. The page shows the state of ${good}.`)
  await expect(delayed).toHaveAttribute('data-tip', /last update /)
  expect(await delayed.getAttribute('data-tip')).not.toContain(await page.evaluate(at => new Date(at).toLocaleString(), failedAt))
  // Back on the tab, the list is read at once, well before the 20-second poll.
  await page.unroute('**/api/harness-sessions?*')
  Object.assign(data.sessions[1]!, { display_label: 'Returned worker' })
  await page.clock.resume()
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await expect(row(page, child).locator('.ava')).toHaveAttribute('aria-label', /Returned worker:/, { timeout: 3000 })
  // Only a delay is said in the head; once updates land again it goes (AEON-780: no "Live").
  await expect(delayed).toHaveCount(0)
  await expect(footer).toHaveAttribute('data-conn', 'on')
})

test('a stopped lead with a live grandchild stays visible and direct worker links open their ancestors', async ({ page }) => {
  const { data } = await setup(page)
  Object.assign(data.sessions[0]!, { phase: 'stopped', stopped_at: new Date(Date.now() - 60_000).toISOString() })
  const grandchild = '5e000000-0000-4000-8000-000000000003'
  data.sessions.push({ ...data.sessions[1]!, id: grandchild, parent_harness_session_id: child, display_label: 'Nested scout' } as typeof data.sessions[number])
  await page.goto(`/agents/${grandchild}`)
  await expect(row(page, lead)).toBeVisible()
  await expect(row(page, grandchild)).toHaveAttribute('data-depth', '2')
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('Nested scout')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`nested workers pass axe and fit a narrow viewport in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await setup(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/agents')
    await expect(row(page, child)).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    const results = await new AxeBuilder({ page }).include('.agents-page').withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'best-practice']).analyze()
    expect(results.violations).toEqual([])
  })
}
