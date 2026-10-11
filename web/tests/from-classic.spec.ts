// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'

const identity = { principal: { id: 'person-1', name: 'Example Person' }, tenant: { id: 'tenant-1', name: 'Studio' } }

async function mockAPI(page: Page, initiallySignedIn = true) {
  let signedIn = initiallySignedIn
  const requested: string[] = []
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/me') return route.fulfill({ status: signedIn ? 200 : 401, json: signedIn ? { ...identity, dev_mode: true } : { dev_mode: true } })
    if (url.pathname === '/api/auth/dev-login') { signedIn = true; return route.fulfill({ status: 204 }) }
    if (url.pathname === '/api/from-classic') {
      const path = url.searchParams.get('path') ?? ''
      requested.push(path)
      return route.fulfill({ json: path.startsWith('/hours/')
        ? { path: '/business/hours' }
        : { path: '/', notice: 'That old link has moved. We brought you to the closest available page.' } })
    }
    if (url.pathname === '/api/version') return route.fulfill({ json: { version: '260926120000.0.0', scheme: 'inspr-calendar-v2' } })
    if (url.pathname === '/api/projects') return route.fulfill({ json: { items: [] } })
    if (url.pathname === '/api/nodes' || url.pathname === '/api/kinds' || url.pathname === '/api/views') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (url.pathname === '/api/events/stream') return route.fulfill({ contentType: 'text/event-stream', body: ': heartbeat\n\n' })
    return route.fulfill({ status: 404, json: {} })
  })
  return requested
}

test('known and unknown classic links resolve through the API', async ({ page }) => {
  const requests = await mockAPI(page)
  await page.goto('/from-classic/hours/week?week=2026-09-21#calendar')
  await expect(page).toHaveURL('/business/hours')
  expect(requests).toEqual(['/hours/week?week=2026-09-21'])
  await page.goto('/from-classic/forgotten/place')
  await expect(page).toHaveURL('/')
  await expect(page.locator('.toast').filter({ hasText: 'That old link has moved' })).toContainText('That old link has moved')
})

test('sign-in returns to the classic resolver before opening its destination', async ({ page }) => {
  const requests = await mockAPI(page, false)
  await page.goto('/from-classic/hours/project?project=7')
  await expect(page).toHaveURL('/signin')
  expect(requests).toEqual([])
  await page.getByLabel('Email address').fill('person@example.test')
  await page.getByRole('button', { name: 'Continue with email' }).click()
  await expect(page).toHaveURL('/business/hours')
  expect(requests).toEqual(['/hours/project?project=7'])
})
