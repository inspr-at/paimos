// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { join } from 'node:path'
import { fixtures, mockWork } from './work-fixtures'
import { journeyWorld, retryJourneyWorld, mockJourney, type JourneyWorld } from './journey-fixtures'

// Start a real refresh before confirmation opens, then release its response
// while the modal is open. Poll suppression cannot cancel this in-flight GET.
async function delayedRefresh(page: Page, world: JourneyWorld) {
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  const started = new Set<string>()
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (route.request().method() !== 'GET' || !['/api/projects/p-pharos/journey', '/api/approvals'].includes(path)) return route.fallback()
    started.add(path)
    await held
    await route.fulfill({ json: path.endsWith('/journey') ? world.journey : world.approvals })
  })
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect.poll(() => started.size).toBe(2)
  return async () => {
    const response = page.waitForResponse(r => r.url().endsWith('/api/projects/p-pharos/journey'))
    release()
    await response
  }
}

const scenarios = {
  requirements: () => journeyWorld('requirements'),
  build: () => journeyWorld('plan'),
  retry: () => retryJourneyWorld(),
}
type Scenario = keyof typeof scenarios
const actionName = { requirements: 'Approve requirements', build: 'Approve and start build', retry: 'Approve and retry deployment' }
const dialogName = { requirements: 'Agree the requirements?', build: 'Start build?', retry: 'Retry deployment?' }
const suffix = { requirements: '/requirements/agree', build: '/journey/actions', retry: '/journey/actions' }

async function open(page: Page, scenario: Scenario) {
  await mockWork(page, fixtures())
  const world = scenarios[scenario]()
  const calls = await mockJourney(page, world)
  await page.goto('/p/PHAROS?view=journey')
  await expect(page.getByRole('button', { name: actionName[scenario], exact: true })).toBeEnabled()
  return { world, calls }
}

