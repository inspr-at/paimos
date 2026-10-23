// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1709: browser connections replace cookie peeking. Risks: a disconnected
// grant silently looks empty, counts differ between surfaces, bearer reads
// send cookies, callback fragments survive routing, or state changes move controls.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

test.use({ timezoneId: 'Europe/Vienna', locale: 'en-GB' })
const NOW = Date.parse('2026-10-21T17:00:00Z')
const HOUR = 3_600_000
const iso = (ms: number) => new Date(ms).toISOString()
const SIBLING = 'https://pma.example'
const SUMMARY = `${SIBLING}/api/decision-desk/owner-summary`
test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

type Sibling = 'ok' | 'down' | 'ended'
const keptGoal = {
  id: 'g-wait', key: 'G-WAIT', title: 'Keep the header still', kind: 'goal', project_id: 'p-aeon', workspace: false, expansion: 'full',
  counts: { outcomes: 1, tickets: 1 }, items: [], steps: [], next_step: null,
  projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 0, question_ids: [] },
  progress: { pct: 0, computed_pct: 0, source: 'computed', basis: 'count', total: 1, done: 0, items_without_estimate: 0, items_partially_estimated: 0 },
  eta: { source: 'none', partial: false }, passed: false, states: { moving: 1, queued: 0, paused: 0, done: 0 }, done: false,
  stall: { stalled: false, since: iso(NOW) }, read_through: 1, generation: 1,
}
async function mockBand(page: Page, options: { connected?: boolean; sibling: Sibling; lines?: number; goal?: boolean; instances?: Sibling; desk?: 'items' | 'empty'; projectKey?: string }) {
  const reads = { sibling: 0, origins: [] as string[], cookie: false, bearer: false }
  let state = options.sibling
  let instances: Sibling = options.instances ?? 'ok'
  const lines = options.lines ?? 1
  const deskItems = options.desk === 'empty' ? [] : [
    { id: '00000000-0000-4000-8000-000000000001', kind: 'question', project_id: 'p-aeon', revision: 1, title: 'Which identity should agent reviews run as for the gatekeeper go-live?', created_at: iso(NOW - 3 * HOUR), held: false, href: '', source: '', ticket_key: 'AEON-1394', asked_by: { id: 'p-lead', name: 'AEON-LEAD' } },
    { id: '00000000-0000-4000-8000-000000000002', kind: 'question', project_id: 'p-pharos', revision: 1, title: 'Run the 60-minute voice soak test on the Aithema test host?', created_at: iso(NOW - 2 * HOUR), held: true, href: '', source: '', asked_by: { id: 'p-ops', name: 'OPS' } },
    { id: '00000000-0000-4000-8000-000000000003', kind: 'approval', project_id: 'p-aeon', revision: 1, title: 'Approval · nodes.write', created_at: iso(NOW - HOUR), held: false, href: '', source: '' },
  ]
  await mockWork(page, fixtures())
  if (options.connected !== false) await page.addInitScript(({ key, origin, now }) => {
    localStorage.setItem(key, JSON.stringify({ token: `desk_${'a'.repeat(8)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(12)}_${'b'.repeat(8)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(12)}_${'c'.repeat(43)}`, origin, account: 'Connected account', createdAt: new Date(now).toISOString(), expiresAt: new Date(now + 90 * 86400_000).toISOString() }))
  }, { key: `aeon.desk-connections.t1:${me.id}.pma`, origin: SIBLING, now: NOW })
  await page.route(/\/api\/goals(\?.*)?$/, route => route.fulfill({ json: {
    items: options.goal ? [keptGoal] : [], totals: options.goal ? { moving: 1, queued: 0, paused: 0, done: 0 } : { moving: 0, queued: 0, paused: 0, done: 0 }, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 1, generation: 1,
  }, headers: { 'Aeon-Contract': 'goals/1.5' } }))
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: {
    items: deskItems,
    counts: options.desk === 'empty' ? { open: 0, held: 0, chores: 0 } : { open: 3, held: 1, chores: 0 }, has_more: false, as_of: iso(NOW),
    upcoming: [{ node_id: 'n-ait', check_id: 'human', project_id: 'p-aeon', ticket_key: 'AIT-140', label: 'Your Aithema test session (~20 min, whenever you like)' }],
  } }))
  const mount = async () => {
  await page.route(/\/api\/decision-desk\/instances$/, route => {
    if (instances === 'down') return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    return route.fulfill({ json: { here: 'ppm', siblings: [{ tag: 'pma', origin: SIBLING }] } })
  })
  await page.route(SUMMARY + '*', route => {
    const request = route.request(), origin = request.headers().origin ?? ''
    const cors = { 'access-control-allow-origin': origin, 'access-control-allow-methods': 'GET', 'access-control-allow-headers': 'Authorization, Accept', vary: 'Origin' }
    if (request.method() === 'OPTIONS') return route.fulfill({ status: 204, headers: cors })
    reads.sibling++; reads.origins.push(origin)
    reads.cookie ||= !!request.headers().cookie
    reads.bearer ||= /^Bearer desk_/.test(request.headers().authorization ?? '')
    if (state !== 'ok') return route.fulfill({ status: state === 'ended' ? 401 : 503, json: { error: state }, headers: cors })
    return route.fulfill({ headers: cors, json: {
      instance: 'pma', counts: { open: lines, held: 0 }, as_of: iso(NOW),
      items: Array.from({ length: lines }, (_, index) => ({
        id: `00000000-0000-4000-8000-0000000000a${index + 1}`, kind: 'question', project_key: options.projectKey ?? 'PORTAL', asked_by: 'PORTAL', held: false, created_at: iso(NOW - HOUR),
        title: index === 0 ? 'Deploy the new sign-in flow to the second test host?' : 'Confirm the second host’s backup before the window?',
        href: `/decision-desk?item=q:00000000-0000-4000-8000-0000000000a${index + 1}`,
      })),
    } })
  })
  }
  await mount()
  return { reads, mount, set: (next: Sibling) => { state = next }, setInstances: (next: Sibling) => { instances = next } }
}
async function openBand(page: Page) {
  await page.goto('/?view=goals')
  const band = page.getByRole('region', { name: 'Waiting on you' })
  await expect(band.locator('.waiting-title')).toBeVisible()
  return band
}
async function shot(page: Page, info: TestInfo, name: string) { await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true }) }

