// SPDX-License-Identifier: AGPL-3.0-only
// QA4 (AEON-140): a session that ends while someone edits keeps what they typed
// and offers sign-in in a new tab, instead of the bare word "unauthorized".
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

const mod = process.platform === 'darwin' ? 'Meta' : 'Control'

for (const width of [1440, 390]) {
  test(`at ${width}px a save after the session ended keeps the draft and offers sign-in`, async ({ page, context }) => {
    await page.setViewportSize({ width, height: 900 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/PHAROS-12')
    const ws = page.getByRole('complementary', { name: 'Ticket details' })
    await ws.getByRole('button', { name: 'Add a description' }).click()
    const area = ws.getByLabel('Description, Markdown')
    await area.fill('Written while the session ends.')
    // The session ends: writes answer 401 from now on.
    await page.route('**/api/nodes/*', route => route.request().method() === 'PATCH' ? route.fulfill({ status: 401, json: { error: 'unauthorized' } }) : route.fallback())
    await page.keyboard.press(`${mod}+Enter`)
    const recovery = page.getByText('Your session has ended. Sign in again in a new tab; what you typed stays on this page.')
    await expect(recovery).toBeVisible()
    await expect(page.getByText(/was not saved: your session has ended/)).toBeVisible()
    await expect(page.getByText(/unauthorized/)).toHaveCount(0)
    await expect(area).toHaveValue('Written while the session ends.')
    const opened = context.waitForEvent('page')
    await page.getByRole('button', { name: 'Sign in' }).click()
    expect(new URL((await opened).url()).pathname + new URL((await opened).url()).search).toBe('/signin?error=expired')
    await expect(area).toHaveValue('Written while the session ends.')
  })
}

// Risk: a shared cookie must not let a later person read or submit the expired
// tab's local draft. Exercise separate tabs and then an explicit same-tab login.
test('AEON-1313: a different person cannot receive or submit an expired tab’s draft', async ({ page, context }) => {
  const data = fixtures(), errors = watchErrors(page)
  const original = data.nodes.find(node => node.key === 'PHAROS-12')!
  const draft = 'Private draft belonging to the original person.'
  let authenticated = true
  let person = { ...me }
  const mira = data.people.find(candidate => candidate.id !== me.id)!
  const sessionRoutes = async (tab: Page) => {
    await tab.route('**/api/me', route => authenticated
      ? route.fulfill({ json: { principal: { ...person, kind: 'person', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' }, dev_mode: true } })
      : route.fulfill({ status: 401, json: { dev_mode: true } }))
    await tab.route('**/api/auth/dev-login', route => {
      person = { id: mira.id, name: mira.name }; authenticated = true
      return route.fulfill({ status: 204 })
    })
  }
  await mockWork(page, data)
  await sessionRoutes(page)
  const expiredWrites: { body: unknown; revision: string | undefined }[] = []
  await page.route('**/api/nodes/n-2', route => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    authenticated = false
    expiredWrites.push({ body: route.request().postDataJSON(), revision: route.request().headers()['if-unmodified-since'] })
    return route.fulfill({ status: 401, json: { error: 'unauthorized' } })
  })
  await page.goto('/p/PHAROS/PHAROS-12')
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await panel.getByRole('button', { name: 'Add a description' }).click()
  const area = panel.getByLabel('Description, Markdown')
  await area.fill(draft)
  await panel.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByText(/was not saved: your session has ended/)).toBeVisible()
  await expect(area).toHaveValue(draft)
  expect(expiredWrites).toEqual([{ body: { body: draft }, revision: original.updated_at }])
  await page.locator('.toast').filter({ hasText: /was not saved: your session has ended/ }).getByRole('button', { name: 'Dismiss' }).click()
  const revokedRequests: string[] = []
  page.on('request', request => { if (new URL(request.url()).pathname.startsWith('/api/')) revokedRequests.push(request.url()) })

  // Existing ownership fixtures: tabs share one origin and a mutable session,
  // while each document runs its own real authentication-generation follower.
  const other = await context.newPage()
  const otherErrors = watchErrors(other), otherCalls = await mockWork(other, data)
  await sessionRoutes(other)
  await other.goto('/signin?error=expired&return=/p/PHAROS/PHAROS-12')
  await other.getByLabel('Email address').fill('mira@example.test')
  await other.getByRole('button', { name: 'Continue with email' }).click()
  await expect(other).toHaveURL('/p/PHAROS/PHAROS-12')
  const otherPanel = other.getByRole('complementary', { name: 'Ticket details' })
  await otherPanel.getByRole('button', { name: 'Add a description' }).click()
  const otherArea = otherPanel.getByLabel('Description, Markdown')
  await expect(otherArea).toHaveValue('')
  await otherArea.fill('Mira’s own description.')
  await otherPanel.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(otherPanel.getByRole('region', { name: 'Description' })).toContainText('Mira’s own description.')
  expect(otherCalls.filter(call => call.method === 'PATCH').map(call => [call.path, call.body])).toEqual([['/api/nodes/n-2', { body: 'Mira’s own description.' }]])
  await expect(area).toHaveValue(draft)

  // The shell freezes controls. Await the real API boundary too, so neither
  // protected operation can reach the network using the new person's cookie.
  await expect(area).toHaveJSProperty('readOnly', true)
  await expect(panel.getByRole('button', { name: 'Save', exact: true })).toBeDisabled()
  expect(await page.evaluate(async () => {
    // @ts-expect-error Vite serves the actual API module in this document.
    const { api } = await import('/src/lib/api.ts')
    const read = await api('/nodes/n-2')
    const write = await api('/nodes/n-2', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body: 'Private draft belonging to the original person.' }) })
    return { read: read.status, write: write.status }
  })).toEqual({ read: 401, write: 401 })
  expect(expiredWrites).toHaveLength(1)
  expect(revokedRequests).toEqual([])
  expect(original.body).toBe('Mira’s own description.')
  await expect(area).toHaveValue(draft)

  // A deliberate sign-in in this document replaces its recovery context too.
  await page.evaluate(async () => {
    // @ts-expect-error Vite serves the actual session store in this document.
    const { useSession } = await import('/src/stores/session.ts')
    await useSession().devLogin('mira@example.test')
    await useSession().refresh()
  })
  await expect(area).toHaveCount(0)
  await expect(panel.getByRole('region', { name: 'Description' })).toContainText('Mira’s own description.')
  await panel.getByRole('button', { name: 'Edit description' }).click()
  await expect(area).toHaveValue('Mira’s own description.')
  expect(errors).toEqual([])
  expect(otherErrors).toEqual([])
  await other.close()
})
