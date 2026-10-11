// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1171 (design AEON-1149, package P1): Settings › Accounts and computers
// became three parts, Accounts, Computers and Capacity, over one set of data.
// Risks: an old link lands in the wrong part or without its panel; a Needs-you
// item shows in a part that does not own it, or an empty "Nothing needs you"
// block stays; the nav counts disagree with the parts; a count that comes or
// goes moves the nav. AEON1171_SHOTS=1 also writes every part at 1440 and 390,
// light and dark, into the test's output folder.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: TZ })
const world: AgentWorld = { me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} }
const CTX = { def: 'e0000000-0000-4000-8000-000000000001', hold: 'e0000000-0000-4000-8000-000000000004' }
const RUN = 'a1000000-0000-4000-8000-000000000071'
const PARTS = ['accounts', 'computers', 'capacity'] as const
type Part = typeof PARTS[number]
interface Setup extends CapacityOptions {
  /** Low-quota warnings at 10 %: Spare (9 % left) is low. Otherwise at 5 %, so nothing is. */
  low?: boolean
  /** Claude's sign-in on build-7 ran out of verification. */
  expired?: boolean
  /** studio was removed and its local cleanup is pending. */
  cleanup?: boolean
  /** The person may choose where accounts work, so the matrix is read. */
  chooses?: boolean
  /** Spare was added while new accounts ask first and has no tick yet. */
  awaiting?: boolean
  /** Projects waiting in Unassigned. */
  holding?: number
}
async function setup(page: Page, o: Setup = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world), capacity = capacityWorld(o)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  const build7 = capacity.computers[0]!
  build7.revision = 4
  build7.enrollments.forEach(e => Object.assign(e, { can_verify: true, verification_state: 'completed' }))
  Object.assign(build7, { harness_statuses: { codex: 'ready', claude: 'ready', grok: 'ready', cursor: 'ready' }, verification_capabilities: { ...build7.verification_capabilities, claude: { supported: true, policy: 'read_only', reason: '' } } })
  const claude = build7.enrollments.find(e => e.harness === 'claude')!
  if (o.expired) Object.assign(claude, { verification_state: 'expired', verification_expired_ready: false, verified_at: new Date(NOW - 86_400_000).toISOString(), verification_expires_at: new Date(NOW - 3_600_000).toISOString() })
  if (o.cleanup) { const studio = capacity.computers[1]!; Object.assign(studio, { computer_state: 'revoked', local_cleanup: 'pending', accounting_state: 'unconfirmed' }); studio.enrollments.forEach(e => Object.assign(e, { state: 'revoked' })) }
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', 'account.manage', ...(o.chooses ? ['account.use.manage'] : [])]
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/settings/quota-warnings', route => route.fulfill({ json: { early_percent: o.low ? 10 : 5, urgent_percent: 3 } }))
  const first = [{ project_id: 'p-pharos', key: 'PHAROS', name: 'Pharos' }, { project_id: 'p-aeon', key: 'AEON', name: 'Aeon' }].slice(0, o.holding ?? 0)
  await page.route(/\/api\/account-use(\?|$)/, route => route.fulfill({ json: {
    rules: { new_accounts: 'ask', new_contexts: 'ask', new_projects: 'holding', new_models: 'allow', revision: 3, enforced_at: null, confirmation_required: false, confirmed_at: null },
    accounts: [
      { id: ACCOUNTS.main, label: 'Main', harness: 'codex', plan: 'Pro', billing_mode: 'subscription', owner_id: null, awaiting: false },
      { id: ACCOUNTS.spare, label: 'Spare', harness: 'codex', plan: 'Pro', billing_mode: 'subscription', owner_id: null, awaiting: !!o.awaiting },
    ],
    contexts: [
      { id: CTX.def, name: 'Default', kind: 'default', new_accounts_override: null, archived_at: null, revision: 1 },
      { id: CTX.hold, name: 'Unassigned', kind: 'holding', new_accounts_override: null, archived_at: null, revision: 1 },
    ],
    cells: [{ account_id: ACCOUNTS.main, context_id: CTX.def, allowed: true }],
    next_account: null, next_context: null, running_outside: [], running_outside_truncated: false,
    ...(o.holding ? { holding_projects: { count: o.holding, first } } : {}),
  } }))
  return { capacity, build7, claude }
}
const navLink = (page: Page, part: Part) => page.locator(`.section-nav .section-link[href="/settings/${part}"]`)
const navCounts = (page: Page) => page.locator('.section-nav .section-link').evaluateAll(links => Object.fromEntries(links.flatMap(link => {
  const n = link.querySelector('.nav-n')?.textContent?.trim()
  return n ? [[link.getAttribute('href')!.replace('/settings/', ''), n]] : []
})))
const needs = (page: Page) => page.locator('.needs-block')
const footer = (page: Page) => page.locator('footer.app-footer .sum')
/** Opens a part and waits until every read a count depends on has answered and shows. */
async function visit(page: Page, part: Part, matrix = false) {
  const answered = (test: RegExp) => page.waitForResponse(r => r.request().method() === 'GET' && test.test(new URL(r.url()).pathname))
  const reads = [answered(/^\/api\/agent-pairing\/computers$/), answered(/^\/api\/agent-accounts\/capacity\/schedule$/), answered(/^\/api\/settings\/quota-warnings$/), ...(matrix ? [answered(/^\/api\/account-use$/)] : [])]
  await page.goto(`/settings/${part}`)
  await Promise.all(reads)
  await expect(page.locator('#agent-accounts')).toHaveAttribute('data-part', part)
  await expect(page.locator('#agent-accounts .loading-state')).toHaveText('')
  await expect(page.locator(part === 'accounts' ? '.list-row[data-account]' : part === 'computers' ? '[data-computer]' : '#capacity-and-load .days').first()).toBeVisible()
}

