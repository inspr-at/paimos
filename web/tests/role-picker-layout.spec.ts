// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, COORDINATOR } from './access-fixtures'
import { expectStableControls } from './helpers/stable'
import type { FloatingList } from './helpers/floating-lists'

async function openRoles(page: Page, extra = 3) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.agents.find(agent => agent.principal_id === COORDINATOR)!.name = 'workstation-agents'
  const viewer = world.roles.find(role => role.key === 'viewer')!
  for (let index = 0; index < extra; index++) world.roles.push({ ...viewer, id: `runtime-${index}`, key: `paired_${index}`, name: 'Paired computer runtime', description: '', builtin: false })
  for (const [index, name] of ['Studio workstation', 'Studio laptop', 'Build computer'].entries()) {
    if (index >= extra) break
    world.agents.push({ principal_id: `paired-principal-${index}`, name, workspace_role: `runtime-${index}`, last_seen_at: null, service: false, paired_computer: true, connected_computer: true })
  }
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  const row = page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'workstation-agents' })
  await row.getByRole('button', { name: /^Role of workstation-agents:/ }).click()
  const picker = page.getByRole('dialog', { name: 'Role of workstation-agents', exact: true })
  await expect(picker).toBeVisible()
  return { world, picker }
}

async function insideViewport(locator: Locator, page: Page) {
  const bounds = (await locator.boundingBox())!
  const viewport = page.viewportSize()!
  expect(bounds.width).toBeGreaterThan(0)
  expect(bounds.height).toBeGreaterThan(0)
  expect(bounds.x).toBeGreaterThanOrEqual(0)
  expect(bounds.y).toBeGreaterThanOrEqual(0)
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width + .5)
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(viewport.height + .5)
}

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
  test(`role choices use the screen and stay still at ${width} ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const errors = watchErrors(page)
    const { world, picker } = await openRoles(page)
    const body = picker.locator('.picker-body'), options = picker.getByRole('radiogroup')
    const preview = picker.getByRole('region', { name: 'What changes' })
    const admin = options.getByRole('radio', { name: /^Admin/ })
    const member = options.getByRole('radio', { name: /^Member/ })
    const apply = picker.locator('.actions .primary')
    await expect(member).toBeFocused()
    await insideViewport(picker, page)
    const frame = (await picker.boundingBox())!
    if (width === 390) { expect(frame.width).toBe(390); expect(frame.height).toBe(844) }
    else { expect(frame.width).toBeGreaterThan(width * .6); expect(frame.width).toBeLessThanOrEqual(960) }
    // The complete list is laid out, with no independent 262px clip box.
    expect(await options.evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
    const rowHeights = await options.getByRole('radio').evaluateAll(rows => rows.map(row => row.getBoundingClientRect().height))
    expect(new Set(rowHeights)).toEqual(new Set([60]))
    await expect(options.getByRole('radio', { name: /^Paired computer runtime/ })).toHaveCount(3)
    await expect(options.getByRole('radio', { name: /^Paired computer runtime/ }).locator('.desc')).toHaveText(['Studio workstation', 'Studio laptop', 'Build computer'])
    const scrolling = await body.evaluate(el => el.scrollHeight > el.clientHeight + 1)
    await expectStableControls({
      controls: { ...(width === 390 || scrolling ? { frame: picker } : {}), options, admin, member, row: admin, preview, actions: picker.locator('.actions'), apply, cancel: picker.getByRole('button', { name: 'Cancel', exact: true }) },
      scrollAreas: { picker, body, options, preview },
      interactions: [admin, member, admin].map((role, index) => ({ name: `pick role ${index + 1}`, run: async () => {
        const scrollBefore = await body.evaluate(el => el.scrollTop)
        await role.click()
        await expect(role).toHaveAttribute('aria-checked', 'true')
        expect(await body.evaluate(el => el.scrollTop), 'pointer selection leaves scrolling alone').toBe(scrollBefore)
      } })),
    })
    await expect(apply).toHaveAccessibleName('Give workstation-agents Admin')
    await insideViewport(apply, page)
    const shots = join(process.cwd(), 'test-results', 'aeon-624')
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `aeon-624-role-picker-${width}-${theme}.png`) })
    await picker.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect(world.calls.filter(call => call.method === 'PUT' && call.path.endsWith('/workspace-role'))).toHaveLength(0)
    expect(errors).toEqual([])
  })
}

test('long role lists and short viewports keep actions visible and keyboard rows reachable', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 480 })
  const { picker } = await openRoles(page, 30)
  const body = picker.locator('.picker-body'), options = picker.getByRole('radiogroup')
  const apply = picker.locator('.actions .primary')
  expect(await body.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(100)
  await insideViewport(apply, page)
  const row = options.getByRole('radio', { name: /^Member/ })
  await expectStableControls({
    controls: { frame: picker, options, row, apply, actions: picker.locator('.actions') },
    scrollAreas: { body, options },
    interactions: [{ name: 'keyboard reveal another role', run: async () => {
      await row.press('ArrowDown')
      await expect(options.getByRole('radio', { name: /^Viewer/ })).toHaveAttribute('aria-checked', 'true')
    } }, { name: 'scroll to the last role', run: async () => { await options.getByRole('radio').last().scrollIntoViewIfNeeded() } }],
  })
  await insideViewport(apply, page)
  const last = options.getByRole('radio').last()
  await expectStableControls({
    controls: { frame: picker, options, row: last, apply, actions: picker.locator('.actions'), preview: picker.getByRole('region', { name: 'What changes' }) },
    scrollAreas: { body, options },
    interactions: [{ name: 'pick the last role', run: async () => {
      await last.click()
      await expect(last).toHaveAttribute('aria-checked', 'true')
    } }],
  })
  await insideViewport(apply, page)
  await page.keyboard.press('Escape')
  await expect(picker).toHaveCount(0)
})

test('a description distinguishes duplicate role names when it is present in the payload', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 1000 })
  const { world, picker } = await openRoles(page, 4)
  await picker.getByRole('button', { name: 'Cancel', exact: true }).click()
  world.roles.find(role => role.id === 'runtime-3')!.description = 'Runtime for the studio workstation'
  await page.reload()
  await page.getByRole('button', { name: /^Role of workstation-agents:/ }).click()
  await expect(picker.getByRole('radio', { name: /Runtime for the studio workstation/ })).toHaveCount(1)
})

for (const width of [1440, 390]) test(`a rejected role change and another choice keep actions still at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const { world, picker } = await openRoles(page, 0)
  const requests: unknown[] = []
  await page.route(`**/api/members/${COORDINATOR}/workspace-role`, route => {
    requests.push(route.request().postDataJSON())
    return route.fulfill({ status: 403, json: { error: 'forbidden', reason: 'Role assignment refused: access changed.' } })
  })
  const options = picker.getByRole('radiogroup'), preview = picker.getByRole('region', { name: 'What changes' })
  const admin = options.getByRole('radio', { name: /^Admin/ }), member = options.getByRole('radio', { name: /^Member/ })
  const apply = picker.locator('.actions .primary')
  await expectStableControls({
    controls: { ...(width === 390 ? { frame: picker } : {}), options, admin, member, row: admin, preview, apply, cancel: picker.getByRole('button', { name: 'Cancel', exact: true }), actions: picker.locator('.actions') },
    scrollAreas: { picker, body: picker.locator('.picker-body'), preview },
    interactions: [
      { name: 'pick Admin', run: async () => { await admin.click(); await expect(admin).toHaveAttribute('aria-checked', 'true') } },
      { name: 'refused write', run: async () => { await apply.click(); await expect(picker.getByRole('alert')).toContainText('Role assignment refused: access changed.') } },
      { name: 'clear refusal with another choice', run: async () => { await member.click(); await expect(picker.getByRole('alert')).toHaveCount(0); await expect(member).toHaveAttribute('aria-checked', 'true') } },
    ],
  })
  expect(requests).toEqual([{ role_id: 'role-admin' }])
  expect(world.agents.find(agent => agent.principal_id === COORDINATOR)!.workspace_role).toBe('role-member')
})

