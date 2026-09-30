// SPDX-License-Identifier: AGPL-3.0-only
// Settings -> Access (AEON-148, ADR-003) on the mocked authz contract: people and
// their aliases, role changes with their effect, the last owner, deactivation,
// linking classic identities, invites and the one-time join link, custom roles
// without escalation, project access, agents and keys, the access log, can()
// gating, axe in both themes and the phone layout.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { DEPLOYER, JONAS, LENA, ME, MIRA, accessWorld, mockAccess, type AccessWorld } from './access-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

async function open(page: Page, path = '/settings/access/people', options: Parameters<typeof accessWorld>[0] = {}): Promise<AccessWorld> {
  await mockWork(page, fixtures())
  const world = accessWorld(options)
  await mockAccess(page, world)
  await page.goto(path)
  await expect(page.locator('.access-card .panel > :not(.set-skeleton)').first()).toBeVisible()
  return world
}
const people = (page: Page) => page.getByRole('table', { name: 'People' })
const row = (page: Page, name: string) => people(page).locator('tr.person').filter({ has: page.getByRole('link', { name: new RegExp(`^${name}`) }) })
const calls = (world: AccessWorld, method: string, path: RegExp) => world.calls.filter(c => c.method === method && path.test(c.path))

test('people: one row per person, classic aliases under them, status apart from actions', async ({ page }) => {
  const errors = watchErrors(page)
  await open(page)
  await expect(people(page).locator('tr.person')).toHaveCount(6)
  const markus = row(page, 'Markus Barta')
  await expect(markus).toContainText('also known as mba (classic)')
  await expect(markus.locator('.c-status')).toHaveText('Active')
  await expect(row(page, 'Paul Steiner').locator('.c-status')).toHaveText('Deactivated')
  await expect(row(page, 'Lena Graf').locator('.c-role')).toContainText('Projects only')
  await expect(row(page, 'Lena Graf').locator('.c-proj')).toHaveText('Pharos, Aeon')
  // Aliases are not rows of their own; unlinked classic identities wait, folded.
  await expect(people(page)).not.toContainText('jw (classic)')
  const fold = page.getByRole('button', { name: /Imported from classic, no sign-in/ })
  await expect(fold).toHaveAttribute('aria-expanded', 'false')
  await expect(fold).toContainText('2')
  await page.getByRole('radio', { name: /^Deactivated/ }).click()
  await expect(people(page).locator('tr.person')).toHaveCount(1)
  await page.getByRole('radio', { name: /^All/ }).click()
  await page.getByLabel('Find a person').fill('mba')
  await expect(people(page).locator('tr.person')).toHaveCount(1)
  expect(errors).toEqual([])
})

test('a role change shows its effect first, applies, and can be undone', async ({ page }) => {
  const world = await open(page)
  await row(page, 'Jonas Weber').getByRole('button', { name: /Workspace role of Jonas Weber/ }).click()
  const picker = page.getByRole('dialog', { name: 'Role of Jonas Weber' })
  await expect(picker.getByRole('radio', { name: /^Member/ })).toHaveAttribute('aria-checked', 'true')
  await picker.getByRole('radio', { name: /^Admin/ }).click()
  await expect(picker.locator('.effect')).toContainText('Can ')
  await expect(picker.locator('.delta-h.gain')).toContainText('Gains 11')
  await expect(picker.locator('.perm', { hasText: 'Manage members' })).toBeVisible()
  expect(calls(world, 'PUT', /workspace-role/)).toHaveLength(0)
  await picker.getByRole('button', { name: 'Give Jonas Weber Admin' }).click()
  await expect(row(page, 'Jonas Weber').locator('.c-role')).toContainText('Admin')
  expect(calls(world, 'PUT', /workspace-role/).at(-1)?.body).toEqual({ role_id: 'role-admin' })
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'Jonas Weber').locator('.c-role')).toContainText('Member')
  expect(calls(world, 'PUT', /workspace-role/).at(-1)?.body).toEqual({ role_id: 'role-member' })
})

test('no escalation: an admin cannot give Owner, and is told why', async ({ page }) => {
  await open(page, '/settings/access/people', { role: 'admin' })
  await row(page, 'Mira Holm').getByRole('button', { name: /Workspace role of Mira Holm/ }).click()
  const owner = page.getByRole('radio', { name: /^Owner/ })
  await expect(owner).toContainText('Includes permissions you do not hold: Manage the workspace, Transfer ownership.')
  await owner.click()
  await expect(page.getByRole('button', { name: 'Give Mira Holm Owner' })).toBeDisabled()
})

test('an invite offers only project roles the inviter holds in the workspace, as the server checks', async ({ page }) => {
  // Invites check a project role against the inviter's workspace permissions,
  // not the project's: a people manager who leads Pharos still cannot invite
  // someone as Delivery lead there.
  await mockWork(page, fixtures())
  const world = accessWorld()
  const member = world.roles.find(r => r.id === 'role-member')!
  world.roles.push({ ...member, id: 'role-people', key: 'people-manager', name: 'People manager', description: 'Invites people.', builtin: false, permissions: [...member.permissions, 'members.manage'], based_on: member.id })
  world.people.find(p => p.principal_id === ME)!.workspace_role = 'role-people'
  world.bindings.push({ principal_id: ME, project_id: 'p-pharos', role_id: 'role-lead' })
  await mockAccess(page, world)
  await page.goto('/settings/access/invites')
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByRole('button', { name: 'Add a project' }).click()
  await sheet.getByLabel('Project 1', { exact: true }).selectOption({ label: 'Pharos' })
  const lead = sheet.getByLabel('Role on project 1', { exact: true }).locator('option', { hasText: 'Delivery lead' })
  await expect(lead).toBeDisabled()
  await expect(lead).toContainText('which you do not hold')
  await expect(sheet.getByLabel('Role on project 1', { exact: true }).locator('option', { hasText: 'Guest' })).toBeEnabled()
})