function replaceRequirements(world: JourneyWorld) {
  const original = world.approvals.find(a => a.id === world.journey.next_action.approval_request_id)!
  world.journey.revision++
  world.journey.requirements_revision++
  world.journey.requirements_digest_sha256 = 'b'.repeat(64)
  world.journey.requirements_approval_scope = `journey.requirements.r13.d${world.journey.requirements_digest_sha256}`
  const replacement = { ...original, id: 'ap-req-replacement', scope: world.journey.requirements_approval_scope }
  world.approvals.push(replacement) // Keep A so validating the stale snapshot alone still succeeds.
  world.journey.next_action.approval_request_id = replacement.id
  world.journey.stages.find(s => s.key === 'requirements')!.gate_offer_id = replacement.id
}

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`requirements confirmation rejects in-flight replacement at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const { world, calls } = await open(page, 'requirements')
    const before = { revision: world.journey.revision, scope: world.journey.requirements_approval_scope, request: world.journey.next_action.approval_request_id }
    const finish = await delayedRefresh(page, world)
    await page.getByRole('button', { name: actionName.requirements, exact: true }).click()
    const dialog = page.getByRole('dialog', { name: dialogName.requirements })
    await expect(dialog).toContainText('revision 3')
    replaceRequirements(world)
    world.journey.next_action.label = 'Review refreshed decision'
    await finish()
    await expect(page.getByRole('button', { name: /^(Approve and )?review refreshed decision$/i })).toBeVisible()
    await testInfo.attach('synthetic-confirmation-identities', { body: JSON.stringify({ before, after: { revision: world.journey.revision, scope: world.journey.requirements_approval_scope, request: world.journey.next_action.approval_request_id } }), contentType: 'application/json' })
    if (process.env.EG2_SHOTS) await page.screenshot({ path: join(process.env.EG2_SHOTS, `confirmation-${width}-${theme}.png`), fullPage: true })
    await dialog.getByRole('button', { name: actionName.requirements, exact: true }).click()
    await expect(page.locator('.toast').first()).toBeVisible()
    expect(calls.filter(c => c.method !== 'GET').map(c => ({ path: c.path, body: c.body }))).toEqual([])
    await expect(page.locator('.toast').filter({ hasText: /review.*again/i })).toBeVisible()
    if (process.env.EG2_SHOTS) await page.screenshot({ path: join(process.env.EG2_SHOTS, `review-again-${width}-${theme}.png`), fullPage: true })
    // A new, explicit review may approve B; the stale click never does.
    await page.getByRole('button', { name: /^(Approve and )?review refreshed decision$/i }).click()
    await expect(dialog).toContainText('revision 4')
    await dialog.getByRole('button', { name: /^(Approve and )?review refreshed decision$/i }).click()
    await expect.poll(() => calls.filter(c => c.path.endsWith('/requirements/agree')).length).toBe(1)
    expect(calls.filter(c => c.path.endsWith('/decision')).map(c => c.path)).toEqual(['/api/approvals/ap-req-replacement/decision'])
    expect(calls.find(c => c.path.endsWith('/requirements/agree'))!.body).toMatchObject({ expected_revision: 13, approval_request_id: 'ap-req-replacement' })
  })
}

const changes: { scenario: Scenario; name: string; change: (world: JourneyWorld) => void }[] = [
  { scenario: 'requirements', name: 'journey revision alone', change: w => { w.journey.revision++ } },
  { scenario: 'requirements', name: 'requirements scope without a revision bump', change: w => {
    w.journey.requirements_approval_scope += 'changed'
    w.approvals.find(a => a.id === 'ap-req')!.scope = w.journey.requirements_approval_scope
  } },
  { scenario: 'build', name: 'pending gate revision', change: w => { w.journey.revision++ } },
  { scenario: 'retry', name: 'fresh request identity beside standing evidence', change: w => {
    const original = w.approvals.find(a => a.id === 'ap-fresh-retry')!
    w.approvals.push({ ...original, id: 'ap-next-retry' })
    w.journey.next_action.approval_request_id = 'ap-next-retry'
    w.journey.stages.find(s => s.key === 'deploy')!.gate_offer_id = 'ap-next-retry'
  } },
  { scenario: 'retry', name: 'action identity with the same gate', change: w => { w.journey.next_action.key = 'approve_deploy' } },
  { scenario: 'retry', name: 'release identity with the same request ID', change: w => {
    w.journey.current_release_id = 'r-1'
    w.approvals.find(a => a.id === 'ap-fresh-retry')!.resource_id = 'r-1'
  } },
]
for (const { scenario, name, change } of changes) {
  test(`confirmation rejects changed ${name}`, async ({ page }) => {
    const { world, calls } = await open(page, scenario)
    const finish = await delayedRefresh(page, world)
    await page.getByRole('button', { name: actionName[scenario], exact: true }).click()
    const dialog = page.getByRole('dialog', { name: dialogName[scenario] })
    await expect(dialog).toBeVisible()
    change(world)
    world.journey.next_action.label = 'Review refreshed decision'
    await finish()
    await expect(page.getByRole('button', { name: /^(Approve and )?review refreshed decision$/i })).toBeVisible()
    await dialog.getByRole('button', { name: actionName[scenario], exact: true }).click()
    await expect(page.locator('.toast').first()).toBeVisible()
    expect(calls.filter(c => c.method !== 'GET').map(c => ({ path: c.path, body: c.body }))).toEqual([])
    await expect(page.locator('.toast').filter({ hasText: /review.*again/i })).toBeVisible()
  })
}

for (const scenario of Object.keys(scenarios) as Scenario[]) {
  test(`unchanged in-flight ${scenario} confirmation submits the captured request and revision`, async ({ page }) => {
    const { world, calls } = await open(page, scenario)
    const captured = { expected_revision: world.journey.revision, approval_request_id: world.journey.next_action.approval_request_id }
    const finish = await delayedRefresh(page, world)
    await page.getByRole('button', { name: actionName[scenario], exact: true }).click()
    await finish()
    await page.getByRole('dialog', { name: dialogName[scenario] }).getByRole('button', { name: actionName[scenario], exact: true }).click()
    await expect.poll(() => calls.filter(c => c.path.endsWith(suffix[scenario])).length).toBe(1)
    expect(calls.find(c => c.path.endsWith(suffix[scenario]))!.body).toMatchObject(captured)
    expect(calls.filter(c => c.path.endsWith('/decision')).map(c => c.path)).toEqual([`/api/approvals/${captured.approval_request_id}/decision`])
  })

  test(`changed ${scenario} refresh during approval cannot advance the confirmed action`, async ({ page }) => {
    const { world, calls } = await open(page, scenario)
    const requested = world.journey.next_action.approval_request_id
    let releaseDecision!: () => void
    const held = new Promise<void>(resolve => { releaseDecision = resolve })
    let deciding = false
    await page.route(`**/api/approvals/${requested}/decision`, async route => {
      deciding = true
      await held
      await route.fallback()
    })
    const finish = await delayedRefresh(page, world)
    await page.getByRole('button', { name: actionName[scenario], exact: true }).click()
    await page.getByRole('dialog', { name: dialogName[scenario] }).getByRole('button', { name: actionName[scenario], exact: true }).click()
    await expect.poll(() => deciding).toBe(true)
    world.journey.revision++
    world.journey.next_action.label = 'Review refreshed decision'
    await finish()
    // The action itself correctly reads "Working…" while its POST is held;
    // the shared header projection proves the refreshed state was committed.
    await expect(page.getByRole('button', { name: /^Journey:.*Next: Review refreshed decision/ })).toBeVisible()
    releaseDecision()
    await expect(page.locator('.toast').filter({ hasText: /review.*again/i })).toBeVisible()
    // A was already sent while it still matched; no later action or B approval.
    expect(calls.filter(c => c.method !== 'GET').map(c => c.path)).toEqual([`/api/approvals/${requested}/decision`])
  })

  test(`unseen ${scenario} server change receives the captured revision and is rejected`, async ({ page }) => {
    const { world, calls } = await open(page, scenario)
    const captured = { expected_revision: world.journey.revision, approval_request_id: world.journey.next_action.approval_request_id }
    await page.getByRole('button', { name: actionName[scenario], exact: true }).click()
    // No refresh delivers this server revision before the human submits.
    world.journey.revision++
    await page.getByRole('dialog', { name: dialogName[scenario] }).getByRole('button', { name: actionName[scenario], exact: true }).click()
    await expect(page.locator('.toast').filter({ hasText: 'The journey changed meanwhile' })).toBeVisible()
    expect(calls.filter(c => c.path.endsWith(suffix[scenario]))).toHaveLength(1)
    expect(calls.find(c => c.path.endsWith(suffix[scenario]))!.body).toMatchObject(captured)
    expect(world.journey.revision).toBe(13) // The rejected write did not move it.
  })
}
