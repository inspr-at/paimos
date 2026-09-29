// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockRules } from './rules-fixtures'

const COMMIT = '21b814057825c06b7f1e93f9deacdb4c549e11c6'
const PRIVATE_COMMIT = '3333333333333333333333333333333333333333'
const BLOB = (url: string, path: string) => `https://github.com/${url}/blob/${COMMIT}/${path}`
const kernelRule = (patch: Record<string, unknown>) => ({
  identity: 'inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets', heading_path: 'AGENTS — Kernel / Hard safety / Secrets', anchor: 'secrets',
  sha256: 'a'.repeat(64), strength: 'normal', set: 'hard-safety/secrets', ...patch,
})

const READY = {
  id: 'd0000000-0000-4000-8000-000000000001', repository: 'inspr-at/inspr-modules', visibility: 'public', ref: 'v260922101217.0.0', commit: COMMIT,
  committed_at: '2026-09-22T10:25:19Z', pinned_at: '2026-09-29T08:00:00Z', paths: ['docs/AGENTS-*.md'], url: `https://github.com/inspr-at/inspr-modules/tree/${COMMIT}`,
  state: 'ready', indexed_at: '2026-09-29T08:00:02Z', skipped: [{ path: 'docs/AGENTS-INDEX.md', reason: 'not a doctrine rule file' }],
  files: [
    {
      path: 'docs/AGENTS-KERNEL.md', blob_sha: 'b'.repeat(40), sha256: 'c'.repeat(64), bytes: 4200, kind: 'kernel', layer: 'company', url: BLOB('inspr-at/inspr-modules', 'docs/AGENTS-KERNEL.md'),
      tldr: { en: 'The rules no agent may break.' },
      sets: [{ set: 'hard-safety/secrets', title: 'Hard safety / Secrets', anchor: 'secrets', tldr: { en: 'Secrets never reach a transcript.' } }, { set: 'git', title: 'Git', anchor: 'git' }],
      rules: [
        kernelRule({
          key: 'no-env-dump', start_line: 7, end_line: 9, strength: 'locked',
          source: '<!-- aeon-rule: no-env-dump -->\r\n- 🔴 **NEVER** run `env`.\r\n  Why: it prints secrets.\r\n', text: '🔴 **NEVER** run `env`.',
          url: `${BLOB('inspr-at/inspr-modules', 'docs/AGENTS-KERNEL.md')}?plain=1#L7-L9`, tldr: { en: 'Never print the environment.', check: true },
        }),
        kernelRule({ key: 't-0123456789abcdef0123', start_line: 11, end_line: 11, source: '- Rotate a leaked secret.\n', text: 'Rotate a leaked secret.', url: `${BLOB('inspr-at/inspr-modules', 'docs/AGENTS-KERNEL.md')}?plain=1#L11` }),
        kernelRule({ key: 't-1123456789abcdef0123', set: 'git', anchor: 'git', start_line: 15, end_line: 15, source: '- Small commits.\n', text: 'Small commits.', url: `${BLOB('inspr-at/inspr-modules', 'docs/AGENTS-KERNEL.md')}?plain=1#L15` }),
      ],
      sidecar: { path: 'docs/AGENTS-KERNEL.tldr.yaml', url: BLOB('inspr-at/inspr-modules', 'docs/AGENTS-KERNEL.tldr.yaml'), unmatched: ['rules.gone-rule'] },
    },
    {
      path: 'docs/AGENTS-DOMAIN-DEV.md', blob_sha: 'd'.repeat(40), sha256: 'e'.repeat(64), bytes: 900, kind: 'domain', layer: 'company', url: BLOB('inspr-at/inspr-modules', 'docs/AGENTS-DOMAIN-DEV.md'),
      sets: [{ set: 'tests', title: 'Tests', anchor: 'tests' }],
      rules: [{ key: 't-2', identity: 'inspr-at/inspr-modules/docs/AGENTS-DOMAIN-DEV.md#tests', set: 'tests', heading_path: 'Dev / Tests', anchor: 'tests', start_line: 5, end_line: 5, source: '- Tests are part of done.\n', sha256: 'f'.repeat(64), text: 'Tests are part of done.', strength: 'normal', url: `${BLOB('inspr-at/inspr-modules', 'docs/AGENTS-DOMAIN-DEV.md')}?plain=1#L5` }],
    },
  ],
}
const PRIVATE_FAILED = {
  id: 'd0000000-0000-4000-8000-000000000002', repository: 'inspr-at/inspr-doctrine-private', visibility: 'private', ref: 'v1', commit: PRIVATE_COMMIT,
  pinned_at: '2026-09-29T08:00:00Z', paths: ['docs/AGENTS-*.md'], credential_ref: 'doctrine-private-read', url: `https://github.com/inspr-at/inspr-doctrine-private/tree/${PRIVATE_COMMIT}`,
  state: 'failed', error: 'credential doctrine-private-read is not provisioned on this server', files: [], skipped: [],
}

