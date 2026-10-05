// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, COORDINATOR, DEPLOYER, ME } from './access-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { crmData, mockCRM, HOFER } from './crm-fixtures'
import { mockQuotes, quoteWorld } from './quote-list-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { mockPairing } from './agent-pairing-fixtures'
import { makePng, mockSettings, settingsData } from './settings-fixtures'
import { defaultStatusHelp } from '../src/lib/statusDefinitions'
import { expectCompactDialog, expectPhoneSheet, type DialogSize } from './helpers/dialog-scale'

// AEON-730: every dialog takes one step of the shared size scale and keeps its
// actions compact on wide screens; phones get full-height sheets with a pinned
// action bar. Geometry, not screenshot equality (GUI-19). The audit widths
// (1440/1920) run only for evidence capture: AEON_DIALOG_AUDIT=1.
const WIDTHS = process.env.AEON_DIALOG_AUDIT === '1' ? [1440, 1920, 2560, 390] : [2560, 390]

interface Check { name: string; frame: Locator; actions: Locator; size: DialogSize; phoneSheet?: boolean }

function checker(page: Page, info: TestInfo, width: number, theme: string) {
  const viewport = { width, height: width === 390 ? 844 : 1000 }
  return async ({ name, frame, actions, size, phoneSheet = true }: Check) => {
    await expect(frame).toBeVisible()
    if (width > 600) await expectCompactDialog(frame, actions, size, viewport)
    else if (phoneSheet) await expectPhoneSheet(frame, actions, viewport)
    else {
      const box = (await frame.boundingBox())!
      expect.soft(box.x).toBeGreaterThanOrEqual(-0.5)
      expect.soft(box.x + box.width).toBeLessThanOrEqual(width + 0.5)
    }
    await page.screenshot({ path: info.outputPath(`aeon-730-${name}-${width}-${theme}.png`) })
  }
}

const dialog = (page: Page, name: string) => page.getByRole('dialog', { name, exact: true })
const agent = (page: Page, name: string) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: name })

async function openAccess(page: Page) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.agents.find(agent => agent.principal_id === COORDINATOR)!.name = 'ops-agm'
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await expect(page.getByRole('list', { name: 'Agents' })).toBeVisible()
  return world
}

