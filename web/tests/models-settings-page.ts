// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test'
import { mockBoard } from './models-board-page'
import { boardPerson } from './models-board-fixtures'
export async function mockModelsSettings(page: Page, options: Parameters<typeof mockBoard>[1] = {}) {
  const state = await mockBoard(page, options)
  await page.route(/\/api\/(models\/resolve|model-preferences\/(coverage|evidence))(\?|$)/, route => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/coverage')) return route.fulfill({ json: { consumers: [{ consumer: 'managed_build', reads_board: true, agreement: 'server-authoritative' }, { consumer: 'managed_review', reads_board: true, agreement: 'server-authoritative' }, { consumer: 'lead_harness', reads_board: false, agreement: 'observed; Engine Wave 2 pending' }] } })
    if (url.pathname.endsWith('/evidence')) return route.fulfill({ json: { items: [], next_cursor: null } })
    const review = url.searchParams.get('role') === 'review-gate', thinking = state.getDocument().profile.thinking || 'standard'
    const effort = review ? 'xhigh' : { lean: 'medium', standard: 'high', deep: 'xhigh', max: 'xhigh' }[thinking]
    return route.fulfill({ json: { profile: { id: review ? 'grok-review' : 'sol-build', model: review ? 'grok-4.7' : 'gpt-6.1-sol', display_name: review ? 'Grok' : 'GPT-6.1 Sol', model_version: review ? '4.7' : '', family: review ? 'xai' : 'openai', harness: review ? 'grok' : 'codex', effort, enabled: true, tier: 'frontier' }, role: review ? 'review-gate' : 'build', owner_required: false, trace: { kind: url.searchParams.get('area') || 'other', column: url.searchParams.get('area') || 'other', situation: 'first', card_index: 1, preference_of: { person: boardPerson, source: 'person' }, held: [{ line: 'anthropic:opus', reason: 'Account at its floor' }], residency: { value: 'any' } } } })
  })
  return state
}
export async function openModelsSettings(page: Page, query = '') { await page.goto(`/tests/models-settings-harness.html${query}`); await expect(page.locator('[data-models-ready="true"]')).toBeVisible(); await expect(page.locator('[data-next-sentence]')).toContainText('Sol') }
