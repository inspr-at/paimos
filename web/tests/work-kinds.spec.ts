// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { WorkKind } from '../src/lib/workKinds'

// AEON-997: Settings › Kinds of work is a simple list, an editor that opens in
// place of its row, and one folded Advanced card. English only until AEON-998.
const row = (page: Page, slug = 'design') => page.locator(`[data-kind="${slug}"]`)
const order = (page: Page) => page.locator('ol.kinds > li[data-kind]').evaluateAll(items => items.map(item => (item as HTMLElement).dataset.kind))
const editButton = (page: Page, slug: string, label: string) => row(page, slug).getByRole('button', { name: `Edit ${label}`, exact: true })
const menuButton = (page: Page, slug: string, label: string) => row(page, slug).getByRole('button', { name: `${label}: more actions`, exact: true })
const form = (page: Page, label: string) => page.getByRole('form', { name: label === 'New kind of work' ? label : `Edit ${label}` })
const advanced = (page: Page) => page.getByRole('button', { name: 'Advanced', exact: true })
const submitKey = async (page: Page) => (await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))) ? 'Meta+Enter' : 'Control+Enter'
function seed(): WorkKind[] {
  const entries = [
    ['design', 'UI design', 'Screens and interaction, designed as an HTML mock before any code.', ['A mock for Settings › Models', 'The empty state of the inbox', 'The ticket view on a phone'], ['ui', 'ux', 'mock'], 41],
    ['frontend', 'Frontend build', 'Vue code that implements an approved design.', ['Build the Models page from its mock', 'Fix a drop-down that closes too early', 'Add a keyboard shortcut'], ['web', 'vue'], 128],
    ['backend', 'Backend build', 'Go code, APIs, SQL and migrations behind the app.', ['Add GET /agent-accounts/usage', 'A migration for model rules', 'Fix a slow query'], ['api', 'db'], 203],
    ['infra', 'Infrastructure', 'CI, Nix, deploys and the machines agents run on.', ['Pin a new release in nixcfg', 'Speed up the CI cache'], ['ci', 'nix', 'deploy'], 57],
    ['docs', 'Docs and copy', 'Words people read: READMEs, guides, release notes and interface text.', ['Release notes for 126', 'Labels for Settings'], ['docs', 'copy'], 34],
    ['security', 'Security', 'Who may do what: sign-in, keys, permissions and audits.', ['Rotate agent keys', 'Tighten a permission check'], ['security'], 19],
    ['other', 'Everything else', 'Any ticket no other kind describes, and every kind without its own column.', ['Tidy up a script', 'A one-off data fix'], [], 96],
  ] as const
  return entries.map((entry, position) => ({ id: `00000000-0000-4000-8000-${String(position + 1).padStart(12, '0')}`, slug: entry[0], label: entry[1], hint: entry[2], examples: [...entry[3]], labels: [...entry[4]], ticket_count: entry[5], position, ...(entry[0] === 'other' || entry[0] === 'security' ? { system: entry[0] } : {}) }))
}
async function open(page: Page, options: { member?: boolean; agent?: boolean; fail?: boolean; width?: number; theme?: string; pagination?: boolean; hash?: string } = {}) {
  const data = { kinds: seed(), limits: { small_hours: 2, fix_rounds: 3, revision: 7, set_by: null, set_at: null }, writes: [] as { path: string; method: string; body: unknown }[], fail: options.fail ?? false, manage: !options.member, reads: 0 }
  await page.setViewportSize({ width: options.width ?? 1440, height: 1000 })
  await mockWork(page, fixtures())
  const settings = settingsData(); settings.profile.locale = 'en-GB'; await mockSettings(page, settings)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: settings.profile.principal_id, name: 'Markus Barta', kind: options.agent ? 'agent' : 'person', roles: ['admin'] }, tenant: { id: 'tenant', slug: 'inspr', name: 'INSPR' } } }))
  await page.route('**/api/me/permissions*', route => {
    const grants = mockEffectivePermissions(options.member ? 'member' : 'admin'); grants.workspace.permissions.push('models.read'); if (data.manage) grants.workspace.permissions.push('model_prefs.manage')
    return route.fulfill({ json: grants })
  })
  await page.route('**/api/work-kinds**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method()
    if (method === 'GET') {
      data.reads++
      const middle = Math.ceil(data.kinds.length / 2), second = !!url.searchParams.get('cursor')
      return route.fulfill({ json: { items: options.pagination ? data.kinds.slice(second ? middle : 0, second ? undefined : middle) : data.kinds, next_cursor: options.pagination && !second ? data.kinds[middle - 1]!.id : null } })
    }
    const body = req.postData() ? req.postDataJSON() : undefined
    data.writes.push({ path, method, body })
    if (data.fail) return route.fulfill({ status: 500, json: { error: 'storage failed' } })
    if (path === '/api/work-kinds/order') {
      const slugs = body.slugs as string[]
      data.kinds = [...slugs.map((slug, position) => ({ ...data.kinds.find(kind => kind.slug === slug)!, position })), ...data.kinds.filter(kind => kind.archived_at)]
      return route.fulfill({ json: { items: data.kinds.filter(kind => !kind.archived_at), next_cursor: null } })
    }
    if (method === 'POST' && path === '/api/work-kinds') {
      const kind: WorkKind = { ...body, id: '00000000-0000-4000-8000-000000000999', slug: 'data-analysis', ticket_count: 0 }; data.kinds.push(kind)
      return route.fulfill({ status: 201, json: kind })
    }
    const id = path.split('/')[3], kind = data.kinds.find(kind => kind.id === id)!
    if (method === 'PATCH') Object.assign(kind, body)
    if (method === 'DELETE') kind.archived_at = '2026-10-07T13:00:00Z'
    if (path.endsWith('/restore')) kind.archived_at = null
    return route.fulfill({ json: kind })
  })
  await page.route('**/api/model-preferences/situations', route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON(); data.writes.push({ path: '/api/model-preferences/situations', method: 'PUT', body })
      // A write for an older revision conflicts and leaves the server copy alone.
      if (data.fail || body.revision !== data.limits.revision) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      Object.assign(data.limits, body, { revision: data.limits.revision + 1 })
    }
    return route.fulfill({ json: data.limits })
  })
  await page.goto(`/settings/kinds${options.hash ?? ''}`)
  await expect(row(page)).toContainText('UI design')
  await expect(advanced(page)).toBeVisible()
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, options.theme ?? 'light')
  return data
}

