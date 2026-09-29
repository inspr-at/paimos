// SPDX-License-Identifier: AGPL-3.0-only
// U10: the project journey inside the project page. The rail with the one next
// action, gates as approvals, the plan with tri-state features, the release walker
// (A3), intake, the blocked deploy gate and the footer's flow pill.
import { test, expect, type Page } from '@playwright/test'
import { join } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { journeyWorld, retryJourneyWorld, mockJourney, type JourneyStart, type WorldOptions } from './journey-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

async function open(page: Page, start: JourneyStart = 'plan', path = '/p/PHAROS?view=journey', options: WorldOptions & { failPlan?: boolean; kind?: 'person' | 'agent'; noTicketRoute?: boolean } = {}) {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld(start, options)
  const calls = await mockJourney(page, world, options)
  await page.goto(path)
  await expect(page.getByRole('navigation', { name: 'Project journey' })).toBeVisible()
  return { world, calls }
}
const rail = (page: Page) => page.getByRole('navigation', { name: 'Project journey' })
const writes = (calls: { method: string; path: string }[], suffix: string) => calls.filter(c => c.method !== 'GET' && c.path.endsWith(suffix))

test('Journey is a third view of the project, with the stage and next action in the footer', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('plan'))
  await page.goto('/p/PHAROS')
  const chip = page.locator('footer.app-footer').getByRole('button', { name: /^Journey: Plan, stage 4 of 8\. Next: Start build/ })
  await expect(chip).toBeVisible()
  await expect(page.locator('.project-head .journey-chip')).toHaveCount(0)
  await page.getByRole('tablist', { name: 'Project sections' }).getByRole('tab', { name: 'Journey' }).click()
  await expect(page).toHaveURL('/p/PHAROS/journey')
  await expect(rail(page).locator('li')).toHaveCount(8)
  await expect(rail(page).getByRole('button', { name: '4. Plan, now' })).toHaveAttribute('aria-current', 'step')
  await expect(rail(page).getByRole('button', { name: '1. Inspire, done' })).toBeVisible()
  // The rail only navigates; the one primary button is on the decision card (the gate is requested, so it approves and starts).
  await expect(rail(page).locator('.btn.primary, .stage-cta')).toHaveCount(0)
  await expect(page.locator('.journey-view .gate-btn:visible')).toHaveText(['Approve and start build'])
  await expect(page.getByRole('heading', { name: 'Plan release 2' })).toBeVisible()
  // [ and ] move between stages.
  await page.locator('body').click({ position: { x: 5, y: 400 } })
  await page.keyboard.press(']')
  await expect(page).toHaveURL(/stage=build/)
  await page.keyboard.press('[')
  await expect(page).toHaveURL(/stage=plan/)
  // Back to the list.
  await page.getByRole('tablist', { name: 'Project sections' }).getByRole('tab', { name: 'Tickets' }).click()
  await expect(page.locator('tr.ticket-row').first()).toBeVisible()
  expect(errors).toEqual([])
})

test('the plan: ticked tickets form the release; features cycle all, none and the last partial pick', async ({ page }) => {
  const { calls } = await open(page)
  const feature = page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 2 of 3 in the release/ })
  await expect(feature).toHaveAttribute('aria-checked', 'mixed')
  await feature.click()
  await expect(page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 3 of 3/ })).toHaveAttribute('aria-checked', 'true')
  await page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 3 of 3/ }).click()
  await expect(page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 0 of 3/ })).toHaveAttribute('aria-checked', 'false')
  // The third click restores the earlier partial pick (PHAROS-11 and PHAROS-13).
  await page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 0 of 3.*restore the earlier 2/ }).click()
  await expect(page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 2 of 3/ })).toHaveAttribute('aria-checked', 'mixed')
  await expect.poll(() => writes(calls, '/plan').length).toBe(3)
  const plans = writes(calls, '/plan').map(c => c.body as { expected_revision: number; ordered_ticket_ids: string[]; included_ticket_ids: string[] })
  expect(plans.map(p => p.expected_revision)).toEqual([7, 8, 9])
  expect(plans[0].included_ticket_ids).toEqual(['n-1', 'n-2', 'n-3'])
  expect(plans[1].included_ticket_ids).toEqual([])
  expect(plans[2].included_ticket_ids).toEqual(['n-1', 'n-3'])
  expect(plans[0].ordered_ticket_ids).toEqual(['n-1', 'n-2', 'n-3', 'n-4'])
  // One ticket's box.
  await page.getByRole('checkbox', { name: 'PHAROS-14 in the release' }).check()
  await expect.poll(() => writes(calls, '/plan').length).toBe(4)
  expect((writes(calls, '/plan')[3].body as { included_ticket_ids: string[] }).included_ticket_ids).toEqual(['n-1', 'n-3', 'n-4'])
  // The epic key is a link that opens a new window.
  await expect(page.locator('.release-tickets .ekey').first()).toHaveAttribute('target', '_blank')
})

