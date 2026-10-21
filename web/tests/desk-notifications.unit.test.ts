// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, test, vi } from 'vitest'
vi.mock('../src/lib/api.ts', () => ({ api: vi.fn(), APIError: class extends Error {} }))
import { api } from '../src/lib/api'
import { deskOfferDismissed, deskOfferEligible, deskPushBrowser, dismissDeskOffer, setDeskSubscription, type PhoneSettings } from '../src/lib/phoneApprovals'

afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })
const settings: PhoneSettings = { available: true, push_available: true, vapid_public_key: 'AQID', passkeys: [], subscriptions: [],
  preferences: { enabled: false, time_zone: 'UTC', quiet_start: 0, quiet_end: 0, escalation_minutes: 15 } }
const browser = { supported: true, homeScreenRequired: false, permission: 'default' as const }

test('Desk offer requires every eligibility condition and tolerates unavailable browser storage', () => {
  vi.stubGlobal('localStorage', { getItem() { throw new Error('blocked') }, setItem() { throw new Error('blocked') } })
  expect(deskOfferDismissed()).toBe(false)
  expect(() => dismissDeskOffer()).not.toThrow()
  expect(deskOfferEligible(settings, 'current', browser, false)).toBe(true)
  expect(deskOfferEligible({ ...settings, push_available: false }, 'current', browser, false)).toBe(false)
  expect(deskOfferEligible(settings, 'current', { ...browser, supported: false }, false)).toBe(false)
  expect(deskOfferEligible(settings, 'current', { ...browser, permission: 'denied' }, false)).toBe(false)
  expect(deskOfferEligible(settings, 'current', { ...browser, homeScreenRequired: true }, false)).toBe(false)
  expect(deskOfferEligible(settings, 'current', browser, true)).toBe(false)
  const enabled = { ...settings, subscriptions: [{ id: 'sub', created_at: '', endpoint_hash: 'current', desk_enabled: true }] }
  expect(deskOfferEligible(enabled, 'current', browser, false)).toBe(false)
  expect(deskOfferEligible(enabled, 'other', browser, false)).toBe(true)
  expect(deskOfferEligible({ ...enabled, subscriptions: [{ ...enabled.subscriptions[0], desk_enabled: false }] }, 'current', browser, false)).toBe(true)
})

test('iPhone and iPad require the Home Screen while standalone and desktop browsers remain eligible', () => {
  vi.stubGlobal('window', { Notification: {}, PushManager: {}, matchMedia: () => ({ matches: false }) })
  vi.stubGlobal('Notification', { permission: 'default' })
  vi.stubGlobal('navigator', { serviceWorker: {}, userAgent: 'iPhone', standalone: false })
  expect(deskPushBrowser()).toEqual({ ...browser, homeScreenRequired: true })
  vi.stubGlobal('navigator', { serviceWorker: {}, userAgent: 'Macintosh', platform: 'MacIntel', maxTouchPoints: 5, standalone: false })
  expect(deskPushBrowser().homeScreenRequired).toBe(true)
  vi.stubGlobal('navigator', { serviceWorker: {}, userAgent: 'iPhone', standalone: true })
  expect(deskPushBrowser().homeScreenRequired).toBe(false)
  vi.stubGlobal('navigator', { serviceWorker: {}, userAgent: 'Macintosh', platform: 'MacIntel', maxTouchPoints: 0 })
  expect(deskPushBrowser().homeScreenRequired).toBe(false)
})

function subscriptionBrowser() {
  const subscription = { toJSON: () => ({ endpoint: 'https://push.example.test/device', expirationTime: null, keys: { auth: 'AQID', p256dh: 'BAUG' } }) }
  const unsubscribe = vi.fn(), requestPermission = vi.fn()
  const registration = { pushManager: { getSubscription: vi.fn(async () => subscription), subscribe: vi.fn(), unsubscribe } }
  const register = vi.fn(async () => registration)
  vi.stubGlobal('navigator', { serviceWorker: { register, getRegistration: vi.fn(async () => registration), ready: Promise.resolve(registration) } })
  vi.stubGlobal('Notification', { requestPermission })
  vi.mocked(api).mockResolvedValue(new Response(JSON.stringify({ id: 'sub', desk_enabled: false }), { status: 200 }))
  return { registration, register, requestPermission, unsubscribe }
}

test('Desk off posts an explicit flag without prompting registering unsubscribing or touching phone preferences', async () => {
  const calls = subscriptionBrowser()
  await setDeskSubscription(false, '', undefined, new AbortController().signal)
  expect(calls.requestPermission).not.toHaveBeenCalled()
  expect(calls.register).not.toHaveBeenCalled()
  expect(calls.unsubscribe).not.toHaveBeenCalled()
  expect(calls.registration.pushManager.subscribe).not.toHaveBeenCalled()
  expect(api).toHaveBeenCalledTimes(1)
  const [path, options] = vi.mocked(api).mock.calls[0]
  expect(path).toBe('/me/phone-approvals/subscriptions')
  expect(JSON.parse(options!.body as string)).toEqual({ endpoint: 'https://push.example.test/device', keys: { auth: 'AQID', p256dh: 'BAUG' }, desk_enabled: false })
})

test('Denied permission and an identity abort between browser work and POST cannot persist a Desk subscription', async () => {
  const calls = subscriptionBrowser()
  await expect(setDeskSubscription(true, 'AQID', Promise.resolve('denied'), new AbortController().signal)).rejects.toThrow('desk-permission')
  expect(calls.register).not.toHaveBeenCalled()
  const controller = new AbortController()
  calls.registration.pushManager.getSubscription.mockImplementation(async () => { controller.abort(); return { toJSON: () => ({ endpoint: '', expirationTime: null, keys: { auth: '', p256dh: '' } }) } })
  await expect(setDeskSubscription(true, 'AQID', Promise.resolve('granted'), controller.signal)).rejects.toThrow()
  expect(api).not.toHaveBeenCalled()
})