test('the last owner keeps Owner, and the reason is said instead of a silent block', async ({ page }) => {
  await open(page)
  const role = row(page, 'Markus Barta').getByRole('button', { name: /Workspace role of Markus Barta/ })
  await role.click()
  const picker = page.getByRole('dialog', { name: 'Role of Markus Barta' })
  await expect(picker.locator('.locked')).toContainText('The last active owner keeps Owner')
  await expect(picker.getByRole('button', { name: 'Close' })).toBeVisible()
  await page.keyboard.press('Escape')
  await row(page, 'Markus Barta').getByRole('button', { name: 'Actions for Markus Barta' }).click()
  const deactivate = page.getByRole('menuitem', { name: /Deactivate/ })
  await expect(deactivate).toHaveAttribute('aria-disabled', 'true')
  await expect(deactivate).toContainText('The last active owner keeps Owner')
})

test('deactivating says what happens, then shows the status; reactivating brings them back', async ({ page }) => {
  const world = await open(page)
  await row(page, 'Mira Holm').getByRole('button', { name: 'Actions for Mira Holm' }).click()
  await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Deactivate Mira Holm?' })
  await expect(dialog).toContainText('Mira is signed out everywhere and cannot sign in again.')
  await expect(dialog).toContainText('Their agent keys are revoked at once.')
  await expect(dialog).toContainText('Their tickets, comments and history stay')
  await dialog.getByRole('button', { name: 'Deactivate Mira' }).click()
  await expect(row(page, 'Mira Holm').locator('.c-status')).toHaveText('Deactivated')
  expect(calls(world, 'POST', new RegExp(`/members/${MIRA}/deactivate`))).toHaveLength(1)
  // The person sheet offers the way back.
  await row(page, 'Mira Holm').getByRole('link', { name: /^Mira Holm/ }).click()
  await expect(page).toHaveURL(`/settings/access/people/${MIRA}`)
  const sheet = page.getByRole('dialog', { name: 'Mira Holm, access' })
  await sheet.getByRole('button', { name: 'Reactivate Mira' }).click()
  await expect(sheet.locator('.facts')).toContainText('Active')
  await sheet.getByRole('button', { name: 'Close Mira Holm, access' }).click()
  await expect(page).toHaveURL('/settings/access/people')
})

test('linking a classic identity to a person, with undo', async ({ page }) => {
  const world = await open(page)
  await page.getByRole('button', { name: /Imported from classic, no sign-in/ }).click()
  await page.getByRole('button', { name: 'Link jw (classic) to a person' }).click()
  const picker = page.getByRole('dialog', { name: 'Link jw (classic) to' })
  await picker.getByLabel(/Search link/i).fill('jonas')
  await picker.getByRole('option', { name: /Jonas Weber/ }).click()
  await expect(row(page, 'Jonas Weber')).toContainText('also known as jw (classic)')
  expect(calls(world, 'POST', new RegExp(`/members/${JONAS}/aliases`)).at(-1)?.body).toEqual({ from_principal_id: world.people.find(p => p.principal_id === JONAS)!.aliases[0]!.principal_id })
  await expect(page.getByRole('button', { name: /Imported from classic/ })).toContainText('1')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'Jonas Weber')).not.toContainText('also known as')
})

test('invite by email: checked fields, a one-time join link to copy, then revoke', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const world = await open(page, '/settings/access/invites')
  await expect(page.getByText('Aeon never sends email.').first()).toBeVisible()
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(sheet.getByText('Enter the email address they sign in with.')).toBeVisible()
  await expect(sheet.getByLabel('Email')).toBeFocused()
  // The server's reason lands on the field it names.
  await sheet.getByLabel('Email').fill('anna@studio.at')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(sheet.locator('#email-error')).toContainText('There is already a pending invite for anna@studio.at')
  await sheet.getByLabel('Email').fill('nora@studio.at')
  await sheet.getByLabel('Workspace role').selectOption({ label: 'None: only the projects below' })
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(sheet.getByRole('alert')).toContainText('Give a workspace role or at least one project')
  await sheet.getByRole('button', { name: 'Add a project' }).click()
  await sheet.getByLabel('Role on project 1').selectOption({ label: 'Guest' })
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  const ready = page.getByRole('dialog', { name: 'Invite ready' })
  await expect(ready).toContainText('This link is shown only now')
  await expect(ready.getByLabel('Join link')).toHaveValue(/\/join\/tok_/)
  await ready.getByRole('button', { name: 'Copy link' }).click()
  await expect(ready.getByRole('button', { name: 'Copied' })).toBeVisible()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toMatch(/\/join\/tok_/)
  expect(calls(world, 'POST', /\/members\/invites$/).at(-1)?.body).toEqual({ email: 'nora@studio.at', project_roles: [{ project_id: 'p-pharos', role_id: 'role-guest' }], expires_in_days: 14 })
  await ready.getByRole('button', { name: 'Done' }).click()
  const invite = page.getByRole('list', { name: 'Invites' }).getByRole('listitem').filter({ hasText: 'nora@studio.at' })
  await expect(invite).toContainText('Guest on Pharos')
  await expect(invite).toContainText('Pending')
  await invite.getByRole('button', { name: 'Revoke the invite for nora@studio.at' }).click()
  const confirm = page.getByRole('dialog', { name: 'Revoke the invite for nora@studio.at?' })
  await expect(confirm).toContainText('The join link stops working at once.')
  await confirm.getByRole('button', { name: 'Revoke invite' }).click()
  await expect(page.getByRole('list', { name: 'Invites' }).getByRole('listitem').filter({ hasText: 'nora@studio.at' })).toHaveCount(0)
  await page.getByRole('radio', { name: /^Revoked/ }).click()
  await expect(page.getByRole('list', { name: 'Invites' })).toContainText('nora@studio.at')
})