for (const width of [1440, 1024, 390] as const) {
  test(`shared sibling counts, exact home links and stable Goals controls at ${width}`, async ({ page }, info) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: 950 })
    const mock = await mockBand(page, { sibling: 'ok', lines: 2 })
    const band = await openBand(page), row = band.getByTestId('instance-pma')
    await expect(band.locator('.n-badge')).toHaveText('5')
    await expect(row).toContainText('2 wait for you')
    expect(mock.reads.sibling).toBe(1); expect(mock.reads.cookie).toBe(false); expect(mock.reads.bearer).toBe(true)
    const action = row.locator('.instance-action'), line = row.locator('.instance-line')
    await expectStableControls({ controls: { action, line, openDesk: band.getByRole('link', { name: /Open Desk/ }) }, scrollAreas: { band }, interactions: [
      { name: 'show home items', run: async () => { await action.click(); await expect(row.locator('.instance-item')).toHaveCount(2) } },
      { name: 'hide home items', run: async () => { await action.click(); await expect(row.locator('.instance-item')).toHaveCount(0) } },
      { name: 'show again', run: async () => { await action.click(); await expect(row.locator('.instance-item')).toHaveCount(2) } },
    ] })
    const home = row.locator('.instance-item').first()
    await expect(home).toHaveAttribute('href', `${SIBLING}/decision-desk?item=q:00000000-0000-4000-8000-0000000000a1`)
    await expect(home).toHaveAttribute('target', '_blank')
    await expect(home).toHaveAttribute('rel', 'noopener noreferrer')
    await expect(band.locator('.coming')).toContainText('Your Aithema test session')
    await shot(page, info, `goals-${width}-light`)
    await page.emulateMedia({ colorScheme: 'dark' }); await shot(page, info, `goals-${width}-dark`)
    await expect.poll(() => page.title()).toMatch(/^\(5\)/)
    const header = page.locator('header .needs-badge').first()
    await expect(header).toHaveText('5')
    // The approved total and per-instance breakdown are present in both the accessible name and tooltip.
    const deskPlace = page.locator('header .places').getByRole('link', { name: 'Decision Desk, 5 open · 5 wait for you · 3 here · 2 on pma', exact: true })
    await expect(deskPlace).toBeVisible()
    await expect(deskPlace).toHaveAttribute('data-tip', /3 here · 2 on pma/)
    expect(errors).toEqual([])
    if (width < 720) { await page.getByRole('button', { name: /^Account for / }).click(); await page.getByRole('menu', { name: 'Account' }).getByRole('menuitem', { name: 'Preview the Decision Desk' }).click() }
    else await page.getByRole('button', { name: 'Preview the Decision Desk', exact: true }).click()
    const preview = page.getByRole('dialog', { name: 'Decision Desk preview', exact: true })
    await expect(preview.getByTestId('instance-pma')).toContainText('On pma · 2')
    await expect(preview.getByTestId('instance-pma').locator('.instance-tag')).toHaveText(['pma', 'pma'])
    await expect(preview.getByRole('link', { name: 'Open pma Desk' })).toHaveAttribute('href', `${SIBLING}/decision-desk`)
    await shot(page, info, `preview-${width}-dark`)
    await page.emulateMedia({ colorScheme: 'light' }); await shot(page, info, `preview-${width}-light`)
    await page.keyboard.press('Escape')
    await mockDecisionDesk(page, { emptyDesk: true })
    await mock.mount()
    await page.emulateMedia({ colorScheme: 'light' })
    await page.goto('/decision-desk')
    await expect(page.getByTestId('desk-tab-open')).toContainText('2')
    await expect(page.getByTestId('desk-clear')).toHaveCount(0)
    await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits here')
    await expect(page.getByTestId('desk-sync').locator('.sync-text')).toHaveAttribute('data-tip', /pma: checked/)
    await expect(page.getByTestId('instance-pma').locator('.instance-item')).toHaveCount(2)
    await expect(page.getByTestId('instance-pma').getByRole('button')).toHaveCount(0)
    await shot(page, info, `desk-${width}-light`)
    await page.emulateMedia({ colorScheme: 'dark' }); await shot(page, info, `desk-${width}-dark`)
    await page.emulateMedia({ colorScheme: 'light' })
    if (width === 390) {
      await page.setViewportSize({ width: 320, height: 950 })
      expect(await page.locator('.desk-page').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      const heading = page.getByTestId('instance-pma').locator('.instance-copy')
      expect(await heading.evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
      const item = await page.getByTestId('instance-pma').locator('.instance-item').first().boundingBox()
      expect(item!.height).toBeGreaterThanOrEqual(44)
      await shot(page, info, 'desk-320-light')
      await page.setViewportSize({ width: 390, height: 950 })
    }
    await page.getByRole('radio', { name: /^Decided/ }).click()
    await expect(page.getByRole('link', { name: 'Decisions made on pma are listed there' })).toHaveAttribute('href', `${SIBLING}/decision-desk?view=decided`)
    await shot(page, info, `decided-${width}-light`)
    await mockSettings(page, settingsData())
    await mock.mount()
    await page.route('**/api/decision-desk/connections', route => route.fulfill({ json: { connections: [{ id: '00000000-0000-4000-8000-000000000099', origin: SIBLING, instance: 'pma', created_at: iso(NOW), expires_at: iso(NOW + 90 * 86400_000), last_used_at: iso(NOW) }] } }))
    await page.goto('/settings/personal')
    const receiving = page.locator('#other-instances'), issuing = page.locator('#connected-instances')
    await expect(receiving).toContainText('Connected account')
    await expect(issuing).toContainText('pma.example')
    const notice = receiving.getByRole('checkbox', { name: 'Notify me about pma' })
    await expect(notice).toBeChecked()
    await expectStableControls({ controls: { notice: receiving.locator('.instance-notify'), disconnect: receiving.getByRole('button', { name: 'Disconnect', exact: true }) }, scrollAreas: { receiving }, interactions: [
      { name: 'mute this instance', run: async () => { await notice.uncheck(); await expect(notice).not.toBeChecked() } },
      { name: 'restore instance notifications', run: async () => { await notice.check(); await expect(notice).toBeChecked() } },
    ] })
    for (const theme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: theme })
      await receiving.screenshot({ path: info.outputPath(`connections-here-${width}-${theme}.png`) })
      await issuing.screenshot({ path: info.outputPath(`connections-home-${width}-${theme}.png`) })
    }
  })
}

