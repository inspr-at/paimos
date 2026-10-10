// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

import { expectStableControls } from './helpers/stable'

import type { Shell } from './helpers/no-shift-shells'
import { session, setup } from './no-shift-fixtures'

async function mountShell(page: Page, kind: Shell, width: number, above = false) {
  await page.setViewportSize({ width, height: 1000 })
  await mockWork(page, fixtures(), { admin: true })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await page.evaluate(async ({ kind, phone, above }) => {
    const path = '/tests/helpers/no-shift-shells.ts'
    const { mountShell } = await import(/* @vite-ignore */ path)
    mountShell(kind, phone, above)
  }, { kind, phone: width === 390, above })
}
const setLongContent = (page: Page, long: boolean) => page.evaluate(async long => {
  const path = '/tests/helpers/no-shift-shells.ts'
  const { setLongContent } = await import(/* @vite-ignore */ path)
  setLongContent(long)
}, long)

for (const width of [1440, 390]) {
  for (const kind of ['access', 'rules'] as const) {
    test(`${kind} short task grows below desktop actions and pins phone actions at ${width}`, async ({ page }) => {
      await mountShell(page, kind, width)
      const dialog = page.getByRole('dialog', { name: 'Short task' })
      const body = dialog.locator(kind === 'access' ? '.sheet-body' : '.body')
      await expect(dialog).toBeVisible()
      if (width === 1440) expect((await dialog.boundingBox())!.height, 'short task fits its content').toBeLessThan(240)
      else expect((await dialog.boundingBox())!.height).toBe(1000)
      await expectStableControls({
        controls: { ...(width === 390 ? { dialog } : {}), title: dialog.getByRole('heading'), accept: dialog.getByRole('button', { name: 'Accept' }), close: dialog.getByRole('button', { name: /^Close/ }) },
        scrollAreas: { body },
        interactions: [
          { name: 'long body', run: async () => { await setLongContent(page, true); await expect(body).toContainText('A longer explanation') } },
          { name: 'scroll body', run: () => body.evaluate(el => { el.scrollTop = 120 }) },
          { name: 'short body again', run: async () => { await setLongContent(page, false); await expect(body).toHaveText('A short explanation.') } },
        ],
      })
      if (width === 390) {
        const accept = (await dialog.getByRole('button', { name: 'Accept' }).boundingBox())!
        expect(accept.y + accept.height).toBeGreaterThan(950)
        if (kind === 'access') await expect(dialog.locator('.sheet-foot')).toHaveCSS('padding-bottom', '14px')
      }
    })
  }

  test(`Keep for you uses a natural vendor disclosure and stable actions at ${width}`, async ({ page }) => {
    await mountShell(page, 'keep', width)
    const dialog = page.getByRole('dialog', { name: 'Keep for you' })
    const vendors = dialog.getByRole('button', { name: 'Per vendor' })
    await expect(vendors).toHaveAttribute('aria-expanded', 'false')
    expect((await dialog.locator('.vendors').boundingBox())!.height, 'closed vendors contain only the disclosure').toBe(30)
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(720)
    else expect((await dialog.boundingBox())!.height).toBe(1000)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), save: dialog.getByRole('button', { name: 'Save', exact: true }), cancel: dialog.getByRole('button', { name: 'Cancel' }), choices: dialog.locator('.modes'), vendors },
      scrollAreas: { body: dialog.locator('.ed-body') },
      interactions: [
        { name: 'open vendors', run: () => vendors.click() },
        { name: 'close vendors', run: () => vendors.click() },
        { name: 'fixed share', run: () => dialog.getByRole('radio', { name: 'A fixed share' }).check() },
        { name: 'nothing', run: () => dialog.getByRole('radio', { name: /Nothing/ }).check() },
      ],
    })
  })

  test(`schedule model changes grow downward with actions in place at ${width}`, async ({ page }) => {
    await mountShell(page, 'night', width)
    const dialog = page.getByRole('dialog', { name: 'Agents outside your hours' })
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(720)
    else expect((await dialog.boundingBox())!.height).toBe(1000)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), save: dialog.getByRole('button', { name: 'Save', exact: true }), cancel: dialog.getByRole('button', { name: 'Cancel' }), models: dialog.locator('.models') },
      scrollAreas: { body: dialog.locator('.ed-body') },
      interactions: ['No shifts', 'Day and night', 'Three shifts', 'Custom blocks', 'No shifts'].map(name => ({ name, run: () => dialog.getByRole('radio', { name: new RegExp(`^${name}`) }).check() })),
    })
  })

  test(`short recovery states keep actions above content at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = await setup(page)
    const selected = data.sessions[1]!
    Object.assign(selected, { management_mode: 'unmanaged', advertised_capabilities: ['status'] })
    let release!: () => void
    const pending = new Promise<void>(resolve => { release = resolve })
    await page.route('**/harness-sessions/*/recovery', async route => {
      await pending
      await route.fulfill({ json: { session_id: selected.id, can_archive: false, force_stop_available: false, force_stop_reason: 'Force stop unavailable.', observed_revision: 'a'.repeat(64), host: 'fixture' } })
    })
    await page.goto(`/agents/${selected.id}`)
    await page.getByRole('button', { name: 'Recover', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Recover session' })
    await expect(dialog.getByText('Loading current session…')).toBeVisible()
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(330)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), title: dialog.getByRole('heading'), cancel: dialog.getByRole('button', { name: 'Cancel' }) },
      scrollAreas: { body: dialog.locator('.recovery-body') },
      interactions: [{ name: 'no recovery action', run: async () => { release(); await expect(dialog.getByText('No recovery action is available for this registration.')).toBeVisible() } }],
    })
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(400)
  })

  test(`Done validation keeps its action position without padding at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    await page.getByRole('button', { name: /Change status of PHAROS-11/ }).click()
    await page.getByRole('menuitemradio', { name: 'Done', exact: true }).click()
    const dialog = page.locator('.gate[open]')
    await expect(dialog).toBeVisible()
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(680)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), actions: dialog.locator('.actions'), submit: dialog.getByRole('button', { name: 'Mark done' }), cancel: dialog.getByRole('button', { name: 'Not now' }) },
      scrollAreas: { body: dialog.locator('.scroll') },
      interactions: [
        { name: 'validation errors', run: async () => { await dialog.getByRole('button', { name: 'Mark done' }).click(); await expect(dialog.getByLabel('Pill · English')).toHaveAttribute('aria-invalid', 'true') } },
        { name: 'resize text field', run: () => dialog.getByLabel('Benefit · English').evaluate(el => { (el as HTMLElement).style.height = '600px' }) },
        { name: 'scroll fields', run: () => dialog.locator('.scroll').evaluate(el => { el.scrollTop = 150 }) },
      ],
    })
  })
}