test('nothing to do: no Needs-you block in any part, no nav count and nothing in the footer', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1280, height: 1000 })
  await setup(page, { chooses: true })
  for (const part of PARTS) {
    await visit(page, part, true)
    await expect(navLink(page, part)).toHaveAttribute('aria-current', 'page')
    await expect(needs(page)).toHaveCount(0)
    await expect(page.getByRole('region', { name: 'The plan' })).toHaveCount(0)
    expect(await navCounts(page)).toEqual({})
    await expect(footer(page)).toHaveCount(0)
  }
  expect(errors).toEqual([])
})

// The design's states (check.mjs): each part lists only its own items; the nav counts every part.
const STATES: { name: string; setup: Setup; counts: Partial<Record<Part, string>>; says: Partial<Record<Part, string>> }[] = [
  { name: 'a sign-in to verify', setup: { expired: true }, counts: { computers: '1' }, says: { computers: 'Claude verification on build-7' } },
  { name: 'a low quota', setup: { low: true }, counts: { accounts: '1' }, says: { accounts: 'Codex quota is low' } },
  { name: 'a new account that asks first', setup: { chooses: true, awaiting: true }, counts: { accounts: '1' }, says: { accounts: 'New account found: Spare' } },
  { name: 'a project waiting in Unassigned', setup: { chooses: true, holding: 1 }, counts: { accounts: '1' }, says: { accounts: 'Pharos waits in Unassigned' } },
  { name: 'pending cleanup', setup: { cleanup: true }, counts: { computers: '1' }, says: { computers: 'studio removed · cleanup pending' } },
  { name: 'the one-time plan', setup: { planCard: 'first' }, counts: { capacity: '1' }, says: {} },
  { name: 'everything at once', setup: { expired: true, low: true, chooses: true, awaiting: true, holding: 2, cleanup: true, planCard: 'first' }, counts: { accounts: '3', computers: '2', capacity: '1' }, says: { accounts: '2 projects wait in Unassigned', computers: 'studio removed · cleanup pending' } },
]
for (const state of STATES) {
  test(`${state.name}: Needs you only in its own part, and the nav counts match`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 1280, height: 1000 })
    await setup(page, state.setup)
    for (const part of PARTS) {
      await visit(page, part, !!state.setup.chooses)
      await expect.poll(() => navCounts(page)).toEqual(state.counts)
      const own = state.counts[part]
      if (part === 'capacity') {
        // Capacity's only item is the plan card in its own form.
        await expect(needs(page)).toHaveCount(0)
        await expect(page.getByRole('region', { name: 'The plan' })).toHaveCount(own ? 1 : 0)
      } else if (own) {
        await expect(needs(page).getByRole('heading')).toHaveText(own === '1' ? '1 thing needs you' : `${own} things need you`)
        await expect(needs(page)).toContainText(state.says[part]!)
      } else await expect(needs(page)).toHaveCount(0)
      // The footer says the open part's count.
      if (own) await expect(footer(page)).toContainText(`${own} ${own === '1' ? 'needs' : 'need'} you`)
      else await expect(footer(page)).toHaveCount(0)
    }
    expect(errors).toEqual([])
  })
}

