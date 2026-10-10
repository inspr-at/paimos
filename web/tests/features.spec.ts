// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type Locator } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

async function setup(page: Page, admin = true) {
  await mockWork(page, fixtures(), { admin })
  await mockBusiness(page, businessData({ role: admin ? 'admin' : 'member' }), { role: admin ? 'admin' : 'member' })
  await mockSettings(page, settingsData())
  const states = new Map<string, { override: boolean | null; revision: number }>()
  const writes: { enabled: boolean | null; expected_revision: number; project: string }[] = []
  let conflict = false
  let failEvaluation = false
  const state = (project: string) => {
    if (!states.has(project)) states.set(project, { override: null, revision: 0 })
    return states.get(project)!
  }
  const evaluation = (project: string) => {
    const tenant = state(''), local = state(project)
    return { key: 'workspace-summary', label: 'Workspace summary', description: 'Show project and work counts in Workspace settings.', enabled: local.override ?? tenant.override ?? false, source: project && local.override !== null ? 'project' : tenant.override !== null ? 'tenant' : 'default' }
  }
  await page.route('**/api/features**', route => {
    if (failEvaluation) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    const project = new URL(route.request().url()).searchParams.get('project_id') || ''
    return route.fulfill({ json: { project_id: project || null, items: [evaluation(project)] } })
  })
  await page.route('**/api/settings/features**', route => {
    const request = route.request()
    const project = new URL(request.url()).searchParams.get('project_id') || ''
    const local = state(project)
    if (request.method() === 'PUT') {
      const body = request.postDataJSON() as { enabled: boolean | null; expected_revision: number }
      writes.push({ ...body, project })
      if (conflict || body.expected_revision !== local.revision) return route.fulfill({ status: 409, json: { error: 'feature settings changed' } })
      local.override = body.enabled; local.revision++
      return route.fulfill({ json: { feature: { ...evaluation(project), ...local }, event_id: local.revision } })
    }
    return route.fulfill({ json: { project_id: project || null, items: [{ ...evaluation(project), ...local }] } })
  })
  return { writes, conflict: () => { conflict = true }, failEvaluation: () => { failEvaluation = true } }
}

test('a shipped dark feature turns on and off without navigation or deployment', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'Feature flags', exact: true })
  const counts = page.locator('dl[aria-label="Workspace summary"]')
  await expect(card.getByLabel('Workspace summary', { exact: true })).toHaveValue('inherit')
  await expect(counts).toHaveCount(0)
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('on')
  await expect(card.getByRole('status')).toContainText('without a deployment')
  await expect(counts).toBeVisible()
  await expect(counts).toContainText('Active projects')
  expect(world.writes[0]).toEqual({ enabled: true, expected_revision: 0, project: '' })
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('off')
  await expect(counts).toHaveCount(0)
  expect(world.writes[1]).toEqual({ enabled: false, expected_revision: 1, project: '' })
  await expect(page).toHaveURL(/\/settings\/workspace$/)
})

