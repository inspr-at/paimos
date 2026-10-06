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
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'

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

async function open(page: Page, options: { readOnly?: boolean; agent?: boolean; learnings?: MockLearning[]; inbox?: boolean; rules?: string[]; emptyScans?: number } = {}) {
  await mockWork(page, fixtures(), { readOnly: options.readOnly })
  if (options.agent) {
    await page.route('**/api/me', route => route.fulfill({
      json: { principal: { id: me.id, name: me.name, kind: 'agent', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } },
    }))
  }
  const world = knowledgeWorld()
  world.learnings = options.learnings ?? learnings()
  world.emptyScans = options.emptyScans ?? 0
  const calls = await mockKnowledge(page, world, { readOnly: options.readOnly })
  const extra = options.rules
  if (extra) {
    // The member fixture has no rules permissions; a test names the ones it needs.
    await page.route('**/api/me/permissions*', route => {
      const found = mockEffectivePermissions('member', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
      found.workspace.permissions = [...found.workspace.permissions, ...extra]
      if (found.project) found.project.permissions = [...found.project.permissions, ...extra]
      return route.fulfill({ json: found })
    })
  }
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

// AEON-788: an agent's prepared decisions on five learnings.
function recommended(now = Date.now()): MockLearning[] {
  const scout = { id: '33333333-3333-4333-8333-333333333333', name: 'Scout' }
  const at = new Date(now - 600_000).toISOString()
  const rec = (extra: Partial<NonNullable<MockLearning['recommendation']>> & { decision: 'accept' | 'dismiss' }) => ({ by: scout, at, event_id: 700, stale: false, target_missing: false, ...extra })
  const row = (n: number, text: string, recommendation: MockLearning['recommendation']): MockLearning => ({
    id: `n-${String(n).repeat(8)}-aaaa-4aaa-8aaa-aaaaaaaaaaaa`, source: 'ticket', node_id: `${String(n).repeat(8)}-aaaa-4aaa-8aaa-aaaaaaaaaaaa`,
    key: `PHAROS-${20 + n}`, title: text, text, at: new Date(now - n * 3_600_000).toISOString(), author: null, href: `/p/PHAROS/PHAROS-${20 + n}`, recommendation,
  })
  return [
    row(1, 'Release notes were late again', rec({ decision: 'accept', knowledge_id: 'k-deploy', knowledge_title: 'Deploy a release to production', lesson: 'Write the release note in the same turn as the change' })),
    row(2, 'Renumbered stages twice', rec({ decision: 'dismiss', reason: 'Already covered by the stage runbook' })),
    row(3, 'Host keys expired during a deploy', rec({ decision: 'accept', knowledge_id: 'k-rotate', knowledge_title: 'Rotate the fleet host keys', lesson: 'Rotate keys before the window closes', stale: true })),
    row(4, 'A one-off typo in a heading', rec({ decision: 'dismiss', reason: 'Not a method learning' })),
    row(5, 'Retries hid a refused login', rec({ decision: 'accept', knowledge_id: 'k-vanished', knowledge_title: 'Retired runbook', lesson: 'Never retry a refusal' })),
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
  expect(post?.body).toEqual({ knowledge_id: 'k-deploy', learning_text: 'Write the release note in the same turn' })
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

const ids = {
  projectLayer: '11111111-1111-4111-8111-111111111111',
  foreignLayer: '22222222-2222-4222-8222-222222222222',
  personLayer: '33333333-3333-4333-8333-333333333333',
  companyLayer: '44444444-4444-4444-8444-444444444444',
  roleLayer: '55555555-5555-4555-8555-555555555555',
  namedLayer: '66666666-6666-4666-8666-666666666666',
  namedAgent: '77777777-7777-4777-8777-777777777777',
  foreignProject: '99999999-9999-4999-8999-999999999999',
  projectSet: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1',
  personSet: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2',
  companySet: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3',
  roleSet: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4',
}
const setFor: Record<string, { id: string; name: string }> = {
  [ids.projectLayer]: { id: ids.projectSet, name: 'Safety' },
  [ids.personLayer]: { id: ids.personSet, name: 'Personal' },
  [ids.companyLayer]: { id: ids.companySet, name: 'Floor' },
  [ids.roleLayer]: { id: ids.roleSet, name: 'Builder rules' },
}

async function mockRuleLayers(page: Page) {
  await page.route('**/api/rules/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/rules/layers')) {
      await route.fulfill({ json: { layers: [
        { id: ids.foreignLayer, scope: { layer: 'project', project_id: ids.foreignProject } },
        { id: ids.projectLayer, scope: { layer: 'project', project_id: 'p-pharos' } },
        { id: ids.namedLayer, scope: { layer: 'agent', owner_id: me.id, agent_id: ids.namedAgent } },
        { id: ids.personLayer, scope: { layer: 'person', owner_id: me.id } },
        { id: ids.companyLayer, scope: { layer: 'company' } },
        { id: ids.roleLayer, scope: { layer: 'agent', role: 'builder' } },
      ] } })
      return
    }
    if (url.pathname.endsWith('/rules/sets')) {
      const found = setFor[url.searchParams.get('layer_id') ?? '']
      await route.fulfill({ json: { sets: found ? [{ id: found.id, layer_id: url.searchParams.get('layer_id'), scope: { layer: 'company' }, name: found.name, revision: 1, rules: [], published_version: '' }] : [] } })
      return
    }
    await route.fulfill({ status: 404, json: { error: 'missing' } })
  })
}

test('a person can save a learning as a rule draft', async ({ page }) => {
  for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const { calls } = await open(page, { rules: ['rules.write', 'rules.publish'] })
    await mockRuleLayers(page)
    await card(page, 'Write the release note in the same turn').getByRole('button', { name: /^Accept PHAROS-11:/ }).click()
    const changelog = page.getByRole('dialog', { name: 'Add to a changelog' })
    await expect(changelog.getByRole('button', { name: 'Add to changelog' })).toBeVisible()
    await expect(changelog.locator('.btn.primary')).toHaveCount(1)
    await changelog.getByRole('button', { name: 'Add to changelog' }).click()
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/accept')).at(-1)?.body).toEqual({ knowledge_id: 'k-deploy', learning_text: 'Write the release note in the same turn' })

    await card(page, 'Renumber at integration').getByRole('button', { name: /^Accept PHAROS-12:/ }).click()
    await page.getByRole('dialog', { name: 'Add to a changelog' }).getByRole('radio', { name: 'Rule draft' }).click()
    const dialog = page.getByRole('dialog', { name: 'Save a rule draft' })
    await expect(dialog.getByLabel('Entry')).toHaveCount(0)
    const layer = dialog.getByLabel('Layer')
    await expect(layer.locator('option')).toHaveText(['Pharos', 'Your rules', 'Company', 'Agent role · Builder'])
    expect((await layer.locator('option').allTextContents()).join('\n')).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}-/)
    await expect(dialog.getByText(ids.foreignProject)).toHaveCount(0)
    await expect(dialog.getByText(ids.namedAgent)).toHaveCount(0)
    await layer.selectOption({ label: 'Pharos' })
    await dialog.getByLabel('Rule set').selectOption({ label: 'Safety' })
    await expect(dialog.getByText('Renumber at integration')).toBeVisible()
    await expect(dialog.getByText('Publishing stays a separate approval.')).toBeVisible()
    await expect(dialog.locator('.btn.primary')).toHaveCount(1)
    expect(await overflow(page)).toBeLessThanOrEqual(1)
    await dialog.getByRole('button', { name: 'Save rule draft' }).click()
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/draft')).at(-1)?.body).toEqual({ layer_id: ids.projectLayer, set_id: ids.projectSet })
    await expect(page.getByText('Saved as a rule draft.')).toBeVisible()
  }
})