test('configured identity provider can create a sign-in account and retry a failed setup', async ({ page }) => {
  const world = await open(page, '/settings/access/invites', { provisioner: 'Zitadel', provisionFailures: 1 })
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByLabel('Email').fill('new@studio.at')
  await sheet.getByLabel('Also create their sign-in account (Zitadel)').check()
  await expect(sheet).toContainText('Zitadel emails them a link to set up sign-in.')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(sheet.getByText('Enter their name for the sign-in account.')).toBeVisible()
  await sheet.getByLabel('Their name').fill('Nora Example')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  const ready = page.getByRole('dialog', { name: 'Invite ready' })
  await expect(ready).toContainText('The sign-in account could not be set up.')
  await expect(ready.getByLabel('Join link')).toHaveValue(/tok_inv-/)
  expect(calls(world, 'POST', /\/members\/invites$/).at(-1)?.body).toMatchObject({ provision_account: true, display_name: 'Nora Example' })
  await ready.getByRole('button', { name: 'Retry account setup' }).click()
  await expect(ready).toContainText('Zitadel has emailed them a sign-in setup link.')
  expect(calls(world, 'POST', /\/members\/invites\/[^/]+\/provision$/)).toHaveLength(1)
})

test('invite account checkbox stays hidden when no provisioner is configured', async ({ page }) => {
  await open(page, '/settings/access/invites')
  await page.getByRole('button', { name: 'Invite people' }).click()
  await expect(page.getByLabel(/Also create their sign-in account/)).toHaveCount(0)
})

test('custom roles: duplicate, compose without escalation, see the diff, save; delete with reassignment', async ({ page }) => {
  const world = await open(page, '/settings/access/roles', { role: 'admin' })
  await page.getByRole('link', { name: /^Member/ }).click()
  await expect(page.getByText('Built-in roles come with Aeon')).toBeVisible()
  await expect(page.getByRole('checkbox', { name: /See work/ })).toBeDisabled()
  await page.getByRole('button', { name: 'Duplicate to customize' }).click()
  await expect(page).toHaveURL('/settings/access/roles/new?from=role-member')
  await page.getByLabel('Name', { exact: true }).fill('Quote lead')
  // What I do not hold stays off, with the reason.
  const workspace = page.locator('.perm', { hasText: 'Manage the workspace' })
  await expect(workspace.getByRole('checkbox')).toBeDisabled()
  await expect(workspace).toContainText('You do not hold this permission, so you cannot give it.')
  await page.getByRole('checkbox', { name: /Issue quotes/ }).check()
  await page.getByRole('checkbox', { name: /Approve hours/ }).check()
  await expect(page.locator('.vs')).toContainText('2 added')
  await expect(page.locator('.perm', { hasText: 'Issue quotes' }).locator('.risk')).toHaveText(/High risk/)
  await page.getByRole('radio', { name: /^Changes/ }).click()
  await expect(page.locator('.perm')).toHaveCount(2)
  await page.getByLabel('Find a permission').fill('hours')
  await expect(page.locator('.perm')).toHaveCount(1)
  await page.getByRole('button', { name: 'Create role' }).click()
  await expect(page).toHaveURL(/\/settings\/access\/roles\/role-custom-/)
  const created = calls(world, 'POST', /\/api\/roles$/).at(-1)!.body as { name: string; permissions: string[]; based_on: string }
  expect(created.name).toBe('Quote lead')
  expect(created.based_on).toBe('role-member')
  expect(created.permissions).toEqual(expect.arrayContaining(['quotes.issue', 'hours.approve']))
  expect(created.permissions).not.toContain('workspace.manage')
  // Delete an in-use role: its holders get the role I choose.
  await page.goto('/settings/access/roles/role-lead')
  await page.getByRole('button', { name: 'Delete role…' }).click()
  const sheet = page.getByRole('dialog', { name: 'Delete Delivery lead?' })
  await expect(sheet).toContainText('1 person or agent holds it')
  await sheet.getByLabel('Give its holders').selectOption({ label: 'Member' })
  await sheet.getByRole('button', { name: 'Delete and give Member' }).click()
  await expect(page).toHaveURL('/settings/access/roles')
  expect(calls(world, 'DELETE', /\/api\/roles\/role-lead\?reassign_to=role-member/)).toHaveLength(1)
  await expect(page.getByRole('link', { name: /^Delivery lead/ })).toHaveCount(0)
})

test('a role name the server refuses is shown at the name field', async ({ page }) => {
  await open(page, '/settings/access/roles/new')
  await page.getByLabel('Name', { exact: true }).fill('Admin')
  await page.getByRole('button', { name: 'Create role' }).click()
  await expect(page.locator('#role-name-error')).toHaveText('There is already a role called “Admin”.')
  await expect(page.getByLabel('Name', { exact: true })).toHaveAttribute('aria-invalid', 'true')
})

