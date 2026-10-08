// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { controlStability } from './control-stability'

const canonical = '260923120000.0.0'
// The shared renderer's accessible name: canonical version and its UTC date-time (INSPR-CalVer3).
const versionName = `${canonical} · 2026-09-23 12:00:00 UTC`
const identity = { principal: { id: 'person-1', name: 'Markus Barta', email: 'markus@barta.com' }, tenant: { id: 'tenant-1', name: 'INSPR Studio' } }
const projects = [
  { id: 'p1', key: 'PRJ-1', title: 'Bakery pickup orders', state: 'active', open: 12, in_progress: 3, done: 20, total: 35, last_activity: '2026-09-23T10:00:00Z' },
  { id: 'p2', key: 'PRJ-2', title: 'Clinic intake forms', state: 'active', open: 4, in_progress: 0, done: 1, total: 5, last_activity: '2026-09-20T10:00:00Z' },
]
const projectNodes = projects.map(p => ({ id: p.id, key: p.key, title: p.title, body: '', state: p.state, kind_slug: 'project', fields: { classic: { key: p.id === 'p1' ? 'BAKE' : 'CLINIC', description: 'A small studio project.' } } }))

// `gate` holds the project responses until the test releases them, so a spec can
// observe (and then wait out) the list's loading state instead of racing it.
async function mockAPI(page: Page, options: { signedIn?: boolean; auth?: { signedIn: boolean }; devMode?: boolean; version?: string; codename?: string; sessionFailure?: boolean; logoutFailure?: boolean; loginFailure?: boolean; gate?: Promise<void> } = {}) {
  let signedIn = options.signedIn ?? true
  const calls: { path: string; method: string; body: string | null }[] = []
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    calls.push({ path, method: request.method(), body: request.postData() })
    if (path === '/api/kinds' || path === '/api/views') return route.fulfill({ json: { items: [] } })
    if (path === '/api/projects') {
      await options.gate
      return route.fulfill({ json: { items: projects } })
    }
    if (path === '/api/nodes') {
      await options.gate
      return route.fulfill({ json: { items: projectNodes, next_cursor: null } })
    }
    if (path === '/api/nodes/tree') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (path === '/api/events/stream') return route.fulfill({ contentType: 'text/event-stream', body: ': heartbeat\n\n' })
    if (path === '/api/version') return route.fulfill({ json: { version: options.version ?? canonical, scheme: 'inspr-calendar-v2', ...(options.codename ? { codename: options.codename } : {}) } })
    if (path === '/api/me') {
      if (options.sessionFailure) return route.fulfill({ status: 503, json: { error: 'Unavailable' } })
      const live = options.auth?.signedIn ?? signedIn
      return route.fulfill({ status: live ? 200 : 401, json: { ...(live ? identity : { error: 'Unauthorized' }), dev_mode: options.devMode ?? false } })
    }
    if (path === '/api/auth/logout') {
      if (options.logoutFailure) return route.fulfill({ status: 503, json: { error: 'Unavailable' } })
      signedIn = false
      if (options.auth) options.auth.signedIn = false
      return route.fulfill({ status: 204 })
    }
    if (path === '/api/auth/dev-login') {
      if (options.loginFailure) return route.fulfill({ status: 403, json: { error: 'Forbidden' } })
      signedIn = true
      if (options.auth) options.auth.signedIn = true
      return route.fulfill({ status: 204 })
    }
    if (path === '/api/auth/login') return route.fulfill({ contentType: 'text/html', body: '<h1>INSPR sign-in</h1>' })
    return route.fulfill({ status: 404, json: {} })
  })
  return calls
}

async function noOverflow(page: Page) {
  expect(await page.evaluate(() => {
    const main = document.querySelector('main')!
    return {
      documentX: document.documentElement.scrollWidth > innerWidth,
      documentY: document.documentElement.scrollHeight > innerHeight,
      mainY: main.scrollHeight > main.clientHeight + 1,
      mainX: main.scrollWidth > main.clientWidth + 1,
    }
  })).toEqual({ documentX: false, documentY: false, mainY: false, mainX: false })
}