test('the draft dialog offers only layers the person can write', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page, { rules: ['rules.write'] })
  await mockRuleLayers(page)
  await card(page, 'Renumber at integration').getByRole('button', { name: /^Accept PHAROS-12:/ }).click()
  await page.getByRole('dialog', { name: 'Add to a changelog' }).getByRole('radio', { name: 'Rule draft' }).click()
  const dialog = page.getByRole('dialog', { name: 'Save a rule draft' })
  // Company, project and role sets need rules.publish, as on the server.
  await expect(dialog.getByLabel('Layer').locator('option')).toHaveText(['Your rules'])
})

const suspectText = `Sign in with password=${'x'.repeat(5)} on staging`
function suspected(now = Date.now()): MockLearning[] {
  const text = suspectText
  const start = text.indexOf('x')
  return [
    {
      id: 'c-eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee-90', source: 'comment', node_id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
      key: 'PHAROS-15', title: 'Staging login', text, comment_id: '90',
      at: new Date(now - 3_600_000).toISOString(), author: { id: me.id, name: me.name }, href: '/p/PHAROS/PHAROS-15',
      sensitive: [{ field: 'text', start, end: start + 5 }],
    },
    {
      id: 'c-ffffffff-ffff-4fff-8fff-ffffffffffff-91', source: 'comment', node_id: 'ffffffff-ffff-4fff-8fff-ffffffffffff',
      key: 'PHAROS-16', title: 'Staging token', text, comment_id: '91',
      at: new Date(now - 7_200_000).toISOString(), author: null, href: '/p/PHAROS/PHAROS-16',
      sensitive: [{ field: 'text', start, end: start + 5 }],
    },
  ]
}

