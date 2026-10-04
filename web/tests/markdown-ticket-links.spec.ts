// SPDX-License-Identifier: AGPL-3.0-only
// AEON-206: ticket keys in Markdown open the app peek; a modified click follows
// the real URL. Keys this workspace does not have stay plain text. Code and
// existing links are left alone. The epic panel does not reserve an empty band
// under the status chips.
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

const BODY = [
  '## Follow AEON-1',
  '',
  'Ship [AEON-1](https://example.com/docs) and PAI-1064 before PHAROS-14.',
  '',
  '- [ ] Confirm AEON-1',
  '',
  '`AEON-1` is code.',
  '',
  '```',
  'AEON-1',
  '```',
  '',
  'Bare https://example.com/safe?q=1.',
  '',
  'Not a link: javascript:alert(1) or https://user:pass@example.com/secret.',
].join('\n')

const shots = '../.agent-shots'

async function openDescribed(page: Page) {
  const data = fixtures()
  const ticket = data.nodes.find(node => node.key === 'PHAROS-12')!
  ticket.body = BODY
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = page.locator('.ticket-ws')
  await expect(ws.getByRole('heading', { name: /Follow AEON-1/ })).toBeVisible()
  return { calls, ws }
}

test('a known ticket key links, an unknown key stays text, and code is left alone', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const errors = watchErrors(page)
  const { calls, ws } = await openDescribed(page)
  const known = ws.getByRole('link', { name: 'AEON-1: Aeon foundation' })
  await expect(known).toHaveCount(2)
  await expect(known.first()).toHaveAttribute('href', '/p/AEON/AEON-1')
  const other = ws.getByRole('link', { name: 'PHAROS-14: Visual acceptance of the version pill' })
  await expect(other).toHaveAttribute('href', '/p/PHAROS/PHAROS-14')
  const plain = ws.locator('.ticket-plain', { hasText: 'PAI-1064' })
  await expect(plain).toHaveJSProperty('tagName', 'SPAN')
  await expect(plain).toHaveAttribute('data-tip', 'PAI-1064 is not a ticket in this AEON workspace')
  await expect(ws.locator('a[href="https://example.com/docs"]')).toHaveText('AEON-1')
  await expect(ws.locator('a[href="https://example.com/docs"]')).not.toHaveClass(/ticket-link/)
  await expect(ws.locator('pre')).toContainText('AEON-1')
  await expect(ws.locator('pre').getByRole('link')).toHaveCount(0)
  await expect(ws.locator('p > code')).toHaveText('AEON-1')
  await expect(ws.locator('p > code').getByRole('link')).toHaveCount(0)
  await expect(ws.locator('a[href="https://example.com/safe?q=1"]')).toBeVisible()
  await expect(ws.locator('a[href^="javascript:"], a[href*="user:pass"]')).toHaveCount(0)
  await expect(ws.getByRole('region', { name: 'Description' }).locator('.task-box')).toHaveCount(1)
  const asked = calls.filter(call => call.path === '/api/nodes/lookup' && call.query.has('keys'))
  expect(asked).toHaveLength(1)
  expect(asked[0].query.get('keys')!.split(',').sort()).toEqual(['AEON-1', 'PAI-1064', 'PHAROS-14'])
  expect(errors).toEqual([])
})

test('a plain click follows the routed ticket and a modified click opens the URL', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { ws } = await openDescribed(page)
  const link = ws.getByRole('link', { name: 'PHAROS-14: Visual acceptance of the version pill' })
  const [tab] = await Promise.all([page.context().waitForEvent('page'), link.click({ modifiers: ['ControlOrMeta'] })])
  await tab.waitForURL('**/p/PHAROS/PHAROS-14')
  await tab.close()
  await expect(page.locator('.ticket-peek-host')).toHaveCount(0)
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-12')

  await link.click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-14')
  await expect(page.locator('.ticket-peek-host')).toHaveCount(0)
  await expect(ws).toHaveCount(1)
  await expect(ws.getByRole('heading', { name: 'Visual acceptance of the version pill' })).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-12')
  await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
})

