// SPDX-License-Identifier: AGPL-3.0-only
// Method learnings (AEON-275): the Knowledge tab inbox, accept into a changelog,
// dismiss, and the read-only and agent states. Screenshots stay outside the repo
// and run only with VISUAL_AUDIT=1 (optional VISUAL_AUDIT_DIR).
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Browser, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, me } from './work-fixtures'
import { knowledgeWorld, mockKnowledge, type MockLearning } from './knowledge-fixtures'

function learnings(now = Date.now()): MockLearning[] {
  return [
    {
      id: 'n-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', source: 'ticket', node_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
      key: 'PHAROS-11', title: 'Connect Hetzner Cloud', text: 'Write the release note in the same turn',
      at: new Date(now - 3_600_000).toISOString(), author: null, href: '/p/PHAROS/PHAROS-11',
    },
    {
      id: 'c-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb-88', source: 'comment', node_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
      key: 'PHAROS-12', title: 'Renumber the stages', text: 'Renumber at integration', comment_id: '88',
      at: new Date(now - 7_200_000).toISOString(), author: { id: me.id, name: me.name }, href: '/p/PHAROS/PHAROS-12',
    },
  ]
}

async function open(page: Page, options: { readOnly?: boolean; agent?: boolean; learnings?: MockLearning[]; inbox?: boolean } = {}) {
  await mockWork(page, fixtures(), { readOnly: options.readOnly })
  if (options.agent) {
    await page.route('**/api/me', route => route.fulfill({
      json: { principal: { id: me.id, name: me.name, kind: 'agent', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } },
    }))
  }
  const world = knowledgeWorld()
  world.learnings = options.learnings ?? learnings()
  const calls = await mockKnowledge(page, world, { readOnly: options.readOnly })
  await page.goto('/p/PHAROS/knowledge')
  const heading = page.getByRole('heading', { name: 'Method learnings' })
  if (options.inbox === false) await expect(heading).toHaveCount(0)
  else await expect(heading).toBeVisible()
  return { calls, world }
}

function four(now = Date.now()): MockLearning[] {
  const long = `Keep${'X'.repeat(180)}Together`
  return [
    ...learnings(now),
    {
      id: 'n-cccccccc-cccc-4ccc-8ccc-cccccccccccc', source: 'ticket', node_id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
      key: 'PHAROS-11', title: 'Same ticket', text: 'Quote the source when the checklist changes',
      at: new Date(now - 5_400_000).toISOString(), author: null, href: '/p/PHAROS/PHAROS-11',
    },
    {
      id: 'n-dddddddd-dddd-4ddd-8ddd-dddddddddddd', source: 'ticket', node_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
      key: 'PHAROS-14', title: 'Long', text: long,
      at: new Date(now - 9_000_000).toISOString(), author: null, href: '/p/PHAROS/PHAROS-14',
    },
  ]
}

const rows = (page: Page) => page.locator('section.learnings').getByRole('listitem')
const card = (page: Page, text: string) => rows(page).filter({ hasText: text })

async function overflow(page: Page) {
  return page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
}

test('a person accepts a learning into the changelog and can dismiss another', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = watchErrors(page)
  const { calls, world } = await open(page)
  await expect(card(page, 'Write the release note in the same turn')).toBeVisible()
  await expect(card(page, 'Renumber at integration')).toBeVisible()
  await expect(page.getByText('Waiting for a person to accept.')).toHaveCount(0)

  const before = world.entries.find(entry => entry.id === 'k-deploy')?.updated_at
  await card(page, 'Write the release note in the same turn').getByRole('button', { name: /^Accept PHAROS-11:/ }).click()
  const dialog = page.getByRole('dialog', { name: 'Add to a changelog' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByLabel('Entry')).toHaveValue('k-deploy')
  await expect(dialog.getByText('Write the release note in the same turn')).toHaveCount(1)
  await expect(dialog.locator('.preview span').first()).toHaveText(/^\d{4}-\d{2}-\d{2}: Write the release note in the same turn\.$/)
  await expect(dialog.getByText('Adds this line')).toHaveCount(0)
  const source = dialog.locator('.preview a')
  await expect(source).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await expect(source).toHaveText('PHAROS-11')
  await expect(dialog.locator('.preview')).not.toContainText('[PHAROS-11]')
  await dialog.getByRole('button', { name: 'Add to changelog' }).click()
  await expect(card(page, 'Write the release note in the same turn')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Accept PHAROS-12:/ })).toBeFocused()
  await expect(page.getByText('Added to the changelog.')).toBeVisible()
  const post = calls.find(call => call.method === 'POST' && call.path.endsWith('/accept'))
  expect(post?.body).toEqual({ knowledge_id: 'k-deploy' })
  expect(post?.headers['if-unmodified-since']).toBe(before)
  const deploy = world.entries.find(entry => entry.id === 'k-deploy')
  expect(deploy?.body).toContain('Write the release note in the same turn')
  expect(deploy?.body).toContain('[PHAROS-11](/p/PHAROS/PHAROS-11)')

  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(card(page, 'Write the release note in the same turn')).toBeVisible()
  expect(world.entries.find(entry => entry.id === 'k-deploy')?.body).not.toContain('Write the release note in the same turn')

  await page.getByRole('button', { name: /^Dismiss PHAROS-12:/ }).click()
  await expect(page.getByRole('heading', { name: 'Dismiss this learning?' })).toBeVisible()
  await page.getByRole('button', { name: 'Dismiss learning' }).click()
  await expect(card(page, 'Renumber at integration')).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Method learnings' })).toBeFocused()
  await expect(page.getByText('Dismissed.')).toBeVisible()
  expect(errors).toEqual([])
})