test('a suspected credential needs a person to confirm it is not one', async ({ page }) => {
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    const errors = watchErrors(page)
    const { calls } = await open(page, { learnings: suspected(), rules: ['rules.write', 'rules.publish'] })
    await mockRuleLayers(page)

    await card(page, 'Sign in with').first().getByRole('button', { name: /^Accept PHAROS-15:/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Add to a changelog' })
    await dialog.getByRole('button', { name: 'Add to changelog' }).click()
    const note = dialog.getByRole('alert')
    await expect(note).toContainText('This looks like a credential — remove it, or confirm it is not one.')
    await expect(note.getByRole('link', { name: 'remove it' })).toHaveAttribute('href', '/p/PHAROS/PHAROS-15')
    await expect(dialog.locator('mark.suspect')).toHaveText('xxxxx')
    const add = dialog.getByRole('button', { name: 'Add to changelog' })
    await expect(add).toBeDisabled()
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/accept')).at(-1)?.body).toEqual({ knowledge_id: 'k-deploy', learning_text: suspectText })
    expect(await overflow(page)).toBeLessThanOrEqual(1)
    if (width === 1280) {
      const result = await new AxeBuilder({ page }).include('dialog').withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      expect(result.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
    }
    await note.getByRole('checkbox', { name: 'It is not a credential' }).check()
    await add.click()
    await expect(page.getByText('Added to the changelog.')).toBeVisible()
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/accept')).at(-1)?.body).toEqual({ knowledge_id: 'k-deploy', learning_text: suspectText, confirm_not_sensitive: true })

    await card(page, 'Sign in with').getByRole('button', { name: /^Accept PHAROS-16:/ }).click()
    await page.getByRole('dialog', { name: 'Add to a changelog' }).getByRole('radio', { name: 'Rule draft' }).click()
    const draft = page.getByRole('dialog', { name: 'Save a rule draft' })
    await expect(draft.getByRole('alert')).toHaveCount(0)
    await draft.getByLabel('Layer').selectOption({ label: 'Pharos' })
    await draft.getByRole('button', { name: 'Save rule draft' }).click()
    await expect(draft.getByRole('alert')).toContainText('This looks like a credential')
    await expect(draft.getByRole('button', { name: 'Save rule draft' })).toBeDisabled()
    await draft.getByRole('checkbox', { name: 'It is not a credential' }).check()
    await draft.getByRole('button', { name: 'Save rule draft' }).click()
    await expect(page.getByText('Saved as a rule draft.')).toBeVisible()
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/draft')).at(-1)?.body).toEqual({ layer_id: ids.projectLayer, set_id: ids.projectSet, confirm_not_sensitive: true })
    expect(errors).toEqual([])
  }
})

test('the inbox has no axe violations', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page)
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  expect(result.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})

test('a person applies agent recommendations in one confirm, with one row left out', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = watchErrors(page)
  const { calls, world } = await open(page, { learnings: recommended() })
  await expect(card(page, 'Release notes were late again')).toContainText('Scout recommends adding to Deploy a release to production: “Write the release note in the same turn as the change”')
  await expect(card(page, 'Renumbered stages twice')).toContainText('Scout recommends dismissing: Already covered by the stage runbook')
  await page.getByRole('button', { name: 'Apply recommendations' }).click()
  const dialog = page.getByRole('dialog', { name: 'Apply recommendations' })
  await expect(dialog).toBeVisible()
  // Stale rows start left out; the rest are chosen.
  await expect(dialog.getByText('2 to accept · 2 to dismiss · 1 left out')).toBeVisible()
  const tick = (text: string) => dialog.getByRole('checkbox', { name: new RegExp(text) })
  await expect(tick('Rotate keys before the window closes')).not.toBeChecked()
  await expect(dialog.getByText('The learning changed after this was recommended.')).toBeVisible()

  const guard = await controlStability(page, {
    close: dialog.getByRole('button', { name: 'Close' }),
    apply: dialog.getByRole('button', { name: 'Apply chosen' }),
    firstRow: tick('Write the release note in the same turn'),
    optOut: tick('A one-off typo in a heading'),
  })
  await guard.check(() => tick('A one-off typo in a heading').uncheck())
  await expect(dialog.getByText('2 to accept · 1 to dismiss · 2 left out')).toBeVisible()
  await guard.check(() => tick('A one-off typo in a heading').check())
  await guard.check(() => tick('A one-off typo in a heading').uncheck())
  // Two rows apply and one fails: the controls and every row stay put.
  await guard.check(async () => {
    await dialog.getByRole('button', { name: 'Apply chosen' }).click()
    await expect(dialog.getByRole('status')).toHaveText('2 applied, 1 failed. The failed learnings stay open.')
  })
  guard.done()

  await expect(dialog.locator('[data-step-id]').filter({ hasText: 'Never retry a refusal' })).toContainText('This entry no longer exists.')
  await expect(dialog.locator('[data-step-id]').filter({ hasText: 'Never retry a refusal' }).locator('[data-result]')).toHaveAttribute('data-result', 'failed')
  await expect(dialog.locator('[data-step-id]').filter({ hasText: 'Write the release note in the same turn' }).locator('[data-result]')).toHaveAttribute('data-result', 'applied')
  await expect(dialog.getByRole('region', { name: /Not applied/ })).toContainText('PHAROS-25This entry no longer exists.')
  const posts = calls.filter(call => call.method === 'POST' && call.path.includes('/learnings/'))
  expect(posts.map(call => call.path.split('/').at(-1))).toEqual(['accept', 'accept', 'dismiss'])
  expect(posts[0].body).toEqual({ knowledge_id: 'k-deploy', lesson: 'Write the release note in the same turn as the change', learning_text: 'Release notes were late again' })
  expect(posts[2].body).toEqual({ reason: 'Already covered by the stage runbook', learning_text: 'Renumbered stages twice' })
  expect(world.decisions.map(decision => decision.item.text).sort()).toEqual(['Release notes were late again', 'Renumbered stages twice'])
  expect(world.entries.find(entry => entry.id === 'k-deploy')?.body).toContain('Write the release note in the same turn as the change. Source: [PHAROS-21]')

  await dialog.getByRole('button', { name: 'Close' }).click()
  await expect(dialog).toBeHidden()
  await expect(card(page, 'Release notes were late again')).toHaveCount(0)
  await expect(card(page, 'Renumbered stages twice')).toHaveCount(0)
  await expect(card(page, 'A one-off typo in a heading')).toBeVisible()
  await expect(card(page, 'Retries hid a refused login')).toBeVisible()
  expect(errors).toEqual([])
})