for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
  test(`flag controls stay put through toggles, refresh and scope reload at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await setup(page)
    await page.goto('/settings/workspace')
    const card = page.getByRole('region', { name: 'Feature flags', exact: true })
    const control = card.getByLabel('Workspace summary', { exact: true })
    const scope = card.getByLabel('Apply to')
    const counts = page.locator('dl[aria-label="Workspace summary"]')
    await expect(control).toBeEnabled()
    await control.scrollIntoViewIfNeeded()
    const original = await control.boundingBox()
    const originalScope = await scope.boundingBox()
    expect(original).not.toBeNull()
    expect(originalScope).not.toBeNull()
    const staysPut = async (locator: Locator, box: NonNullable<typeof original>) => {
      const current = await locator.boundingBox()
      expect(current).not.toBeNull()
      for (const axis of ['x', 'y', 'width', 'height'] as const) expect(current![axis]).toBeCloseTo(box[axis], 1)
    }
    const checkControls = async () => {
      await staysPut(control, original!)
      await staysPut(scope, originalScope!)
    }

    await control.selectOption('on')
    await expect(counts).toBeVisible()
    await expect(control).toBeEnabled()
    await checkControls()

    let releaseEvaluation!: () => void
    const evaluationHeld = new Promise<void>(resolve => { releaseEvaluation = resolve })
    await page.route('**/api/features', async route => { await evaluationHeld; await route.fallback() })
    const recheck = page.waitForRequest(request => new URL(request.url()).pathname === '/api/features')
    await page.evaluate(() => window.dispatchEvent(new Event('focus')))
    await recheck
    await expect(counts).toBeVisible()
    await checkControls()
    const refreshed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/features')
    releaseEvaluation()
    await refreshed
    await expect(counts).toBeVisible()
    await checkControls()

    await control.selectOption('off')
    await expect(counts).toHaveCount(0)
    await expect(control).toBeEnabled()
    await checkControls()

    let releaseSettings!: () => void
    const settingsHeld = new Promise<void>(resolve => { releaseSettings = resolve })
    await page.route('**/api/settings/features?project_id=p-pharos', async route => { await settingsHeld; await route.fallback() })
    await scope.selectOption('p-pharos')
    await expect(control).toBeVisible()
    await expect(control).toBeDisabled()
    await checkControls()
    releaseSettings()
    await expect(control).toBeEnabled()
    await expect(control).toHaveValue('inherit')
    await checkControls()
    await control.selectOption('on')
    await expect(card.locator('dl[aria-label="Project summary"]')).toBeVisible()
    await expect(control).toBeEnabled()
    await checkControls()
    await control.selectOption('off')
    await expect(card.locator('dl[aria-label="Project summary"]')).toHaveCount(0)
    await expect(control).toBeEnabled()
    await checkControls()
    await scope.selectOption('')
    await expect(control).toBeEnabled()
    await expect(control).toHaveValue('off')
    await checkControls()
  })
}

test('a failed scope reload retains disabled rows until retry succeeds', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'Feature flags', exact: true })
  const control = card.getByLabel('Workspace summary', { exact: true })
  await expect(control).toBeEnabled()
  let fail = true
  await page.route('**/api/settings/features?project_id=p-pharos', route => fail
    ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fallback())
  await card.getByLabel('Apply to').selectOption('p-pharos')
  await expect(card.getByRole('alert')).toContainText('could not be loaded')
  await expect(control).toBeVisible()
  await expect(control).toBeDisabled()
  expect(world.writes).toHaveLength(0)
  fail = false
  await card.getByRole('button', { name: 'Reload' }).click()
  await expect(control).toBeEnabled()
  await control.selectOption('on')
  await expect(card.getByRole('status')).toContainText('Saved.')
  expect(world.writes).toEqual([{ enabled: true, expected_revision: 0, project: 'p-pharos' }])
})

test('project OFF overrides workspace ON and inherit restores it', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'Feature flags', exact: true })
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('on')
  await expect(card.getByRole('status')).toContainText('Saved.')
  await card.getByLabel('Apply to').selectOption('p-pharos')
  await expect(card.getByLabel('Workspace summary', { exact: true })).toHaveValue('inherit')
  await expect(card).toContainText('Currently on · workspace choice')
  await expect(card.locator('dl[aria-label="Project summary"]')).toBeVisible()
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('off')
  await expect(card).toContainText('Currently off · project override')
  await expect(card.locator('dl[aria-label="Project summary"]')).toHaveCount(0)
  expect(world.writes[1]).toEqual({ enabled: false, expected_revision: 0, project: 'p-pharos' })
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('inherit')
  await expect(card).toContainText('Currently on · workspace choice')
  await expect(card.locator('dl[aria-label="Project summary"]')).toBeVisible()
  expect(world.writes[2]).toEqual({ enabled: null, expected_revision: 1, project: 'p-pharos' })
  await card.getByLabel('Apply to').selectOption('')
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('off')
  await expect(card.getByRole('status')).toContainText('Saved.')
  await card.getByLabel('Apply to').selectOption('p-pharos')
  await expect(card).toContainText('Currently off · workspace choice')
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('on')
  await expect(card.locator('dl[aria-label="Project summary"]')).toBeVisible()
  await expect(page.locator('dl[aria-label="Workspace summary"]')).toHaveCount(0)
})

test('conflicts and failed evaluations keep the dark feature off', async ({ page }) => {
  const world = await setup(page)
  world.conflict()
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'Feature flags', exact: true })
  await card.getByLabel('Workspace summary', { exact: true }).selectOption('on')
  await expect(card.getByRole('alert')).toContainText('Another admin changed this feature')
  await expect(card.getByLabel('Workspace summary', { exact: true })).toHaveValue('inherit')
  await expect(page.locator('dl[aria-label="Workspace summary"]')).toHaveCount(0)
  await card.getByRole('button', { name: 'Reload' }).click()
  await expect(card.getByRole('alert')).toHaveCount(0)
  world.failEvaluation()
  await page.reload()
  await expect(page.locator('dl[aria-label="Workspace summary"]')).toHaveCount(0)
})

test('members receive no rollout administration controls', async ({ page }) => {
  const world = await setup(page, false)
  await page.goto('/settings/workspace')
  await expect(page.getByRole('region', { name: 'Feature flags', exact: true })).toHaveCount(0)
  expect(world.writes).toHaveLength(0)
})
