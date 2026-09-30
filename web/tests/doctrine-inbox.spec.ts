// SPDX-License-Identifier: AGPL-3.0-only
// The doctrine inbox (AEON-444): agent-proposed rule changes wait for a person
// with a word diff, a dot on Settings and Agent rules, and one toast each.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockRules } from './rules-fixtures'

const COMMIT = '21b814057825c06b7f1e93f9deacdb4c549e11c6'
const BLOB = `https://github.com/inspr-at/inspr-modules/blob/${COMMIT}`
const GIT_SHA = '1'.repeat(64)
const TESTS_SHA = 'f'.repeat(64)
const SOURCE = {
  id: 'd0000000-0000-4000-8000-000000000001', repository: 'inspr-at/inspr-modules', visibility: 'public', ref: 'v260922101217.0.0', commit: COMMIT,
  committed_at: '2026-09-22T10:25:19Z', pinned_at: '2026-09-29T08:00:00Z', paths: ['docs/AGENTS-*.md'], url: `https://github.com/inspr-at/inspr-modules/tree/${COMMIT}`,
  state: 'ready', indexed_at: '2026-09-29T08:00:02Z', skipped: [],
  files: [
    {
      path: 'docs/AGENTS-KERNEL.md', blob_sha: 'b'.repeat(40), sha256: 'c'.repeat(64), bytes: 4200, kind: 'kernel', layer: 'company', url: `${BLOB}/docs/AGENTS-KERNEL.md`,
      sets: [{ set: 'git', title: 'Git', anchor: 'git' }],
      rules: [{ key: 't-1123456789abcdef0123', identity: 'inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#git', set: 'git', heading_path: 'Kernel / Git', anchor: 'git', start_line: 15, end_line: 15, source: '- Small commits.\n', sha256: GIT_SHA, text: 'Small commits.', strength: 'normal', url: `${BLOB}/docs/AGENTS-KERNEL.md?plain=1#L15`, tldr: { en: 'Small commits.' } }],
    },
    {
      path: 'docs/AGENTS-DOMAIN-DEV.md', blob_sha: 'd'.repeat(40), sha256: 'e'.repeat(64), bytes: 900, kind: 'domain', layer: 'company', url: `${BLOB}/docs/AGENTS-DOMAIN-DEV.md`,
      sets: [{ set: 'tests', title: 'Tests', anchor: 'tests' }],
      rules: [{ key: 't-2', identity: 'inspr-at/inspr-modules/docs/AGENTS-DOMAIN-DEV.md#tests', set: 'tests', heading_path: 'Dev / Tests', anchor: 'tests', start_line: 5, end_line: 5, source: '- Tests are part of done.\n', sha256: TESTS_SHA, text: 'Tests are part of done.', strength: 'normal', url: `${BLOB}/docs/AGENTS-DOMAIN-DEV.md?plain=1#L5` }],
    },
  ],
}

const item = (patch: Record<string, unknown>) => ({
  source_id: SOURCE.id, repository: SOURCE.repository, state: 'pending', head_sha: '', pr_number: 0, pr_url: '', proposed_by: 'agent-builder',
  gate_ready: false, pinned_machines: 0, inbox: true, proposer: 'builder', proposer_kind: 'agent', ...patch,
})
const ESTIMATE = item({
  id: '44400000-0000-4000-8000-000000000001', path: 'docs/AGENTS-DOMAIN-DEV.md', rule_key: 't-2', heading: 'Tests', label: 'Estimate before work, and report a live ETA in every heartbeat.',
  base: '- Tests are part of done.', base_sha256: TESTS_SHA, proposed: '- Tests and a written estimate are part of done.', tldr: { en: 'Estimate before work, and report a live ETA in every heartbeat.' },
  why: 'INSPR-491: an estimate before work makes every heartbeat ETA honest, and reviewers can see drift early.',
  diff: [{ op: 'eq', text: '- Tests ' }, { op: 'ins', text: 'and a written estimate ' }, { op: 'eq', text: 'are part of done.' }],
  ticket: 'INSPR-491', ticket_href: '/p/INSPR/INSPR-491', created_at: new Date(Date.now() - 12 * 60_000).toISOString(),
})
const REVIEWED = item({
  id: '44400000-0000-4000-8000-000000000002', path: 'docs/AGENTS-KERNEL.md', rule_key: 't-1123456789abcdef0123', heading: 'Git', label: 'Keep commits small and reviewed.',
  base: '- Small commits.', base_sha256: GIT_SHA, proposed: '- Small, reviewed commits.', tldr: { en: 'Keep commits small and reviewed.' },
  why: 'Reviews catch more in small commits.', diff: [{ op: 'del', text: '- Small' }, { op: 'ins', text: '- Small,' }, { op: 'eq', text: ' ' }, { op: 'ins', text: 'reviewed ' }, { op: 'eq', text: 'commits.' }],
  created_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
})