test('the one next action approves the gate and starts the build; the stage follows', async ({ page }) => {
  const { calls } = await open(page)
  await expect(page.getByRole('region', { name: 'Decision: Release 2' }).getByRole('listitem', { name: /Build gate on Release 2, asked by/ })).toBeVisible()
  await page.getByRole('region', { name: 'Decision: Release 2' }).getByRole('button', { name: 'Approve and start build' }).click()
  const dialog = page.getByRole('dialog', { name: 'Start build?' })
  await expect(dialog).toContainText('This approves the build gate')
  await dialog.getByRole('button', { name: 'Approve and start build' }).click()
  await expect(page).toHaveURL(/stage=build/)
  await expect(page.getByRole('heading', { name: 'Build release 2' })).toBeVisible()
  await expect(rail(page).getByRole('button', { name: '5. Build, now' })).toHaveAttribute('aria-current', 'step')
  const decision = writes(calls, '/decision')
  expect(decision).toHaveLength(1)
  expect(decision[0].body).toEqual({ decision: 'approved', reason: '' })
  const action = writes(calls, '/journey/actions')
  expect(action).toHaveLength(1)
  expect(action[0].body).toMatchObject({ action: 'start_build', expected_revision: 12, approval_request_id: 'ap-build', release_id: 'r-2' })
  expect(typeof (action[0].body as { idempotency_key: string }).idempotency_key).toBe('string')
})

test('without a requested gate the next action waits and says why', async ({ page }) => {
  const { calls } = await open(page, 'plan', '/p/PHAROS?view=journey', { noGate: true })
  const button = page.getByRole('region', { name: 'Decision: Release 2' }).getByRole('button', { name: /Start build/ })
  await expect(button).toBeDisabled()
  await expect(button).toHaveAttribute('data-tip', 'Build start needs an approved gate.')
  await expect(page.getByText('An agent asks for the build gate on Release 2')).toBeVisible()
  expect(writes(calls, '/journey/actions')).toHaveLength(0)
})

