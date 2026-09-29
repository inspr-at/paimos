// SPDX-License-Identifier: AGPL-3.0-only
// Method learnings (AEON-275): the Knowledge tab inbox, accept into a changelog,
// dismiss, and the read-only and agent states. Screenshots stay outside the repo.
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Browser, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, me } from './work-fixtures'
import { knowledgeWorld, mockKnowledge, type MockLearning } from './knowledge-fixtures'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-275'

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

async function open(page: Page, options: { readOnly?: boolean; agent?: boolean } = {}) {
  await mockWork(page, fixtures(), { readOnly: options.readOnly })
  if (options.agent) {
    await page.route('**/api/me', route => route.fulfill({
      json: { principal: { id: me.id, name: me.name, kind: 'agent', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } },
    }))
  }
  const world = knowledgeWorld()
  world.learnings = learnings()
  const calls = await mockKnowledge(page, world, { readOnly: options.readOnly })
  await page.goto('/p/PHAROS/knowledge')
  await expect(page.getByRole('heading', { name: 'Method learnings' })).toBeVisible()
  return { calls, world }
}

const card = (page: Page, text: string) => page.getByRole('listitem').filter({ hasText: text })

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
  await card(page, 'Write the release note in the same turn').getByRole('button', { name: 'Accept' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add to a changelog' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByLabel('Entry')).toHaveValue('k-deploy')
  await expect(dialog.locator('.preview')).toContainText('Write the release note in the same turn')
  await expect(dialog.locator('.preview')).toContainText('[PHAROS-11](/p/PHAROS/PHAROS-11)')
  await dialog.getByRole('button', { name: 'Add to changelog' }).click()
  await expect(card(page, 'Write the release note in the same turn')).toHaveCount(0)
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

  await page.getByRole('button', { name: 'Dismiss PHAROS-12' }).click()
  await expect(page.getByRole('heading', { name: 'Dismiss this learning?' })).toBeVisible()
  await page.getByRole('button', { name: 'Dismiss learning' }).click()
  await expect(card(page, 'Renumber at integration')).toHaveCount(0)
  await expect(page.getByText('Dismissed.')).toBeVisible()
  expect(errors).toEqual([])
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

test('the inbox has no axe violations', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page)
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  expect(result.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})

test('screenshots at 1600 and 390, light and dark', async ({ browser }) => {
  test.setTimeout(120_000)
  mkdirSync(shots, { recursive: true })
  const shot = async (browser: Browser, width: number, theme: 'light' | 'dark', dialog: boolean) => {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, reducedMotion: 'reduce', deviceScaleFactor: 1 })
    const page = await context.newPage()
    await open(page)
    await expect(page.getByRole('heading', { name: 'Method learnings' })).toBeVisible()
    if (dialog) {
      await card(page, 'Write the release note in the same turn').getByRole('button', { name: 'Accept' }).click()
      await expect(page.getByRole('dialog', { name: 'Add to a changelog' })).toBeVisible()
    }
    expect(await overflow(page), `${width} ${theme}${dialog ? ' dialog' : ''}`).toBeLessThanOrEqual(1)
    const name = dialog ? `dialog-${width}-${theme}.png` : `inbox-${width}-${theme}.png`
    await page.screenshot({ path: resolve(shots, name), fullPage: true, animations: 'disabled' })
    await context.close()
  }
  for (const theme of ['light', 'dark'] as const) {
    for (const width of [1600, 390]) await shot(browser, width, theme, false)
  }
  await shot(browser, 1600, 'light', true)
  await shot(browser, 390, 'dark', true)
})