test('project access: through the workspace or the project; add, change and remove project roles', async ({ page }) => {
  const world = await open(page, '/settings/access/projects')
  await page.getByRole('link', { name: /PHAROS\s*Pharos/ }).click()
  await expect(page).toHaveURL('/settings/access/projects/p-pharos')
  const list = page.getByRole('list', { name: 'People on Pharos' })
  await expect(list.getByRole('listitem').filter({ hasText: 'Jonas Weber' })).toContainText('Delivery lead here · Member in the workspace')
  await expect(list.getByRole('listitem').filter({ hasText: 'Lena Graf' })).toContainText('Guest here · no workspace role')
  await expect(list.getByRole('listitem').filter({ hasText: 'Mira Holm' })).toContainText('Admin in the workspace, which reaches every project')
  // Add someone with a project role.
  await page.getByRole('button', { name: 'Add someone' }).click()
  await page.getByRole('dialog', { name: 'Add to Pharos' }).getByRole('option', { name: /Cleo Customer/ }).click()
  const picker = page.getByRole('dialog', { name: 'Role of Cleo Customer on Pharos' })
  await picker.getByRole('radio', { name: /^Guest/ }).click()
  await picker.getByRole('button', { name: 'Give Cleo Customer Guest on Pharos' }).click()
  await expect(list.getByRole('listitem').filter({ hasText: 'Cleo Customer' })).toContainText('Guest here')
  expect(calls(world, 'PUT', /\/projects\/p-pharos\/members\//).at(-1)?.body).toEqual({ role_id: 'role-guest' })
  // Remove Lena: the confirmation says she loses the project entirely.
  await list.getByRole('button', { name: 'Remove Lena Graf from Pharos' }).click()
  const confirm = page.getByRole('dialog', { name: 'Remove Lena Graf from Pharos?' })
  await expect(confirm).toContainText('Lena no longer sees Pharos.')
  await confirm.getByRole('button', { name: 'Remove from project' }).click()
  await expect(list.getByRole('listitem').filter({ hasText: 'Lena Graf' })).toHaveCount(0)
  expect(calls(world, 'DELETE', new RegExp(`/projects/p-pharos/members/${LENA}`))).toHaveLength(1)
})

test('project access is reachable from a person', async ({ page }) => {
  await open(page, `/settings/access/people/${LENA}`)
  const sheet = page.getByRole('dialog', { name: 'Lena Graf, access' })
  await expect(sheet.locator('#projects')).toContainText('Pharos')
  await sheet.getByRole('link', { name: 'Aeon' }).click()
  await expect(page).toHaveURL('/settings/access/projects/p-aeon')
  await expect(page.getByRole('heading', { name: 'Aeon', level: 3 })).toBeVisible()
})

test('agents: role and keys; a new key shows once; service principals are internal and keyless', async ({ page }) => {
  const world = await open(page, '/settings/access/agents')
  const agents = page.getByRole('list', { name: 'Agents' })
  await expect(agents).toContainText('aeon-coordinator')
  const internal = page.locator('.internal')
  await expect(internal).toContainText('System')
  await expect(internal).toContainText('Internal')
  await expect(internal.getByRole('button')).toHaveCount(0)
  const deployer = agents.getByRole('listitem').filter({ hasText: 'pharos-deployer' })
  await deployer.getByRole('button', { name: /active key/ }).click()
  await expect(deployer.locator('tbody tr')).toHaveCount(2)
  await deployer.getByRole('button', { name: 'New key' }).click()
  const sheet = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
  await expect(sheet).toContainText('never more')
  // A key without scopes can do nothing (SEC4), so one is never created.
  await sheet.getByRole('button', { name: 'Create key' }).click()
  await expect(sheet.getByRole('alert')).toContainText('Choose at least one thing it may do')
  expect(calls(world, 'POST', /\/agent-keys$/)).toHaveLength(0)
  // The scopes are the registry's agent-grantable permissions; human governance is not offered.
  await expect(sheet.getByRole('checkbox', { name: /members\.manage/ })).toHaveCount(0)
  await sheet.getByRole('button', { name: 'Coordinator' }).click()
  await expect(sheet.getByRole('button', { name: 'Coordinator' })).toHaveAttribute('aria-pressed', 'true')
  // The deployer is a Viewer: a scope beyond that role is disabled, with the reason.
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
  await expect(sheet).toContainText('nodes.write · beyond pharos-deployer’s role (Viewer)')
  await sheet.getByRole('checkbox', { name: /nodes\.read/ }).uncheck()
  await expect(sheet.getByRole('button', { name: 'Coordinator' })).toHaveAttribute('aria-pressed', 'false')
  await sheet.getByRole('checkbox', { name: /nodes\.read/ }).check()
  await sheet.getByRole('button', { name: 'Create key' }).click()
  const ready = page.getByRole('dialog', { name: 'Key ready' })
  await expect(ready.getByLabel('New agent key')).toHaveValue(/^aeon_/)
  await expect(ready).toContainText('shown only now')
  await ready.getByRole('button', { name: 'Done' }).click()
  await expect(deployer.locator('tbody tr')).toHaveCount(3)
  const body = calls(world, 'POST', /\/agent-keys$/)[0]!.body as { principal_id: string; scopes: string[] }
  expect(body.principal_id).toBe(DEPLOYER)
  expect(body.scopes).toContain('nodes.read')
  expect(body.scopes).not.toContain('nodes.write')
  await expect(deployer.locator('tbody tr').first()).toContainText('nodes.read')
  await deployer.getByRole('button', { name: 'Revoke key aeon_ph4r' }).click()
  await page.getByRole('dialog', { name: /Revoke the key aeon_ph4r/ }).getByRole('button', { name: 'Revoke key' }).click()
  await expect(deployer.locator('tbody tr', { hasText: 'aeon_ph4r' }).locator('.state')).toHaveText('Revoked')
  expect(calls(world, 'DELETE', /\/agent-keys\/k2/)).toHaveLength(1)
})

const RELIC = '99999999-9999-4999-8999-999999999991'
const LAPTOP = '99999999-9999-4999-8999-999999999992'
const LAPTOP_OLD = '99999999-9999-4999-8999-999999999993'
// A relic with a live key, a connected computer, and an old computer identity already deactivated (AEON-470).
async function withRetirable(page: Page, world: AccessWorld) {
  world.agents.push(
    { principal_id: RELIC, name: 'relic', workspace_role: null, last_seen_at: new Date(Date.parse('2026-09-20T12:00:00Z')).toISOString(), service: false },
    { principal_id: LAPTOP, name: 'mbp2607', workspace_role: null, last_seen_at: new Date(Date.parse('2026-09-23T11:30:00Z')).toISOString(), service: false, connected_computer: true },
    { principal_id: LAPTOP_OLD, name: 'mbp2607 old', workspace_role: null, last_seen_at: new Date(Date.parse('2026-09-21T12:00:00Z')).toISOString(), service: false, status: 'deactivated' },
  )
  world.keys.push(
    { id: 'k-relic', principal_id: RELIC, name: 'relic', prefix: 'r3lc', scopes: ['nodes.read'], created_at: '2026-09-01T00:00:00Z', expires_at: null, last_used_at: null, revoked_at: null },
    { id: 'k-laptop', principal_id: LAPTOP, name: 'mbp2607', prefix: 'l4pt', scopes: ['nodes.read'], created_at: '2026-09-22T00:00:00Z', expires_at: null, last_used_at: null, revoked_at: null },
    { id: 'k-old', principal_id: LAPTOP_OLD, name: 'mbp2607 old', prefix: 'o1d0', scopes: ['nodes.read'], created_at: '2026-09-10T00:00:00Z', expires_at: null, last_used_at: null, revoked_at: '2026-09-21T12:00:00Z' },
  )
  await page.reload()
  await expect(page.getByRole('list', { name: 'Agents' })).toBeVisible()
}

test('agents: a person deactivates an agent (its keys go with it), it rests in a folded Deactivated group and can come back', async ({ page }) => {
  const world = await open(page, '/settings/access/agents')
  await withRetirable(page, world)
  const agents = page.getByRole('list', { name: 'Agents' })
  const fold = page.getByRole('button', { name: /^Deactivated/ })
  await expect(fold).toHaveAttribute('aria-expanded', 'false')
  await expect(fold).toContainText('1')
  await expect(page.getByRole('list', { name: 'Deactivated agents' })).toHaveCount(0)
  await expect(agents.getByRole('listitem').filter({ hasText: 'mbp2607 old' })).toHaveCount(0)

  const relic = agents.getByRole('listitem').filter({ hasText: 'relic' })
  await expect(relic).toContainText('1 active key')
  await relic.getByRole('button', { name: 'Actions for relic' }).click()
  await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Deactivate relic?' })
  await expect(dialog).toContainText('Its active key is revoked now; anything using it is refused.')
  await expect(dialog).toContainText('revoked keys stay revoked')
  // Cancelling changes nothing.
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  expect(calls(world, 'POST', /\/deactivate$/)).toHaveLength(0)
  await relic.getByRole('button', { name: 'Actions for relic' }).click()
  await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
  await page.getByRole('dialog', { name: 'Deactivate relic?' }).getByRole('button', { name: 'Revoke keys and deactivate' }).click()
  expect(calls(world, 'POST', new RegExp(`/members/${RELIC}/deactivate$`))).toHaveLength(1)
  await expect(agents.getByRole('listitem').filter({ hasText: 'relic' })).toHaveCount(0)
  await expect(page.getByText('relic is deactivated')).toBeVisible()
  expect(world.keys.find(k => k.id === 'k-relic')?.revoked_at).toBeTruthy()
  await expect(fold).toContainText('2')

  // Folded until asked for; there the agent can come back, with no key until a new one is made.
  await fold.click()
  const resting = page.getByRole('list', { name: 'Deactivated agents' })
  await expect(resting.getByRole('listitem')).toHaveCount(2)
  await expect(resting).toContainText('mbp2607 old')
  await resting.getByRole('button', { name: 'Reactivate relic' }).click()
  expect(calls(world, 'POST', new RegExp(`/members/${RELIC}/reactivate$`))).toHaveLength(1)
  await expect(page.getByText('relic is active again; add a new key to connect it')).toBeVisible()
  await expect(agents.getByRole('listitem').filter({ hasText: 'relic' })).toContainText('0 active keys')
  await expect(resting.getByRole('listitem')).toHaveCount(1)
})

test('agents: a connected computer is not deactivated here; a refusal from the server is said', async ({ page }) => {
  const world = await open(page, '/settings/access/agents')
  await withRetirable(page, world)
  const agents = page.getByRole('list', { name: 'Agents' })
  await agents.getByRole('listitem').filter({ hasText: 'mbp2607' }).getByRole('button', { name: 'Actions for mbp2607' }).click()
  const item = page.getByRole('menuitem', { name: /Deactivate/ })
  await expect(item).toHaveAttribute('aria-disabled', 'true')
  await expect(item).toContainText('Disconnect it on the Agents page first')
  await page.keyboard.press('Escape')
  expect(calls(world, 'POST', /\/deactivate$/)).toHaveLength(0)
  // The server decides: an agent that became a connected computer meanwhile stays active, with the reason.
  world.agents.find(a => a.principal_id === RELIC)!.connected_computer = true
  await agents.getByRole('listitem').filter({ hasText: 'relic' }).getByRole('button', { name: 'Actions for relic' }).click()
  await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
  await page.getByRole('dialog', { name: 'Deactivate relic?' }).getByRole('button', { name: 'Revoke keys and deactivate' }).click()
  await expect(page.getByText('relic stays active: This is a connected computer.')).toBeVisible()
  await expect(agents.getByRole('listitem').filter({ hasText: 'relic' })).toContainText('1 active key')
})

test('agents: deactivating is person-only with Manage members: internal identities and viewers get no action', async ({ page }) => {
  const world = await open(page, '/settings/access/agents', { role: 'viewer' })
  await withRetirable(page, world)
  await expect(page.getByRole('list', { name: 'Agents' })).toContainText('relic')
  await expect(page.getByRole('button', { name: /^Actions for/ })).toHaveCount(0)
  await page.getByRole('button', { name: /^Deactivated/ }).click()
  await expect(page.getByRole('list', { name: 'Deactivated agents' })).toContainText('mbp2607 old')
  await expect(page.getByRole('button', { name: /^Reactivate/ })).toHaveCount(0)
  expect(calls(world, 'POST', /\/(de|re)activate$/)).toHaveLength(0)
})

test('agents: the access log records the deactivation', async ({ page }) => {
  const world = await open(page, '/settings/access/agents')
  await withRetirable(page, world)
  await page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'relic' }).getByRole('button', { name: 'Actions for relic' }).click()
  await page.getByRole('menuitem', { name: 'Deactivate…' }).click()
  await page.getByRole('dialog', { name: 'Deactivate relic?' }).getByRole('button', { name: 'Revoke keys and deactivate' }).click()
  await expect(page.getByText('relic is deactivated')).toBeVisible()
  await page.getByRole('tab', { name: /Access log/ }).click()
  await expect(page.locator('.event').filter({ hasText: 'deactivated relic' }).first()).toBeVisible()
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`agents: the Deactivated group and the actions menu pass axe and fit the phone (${colorScheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 390, height: 844 })
    const world = await open(page, '/settings/access/agents')
    await withRetirable(page, world)
    await page.getByRole('button', { name: /^Deactivated/ }).click()
    await expect(page.getByRole('list', { name: 'Deactivated agents' })).toBeVisible()
    await page.waitForTimeout(100)
    const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.access-card *')].filter(el => {
      const r = el.getBoundingClientRect()
      if (r.width <= 1 || r.height <= 1 || getComputedStyle(el).display === 'none' || el.closest('.tabs')) return false
      return r.left < -0.5 || r.right > innerWidth + 0.5
    }).map(el => `${el.tagName}.${el.className}`))
    expect(cut).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'relic' }).getByRole('button', { name: 'Actions for relic' }).click()
    await expect(page.getByRole('menuitem', { name: 'Deactivate…' })).toBeVisible()
    const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
    expect(result.violations.filter(v => v.impact === 'serious' || v.impact === 'critical').map(v => `${v.id} ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
}

test('the access log reads as sentences and filters by kind, person and words', async ({ page }) => {
  await open(page, '/settings/access/audit')
  const events = page.locator('.event')
  await expect(events).toHaveCount(9)
  await expect(events.first()).toContainText('Markus Barta linked mba (classic) to Markus Barta')
  await expect(page.locator('.event', { hasText: 'changed Jonas Weber’s role on Pharos from Member to Delivery lead' })).toBeVisible()
  await page.getByRole('button', { name: 'Invites' }).click()
  await expect(events).toHaveCount(3)
  await page.getByRole('button', { name: 'Keys' }).click()
  await expect(events).toHaveCount(4)
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await page.getByLabel('Changes by or about').selectOption({ label: 'Mira Holm' })
  await expect(events).toHaveCount(3)
  await page.getByLabel('Find in the access log').fill('Lena')
  await expect(events).toHaveCount(1)
  await expect(page.locator('.audit-tab .summary')).toHaveText('1 of 9 changes')
})

test('can() decides: a member reads, and is offered nothing they may not do', async ({ page }) => {
  await open(page, '/settings/access/people', { role: 'member' })
  await expect(people(page).locator('tr.person')).toHaveCount(6)
  await expect(page.getByRole('button', { name: 'Invite people' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Workspace role of/ })).toHaveCount(0)
  await expect(page.getByRole('tab', { name: /Access log/ })).toHaveCount(0)
  await row(page, 'Mira Holm').getByRole('button', { name: 'Actions for Mira Holm' }).click()
  await expect(page.getByRole('menu').getByRole('menuitem')).toHaveText([/Open/])
  await page.keyboard.press('Escape')
  await page.goto('/settings/access/roles/role-lead')
  await expect(page.getByRole('button', { name: /Duplicate|Delete role/ })).toHaveCount(0)
  await expect(page.getByRole('checkbox').first()).toBeDisabled()
})

test('without See members, Access is not offered at all', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockAccess(page, accessWorld({ role: 'guest' }))
  await page.goto('/settings/access')
  await expect(page.getByRole('heading', { name: 'Access is for people who manage the workspace' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Settings sections' }).getByRole('link', { name: /Access/ })).toHaveCount(0)
})

test('review #3: a duplicate leaves out what I do not hold and never sends it', async ({ page }) => {
  const world = await open(page, '/settings/access/roles/new?from=role-owner', { role: 'admin' })
  await expect(page.locator('.role-editor')).toContainText('Left out of the copy, because you do not hold them: Manage the workspace, Transfer ownership.')
  await page.getByLabel('Name', { exact: true }).fill('Almost owner')
  await page.getByRole('button', { name: 'Create role' }).click()
  await expect(page.locator('.savebar')).toContainText('Saved')
  const sent = calls(world, 'POST', /\/api\/roles$/)[0]!.body.permissions as string[]
  expect(sent).not.toContain('ownership.transfer')
  expect(sent).not.toContain('workspace.manage')
})

test('review #4: an owner’s role changes only with Transfer ownership', async ({ page }) => {
  // Two owners (Mira and Jonas), so neither is the last one; I am an admin.
  await mockWork(page, fixtures())
  const world = accessWorld({ role: 'admin', secondOwner: true })
  world.people.find(p => p.principal_id === JONAS)!.workspace_role = 'role-owner'
  await mockAccess(page, world)
  await page.goto('/settings/access/people')
  await row(page, 'Mira Holm').getByRole('button', { name: /Workspace role of Mira Holm/ }).click()
  const picker = page.getByRole('dialog', { name: 'Role of Mira Holm' })
  await expect(picker).toContainText('Changing an owner’s role needs Transfer ownership, which you do not hold.')
  await expect(picker.getByRole('radio', { name: /^Admin/ })).toHaveClass(/off/)
  await expect(picker.getByRole('button', { name: /^Give/ })).toHaveCount(0)
  await picker.getByRole('button', { name: 'Close' }).click()
  expect(calls(world, 'PUT', /workspace-role$/)).toHaveLength(0)
})

test('review #7: scopes I no longer hold leave the new key and are never sent', async ({ page }) => {
  const world = await open(page, '/settings/access/agents')
  const coordinator = page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'aeon-coordinator' })
  await coordinator.getByRole('button', { name: /active key/ }).click()
  await coordinator.getByRole('button', { name: 'New key' }).click()
  const sheet = page.getByRole('dialog', { name: 'New key for aeon-coordinator' })
  await sheet.getByRole('checkbox', { name: /nodes\.write/ }).check()
  await sheet.getByRole('checkbox', { name: /nodes\.read/ }).check()
  // My own role loses nodes.write; the next access check (window focus) says so.
  const owner = world.roles.find(r => r.id === 'role-owner')!
  owner.permissions = owner.permissions.filter(k => k !== 'nodes.write')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await sheet.getByRole('button', { name: 'Create key' }).click()
  await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
  expect((calls(world, 'POST', /\/agent-keys$/)[0]!.body as { scopes: string[] }).scopes).toEqual(['nodes.read'])
})

test('review r2 #1: a 401 on an Access call revokes every grant at once, without a refetch loop', async ({ page }) => {
  const world = await open(page, '/settings/access/roles/new?from=role-member')
  await page.getByLabel('Name', { exact: true }).fill('Night shift')
  world.sessionEnded = true
  await page.getByRole('button', { name: 'Create role' }).click()
  const card = page.locator('.access-card')
  await expect(card.getByRole('alert').first()).toContainText('Your session has ended, so nothing here can change.')
  await expect(page.locator('.toast', { hasText: 'Your session has ended' })).toBeVisible()
  // Inert, and nothing typed is lost: the editor is read-only with the name kept.
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Night shift')
  await expect(page.getByRole('button', { name: 'Create role' })).toHaveCount(0)
  const asked = () => calls(world, 'GET', /\/api\/me\/permissions/).length
  const before = asked()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await page.waitForTimeout(600)
  expect(asked()).toBeLessThanOrEqual(before + 1) // one focus re-check, then latched again
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Night shift')
})

test('review r2 #2: a join link on screen stays when the session ends', async ({ page }) => {
  const world = await open(page, '/settings/access/invites')
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByLabel('Email').fill('late@studio.at')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  const ready = page.getByRole('dialog', { name: 'Invite ready' })
  const link = await ready.getByRole('textbox').inputValue()
  expect(link).toMatch(/join/)
  world.sessionEnded = true
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.locator('.access-card').getByRole('alert').first()).toContainText('Your session has ended')
  await expect(ready.getByRole('textbox')).toHaveValue(link)
  await expect(page.getByRole('tablist', { name: 'Access' })).toBeVisible()
  await ready.getByRole('button', { name: 'Done' }).click()
  await expect(page.locator('.access-card')).toBeVisible()
})