test('the walker (A3): release eyebrow, features and tickets, arrows, Space, the sheet and Esc', async ({ page }) => {
  const { calls } = await open(page)
  await page.getByRole('button', { name: 'Full screen' }).click()
  const walker = page.getByRole('dialog', { name: 'Release walker' })
  await expect(walker).toBeVisible()
  await expect(page).toHaveURL(/walk=PHAROS-11/)
  const bar = walker.locator('.walker-bar')
  await expect(bar.locator('.rt2 .eyebrow')).toHaveText('Release 2')
  await expect(bar.locator('.count')).toHaveText('2 of 4')
  // Feature line: its name, and the epic key as the only link (new window).
  await expect(bar.locator('.fg').first().locator('.fn')).toContainText('Guarded multi-cloud provisioning')
  const line = bar.locator('.fg').first().locator('.fl')
  await expect(line.getByRole('link')).toHaveCount(1)
  await expect(line.getByRole('link')).toHaveText('PHAROS-10')
  await expect(line.getByRole('link')).toHaveAttribute('target', '_blank')
  // The current chip shows its title; its checkbox shows because it is current.
  await expect(bar.locator('.ck.on .tt')).toContainText('Connect Hetzner')
  // Screens: the ticket's image attachments.
  await expect(walker.locator('img.shot')).toBeVisible()
  await expect(walker.getByRole('group', { name: 'Screens' }).getByRole('button')).toHaveCount(5)
  await page.keyboard.press('ArrowRight')
  await expect(page).toHaveURL(/walk=PHAROS-12/)
  await expect(bar.locator('.ck.on .tt')).toContainText('Oracle')
  // Space includes PHAROS-12.
  await page.keyboard.press(' ')
  await expect.poll(() => writes(calls, '/plan').length).toBe(1)
  expect((writes(calls, '/plan')[0].body as { included_ticket_ids: string[] }).included_ticket_ids).toEqual(['n-1', 'n-2', 'n-3'])
  await expect(bar.locator('.count')).toHaveText('3 of 4')
  // Shift+→ goes to the next feature (the loose tickets), → wraps around.
  await page.keyboard.press('Shift+ArrowRight')
  await expect(page).toHaveURL(/walk=PHAROS-14/)
  await page.keyboard.press('ArrowRight')
  await expect(page).toHaveURL(/walk=PHAROS-11/)
  // ? opens the sheet, Esc closes it, then the walker.
  await page.keyboard.press('?')
  await expect(walker.getByRole('dialog', { name: 'Walker shortcuts' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(walker.getByRole('dialog', { name: 'Walker shortcuts' })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(walker).toHaveCount(0)
  await expect(page).not.toHaveURL(/walk=/)
})

test('the walker checkbox shows only over its slot, never over the chip', async ({ page }) => {
  await open(page, 'plan', '/p/PHAROS?view=journey&walk=PHAROS-11')
  const walker = page.getByRole('dialog', { name: 'Release walker' })
  const chip = walker.locator('.ck').filter({ hasText: 'PHAROS-13' })
  await chip.locator('.go').hover()
  await expect(chip.locator('.cb')).toBeHidden()
  await chip.locator('.slot').hover()
  await expect(chip.locator('.cb')).toBeVisible()
})

test('an earlier release reads without boxes and its walker cannot change it', async ({ page }) => {
  const { calls } = await open(page, 'plan', '/p/PHAROS?view=journey&stage=plan&release=PHAROS-30')
  await expect(page.getByRole('heading', { name: 'Plan release 1' })).toBeVisible()
  await expect(page.locator('.release-tickets input[type=checkbox]')).toHaveCount(0)
  await expect(page.locator('.release-tickets .fcb')).toHaveCount(0)
  await page.keyboard.press('w')
  const walker = page.getByRole('dialog', { name: 'Release walker' })
  await expect(walker.locator('.cb')).toHaveCount(0)
  await page.keyboard.press(' ')
  expect(writes(calls, '/plan')).toHaveLength(0)
})

test('Inspire shows the sources, the transcript and cited drafts; accepting the brief moves the next action', async ({ page }) => {
  const { calls } = await open(page, 'inspire')
  await expect(page.getByRole('heading', { name: 'Conversation and sources' })).toBeVisible()
  await expect(page.getByText('Voice conversation with Markus', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '00:31', exact: false }).first().click()
  await expect(page.locator('#turn-t-1')).toBeVisible()
  await expect(page.locator('#turn-t-1')).toHaveClass(/j-flash/)
  await page.getByRole('button', { name: 'Accept draft' }).click()
  await expect.poll(() => writes(calls, '/accept').length).toBe(1)
  expect(writes(calls, '/accept')[0].body).toEqual({ expected_base_event_id: 42 })
  await expect(page.getByRole('region', { name: 'Decision: Confirm the brief' }).getByRole('button', { name: 'Confirm brief' })).toBeEnabled()
})

test('Shape decides with the gate: park needs a reason', async ({ page }) => {
  const { calls } = await open(page, 'shape')
  await expect(page.getByRole('listitem', { name: /Shape gate on the project, asked by/ })).toBeVisible()
  await page.getByRole('button', { name: /^Park/ }).click()
  await page.getByLabel('Why park it?').fill('Waiting for the provider contract.')
  await page.getByRole('button', { name: 'Record' }).click()
  await expect.poll(() => writes(calls, '/journey/actions').length).toBe(1)
  expect(writes(calls, '/decision')[0].body).toEqual({ decision: 'approved', reason: '' })
  expect(writes(calls, '/journey/actions')[0].body).toMatchObject({ action: 'park', approval_request_id: 'ap-shape', reason: 'Waiting for the provider contract.' })
  expect((writes(calls, '/journey/actions')[0].body as Record<string, unknown>).release_id).toBeUndefined()
})

test('requirements agree with the gate scoped to the revision', async ({ page }) => {
  const { calls } = await open(page, 'requirements')
  await expect(page.getByText('A new host is provisioned only after its price is approved.')).toBeVisible()
  await page.getByRole('region', { name: 'Decision: Approve requirements' }).getByRole('button', { name: /Approve requirements/ }).click()
  await page.getByRole('dialog', { name: 'Agree the requirements?' }).getByRole('button', { name: /Approve and approve requirements|Approve requirements/ }).click()
  await expect.poll(() => writes(calls, '/requirements/agree').length).toBe(1)
  expect(writes(calls, '/requirements/agree')[0].body).toMatchObject({ approval_request_id: 'ap-req', expected_revision: 12 })
})

test('Deploy shows closed launch admission as a check with its reason, one decision and the refused handoff', async ({ page }) => {
  await open(page, 'deploy')
  // One decision card; the closed admission is a waiting check, not a second alarm card.
  await expect(page.getByRole('region', { name: /^Blocked:/ })).toHaveCount(0)
  await expect(page.locator('.deploy-checks')).toContainText('Pharos launch checks are unavailable')
  await expect(page.getByText('The host policy refused it')).toBeVisible()
  await expect(page.getByText('Launch admission · closed')).toBeVisible()
  const card = page.getByRole('region', { name: 'Decision: The host did not apply it' })
  await expect(card).toContainText('Pharos deploys Release 2')
  await expect(page.locator('.btn.primary:visible')).toHaveCount(1)
})

test('a stale plan write restores the list and says so', async ({ page }) => {
  await open(page, 'plan', '/p/PHAROS?view=journey', { failPlan: true })
  await page.getByRole('checkbox', { name: 'PHAROS-14 in the release' }).check()
  await expect(page.locator('.toast').filter({ hasText: 'The plan changed elsewhere' })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'PHAROS-14 in the release' })).not.toBeChecked()
})

test('an agent principal reads the journey but cannot move it', async ({ page }) => {
  const { calls } = await open(page, 'plan', '/p/PHAROS?view=journey', { kind: 'agent' })
  await expect(page.locator('.release-tickets input[type=checkbox]')).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Decision: Release 2' }).getByRole('button', { name: /start build/i })).toBeDisabled()
  expect(calls.filter(c => c.method !== 'GET')).toHaveLength(0)
})

test('on a phone the rail scrolls and says what is next', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('plan'))
  await page.goto('/p/PHAROS?view=journey')
  await expect(rail(page).locator('.next-line')).toContainText('Approve and start build')
  await expect(rail(page).getByRole('button', { name: '4. Plan, now' })).toBeInViewport()
  const width = await page.evaluate(() => document.documentElement.scrollWidth)
  expect(width).toBeLessThanOrEqual(390)
})

