// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, COORDINATOR, DEPLOYER, ME } from './access-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { expectStableControls } from './helpers/stable'

async function open(page: Page) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.agents.find(agent => agent.principal_id === COORDINATOR)!.name = 'ops-agm'
  const role = world.roles.find(role => role.key === 'owner')!
  world.roles.push({ ...role, id: 'role-scopes', key: 'scope-worker', name: 'Scope worker', builtin: false })
  world.agents.find(agent => agent.principal_id === DEPLOYER)!.workspace_role = 'role-scopes'
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await expect(page.getByRole('list', { name: 'Agents' })).toBeVisible()
  return world
}

test('shared sheet variants and Start agent follow window resizing within their limits', async ({ page }) => {
  await open(page)
  const resize = async (sheet: Locator, maximum: number, phoneSheet = true) => {
    let previous = 0
    for (const width of [1024, 1440, 2560]) {
      await page.setViewportSize({ width, height: 1000 })
      const box = (await sheet.boundingBox())!
      expect(box.width).toBeGreaterThan(previous)
      expect(box.width).toBeLessThanOrEqual(maximum + 0.5)
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(width + 0.5)
      expect(await sheet.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      previous = box.width
    }
    await page.setViewportSize({ width: 390, height: 844 })
    const phone = (await sheet.boundingBox())!
    expect(phone.x).toBeGreaterThanOrEqual(0)
    expect(phone.x + phone.width).toBeLessThanOrEqual(390.5)
    if (phoneSheet) {
      expect(phone.width).toBeCloseTo(390, 0)
      expect(phone.height).toBeCloseTo(844, 0)
    }
  }

  await page.goto(`/settings/access/people/${ME}`)
  const person = dialog(page, 'Markus Barta, access')
  await expect(person).toBeVisible()
  await resize(person, 960)

  await page.goto('/settings/access/invites')
  await page.getByRole('button', { name: 'Invite people', exact: true }).click()
  const invite = dialog(page, 'Invite people')
  await expect(invite).toBeVisible()
  await resize(invite, 1120)
  await invite.getByRole('button', { name: 'Cancel', exact: true }).click()

  await page.goto('/settings/access/roles/role-lead')
  await page.getByRole('button', { name: /^Delete/ }).click()
  const deleteRole = dialog(page, 'Delete Delivery lead?')
  await expect(deleteRole).toBeVisible()
  await resize(deleteRole, 960)
  await deleteRole.getByRole('button', { name: 'Keep the role', exact: true }).click()

  await mockStartAgent(page)
  await page.goto('/agents')
  await page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true }).click()
  await page.getByRole('menuitem', { name: /^Start agent/ }).click()
  const start = dialog(page, 'Start agent')
  await expect(start).toBeVisible()
  await resize(start, 1040, false)
  await start.getByRole('button', { name: 'Cancel', exact: true }).click()
})

const agent = (page: Page, name: string) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: name })
const dialog = (page: Page, name: string) => page.getByRole('dialog', { name, exact: true })

async function screenshot(page: Page, info: TestInfo, name: string, width: number, theme: string) {
  await page.screenshot({ path: info.outputPath(`aeon-624-${name}-${width}-${theme}.png`) })
}

async function fits(button: Locator) {
  const sample = await button.evaluate(element => {
    const rect = element.getBoundingClientRect()
    const range = document.createRange()
    range.selectNodeContents(element)
    return {
      rect: { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width },
      rowWidth: element.parentElement!.clientWidth,
      overflow: element.scrollWidth - element.clientWidth,
      text: [...range.getClientRects()].map(line => ({ left: line.left, right: line.right, top: line.top, bottom: line.bottom })),
    }
  })
  expect(sample.rect.width).toBeGreaterThanOrEqual(Math.min(140, sample.rowWidth) - 0.5)
  expect(sample.rect.width).toBeLessThanOrEqual(sample.rowWidth + 0.5)
  expect(sample.overflow).toBeLessThanOrEqual(1)
  expect(sample.text.length).toBeGreaterThan(0)
  for (const line of sample.text) {
    expect(line.left).toBeGreaterThanOrEqual(sample.rect.left - 0.5)
    expect(line.right).toBeLessThanOrEqual(sample.rect.right + 0.5)
    expect(line.top).toBeGreaterThanOrEqual(sample.rect.top - 0.5)
    expect(line.bottom).toBeLessThanOrEqual(sample.rect.bottom + 0.5)
  }
}

