// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

test('ticket benefits edit together with a precondition and preserve unrelated fields', async ({ page }) => {
  const errors = watchErrors(page)
  const data = fixtures()
  const target = data.nodes.find(n => n.key === 'PHAROS-12')!
  const prior = { ...target.fields }
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(ws.getByRole('region', { name: 'User benefit' })).toContainText('Before Done')
  await ws.getByRole('button', { name: 'Edit', exact: true }).click()
  await ws.getByLabel('Pill · English').fill('Clear release notes')
  await ws.getByLabel('Pill · Deutsch').fill('Verständliche Release Notes')
  await ws.getByLabel('Benefit · English').fill('Tickets explain what you gain.')
  await ws.getByLabel('Benefit · Deutsch').fill('Tickets erklären den Nutzen.')
  await ws.getByLabel('Hide from release notes').check()
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  const benefits = ws.getByRole('region', { name: 'User benefit' })
  await expect(benefits).toContainText('Tickets erklären den Nutzen.')
  await expect(benefits).toContainText('Hidden from release notes')
  await expect(benefits).not.toContainText('Before Done')
  const write = calls.find(c => c.method === 'PATCH' && c.path === `/api/nodes/${target.id}`)!
  expect(write.headers['if-unmodified-since']).toBeTruthy()
  expect(write.body).toEqual({ fields: { ...prior, pill_en: 'Clear release notes', pill_de: 'Verständliche Release Notes', benefit_en: 'Tickets explain what you gain.', benefit_de: 'Tickets erklären den Nutzen.', hide_from_release_notes: true } })
  expect(errors).toEqual([])
})

test('read-only ticket shows missing-language guidance without edit controls', async ({ page }) => {
  const data = fixtures()
  await mockWork(page, data, { readOnly: true })
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(ws.getByRole('region', { name: 'User benefit' })).toContainText('both languages')
  await expect(ws.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
})

test('release notes switch languages and expose gaps without Git-headline fallback', async ({ page }) => {
  const { mockReleases, releaseHistory } = await import('./releases-fixtures')
  const history = releaseHistory()
  const current = history.releases[0]!
  Object.assign(current, { notes: { source: 'tag:release-notes/synthetic.json', snapshot_sha256: 'a'.repeat(64), captured_at: '2026-09-28T12:00:00Z', release_revision: 2, hidden: 1, gaps: ['AEON-99: benefit_de is required'], items: [{ id: 'note-one', key: 'AEON-75', pill_en: 'Clear release notes', pill_de: 'Verständliche Release Notes', benefit_en: 'Tickets explain what you gain.', benefit_de: 'Tickets erklären den Nutzen.' }] } })
  await mockWork(page, fixtures())
  await mockReleases(page, history)
  await page.goto(`/releases/${current.version}`)
  const notes = page.getByRole('region', { name: 'Release notes' })
  await expect(notes).toContainText('Tickets explain what you gain.')
  await expect(notes).toContainText('benefit_de is required')
  await expect(notes).not.toContainText(current.headline)
  await notes.getByRole('button', { name: 'Deutsch', exact: true }).click()
  await expect(notes).toContainText('Tickets erklären den Nutzen.')
  await expect(notes).not.toContainText('Tickets explain what you gain.')
})