// Risk: the editor drifts back into a dialog that dims the page and traps the keyboard.
test('Edit turns the row into a two-field form in the list: no dialog, no overlay, the rows above stay put', async ({ page }) => {
  await open(page)
  const above = { row: row(page, 'design'), edit: editButton(page, 'design', 'UI design'), menu: menuButton(page, 'design', 'UI design'), openModels: page.getByRole('link', { name: 'Open Models' }) }
  const frontend = form(page, 'Frontend build')
  await expectStableControls({ controls: above, interactions: [{ name: 'open the form in place', run: async () => { await editButton(page, 'frontend', 'Frontend build').click(); await expect(frontend).toBeVisible() } }], scrollAreas: { page: page.locator('.settings-page') } })
  // The form takes the row's place in the list, not a layer on top of it.
  expect(await page.locator('ol.kinds > li').evaluateAll(items => items.map(item => item.querySelector('form') ? 'form' : (item as HTMLElement).dataset.kind))).toEqual(['design', 'form', 'backend', 'infra', 'docs', 'security', 'other'])
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.locator('[aria-modal="true"], dialog[open]')).toHaveCount(0)
  await expect(page.locator('#app')).not.toHaveAttribute('inert', /.*/)
  // Two fields are visible; examples, area and labels wait behind More options.
  await expect(frontend.locator('input:visible:not([readonly]), textarea:visible')).toHaveCount(2)
  await expect(frontend.locator('[name="label"]')).toBeFocused()
  const more = frontend.getByRole('button', { name: /^More options/ })
  await expect(more).toHaveAttribute('aria-expanded', 'false')
  await expect(frontend.locator('[name="examples"]')).toBeHidden()
  // Tab moves on naturally; nothing traps focus inside the form.
  await frontend.locator('[name="hint"]').focus(); await page.keyboard.press('Tab'); await expect(more).toBeFocused()
  await page.keyboard.press('Tab'); await expect(frontend.getByRole('button', { name: /^Cancel/ })).toBeFocused()
  await page.keyboard.press('Tab'); await expect(frontend.getByRole('button', { name: /^Save/ })).toBeFocused()
  await page.keyboard.press('Tab'); await expect(frontend.locator(':focus')).toHaveCount(0)
})