test('the epic panel keeps the Updated line against the status chips', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  const data = fixtures()
  await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-10')
  const ws = page.locator('.ticket-ws')
  await expect(ws.getByRole('heading', { name: 'Guarded multi-cloud provisioning' })).toBeVisible()
  await expect(ws.getByRole('button', { name: /Status:/ })).toBeVisible()
  const gap = await ws.evaluate(root => {
    const props = root.querySelector('.ws-props')
    const meta = root.querySelector('.meta')
    if (!props || !meta) return -1
    const chips = [...props.querySelectorAll('.prop-btn, .prop-static, .hours-chip')]
    const bottom = Math.max(...chips.map(el => el.getBoundingClientRect().bottom))
    return meta.getBoundingClientRect().top - bottom
  })
  expect(gap).toBeGreaterThan(0)
  expect(gap).toBeLessThan(24)
})

test('markdown ticket links and the epic panel, light and dark', async ({ page }) => {
  test.setTimeout(60_000)
  mkdirSync(shots, { recursive: true })
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  const { ws } = await openDescribed(page)
  await expect(ws.getByRole('link', { name: 'AEON-1: Aeon foundation' }).first()).toBeVisible()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await page.screenshot({ path: `${shots}/md1-links-${colorScheme}.png` })
  }
  await page.goto('/p/PHAROS/PHAROS-10')
  await expect(page.locator('.ticket-ws').getByRole('heading', { name: 'Guarded multi-cloud provisioning' })).toBeVisible()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 800 })
      await expect(page.locator('.ticket-ws .meta')).toBeVisible()
      await page.screenshot({ path: `${shots}/md1-epic-gap-${colorScheme}-${width}.png` })
    }
  }
})

for (const [oldKey, canonicalKey, projectId, projectKey] of [
  ['PHAROS-14', 'AEON-2', 'p-aeon', 'AEON'],
  ['AEON-1', 'PHAROS-17', 'p-pharos', 'PHAROS'],
]) {
  test(`a moved Markdown key ${oldKey} follows ${canonicalKey} and Back restores the source`, async ({ page }) => {
    const errors = watchErrors(page)
    const data = fixtures()
    const source = data.nodes.find(node => node.key === 'PHAROS-12')!
    source.body = `Follow ${oldKey}.`
    const moved = data.nodes.find(node => node.key === oldKey)!
    moved.key = canonicalKey
    moved.project = projectId
    moved.parent_id = projectId
    await mockWork(page, data)
    // The server's key alias resolves the old display key to its current owner.
    await page.route('**/api/nodes/lookup?*', async route => {
      const keys = new URL(route.request().url()).searchParams.get('keys')?.split(',') ?? []
      if (!keys.includes(oldKey)) return route.fallback()
      await route.fulfill({ json: { items: [{ id: moved.id, key: moved.key, title: moved.title,
        state: moved.state, requested_key: oldKey, project_id: moved.project }] } })
    })
    const sourceURL = '/p/PHAROS/PHAROS-12?q=Oracle&type=ticket'
    await page.goto(sourceURL)
    const ws = page.getByRole('complementary', { name: 'Ticket details' })
    await expect(ws.getByRole('heading', { name: source.title, exact: true })).toBeVisible()
    const link = ws.getByRole('link', { name: `${oldKey}: ${moved.title}`, exact: true })
    await expect(link).toHaveText(oldKey)
    await expect(link).toHaveAttribute('href', `/p/${projectKey}/${canonicalKey}`)
    await link.click()
    await expect(page).toHaveURL(`/p/${projectKey}/${canonicalKey}?q=Oracle&type=ticket`)
    await expect(ws).toHaveCount(1)
    await expect(ws.getByRole('button', { name: `Copy ${canonicalKey}`, exact: true })).toBeVisible()
    await expect(ws.getByRole('heading', { name: moved.title, exact: true })).toBeVisible()
    await expect(page).toHaveTitle(new RegExp(canonicalKey))
    await expect(page.locator('.ticket-peek-host')).toHaveCount(0)
    await ws.getByRole('button', { name: /Back to PHAROS-12/ }).click()
    await expect(page).toHaveURL(sourceURL)
    await expect(ws.getByRole('heading', { name: source.title, exact: true })).toBeVisible()
    await expect(ws.getByRole('link', { name: `${oldKey}: ${moved.title}`, exact: true })).toBeVisible()
    expect(errors).toEqual([])
  })
}