test('unreachable results remain dated, ended results disappear, and connection states keep the same row', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 950 })
  const mock = await mockBand(page, { sibling: 'ok', lines: 2 })
  const band = await openBand(page), row = band.getByTestId('instance-pma'), action = row.locator('.instance-action')
  await expect(action).toHaveText('Show'); await action.click()
  await expectStableControls({ controls: { action, line: row.locator('.instance-line') }, scrollAreas: { band }, interactions: [
    { name: 'unreachable keeps cache', run: async () => { mock.set('down'); await page.clock.fastForward(30_000); await expect(row).toContainText("couldn't be reached"); await expect(row.locator('.instance-item')).toHaveCount(2) } },
    { name: 'retry restores current answer', run: async () => { mock.set('ok'); await action.click(); await expect(row).toContainText('2 wait for you') } },
    { name: 'revoked drops cached items', run: async () => { mock.set('ended'); await page.clock.fastForward(30_000); await expect(row).toContainText('connection ended'); await expect(row.locator('.instance-item')).toHaveCount(0) } },
  ] })
  await expect(band.locator('.n-badge')).toHaveText('3')
  await shot(page, info, 'goals-390-ended')
})

test('every signed-in person can connect and unknown coverage never claims an empty Desk', async ({ page }) => {
  await mockBand(page, { connected: false, sibling: 'ok', instances: 'down', desk: 'empty' })
  await page.goto('/?view=goals')
  const band = page.getByRole('region', { name: 'Waiting on you' })
  await expect(band).toContainText('Other instances couldn’t be read')
  await expect(band).not.toContainText('Nothing waits on you')
})