test('the new-account and Unassigned items lead to where they are decided', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await setup(page, { chooses: true, awaiting: true, holding: 1 })
  await visit(page, 'accounts', true)
  await needs(page).locator('[data-need^="new:"]').getByRole('button', { name: 'Choose where' }).click()
  await expect(page.locator('#account-use')).toBeInViewport()
  await needs(page).locator('[data-need="holding"]').getByRole('button', { name: 'Choose its context' }).click()
  await expect(page).toHaveURL('/p/PHAROS/settings#project-work-context')
})

test('every old link lands in the right part with the same panel open', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { chooses: true })
  const pane = page.locator('section.pane')
  const build7 = 'd0000000-0000-4000-8000-000000000001'
  // ?account opens the account's panel in Accounts.
  await page.goto(`/settings/accounts?account=${ACCOUNTS.main}`)
  await expect(page).toHaveURL('/settings/accounts')
  await expect(pane.getByRole('heading', { name: 'Codex', exact: true })).toBeVisible()
  await expect(pane.locator(`.use-sec [data-account="${ACCOUNTS.main}"]`)).toBeVisible()
  // ?computer&signin opens the computer's panel in Computers at that sign-in.
  await page.goto(`/settings/accounts?computer=${build7}&signin=${ACCOUNTS.claude}`)
  await expect(page).toHaveURL('/settings/computers')
  await expect(pane.getByRole('heading', { name: 'build-7', exact: true })).toBeVisible()
  await expect(pane.locator(`.si-row.is-target[data-signin="${ACCOUNTS.claude}"]`)).toBeInViewport()
  // An approval link opens the computer that holds the sign-in.
  await page.goto(`/settings/accounts?verify_account=${ACCOUNTS.claude}`)
  await expect(page).toHaveURL(`/settings/computers?verify_account=${ACCOUNTS.claude}`)
  await expect(pane.getByRole('heading', { name: 'build-7', exact: true })).toBeVisible()
  await expect(pane.locator(`.si-row.is-target[data-signin="${ACCOUNTS.claude}"]`)).toBeVisible()
  // #add-account opens Add an account in Accounts.
  await page.goto('/settings/accounts#add-account')
  await expect(page.getByRole('region', { name: 'Add an account' })).toBeVisible()
  await expect(page).toHaveURL('/settings/accounts')
  // #account-use and #quota-warnings stay in Accounts and ring their target.
  await page.goto('/settings/accounts#account-use')
  await expect(page.locator('#account-use')).toHaveClass(/arrived/)
  await page.goto('/settings/accounts#quota-warnings')
  await expect(page.locator('#quota-warnings')).toHaveClass(/arrived/)
  await page.goto('/settings/workspace#quota-warnings')
  await expect(page).toHaveURL('/settings/accounts#quota-warnings')
  // #capacity-and-load opens Capacity with the pacing editors focused.
  await page.goto('/settings/accounts#capacity-and-load')
  await expect(page).toHaveURL('/settings/capacity')
  await expect(page.locator('#capacity-and-load').getByRole('radio', { name: '5' })).toBeFocused()
  await page.goto('/settings/workspace#capacity-and-load')
  await expect(page).toHaveURL('/settings/capacity')
  await page.goto('/settings/capacity#when')
  await expect(page).toHaveURL('/settings/capacity')
  await expect(page.locator('#capacity-and-load').getByRole('radio', { name: '5' })).toBeFocused()
  // In-app links take the same way.
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/settings/accounts?computer=d0000000-0000-4000-8000-000000000001')))
  await expect(page).toHaveURL('/settings/computers')
  await expect(pane.getByRole('heading', { name: 'build-7', exact: true })).toBeVisible()
  expect(errors).toEqual([])
})