async function columns(sheet: Locator, count: number) {
  const groups = sheet.locator('.scope-group')
  expect(await groups.count()).toBeGreaterThan(0)
  for (const group of await groups.all()) {
    const tracks = await group.evaluate(element => getComputedStyle(element).gridTemplateColumns.split(' ').map(Number.parseFloat))
    expect(tracks).toHaveLength(count)
    expect(tracks.every(track => track > 0)).toBe(true)
  }
}

function actions(sheet: Locator) {
  return {
    close: sheet.locator('.sheet-close'),
    cancel: sheet.getByRole('button', { name: 'Cancel', exact: true }),
    submit: sheet.locator('.sheet-foot .primary'),
    actions: sheet.locator('.sheet-foot'),
  }
}

// One targeted browser spec exercises the production components and preserves
// the required shots. Wider 1920px coverage also reaches the third scope column.
for (const width of [1440, 1024, 390, 1920]) for (const theme of ['light', 'dark'] as const) {
  test(`dialogs fit and keep their controls at ${width}px ${theme}`, async ({ page }, info) => {
    test.setTimeout(60_000)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const errors = watchErrors(page)
    const world = await open(page)

    await agent(page, 'ops-agm').getByRole('button', { name: 'Actions for ops-agm' }).click()
    await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
    const confirmation = dialog(page, 'Deactivate ops-agm?')
    await expect(confirmation).toBeVisible()
    const cancel = confirmation.getByRole('button', { name: 'Cancel', exact: true })
    const revoke = confirmation.getByRole('button', { name: 'Revoke keys and deactivate', exact: true })
    await expect(cancel).toBeFocused()
    await fits(cancel)
    await fits(revoke)
    const cancelBox = (await cancel.boundingBox())!, revokeBox = (await revoke.boundingBox())!
    if (width === 390) expect(revokeBox.y).toBeGreaterThanOrEqual(cancelBox.y + cancelBox.height)
    else expect(Math.abs(cancelBox.y - revokeBox.y)).toBeLessThanOrEqual(0.5)
    await screenshot(page, info, 'deactivate-confirm', width, theme)
    await expectStableControls({
      controls: { cancel, revoke, actions: confirmation.locator('.actions'), ...(width === 390 ? { frame: confirmation } : {}) },
      scrollAreas: { card: confirmation.locator('.confirm-card'), body: confirmation.locator('#confirm-body') },
      interactions: [{ name: 'long confirmation explanation', run: async () => {
        await page.evaluate(async () => {
          const path = '/src/lib/confirm.ts'
          const { confirmState } = await import(path)
          confirmState.request.body = 'Revoked keys stay revoked. '.repeat(200)
        })
        await expect(confirmation.locator('#confirm-body')).toContainText('Revoked keys stay revoked.')
      } }],
    })
    await cancel.click()
    expect(world.calls.filter(call => call.path.endsWith('/deactivate') && call.method === 'POST')).toHaveLength(0)

    // Exercise both translated labels and an unbroken compound word, without
    // changing a label on an already-open dialog under the pointer.
    for (const label of ['Schlüssel widerrufen und Agent deaktivieren', 'SämtlicheAgentenzugriffsschlüsselUnwiderruflichWiderrufenUndDenAgentenDeaktivieren'.repeat(3)]) {
      await page.evaluate(async confirmLabel => {
        const path = '/src/lib/confirm.ts'
        const { confirmAction } = await import(path)
        void confirmAction({ title: 'Deaktivieren?', confirmLabel, cancelLabel: 'Abbrechen', danger: true })
      }, label)
      const translated = dialog(page, 'Deaktivieren?')
      await expect(translated).toBeVisible()
      await fits(translated.getByRole('button', { name: label, exact: true }))
      await fits(translated.getByRole('button', { name: 'Abbrechen', exact: true }))
      await page.keyboard.press('Escape')
      await expect(translated).not.toBeVisible()
    }

    await agent(page, 'pharos-deployer').getByRole('button', { name: /active key/ }).click()
    await agent(page, 'pharos-deployer').getByRole('button', { name: /^Edit scopes/ }).click()
    const edit = dialog(page, 'Edit scopes for pharos-deployer')
    // Container breakpoints use body content width; scrollbar gutters differ by host.
    const availableWidth = await edit.locator('.sheet-body').evaluate(element => {
      const style = getComputedStyle(element)
      return element.clientWidth - Number.parseFloat(style.paddingLeft) - Number.parseFloat(style.paddingRight)
    })
    expect(availableWidth).toBeGreaterThan(0)
    const expectedColumns = availableWidth >= 900 ? 3 : availableWidth >= 600 ? 2 : 1
    await expect(edit.getByRole('checkbox', { name: /nodes\.read/ })).toBeChecked()
    await columns(edit, expectedColumns)
    const scope = edit.getByRole('checkbox', { name: /nodes\.write/ })
    const row = edit.locator('.scope-row').filter({ has: page.getByRole('checkbox', { name: /nodes\.write/ }) })
    await expectStableControls({
      controls: { ...actions(edit), scope, row, group: edit.getByRole('group', { name: 'Work', exact: true }) },
      scrollAreas: { sheet: edit, body: edit.locator('.sheet-body') },
      interactions: [
        { name: 'select a scope', run: async () => { await scope.check(); await expect(scope).toBeChecked() } },
        { name: 'clear the same scope', run: async () => { await scope.uncheck(); await expect(scope).not.toBeChecked() } },
      ],
    })
    await screenshot(page, info, 'edit-scopes', width, theme)
    await edit.getByRole('button', { name: 'Cancel', exact: true }).click()

    await agent(page, 'pharos-deployer').getByRole('button', { name: 'New key', exact: true }).click()
    const key = dialog(page, 'New key for pharos-deployer')
    await expect(key).toBeVisible()
    await columns(key, expectedColumns)
    const search = key.getByRole('searchbox', { name: 'Find a scope' })
    const preview = key.locator('details').filter({ hasText: 'Ticket worker' })
    await expectStableControls({
      controls: { ...actions(key), search, presets: key.locator('.preset-actions'), preview: preview.locator('summary') },
      scrollAreas: { sheet: key, body: key.locator('.sheet-body') },
      interactions: [
        { name: 'expand a preset preview', run: async () => { await preview.locator('summary').click(); await expect(preview).toHaveAttribute('open', '') } },
        { name: 'collapse the preview', run: async () => { await preview.locator('summary').click(); await expect(preview).not.toHaveAttribute('open', '') } },
        { name: 'filter scopes', run: async () => { await search.fill('nodes.write'); await expect(key.locator('.scope-row')).toHaveCount(1); await columns(key, expectedColumns) } },
        { name: 'restore scopes', run: async () => { await search.fill(''); await expect(key.locator('.scope-row').nth(1)).toBeVisible(); await columns(key, expectedColumns) } },
      ],
    })
    await screenshot(page, info, 'new-key', width, theme)
    await key.getByRole('button', { name: 'Cancel', exact: true }).click()

    await page.getByRole('button', { name: 'New agent', exact: true }).click()
    const newAgent = dialog(page, 'New agent')
    const purpose = newAgent.getByLabel('Purpose preset')
    const access = newAgent.getByLabel('Project access', { exact: true })
    await newAgent.getByLabel('Name', { exact: true }).fill('release-helper')
    await expectStableControls({
      controls: { ...actions(newAgent), purpose, name: newAgent.getByLabel('Name', { exact: true }) },
      scrollAreas: { sheet: newAgent, body: newAgent.locator('.sheet-body') },
      interactions: [
        { name: 'choose purpose', run: async () => { await purpose.selectOption('ticket-worker'); await expect(newAgent.locator('.preset-preview')).toBeVisible() } },
        { name: 'restore custom purpose', run: async () => { await purpose.selectOption(''); await expect(newAgent.locator('.preset-preview')).toHaveCount(0) } },
      ],
    })
    await expectStableControls({
      controls: { ...actions(newAgent), access },
      scrollAreas: { sheet: newAgent, body: newAgent.locator('.sheet-body') },
      interactions: [
        { name: 'show project choices', run: async () => { await access.selectOption('projects'); await expect(newAgent.getByRole('checkbox', { name: 'Pharos', exact: true })).toBeVisible() } },
        { name: 'hide project choices', run: async () => { await access.selectOption('workspace'); await expect(newAgent.locator('.projects')).toHaveCount(0) } },
      ],
    })
    await screenshot(page, info, 'new-agent', width, theme)
    await newAgent.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(errors).toEqual([])
  })
}
