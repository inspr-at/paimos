// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

async function setup(page: Page, role: 'admin' | 'member' = 'admin') {
  await mockWork(page, fixtures(), { admin: role === 'admin' })
  await mockBusiness(page, businessData({ role }), { role })
  await mockSettings(page, settingsData())
  const state = { enabled: false, base_url: '', chat_model: '', embedding_model: '', features: { crm_note_rewrite: false, embeddings: false }, provider_id: '', revision: 0, has_api_key: false }
  const writes: Record<string, unknown>[] = []
  let tests = 0
  let failTest = false
  let conflict = false
  await page.route('**/api/settings/model-provider**', async route => {
    const req = route.request()
    if (req.url().endsWith('/test')) {
      tests++
      return route.fulfill({ status: failTest ? 502 : 200, json: failTest ? { error: 'provider connection test failed' } : { ok: true } })
    }
    if (req.method() === 'PUT') {
      const body = req.postDataJSON() as Record<string, unknown>
      writes.push(body)
      if (conflict) return route.fulfill({ status: 409, json: { error: 'settings changed' } })
      Object.assign(state, { enabled: body.enabled, base_url: body.base_url, chat_model: body.chat_model, embedding_model: body.embedding_model, features: body.features, revision: state.revision + 1, provider_id: 'fixture-provider', has_api_key: body.api_key === '' ? false : body.api_key ? true : state.has_api_key })
    }
    return route.fulfill({ json: state })
  })
  return { state, writes, testCalls: () => tests, failTest: () => { failTest = true }, conflict: () => { conflict = true } }
}

test('workspace AI starts off, saves explicit feature choices and tests only saved configuration', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'In-app AI' })
  await expect(card.getByLabel('Enable workspace AI')).not.toBeChecked()
  await expect(card.getByLabel('Rewrite customer notes with AI')).not.toBeChecked()
  await expect(card.getByLabel('Semantic search and background indexing')).not.toBeChecked()
  await expect(card.getByRole('button', { name: 'Test connection' })).toBeDisabled()
  await card.getByLabel('API base URL').fill('http://localhost:11434/v1')
  await card.getByLabel('Chat model', { exact: true }).fill('local-fixture')
  await card.getByLabel('API key optional').fill('test-only-provider-value')
  await card.getByLabel('Enable workspace AI').check()
  await card.getByLabel('Rewrite customer notes with AI').check()
  await expect(card.getByRole('button', { name: 'Test connection' })).toBeDisabled()
  await card.getByRole('button', { name: 'Save settings' }).click()
  await expect(card.getByRole('status')).toContainText('Saved.')
  expect(world.writes).toHaveLength(1)
  expect(world.writes[0]).toMatchObject({ enabled: true, features: { crm_note_rewrite: true, embeddings: false }, expected_revision: 0, api_key: 'test-only-provider-value' })
  await expect(card.getByLabel('API key optional')).toHaveValue('')
  await card.getByRole('button', { name: 'Test connection' }).click()
  await expect(card.getByRole('status')).toContainText('Connection succeeded')
  expect(world.testCalls()).toBe(1)
  await card.getByLabel('Enable workspace AI').uncheck()
  await card.getByRole('button', { name: 'Save settings' }).click()
  await expect(card.getByRole('status')).toContainText('Saved.')
  expect(world.writes[1]).not.toHaveProperty('api_key')
  await card.getByRole('button', { name: 'Test connection' }).click()
  await expect(card.getByRole('status')).toContainText('Connection succeeded')
  expect(world.testCalls()).toBe(2)
  await card.getByLabel('Remove stored API key').check()
  await card.getByRole('button', { name: 'Save settings' }).click()
  await expect(card.getByRole('status')).toContainText('Saved.')
  expect(world.writes[2]).toHaveProperty('api_key', '')
})

test('connection failures and stale saves remain actionable', async ({ page }) => {
  const world = await setup(page)
  world.state.base_url = 'http://localhost:11434/v1'; world.state.chat_model = 'local-fixture'; world.state.revision = 1
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'In-app AI' })
  world.failTest()
  await card.getByRole('button', { name: 'Test connection' }).click()
  await expect(card.getByRole('alert')).toContainText('Connection test failed')
  await card.getByLabel('Chat model', { exact: true }).fill('other-fixture')
  world.conflict()
  await card.getByRole('button', { name: 'Save settings' }).click()
  await expect(card.getByRole('alert')).toContainText('Another admin changed these settings')
  await card.getByRole('button', { name: 'Reload' }).click()
  await expect(card.getByLabel('Chat model', { exact: true })).toHaveValue('local-fixture')
})

test('members do not receive provider controls', async ({ page }) => {
  await setup(page, 'member')
  await page.goto('/settings/workspace')
  await expect(page.getByRole('heading', { name: 'In-app AI' })).toHaveCount(0)
})