test('Verify again keeps its calm row; the count leaves and nothing in the nav or the part moves', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { claude } = await setup(page, { expired: true })
  const posts: unknown[] = []
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => {
    posts.push(route.request().postDataJSON())
    Object.assign(claude, { verification_state: 'completed', verification_run_id: RUN, verified_at: new Date(NOW).toISOString(), verification_expires_at: new Date(NOW + 86_400_000).toISOString() })
    return route.fulfill({ json: { account_id: ACCOUNTS.claude, run_id: RUN } })
  })
  await visit(page, 'computers')
  await expect.poll(() => navCounts(page)).toEqual({ computers: '1' })
  const row = needs(page).locator(`[data-need$=":${ACCOUNTS.claude}"]`)
  const verify = row.getByRole('button', { name: 'Verify again' })
  const guard = await controlStability(page, {
    accounts: navLink(page, 'accounts'), computers: navLink(page, 'computers'), capacity: navLink(page, 'capacity'),
    head: page.locator('.page-head h2'), grid: page.locator('.ct-wrap'),
  })
  await guard.check(async () => {
    await verify.click()
    await expect(row.getByRole('status')).toHaveText('Verification passed.')
    await expect.poll(() => navCounts(page)).toEqual({})
  })
  guard.done()
  expect(posts).toHaveLength(1)
  // The acted-on row stays as a calm row under "Nothing needs you".
  await expect(needs(page).getByRole('heading')).toHaveText('Nothing needs you')
  await expect(row).toHaveClass(/calm/)
  await expect(footer(page)).toHaveCount(0)
  expect(errors).toEqual([])
})

test('without a paired computer Accounts offers Connect a computer where Add an account would be', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  for (const c of capacity.computers) Object.assign(c, { computer_state: 'revoked', local_cleanup: 'confirmed', accounting_state: 'settled' })
  await visit(page, 'accounts')
  await expect(page.getByRole('button', { name: 'Add an account' })).toHaveCount(0)
  await expect(page.locator('section[aria-labelledby="accounts-list-title"]').getByRole('link', { name: 'Connect a computer' })).toHaveAttribute('href', '/agents/register-agent')
})

for (const [state, options] of [['things need you', { expired: true, low: true, planCard: 'first' }], ['nothing needs you', {}]] as const) {
  test(`every part at 1440 and 390, light and dark, without sideways scrolling: ${state}`, async ({ page }, info) => {
    test.skip(!process.env.AEON1171_SHOTS, 'evidence run only')
    await setup(page, options)
    for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height: width > 600 ? 1000 : 860 })
      await page.emulateMedia({ colorScheme: theme })
      await page.addInitScript(value => localStorage.setItem('aeon-theme', value), theme)
      for (const part of PARTS) {
        await visit(page, part)
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await page.screenshot({ path: info.outputPath(`${part}-${state.replaceAll(' ', '-')}-${width}-${theme}.png`), fullPage: true })
        expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), `${part} at ${width}`).toBeLessThanOrEqual(0)
      }
    }
  })
}
