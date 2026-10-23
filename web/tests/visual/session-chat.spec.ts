// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Response } from '@playwright/test'
import { visualCases, checkVisual } from './checkpoint'
import { sessionChat } from './fixtures'

for (const checkpoint of visualCases('session-chat')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, sessionChat)
  })
}

// Risk: optional native/chat reads escape the fixture to a backend 404, so
// the intended chat reference depicts an outage before comparison can run.
test('P02 Session Chat fixture binds its chat reads and native refusal to the displayed session', async ({ page }) => {
  const errors: string[] = [], reads: Response[] = []
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => reads.push(response))
  const nativeRead = page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/native-context'))
  const threadRead = page.waitForResponse(response => /^\/api\/chat-sessions\/[^/]+\/thread$/.test(new URL(response.url()).pathname))
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await sessionChat(page, 'desktop', 'light')
  const [native, thread] = await Promise.all([nativeRead, threadRead])
  expect(native.status(), 'legacy chat requires the typed native refusal, not a backend 404 or a native snapshot').toBe(503)
  expect(thread.status(), 'chat binding must be mocked, not a backend refusal').toBe(200)
  const session = page.url().split('/').at(-1)!.split('?')[0]!
  const binding = await thread.json()
  expect(new URL(native.url()).pathname).toBe(`/api/projects/p-pharos/harness-sessions/${session}/native-context`)
  expect(new URL(thread.url()).pathname).toBe(`/api/chat-sessions/${session}/thread`)
  expect(await native.json()).toEqual({ reason: 'feature_disabled', request_id: null, retryable: false })
  expect(binding).toMatchObject({ contract: 'chat-v1', conversation_id: '7c000000-0000-4000-8000-000000001674' })
  const historyPath = `/api/chat-threads/${binding.conversation_id}/messages`
  await expect.poll(() => reads.some(response => new URL(response.url()).pathname === historyPath)).toBe(true)
  const history = reads.find(response => new URL(response.url()).pathname === historyPath)!
  expect(history.status()).toBe(200)
  expect(new URL(history.url()).pathname).toBe(`/api/chat-threads/${binding.conversation_id}/messages`)
  expect(await history.json()).toMatchObject({ contract: 'chat-v1', items: [], has_more_before: false, has_more_after: false })
  await page.getByRole('complementary', { name: 'Session details' }).getByRole('tab', { name: /^Events/ }).click()
  await expect(page.getByText('Live events are not available for this session yet.', { exact: true })).toBeVisible()
  await page.getByRole('complementary', { name: 'Session details' }).getByRole('tab', { name: /^Chat/ }).click()
  await expect(page.getByRole('textbox', { name: 'Message to Release coordination', exact: true })).toBeVisible()
  expect(errors).toEqual([])
  expect(reads.filter(response => response.status() >= 400).map(response => ({ path: new URL(response.url()).pathname, status: response.status() })))
    .toEqual([{ path: new URL(native.url()).pathname, status: 503 }])
})