// Risk: opening a second form silently drops words the person has not saved.
test('only one form at a time; another Edit waits until unsaved words are saved or cancelled', async ({ page }) => {
  await open(page)
  await editButton(page, 'frontend', 'Frontend build').click()
  await form(page, 'Frontend build').locator('[name="hint"]').fill('A sentence that is not saved yet.')
  await editButton(page, 'backend', 'Backend build').click()
  await expect(form(page, 'Frontend build')).toContainText('Save or cancel this kind first.')
  await expect(form(page, 'Backend build')).toHaveCount(0)
  await form(page, 'Frontend build').getByRole('button', { name: /^Cancel/ }).click()
  await editButton(page, 'backend', 'Backend build').click()
  await expect(form(page, 'Backend build')).toBeVisible()
  await expect(page.locator('ol.kinds form')).toHaveCount(1)
})

// Risk: a form that only the mouse can leave; Esc discarding a draft by accident.
test('Enter edits a focused row; Esc leaves the field, the next Esc cancels and returns focus to the row', async ({ page }) => {
  const data = await open(page)
  await row(page, 'frontend').focus(); await page.keyboard.press('Enter')
  const frontend = form(page, 'Frontend build')
  await expect(frontend).toBeVisible(); await expect(frontend.locator('[name="label"]')).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(frontend).toBeVisible(); await expect(frontend.locator('[name="label"]')).not.toBeFocused()
  await page.keyboard.press('Escape')
  await expect(frontend).toHaveCount(0); await expect(row(page, 'frontend')).toBeFocused()
  // Unsaved words survive the first Esc and are dropped, deliberately, by the second.
  await row(page, 'frontend').press('Enter')
  await frontend.locator('[name="hint"]').fill('Words that are not saved.')
  await page.keyboard.press('Escape')
  await expect(frontend).toBeVisible(); await expect(frontend.locator('[name="hint"]')).not.toBeFocused()
  await expect(frontend.locator('[name="hint"]')).toHaveValue('Words that are not saved.')
  await page.keyboard.press('Escape')
  await expect(frontend).toHaveCount(0); await expect(row(page, 'frontend')).toBeFocused()
  await expect(row(page, 'frontend')).toContainText('Vue code that implements an approved design.')
  // A button is not a field: Esc on More options cancels at once.
  await row(page, 'frontend').press('Enter')
  await frontend.getByRole('button', { name: /^More options/ }).focus(); await page.keyboard.press('Escape')
  await expect(frontend).toHaveCount(0); await expect(row(page, 'frontend')).toBeFocused()
  // A built-in kind keeps its name; focus starts at the sentence.
  await row(page, 'security').focus(); await page.keyboard.press('Enter')
  const security = form(page, 'Security')
  await expect(security.locator('[name="label"]')).toHaveAttribute('readonly', '')
  await expect(security.locator('[name="hint"]')).toBeFocused()
  await security.getByRole('button', { name: /^Cancel/ }).click()
  await expect(row(page, 'security')).toBeFocused()
  expect(data.writes).toHaveLength(0)
})

// Risk: a save that fires twice, loses the stored area, or cannot be undone.
test('⌘/Ctrl+Enter saves once from any field; the area stays; Undo restores the words', async ({ page }) => {
  const data = await open(page)
  await editButton(page, 'frontend', 'Frontend build').click()
  const frontend = form(page, 'Frontend build')
  await frontend.locator('[name="hint"]').fill('Vue code that builds an approved design.')
  await frontend.getByRole('button', { name: /^More options/ }).click()
  await frontend.locator('[name="examples"]').fill('Build the board\nFix a menu')
  await expect(frontend.locator('[name="area"]')).toHaveValue('frontend'); await expect(frontend.locator('[name="area"]')).toHaveAttribute('readonly', '')
  await frontend.locator('[name="examples"]').press(await submitKey(page))
  await expect(frontend).toHaveCount(0)
  await expect(row(page, 'frontend')).toContainText('Vue code that builds an approved design.')
  await expect(row(page, 'frontend')).toBeFocused()
  await expect(page.locator('.toast').first()).toContainText('Frontend build saved. Its column on Models shows the new words.')
  expect(data.writes).toHaveLength(1)
  expect(data.writes[0]).toMatchObject({ method: 'PATCH', body: { label: 'Frontend build', hint: 'Vue code that builds an approved design.', examples: ['Build the board', 'Fix a menu'], labels: ['web', 'vue'] } })
  expect(data.writes[0]!.body).not.toHaveProperty('slug')
  await page.locator('.toast').getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(row(page, 'frontend')).toContainText('Vue code that implements an approved design.')
  expect(data.writes).toHaveLength(2)
})