interface Inbox { items: Record<string, unknown>[]; calls: { method: string; path: string; body?: unknown }[]; proposals: Record<string, unknown>[] }

async function setup(page: Page, items: Record<string, unknown>[]): Promise<Inbox> {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  await mockRules(page)
  const inbox: Inbox = { items: [...items], calls: [], proposals: [] }
  const notified = new Set<string>()
  await page.route('**/api/me/permissions**', route => {
    const url = new URL(route.request().url())
    const answer = mockEffectivePermissions('admin', url.searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'rules.read', 'rules.write', 'rules.publish']
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/rules/doctrine**', route => route.fulfill({ json: { sources: [SOURCE], proposals_enabled: true } }))
  await page.route('**/api/rules/doctrine/proposals**', route => route.fulfill({ json: { proposals: inbox.proposals } }))
  await page.route('**/api/rules/doctrine/inbox**', route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    inbox.calls.push({ method, path, body: method === 'GET' ? undefined : request.postDataJSON() })
    if (path.endsWith('/summary')) return route.fulfill({ json: { pending: inbox.items.length, items: inbox.items.map(i => ({ id: i.id, label: i.label, created_at: i.created_at, notified: notified.has(i.id) })) } })
    if (path.endsWith('/notified')) {
      // The server grants each person one claim per waiting proposal.
      const id = path.split('/').at(-2)!
      const claimed = inbox.items.some(i => i.id === id) && !notified.has(id)
      notified.add(id)
      return route.fulfill({ json: { claimed } })
    }
    if (method === 'GET') return route.fulfill({ json: { pending: inbox.items.length, items: inbox.items } })
    const id = path.split('/').at(-2)
    const target = inbox.items.find(i => i.id === id)!
    inbox.items = inbox.items.filter(i => i.id !== id)
    if (path.endsWith('/dismiss')) return route.fulfill({ json: { ...target, state: 'dismissed', dismiss_reason: (request.postDataJSON() as { reason: string }).reason } })
    const proposal = { ...target, state: 'proposed', pr_number: 17, pr_url: 'https://github.com/inspr-at/inspr-modules/pull/17', head_sha: '2'.repeat(40), submitted_by: 'person' }
    inbox.proposals = [proposal]
    return route.fulfill({ json: proposal })
  })
  return inbox
}

const doctrine = (page: Page) => page.getByRole('region', { name: 'Doctrine' })
const inboxRegion = (page: Page) => page.getByRole('region', { name: /Proposed changes/ })

test('agent proposals wait in the doctrine inbox with a diff, a dot and one toast each', async ({ page }) => {
  const errors = watchErrors(page)
  const mock = await setup(page, [ESTIMATE, REVIEWED])
  await page.goto('/settings/agent-rules')

  // One toast for the new proposals, with a way to review them.
  const toast = page.getByText('2 doctrine changes proposed')
  await expect(toast).toBeVisible()
  await expect(page.getByRole('button', { name: 'Review', exact: true })).toBeVisible()
  // A neutral dot on Settings (the gear) and on the Agent rules tab.
  await expect(page.getByRole('button', { name: 'App and workspace, 2 doctrine proposals wait' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Settings sections' }).getByRole('img', { name: '2 doctrine proposals wait' })).toBeVisible()

  const layer = doctrine(page)
  await expect(layer.getByRole('link', { name: '2 proposed changes' })).toHaveText('2')
  const inbox = inboxRegion(page)
  await expect(inbox.getByRole('heading', { name: 'Proposed changes · 2' })).toBeVisible()
  const estimate = inbox.getByRole('article', { name: 'Estimate before work, and report a live ETA in every heartbeat' })
  await expect(estimate.locator('ins')).toContainText('and a written estimate')
  await expect(estimate.getByText('builder')).toBeVisible()
  await expect(estimate.getByRole('link', { name: 'INSPR-491' })).toHaveAttribute('href', '/p/INSPR/INSPR-491')
  await expect(estimate.getByText(/an estimate before work makes every heartbeat ETA honest/)).toBeVisible()
  const reviewed = inbox.getByRole('article', { name: 'Keep commits small and reviewed' })
  await expect(reviewed.locator('del')).toContainText('- Small')

  if (process.env.DOCTRINE_SHOTS) {
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      for (const theme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme: theme })
        await layer.scrollIntoViewIfNeeded()
        await page.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/settings__${width}__${theme}.png` })
        await inbox.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/inbox__${width}__${theme}.png` })
      }
    }
    await page.setViewportSize({ width: 1600, height: 1000 })
    await page.emulateMedia({ colorScheme: 'light' })
  }
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
  const axe = await new AxeBuilder({ page }).include('#doctrine-inbox').analyze()
  expect(axe.violations.map(v => v.id)).toEqual([])

  // Claimed once on the server, never again: a reload shows no toast.
  await page.reload()
  await expect(inboxRegion(page).getByRole('heading', { name: 'Proposed changes · 2' })).toBeVisible()
  await expect(page.getByText('2 doctrine changes proposed')).toHaveCount(0)

  // Dismiss needs a reason; it goes back to the proposer.
  const again = inboxRegion(page).getByRole('article', { name: 'Keep commits small and reviewed' })
  await again.getByRole('button', { name: 'Dismiss Keep commits small and reviewed' }).click()
  const reason = again.getByRole('textbox', { name: 'Why dismiss it' })
  await expect(again.getByRole('button', { name: 'Dismiss', exact: true })).toBeDisabled()
  if (process.env.DOCTRINE_SHOTS) await again.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/dismiss__1600__light.png` })
  await reason.fill('Review size belongs in the git pack.')
  await again.getByRole('button', { name: 'Dismiss', exact: true }).click()
  await expect(inboxRegion(page).getByRole('heading', { name: 'Proposed changes · 1' })).toBeVisible()
  expect(mock.calls.find(c => c.path.endsWith('/dismiss'))?.body).toEqual({ reason: 'Review size belongs in the git pack.' })

  // Propose PR sends it to git through the normal proposal path.
  await inboxRegion(page).getByRole('button', { name: 'Propose PR for Estimate before work, and report a live ETA in every heartbeat' }).click()
  await expect(page.getByText('Pull request created')).toBeVisible()
  expect(mock.calls.find(c => c.path.endsWith('/pull-request'))?.body).toEqual({})
  await expect(inboxRegion(page)).toHaveCount(0)
  await expect(doctrine(page).getByRole('link', { name: 'inspr-modules · PR #17' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Settings sections' }).getByRole('img', { name: /doctrine proposal/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'App and workspace', exact: true })).toBeVisible()
  expect(errors).toEqual([])
})

test('edit then propose replaces the agent text; an outdated proposal needs an edit', async ({ page }) => {
  const outdated = { ...REVIEWED, outdated: true, base: '- Small commits, one topic each.' }
  const mock = await setup(page, [ESTIMATE, outdated])
  await page.goto('/settings/agent-rules')
  const inbox = inboxRegion(page)
  const stale = inbox.getByRole('article', { name: 'Keep commits small and reviewed' })
  await expect(stale.getByText('The rule changed since this was proposed.', { exact: false })).toBeVisible()
  await expect(stale.getByRole('button', { name: /^Propose PR/ })).toBeDisabled()

  await inbox.getByRole('button', { name: 'Edit Estimate before work, and report a live ETA in every heartbeat, then propose' }).click()
  const dialog = page.getByRole('dialog', { name: 'Edit, then propose' })
  await expect(dialog.getByRole('textbox', { name: 'Rule', exact: true })).toHaveValue(ESTIMATE.proposed as string)
  await expect(dialog.getByRole('textbox', { name: 'Why this change' })).toHaveValue(ESTIMATE.why as string)
  if (process.env.DOCTRINE_SHOTS) {
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      for (const theme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme: theme })
        await dialog.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/edit__${width}__${theme}.png` })
      }
    }
  }
  await dialog.getByRole('textbox', { name: 'TL;DR · English' }).fill('Estimate before work.')
  await dialog.getByRole('button', { name: 'Create pull request' }).click()
  await expect(dialog).toHaveCount(0)
  const body = mock.calls.find(c => c.path.endsWith('/pull-request'))?.body as Record<string, unknown>
  expect(body).toMatchObject({ source: ESTIMATE.proposed, why: ESTIMATE.why, rule_sha256: TESTS_SHA, tldr: { en: 'Estimate before work.' } })
  await expect(inbox.getByRole('heading', { name: 'Proposed changes · 1' })).toBeVisible()
})