interface DoctrineMock { calls: { method: string; path: string; body?: unknown }[] }

async function setup(page: Page, options: { manage: boolean; layer: { sources: unknown[]; proposals_enabled?: boolean }; after?: { sources: unknown[] } }): Promise<DoctrineMock> {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  await mockRules(page)
  const calls: DoctrineMock['calls'] = []
  let layer = options.layer
  // Registered last, so these win over the shared rules mock.
  await page.route('**/api/me/permissions**', route => {
    const url = new URL(route.request().url())
    const answer = mockEffectivePermissions('admin', url.searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions.filter(key => key !== 'settings.manage'), 'rules.read', 'rules.write', 'rules.publish', ...(options.manage ? ['settings.manage'] : [])]
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/rules/doctrine**', route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    calls.push({ method, path, body: method === 'GET' || method === 'DELETE' ? undefined : request.postDataJSON() })
    if (method !== 'GET' && options.after) layer = options.after
    return route.fulfill({ json: layer })
  })
  return { calls }
}

const section = (page: Page) => page.getByRole('region', { name: 'Doctrine' })

test('the doctrine layer shows each rule as git holds it, read-only, linked to its lines', async ({ page }) => {
  const errors = watchErrors(page)
  const mock = await setup(page, { manage: false, layer: { sources: [READY, PRIVATE_FAILED] } })
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await expect(doctrine.getByRole('heading', { name: 'Doctrine' })).toBeVisible()
  await expect(doctrine.getByText(/^v260922101217\.0\.0 · 21b8140 · pinned since /)).toBeVisible()
  await expect(doctrine.getByRole('link', { name: 'inspr-modules' })).toHaveAttribute('href', `https://github.com/inspr-at/inspr-modules/tree/${COMMIT}`)
  await expect(doctrine.getByText('1 not indexed')).toBeVisible()
  // The private source failed; a reader sees why and no action.
  await expect(doctrine.getByText('Could not read this commit: credential doctrine-private-read is not provisioned on this server')).toBeVisible()
  await expect(doctrine.getByRole('button', { name: 'Read now' })).toHaveCount(0)

  const kernel = doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ })
  await expect(kernel).toContainText('3 rules · 1 locked')
  await expect(kernel).toContainText('The rules no agent may break.')
  await kernel.click()
  await expect(doctrine.getByRole('heading', { name: 'Secrets' })).toBeVisible()
  await expect(doctrine.getByText('Secrets never reach a transcript.')).toBeVisible()
  await expect(doctrine.getByText('Never print the environment.')).toBeVisible()
  await expect(doctrine.getByText('1 TL;DR matches no rule at this commit.')).toBeVisible()
  // The exact bytes, marker line and CRLF included (pre keeps them as lines).
  const source = doctrine.locator('pre').first()
  expect(await source.evaluate(node => node.textContent)).toBe('<!-- aeon-rule: no-env-dump -->\r\n- 🔴 **NEVER** run `env`.\r\n  Why: it prints secrets.')
  const lines = doctrine.getByRole('link', { name: 'Open lines 7–9 of AGENTS-KERNEL.md in git' })
  await expect(lines).toHaveText('L7–9')
  await expect(lines).toHaveAttribute('href', `https://github.com/inspr-at/inspr-modules/blob/${COMMIT}/docs/AGENTS-KERNEL.md?plain=1#L7-L9`)
  await expect(lines).toHaveAttribute('target', '_blank')
  await expect(doctrine.getByRole('link', { name: 'Open AGENTS-KERNEL.md at the pinned commit' })).toHaveAttribute('href', `https://github.com/inspr-at/inspr-modules/blob/${COMMIT}/docs/AGENTS-KERNEL.md`)

  // Read-only: nothing in the layer can be edited, switched or pinned by a reader.
  await expect(doctrine.locator('input, textarea, select, [contenteditable="true"]')).toHaveCount(0)
  await expect(doctrine.getByRole('button', { name: /Pin|Link/ })).toHaveCount(0)
  expect(mock.calls.every(call => call.method === 'GET')).toBe(true)
  const axe = await new AxeBuilder({ page }).include('[aria-labelledby="doctrine-title"]').analyze()
  expect(axe.violations.map(v => v.id)).toEqual([])
  expect(errors).toEqual([])
})

