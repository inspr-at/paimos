// SPDX-License-Identifier: AGPL-3.0-only
// AEON-301: Agents → Usage is one page for capacity and usage. "Now" is the
// capacity band; below it what agents got done, what it took and where the
// waste is. Nothing unknown is drawn as a dash or a zero.
import { test, expect, type Page } from '@playwright/test'
import { watchErrors } from './work-fixtures'
import { USAGE_TZ, setupUsage } from './usage-fixtures'
import { usageDashboard } from './usage-data'

test.use({ timezoneId: USAGE_TZ })

const band = (page: Page) => page.getByRole('region', { name: 'Capacity' })
const tile = (page: Page, key: string) => page.locator(`.tile-${key}`)
const noScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)
async function open(page: Page, query = '') {
  await page.goto(`/agents/usage${query}`)
  await expect(page.getByRole('heading', { name: 'Usage', level: 1 })).toBeVisible()
}
// A dash as a value, not as punctuation in a sentence: no element holds only a dash.
async function noDashes(page: Page) {
  const placeholders = await page.locator('.usage-page *').evaluateAll(els => els.filter(el => /^[–—-]$/.test((el.textContent ?? '').trim())).length)
  expect(placeholders).toBe(0)
  expect(await page.locator('.usage-page').innerText()).not.toMatch(/Unknown|Not reported|Price unknown/)
}

test('nothing reported yet: capacity now, then done, agent time and waste — no dashes', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await setupUsage(page, { variant: 'unreported' })
  await open(page)

  // Now: one row per vendor pool, soonest reset first, with the plan sentence.
  await expect(band(page).locator('.meta')).toHaveText('5 of 6 accounts ready')
  await expect(band(page).locator('.row')).toHaveCount(4)
  await expect(band(page).locator('.row .pool-name')).toHaveText(['Claude', 'Codex', 'Grok', 'Cursor'])
  const claude = band(page).locator('[data-pool="claude"]')
  await expect(claude.locator('.reset')).toHaveText('resets 16:40 · 5-hour')
  await expect(claude.locator('.sentence')).toContainText('Today: use up to ~10% of Claude (4% so far)')
  await expect(claude.getByRole('meter')).toHaveAttribute('aria-label', 'Claude: 37% left, 4% of ~10% used today')
  const codex = band(page).locator('[data-pool="codex"]')
  await expect(codex.locator('.plan-name')).toHaveText('3 accounts · Pro · weekly')
  await expect(codex.locator('.reset')).toHaveText('resets tomorrow 18:02')
  await expect(band(page).locator('[data-pool="cursor"] .sentence')).toContainText('Ahead of pace')
  await expect(band(page).getByRole('link', { name: 'Pacing' })).toHaveAttribute('href', '/agents')

  // So far: four tiles, the cost tile is its reason with coverage.
  await expect(tile(page, 'done')).toContainText('41 tickets done')
  await expect(tile(page, 'done')).toContainText('6 released · 1% rework')
  await expect(tile(page, 'cost')).toContainText('Cost is not measured yet')
  await expect(tile(page, 'cost')).toContainText('reported by 0 of 357 sessions')
  await expect(tile(page, 'time')).toContainText('612 h agent time')
  await expect(tile(page, 'time')).toContainText('timed for 349 of 357 sessions')
  await expect(tile(page, 'waste')).toContainText('9 runs with nothing to show')

  await expect(page.getByRole('img', { name: /Tickets done per day: 41 in total/ })).toBeVisible()

  const tickets = page.getByRole('region', { name: 'Tickets' })
  await expect(tickets.locator('.card-meta')).toHaveText('64 worked · 43 done')
  await expect(tickets.locator('.ticket')).toHaveCount(8)
  await expect(tickets.locator('.ticket').first()).toContainText('AEON-292')
  await expect(tickets.locator('.ticket').first()).toContainText('Released · 14 sessions')
  await expect(tickets.locator('.ticket').first().locator('.time')).toHaveText('32 h')
  await expect(tickets.getByRole('link', { name: /AEON-292/ })).toHaveAttribute('href', '/p/AEON/AEON-292')
  await tickets.getByRole('button', { name: 'Show 3 more' }).click()
  await expect(tickets.locator('.ticket')).toHaveCount(11)
  await expect(tickets).toContainText('and 53 more tickets with less agent time')

  const waste = page.getByRole('region', { name: 'Waste' })
  await expect(waste.locator('.waste')).toHaveCount(9)
  await expect(waste.locator('.waste').first()).toContainText('Tried 4 times not done yet')
  await expect(waste.locator('.waste').nth(2)).toContainText('Stuck silent for 42 min')
  await expect(waste.getByRole('link', { name: /^Open session: Stuck, AEON-301/ })).toHaveAttribute('href', '/agents/5e000000-0000-4000-8000-000000000042')
  await expect(waste.getByRole('link', { name: 'Open ticket PHAROS-12' })).toHaveAttribute('href', /^\/p\/[A-Z0-9-]+\/PHAROS-12$/)

  const breakdown = page.getByRole('region', { name: 'Breakdown' })
  await expect(breakdown.getByRole('columnheader')).toHaveText(['Harness', 'Done', 'Sessions', 'Agent time', 'Rework'])
  await expect(breakdown.getByRole('row', { name: /Claude/ })).toContainText('22')
  await breakdown.getByRole('button', { name: 'Model' }).click()
  await expect(breakdown.getByRole('button', { name: 'Model' })).toHaveAttribute('aria-pressed', 'true')
  await expect(breakdown).toContainText('claude-opus-5-5')
  await expect(breakdown).toContainText('Model registered by 331 of 357 sessions.')
  // Nobody reported tokens, so there is no token column at all.
  await expect(breakdown.getByRole('columnheader', { name: 'Tokens' })).toHaveCount(0)
  await noDashes(page)

  await page.getByRole('button', { name: '7 days' }).click()
  await expect.poll(() => calls.at(-1) ?? '').toContain('from=2026-09-2')
  await expect(page).toHaveURL(/days=7/)
  await expect(page.getByRole('heading', { name: 'Last 7 days' })).toBeVisible()
  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(() => calls.at(-1) ?? '').toContain('project=p-pharos')
  expect(await noScroll(page)).toBe(true)
  // No coloured edge accents (AGENTS.md rule 11).
  const edges = await page.locator('.usage-page *').evaluateAll(els => els.filter(el => { const s = getComputedStyle(el); return ['Left', 'Top'].some(side => parseFloat(s[`border${side}Width` as 'borderLeftWidth']) >= 3 && s[`border${side}Style` as 'borderLeftStyle'] !== 'none') }).length)
  expect(edges).toBe(0)
  expect(errors).toEqual([])
})

