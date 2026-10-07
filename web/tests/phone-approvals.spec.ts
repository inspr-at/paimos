// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { controlStability } from './control-stability'
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

const endpoint = 'https://web.push.apple.com/phone-polish-test'
const endpointHash = createHash('sha256').update(JSON.stringify(endpoint)).digest('hex')
async function phoneDevice(page: Page) {
 await page.addInitScript(({ endpoint }) => {
  let subscribed = false, permission = 'default'
  Object.defineProperty(window, 'Notification', { configurable: true, value: {
   get permission() { return permission }, requestPermission: async () => { permission = 'granted'; return permission },
  } })
  const subscription = { endpoint, toJSON: () => ({ endpoint, keys: { p256dh: 'AQID', auth: 'AQID' } }) }
  const registration = { pushManager: { getSubscription: async () => subscribed ? subscription : null,
   subscribe: async () => { subscribed = true; return subscription } } }
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: {
   getRegistration: async () => registration, register: async () => registration, ready: Promise.resolve(registration),
  } })
  Object.defineProperty(PublicKeyCredential, 'isUserVerifyingPlatformAuthenticatorAvailable', { value: async () => true })
  Object.defineProperty(navigator.credentials, 'create', { configurable: true, value: async (options: CredentialCreationOptions) => {
   if (options.publicKey?.authenticatorSelection?.userVerification !== 'required' || options.publicKey.authenticatorSelection.authenticatorAttachment !== 'platform') throw new Error('Platform verification missing')
   const probe = window as unknown as { passkeyCalls: number; cancelPasskey?: boolean }
   probe.passkeyCalls = (probe.passkeyCalls ?? 0) + 1
   if (probe.cancelPasskey) throw new DOMException('Cancelled', 'NotAllowedError')
   const bytes = new Uint8Array([1, 2, 3]).buffer
   return { id: 'AQID', rawId: bytes, type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}),
    response: { clientDataJSON: bytes, attestationObject: bytes, getTransports: () => ['internal'] } }
  } })
 }, { endpoint })
}

// Risk: another browser's subscription is mistaken for enabling this device,
// or status/feedback moves the next tap. Exercise the subscription round-trip.
test('device notifications replace Enable with state and stable Pause in both themes', async ({ page }, testInfo) => {
 await setup(page); await phoneDevice(page)
 const settings = { available: true, push_available: true, vapid_public_key: 'AQID',
  preferences: { enabled: true, time_zone: 'Europe/Vienna', quiet_start: 1320, quiet_end: 420, escalation_minutes: 15 },
  passkeys: [{ id: challenge, created_at: '2026-10-01T12:00:00Z' }], subscriptions: [{ id: 'another-device', created_at: '2026-10-01T12:00:00Z', endpoint_hash: 'b'.repeat(64) }] }
 let failSave = false
 await page.route('**/api/me/phone-approvals**', route => {
  if (route.request().method() === 'POST') { settings.subscriptions.push({ id, created_at: '2026-10-07T12:00:00Z', endpoint_hash: endpointHash }); return route.fulfill({ json: { id, created_at: '2026-10-07T12:00:00Z' } }) }
  if (route.request().method() === 'PUT') {
   if (failSave) return route.fulfill({ status: 503, json: { error: 'Notification settings could not be saved.' } })
   settings.preferences = route.request().postDataJSON(); return route.fulfill({ json: settings.preferences })
  }
  return route.fulfill({ json: settings })
 })
 for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  settings.subscriptions = settings.subscriptions.filter(sub => sub.id !== id); settings.preferences.enabled = true; failSave = false
  await page.setViewportSize({ width, height: 1000 }); await page.goto('/settings/personal#phone-approvals')
  const card = page.getByRole('region', { name: 'Phone approvals', exact: true })
  await expect(card.getByRole('button', { name: 'Add device passkey' })).toBeEnabled()
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme; document.querySelector('#phone-approvals .phone-settings > p')!.textContent = 'Benachrichtigungen zu Freigabeanfragen werden auf diesem Gerät angezeigt. Die ausführliche Anfrage bleibt im angemeldeten Arbeitsbereich und wird vor jeder Entscheidung mit dem Geräte-Passkey bestätigt.' }, theme)
  await card.scrollIntoViewIfNeeded()
  const action = card.locator('.notification-action')
  const controls = { add: card.getByRole('button', { name: 'Add device passkey' }), notification: action, save: card.getByRole('button', { name: 'Save notification settings' }), quiet: card.getByRole('checkbox', { name: 'Quiet hours' }), hours: card.locator('.hours') }
  const guard = await controlStability(page, controls)
  await expect(action).toHaveAccessibleName('Enable notifications on this device')
  await page.screenshot({ path: testInfo.outputPath(`aeon-903-phonepolish/settings-${width}-${theme}.png`), fullPage: true })
  await guard.check(async () => {
   await action.click(); await expect(action).toHaveAccessibleName('Pause notifications')
   await expect(card.getByText('Notifications on for this device', { exact: true })).toBeVisible()
   await expect(card.getByRole('button', { name: 'Enable notifications on this device' })).toHaveCount(0)
  })
  await page.screenshot({ path: testInfo.outputPath(`aeon-903-phonepolish/notifications-on-${width}-${theme}.png`), fullPage: true })
  await guard.check(async () => {
   failSave = true; await action.click()
   await expect(card.getByRole('alert')).toContainText('could not be saved')
   await expect(action).toHaveAccessibleName('Pause notifications'); await expect(card.getByRole('status')).toHaveCount(0)
  })
  await guard.check(async () => {
   failSave = false; await action.click(); await expect(action).toHaveAccessibleName('Enable notifications on this device')
   await expect(card.getByText('Notifications paused', { exact: true })).toBeVisible()
  })
  await guard.check(async () => { await controls.quiet.uncheck(); await expect(card.getByLabel('From', { exact: true })).toBeDisabled() })
  guard.done()
 }
})