test('an ended session keeps a one-time join link copyable', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const world = await open(page, '/settings/access/invites')
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByLabel('Email').fill('late@studio.at')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  const ready = page.getByRole('dialog', { name: 'Invite ready' })
  const link = await ready.getByRole('textbox').inputValue()
  world.sessionEnded = true
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(ready.getByRole('textbox')).toHaveValue(link)
  await expect(ready.getByRole('button', { name: 'Sign in again' })).toBeEnabled()
  await ready.getByRole('button', { name: 'Copy link' }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(link)
})

test('review r3 #1: another person signing in never sees the previous session’s access data', async ({ page }) => {
  const first = await open(page, '/settings/access/people')
  await expect(people(page)).toContainText('Jonas Weber')
  // Sign out, and Mira signs in to another workspace with a smaller directory.
  const second = accessWorld({ secondOwner: true })
  second.me = MIRA
  second.people = second.people.filter(p => p.principal_id === MIRA || p.principal_id === LENA)
  await mockAccess(page, second)
  await page.route(/\/api\/me$/, route => route.fulfill({ json: { principal: { id: MIRA, name: 'Mira Holm', kind: 'person', roles: [] }, tenant: { id: 't2', name: 'Agentur K' } } }))
  await page.locator('.section-nav').getByRole('link', { name: /^Personal/ }).click()
  await page.locator('.section-nav').getByRole('link', { name: /^Access/ }).click()
  await expect(people(page)).toContainText('Lena Graf')
  await expect(people(page)).not.toContainText('Jonas Weber')
  expect(calls(second, 'GET', /\/api\/members$/).length).toBeGreaterThan(0)
  void first
})