test('partly reported: tokens with coverage, API spend as the only money, a token column', async ({ page }) => {
  await setupUsage(page, { variant: 'reported' })
  await open(page)
  await expect(tile(page, 'cost')).toContainText('100M tokens')
  await expect(tile(page, 'cost')).toContainText('12.40 USD at API list price')
  await expect(tile(page, 'cost')).toContainText('reported by 212 of 357 sessions')
  const tickets = page.getByRole('region', { name: 'Tickets' })
  await expect(tickets.locator('.card-meta')).toHaveText('64 worked · 43 done · tokens for 8')
  await expect(tickets.locator('.ticket').first()).toContainText('12.9M tokens')
  const breakdown = page.getByRole('region', { name: 'Breakdown' })
  await expect(breakdown.getByRole('columnheader', { name: 'Tokens' })).toBeVisible()
  await expect(breakdown.getByRole('row', { name: /Claude/ })).toContainText('61.4M')
  // A group that reported nothing has an empty cell, not a dash or a zero.
  await expect(breakdown.getByRole('row', { name: /Grok/ }).getByRole('cell').nth(4)).toHaveText('')
  await noDashes(page)
})

test('a range without sessions is one line; capacity still shows now', async ({ page }) => {
  await setupUsage(page, { variant: 'empty' })
  await open(page)
  await expect(page.getByText('No agent sessions in the last 30 days.')).toBeVisible()
  await expect(page.locator('.tiles')).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Tickets' })).toHaveCount(0)
  await expect(band(page).locator('.row')).toHaveCount(4)
})

test('without account access the capacity band is hidden and the rest works', async ({ page }) => {
  await setupUsage(page, { variant: 'unreported', noAccounts: true })
  await open(page)
  await expect(tile(page, 'done')).toContainText('41 tickets done')
  await expect(band(page)).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Now' })).toHaveCount(0)
})

test('no accounts yet: one line and the one action that fixes it', async ({ page }) => {
  await setupUsage(page, { variant: 'unreported', noPools: true })
  await open(page)
  await expect(band(page)).toContainText('No accounts yet. Sign in to a harness on a connected computer and it appears here.')
  await expect(band(page).getByRole('link', { name: 'Connect computer' })).toHaveAttribute('href', '/agents/register-agent')
  await expect(band(page).locator('.band-foot')).toHaveCount(0)
})

test('a pool measured by some of its accounts says how many, never one account as the pool', async ({ page }) => {
  await setupUsage(page, { variant: 'unreported', unmeasured: true })
  await open(page)
  const codex = band(page).locator('[data-pool="codex"]')
  await expect(codex.locator('.plan-name')).toHaveText('Pro · weekly')
  await expect(codex.locator('.sentence')).toContainText('Readings from 2 of 3 accounts.')
  await expect(codex.getByRole('meter')).toHaveAttribute('aria-label', /^Codex, readings from 2 of 3 accounts: \d+% left/)
  // A fully measured pool keeps its plain account count.
  await expect(band(page).locator('[data-pool="claude"] .sentence')).not.toContainText('Readings from')
})

