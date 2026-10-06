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
import { expectCompactDialog, expectPhoneSheet, phoneSheetFindings, sampleDialog, type DialogSize, type PhoneSheetGeometry } from './helpers/dialog-scale'
import { expectStableControls, type StableInteraction } from './helpers/stable'

// AEON-730: every dialog takes one step of the shared size scale and keeps its
// actions compact on wide screens; phones get full-height sheets with a pinned
// action bar. Geometry, not screenshot equality (GUI-19). The audit widths
// (1440/1920) run only for evidence capture: AEON_DIALOG_AUDIT=1.
const WIDTHS = process.env.AEON_DIALOG_AUDIT === '1' ? [1440, 1920, 2560, 390] : [2560, 390]

// Phone sheets name their scrolling body and the content changes their action
// bar must ride out (AEON-541 pattern B).
interface Check { name: string; frame: Locator; actions: Locator; size: DialogSize; phoneSheet?: boolean; hasActions?: boolean; body?: Locator; changes?: StableInteraction[] }

function checker(page: Page, info: TestInfo, width: number, theme: string) {
  const viewport = { width, height: width === 390 ? 844 : 1000 }
  return async ({ name, frame, actions, size, phoneSheet = true, hasActions = true, body, changes }: Check) => {
    await expect(frame).toBeVisible()
    if (width > 600) await expectCompactDialog(frame, actions, size, viewport, hasActions)
    else if (phoneSheet) {
      if (!body) throw new Error(`${name}: a phone sheet needs its scrolling body`)
      await expectPhoneSheet(frame, actions, viewport, { body, changes })
    }
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
    await check({ name: 'confirm', frame: confirmation, actions: confirmation.locator('.actions'), size: 's', body: confirmation.locator('#confirm-body') })
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()

    // New key, then Key ready (AccessSheet, actions first): L, one frame for both states.
    await agent(page, 'pharos-deployer').getByRole('button', { name: /active key/ }).click()
    await agent(page, 'pharos-deployer').getByRole('button', { name: 'New key', exact: true }).click()
    const key = dialog(page, 'New key for pharos-deployer')
    const search = key.getByRole('searchbox', { name: 'Find a scope' })
    await check({ name: 'new-key', frame: key, actions: key.locator('.sheet-foot'), size: 'l', body: key.locator('.sheet-body'), changes: [
      { name: 'filter the scopes to nothing', run: async () => { await search.fill('zz-no-such-scope'); await expect(key.getByText('No scope matches')).toBeVisible() } },
      { name: 'clear the filter', run: async () => { await search.fill(''); await expect(key.getByRole('checkbox', { name: /nodes\.read/ })).toBeVisible() } },
    ] })
    if (width > 600) {
      // Scope presets are buttons too: each keeps its label's width.
      const presets = await sampleDialog(key, key.locator('.preset-actions'))
      expect.soft(presets.buttons.length).toBeGreaterThan(0)
      for (const button of presets.buttons) expect.soft(button.width, `preset “${button.label}” fits its label`).toBeLessThanOrEqual(button.natural + 1)
    }
    await key.getByRole('checkbox', { name: /nodes\.read/ }).check()
    await key.getByRole('button', { name: 'Create key', exact: true }).click()
    const ready = dialog(page, 'Key ready')
    await expect(ready.getByLabel('New agent key')).toBeVisible()
    await check({ name: 'key-ready', frame: ready, actions: ready.locator('.sheet-foot'), size: 'l', body: ready.locator('.sheet-body'), changes: [
      { name: 'copy the command', run: async () => { await ready.getByRole('button', { name: 'Copy command', exact: true }).click(); await expect(ready.getByRole('button', { name: 'Command copied', exact: true })).toBeVisible() } },
    ] })
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
    await check({ name: 'edit-scopes', frame: edit, actions: edit.locator('.sheet-foot'), size: 'l', body: edit.locator('.sheet-body') })
    await edit.getByRole('button', { name: 'Cancel', exact: true }).click()

    // New agent (AccessSheet, actions first): M.
    await page.getByRole('button', { name: 'New agent', exact: true }).click()
    const newAgent = dialog(page, 'New agent')
    await check({ name: 'new-agent', frame: newAgent, actions: newAgent.locator('.sheet-foot'), size: 'm', body: newAgent.locator('.sheet-body'), changes: [
      { name: 'type a name', run: async () => { await newAgent.getByLabel('Name', { exact: true }).fill('release-helper'); await expect(newAgent.getByLabel('Name', { exact: true })).toHaveValue('release-helper') } },
    ] })
    await newAgent.getByRole('button', { name: 'Cancel', exact: true }).click()

    // Invite people and Invite ready (AccessSheet): M.
    await page.goto('/settings/access/invites')
    await page.getByRole('button', { name: 'Invite people', exact: true }).click()
    const invite = dialog(page, 'Invite people')
    const email = invite.getByLabel('Email')
    await check({ name: 'invite', frame: invite, actions: invite.locator('.sheet-foot'), size: 'm', body: invite.locator('.sheet-body'), changes: [
      { name: 'type an email', run: async () => { await email.fill('nora@studio.at'); await expect(email).toHaveValue('nora@studio.at') } },
    ] })
    await email.fill('nora@studio.at')
    await invite.getByRole('button', { name: 'Create invite link', exact: true }).click()
    const inviteReady = dialog(page, 'Invite ready')
    await check({ name: 'invite-ready', frame: inviteReady, actions: inviteReady.locator('.sheet-foot'), size: 'm', body: inviteReady.locator('.sheet-body') })
    await inviteReady.getByRole('button', { name: 'Done', exact: true }).click()

    // Delete role (AccessSheet): S.
    await page.goto('/settings/access/roles/role-lead')
    await page.getByRole('button', { name: /^Delete/ }).click()
    const deleteRole = dialog(page, 'Delete Delivery lead?')
    await check({ name: 'delete-role', frame: deleteRole, actions: deleteRole.locator('.sheet-foot'), size: 's', body: deleteRole.locator('.sheet-body') })
    await deleteRole.getByRole('button', { name: 'Keep the role', exact: true }).click()

    // A person's access (AccessSheet, side): M.
    await page.goto(`/settings/access/people/${ME}`)
    const person = dialog(page, 'Markus Barta, access')
    await check({ name: 'person', frame: person, actions: person.locator('.sheet-body'), size: 'm', phoneSheet: false })

    // Start agent: L; on phones a near-full card whose footer ends the form.
    await mockStartAgent(page)
    await page.goto('/agents')
    await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
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
    // Each area's mocks are added just before it: later routes win, and an
    // area's permission mock must not turn the ticket pages read-only.
    await mockWork(page, fixtures(), { admin: true })
    await page.route('**/api/status/help**', route => route.fulfill({ json: defaultStatusHelp() }))

    // Keyboard shortcuts: M.
    await page.goto('/p/PHAROS')
    await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
    if (width > 600) {
      await page.keyboard.press('Shift+?')
      const shortcuts = dialog(page, 'Keyboard shortcuts')
      await check({ name: 'shortcuts', frame: shortcuts, actions: shortcuts, size: 'm', hasActions: false })
      await shortcuts.getByRole('button', { name: 'Close shortcuts' }).click()
    }

    // Done gate: M.
    const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-11$/ }) })
    await row.getByRole('button', { name: /Change status of PHAROS-11/ }).click()
    await page.getByRole('menuitemradio', { name: 'Done' }).click()
    const gate = dialog(page, "Before it's done: what does the user gain?")
    const pill = gate.locator('textarea, input.field').first()
    await check({ name: 'done-gate', frame: gate, actions: gate.locator('.actions'), size: 'm', body: gate.locator('.scroll'), changes: [
      { name: 'type a pill', run: async () => { await pill.fill('Faster releases'); await expect(pill).toHaveValue('Faster releases') } },
    ] })
    await gate.getByRole('button', { name: 'Not now' }).click()

    // Convert kind: M. Status help: L. A tenant kind with a longer name makes
    // the primary label change width when the kind changes.
    await page.route(/\/api\/kinds(\?|$)/, route => route.fulfill({ json: { items: ['work', 'epic', 'ticket', 'task', 'project', 'incident'].map(slug => ({ id: `k-${slug}`, slug, label: slug[0]!.toUpperCase() + slug.slice(1), short_prefix: slug.slice(0, 3).toUpperCase(), icon: slug === 'incident' ? 'bug' : slug, allowed_child_kinds: null, field_schema: slug === 'incident' ? { issue_family: true } : {} })) } }))
    await page.goto('/p/PHAROS/PHAROS-11')
    const details = page.getByRole('complementary', { name: 'Ticket details' })
    await details.getByRole('button', { name: 'More actions' }).click()
    await page.getByRole('menuitem', { name: 'Convert to…' }).click()
    const convert = dialog(page, 'Convert PHAROS-11')
    const kindChoice = (name: string) => convert.getByRole('radio', { name, exact: true })
    const pickKind = (name: string) => ({ name: `pick ${name}`, run: async () => { await kindChoice(name).click(); await expect(kindChoice(name)).toHaveAttribute('aria-checked', 'true') } })
    await expect(kindChoice('Epic')).toHaveAttribute('aria-checked', 'true')
    await check({ name: 'convert-kind', frame: convert, actions: convert.locator('.actions'), size: 'm', body: convert.locator('.convert-scroll'), changes: [pickKind('Incident'), pickKind('Task'), pickKind('Epic')] })
    // Switching the kind relabels the primary action; neither action nor any
    // kind row moves or resizes (AEON-541), on desktop as on phones.
    await expectStableControls({
      controls: {
        cancel: convert.getByRole('button', { name: 'Cancel', exact: true }),
        convert: convert.locator('.actions .btn').nth(1),
        'kind selector': convert.getByRole('radiogroup', { name: 'New kind' }),
        'Epic row': kindChoice('Epic'),
        'Task row': kindChoice('Task'),
        'Incident row': kindChoice('Incident'),
      },
      interactions: [pickKind('Incident'), pickKind('Task'), pickKind('Epic'), pickKind('Incident')],
    })
    await expect(convert.getByRole('button', { name: 'Convert to incident', exact: true })).toBeVisible()
    await convert.getByRole('button', { name: 'Cancel' }).click()
    await details.getByRole('button', { name: /Status: / }).click()
    await page.getByRole('menu', { name: 'Status of PHAROS-11' }).getByRole('menuitem', { name: 'What do these mean?' }).click()
    const help = dialog(page, 'What the statuses mean')
    await check({ name: 'status-help', frame: help, actions: help, size: 'l', phoneSheet: false })
    await help.getByRole('button', { name: 'Close the status help' }).click()

    // New knowledge entry: L (an editor).
    await mockKnowledge(page, knowledgeWorld())
    await page.goto('/p/PHAROS/knowledge')
    await expect(page.locator('.k-row').first()).toBeVisible()
    await page.keyboard.press('n')
    const knowledge = dialog(page, 'New knowledge entry')
    const title = knowledge.getByLabel('Title')
    await check({ name: 'knowledge-create', frame: knowledge, actions: knowledge.locator('.create-foot'), size: 'l', body: knowledge.locator('.create-card'), changes: [
      { name: 'type a title', run: async () => { await title.fill('Deploy a release'); await expect(title).toHaveValue('Deploy a release') } },
    ] })
    await knowledge.getByRole('button', { name: 'Cancel' }).click()

    // New customer and New quote: M.
    // The desktop lists open them with n; phones list customers differently.
    await mockCRM(page, crmData())
    if (width > 600) {
      await mockQuotes(page, quoteWorld())
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
    }

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
    await mockPairing(page)
    await page.goto('/agents/register-agent')
    await page.getByRole('button', { name: 'Disconnect', exact: true }).first().click()
    const disconnect = page.getByRole('dialog', { name: /^Disconnect / })
    await check({ name: 'disconnect', frame: disconnect, actions: disconnect, size: 's', phoneSheet: false })
    await disconnect.getByRole('button', { name: 'Cancel' }).click()

    // Crop a photo: L.
    await mockSettings(page, settingsData())
    await page.goto('/settings/personal')
    await expect(page.getByLabel('First name')).toHaveValue('Markus')
    const chooser = page.waitForEvent('filechooser')
    await page.getByRole('button', { name: /^(Add a photo|Change your photo)$/ }).click()
    await (await chooser).setFiles({ name: 'p.png', mimeType: 'image/png', buffer: makePng(1000, 500) })
    const crop = dialog(page, 'Crop your photo')
    await check({ name: 'avatar-crop', frame: crop, actions: crop.locator('.foot'), size: 'l', phoneSheet: false })
    await crop.locator('.foot').getByRole('button', { name: /^Cancel/ }).click()

    expect(errors).toEqual([])
  })
}