test('review r3 #2: open dialogs keep their draft but cannot submit once the permission is gone', async ({ page }) => {
  const world = await open(page, '/settings/access/invites')
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByLabel('Email').fill('draft@studio.at')
  const owner = world.roles.find(r => r.id === 'role-owner')!
  owner.permissions = owner.permissions.filter(k => k !== 'members.manage')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(sheet.getByRole('alert')).toContainText('You no longer have Manage members, so this cannot be saved.')
  await expect(sheet.getByRole('button', { name: 'Create invite link' })).toBeDisabled()
  await expect(sheet.getByLabel('Email')).toHaveValue('draft@studio.at')
  expect(calls(world, 'POST', /\/members\/invites$/)).toHaveLength(0)
  await sheet.getByRole('button', { name: 'Cancel' }).click()

  await page.goto('/settings/access/agents')
  const coordinator = page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'aeon-coordinator' })
  await coordinator.getByRole('button', { name: /active key/ }).click()
  await coordinator.getByRole('button', { name: 'New key' }).click()
  const keySheet = page.getByRole('dialog', { name: 'New key for aeon-coordinator' })
  await keySheet.getByRole('checkbox', { name: /nodes\.read/ }).check()
  owner.permissions = owner.permissions.filter(k => k !== 'keys.manage')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(keySheet.getByRole('button', { name: 'Create key' })).toBeDisabled()
  await expect(keySheet).toContainText('You no longer have Manage agent keys')
  await expect(keySheet.getByRole('checkbox', { name: /nodes\.read/ })).toBeChecked()
  expect(calls(world, 'POST', /\/agent-keys$/)).toHaveLength(0)
})