for (const viewport of [{ width: 1280, height: 720 }, { width: 390, height: 844 }]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    for (const screen of ['home', 'signin', 'signin-dev', '404'] as const) {
      test(`${screen} ${viewport.width} ${colorScheme}`, async ({ page }, testInfo) => {
        const errors: string[] = []
        page.on('pageerror', error => errors.push(error.message))
        await page.setViewportSize(viewport)
        await page.emulateMedia({ colorScheme })
        await mockAPI(page, { signedIn: screen === 'home' || screen === '404', devMode: screen === 'signin-dev' })
        await page.goto(screen === '404' ? '/missing' : screen.startsWith('signin') ? '/signin' : '/')
        await expect(page.locator('h1')).toBeVisible()
        // Sign-in's card carries the copyable version; signed in, the footer bar's pill opens the release history.
        if (screen.startsWith('signin')) await expect(page.locator('footer [data-version-view="pretty"]')).toBeVisible()
        else await expect(page.locator('footer.app-footer .version-pill .calendar-version[role="img"]')).toHaveAttribute('aria-label', versionName)
        // Skeleton rows overflow the shell; measure the loaded list, not the loading state.
        if (screen === 'home') await expect(page.getByRole('list', { name: 'Projects' }).getByRole('link')).toHaveCount(2)
        await page.evaluate(() => document.fonts.ready)
        await noOverflow(page)
        // Sign-in is a bare page: no header, the card carries brand, version and theme.
        if (screen.startsWith('signin')) await expect(page.locator('.app-header')).toHaveCount(0)
        else expect(await page.locator('.app-header').evaluate(el => el.getBoundingClientRect().height)).toBe(56)
        // Phones get 44px touch targets; a desktop pointer works with the compact rail.
        const min = viewport.width < 600 ? 44 : 20
        // Visible controls can change as the shell settles. Snapshot their
        // rectangles together instead of resolving stale nth() locators later.
        const controls = await page.locator('button:visible, a:visible:not(.skip-link), input:visible, [role="button"]:visible').evaluateAll(elements => elements.map(el => {
          const target = el.closest('label.switch') ?? el
          const { height, width } = target.getBoundingClientRect()
          return { height, width }
        }))
        for (const bounds of controls) {
          expect(bounds.height).toBeGreaterThanOrEqual(min)
          expect(bounds.width).toBeGreaterThanOrEqual(min)
        }
        await page.screenshot({ path: testInfo.outputPath(`${screen}-${viewport.width}-${colorScheme}.png`), fullPage: true })
        expect(errors).toEqual([])
      })
    }
  }
}

