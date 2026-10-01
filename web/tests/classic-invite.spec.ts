// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { accessWorld, mockAccess } from './access-fixtures'

test('an imported colleague can be invited with their email and classic role', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.imported[0]!.email = 'classic@example.com'
  world.imported[0]!.classic_role = 'admin'
  await mockAccess(page, world)
  await page.goto('/settings/access/people')
  await page.getByRole('button', { name: /Imported from classic, no sign-in/ }).click()
  await page.getByRole('button', { name: 'Invite jw (classic)' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await expect(sheet.getByLabel('Email', { exact: true })).toHaveValue('classic@example.com')
  await expect(sheet.getByLabel('Workspace role', { exact: true })).toHaveValue('role-admin')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(page.getByRole('dialog', { name: 'Invite ready' })).toBeVisible()
  expect(world.calls.find(c => c.method === 'POST' && c.path === '/api/members/invites')?.body).toMatchObject({ email: 'classic@example.com', workspace_role_id: 'role-admin' })
})

test('an imported role outside my authority needs an explicit allowed choice', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = accessWorld({ role: 'admin' })
  world.imported[0]!.email = 'owner@example.com'
  world.imported[0]!.classic_role = 'super_admin'
  await mockAccess(page, world)
  await page.goto('/settings/access/people')
  await page.getByRole('button', { name: /Imported from classic, no sign-in/ }).click()
  await page.getByRole('button', { name: 'Invite jw (classic)' }).click()
  const sheet = page.getByRole('dialog', { name: 'Invite people' })
  await expect(sheet.getByLabel('Workspace role', { exact: true })).toHaveValue('')
  await sheet.getByRole('button', { name: 'Create invite link' }).click()
  await expect(sheet.getByText('Give a workspace role or at least one project, or they could not see anything.')).toBeVisible()
  expect(world.calls.filter(c => c.method === 'POST' && c.path === '/api/members/invites')).toHaveLength(0)
})

test('a member cannot invite an imported colleague', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockAccess(page, accessWorld({ role: 'member' }))
  await page.goto('/settings/access/people')
  await page.getByRole('button', { name: /Imported from classic, no sign-in/ }).click()
  await expect(page.getByRole('button', { name: 'Invite jw (classic)' })).toHaveCount(0)
})

test('verified imported-account sign-in explains the invite and preserves ambiguous linking', async ({ page }) => {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/me') return route.fulfill({ status: 401, json: { error: 'unauthorized', dev_mode: false } })
    if (path === '/api/version') return route.fulfill({ json: { version: '260924020145.0.0', scheme: 'inspr-calendar-v2' } })
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.goto('/signin?error=imported_account')
  const alert = page.getByRole('alert')
  await expect(alert).toContainText('Your earlier PMA account was found; ask an admin to invite you.')
  await expect(alert).toContainText('If one imported account matches your verified email')
  await alert.getByRole('button', { name: 'Dismiss' }).click()
  await expect(alert).toHaveCount(0)
})