// Risk: a failed or invalid save reports success, loses the draft, or hides the field at fault.
test('invalid and failed saves keep the draft and the controls still', async ({ page }) => {
  const data = await open(page, { fail: true })
  await editButton(page, 'frontend', 'Frontend build').click()
  const frontend = form(page, 'Frontend build'), more = frontend.getByRole('button', { name: /^More options/ })
  const save = frontend.getByRole('button', { name: /^Save/ }), cancel = frontend.getByRole('button', { name: /^Cancel/ })
  const name = frontend.locator('[name="label"]'), hint = frontend.locator('[name="hint"]')
  // Save and Cancel stay put while More options grows, shrinks and the examples field is resized.
  await expectStableControls({ controls: { name, hint, more, save, cancel, above: row(page, 'design') }, interactions: [
    { name: 'typing a long sentence', run: async () => { await hint.fill('A long and careful sentence that a newcomer and an agent can both follow without asking anyone for help.') } },
    { name: 'opening More options', run: async () => { await more.click(); await expect(frontend.locator('[name="examples"]')).toBeVisible() } },
    { name: 'resizing the examples field', run: async () => {
      const examples = frontend.locator('[name="examples"]')
      const before = (await examples.boundingBox())!.height
      await examples.evaluate((el: HTMLTextAreaElement) => { el.style.height = `${el.getBoundingClientRect().height + 48}px` })
      await expect.poll(async () => (await examples.boundingBox())?.height ?? 0).toBeGreaterThan(before + 40)
    } },
    { name: 'closing More options', run: async () => { await more.click(); await expect(frontend.locator('[name="examples"]')).toBeHidden() } },
  ] })
  await more.click()
  await expect(frontend.locator('[name="examples"]')).toBeVisible()
  // Feedback grows below the actions, so Save and Cancel stay where they are.
  await expectStableControls({ controls: { save, cancel, name, hint, more }, interactions: [
    { name: 'validation appears below the actions', run: async () => { await frontend.locator('[name="examples"]').fill('one\ntwo\nthree\nfour'); await save.click(); await expect(frontend).toContainText('Use a name') } },
    { name: 'a server failure keeps the draft', run: async () => { await frontend.locator('[name="examples"]').fill('one'); await save.click(); await expect(frontend).toContainText('could not be saved'); await expect(frontend.locator('[name="examples"]')).toHaveValue('one') } },
  ], scrollAreas: { form: frontend } })
  expect(data.writes).toHaveLength(1)
  await expect(page.locator('.toast')).toHaveCount(0)
  await expect(hint).toHaveValue(/A long and careful sentence/)
  // A problem hidden behind More options opens it.
  await more.click(); await expect(frontend.locator('[name="examples"]')).toBeHidden()
  await frontend.locator('[name="examples"]').evaluate((el: HTMLTextAreaElement) => { el.value = 'a\nb\nc\nd'; el.dispatchEvent(new Event('input')) })
  await save.click(); await expect(frontend.locator('[name="examples"]')).toBeVisible()
})