test('older learnings load after the newest 50', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const now = Date.now()
  const many: MockLearning[] = Array.from({ length: 55 }, (_, i) => ({
    id: `n-${String(i).padStart(8, '0')}-aaaa-4aaa-8aaa-aaaaaaaaaaaa`, source: 'ticket', node_id: `${String(i).padStart(8, '0')}-aaaa-4aaa-8aaa-aaaaaaaaaaaa`,
    key: `PHAROS-${100 + i}`, title: `Learning ${i}`, text: `Learning number ${i}`, at: new Date(now - i * 60_000).toISOString(), author: null, href: `/p/PHAROS/PHAROS-${100 + i}`,
  }))
  const { calls } = await open(page, { learnings: many })
  await expect(page.getByText('Showing the 50 newest. Load older from the top.')).toBeVisible()
  const older = page.getByRole('button', { name: 'Load older' })
  await older.click()
  await expect(older).toHaveCount(0)
  await expect(rows(page)).toHaveCount(55)
  await expect(card(page, 'Learning number 54')).toBeVisible()
  expect(calls.some(call => call.method === 'GET' && call.query.get('cursor') === '50')).toBe(true)
})

test('pages that scanned no open learning still lead to older ones', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = watchErrors(page)
  const { calls } = await open(page, { emptyScans: 2 })
  const older = page.getByRole('button', { name: 'Load older' })
  await expect(page.getByText('None open among the newest. Load older from the top.')).toBeVisible()
  await expect(rows(page)).toHaveCount(0)
  await older.click()
  await expect.poll(() => calls.filter(call => call.method === 'GET' && call.query.get('cursor') === 'scan-1').length).toBe(1)
  await expect(older).toBeEnabled()
  await expect(rows(page)).toHaveCount(0)
  await older.click()
  await expect(card(page, 'Write the release note in the same turn')).toBeVisible()
  await expect(card(page, 'Renumber at integration')).toBeVisible()
  await expect(older).toHaveCount(0)
  expect(calls.some(call => call.method === 'GET' && call.query.get('cursor') === 'scan-2')).toBe(true)
  expect(errors).toEqual([])
})