// Risk: a deferred section bundle or phone API blocks the Settings shell.
test('phone Settings shell renders before its personal section and phone data', async ({ page }) => {
 await setup(page); await phoneDevice(page)
 let releaseSection!: () => void, releaseData!: () => void
 const section = new Promise<void>(resolve => { releaseSection = resolve }), data = new Promise<void>(resolve => { releaseData = resolve })
 await page.route('**/src/components/settings/PersonalSection.vue', async route => { await section; await route.continue() })
 await page.route('**/api/me/phone-approvals', async route => { await data; await route.fulfill({ json: { available: true, push_available: false, vapid_public_key: '', preferences: { enabled: false, time_zone: 'UTC', quiet_start: 0, quiet_end: 0, escalation_minutes: 15 }, passkeys: [], subscriptions: [] } }) })
 await page.setViewportSize({ width: 390, height: 1000 }); await page.goto('/settings/personal')
 await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible()
 const picker = page.getByRole('button', { name: 'Section: Personal. Choose another section' })
 const guard = await controlStability(page, { picker })
 await guard.check(async () => { await picker.click(); await expect(page.getByRole('navigation', { name: 'Settings sections' })).toBeVisible(); await picker.click() })
 await guard.check(async () => {
  releaseSection(); await expect(page.getByRole('heading', { name: 'Phone approvals', exact: true })).toBeVisible()
  await expect(page.getByText('Loading phone approvals…')).toBeVisible()
 })
 await guard.check(async () => { releaseData(); await expect(page.getByRole('button', { name: 'Save notification settings' })).toBeVisible() })
 guard.done()
})

test('registration awaits one fresh options response before platform verification and keeps actions still', async ({ page }) => {
 await setup(page); await phoneDevice(page)
 let release!: () => void
 const optionsReady = new Promise<void>(resolve => { release = resolve })
 let options = 0, registrations = 0
 const settings = { available: true, push_available: false, vapid_public_key: '', preferences: { enabled: false, time_zone: 'UTC', quiet_start: 0, quiet_end: 0, escalation_minutes: 15 }, passkeys: [] as { id: string; created_at: string }[], subscriptions: [] }
 await page.route('**/api/me/phone-approvals**', async route => {
  if (route.request().url().endsWith('/options')) {
   options++; await optionsReady
   return route.fulfill({ json: { challenge_id: challenge, publicKey: { challenge: 'AQID', user: { id: 'AQID', name: 'person', displayName: 'Person' }, rp: { name: 'Aeon', id: '127.0.0.1' }, pubKeyCredParams: [{ type: 'public-key', alg: -7 }] } } })
  }
  if (route.request().method() === 'POST') {
   registrations++; const body = route.request().postDataJSON()
   expect(body.challenge_id).toBe(challenge); expect(body.credential.response.attestationObject).toBe('AQID')
   settings.passkeys = [{ id: challenge, created_at: '2026-10-07T12:00:00Z' }]; return route.fulfill({ json: settings.passkeys[0] })
  }
  return route.fulfill({ json: settings })
 })
 await page.setViewportSize({ width: 390, height: 1000 }); await page.goto('/settings/personal#phone-approvals')
 const card = page.getByRole('region', { name: 'Phone approvals', exact: true }), add = card.getByRole('button', { name: 'Add device passkey' })
 await expect(add).toBeEnabled()
 const guard = await controlStability(page, { add, notification: card.locator('.notification-action'), save: card.getByRole('button', { name: 'Save notification settings' }) })
 await guard.check(async () => {
  await add.click(); await expect(card.getByRole('status')).toHaveText('Preparing passkey verification…')
  expect(await page.evaluate(() => (window as unknown as { passkeyCalls?: number }).passkeyCalls ?? 0)).toBe(0)
  release(); await expect(card.getByRole('status')).toHaveText('Passkey added.')
 })
 expect(options).toBe(1); expect(registrations).toBe(1)
 await page.evaluate(() => { (window as unknown as { cancelPasskey: boolean }).cancelPasskey = true })
 await guard.check(async () => { await add.click(); await expect(card.getByRole('alert')).toHaveText('Verification cancelled. No passkey was added.') })
 expect(options).toBe(2); expect(registrations).toBe(1); guard.done()
})