// Risk: ordering that only works with a pointer, or moves Everything else.
test('Alt+↑/↓ on a focused row reorders; the ends and Everything else do not move', async ({ page }) => {
  const data = await open(page)
  await row(page, 'backend').focus(); await row(page, 'backend').press('Alt+ArrowUp')
  await expect.poll(() => order(page)).toEqual(['design', 'backend', 'frontend', 'infra', 'docs', 'security', 'other'])
  expect(data.writes.at(-1)!.body).toEqual({ slugs: ['design', 'backend', 'frontend', 'infra', 'docs', 'security', 'other'] })
  await expect(row(page, 'backend')).toBeFocused()
  await row(page, 'backend').press('Alt+ArrowDown')
  await expect.poll(() => order(page)).toEqual(['design', 'frontend', 'backend', 'infra', 'docs', 'security', 'other'])
  await expect(row(page, 'backend')).toBeFocused()
  expect(data.writes).toHaveLength(2)
  // None of these may write: the first row cannot go up, nothing passes Everything else, plain arrows are the browser's.
  await row(page, 'design').focus(); await row(page, 'design').press('Alt+ArrowUp')
  await row(page, 'security').focus(); await row(page, 'security').press('Alt+ArrowDown')
  await row(page, 'other').focus(); await row(page, 'other').press('Alt+ArrowUp')
  await row(page, 'backend').focus(); await row(page, 'backend').press('ArrowUp')
  // Writes are answered in order, so the next real move proves the keys above sent nothing before it.
  await row(page, 'docs').focus(); await row(page, 'docs').press('Alt+ArrowUp')
  await expect.poll(() => order(page)).toEqual(['design', 'frontend', 'backend', 'docs', 'infra', 'security', 'other'])
  expect(data.writes).toHaveLength(3)
})

// Risk: archive or order reachable where they must not be; no reason given for a built-in kind.
test('the ⋯ menu orders and archives; built-in kinds say why they cannot be archived', async ({ page }) => {
  const data = await open(page)
  await menuButton(page, 'design', 'UI design').click()
  const menu = page.getByRole('menu', { name: 'Actions for UI design' })
  await expect(menu.getByRole('menuitem', { name: /^Move up/ })).toBeDisabled()
  await expect(menu.getByRole('menuitem', { name: /^Archive…/ })).toContainText('41 tickets follow Everything else')
  await menu.getByRole('menuitem', { name: /^Move down/ }).click()
  await expect.poll(() => order(page)).toEqual(['frontend', 'design', 'backend', 'infra', 'docs', 'security', 'other'])
  expect(data.writes.at(-1)!.body).toEqual({ slugs: ['frontend', 'design', 'backend', 'infra', 'docs', 'security', 'other'] })
  await expect(menuButton(page, 'design', 'UI design')).toBeFocused()
  await menuButton(page, 'security', 'Security').click()
  const security = page.getByRole('menu', { name: 'Actions for Security' })
  await expect(security.getByRole('menuitem', { name: /^Archive…/ })).toBeDisabled()
  await expect(security.getByRole('menuitem', { name: /^Archive…/ })).toContainText('Built in: PAIMOS needs it')
  await page.keyboard.press('Escape')
  await menuButton(page, 'other', 'Everything else').click()
  const other = page.getByRole('menu', { name: 'Actions for Everything else' })
  await expect(other.getByRole('menuitem', { name: /^Move/ })).toHaveCount(0)
  await expect(other.getByRole('menuitem', { name: /^Archive…/ })).toBeDisabled()
  await page.keyboard.press('Escape')
  // Archive asks first, names what happens, and can be restored.
  await menuButton(page, 'design', 'UI design').click()
  await page.getByRole('menu', { name: 'Actions for UI design' }).getByRole('menuitem', { name: /^Archive…/ }).click()
  const confirm = page.getByRole('dialog', { name: 'Archive UI design?' })
  await expect(confirm).toContainText('41 tickets keep their area value')
  const writes = data.writes.length
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(menuButton(page, 'design', 'UI design')).toBeFocused(); expect(data.writes).toHaveLength(writes)
  await menuButton(page, 'design', 'UI design').click()
  await page.getByRole('menu', { name: 'Actions for UI design' }).getByRole('menuitem', { name: /^Archive…/ }).click()
  await page.getByRole('dialog', { name: 'Archive UI design?' }).getByRole('button', { name: /^Archive/ }).click()
  await expect(row(page)).toHaveCount(0)
  expect(data.writes.at(-1)).toMatchObject({ method: 'DELETE' })
  await page.locator('.archived-kind').getByRole('button', { name: 'Restore' }).click()
  await expect(row(page)).toContainText('Screens and interaction')
  expect(data.writes.at(-1)!.path).toMatch(/restore$/)
})