test('an unconnected sibling contributes nothing to the empty Desk, Goals and preview', async ({ page }) => {
  await mockBand(page, { connected: false, sibling: 'ok', desk: 'empty' })
  await page.goto('/?view=goals')
  const band = page.getByRole('region', { name: 'Waiting on you' })
  await expect(band).toHaveClass(/is-clear/)
  await expect(band).toContainText('Nothing waits on you')
  await band.getByRole('button', { name: 'Not now', exact: true }).click()
  await expect(band.getByTestId('instance-pma')).toHaveCount(0)
  await expect(band).toContainText('Nothing waits on you')
  await page.getByRole('button', { name: 'Preview the Decision Desk', exact: true }).click()
  const preview = page.getByRole('dialog', { name: 'Decision Desk preview', exact: true })
  await expect(preview.locator('.dp-count')).toHaveText('Nothing for you')
  await expect(preview).toContainText('Nothing waits on you.')
  await page.keyboard.press('Escape')
  await mockDecisionDesk(page, { emptyDesk: true })
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-clear')).toBeVisible()
  await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
})

test('phone status and project keys expose their full copy with text spacing', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 950 })
  const projectKey = 'DATENSCHUTZVERANTWORTLICHKEITEN-1234'
  const mock = await mockBand(page, { sibling: 'ok', projectKey })
  const band = await openBand(page), row = band.getByTestId('instance-pma')
  await row.locator('.instance-action').click()
  const project = row.locator('.instance-project')
  await expect(project).toHaveCSS('white-space', 'nowrap')
  await expect(project).toHaveAttribute('data-tip', projectKey)
  await expect(project).toHaveAttribute('tabindex', '0')
  await page.addStyleTag({ content: '.instance-copy { line-height: 2 !important; letter-spacing: .12em !important; word-spacing: .16em !important; }' })
  mock.set('down'); await page.clock.fastForward(30_000)
  const copy = row.locator('.instance-copy')
  await expect(copy).toContainText("couldn't be reached")
  // Two-line clamping must retain a keyboard-accessible complete status.
  expect(await copy.evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true)
  await expect(copy).toHaveAttribute('tabindex', '0')
  await expect(copy).toHaveAttribute('data-tip', /pma.*couldn't be reached.*showing/)
  await copy.focus(); await expect(copy).toBeFocused()
})

test('sibling sync ages keep the number and seconds unit together', async ({ page }) => {
  await mockBand(page, { sibling: 'ok' })
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-sync').locator('.sync-text')).toHaveAttribute('data-tip', /pma: checked \d+\u00a0s ago/)
})