test('on a phone the review sheet keeps its actions pinned through apply', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const errors = watchErrors(page)
  await open(page, { learnings: recommended() })
  await page.getByRole('button', { name: 'Apply recommendations' }).click()
  const dialog = page.getByRole('dialog', { name: 'Apply recommendations' })
  const tick = (text: string) => dialog.getByRole('checkbox', { name: new RegExp(text) })
  const guard = await controlStability(page, {
    frame: dialog.locator('.review-card'),
    close: dialog.getByRole('button', { name: 'Close' }),
    apply: dialog.getByRole('button', { name: 'Apply chosen' }),
    firstRow: tick('Write the release note in the same turn'),
  })
  await guard.check(() => tick('A one-off typo in a heading').uncheck())
  await guard.check(async () => {
    await dialog.getByRole('button', { name: 'Apply chosen' }).click()
    await expect(dialog.getByRole('status')).toHaveText('2 applied, 1 failed. The failed learnings stay open.')
  })
  guard.done()
  expect(errors).toEqual([])
})

test('a learning that changed after review is refused and shown again', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls, world } = await open(page, { learnings: recommended() })
  await page.getByRole('button', { name: 'Apply recommendations' }).click()
  const dialog = page.getByRole('dialog', { name: 'Apply recommendations' })
  await expect(dialog).toBeVisible()
  // Someone edits a learning while the person reviews it.
  const edited = world.learnings.find(item => item.text === 'Renumbered stages twice')!
  edited.text = edited.title = 'Renumbered stages three times'
  await dialog.getByRole('button', { name: 'Apply chosen' }).click()
  await expect(dialog.getByRole('status')).toHaveText('2 applied, 2 failed. The failed learnings stay open.')
  await expect(dialog.getByRole('region', { name: /Not applied/ })).toContainText('PHAROS-22This learning changed since you reviewed it. Review it again.')
  expect(world.decisions.map(decision => decision.item.key)).toEqual(['PHAROS-21', 'PHAROS-24'])
  const lists = calls.filter(call => call.method === 'GET' && call.path.endsWith('/learnings')).length
  await dialog.getByRole('button', { name: 'Close' }).click()
  // The inbox reloads and shows the current text for a new review.
  await expect(card(page, 'Renumbered stages three times')).toBeVisible()
  expect(calls.filter(call => call.method === 'GET' && call.path.endsWith('/learnings')).length).toBe(lists + 1)
})