for (const width of WIDTHS) for (const theme of ['light', 'dark'] as const) {
  test(`dialogs keep their scale step and compact actions at ${width}px ${theme}`, async ({ page, context }, info) => {
    test.setTimeout(120_000)
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const errors = watchErrors(page)
    const check = checker(page, info, width, theme)
    await openAccess(page)

    // Confirmation (ConfirmHost): S.
    await agent(page, 'ops-agm').getByRole('button', { name: 'Actions for ops-agm' }).click()
    await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
    const confirmation = dialog(page, 'Deactivate ops-agm?')
    await check({ name: 'confirm', frame: confirmation, actions: confirmation.locator('.actions'), size: 's' })
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()

    // New key, then Key ready (AccessSheet, actions first): L, one frame for both states.
    await agent(page, 'pharos-deployer').getByRole('button', { name: /active key/ }).click()
    await agent(page, 'pharos-deployer').getByRole('button', { name: 'New key', exact: true }).click()
    const key = dialog(page, 'New key for pharos-deployer')
    await check({ name: 'new-key', frame: key, actions: key.locator('.sheet-foot'), size: 'l' })
    await key.getByRole('checkbox', { name: /nodes\.read/ }).check()
    await key.getByRole('button', { name: 'Create key', exact: true }).click()
    const ready = dialog(page, 'Key ready')
    await expect(ready.getByLabel('New agent key')).toBeVisible()
    await check({ name: 'key-ready', frame: ready, actions: ready, size: 'l' })
    if (width > 600) {
      // The one-time key and the CLI command take the room their text needs.
      const token = (await ready.getByLabel('New agent key').boundingBox())!
      const command = (await ready.getByLabel('CLI login command').boundingBox())!
      const frame = (await ready.boundingBox())!
      expect.soft(token.width, 'key field fits its key').toBeLessThan(frame.width - 120)
      expect.soft(command.height, 'command box fits its command').toBeLessThanOrEqual(64)
    }
    await ready.getByRole('button', { name: 'Done', exact: true }).click()
    await expect(ready).toHaveCount(0)

    // Edit scopes (AccessSheet, actions first): L.
    await agent(page, 'pharos-deployer').getByRole('button', { name: /^Edit scopes/ }).first().click()
    const edit = dialog(page, 'Edit scopes for pharos-deployer')
    await check({ name: 'edit-scopes', frame: edit, actions: edit.locator('.sheet-foot'), size: 'l' })
    await edit.getByRole('button', { name: 'Cancel', exact: true }).click()

    // New agent (AccessSheet, actions first): M.
    await page.getByRole('button', { name: 'New agent', exact: true }).click()
    const newAgent = dialog(page, 'New agent')
    await check({ name: 'new-agent', frame: newAgent, actions: newAgent.locator('.sheet-foot'), size: 'm' })
    await newAgent.getByRole('button', { name: 'Cancel', exact: true }).click()

    // Invite people and Invite ready (AccessSheet): M.
    await page.goto('/settings/access/invites')
    await page.getByRole('button', { name: 'Invite people', exact: true }).click()
    const invite = dialog(page, 'Invite people')
    await check({ name: 'invite', frame: invite, actions: invite.locator('.sheet-foot'), size: 'm' })
    await invite.getByLabel('Email').fill('nora@studio.at')
    await invite.getByRole('button', { name: 'Create invite link', exact: true }).click()
    const inviteReady = dialog(page, 'Invite ready')
    await check({ name: 'invite-ready', frame: inviteReady, actions: inviteReady, size: 'm' })
    await inviteReady.getByRole('button', { name: 'Done', exact: true }).click()

    // Delete role (AccessSheet): S.
    await page.goto('/settings/access/roles/role-lead')
    await page.getByRole('button', { name: /^Delete/ }).click()
    const deleteRole = dialog(page, 'Delete Delivery lead?')
    await check({ name: 'delete-role', frame: deleteRole, actions: deleteRole.locator('.sheet-foot'), size: 's' })
    await deleteRole.getByRole('button', { name: 'Keep the role', exact: true }).click()

    // A person's access (AccessSheet, side): M.
    await page.goto(`/settings/access/people/${ME}`)
    const person = dialog(page, 'Markus Barta, access')
    await check({ name: 'person', frame: person, actions: person.locator('.sheet-body'), size: 'm', phoneSheet: false })

    // Start agent: L; on phones a near-full card whose footer ends the form.
    await mockStartAgent(page)
    await page.goto('/agents')
    await page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true }).click()
    await page.getByRole('menuitem', { name: /^Start agent/ }).click()
    const start = dialog(page, 'Start agent')
    await check({ name: 'start-agent', frame: start, actions: start.locator('footer'), size: 'l', phoneSheet: false })
    await start.getByRole('button', { name: 'Cancel', exact: true }).click()

    expect(errors).toEqual([])
  })
}