test('a failed accounts read is an error with retry, never "No accounts yet"', async ({ page }) => {
  await setupUsage(page, { variant: 'unreported' })
  let fail = true
  await page.route(/\/api\/agent-accounts(\?.*)?$/, async route => {
    if (fail) return route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } })
    return route.fallback()
  })
  await open(page)
  await expect(tile(page, 'done')).toContainText('41 tickets done')
  await expect(band(page).getByRole('alert')).toHaveText('Capacity could not be loaded right now. Try again')
  await expect(band(page)).not.toContainText('No accounts yet')
  fail = false
  await band(page).getByRole('button', { name: 'Try again' }).click()
  await expect(band(page).locator('.row')).toHaveCount(4)
  await expect(band(page).getByRole('alert')).toHaveCount(0)
})

test('a failed read says so in one line and retries', async ({ page }) => {
  await setupUsage(page, { variant: 'unreported' })
  let fail = true
  await page.route('**/api/usage/dashboard**', async route => {
    if (fail) { fail = false; return route.fulfill({ status: 503, json: { error: 'session usage is not available' } }) }
    return route.fallback()
  })
  await open(page)
  await expect(page.getByRole('alert')).toHaveText('Usage could not be loaded right now.Try again')
  await page.getByRole('button', { name: 'Try again' }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(tile(page, 'done')).toContainText('41 tickets done')
})

test('at 390 the page stacks without horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setupUsage(page, { variant: 'reported' })
  await open(page)
  await expect(tile(page, 'waste')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Breakdown' }).getByRole('columnheader')).toHaveText(['Harness', 'Done', 'Agent time'])
  expect(await noScroll(page)).toBe(true)
})

function marked(done: number) {
  const d = usageDashboard('unreported', 7)
  d.work.done = done
  return d
}

test('an older usage response cannot replace the selection that followed it', async ({ page }) => {
  const pageErrors: string[] = []
  page.on('pageerror', error => pageErrors.push(error.message))
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const held: { url: string; signal: AbortSignal | undefined; finish: (value: { status: number; body: unknown }) => void }[] = []
    ;(window as unknown as { __usageHeld: typeof held }).__usageHeld = held
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      if (!url.includes('/api/usage/dashboard')) return original(input, init)
      return new Promise(resolve => {
        held.push({ url, signal: init?.signal ?? undefined, finish: value => resolve(new Response(JSON.stringify(value.body), { status: value.status, headers: { 'Content-Type': 'application/json' } })) })
      })
    }
  })
  await setupUsage(page, { variant: 'unreported' })
  const heldCount = () => page.evaluate(() => (window as unknown as { __usageHeld: unknown[] }).__usageHeld.length)
  const aborted = (index: number) => page.evaluate(index => (window as unknown as { __usageHeld: { signal?: AbortSignal }[] }).__usageHeld[index]?.signal?.aborted ?? false, index)
  const finish = (index: number, status: number, body: unknown) => page.evaluate(({ index, status, body }) => {
    ;(window as unknown as { __usageHeld: { finish: (value: { status: number; body: unknown }) => void }[] }).__usageHeld[index].finish({ status, body })
  }, { index, status, body })
  const done = tile(page, 'done')
  const status = page.getByRole('status').filter({ hasText: /usage/ })

  await page.goto('/agents/usage?days=7')
  await expect.poll(heldCount).toBe(1)
  await finish(0, 200, marked(3))
  await expect(done).toContainText('3 tickets done')
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'false')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(heldCount).toBe(2)
  await expect.poll(() => aborted(0)).toBe(true)
  await expect(done).toHaveCount(0)
  await expect(status).toHaveText('Loading usage')
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'true')
  await expect(page.getByRole('button', { name: '7 days' })).toHaveAttribute('aria-pressed', 'true')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'All projects' })
  await expect.poll(heldCount).toBe(3)
  await expect.poll(() => aborted(1)).toBe(true)
  await expect(done).toContainText('3 tickets done')
  await expect(status).toHaveText('Updating usage')

  await finish(1, 200, marked(999))
  await expect(page.getByText('999 tickets done')).toHaveCount(0)
  await expect(done).toContainText('3 tickets done')
  await expect(status).toHaveText('Updating usage')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(heldCount).toBe(4)
  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'All projects' })
  await expect.poll(heldCount).toBe(5)
  await expect.poll(() => aborted(3)).toBe(true)
  await finish(3, 503, { error: 'The old project range failed' })
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(done).toContainText('3 tickets done')

  await finish(4, 503, { error: 'The selected range could not be loaded' })
  await expect(page.getByRole('alert')).toContainText('Usage could not be loaded right now.')
  await expect(done).toContainText('3 tickets done')
  await expect(status).toHaveCount(0)
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'false')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(heldCount).toBe(6)
  await page.getByRole('region', { name: 'Usage' }).getByRole('link', { name: 'Agents', exact: true }).click()
  await expect(page).toHaveURL(/\/agents$/)
  await expect.poll(() => aborted(5)).toBe(true)
  await finish(5, 200, marked(999))
  await expect(page.getByText('999 tickets done')).toHaveCount(0)
  expect(pageErrors).toEqual([])
})