for (const above of [false, true]) test(`floating search ${above ? 'above' : 'below'} its trigger keeps controls while results change`, async ({ page }) => {
  await mountShell(page, 'floating', 1440, above)
  const dialog = page.getByRole('dialog', { name: 'Fixture picker' })
  await expect(dialog).toBeVisible()
  if (!above) expect((await dialog.boundingBox())!.height).toBeLessThan(160)
  await expectStableControls({
    controls: { ...(above ? { dialog } : {}), search: dialog.getByRole('textbox') },
    scrollAreas: { dialog },
    interactions: [
      { name: 'long results', run: () => setLongContent(page, true) },
      { name: 'short results', run: () => setLongContent(page, false) },
    ],
  })
})

for (const width of [1440, 1024]) {
  for (const constrained of [false, true]) test(`Display grouping keeps sort, density and columns in their slots at ${width}${constrained ? ' with wider text and scrollbar' : ''}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    // Exercise wider text metrics and a classic scrollbar even on macOS, whose
    // default overlay scrollbar and system font leave more room than Linux CI.
    if (constrained) await page.addStyleTag({ content: '.display-panel { font-family: var(--mono); } .floating { scrollbar-gutter: stable; } .floating::-webkit-scrollbar { width: 15px; height: 15px; }' })
    await page.getByRole('button', { name: /^Display:/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Display options', exact: true })
    const groups = dialog.getByRole('radiogroup', { name: 'Group by' })
    await expectStableControls({
      controls: { groups, ...Object.fromEntries(['None', 'Status', 'Priority'].map(name => [name, groups.getByRole('radio', { name, exact: true })])), expand: dialog.getByRole('button', { name: 'Expand groups' }), collapse: dialog.getByRole('button', { name: 'Collapse groups' }), sort: dialog.locator('.sort-editor'), density: dialog.getByRole('radiogroup', { name: 'Row height' }), columns: dialog.locator('.columns') },
      scrollAreas: { dialog },
      interactions: ['None', 'Status', 'Priority', 'None'].map(name => ({ name, run: async () => { await groups.getByRole('radio', { name, exact: true }).click(); await expect(groups.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
    })
    await expect(dialog.getByRole('button', { name: 'Expand groups' })).toBeDisabled()
    for (const button of await dialog.locator('.pair button').all()) {
      expect(await button.evaluate(el => el.scrollWidth - el.clientWidth), 'group action label fits its button').toBeLessThanOrEqual(1)
    }
  })

  test(`invalid date range feedback never moves Apply at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    await page.getByRole('button', { name: 'Filter by more' }).click()
    await page.getByRole('menuitem', { name: /Date/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Filter by date' })
    const from = dialog.getByLabel('From', { exact: true }), to = dialog.getByLabel('To', { exact: true })
    const apply = dialog.getByRole('button', { name: 'Apply range' })
    await expectStableControls({
      controls: { fields: dialog.getByRole('radiogroup', { name: 'Which date' }), from, to, apply },
      scrollAreas: { dialog },
      interactions: [
        { name: 'valid start', run: () => from.fill('2026-10-02') },
        { name: 'invalid end', run: async () => { await to.fill('2026-10-01'); await expect(dialog.getByRole('alert')).toHaveText('The end is before the start.'); await expect(apply).toBeDisabled() } },
        { name: 'valid end', run: async () => { await to.fill('2026-10-03'); await expect(dialog.getByRole('alert')).toHaveCount(0); await expect(apply).toBeEnabled() } },
      ],
    })
  })
}

for (const width of [390, 1440]) {
  test(`model estimate picker hints and selection hold every control at ${width}`, async ({ page }) => {
    await mountShell(page, 'model-picker', width)
    const picker = page.getByRole('dialog', { name: 'Fixture picker' })
    const options = picker.getByRole('option')
    await expect(options).toHaveCount(3)
    const second = options.nth(1)
    await expectStableControls({
      controls: { search: picker.getByRole('searchbox'), group: picker.getByRole('listbox'), first: options.nth(0), clicked: second, last: options.nth(2) },
      scrollAreas: { list: picker.getByRole('listbox') },
      interactions: [
        { name: 'history arrives', run: async () => { await page.evaluate(async () => { const path = '/tests/helpers/no-shift-shells.ts'; const shell = await import(/* @vite-ignore */ path); shell.setModelHistory(true) }); await expect(second).toContainText('typically ~2.4 h') } },
        { name: 'select a model', run: async () => { await second.click(); await expect(second).toHaveAttribute('aria-selected', 'true') } },
        { name: 'uncalibrated history', run: async () => { await page.evaluate(async () => { const path = '/tests/helpers/no-shift-shells.ts'; const shell = await import(/* @vite-ignore */ path); shell.setModelHistory(false) }); await expect(second).not.toContainText('typically') } },
      ],
    })
  })
}