test('Plan adds a ticket to the release, under a feature', async ({ page }) => {
  const { calls } = await open(page)
  await page.getByLabel('New ticket for this release').fill('Show the provider price before approval')
  await page.getByLabel('Feature of the new ticket').selectOption({ label: 'Guarded multi-cloud provisioning' })
  await page.getByRole('button', { name: 'Add', exact: true }).click()
  await expect(page.locator('.toast').filter({ hasText: 'PHAROS-44 joins release 2.' })).toBeVisible()
  await expect(page.locator('.release-tickets').getByText('Show the provider price before approval')).toBeVisible()
  const add = writes(calls, '/tickets')
  expect(add).toHaveLength(1)
  expect(add[0].body).toMatchObject({ title: 'Show the provider price before approval', feature_node_id: 'n-epic', included: true, expected_revision: 7 })
})

test('adding a ticket on a server without the route says so', async ({ page }) => {
  await open(page, 'plan', '/p/PHAROS?view=journey', { noTicketRoute: true })
  await page.getByLabel('New ticket for this release').fill('Anything')
  await page.getByRole('button', { name: 'Add', exact: true }).click()
  await expect(page.locator('.toast').filter({ hasText: 'This server cannot add tickets to a plan yet.' })).toBeVisible()
})

test('a mature project leads with its build: progress, what is left, releases; Inspire and Shape are history', async ({ page }) => {
  await open(page, 'build', '/p/PHAROS?view=journey', { derived: true })
  await expect(page.getByRole('heading', { name: 'Build release 2' })).toBeVisible()
  await expect(page.getByRole('list', { name: 'Tickets by status' })).toContainText('1 in QA')
  await expect(page.getByRole('region', { name: 'What stands before the candidate' })).toContainText('still open')
  await expect(page.getByRole('list', { name: 'Releases, newest first' }).getByRole('listitem')).toHaveCount(2)
  // The plan cannot change during the build.
  await page.goto('/p/PHAROS?view=journey&stage=plan')
  await expect(page.locator('.release-tickets .tk').first()).toBeVisible()
  await expect(page.locator('.release-tickets input[type=checkbox]')).toHaveCount(0)
  // U24: an imported project shows what it brought where a new one shows its conversation.
  await page.goto('/p/PHAROS?view=journey&stage=inspire')
  await expect(page.getByRole('heading', { name: 'Where it came from' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Imported: Started in Paimos' })).toBeVisible()
  await expect(page.getByRole('list', { name: 'What the project brought' })).toBeVisible()
  // Nothing was recorded here, so there is no empty history to unfold.
  await expect(page.locator('section.history')).toHaveCount(0)
})

test('a past Inspire of a project started here folds away as history, with its sources', async ({ page }) => {
  await open(page, 'build', '/p/PHAROS?view=journey&stage=inspire')
  const history = page.locator('section.history')
  await expect(history).toContainText('3 sources recorded · 0 drafts accepted.')
  await expect(page.getByText('Sources · stored with the project')).toHaveCount(0)
  await history.getByRole('button', { name: 'Show the sources' }).click()
  await expect(page.getByText('Sources · stored with the project')).toBeVisible()
})

test('the footer pill hides until the project has really started its journey', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('inspire', { noIntake: true }))
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row').first()).toBeVisible()
  await page.waitForTimeout(300)
  await expect(page.locator('.journey-chip')).toHaveCount(0)
})

test('the footer pill shows once there are sources, or when the stage is derived', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('inspire'))
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('button', { name: /^Journey: Inspire, stage 1 of 8\. Next: Continue intake/ })).toBeVisible()
})

