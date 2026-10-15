// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

const benefits = {
  pill_en: 'Clear release notes', pill_de: 'Verständliche Hinweise für Arbeit',
  benefit_en: 'Each work item explains the benefit of its change.',
  benefit_de: 'Jeder Arbeitsschritt erklärt den Nutzen seiner Änderung, auch bei ausführlichen Beschreibungen in deutscher Sprache.',
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`migrated work completion and benefit editing ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.clock.setSystemTime(new Date('2026-10-04T10:00:00Z'))
    const data = fixtures(); data.preferences.theme = { choice: theme }
    const template = data.nodes.find(n => n.id === 'n-1')!
    data.nodes = [
      { ...template, id: 'first', key: 'PHAROS-1', parent_id: 'p-pharos', kind_slug: 'work', is_leaf: true, work_children_count: 0, state: 'open', fields: { priority: 'high' }, title: 'Arbeitsschritt mit ausführlicher Beschreibung für die Abschlussprüfung' },
      { ...template, id: 'second', key: 'PHAROS-2', parent_id: 'p-pharos', kind_slug: 'work', is_leaf: true, work_children_count: 0, state: 'open', fields: {}, title: 'Weiterer Arbeitsschritt für die Bearbeitung des Nutzens' },
    ]
    const errors = watchErrors(page), calls = await mockWork(page, data, { admin: true, entryChecks: true })
    await page.goto('/p/PHAROS/tickets?closed=1&sort=key')
    const row = page.getByRole('grid', { name: 'Tickets' }).locator('#row-first')
    await row.getByRole('button', { name: /Change status of PHAROS-1/ }).click()
    await expect(page.getByRole('menuitemradio', { name: /^Done/ })).toContainText('0 of 1')
    await page.getByRole('menuitemradio', { name: /^Done/ }).click()
    const dialog = page.getByRole('dialog', { name: 'PHAROS-1: Done checks' })
    await expect(dialog).toBeVisible()
    expect(calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
    // The configured check opens the approved checklist before a mutation.
    // Empty fixes still point to the field, and passing checks need no override.
    await dialog.getByRole('button', { name: 'Write it', exact: true }).click()
    const saveCopy = dialog.getByRole('button', { name: /^Save/ })
    const gateStable = await controlStability(page, { saveCopy, close: dialog.getByRole('button', { name: 'Close checklist', exact: true }) })
    await gateStable.check(async () => {
      await saveCopy.click()
      await expect(dialog.getByLabel('Pill · English')).toHaveAttribute('aria-invalid', 'true')
    })
    await gateStable.check(async () => {
      await dialog.getByLabel('Pill · English').fill(benefits.pill_en)
      await dialog.getByLabel('Pill · Deutsch').fill(benefits.pill_de)
      await dialog.getByLabel('Benefit · English').fill(benefits.benefit_en)
      await dialog.getByLabel('Benefit · Deutsch').fill(benefits.benefit_de)
      await dialog.getByRole('checkbox', { name: 'Hide from release notes' }).check()
    })
    gateStable.done()
    expect(calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
    await page.screenshot({ path: testInfo.outputPath(`gate-${width}-${theme}.png`) })
    await saveCopy.click()
    const move = dialog.getByRole('button', { name: /^Move to Done(?: (?:Command|Ctrl) Enter)?$/ })
    await expect(move).toBeEnabled()
    await expect(dialog.getByRole('textbox', { name: 'Why move it now?' })).toHaveValue('')
    await move.click()
    await expect(dialog).toHaveCount(0)
    const firstPatch = calls.filter(c => c.method === 'PATCH' && c.path === '/api/nodes/first')
    expect(firstPatch).toHaveLength(2)
    expect(firstPatch[0].headers['if-unmodified-since']).toBe(template.updated_at)
    expect(firstPatch[0].body).toEqual({ fields: { priority: 'high', ...benefits, hide_from_release_notes: true } })
    expect(firstPatch[1].body).toEqual({ state: 'done' })
    expect(firstPatch[1].headers['if-unmodified-since']).not.toBe(template.updated_at)
    await expect(row.locator('.status-btn')).toHaveText('Done')

    await page.goto('/p/PHAROS/PHAROS-2?closed=1')
    const workspace = page.getByRole('complementary', { name: 'Ticket details' })
    await expect(workspace.locator('.ws-benefits').getByText('User benefit', { exact: true })).toBeVisible()
    await workspace.getByRole('button', { name: 'Edit', exact: true }).click()
    const form = workspace.getByRole('form', { name: 'Edit PHAROS-2' })
    await expect(form.getByLabel('Pill · English')).toBeVisible()
    await form.getByRole('button', { name: 'Status Open', exact: true }).click()
    await expect(page.getByRole('menuitemradio', { name: /^Done/ })).toContainText('0 of 1')
    await page.getByRole('menuitemradio', { name: /^Done/ }).click()
    const save = workspace.getByRole('button', { name: 'Save', exact: true })
    const editStable = await controlStability(page, { save, cancel: workspace.getByRole('button', { name: 'Cancel', exact: true }) })
    await editStable.check(async () => {
      await save.click()
      await expect(form.getByLabel('Pill · English')).toHaveAttribute('aria-invalid', 'true')
      await expect(form.getByLabel('Pill · English')).toBeFocused()
    })
    expect(calls.filter(c => c.method === 'PATCH' && c.path === '/api/nodes/second')).toHaveLength(0)
    await editStable.check(async () => {
      await form.getByLabel('Pill · English').fill(benefits.pill_en)
      await form.getByLabel('Pill · Deutsch').fill(benefits.pill_de)
      await form.getByLabel('Benefit · English').fill(benefits.benefit_en)
      await form.getByLabel('Benefit · Deutsch').fill(benefits.benefit_de)
    })
    editStable.done()
    await page.screenshot({ path: testInfo.outputPath(`editor-${width}-${theme}.png`) })
    await save.click()
    await expect(page.getByText('Saved PHAROS-2', { exact: true })).toBeVisible()
    const secondPatch = calls.filter(c => c.method === 'PATCH' && c.path === '/api/nodes/second')
    expect(secondPatch).toHaveLength(1)
    expect(secondPatch[0].headers['if-unmodified-since']).toBe(template.updated_at)
    expect(secondPatch[0].body).toMatchObject({ state: 'done', fields: benefits })
    await expect(workspace.locator('.ws-benefits').getByText(benefits.benefit_de, { exact: true })).toBeVisible()
    expect(errors).toEqual([])
  })
}