test('a workspace manager links a repository at a pinned release', async ({ page }) => {
  const mock = await setup(page, { manage: true, layer: { sources: [] }, after: { sources: [READY] } })
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await expect(doctrine.getByText('No doctrine repository linked.')).toBeVisible()
  await doctrine.getByRole('button', { name: 'Link repository' }).click()
  const dialog = page.getByRole('dialog', { name: 'Link a doctrine repository' })
  const submit = dialog.getByRole('button', { name: 'Link repository' })
  await expect(submit).toBeDisabled()
  await dialog.getByRole('textbox', { name: 'Repository' }).fill('inspr-at/inspr-modules')
  await dialog.getByRole('textbox', { name: 'Release' }).fill('v260922101217.0.0')
  // Private needs the name of a server-side credential before it can be saved.
  await dialog.getByRole('radio', { name: 'Private' }).click()
  await expect(submit).toBeDisabled()
  await expect(dialog.getByText('Name of a read-only token on the server (never stored here); members who read rules will see this doctrine.')).toBeVisible()
  await dialog.getByRole('radio', { name: 'Public' }).click()
  await expect(submit).toBeEnabled()
  await submit.click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText('Repository linked')).toBeVisible()
  const post = mock.calls.find(call => call.method === 'POST')
  expect(post).toEqual({ method: 'POST', path: '/api/rules/doctrine/sources', body: { repository: 'inspr-at/inspr-modules', visibility: 'public', ref: 'v260922101217.0.0', paths: ['docs/AGENTS-*.md'] } })
  await expect(doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ })).toBeVisible()

  // Changing the pin to an exact commit sends the commit, not a ref.
  await doctrine.getByRole('button', { name: 'Pin inspr-modules' }).click()
  const pinDialog = page.getByRole('dialog', { name: 'Pin inspr-modules' })
  await expect(pinDialog.getByRole('textbox', { name: 'Repository' })).toHaveCount(0)
  await pinDialog.getByRole('textbox', { name: 'Release' }).fill(PRIVATE_COMMIT)
  await pinDialog.getByRole('button', { name: 'Save pin' }).click()
  await expect(pinDialog).toHaveCount(0)
  const put = mock.calls.find(call => call.method === 'PUT')
  expect(put?.path).toBe(`/api/rules/doctrine/sources/${READY.id}`)
  expect(put?.body).toEqual({ repository: 'inspr-at/inspr-modules', visibility: 'public', commit: PRIVATE_COMMIT, paths: ['docs/AGENTS-*.md'] })
})

test('a manager retries a source that could not be read', async ({ page }) => {
  const mock = await setup(page, { manage: true, layer: { sources: [PRIVATE_FAILED] }, after: { sources: [{ ...PRIVATE_FAILED, state: 'ready', error: '', files: READY.files }] } })
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await doctrine.getByRole('button', { name: 'Read now' }).click()
  await expect(page.getByText('Read again')).toBeVisible()
  expect(mock.calls.some(call => call.method === 'POST' && call.path === `/api/rules/doctrine/sources/${PRIVATE_FAILED.id}/index`)).toBe(true)
  await expect(doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ })).toBeVisible()
})