// The phone guard itself: it must reject what the AEON-730 review found it
// accepting (the frame passed as its own action bar, a short card, a bar that
// floats above the bottom edge or runs off screen, an empty bar).
test('the phone sheet guard rejects a short sheet, a floating bar and the frame as its own bar', () => {
  const viewport = { width: 390, height: 844 }
  const frame = { x: 0, y: 0, width: 390, height: 844 }
  const bar = { x: 0, y: 774, width: 390, height: 70 }
  const buttons = [{ label: 'Done', width: 358, height: 44, bottom: 830 }]
  const good: PhoneSheetGeometry = { frame, bar, barIsFrame: false, buttons }
  expect(phoneSheetFindings(good, viewport)).toEqual([])
  expect(phoneSheetFindings({ ...good, bar: frame, barIsFrame: true }, viewport)).toEqual(['the action bar is the sheet itself; pass its footer'])
  expect(phoneSheetFindings({ ...good, frame: { ...frame, y: 96, height: 420 }, bar: { ...bar, y: 446 } }, viewport)).toEqual(['sheet covers 96–516, not the window height 844'])
  expect(phoneSheetFindings({ ...good, frame: { ...frame, x: 8, width: 374 } }, viewport)).toEqual(['sheet spans 8–382, not the window width 390'])
  expect(phoneSheetFindings({ ...good, bar: { ...bar, y: 600 } }, viewport)).toEqual(["the action bar ends 174px above the sheet's bottom edge"])
  expect(phoneSheetFindings({ ...good, frame: { ...frame, height: 1000 }, bar: { ...bar, y: 930 }, buttons: [{ ...buttons[0]!, bottom: 986 }] }, viewport)).toEqual(['the action bar ends below the window (1000 > 844)', '“Done” ends below the window'])
  expect(phoneSheetFindings({ ...good, bar: null, buttons: [] }, viewport)).toEqual(['the action bar has no size', 'the action bar has no visible actions'])
  expect(phoneSheetFindings({ ...good, buttons: [{ ...buttons[0]!, height: 34 }] }, viewport)).toEqual(['“Done” is 34px, not a 44px touch target'])
})