test('Deploy follows launch readiness: ready shows a check, blocked shows its reason', async ({ page }) => {
  await open(page, 'deploy', '/p/PHAROS?view=journey', { readiness: { can_admit: false, reason: 'Backup evidence is older than the policy allows.' } })
  await expect(page.locator('.deploy-checks')).toContainText('Backup evidence is older than the policy allows.')
  await expect(page.getByText('Launch admission · closed')).toBeVisible()
  await page.unroute('**/api/**')
  await open(page, 'deploy', '/p/PHAROS?view=journey', { readiness: { can_admit: true, reason: '' } })
  await expect(page.getByText('Launch admission · ready')).toBeVisible()
  await expect(page.getByRole('region', { name: /Blocked: Launch admission/ })).toHaveCount(0)
})

for (const [label, available, description] of [
  ['Apply deployment approval', true, 'The gate is approved. Apply that decision to hand the release to Pharos for deployment.'],
  ['Await deployment evidence', false, 'The approved gate was applied. Pharos now reports the deployment and verification outcome here.'],
] as const) {
  test(`Deploy decision card follows the ${label} action`, async ({ page }) => {
    await mockWork(page, fixtures())
    const world = journeyWorld('deploy')
    const request = world.approvals[0]
    request.decision = 'approved'
    request.decided_by_principal_id = me.id
    world.walkers['r-2'].state = 'deploying'
    world.handoffs = {}
    world.journey.stages = world.journey.stages.map(stage => stage.key === 'deploy' ? {
      ...stage, state: 'current', handoff_id: null, gate_approval_id: available ? null : request.id,
      gate_live: !available, gate_offer_id: available ? request.id : undefined, gate_offer_state: available ? 'approved_live' : undefined, gate_offer_expires_at: available ? request.expires_at : undefined,
    } : stage)
    world.journey.next_action = {
      key: 'approve_deploy', label, stage: 'deploy', available,
      reason: available ? '' : 'Deployment evidence is not terminal.', approval_request_id: available ? request.id : null,
    }
    await mockJourney(page, world)
    for (const width of [1600, 390]) {
      for (const theme of ['light', 'dark'] as const) {
        await page.setViewportSize({ width, height: 900 })
        await page.emulateMedia({ colorScheme: theme })
        await page.goto('/p/PHAROS?view=journey')
        const card = page.getByRole('region', { name: `Decision: ${label}` })
        await expect(card).toContainText(description)
        await expect(card.getByRole('button', { name: label })).toBeEnabled({ enabled: available })
        if (!available) {
          await expect(page.getByText('Launch admission · closed')).toBeVisible()
          await expect(page.getByText('Pharos has not reported a deployment attempt yet.')).toBeVisible()
        }
        if (process.env.EG2_SHOTS) {
          await page.screenshot({ path: join(process.env.EG2_SHOTS, `${available ? 'apply' : 'await'}-${width}-${theme}.png`), fullPage: true, animations: 'disabled' })
          await card.screenshot({ path: join(process.env.EG2_SHOTS, `${available ? 'apply' : 'await'}-${width}-${theme}-card.png`), animations: 'disabled' })
        }
      }
    }
  })
}

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`distinct deployment retry keeps pending and applied authority separate at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme })
    await mockWork(page, fixtures())
    const world = retryJourneyWorld()
    const calls = await mockJourney(page, world)
    await page.goto('/p/PHAROS?view=journey')
    const card = page.getByRole('region', { name: 'Decision: The host did not apply it' })
    const shot = async (state: string) => {
      if (process.env.EG2_SHOTS) {
        await page.screenshot({ path: join(process.env.EG2_SHOTS, `retry-${state}-${width}-${theme}.png`), fullPage: true, animations: 'disabled' })
        await card.screenshot({ path: join(process.env.EG2_SHOTS, `retry-${state}-${width}-${theme}-card.png`), animations: 'disabled' })
      }
    }
    await expect(card).toContainText('Applied by you')
    await expect(card.getByRole('button', { name: 'Approve and retry deployment' })).toBeEnabled()
    await expect(page.getByText('Launch admission · closed')).toBeVisible()
    // One decision, one primary action: the pending request offers only Deny beside it.
    await expect(card.getByRole('button', { name: 'Approve', exact: true })).toHaveCount(0)
    await expect(card.getByRole('button', { name: 'Deny', exact: true })).toBeVisible()
    await shot('pending')
    await card.getByRole('button', { name: 'Approve and retry deployment' }).click()
    await page.getByRole('dialog', { name: 'Retry deployment?' }).getByRole('button', { name: 'Approve and retry deployment' }).click()
    await expect.poll(() => writes(calls, '/journey/actions').length).toBe(1)
    expect(writes(calls, '/journey/actions')[0].body.approval_request_id).toBe('ap-fresh-retry')
    expect(writes(calls, '/decision').map(c => c.path)).toEqual(['/api/approvals/ap-fresh-retry/decision'])
    await expect(page.getByRole('region', { name: 'Decision: Await deployment evidence' })).toContainText('Applied by you')
    expect(calls.some(c => c.body.approval_request_id === 'ap-standing-consumed')).toBe(false)
  })
}

test('distinct deployment retry primary control decides and applies only the fresh pending ID', async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockJourney(page, retryJourneyWorld())
  await page.goto('/p/PHAROS?view=journey')
  await page.getByRole('region', { name: 'Decision: The host did not apply it' }).getByRole('button', { name: 'Approve and retry deployment' }).click()
  await page.getByRole('dialog', { name: 'Retry deployment?' }).getByRole('button', { name: 'Approve and retry deployment' }).click()
  await expect.poll(() => writes(calls, '/journey/actions').length).toBe(1)
  expect(writes(calls, '/decision').map(c => c.path)).toEqual(['/api/approvals/ap-fresh-retry/decision'])
  expect(writes(calls, '/journey/actions')[0].body.approval_request_id).toBe('ap-fresh-retry')
})

for (const state of ['expired', 'revoked', 'missing'] as const) {
  test(`distinct deployment retry fails closed with ${state} action evidence`, async ({ page }) => {
    await mockWork(page, fixtures())
    const world = retryJourneyWorld(state)
    const calls = await mockJourney(page, world)
    await page.goto('/p/PHAROS?view=journey')
    const card = page.getByRole('region', { name: 'Decision: The host did not apply it' })
    await expect(card).toContainText('Applied by you')
    await expect(card.getByRole('button', { name: 'Retry deployment', exact: true })).toBeDisabled()
    await expect(card).not.toContainText('Approved by you')
    await expect(card).toContainText(state === 'missing' ? 'details' : state === 'revoked' ? 'Approval revoked' : 'Approval expired at')
    // The server can later revoke the standing grant without converting it to an offer.
    world.journey.stages.find(s => s.key === 'deploy')!.gate_live = false
    await page.reload()
    await expect(card).toContainText('Applied by you · no longer live')
    await expect(card.getByRole('button', { name: 'Retry deployment', exact: true })).toBeDisabled()
    expect(writes(calls, '/journey/actions')).toHaveLength(0)
    expect(writes(calls, '/decision')).toHaveLength(0)
  })
}

test('distinct deployment retry expires at the grant deadline on screen and inside confirmation', async ({ page }) => {
  const start = Date.now()
  await page.clock.install({ time: start })
  await mockWork(page, fixtures())
  const world = retryJourneyWorld('approved')
  world.journey.stages.find(s => s.key === 'deploy')!.gate_offer_expires_at = new Date(start + 20_000).toISOString()
  const calls = await mockJourney(page, world)
  await page.goto('/p/PHAROS?view=journey')
  const card = page.getByRole('region', { name: 'Decision: The host did not apply it' })
  const action = card.getByRole('button', { name: 'Retry deployment', exact: true })
  await expect(action).toBeEnabled()
  await action.click()
  const dialog = page.getByRole('dialog', { name: 'Retry deployment?' })
  // The grant expires between 15-second display ticks; do not wait for polling.
  await page.clock.fastForward(15_000)
  await expect(action).toBeEnabled()
  await page.clock.fastForward(5_000)
  await expect(action).toBeDisabled()
  await expect(card).toContainText('Approval expired at')
  await expect(card).not.toContainText('Approved by you')
  await dialog.getByRole('button', { name: 'Retry deployment', exact: true }).click()
  await expect(page.getByText('The gate is no longer available. Refresh before taking this step.')).toBeVisible()
  expect(writes(calls, '/journey/actions')).toHaveLength(0)
  expect(writes(calls, '/decision')).toHaveLength(0)
})

test('a member sees the deploy gate without an approval action', async ({ page }) => {
  const { calls } = await open(page, 'deploy')
  await page.route('**/api/me/permissions*', route => route.fulfill({ json: mockEffectivePermissions('member', new URL(route.request().url()).searchParams.get('project_id') ?? undefined) }))
  await page.reload()
  const gate = page.getByRole('listitem', { name: /Deployment gate on Release 2, asked by/ })
  await expect(gate).toBeVisible()
  await expect(gate.getByRole('button', { name: 'Approve' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Approve and retry deployment/ })).toBeDisabled()
  expect(writes(calls, '/journey/actions')).toHaveLength(0)
})

test('Build: marking the candidate approves the build gate and sends mark_candidate', async ({ page }) => {
  const { calls } = await open(page, 'mark')
  const card = page.getByRole('region', { name: 'Decision: Mark release 2 as the candidate' })
  await expect(card.getByRole('listitem', { name: /Build gate on Release 2, asked by/ })).toBeVisible()
  await expect(page.locator('.btn.primary:visible')).toHaveText(['Approve and mark candidate'])
  await card.getByRole('button', { name: 'Approve and mark candidate' }).click()
  await page.getByRole('dialog', { name: 'Mark candidate?' }).getByRole('button', { name: 'Approve and mark candidate' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Release 2 is the candidate.' })).toBeVisible()
  expect(writes(calls, '/journey/actions')[0].body).toMatchObject({ action: 'mark_candidate', approval_request_id: 'ap-mark', release_id: 'r-2' })
  await expect(page.getByRole('region', { name: 'Decision: Approve the release candidate' })).toBeVisible()
})

for (const state of ['expired', 'pending', 'approved_live'] as const) {
  test(`candidate decision card reflects a ${state} gate offer and its deadline`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await mockWork(page, fixtures())
    const world = journeyWorld('mark')
    const expiry = new Date(Date.now() + (state === 'expired' ? -30 : 90) * 60_000).toISOString()
    const gate = world.approvals[0]
    gate.id = 'ap-candidate'
    gate.scope = 'journey.candidate'
    gate.expires_at = expiry
    gate.decision = state === 'pending' ? null : 'approved'
    gate.decided_by_principal_id = gate.decision ? me.id : null
    world.walkers['r-2'].state = 'candidate'
    world.journey.next_action = {
      key: 'approve_candidate', label: 'Approve candidate', stage: 'build', available: state === 'approved_live',
      reason: state === 'expired' ? 'Gate approval expired. The agent asks again for a fresh one.' : state === 'pending' ? 'Candidate review needs an approved gate.' : '',
      approval_request_id: state === 'expired' ? null : gate.id,
    }
    const build = world.journey.stages.find(s => s.key === 'build')!
    build.gate_offer_id = gate.id
    build.gate_offer_state = state
    build.gate_offer_expires_at = expiry
    await mockJourney(page, world)
    await page.goto('/p/PHAROS?view=journey')
    const card = page.getByRole('region', { name: 'Decision: Approve the release candidate' })
    await expect(card).toBeVisible()
    const button = card.getByRole('button', { name: 'Approve candidate' })
    if (state === 'expired') {
      const localExpiry = await page.evaluate(value => new Date(value).toLocaleString(), expiry)
      await expect(card).toContainText(`Approval expired at ${localExpiry}`)
      await expect(card).toContainText('The agent asks again for a fresh gate request.')
      await expect(card).not.toContainText('Approved by you')
      await expect(button).toBeDisabled()
      await expect(button).toHaveAttribute('data-tip', /approval expired/i)
    } else if (state === 'pending') {
      await expect(card.locator('time.expiry')).toHaveAttribute('datetime', expiry)
      await expect(card.locator('time.expiry')).toContainText('Expires in')
      await expect(card).not.toContainText('Approved by you')
    } else {
      await expect(card).toContainText('Approved by you')
      await expect(button).toBeEnabled()
    }
  })
}

test('Plan: a project without a release opens release 1 with one click', async ({ page }) => {
  const { calls } = await open(page, 'open')
  const card = page.getByRole('region', { name: 'Decision: Open release 1' })
  await card.getByRole('button', { name: 'Open release 1' }).click()
  await page.getByRole('dialog', { name: 'Open release 1?' }).getByRole('button', { name: 'Open release 1' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Release 1 is open.' })).toBeVisible()
  const action = writes(calls, '/journey/actions')[0].body as Record<string, unknown>
  expect(action).toMatchObject({ action: 'open_first_release', approval_request_id: null })
  expect(action.release_id).toBeUndefined()
  await expect(page.getByRole('checkbox', { name: 'PHAROS-14 in the release' })).toBeVisible()
})