// Risk: a new kind that lands in the wrong place, posts twice or loses its fields.
test('New kind opens the same form at the end of the list and creates once', async ({ page }) => {
  const data = await open(page, { pagination: true })
  expect(data.reads).toBe(2)
  await page.getByRole('button', { name: 'New kind', exact: true }).click()
  const created = form(page, 'New kind of work')
  await expect(created).toBeVisible()
  expect(await page.locator('ol.kinds > li').evaluateAll(items => items.map(item => item.querySelector('form') ? 'form' : (item as HTMLElement).dataset.kind))).toEqual(['design', 'frontend', 'backend', 'infra', 'docs', 'security', 'other', 'form'])
  await expect(created.locator('[name="label"]')).toBeFocused()
  await created.locator('[name="label"]').fill('Data analysis'); await created.locator('[name="hint"]').fill('Analyse the data behind a decision.')
  await created.getByRole('button', { name: /^More options/ }).click()
  await expect(created.locator('[name="area"]')).toHaveValue('Assigned when created')
  await created.locator('[name="examples"]').fill('A churn query'); await created.locator('[name="labels"]').fill('data, sql')
  await created.locator('[name="labels"]').press(await submitKey(page))
  await expect(row(page, 'data-analysis')).toContainText('Data analysis')
  await expect(page.locator('.toast').filter({ hasText: 'Data analysis is a kind of work now, and a column on Models.' })).toBeVisible()
  expect(data.writes.filter(write => write.method === 'POST' && write.path === '/api/work-kinds')).toHaveLength(1)
  expect(data.writes[0]!.body).toMatchObject({ label: 'Data analysis', hint: 'Analyse the data behind a decision.', examples: ['A churn query'], labels: ['data', 'sql'], position: 6 })
  await expect(row(page, 'data-analysis')).toBeFocused()
})

// Risk: the rare limits are lost, saved without a way back, or the fold forgets the person.
test('Advanced is folded, remembers its state, shows how kinds are recognised and saves limits with Undo', async ({ page }) => {
  const data = await open(page)
  await expect(advanced(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('table')).toBeHidden()
  await advanced(page).click()
  await expect(advanced(page)).toHaveAttribute('aria-expanded', 'true')
  const table = page.getByRole('table')
  await expect(table.getByRole('row')).toHaveCount(8)
  await expect(table.getByRole('row', { name: /^Frontend build/ })).toContainText('frontend'); await expect(table.getByRole('row', { name: /^Frontend build/ })).toContainText('web'); await expect(table.getByRole('row', { name: /^Frontend build/ })).toContainText('vue')
  await expect(table.getByRole('row', { name: /^Everything else/ })).toContainText('(none)')
  const small = page.getByRole('spinbutton', { name: 'Small work, hours' }), rounds = page.getByRole('spinbutton', { name: 'Fix rounds before Stuck' })
  await expect(page.locator('#k-adv')).toContainText('Small work is'); await expect(page.locator('#k-adv')).toContainText('Another model family takes over.')
  await expectStableControls({ controls: { small, rounds, kindsCard: page.locator('#k-kinds'), toggle: advanced(page) }, interactions: [
    { name: 'save small work', run: async () => { await small.fill('3'); await small.press('Tab'); await expect.poll(() => data.limits.revision).toBe(8) } },
    { name: 'save fix rounds', run: async () => { await rounds.fill('6'); await rounds.press('Tab'); await expect.poll(() => data.limits.revision).toBe(9) } },
  ] })
  expect(data.writes.map(write => write.body)).toEqual([{ small_hours: 3, fix_rounds: 3, revision: 7 }, { small_hours: 3, fix_rounds: 6, revision: 8 }])
  await expect(page.locator('.toast').filter({ hasText: 'Stuck after 6 fix rounds.' })).toBeVisible()
  await page.locator('.toast').filter({ hasText: 'Stuck after 6 fix rounds.' }).getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(rounds).toHaveValue('3'); expect(data.limits).toMatchObject({ small_hours: 3, fix_rounds: 3, revision: 10 })
  // The fold is a browser preference of the person: it stays open after a reload.
  await page.reload(); await expect(advanced(page)).toHaveAttribute('aria-expanded', 'true')
  await advanced(page).click(); await page.reload(); await expect(advanced(page)).toHaveAttribute('aria-expanded', 'false')
})

// Risk: the Models situation picker links to a card that no longer exists.
test('the situations link on Models still lands on the limits, opening the fold', async ({ page }) => {
  await open(page, { hash: '#k-sits' })
  await expect(advanced(page)).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('heading', { name: 'Situation limits' })).toBeInViewport()
  await page.reload(); await expect(advanced(page)).toHaveAttribute('aria-expanded', 'true')
})