test('removing the last learning keeps focus on the search', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page, { learnings: learnings().slice(0, 1) })
  await page.getByRole('button', { name: /^Dismiss PHAROS-11:/ }).click()
  await page.getByRole('button', { name: 'Dismiss learning' }).click()
  await expect(page.getByRole('heading', { name: 'Method learnings' })).toHaveCount(0)
  await expect(page.getByRole('searchbox', { name: 'Search knowledge in Pharos' })).toBeFocused()

  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(card(page, 'Write the release note in the same turn')).toBeVisible()
  await card(page, 'Write the release note in the same turn').getByRole('button', { name: /^Accept PHAROS-11:/ }).click()
  await page.getByRole('button', { name: 'Add to changelog' }).click()
  await expect(page.getByRole('heading', { name: 'Method learnings' })).toHaveCount(0)
  await expect(page.getByRole('searchbox', { name: 'Search knowledge in Pharos' })).toBeFocused()
})

test('viewers and agents see the inbox without a way to decide', async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 800 })
  await open(page, { readOnly: true })
  await expect(page.getByRole('button', { name: 'Accept' })).toHaveCount(0)
  await expect(page.getByText('Waiting for a person to accept.')).toBeVisible()

  await open(page, { agent: true })
  await expect(page.getByRole('button', { name: 'Accept' })).toHaveCount(0)
  await expect(page.getByText('Waiting for a person to accept.')).toBeVisible()
})

test('the inbox shows three rows, names each action, and keeps focus', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page, { learnings: four() })
  await expect(rows(page)).toHaveCount(3)
  await expect(page.getByRole('button', { name: 'Show all 4' })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Accept PHAROS-11: Write the release note/ })).toHaveCount(1)
  await expect(page.getByRole('button', { name: /^Accept PHAROS-11: Quote the source/ })).toHaveCount(1)
  const row = card(page, 'Write the release note in the same turn')
  const rowBox = await row.boundingBox()
  const buttons = row.locator('.actions button')
  expect(await buttons.count()).toBe(2)
  for (let i = 0; i < 2; i++) {
    const box = await buttons.nth(i).boundingBox()
    expect(box && rowBox && box.width < rowBox.width * 0.55).toBe(true)
  }
  await page.getByRole('button', { name: 'Show all 4' }).click()
  await expect(rows(page)).toHaveCount(4)
  const line = card(page, 'KeepX').locator('.line')
  await expect(line).toHaveCSS('overflow-wrap', 'anywhere')
  expect(await overflow(page)).toBeLessThanOrEqual(1)
  await page.getByRole('button', { name: 'Show fewer' }).click()
  await expect(rows(page)).toHaveCount(3)

  await page.getByRole('button', { name: 'Show all 4' }).click()
  await page.getByRole('button', { name: /^Dismiss PHAROS-14:/ }).click()
  await page.getByRole('button', { name: 'Dismiss learning' }).click()
  await expect(page.getByRole('heading', { name: 'Method learnings' })).toBeFocused()
  await page.getByRole('button', { name: /^Dismiss PHAROS-11: Write/ }).click()
  await page.getByRole('button', { name: 'Dismiss learning' }).click()
  await expect(page.getByRole('button', { name: /^Accept PHAROS-12:/ })).toBeFocused()
})

test('an empty inbox leaves the knowledge tab in place', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page, { learnings: [], inbox: false })
  await expect(page.getByRole('searchbox', { name: 'Search knowledge in Pharos' })).toBeVisible()
  expect(await overflow(page)).toBeLessThanOrEqual(1)
})

test('the inbox has no axe violations', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page)
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  expect(result.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})

test('screenshots at 1600 and 390, light and dark', async ({ browser }) => {
  test.skip(process.env.VISUAL_AUDIT !== '1', 'Screenshots run only with VISUAL_AUDIT=1.')
  test.setTimeout(240_000)
  const shots = resolve(process.env.VISUAL_AUDIT_DIR ?? 'test-results/knowledge-learnings')
  mkdirSync(shots, { recursive: true })
  const shot = async (browser: Browser, width: number, theme: 'light' | 'dark', mode: 'inbox' | 'dialog' | 'empty' | 'long') => {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, reducedMotion: 'reduce', deviceScaleFactor: 1 })
    const page = await context.newPage()
    if (mode === 'empty') await open(page, { learnings: [], inbox: false })
    else if (mode === 'long') {
      await open(page, { learnings: four() })
      await page.getByRole('button', { name: 'Show all 4' }).click()
    } else {
      await open(page)
      if (mode === 'dialog') {
        await card(page, 'Write the release note in the same turn').getByRole('button', { name: /^Accept PHAROS-11:/ }).click()
        await expect(page.getByRole('dialog', { name: 'Add to a changelog' })).toBeVisible()
      }
    }
    expect(await overflow(page), `${width} ${theme} ${mode}`).toBeLessThanOrEqual(1)
    await page.screenshot({ path: resolve(shots, `${mode}-${width}-${theme}.png`), fullPage: true, animations: 'disabled' })
    await context.close()
  }
  for (const theme of ['light', 'dark'] as const) {
    for (const width of [1600, 390]) {
      for (const mode of ['inbox', 'empty', 'long'] as const) await shot(browser, width, theme, mode)
    }
  }
  await shot(browser, 1600, 'light', 'dialog')
  await shot(browser, 390, 'dark', 'dialog')
})
