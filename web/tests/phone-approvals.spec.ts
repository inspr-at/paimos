// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
const id = '33333333-3333-4333-8333-333333333333'
const challenge = '44444444-4444-4444-8444-444444444444'
const hash = 'a'.repeat(64)
const review = () => ({ kind: 'approval', request_id: id, request_hash: hash, pending: true, approval: {
  id, agent_principal_id: '22222222-2222-4222-8222-222222222222', scope: 'journey.deploy', risk: 'high',
  resource_kind: 'tenant', rationale: 'Deploy the reviewed release to the specified server.',
  expires_at: new Date(Date.now() + 3600_000).toISOString(), proposed_at: new Date().toISOString(), decision: null,
  target: { hosts: ['edge-test'], environment: 'staging', service: 'aeon', change: 'Reviewed update' },
} })
async function setup(page: Page, cancel = false) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  await page.addInitScript(({ cancel }) => {
    Object.defineProperty(navigator.credentials, 'get', { value: async (options: CredentialRequestOptions) => {
      if (options.publicKey?.userVerification !== 'required') throw new Error('UV missing')
      if (cancel) throw new DOMException('Cancelled', 'NotAllowedError')
      const bytes = new Uint8Array([1, 2, 3]).buffer
      return { id: 'AQID', rawId: bytes, type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}),
        response: { clientDataJSON: bytes, authenticatorData: bytes, signature: bytes, userHandle: null } }
    } })
  }, { cancel })
  let decisions = 0, options = 0
  await page.route(`**/api/phone-approvals/approval/${id}**`, route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/options')) {
      options++
      const body = route.request().postDataJSON()
      expect(body.request_hash).toBe(hash)
      return route.fulfill({ json: { challenge_id: challenge, publicKey: { challenge: 'AQID', rpId: '127.0.0.1', allowCredentials: [{ id: 'AQID', type: 'public-key' }], userVerification: 'required' } } })
    }
    if (path.endsWith('/decision')) {
      decisions++
      const body = route.request().postDataJSON()
      expect(body.challenge_id).toBe(challenge)
      expect(body.credential.response.signature).toBe('AQID')
      const value = review(); value.pending = false; value.approval.decision = body.decision
      return route.fulfill({ json: value })
    }
    return route.fulfill({ json: review() })
  })
  return { decisions: () => decisions, options: () => options }
}
test('phone review requires an explicit tap and fresh passkey; cancellation records nothing', async ({ page }) => {
  const calls = await setup(page, true)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`/phone-approvals/approval/${id}`)
  await expect(page.getByRole('heading', { name: 'Review approval' })).toBeVisible()
  await expect(page.getByText('edge-test', { exact: false })).toBeVisible()
  expect(calls.options()).toBe(0); expect(calls.decisions()).toBe(0)
  await page.getByRole('button', { name: 'Approve with passkey' }).click()
  await expect(page.getByRole('alert')).toContainText('Verification cancelled')
  expect(calls.options()).toBe(1); expect(calls.decisions()).toBe(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
test('phone decline sends a bound assertion and announces the recorded result', async ({ page }) => {
  const calls = await setup(page)
  await page.goto(`/phone-approvals/approval/${id}`)
  await page.getByLabel('Reason (optional; shared with the agent)').fill('Wait for the maintenance window')
  await page.getByRole('button', { name: 'Decline with passkey' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Declined.' })).toContainText('Your decision was recorded')
  expect(calls.options()).toBe(1); expect(calls.decisions()).toBe(1)
  await expect(page.getByRole('button', { name: 'Approve with passkey' })).toHaveCount(0)
})
test('ended requests offer context without a decision button', async ({ page }) => {
  await setup(page)
  await page.route(`**/api/phone-approvals/approval/${id}`, route => route.fulfill({ json: { ...review(), pending: false } }))
  await page.goto(`/phone-approvals/approval/${id}`)
  await expect(page.getByText('This request has ended or was already decided.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Approve with passkey' })).toHaveCount(0)
})
test('personal phone settings save quiet hours and revoke a device', async ({ page }) => {
  await setup(page)
  const settings = { available: true, push_available: false, vapid_public_key: '',
    preferences: { enabled: false, time_zone: 'Europe/Vienna', quiet_start: 1320, quiet_end: 420, escalation_minutes: 15 },
    passkeys: [{ id: challenge, created_at: '2026-10-01T12:00:00Z' }], subscriptions: [{ id, created_at: '2026-10-01T12:00:00Z' }] }
  await page.route('**/api/me/phone-approvals**', route => {
    if (route.request().method() === 'PUT') { settings.preferences = route.request().postDataJSON(); return route.fulfill({ json: settings.preferences }) }
    if (route.request().method() === 'DELETE') { settings.subscriptions = []; return route.fulfill({ status: 204 }) }
    return route.fulfill({ json: settings })
  })
  await page.setViewportSize({ width: 390, height: 1000 })
  await page.goto('/settings/personal#phone-approvals')
  const card = page.getByRole('region', { name: 'Phone approvals', exact: true })
  await expect(card.getByRole('button', { name: 'Enable notifications on this device' })).toBeDisabled()
  await card.getByLabel('From', { exact: true }).fill('21:30')
  await card.getByRole('button', { name: 'Save notification settings' }).click()
  await expect(card.getByRole('status')).toHaveText('Notification settings saved.')
  expect(settings.preferences.quiet_start).toBe(1290)
  await card.getByRole('button', { name: 'Revoke device 1' }).click()
  await expect(card.getByRole('status')).toHaveText('Access revoked.')
  await expect(card.getByRole('button', { name: 'Revoke device 1' })).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  const audit = await new AxeBuilder({ page }).include('#phone-approvals').analyze()
  expect(audit.violations).toEqual([])
})
