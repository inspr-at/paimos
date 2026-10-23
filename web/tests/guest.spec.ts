// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { mockGuestPermissions } from './authz-fixtures'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

const ticket = '/p/PHAROS/PHAROS-12'
const details = (page: import('@playwright/test').Page) => page.getByRole('complementary', { name: 'Ticket details' })

// ADR-003 P2: a guest bound to one project has no workspace permissions. The
// server already filters what it sees; the app must still load, read the
// project and let the guest comment, and treat workspace areas that answer
// 403 (members, project groups, plugins, people) as simply not there.
async function asGuest(page: import('@playwright/test').Page) {
  const calls = await mockWork(page, fixtures(), { readOnly: true })
  const forbidden = { status: 403, json: { error: 'permission denied', code: 'forbidden', reason: 'This action needs a permission you do not hold' } }
  for (const path of ['**/api/members*', '**/api/project-groups*', '**/api/plugins*', '**/api/business/principals*', '**/api/agent-keys*', '**/api/roles*']) {
    await page.route(path, route => route.fulfill(forbidden))
  }
  await page.route('**/api/me/permissions*', route => {
    const projectId = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    return route.fulfill({ json: mockGuestPermissions(projectId, ['p-pharos']) })
  })
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'person', roles: [] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  return calls
}

test('P08: a project guest opens work, posts an owned comment and has no edit or admin controls', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await asGuest(page)
  await page.goto('/')
  await expect(page.getByRole('link', { name: /Pharos/ }).first()).toBeVisible()
  await expect(page.getByText(/something went wrong/i)).toHaveCount(0)
  await page.goto(ticket)
  const panel = details(page)
  await expect(panel.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  const composer = panel.getByLabel('Add a comment')
  await expect(composer).toBeEnabled()
  await composer.fill('Guest review: **ready**.')
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
  await composer.press(mac ? 'Meta+Enter' : 'Control+Enter')
  const comment = panel.locator('.entry.comment').filter({ hasText: 'Guest review:' })
  await expect(comment.locator('strong').last()).toHaveText('ready')
  await expect(comment).toContainText(me.name)
  await expect(composer).toHaveValue('')
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/comments'))).toMatchObject([
    { path: '/api/nodes/n-2/comments', body: { body_markdown: 'Guest review: **ready**.' } },
  ])
  await expect(panel.getByRole('button', { name: /^(?:Edit|Add a) description$/ })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: 'Delete ticket', exact: true })).toHaveCount(0)
  await page.goto('/settings/access')
  await expect(page.getByRole('heading', { name: 'Access is for people who manage the workspace', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Access', exact: true })).toHaveCount(0)
  await expect(page.getByRole('list', { name: 'People', exact: true })).toHaveCount(0)
  expect(calls.filter(call => ['PATCH', 'PUT', 'DELETE'].includes(call.method) && !call.path.startsWith('/api/preferences/'))).toEqual([])
  expect(errors).toEqual([])
})