// Risk: a failed limit write shows the new number as if it had been saved.
test('a rejected limit returns to the stored value and says why', async ({ page }) => {
  const data = await open(page, { fail: true })
  await advanced(page).click()
  const small = page.getByRole('spinbutton', { name: 'Small work, hours' })
  await small.fill('4'); await small.press('Tab')
  await expect(small).toHaveValue('2'); await expect(page.locator('#k-adv')).toContainText('changed elsewhere')
  await expect(page.locator('.toast')).toHaveCount(0)
  expect(data.writes.at(-1)!.body).toEqual({ small_hours: 4, fix_rounds: 3, revision: 7 })
})

// Risk: a conflict keeps the stale revision and hides the only way to fetch a fresh one.
test('a limit conflict offers Reload and the next save uses the refreshed revision', async ({ page }) => {
  const data = await open(page)
  await advanced(page).click()
  const small = page.getByRole('spinbutton', { name: 'Small work, hours' })
  // Someone else saved first. The page still holds revision 7.
  data.limits.small_hours = 5
  data.limits.revision = 8
  await small.fill('4'); await small.press('Tab')
  await expect(small).toHaveValue('2')
  await expect(page.locator('#k-adv')).toContainText('changed elsewhere')
  await expect(page.locator('.toast')).toHaveCount(0)
  expect(data.writes.at(-1)!.body).toEqual({ small_hours: 4, fix_rounds: 3, revision: 7 })
  expect(data.limits).toMatchObject({ small_hours: 5, revision: 8 })
  const reload = page.locator('#k-adv').getByRole('button', { name: 'Reload', exact: true })
  await expect(reload).toBeVisible()
  await reload.click()
  await expect(small).toHaveValue('5')
  await expect(page.locator('#k-adv')).not.toContainText('changed elsewhere')
  await expect(reload).toBeHidden()
  await small.fill('4'); await small.press('Tab')
  await expect.poll(() => data.limits.revision).toBe(9)
  expect(data.writes.at(-1)!.body).toEqual({ small_hours: 4, fix_rounds: 3, revision: 8 })
  await expect(small).toHaveValue('4')
  await expect(page.locator('.toast').filter({ hasText: 'Small work: 4 h or less.' })).toBeVisible()
})

// Risk: the two cards touch again (the section lost the settings body gap).
test('the cards keep the settings gap', async ({ page }) => {
  await open(page)
  const gap = await page.evaluate(() => {
    const who = document.querySelector('.kinds-section .who')!.getBoundingClientRect(), kinds = document.querySelector('#k-kinds')!.getBoundingClientRect(), adv = document.querySelector('#k-adv')!.getBoundingClientRect()
    return { whoToKinds: kinds.top - who.bottom, kindsToAdvanced: adv.top - kinds.bottom }
  })
  expect(Math.abs(gap.kindsToAdvanced - 14)).toBeLessThanOrEqual(0.5); expect(Math.abs(gap.whoToKinds - 14)).toBeLessThanOrEqual(0.5)
  const models = await page.evaluate(() => getComputedStyle(document.querySelector('.settings-page .body')!).rowGap)
  expect(models).toBe('14px')
})

for (const identity of ['member', 'agent']) test(`${identity} reads kinds with write controls absent`, async ({ page }) => {
  const data = await open(page, { member: identity === 'member', agent: identity === 'agent' })
  await expect(page.locator('#k-kinds button')).toHaveCount(0)
  await expect(page.locator('#k-kinds a')).toHaveCount(1)
  await expect(page.locator('#k-kinds a')).toHaveAttribute('href', '/settings/models')
  await expect(page.locator('#k-kinds a')).toHaveText('Open Models')
  await expect(row(page, 'design')).not.toHaveAttribute('tabindex', '0')
  await expect(row(page, 'design')).toContainText('41 tickets')
  await advanced(page).click()
  await expect(page.locator('#k-adv input')).toHaveCount(0)
  await expect(page.locator('#k-adv')).toContainText('Small work is 2 hours or less.')
  await expect(page.locator('.kinds-section .who')).toContainText('you can read them')
  await row(page, 'design').click(); await expect(page.locator('ol.kinds form')).toHaveCount(0)
  expect(data.writes).toHaveLength(0)
})