test('leaving the knowledge view during a run sends no further step', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = watchErrors(page)
  const { calls, world } = await open(page, { learnings: recommended() })
  // Hold the first write until the view is gone.
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  let first = true
  await page.route('**/api/knowledge/learnings/*/accept', async route => {
    if (first) { first = false; await held }
    await route.fallback()
  })
  // Arrive in-app from Tickets, so Back stays inside the app.
  const sections = page.getByRole('tablist', { name: 'Project sections' })
  await sections.getByRole('tab', { name: 'Tickets' }).click()
  await expect(page.locator('section.learnings')).toHaveCount(0)
  await sections.getByRole('tab', { name: 'Knowledge' }).click()
  await expect(page.locator('section.learnings')).toBeVisible()
  await page.getByRole('button', { name: 'Apply recommendations' }).click()
  const dialog = page.getByRole('dialog', { name: 'Apply recommendations' })
  await dialog.getByRole('button', { name: 'Apply chosen' }).click()
  await expect(dialog.getByRole('status')).toHaveText('Applying 1 of 4…')
  // The person presses Back; the inbox unmounts with its dialog.
  await page.goBack()
  await expect(page.locator('section.learnings')).toHaveCount(0)
  const answered = page.waitForResponse(response => response.url().includes('/accept') && response.request().method() === 'POST')
  release()
  await answered
  // Coming back loads the inbox again; a step sent after the first would be
  // recorded before this list request.
  const listed = calls.length
  await sections.getByRole('tab', { name: 'Knowledge' }).click()
  await expect(page.locator('section.learnings')).toBeVisible()
  const after = calls.slice(listed)
  expect(after.some(call => call.method === 'GET' && call.path.endsWith('/learnings'))).toBe(true)
  const writes = calls.filter(call => call.method === 'POST' && call.path.includes('/learnings/'))
  expect(writes).toHaveLength(1)
  expect(world.decisions).toHaveLength(1)
  expect(errors).toEqual([])
})

test('screenshots at 1600 and 390, light and dark', async ({ browser }) => {
  test.skip(process.env.VISUAL_AUDIT !== '1', 'Screenshots run only with VISUAL_AUDIT=1.')
  test.setTimeout(240_000)
  const shots = resolve(process.env.VISUAL_AUDIT_DIR ?? 'test-results/knowledge-learnings')
  mkdirSync(shots, { recursive: true })
  const shot = async (browser: Browser, width: number, theme: 'light' | 'dark', mode: 'inbox' | 'dialog' | 'empty' | 'long' | 'suspect' | 'recommended' | 'review') => {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, reducedMotion: 'reduce', deviceScaleFactor: 1 })
    const page = await context.newPage()
    if (mode === 'empty') await open(page, { learnings: [], inbox: false })
    else if (mode === 'recommended' || mode === 'review') {
      await open(page, { learnings: recommended() })
      await page.getByRole('button', { name: /^Show all/ }).click()
      if (mode === 'review') {
        await page.getByRole('button', { name: 'Apply recommendations' }).click()
        await expect(page.getByRole('dialog', { name: 'Apply recommendations' })).toBeVisible()
      }
    }
    else if (mode === 'long') {
      await open(page, { learnings: four() })
      await page.getByRole('button', { name: 'Show all 4' }).click()
    } else if (mode === 'suspect') {
      await open(page, { learnings: suspected() })
      await card(page, 'Sign in with').first().getByRole('button', { name: /^Accept PHAROS-15:/ }).click()
      await page.getByRole('dialog', { name: 'Add to a changelog' }).getByRole('button', { name: 'Add to changelog' }).click()
      await expect(page.getByRole('dialog', { name: 'Add to a changelog' }).getByRole('alert')).toBeVisible()
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
  for (const theme of ['light', 'dark'] as const) {
    for (const width of [1600, 390]) await shot(browser, width, theme, 'suspect')
    for (const width of [1440, 1024, 390]) {
      await shot(browser, width, theme, 'recommended')
      await shot(browser, width, theme, 'review')
    }
  }
})