for (const width of [390, 1024, 1440]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`home attention entry fits and stays still at ${width}px in ${colorScheme}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 844 })
      await page.emulateMedia({ colorScheme })
      // Branch gate holds project and node payloads. Main holds /api/projects one
      // layer above that so the header link renders before either response, on
      // every run. Release both before measuring the loaded list: skeleton rows
      // are taller than the shell and are not the layout under test.
      let release!: () => void
      const gate = new Promise<void>(resolve => { release = resolve })
      await mockAPI(page, { gate })
      let releaseProjects!: () => void
      const projectsGate = new Promise<void>(resolve => { releaseProjects = resolve })
      await page.route(/\/api\/projects(?:\?|$)/, async route => { await projectsGate; await route.fallback() })
      const projectsRequested = page.waitForRequest(request => new URL(request.url()).pathname === '/api/projects')
      await page.goto('/')
      await projectsRequested
      const loading = page.getByRole('status', { name: 'Loading projects' })
      await expect(loading).toBeVisible()
      const attention = page.getByRole('link', { name: 'Needs attention', exact: true })
      await expect(attention).toHaveAttribute('href', '/tickets?view=needs-attention')
      await expect(page.getByRole('status', { name: 'Loading projects' })).toBeVisible()
      const projectsLoaded = page.waitForResponse(response => response.ok() && new URL(response.url()).pathname === '/api/projects')
      releaseProjects()
      release()
      await projectsLoaded
      await expect(page.getByRole('list', { name: 'Projects' }).getByRole('link')).toHaveCount(2)
      await expect(page.getByRole('list', { name: 'Projects' }).getByRole('listitem')).toHaveCount(projects.length)
      await expect(loading).toHaveCount(0)
      await expect(page.getByRole('status', { name: 'Loading projects' })).toHaveCount(0)
      await page.evaluate(() => document.fonts.ready)
      await noOverflow(page)
      const bounds = await attention.boundingBox()
      expect(bounds!.height).toBeGreaterThanOrEqual(width === 390 ? 44 : 20)
      expect(bounds!.width).toBeGreaterThanOrEqual(44)
      const guard = await controlStability(page, {
        attention,
        search: page.getByRole('searchbox', { name: 'Filter projects' }),
        display: page.getByRole('button', { name: /^Display/ }),
      })
      await guard.check(async () => { await attention.focus(); await attention.hover() })
      guard.done()
      await page.screenshot({ path: testInfo.outputPath(`home-attention-${width}-${colorScheme}.png`), fullPage: true })
    })
  }
}

test('401 redirects, production hides email sign-in, OIDC navigates to login', async ({ page }) => {
  await mockAPI(page, { signedIn: false })
  await page.goto('/')
  await expect(page).toHaveURL('/signin')
  await expect(page.getByLabel('Email address')).toHaveCount(0)
  await page.getByRole('link', { name: 'Sign in', exact: true }).click()
  await expect(page).toHaveURL('/api/auth/login')
})

test('server-authorized email login and account logout use POST', async ({ page }) => {
  const calls = await mockAPI(page, { signedIn: false, devMode: true })
  await page.goto('/signin')
  await page.getByLabel('Email address').fill('markus@barta.com')
  await page.getByRole('button', { name: 'Continue with email' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  await page.getByRole('button', { name: 'Account for Markus Barta' }).click()
  // Focus lands on the first item of the account menu.
  await expect(page.getByRole('menu', { name: 'Account' }).getByRole('menuitem', { name: 'Personal settings' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: 'Account for Markus Barta' })).toBeFocused()
  await page.keyboard.press('Enter')
  await page.getByRole('menuitem', { name: 'Sign out', exact: true }).click()
  await expect(page).toHaveURL('/signin')
  expect(calls).toContainEqual({ path: '/api/auth/dev-login', method: 'POST', body: JSON.stringify({ email: 'markus@barta.com' }) })
  expect(calls).toContainEqual({ path: '/api/auth/logout', method: 'POST', body: null })
})

test('an expired session cannot navigate into another protected view', async ({ page }) => {
  const auth = { signedIn: true }
  const calls = await mockAPI(page, { auth, devMode: true })
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  auth.signedIn = false
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/settings/personal')))
  await expect(page).toHaveURL(/\/signin\?error=expired&return=\/settings\/personal/)
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Personal' })).toHaveCount(0)
  expect(calls.filter(call => call.path === '/api/me').length).toBeGreaterThanOrEqual(2)
  await page.getByLabel('Email address').fill('markus@barta.com')
  await page.getByRole('button', { name: 'Continue with email' }).click()
  await expect(page).toHaveURL('/settings/personal')
})

test('a revoked session is checked on the next navigation', async ({ page }) => {
  const auth = { signedIn: true }
  await mockAPI(page, { auth })
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  auth.signedIn = false
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/agents')))
  await expect(page).toHaveURL(/\/signin\?error=expired&return=\/agents/)
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toHaveCount(0)
})

test('OIDC sign-in resumes the saved path after the full-page callback', async ({ page }) => {
  const auth = { signedIn: false }
  await mockAPI(page, { auth })
  await page.goto('/signin?error=expired&return=/agents')
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
  await page.getByRole('link', { name: 'Sign in', exact: true }).click()
  await expect(page).toHaveURL('/api/auth/login')
  auth.signedIn = true
  await page.goto('/') // The OIDC callback returns home with its new session.
  await expect(page).toHaveURL('/agents')
})

test('a raw api 401 clears identity but keeps the current view until navigation', async ({ page }) => {
  const calls = await mockAPI(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  await page.route('**/api/raw-revocation', route => route.fulfill({ status: 401, json: { error: 'unauthorized' } }))
  expect(await page.evaluate(() => import('/src/lib/api.ts').then(async ({ api }) => (await api('/raw-revocation')).status))).toBe(401)
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  await expect(page.locator('.session-ended')).toContainText('Your session has ended')
  expect(await page.evaluate(() => import('/src/stores/session.ts').then(({ useSession }) => useSession().identity))).toBeNull()
  const before = calls.length
  expect(await page.evaluate(() => import('/src/lib/api.ts').then(async ({ api }) => (await api('/kinds')).status))).toBe(401)
  expect(calls.length).toBe(before)
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/agents')))
  await expect(page).toHaveURL('/signin?error=expired&return=/agents')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toHaveCount(0)
})

test('a raw api 401 also leaves a public route in place until protected navigation', async ({ page }) => {
  await mockAPI(page)
  await page.goto('/offers/studio/token')
  await expect(page).toHaveURL('/offers/studio/token')
  await expect.poll(() => page.evaluate(() => import('/src/router.ts').then(({ router }) => router.currentRoute.value.fullPath))).toBe('/offers/studio/token')
  await page.route('**/api/raw-revocation', route => route.fulfill({ status: 401, json: { error: 'unauthorized' } }))
  expect(await page.evaluate(() => import('/src/lib/api.ts').then(async ({ api }) => (await api('/raw-revocation')).status))).toBe(401)
  await expect(page).toHaveURL('/offers/studio/token')
  await expect(page.locator('.session-ended')).toContainText('Your session has ended')
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/agents')))
  await expect(page).toHaveURL('/signin?error=expired&return=/agents')
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
})

test('sign-out then Back never restores a protected page from cache', async ({ page }) => {
  await mockAPI(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/agents')))
  await expect(page).toHaveURL('/agents')
  await page.getByRole('button', { name: 'Account for Markus Barta' }).click()
  await page.getByRole('menuitem', { name: 'Sign out', exact: true }).click()
  await expect(page).toHaveURL(/\/signin/)
  await page.goBack()
  await expect(page).toHaveURL(/\/signin/)
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toHaveCount(0)
})

test('session outages show retry rather than authenticated content', async ({ page }) => {
  await mockAPI(page, { sessionFailure: true })
  await page.goto('/')
  await expect(page.getByRole('alert')).toContainText('couldn’t reach your workspace')
  await expect(page.getByRole('heading', { name: 'Projects' })).toHaveCount(0)
  await page.unroute('**/api/**')
  await mockAPI(page)
  await page.getByRole('button', { name: 'Try again' }).click()
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
})

test('failed sign-out retains identity and allows retry', async ({ page }) => {
  await mockAPI(page, { logoutFailure: true })
  await page.goto('/')
  await page.getByRole('button', { name: 'Account for Markus Barta' }).click()
  await page.getByRole('menuitem', { name: 'Sign out', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('Sign out didn’t complete')
  await expect(page).toHaveURL('/')
})

test('failed dev login remains on sign-in with an accessible error', async ({ page }) => {
  await mockAPI(page, { signedIn: false, devMode: true, loginFailure: true })
  await page.goto('/signin')
  await page.getByLabel('Email address').fill('markus@barta.com')
  await page.getByRole('button', { name: 'Continue with email' }).click()
  await expect(page.getByRole('alert')).toContainText('This email is not a member of this workspace yet.')
  await expect(page).toHaveURL('/signin')
})

test('theme follows the OS and explicit choice wins without hover layout shift', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' })
  await mockAPI(page)
  await page.goto('/')
  const theme = page.getByRole('button', { name: 'Switch to light theme' })
  await expect(theme).toBeVisible()
  const before = await theme.boundingBox()
  await theme.hover()
  expect(await theme.boundingBox()).toEqual(before)
  await theme.click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await page.emulateMedia({ colorScheme: 'light' })
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.getByRole('button', { name: 'Switch to dark theme' })).toBeVisible()
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--canvas').trim())).toBe('#f7f6f2')
})

test('version uses six-segment Pretty, keyboard reveal, exact clipboard and one request', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const calls = await mockAPI(page, { signedIn: false })
  await page.goto('/signin')
  const version = page.locator('footer').getByRole('button', { name: `${versionName} — Copy version` })
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await expect(version).toHaveAttribute('title', versionName)
  await page.evaluate(() => document.fonts.ready)
  // INSPR-CalVer3: the decorative v and the .0.0 tail are never drawn; the
  // seconds take no width at rest and appear on reveal.
  const drawn = await version.evaluate(el => el.textContent ?? '')
  expect(drawn).not.toContain('.0.0')
  expect(drawn.trimStart().startsWith('v')).toBe(false)
  const before = await version.boundingBox()
  const seconds = version.locator('[data-collapsed="true"]').first()
  expect((await seconds.boundingBox())?.width ?? 0).toBe(0)
  await version.focus()
  await expect(version).toHaveAttribute('data-version-view', 'revealed')
  await expect.poll(async () => (await version.boundingBox())!.width).toBeGreaterThan(before!.width)
  await page.keyboard.press('Enter')
  await expect(version).toHaveAttribute('data-copy-state', 'copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(canonical)
  expect(calls.filter(call => call.path === '/api/version')).toHaveLength(1)
})

// AEON-430: the sign-in card names the release too; the version waits for hover or focus.
test('sign-in card shows the release name; hover and focus reveal the version, and the name still copies it', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const calls = await mockAPI(page, { signedIn: false, codename: 'Hinged Hangar' })
  await page.goto('/signin')
  const copy = page.locator('.card-foot').getByRole('button', { name: `Hinged Hangar, version ${canonical} — Copy version` })
  await expect(copy).toBeVisible()
  await expect(copy.locator('.rn-name')).toHaveText('Hinged Hangar')
  // No calendar version at rest: the name shows, the stamp waits, and the page never draws the number.
  await expect(copy.locator('.rn-name')).toHaveCSS('opacity', '1')
  await expect(copy.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  await expect(page.locator('.card-foot .version-coordinate')).toHaveCount(0)
  await copy.hover()
  await expect(copy.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(copy.locator('.rn-stamp .calendar-version')).toContainText('26·09·23 12:00')
  await page.mouse.move(2, 2)
  await expect(copy.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  await copy.focus()
  await page.keyboard.press('Shift+Tab')
  await page.keyboard.press('Tab')
  await expect(copy).toBeFocused()
  await expect(copy.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(copy).toHaveAccessibleDescription(versionName)
  await page.keyboard.press('Enter')
  await expect(copy).toHaveAttribute('data-copy-state', 'copied')
  await expect(copy.locator('[role="status"]')).toHaveText('Copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(canonical)
  expect(calls.filter(call => call.path === '/api/version')).toHaveLength(1)
  // The confirmation passes; focus is still on the name, so the version stays; moving on brings the name back.
  await expect(copy).not.toHaveAttribute('data-copy-state', /.+/)
  await expect(copy.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await page.keyboard.press('Tab')
  await expect(copy.locator('.rn-name')).toHaveCSS('opacity', '1')
})

test('sign-in card: when the clipboard is unavailable the version is offered selected', async ({ page, context }) => {
  await context.grantPermissions([])
  await page.addInitScript(() => { Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.reject(new Error('denied')) } }); document.execCommand = () => false })
  await mockAPI(page, { signedIn: false, codename: 'Hinged Hangar' })
  await page.goto('/signin')
  const copy = page.locator('.card-foot').getByRole('button', { name: /— Copy version$/ })
  await copy.click()
  await expect(copy).toHaveAttribute('data-copy-state', 'failed')
  await expect(copy.locator('.copy-note')).toHaveText(canonical)
  await expect(copy.locator('[role="status"]')).toContainText('Copy unavailable')
  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(canonical)
})

test('dev version remains plain text', async ({ page }) => {
  await mockAPI(page, { signedIn: false, version: 'dev' })
  await page.goto('/signin')
  await expect(page.locator('.version-coordinate')).toHaveText(['dev'])
  await expect(page.locator('.version-coordinate[role="button"]')).toHaveCount(0)
})