// A browser proof is necessary for the handshake: unit tests cannot show that
// the actual router strips a credential fragment before rendering or retaining
// a sign-in return path. No third-party cookies are used in this flow.
test('connect opens a top-level confirmation and a single-use callback clears the fragment before rendering', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 950 })
  const mock = await mockBand(page, { connected: false, sibling: 'ok' })
  await page.addInitScript(() => {
    Object.assign(window, { __connectOpened: '', __connectClosed: false })
    window.close = () => { (window as unknown as { __connectClosed: boolean }).__connectClosed = true }
    window.open = ((url: string) => { (window as unknown as { __connectOpened: string }).__connectOpened = String(url); return null }) as typeof window.open
  })
  const band = await openBand(page), row = band.getByTestId('instance-pma')
  const action = row.locator('.instance-action')
  await expectStableControls({ controls: { action, line: row.locator('.instance-line') }, scrollAreas: { band }, interactions: [
    { name: 'start confirmation', run: async () => { await action.click(); await expect(row).toContainText('confirm in the tab that opened') } },
  ] })
  const opened = await page.evaluate(() => (window as unknown as { __connectOpened: string }).__connectOpened)
  const target = new URL(opened), state = target.searchParams.get('state')!
  expect(target.origin+target.pathname).toBe(`${SIBLING}/instances/connect`)
  const token = `desk_${'a'.repeat(8)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(12)}_${'b'.repeat(8)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(12)}_${'c'.repeat(43)}`
  const fragment = new URLSearchParams({ state, token, instance: 'pma', account: 'Connected account', created_at: iso(NOW), expires_at: iso(NOW+90*86400_000) }).toString()
  const sent: string[] = []
  page.on('request', request => sent.push(request.url()))
  await page.goto(`/instances/connected#${fragment}`)
  await expect(page.getByText('Connected. You can close this tab.')).toBeVisible()
  expect(new URL(page.url()).hash).toBe('')
  expect(await page.evaluate(() => (window as unknown as { __connectClosed: boolean }).__connectClosed)).toBe(true)
  expect(sent.some(url => url.includes(token))).toBe(false)
  expect(await page.content()).not.toContain(token)
  await shot(page, info, 'connected-1440')
  await page.goto(`/instances/connected#${fragment}`)
  await expect(page.getByRole('alert')).toContainText('Return and connect again')
  expect(mock.reads.cookie).toBe(false)
})

