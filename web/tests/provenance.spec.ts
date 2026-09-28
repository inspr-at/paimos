// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-27T10:00:00Z')
const agentsHash = 'a'.repeat(64)
const skillHash = 'b'.repeat(64)
const session = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`

function recorded(sessionId: string, version: string, absent: boolean) {
  return {
    session_id: sessionId,
    truncated: false,
    revisions: [{
      id: `rev-${sessionId.slice(0, 8)}`,
      session_id: sessionId,
      revision: 1,
      recorded_at: new Date(now - 5 * 60_000).toISOString(),
      items: [
        { kind: 'agents', logical_name: 'AGENTS.md', hash_kind: 'content', content_sha256: agentsHash, version: 'pin-1', byte_size: 24 },
        { kind: 'skill', logical_name: 'demo/SKILL.md', hash_kind: 'content', content_sha256: skillHash, version: null, byte_size: 8 },
        { kind: 'prompt_template', logical_name: 'prompt-template', hash_kind: absent ? 'absent' : 'content', content_sha256: absent ? null : agentsHash, version, byte_size: null },
      ],
    }],
  }
}

async function world(page: Page, theme: 'light' | 'dark') {
  await page.clock.install({ time: now })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    now, me: me.id,
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: {
      'p-pharos': { key: 'PHAROS', title: 'Pharos' },
      'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud' },
      'n-2': { key: 'PHAROS-12', title: 'PDF worker image' },
    },
  })
  await mockAgents(page, data)
  return data
}

function fulfillStatus(status: number) {
  return (route: Route) => route.fulfill({ status, json: { error: status === 404 ? 'not found' : 'forbidden' } })
}

test('an empty provenance record stays empty inside the open session', async ({ page }) => {
  await world(page, 'light')
  await page.goto(`/agents/${session(2)}`)
  const block = page.getByRole('complementary', { name: 'Session details' }).locator('.provenance')
  // Nothing recorded is not filler: the section is left out.
  await expect(page.getByRole('complementary', { name: 'Session details' }).locator('.now-step')).toBeVisible()
  await expect(block).toHaveCount(0)
})

test('a missing session shows an error and no instruction rows', async ({ page }) => {
  await world(page, 'light')
  await page.route('**/api/projects/*/harness-sessions/*/provenance', fulfillStatus(404))
  await page.goto(`/agents/${session(2)}`)
  const block = page.getByRole('complementary', { name: 'Session details' }).locator('.provenance')
  await expect(block.getByRole('alert')).toHaveText('Instruction versions could not be loaded.')
  await expect(block.locator('.items')).toHaveCount(0)
  await expect(block).not.toContainText('AGENTS.md')
})

test('a forbidden read shows an error and no instruction rows', async ({ page }) => {
  await world(page, 'light')
  await page.route('**/api/projects/*/harness-sessions/*/provenance', fulfillStatus(403))
  await page.goto(`/agents/${session(2)}`)
  const block = page.getByRole('complementary', { name: 'Session details' }).locator('.provenance')
  await expect(block.getByRole('alert')).toHaveText('Instruction versions could not be loaded.')
  await expect(block.locator('.hash')).toHaveCount(0)
})

test('a late provenance response does not attach to the session now open', async ({ page }) => {
  const data = await world(page, 'light')
  const older = session(1)
  const current = session(2)
  let releaseOlder = () => {}
  const held = new Promise<void>(resolve => { releaseOlder = resolve })
  await page.route('**/api/projects/*/harness-sessions/*/provenance', async route => {
    const id = route.request().url().match(/harness-sessions\/([^/]+)\/provenance/)?.[1]
    if (id === older) {
      await held
      await route.fulfill({ json: recorded(older, 'stale-marker', true) })
      return
    }
    await route.fulfill({ json: recorded(current, 'live-marker', true) })
  })
  await page.goto('/agents')
  await page.locator(`[data-row="s:${older}"] .agent-link`).click()
  const block = page.getByRole('complementary', { name: 'Session details' }).locator('.provenance')
  await page.locator(`[data-row="s:${current}"] .agent-link`).click()
  await expect(block).toContainText('live-marker')
  releaseOlder()
  await expect(block).toContainText('live-marker')
  await expect(block).not.toContainText('stale-marker')
  expect(data.sessions.some(item => item.id === older)).toBe(true)
})


for (const pass of [1, 2]) for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`instruction versions render at ${width}px in ${theme}, pass ${pass}`, async ({ page }) => {
    await world(page, theme)
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const id = session(2)
    await page.route('**/api/projects/*/harness-sessions/*/provenance', route => route.fulfill({ json: recorded(id, '260927120000.0.0', true) }))
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${id}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    const block = panel.locator('.provenance')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(block).toContainText('AGENTS.md')
    await expect(block).toContainText('demo/SKILL.md')
    await expect(block).toContainText('No content digest')
    await expect(block).toContainText('260927120000.0.0')
    await expect(block.locator('.hash')).toHaveCount(2)
    await expect(block).not.toContainText(agentsHash)
    const borderLeft = await block.locator('.revision').evaluate(element => getComputedStyle(element).borderLeftWidth)
    const borderTop = await block.evaluate(element => getComputedStyle(element).borderTopWidth)
    expect(borderLeft).toBe('0px')
    expect(borderTop).toBe('0px')
    await block.scrollIntoViewIfNeeded()
    expect(await panel.locator('.scroll').evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
    const dir = resolve('..', '.agent-shots')
    mkdirSync(dir, { recursive: true })
    const name = pass === 2 ? `provenance-${width}-${theme}.png` : `provenance-pass1-${width}-${theme}.png`
    await block.screenshot({ path: resolve(dir, name) })
  })
}
