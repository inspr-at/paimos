// SPDX-License-Identifier: AGPL-3.0-only
// The doctrine inbox (AEON-444): agent-proposed rule changes wait for a person
// with a word diff, a dot on Settings and Agent rules, and one toast each.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { watchErrors } from './work-fixtures'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
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
  await mockDecisionDesk(page)
  await mockSettings(page, settingsData())
  await mockRules(page)
  const inbox: Inbox = { items: [...items], calls: [], proposals: [] }
  const notified = new Set<string>()
  await page.route('**/api/decision-desk/projection?**', route => route.fulfill({ json: {
    items: inbox.items.map(i => ({ id: i.id, kind: 'doctrine', revision: 1, title: i.label, held: false, can_decide: true, created_at: i.created_at, href: `/decision-desk?item=r:${i.id}`, source: `/settings/agent-rules#${i.id}` })),
    counts: { open: inbox.items.length, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString(),
  } }))
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
test('native doctrine proposals retain diff, notices, source links and protected settlement in one desk', async ({ page }) => {
  const errors = watchErrors(page), mock = await setup(page, [ESTIMATE, REVIEWED])
  await page.goto('/settings/agent-rules')
  await expect(page.getByText('2 doctrine changes proposed')).toBeVisible()
  await expect(page.getByRole('button', { name: 'App and workspace, 2 doctrine proposals wait' })).toBeVisible()
  await expect(doctrine(page).getByRole('link', { name: '2 proposed changes' })).toHaveAttribute('href', '/decision-desk')
  await expect(page.getByRole('button', { name: /^Propose PR for/ })).toHaveCount(0)
  await page.getByRole('button', { name: 'Review', exact: true }).click()
  await expect(page).toHaveURL('/decision-desk')
  await page.getByTestId(`desk-row-r:${ESTIMATE.id}`).click()
  const details = page.getByRole('region', { name: 'Rule proposal details', exact: true })
  await expect(page.getByRole('link', { name: 'Open source record', exact: true })).toHaveAttribute('href', `/settings/agent-rules#${ESTIMATE.id}`)
  await expect(details.locator('ins')).toContainText('and a written estimate')
  await expect(details).toContainText('Proposed by builder')
  await expect(page.getByTestId('desk-body')).toContainText('every heartbeat ETA honest')
  const axe = await new AxeBuilder({ page }).include('.desk-dialog').analyze()
  expect(axe.violations.map(v => v.id)).toEqual([])
  // A bookmarked source reopens on reload; opening an overview row is local
  // round state and does not change the overview's URL.
  await page.goto(`/decision-desk?item=r:${ESTIMATE.id}`)
  await expect(details).toBeVisible()
  await page.reload()
  await expect(details).toBeVisible()
  await expect(page.getByText('2 doctrine changes proposed')).toHaveCount(0)
  await page.goto(`/decision-desk?item=r:${REVIEWED.id}`)
  await expect(details.locator('del')).toContainText('- Small')
  await page.getByTestId('choice-1').click()
  const reason = page.getByRole('textbox', { name: 'Reason', exact: true })
  await reason.fill('Review size belongs in the git pack.'); await reason.press('Enter')
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-announcement')).toContainText('Decision recorded')
  expect(mock.calls.find(c => c.path.endsWith('/dismiss'))?.body).toEqual({ reason: 'Review size belongs in the git pack.' })
  await expect(page.getByRole('link', { name: 'Decision Desk, 1 open', exact: true })).toBeVisible()
  await page.goto(`/decision-desk?item=r:${ESTIMATE.id}`)
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-announcement')).toContainText('Decision recorded')
  expect(mock.calls.find(c => c.path.endsWith('/pull-request'))?.body).toEqual({})
  await expect(page.getByRole('link', { name: 'Decision Desk, 0 open', exact: true })).toBeVisible()
  await page.goto('/settings/agent-rules')
  await expect(doctrine(page).getByRole('link', { name: 'inspr-modules · PR #17' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'App and workspace', exact: true })).toBeVisible()
  expect(errors).toEqual([])
})

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`native Edit then propose and outdated protection ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await page.emulateMedia({ colorScheme: theme })
    const outdated = { ...REVIEWED, outdated: true, base: '- Small commits, one topic each.' }
    const mock = await setup(page, [ESTIMATE, outdated])
    await page.goto(`/decision-desk?item=r:${REVIEWED.id}`)
    await expect(page.getByTestId('desk-body')).toContainText('The rule changed since this was proposed.')
    await expect(page.getByTestId('choice-0')).toBeDisabled()
    await expect(page.getByTestId('desk-decide')).toBeDisabled()
    await page.getByTestId('choice-1').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
    expect(mock.calls.filter(c => c.method === 'POST' && !c.path.endsWith('/notified'))).toHaveLength(0)
    await page.goto(`/decision-desk?item=r:${ESTIMATE.id}`)
    const edit = page.getByTestId('desk-edit-rule')
    await expect(edit).toBeEnabled()
    await edit.click()
    const dialog = page.getByRole('dialog', { name: 'Edit, then propose' })
    await expect(dialog.getByRole('textbox', { name: 'Rule', exact: true })).toHaveValue(ESTIMATE.proposed as string)
    await expect(dialog.getByRole('textbox', { name: 'Why this change' })).toHaveValue(ESTIMATE.why as string)
    await expectStableControls({ controls: { cancel: dialog.getByRole('button', { name: 'Cancel', exact: true }), submit: dialog.getByRole('button', { name: 'Create pull request' }), ...(width < 600 ? { frame: dialog } : {}) }, scrollAreas: { body: dialog.locator('.body') }, interactions: [
      { name: 'edit the English summary', run: async () => { await dialog.getByRole('textbox', { name: 'TL;DR · English' }).fill('Estimate before work.') } },
      { name: 'show the German translation below the controls', run: async () => { await dialog.getByText('German TL;DR', { exact: true }).click(); await dialog.getByRole('textbox', { name: 'TL;DR · German' }).fill('Vor Arbeitsbeginn den Aufwand und die voraussichtliche Fertigstellung nachvollziehbar dokumentieren.') } },
    ] })
    const dir = process.env.AEON_DESK_SHOTS || testInfo.outputPath('desk-shots'); await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `doctrine-edit-${width}-${theme}.png`) })
    let release!: () => void, entered!: () => void
    const responseGate = new Promise<void>(resolve => { release = resolve })
    const received = new Promise<void>(resolve => { entered = resolve })
    await page.route(`**/api/rules/doctrine/inbox/${ESTIMATE.id}/pull-request`, async route => { entered(); await responseGate; await route.fallback() })
    const submit = dialog.getByRole('button', { name: /Create pull request|Creating PR|Retry proposal/ })
    try {
      await expectStableControls({ controls: { cancel: dialog.getByRole('button', { name: 'Cancel', exact: true }), submit, ...(width < 600 ? { frame: dialog } : {}) }, interactions: [{ name: 'hold the native proposal response while recording', run: async () => {
        await submit.click(); await received
        await expect(submit).toHaveAccessibleName('Creating PR…')
        await expect(submit).toBeDisabled()
        await expect(dialog).toBeVisible()
        expect(mock.calls.filter(c => c.path.endsWith('/pull-request'))).toHaveLength(0)
      } }] })
    } finally { release() }
    await expect(dialog).toHaveCount(0)
    const body = mock.calls.find(c => c.path.endsWith('/pull-request'))?.body as Record<string, unknown>
    expect(body).toMatchObject({ source: ESTIMATE.proposed, why: ESTIMATE.why, rule_sha256: TESTS_SHA, tldr: { en: 'Estimate before work.' } })
    await expect(page.getByTestId('desk-decide')).toHaveText(/Next/)
    await expect(page.getByRole('link', { name: 'Decision Desk, 1 open', exact: true })).toBeVisible()
  })
}