for (const width of [1440, 390]) for (const kind of ['choice', 'group', 'epic', 'option', 'label', 'relation', 'facet', 'business'] as const) {
  test(`${kind} floating choices scroll as one panel at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const errors = watchErrors(page)
    const data = fixtures()
    const epic = data.nodes.find(node => node.kind_slug === 'epic')!
    data.nodes.push(...Array.from({ length: 16 }, (_, index) => ({ ...epic, id: `extra-epic-${index}`, key: `PHAROS-${index + 100}`, title: `Choice ${index + 1}`, state: 'open' })))
    await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
    await page.evaluate(async kind => {
      const path = '/tests/helpers/floating-lists.ts'
      const { mountFloatingList } = await import(/* @vite-ignore */ path)
      mountFloatingList(kind as FloatingList)
    }, kind)
    if (kind === 'facet') await page.getByRole('button', { name: 'Choices', exact: true }).click()
    const panel = page.locator('.floating[role="dialog"]')
    await expect(panel).toBeVisible()
    const list = panel.locator('.menu, .options, .picker-list')
    const rows = list.locator(':scope > button, :scope > label, :scope > li, :scope > .option')
    await expect.poll(() => rows.count()).toBeGreaterThanOrEqual(8)
    expect(await list.evaluate(el => el.scrollHeight - el.clientHeight), 'no nested clipping').toBeLessThanOrEqual(1)
    await insideViewport(panel, page)
    if (kind === 'label') await insideViewport(panel.getByRole('button', { name: 'Apply', exact: true }), page)
    await rows.last().scrollIntoViewIfNeeded()
    const last = (await rows.last().boundingBox())!, bounds = (await panel.boundingBox())!
    expect(last.y).toBeGreaterThanOrEqual(bounds.y)
    expect(last.y + last.height).toBeLessThanOrEqual(bounds.y + bounds.height)
    if (kind === 'label') await expectStableControls({
      controls: { panel, group: list, row: rows.last(), apply: panel.getByRole('button', { name: 'Apply', exact: true }) },
      scrollAreas: { panel, list },
      interactions: [{ name: 'choose the last label', run: async () => {
        await rows.last().click()
        await expect(rows.last()).toHaveAttribute('aria-checked', 'true')
      } }],
    })
    expect(await panel.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    expect(errors).toEqual([])
  })
}