for (const width of [1440, 1024, 390] as const) {
  test(`confirmation shows the account, keeps actions stable and Cancel shares nothing at ${width}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 950 })
    await mockBand(page, { connected: false, sibling: 'ok' })
    let writes = 0
    await page.route('**/api/decision-desk/connections', async route => { writes++; await route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } }) })
    await page.goto(`/instances/connect?origin=${encodeURIComponent(SIBLING)}&state=${'a'.repeat(43)}`)
    await expect(page.getByRole('heading', { name: 'Show your waiting decisions on pma?' })).toBeVisible()
    await expect(page.locator('.connect-account')).toContainText(me.name)
    await expect(page.locator('.connect-account .avatar')).toHaveCSS('width', '36px')
    await expect(page.locator('.connect-account .avatar')).toHaveCSS('height', '36px')
    const switchAccount = page.getByRole('button', { name: 'Not you? Switch account', exact: true })
    await expect(switchAccount).toHaveCSS('border-top-width', '0px')
    await expect(switchAccount).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    const linkColor = await switchAccount.evaluate(el => {
      const probe = document.createElement('span'); probe.style.color = 'var(--teal-ink)'; el.append(probe)
      const color = getComputedStyle(probe).color; probe.remove(); return color
    })
    await expect(switchAccount).toHaveCSS('color', linkColor)
    const connect = page.getByRole('button', { name: 'Connect', exact: true }), cancel = page.getByRole('button', { name: 'Cancel', exact: true })
    await expectStableControls({ controls: { connect, cancel, switchAccount }, scrollAreas: { card: page.locator('.connect-card') }, interactions: [
      { name: 'failed connect preserves actions', run: async () => { await connect.click(); await expect(page.getByRole('alert')).toBeVisible() } },
    ] })
    await shot(page, info, `confirm-${width}-light`)
    await page.emulateMedia({ colorScheme: 'dark' }); await shot(page, info, `confirm-${width}-dark`)
    await cancel.click(); await expect(page.getByText('Nothing was shared.')).toBeVisible()
    expect(writes).toBe(1)
  })
}

for (const width of [1440, 1024, 390] as const) {
  test(`long German account names and hosts remain readable at ${width}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 950 })
    const account = 'Datenschutzbeauftragte für grenzüberschreitende Unternehmenszusammenarbeit', email = `${'datenschutz'.repeat(5)}@example.test`
    const origin = `https://${Array(4).fill('unternehmenszusammenarbeit'.repeat(2)).join('.')}.example`
    await mockBand(page, { connected: false, sibling: 'ok' })
    await page.route('**/api/me', route => route.fulfill({ json: { principal: { ...me, name: account, email, kind: 'person', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
    await page.route('**/api/decision-desk/instances', route => route.fulfill({ json: { here: 'ppm', siblings: [{ tag: 'pma', origin }] } }))
    await page.route(`${origin}/api/decision-desk/owner-summary*`, route => route.fulfill({ status: 503, headers: { 'access-control-allow-origin': new URL(page.url()).origin, 'access-control-allow-methods': 'GET', 'access-control-allow-headers': 'Authorization, Accept' }, json: { error: 'unavailable' } }))
    await page.goto(`/instances/connect?origin=${encodeURIComponent(origin)}&state=${'a'.repeat(43)}`)
    const card = page.locator('.connect-card'), name = page.locator('.connect-account b'), address = page.locator('.connect-account small')
    await expect(name).toHaveText(account)
    await expect(page.locator('.connect-account .avatar')).toHaveCSS('width', '36px')
    await expect(page.locator('.connect-account .avatar')).toHaveCSS('height', '36px')
    await expect(name).toHaveCSS('hyphens', 'auto')
    await expect(name).toHaveCSS('white-space', 'nowrap')
    await expect(name).toHaveAttribute('data-tip', account)
    await expect(address).toHaveAttribute('data-tip', email)
    await expect(page.locator('.connect-host')).toHaveAttribute('data-tip', new URL(origin).host)
    await expect(page.getByRole('heading', { level: 1 })).toHaveCSS('hyphens', 'auto')
    for (const theme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: theme })
      expect(await card.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      await name.focus(); await expect(name).toBeFocused()
      await shot(page, info, `confirm-long-${width}-${theme}`)
    }
    await mockSettings(page, settingsData())
    await page.route('**/api/decision-desk/connections', route => route.fulfill({ json: { connections: [{ id: 'c1', instance: 'pma', origin, created_at: iso(NOW), expires_at: iso(NOW + 90 * 86400_000) }] } }))
    await page.addInitScript(({ key, origin, account, now }) => localStorage.setItem(key, JSON.stringify({ token: `desk_${'a'.repeat(8)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(12)}_${'b'.repeat(8)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(12)}_${'c'.repeat(43)}`, origin, account, createdAt: new Date(now).toISOString(), expiresAt: new Date(now + 90 * 86400_000).toISOString() })), { key: `aeon.desk-connections.t1:${me.id}.pma`, origin, account, now: NOW })
    await page.goto('/settings/personal')
    const receiving = page.locator('#other-instances'), issuing = page.locator('#connected-instances')
    await expect(receiving).toBeVisible(); await expect(issuing).toBeVisible()
    for (const theme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: theme })
      for (const section of [receiving, issuing]) {
        const host = section.locator('.connection-host')
        const extent = await host.evaluate(el => ({ width: el.clientWidth, height: el.clientHeight, scrollWidth: el.scrollWidth }))
        expect(extent.width, 'host has a measured text box').toBeGreaterThan(0)
        expect(extent.height, 'host has a measured text box').toBeGreaterThan(0)
        expect(extent.scrollWidth, 'long host is ellipsized inside its card').toBeGreaterThan(extent.width + 1)
        await expect(host).toHaveCSS('white-space', 'nowrap')
        await expect(host).toHaveAttribute('data-tip', new URL(origin).host)
        await expect(host).toHaveAttribute('tabindex', '0')
        await host.focus(); await expect(host).toBeFocused()
        expect(await section.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      }
      await expect(receiving.locator('.connection-account')).toHaveCSS('white-space', 'nowrap')
      if (width === 390) await expect(receiving.locator('.connection-account')).toHaveAttribute('data-tip', `connected as ${account}`)
      await receiving.screenshot({ path: info.outputPath(`connections-long-${width}-${theme}.png`) })
    }
    const notice = receiving.getByRole('checkbox', { name: 'Notify me about pma' })
    await expectStableControls({ controls: { notice: receiving.locator('.instance-notify'), disconnect: receiving.getByRole('button', { name: 'Disconnect', exact: true }) }, scrollAreas: { receiving }, interactions: [
      { name: 'mute without moving actions', run: async () => { await notice.uncheck(); await expect(notice).not.toBeChecked() } },
    ] })
  })
}
