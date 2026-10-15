// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1309: retain the panel authority and stability regressions after balancing the spec files.
import { expect, test } from '@playwright/test'
import type { NativeStreamEvent } from '../src/lib/nativeSessionContract'
import { WORKER, binding, makeNative, setup, refresh } from './native-session-program-fixtures'

// Risk: syntactically valid JSON still has an untrusted shape. Reject it before
// granting a viewer, rendering a frame or restoring legacy write controls.
test('AEON-1309: malformed native JSON and invalid shapes stay incomplete without granting control', async ({ page }) => {
  const { state, worker, errors } = await setup(page)
  await page.goto(`/agents/${WORKER}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toBeVisible()
  await panel.getByRole('tab', { name: 'Events', exact: true }).click()
  const watch = panel.getByRole('button', { name: 'Watch live', exact: true })
  await expect(watch).toBeEnabled()

  let context = ''
  await page.route(`**/api/projects/p-pharos/harness-sessions/${WORKER}/native-context`, route => route.fulfill({ contentType: 'application/json', body: context }))
  for (const [index, body] of ['{', 'null', JSON.stringify({ ...makeNative(), capabilities: { ...makeNative().capabilities, take_supported: 'true' } })].entries()) {
    context = body; worker.heartbeat_at = `2026-10-06T10:01:0${index * 2}Z`
    await refresh(page)
    await expect(panel.locator('#session-panel-events')).toContainText('The session’s live capabilities could not be read.')
    await panel.getByRole('tab', { name: 'Chat', exact: true }).click()
    await expect(panel.locator('#session-panel-messages')).toContainText('Owned session input from this panel is not available yet.')
    await expect(panel.locator('textarea')).toHaveCount(0)
    await panel.getByRole('tab', { name: 'Events', exact: true }).click()
    // Restore a proven readable context between failures, so a previous error
    // cannot satisfy the next malformed-response assertion.
    context = JSON.stringify(state.native); worker.heartbeat_at = `2026-10-06T10:01:0${index * 2 + 1}Z`
    await refresh(page)
    await expect(watch).toBeEnabled()
  }

  const emptyPage = { generation: 1, items: [], next_before: null, truncated: false }
  const grant = { viewer_id: 'viewer-fixture', mode: 'view', control_capability: null, binding, expires_in_ms: 30_000 }
  const event: NativeStreamEvent = { binding, generation: 1, seq: '1', sequence: '1', observed_at: '2026-10-06T10:00:00Z', dropped_frames: '0', source: 'native', kind: 'assistant.delta', payload: { message_id: 'boundary-event', text: 'Validated event' } }
  let replay = JSON.stringify(emptyPage), attach = JSON.stringify(grant), stream = ''
  let attached = 0, streamed = 0, detached = 0
  await page.route('**/api/projects/p-pharos/native-sessions/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'application/json', body: replay })
    if (path.endsWith('/attach')) {
      attached++
      expect(route.request().postDataJSON()).toMatchObject({ mode: 'view', view: 'structured', consent_id: null, expected_binding: binding })
      return route.fulfill({ contentType: 'application/json', body: attach })
    }
    if (path.endsWith('/stream')) {
      streamed++
      expect(route.request().headers()['x-aeon-viewer-id']).toBe(grant.viewer_id)
      return route.fulfill({ contentType: 'text/event-stream', body: stream })
    }
    expect(path).toMatch(/\/detach$/)
    detached++
    expect(route.request().postDataJSON()).toMatchObject({ viewer_id: grant.viewer_id, expected_binding: binding })
    return route.fulfill({ json: {} })
  })
  const rejected = async () => {
    await watch.click()
    await expect(panel.getByText('Live events could not be read. The view is incomplete. Watch live to try again.', { exact: true })).toBeVisible()
    await expect(panel.locator('[data-event-row]')).toHaveCount(0)
    await expect(watch).toBeEnabled()
  }
  for (const body of ['{', 'null', JSON.stringify({ ...emptyPage, items: [null] })]) {
    replay = body; await rejected()
    expect(attached).toBe(0); expect(streamed).toBe(0)
  }
  replay = JSON.stringify(emptyPage)
  for (const body of ['{', JSON.stringify({ ...grant, viewer_id: 42 })]) {
    attach = body; await rejected()
    expect(streamed).toBe(0); expect(detached).toBe(0)
  }
  attach = JSON.stringify(grant)
  for (const [index, body] of ['{', 'null', JSON.stringify({ ...event, observed_at: 42 })].entries()) {
    stream = `data: ${body}\n\n`; await rejected()
    expect(streamed).toBe(index + 1); expect(detached).toBe(index + 1)
  }
  stream = `data: ${JSON.stringify(event)}\n\n`
  await watch.click()
  await expect(panel.locator('[data-event-row]')).toHaveCount(1)
  await expect(panel.locator('[data-event-row]')).toContainText('Validated event')
  await expect.poll(() => detached).toBe(4)
  expect(errors).toEqual([])
})