test('review r4 #1: on a project only its project-grantable permissions must be mine', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = accessWorld({ role: 'admin' })
  // workspace.manage is workspace-only: it does not count on a project, so an admin may give this role there.
  world.roles.push({ id: 'role-wide', key: 'wide-lead', name: 'Wide lead', description: 'Leads a project; also appoints owners.', builtin: false, permissions: ['nodes.read', 'nodes.write', 'workspace.manage'], based_on: null })
  await mockAccess(page, world)
  await page.goto('/settings/access/projects/p-pharos')
  const list = page.getByRole('list', { name: 'People on Pharos' })
  await list.getByRole('listitem').filter({ hasText: 'Jonas Weber' }).getByRole('button', { name: /Delivery lead/ }).click()
  const picker = page.getByRole('dialog', { name: 'Role of Jonas Weber on Pharos' })
  await expect(picker.getByRole('radio', { name: /^Wide lead/ })).not.toHaveClass(/off/)
  await picker.getByRole('radio', { name: /^Wide lead/ }).click()
  await picker.getByRole('button', { name: 'Give Jonas Weber Wide lead on Pharos' }).click()
  await expect(list.getByRole('listitem').filter({ hasText: 'Jonas Weber' })).toContainText('Wide lead')
  expect(calls(world, 'PUT', /\/projects\/p-pharos\/members\//).at(-1)!.body).toEqual({ role_id: 'role-wide' })
})

test('review r4 #3: a sheet cannot be dismissed while its one-time link is being made', async ({ page }) => {
  const world = await open(page, '/settings/access/invites')
  world.slow = 900
  await page.getByRole('button', { name: 'Invite people' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await sheet.getByLabel('Email').fill('slow@studio.at')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(sheet.getByRole('button', { name: 'Cancel' })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(sheet).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Invite ready' }).getByRole('textbox')).toHaveValue(/join/)
})

test('review r4 #4: the access log alone opens Access, without the member data', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.roles.push({ id: 'role-auditor', key: 'auditor', name: 'Auditor', description: 'Reads the access log.', builtin: false, permissions: ['audit.read'], based_on: null })
  world.people.find(p => p.principal_id === ME)!.workspace_role = 'role-auditor'
  await mockAccess(page, world)
  await page.goto('/settings')
  await page.locator('.section-nav').getByRole('link', { name: /^Access/ }).click()
  await expect(page.getByRole('tab')).toHaveText([/Access log/])
  await expect(page.locator('.event').first()).toBeVisible()
  expect(calls(world, 'GET', /\/api\/members$/)).toHaveLength(0)
})

test('tabs are a tablist: arrows move between them', async ({ page }) => {
  await open(page)
  await page.getByRole('tab', { name: /People/ }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page).toHaveURL('/settings/access/invites')
  await expect(page.getByRole('tab', { name: /Invites/ })).toBeFocused()
  await page.keyboard.press('End')
  await expect(page).toHaveURL('/settings/access/audit')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: people, a role, the invite form, project access and the log in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1280, height: 900 })
    const scan = async (what: string) => {
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      expect(result.violations.filter(v => v.impact === 'serious' || v.impact === 'critical').map(v => `${what}: ${v.id} ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
    }
    await open(page)
    await scan('people')
    await page.getByRole('button', { name: /Imported from classic/ }).click()
    await row(page, 'Jonas Weber').getByRole('button', { name: /Workspace role of Jonas Weber/ }).click()
    await page.getByRole('radio', { name: /^Admin/ }).click()
    await scan('role picker')
    await page.keyboard.press('Escape')
    await page.goto('/settings/access/roles/new?from=role-member')
    await expect(page.getByLabel('Name', { exact: true })).toBeVisible()
    await scan('role composer')
    await page.goto('/settings/access/invites')
    await page.getByRole('button', { name: 'Invite people' }).click()
    await expect(page.getByRole('dialog', { name: 'Invite people' })).toBeVisible()
    await scan('invite')
    await page.goto(`/settings/access/people/${ME}`)
    await expect(page.getByRole('dialog', { name: 'Markus Barta, access' })).toBeVisible()
    await scan('person')
    await page.goto('/settings/access/projects/p-pharos')
    await expect(page.getByRole('list', { name: 'People on Pharos' })).toBeVisible()
    await scan('project access')
    await page.goto('/settings/access/audit')
    await expect(page.locator('.event').first()).toBeVisible()
    await scan('access log')
  })
}

for (const colorScheme of ['light', 'dark'] as const) {
  test(`390: every Access tab fits the phone (${colorScheme})`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await open(page)
    for (const tab of ['people', 'invites', 'roles', 'roles/role-lead', 'projects', 'projects/p-pharos', 'agents', 'audit']) {
      await page.goto(`/settings/access/${tab}`)
      await expect(page.locator('.access-card .panel > :not(.set-skeleton)').first()).toBeVisible()
      await page.waitForTimeout(100)
      const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.access-card *')].filter(el => {
        const r = el.getBoundingClientRect()
        if (r.width <= 1 || r.height <= 1 || getComputedStyle(el).display === 'none') return false
        if (el.closest('.tabs')) return false
        return r.left < -0.5 || r.right > innerWidth + 0.5
      }).map(el => `${el.tagName}.${el.className}`))
      expect(cut, tab).toEqual([])
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), tab).toBe(true)
    }
  })
}