// Risk: targets under 44 px, a count squeezed beside the name, or sideways scrolling on a phone.
test('at 400 px the count moves under the name, targets are 44 px and nothing scrolls sideways', async ({ page }) => {
  await open(page, { width: 400 })
  const name = row(page, 'design').locator('.kind-name'), count = row(page, 'design').locator('.kind-c')
  const [nameBox, countBox, editBox, menuBox] = await Promise.all([name.boundingBox(), count.boundingBox(), editButton(page, 'design', 'UI design').boundingBox(), menuButton(page, 'design', 'UI design').boundingBox()])
  expect(countBox!.y).toBeGreaterThan(nameBox!.y + nameBox!.height - 1)
  expect(editBox!.x).toBeGreaterThan(nameBox!.x + nameBox!.width - 1); expect(menuBox!.x).toBeGreaterThan(editBox!.x)
  for (const box of [editBox!, menuBox!]) { expect(box.width).toBeGreaterThanOrEqual(43.5); expect(box.height).toBeGreaterThanOrEqual(43.5) }
  expect((await page.getByRole('button', { name: 'New kind', exact: true }).boundingBox())!.height).toBeGreaterThanOrEqual(43.5)
  await editButton(page, 'design', 'UI design').click()
  const design = form(page, 'UI design')
  for (const button of await design.getByRole('button').all()) expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(43.5)
  for (const input of await design.locator('input:visible').all()) expect((await input.boundingBox())!.height).toBeGreaterThanOrEqual(43.5)
  await design.getByRole('button', { name: /^More options/ }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
  expect(await design.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  await design.getByRole('button', { name: /^Cancel/ }).click()
  await advanced(page).click()
  expect((await advanced(page).boundingBox())!.height).toBeGreaterThanOrEqual(43.5)
  for (const input of await page.locator('#k-adv input').all()) expect((await input.boundingBox())!.height).toBeGreaterThanOrEqual(43.5)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
})

for (const width of [1440, 1024, 400]) for (const theme of ['light', 'dark']) test(`kinds evidence and control stability ${width} ${theme}`, async ({ page }, testInfo) => {
  await open(page, { width, theme })
  await page.screenshot({ path: testInfo.outputPath(`kinds-${width}-${theme}.png`), fullPage: true })
  const controls = { design: row(page, 'design'), designEdit: editButton(page, 'design', 'UI design'), designMenu: menuButton(page, 'design', 'UI design'), advanced: advanced(page) }
  await expectStableControls({ controls: { design: controls.design, designEdit: controls.designEdit, designMenu: controls.designMenu }, interactions: [{ name: 'open the form below the first row', run: async () => { await editButton(page, 'frontend', 'Frontend build').click(); await expect(form(page, 'Frontend build')).toBeVisible() } }, { name: 'open More options', run: async () => { await form(page, 'Frontend build').getByRole('button', { name: /^More options/ }).click(); await expect(form(page, 'Frontend build').locator('[name="examples"]')).toBeVisible() } }], scrollAreas: { page: page.locator('.settings-page') } })
  await form(page, 'Frontend build').scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath(`kind-form-${width}-${theme}.png`), fullPage: true })
  await form(page, 'Frontend build').getByRole('button', { name: /^Cancel/ }).click()
  await menuButton(page, 'design', 'UI design').click()
  await expect(page.getByRole('menu', { name: 'Actions for UI design' })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath(`kind-menu-${width}-${theme}.png`) })
  await page.keyboard.press('Escape')
  await advanced(page).click()
  await expect(page.getByRole('table')).toBeVisible()
  await page.locator('#k-adv').scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath(`kinds-advanced-${width}-${theme}.png`), fullPage: true })
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
})