test('the doctrine layer fits a phone without sideways scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { manage: true, layer: { sources: [READY, PRIVATE_FAILED] } })
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ }).click()
  await expect(doctrine.locator('pre').first()).toBeVisible()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
  if (process.env.DOCTRINE_SHOTS) {
    for (const theme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: theme })
      await doctrine.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/doctrine__390__${theme}.png` })
    }
  }
})

test('screenshots of the doctrine layer at desk width', async ({ page }) => {
  test.skip(!process.env.DOCTRINE_SHOTS, 'set DOCTRINE_SHOTS=<dir> to capture')
  await page.setViewportSize({ width: 1600, height: 1000 })
  await setup(page, { manage: true, layer: { sources: [READY, PRIVATE_FAILED] } })
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ }).click()
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme })
    await doctrine.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/doctrine__1600__${theme}.png` })
  }
  await page.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/page__1600__dark.png`, fullPage: true })
  await doctrine.getByRole('button', { name: 'Pin inspr-modules' }).click()
  await page.getByRole('dialog', { name: 'Pin inspr-modules' }).screenshot({ path: `${process.env.DOCTRINE_SHOTS}/doctrine-pin__1600__dark.png` })
})

test('propose a rule, recover a refused public edit, and approve a checked PR', async ({ page }) => {
  await setup(page, { manage: true, layer: { sources: [READY], proposals_enabled: true } })
  let proposal: Record<string, unknown> | undefined
  const edits: Record<string, unknown>[] = []
  let refused = false
  await page.route('**/api/rules/doctrine/proposals**', route => {
    const r = route.request()
    const path = new URL(r.url()).pathname
    if (r.method() === 'GET') return route.fulfill({ json: { proposals: proposal ? [proposal] : [] } })
    if (path.endsWith('/refresh')) {
      proposal = { ...proposal, state: 'in_review', gate_ready: true }
      return route.fulfill({ json: proposal })
    }
    if (path.endsWith('/approve')) {
      expect(r.postDataJSON()).toEqual({ head_sha: '2'.repeat(40) })
      proposal = { ...proposal, state: 'merged', gate_ready: false, release_requested: true, merge_commit: '3'.repeat(40) }
      return route.fulfill({ json: proposal })
    }
    const input = r.postDataJSON() as Record<string, unknown>
    edits.push(input)
    if (!refused) {
      refused = true
      return route.fulfill({ status: 422, json: { code: 'public_identity', error: 'This public proposal contains identity-bearing text. Generalise it or propose the private rule. Nothing was published.' } })
    }
    proposal = { id: input.request_id, source_id: READY.id, repository: READY.repository, path: input.path, rule_key: input.rule_key, state: 'proposed', head_sha: '2'.repeat(40), pr_number: 12, pr_url: 'https://github.com/inspr-at/inspr-modules/pull/12', proposed_by: 'person', created_at: '2026-09-29T10:00:00Z', gate_ready: false, pinned_machines: 0 }
    return route.fulfill({ json: proposal })
  })
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ }).click()
  await doctrine.getByRole('button', { name: 'Propose change' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Propose change' })
  await expect(dialog.getByRole('textbox', { name: 'Rule', exact: true })).toHaveValue((READY.files[0]!.rules[0]!.source as string).replaceAll("\r\n", "\n"))
  await dialog.getByRole('textbox', { name: 'TL;DR · English' }).fill('Keep credentials out of transcripts.')
  await dialog.getByRole('textbox', { name: 'Why this change' }).fill('Contact operator@example.test')
  await dialog.getByRole('button', { name: 'Create pull request' }).click()
  await expect(dialog.getByRole('alert')).toContainText('Nothing was published')
  await dialog.getByRole('textbox', { name: 'Why this change' }).fill('Clarify the reason.')
  if (process.env.DOCTRINE_SHOTS) {
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      for (const theme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme: theme })
        await dialog.screenshot({ path: `${process.env.DOCTRINE_SHOTS}/proposal__${width}__${theme}.png` })
      }
    }
  }
  await dialog.getByRole('button', { name: 'Create pull request' }).click()
  await expect(dialog).toHaveCount(0)
  expect(edits[0]?.request_id).toEqual(edits[1]?.request_id)
  expect(edits[1]?.rule_sha256).toBe('a'.repeat(64))
  await expect(doctrine.getByRole('link', { name: 'inspr-modules · PR #12' })).toBeVisible()
  await expect(doctrine.getByRole('button', { name: 'Approve & merge' })).toHaveCount(0)
  await doctrine.getByRole('button', { name: 'Refresh' }).click()
  await doctrine.getByRole('button', { name: 'Approve & merge' }).click()
  await expect(doctrine.getByText('Merged', { exact: true })).toBeVisible()
  await expect(doctrine.getByText('Release requested; waiting for the repository.')).toBeVisible()
  await expect(doctrine.getByText('Released', { exact: true })).toHaveCount(0)
  if (process.env.DOCTRINE_SHOTS) {
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      for (const theme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme: theme })
        await doctrine.locator('.proposals').scrollIntoViewIfNeeded()
        await doctrine.locator('.proposals').screenshot({ path: `${process.env.DOCTRINE_SHOTS}/proposal-state__${width}__${theme}.png` })
      }
    }
  }
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
  const axe = await new AxeBuilder({ page }).include('[aria-labelledby="doctrine-title"]').analyze()
  expect(axe.violations.map(v => v.id)).toEqual([])
})


test('an agent can propose but never sees the human merge action', async ({ page }) => {
  await setup(page, { manage: false, layer: { sources: [READY], proposals_enabled: true } })
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: 'agent-test', name: 'Builder', kind: 'agent', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.route('**/api/rules/doctrine/proposals', route => route.fulfill({ json: { proposals: [{ id: 'proposal', repository: READY.repository, pr_number: 12, pr_url: 'https://github.com/inspr-at/inspr-modules/pull/12', head_sha: '2'.repeat(40), state: 'in_review', gate_ready: true, pinned_machines: 0 }] } }))
  await page.goto('/settings/agent-rules')
  const doctrine = section(page)
  await doctrine.getByRole('button', { name: /^AGENTS-KERNEL\.md/ }).click()
  await expect(doctrine.getByRole('button', { name: 'Propose change' }).first()).toBeVisible()
  await expect(doctrine.getByText('In review', { exact: true })).toBeVisible()
  await expect(doctrine.getByRole('button', { name: 'Approve & merge' })).toHaveCount(0)
})