for (const width of WIDTHS) for (const theme of ['light', 'dark'] as const) {
  test(`work, business and settings dialogs keep their scale step at ${width}px ${theme}`, async ({ page }, info) => {
    test.setTimeout(120_000)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
    const errors = watchErrors(page)
    const check = checker(page, info, width, theme)
    await mockWork(page, fixtures(), { admin: true })
    await mockCRM(page, crmData())
    await mockQuotes(page, quoteWorld())
    await mockKnowledge(page, knowledgeWorld())
    await mockSettings(page, settingsData())
    await mockPairing(page)
    await page.route('**/api/status/help**', route => route.fulfill({ json: defaultStatusHelp() }))

    // Keyboard shortcuts: M.
    await page.goto('/p/PHAROS')
    await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
    if (width > 600) {
      await page.keyboard.press('Shift+?')
      const shortcuts = dialog(page, 'Keyboard shortcuts')
      await check({ name: 'shortcuts', frame: shortcuts, actions: shortcuts, size: 'm' })
      await shortcuts.getByRole('button', { name: 'Close shortcuts' }).click()
    }

    // Done gate: M.
    const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-11$/ }) })
    await row.getByRole('button', { name: /Change status of PHAROS-11/ }).click()
    await page.getByRole('menuitemradio', { name: 'Done' }).click()
    const gate = dialog(page, "Before it's done: what does the user gain?")
    await check({ name: 'done-gate', frame: gate, actions: gate.locator('.actions'), size: 'm' })
    await gate.getByRole('button', { name: 'Not now' }).click()

    // Convert kind: M. Status help: L.
    await page.goto('/p/PHAROS/PHAROS-11')
    const details = page.getByRole('complementary', { name: 'Ticket details' })
    await details.getByRole('button', { name: 'More actions' }).click()
    await page.getByRole('menuitem', { name: 'Convert to…' }).click()
    const convert = dialog(page, 'Convert PHAROS-11')
    await check({ name: 'convert-kind', frame: convert, actions: convert.locator('.actions'), size: 'm' })
    await convert.getByRole('button', { name: 'Cancel' }).click()
    await details.getByRole('button', { name: /Status: / }).click()
    await page.getByRole('menu', { name: 'Status of PHAROS-11' }).getByRole('menuitem', { name: 'What do these mean?' }).click()
    const help = dialog(page, 'What the statuses mean')
    await check({ name: 'status-help', frame: help, actions: help, size: 'l', phoneSheet: false })
    await help.getByRole('button', { name: 'Close the status help' }).click()

    // New knowledge entry: M.
    await page.goto('/p/PHAROS/knowledge')
    await expect(page.locator('.k-row').first()).toBeVisible()
    await page.keyboard.press('n')
    const knowledge = dialog(page, 'New knowledge entry')
    await check({ name: 'knowledge-create', frame: knowledge, actions: knowledge.locator('.create-foot'), size: 'm' })
    await knowledge.getByRole('button', { name: 'Cancel' }).click()

    // New customer and New quote: M.
    await page.goto('/business/customers')
    await expect(page.getByRole('grid', { name: 'Customers' }).locator('tbody .name-link').first()).toBeVisible()
    await page.keyboard.press('n')
    const customer = dialog(page, 'New customer')
    await check({ name: 'customer-create', frame: customer, actions: customer.locator('.create-foot'), size: 'm' })
    await customer.getByRole('button', { name: 'Cancel' }).click()
    await page.goto('/business/quotes')
    await expect(page.getByRole('grid', { name: 'Quotes' })).toBeVisible()
    await page.getByRole('button', { name: 'New quote' }).click()
    const quote = dialog(page, 'New quote')
    await check({ name: 'quote-create', frame: quote, actions: quote.locator('.create-foot'), size: 'm' })
    await quote.getByRole('button', { name: 'Cancel' }).click()

    // Apply a notes rewrite: L.
    await page.goto(`/business/customers/${HOFER}`)
    const notes = page.getByRole('region', { name: 'Notes' })
    await notes.getByRole('button', { name: 'Propose a rewrite' }).click()
    await notes.getByRole('textbox', { name: 'Proposed notes, Markdown' }).fill('Prefers calls before 10:00.\nNew line.')
    await notes.getByRole('button', { name: 'Save proposal' }).click()
    await notes.getByRole('region', { name: 'Proposed rewrite' }).getByRole('button', { name: 'Review and apply…' }).click()
    const apply = dialog(page, 'Apply this rewrite?')
    await check({ name: 'notes-apply', frame: apply, actions: apply.locator('.apply-actions'), size: 'l', phoneSheet: false })
    await apply.getByRole('button', { name: 'Cancel' }).click()

    // Disconnect a computer: S.
    await page.goto('/agents/register-agent')
    await page.getByRole('button', { name: 'Disconnect', exact: true }).first().click()
    const disconnect = page.getByRole('dialog', { name: /^Disconnect / })
    await check({ name: 'disconnect', frame: disconnect, actions: disconnect, size: 's', phoneSheet: false })
    await disconnect.getByRole('button', { name: 'Cancel' }).click()

    // Crop a photo: L.
    await page.goto('/settings/personal')
    await expect(page.getByLabel('First name')).toHaveValue('Markus')
    const chooser = page.waitForEvent('filechooser')
    await page.getByRole('button', { name: /^(Add a photo|Change your photo)$/ }).click()
    await (await chooser).setFiles({ name: 'p.png', mimeType: 'image/png', buffer: makePng(1000, 500) })
    const crop = dialog(page, 'Crop your photo')
    await check({ name: 'avatar-crop', frame: crop, actions: crop.locator('.foot'), size: 'l', phoneSheet: false })
    await crop.getByRole('button', { name: 'Cancel' }).click()

    expect(errors).toEqual([])
  })
}
