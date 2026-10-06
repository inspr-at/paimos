// SPDX-License-Identifier: AGPL-3.0-only
// AEON-785: the footer's centre says what the screen shows. Risks: a wrong or
// leaked number, a click that does nothing, a summary that moves the mark or the
// release, a failed save reported as calm.
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { capacityWorld, NOW, TZ } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { accessWorld, mockAccess } from './access-fixtures'
import { mockReleases, presentedHistory } from './releases-fixtures'

const sum = (page: Page) => page.locator('footer.app-footer .sum')
// A connected live stream: without one the page says updates are paused, which is its own test.
const connectedStream = (page: Page) => page.addInitScript(() => {
  class Stream extends EventTarget {
    readyState = 1
    onopen: (() => void) | null = null
    onerror: (() => void) | null = null
    close() { this.readyState = 2 }
    constructor(public url: string) {
      super()
      queueMicrotask(() => { this.onopen?.(); this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 40, resumed: false }), lastEventId: '40' })) })
    }
  }
  Object.assign(window, { EventSource: Stream })
})
const visibleText = (page: Page) => sum(page).locator(':scope > .full, :scope > .short').evaluateAll(parts => parts.filter(part => getComputedStyle(part).display !== 'none').map(part => part.textContent).join(''))

for (const [width, form] of [[1440, 'full'], [390, 'short']] as const) {
  test(`tickets say count, agents and the blocked ones, and the click adds the filter (${form} at ${width})`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 })
    const errors = watchErrors(page)
    await connectedStream(page)
    const data = fixtures()
    data.nodes.find(node => node.id === 'n-4')!.state = 'blocked'
    await mockWork(page, data)
    await page.goto('/p/PHAROS/tickets')
    await expect(sum(page)).toHaveAttribute('data-tone', 'attention')
    await expect(sum(page)).toHaveAttribute('data-conn', 'on')
    await expect.poll(() => visibleText(page)).toMatch(form === 'full' ? /^\d+ tickets? · .*1 blocked$/ : /^1 blocked$/)
    await expect(sum(page)).toHaveAccessibleName(/1 blocked\. Show the blocked tickets\.$/)
    // Nothing the summary says is cut off by its neighbours.
    expect(await sum(page).locator(':scope > .full, :scope > .short').evaluateAll(parts => parts.filter(part => getComputedStyle(part).display !== 'none').map(part => part.scrollWidth - part.clientWidth))).toEqual([0])
    // The mark and the release keep their place; the summary only fills the middle.
    const guard = await controlStability(page, { mark: page.locator('footer.app-footer .footer-wordmark'), release: page.locator('footer.app-footer .version-pill, footer.app-footer .version-plain') })
    await guard.check(async () => {
      await sum(page).click()
      await expect(page).toHaveURL(/status=blocked/)
    })
    guard.done()
    const footer = await page.locator('footer.app-footer').boundingBox(), centre = await sum(page).boundingBox()
    expect(footer && centre && centre.height >= 28 && centre.y >= footer.y - .5 && centre.y + centre.height <= footer.y + footer.height + .5).toBe(true)
    await page.screenshot({ path: info.outputPath(`tickets-${width}.png`) })
    expect(errors).toEqual([])
  })
}

test('knowledge says what waits for review and the click filters to it', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockKnowledge(page, knowledgeWorld())
  await page.goto('/p/PHAROS/knowledge')
  await expect(sum(page)).toHaveAttribute('data-tone', 'attention')
  await expect(sum(page)).toContainText(/\d+ entries · 1 to review/)
  await sum(page).click()
  await expect(page).toHaveURL(/status=proposed/)
  await expect(sum(page)).toContainText(/1 entry · 1 to review/)
})

test('settings stay silent when calm, and a failed save says so until it is saved again', async ({ page }) => {
  await mockWork(page, fixtures())
  let fail = true
  const writes: number[] = []
  await page.route('**/api/preferences/footer-summary-probe', route => {
    if (route.request().method() !== 'PUT') return route.fulfill({ json: { key: 'footer-summary-probe', value: null, updated_at: null } })
    writes.push(1)
    return fail ? route.fulfill({ status: 500, json: { error: 'unavailable' } }) : route.fulfill({ json: { key: 'footer-summary-probe', value: { a: 1 }, updated_at: new Date().toISOString() } })
  })
  await page.goto('/settings/personal')
  await expect(page.getByRole('heading', { name: 'Settings', level: 1 })).toBeVisible()
  await expect(sum(page)).toHaveCount(0)
  await page.evaluate(async () => { const { usePreference } = await import('/src/lib/preferences.ts'); usePreference('footer-summary-probe').save({ a: 1 }, 0) })
  await expect(sum(page)).toContainText('Not saved · Try again')
  await expect(sum(page)).toHaveAttribute('data-tone', 'attention')
  fail = false
  await sum(page).click()
  await expect(sum(page)).toHaveCount(0)
  expect(writes).toHaveLength(2)
})

const world: AgentWorld = {
  me: me.id, now: NOW,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}

test.describe('agents', () => {
  test.use({ timezoneId: TZ })
  test('say how many there are and the one exception, and the click goes to it', async ({ page }) => {
    await page.clock.setSystemTime(NOW)
    await mockWork(page, fixtures(), { admin: true })
    const capacity = capacityWorld({})
    const data = agentData(world)
    data.accounts = capacity.accounts as unknown as typeof data.accounts
    await mockAgents(page, data, { capacity })
    await page.route('**/api/me/permissions*', route => route.fulfill({ json: mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined) }))
    await page.goto('/agents')
    await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
    // Six sessions are listed and four requests wait on this person: the exception is theirs, and the click lands on the first.
    await expect(sum(page)).toHaveAccessibleName('6 agents, 4 ask you. Go to the requests.')
    await expect(sum(page)).toHaveAttribute('data-tone', 'attention')
    await sum(page).click()
    await expect(page.locator('.agents-page [data-row^="a:"], .agents-page [data-row^="m:"]').first()).toBeFocused()
  })
})

test('access says people, agent keys and the key that expires soon, and the click opens it', async ({ page }) => {
  const NOW = '2026-09-23T12:00:00Z'
  await page.clock.setFixedTime(new Date(NOW))
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.keys.find(key => key.id === 'k2')!.expires_at = new Date(Date.parse(NOW) + 10 * 86_400_000).toISOString()
  await mockAccess(page, world)
  await page.goto('/settings/access/people')
  await expect(sum(page)).toHaveAttribute('data-tone', 'attention')
  await expect(sum(page)).toHaveAccessibleName(/^\d+ people, \d+ agent keys?, 1 key expires soon\. Show the expiring keys\.$/)
  await sum(page).click()
  await expect(page).toHaveURL(/\/settings\/access\/agents$/)
  await expect(page.locator('.keys-table').first()).toBeVisible()
})

test('releases on their way are counted, a failed run is the exception, and the sheet gives the footer back when it closes', async ({ page }) => {
  await connectedStream(page)
  await mockWork(page, fixtures())
  const history = presentedHistory()
  // Two releases tagged and not yet published; one of them failed its checks.
  const [first, second] = history.releases as unknown as { state: string; evidence: { ci: unknown } }[]
  first.state = second.state = 'candidate'
  second.evidence.ci = { name: 'ci', url: '', status: 'completed', conclusion: 'failure' }
  await mockReleases(page, history)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(sum(page)).toHaveCount(0)
  await page.goto('/releases')
  await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
  await expect(sum(page)).toHaveAccessibleName('2 planned, 1 at risk. Open the release at risk.')
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeHidden()
  await expect(sum(page)).toHaveCount(0)
})
