// SPDX-License-Identifier: AGPL-3.0-only
// AEON-434: the register page follows one computer. A reset, a sign-out or a
// different computer retires every outstanding read; one deadline bounds the
// polling; the live stream is followed in live mode, filtered and coalesced.
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import {
  MAX_REQUEST_POLL_MS, PAIRING_EVENT_COALESCE_MS, PairingError, pairingEventScope, subscribePairingEvents, type PairingView,
} from '../src/lib/agentPairing'
import { PairingFollow } from '../src/lib/pairingFollow'

const COMPUTER = '33333333-3333-4333-8333-333333333333'
const OTHER = '99999999-9999-4999-8999-999999999999'
const REQUEST = '22222222-2222-4222-8222-222222222222'
const ACCOUNT = '44444444-4444-4444-8444-444444444444'

function view(overrides: Record<string, unknown> = {}): PairingView {
  return {
    request_id: REQUEST, tenant_id: '11111111-1111-4111-8111-111111111111', tenant_name: 'INSPR', state: 'approved',
    request_digest: 'ab'.repeat(32), expires_at: '2099-01-01T00:00:00Z', computer_name: 'studio', platform: 'darwin', arch: 'arm64',
    workspace_path: '/work', capabilities: ['managed_runs'], requested_accounts: [], verification: null, verification_capabilities: {},
    computer_id: COMPUTER, computer_state: 'connected', principal_id: null, daemon_id: null, runtime_prefix: '',
    local_cleanup: 'pending', local_processes: 'unconfirmed', setup_state: 'approved', connectivity: 'unknown', interval_seconds: 5,
    enrollments: [{ account_id: ACCOUNT, account_key: 'cursor-1', harness: 'cursor', label: 'Cursor', model_profile_id: 'm', state: 'connected', local_cleanup: 'pending', verification_run_id: null, active_run_ids: [] }],
    ...overrides,
  } as unknown as PairingView
}
const settingUp = (computer = COMPUTER) => view({ computer_id: computer })

interface Deferred { resolve(value: PairingView): void; reject(error: unknown): void }
function harness(options: { generation?: () => number } = {}) {
  const reads: { computer: string; call: Deferred }[] = []
  const views: PairingView[] = []
  const failures: unknown[] = []
  const follow = new PairingFollow({
    read: computer => new Promise<PairingView>((resolve, reject) => { reads.push({ computer, call: { resolve, reject } }) }),
    generation: options.generation,
    onView: v => { views.push(v) },
    onFailure: e => { failures.push(e) },
  })
  return { follow, reads, views, failures }
}

beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-01T10:00:00Z')) })
afterEach(() => { vi.useRealTimers() })

it('a read that fails after a reset does not restart polling', async () => {
  const { follow, reads, failures } = harness()
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  expect(reads).toHaveLength(1)
  follow.stop() // "Use a different code", sign-out or unmount while that read is outstanding
  reads[0].call.reject(new PairingError(503, 'unavailable', { code: 'unavailable' }))
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS * 2)
  expect(reads).toHaveLength(1)
  expect(failures).toHaveLength(0)
  expect(follow.computerId).toBeNull()
})

it('a read that succeeds after a reset is dropped and arms nothing', async () => {
  const { follow, reads, views } = harness()
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  follow.stop()
  reads[0].call.resolve(settingUp())
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS)
  expect(views).toHaveLength(0)
  expect(reads).toHaveLength(1)
})

it('a read for a computer the page no longer follows does not retry it', async () => {
  const { follow, reads, views } = harness()
  follow.follow(settingUp(COMPUTER))
  await vi.advanceTimersByTimeAsync(5_000)
  expect(reads.map(r => r.computer)).toEqual([COMPUTER])
  follow.follow(settingUp(OTHER)) // another pairing replaces it while the first read is out
  reads[0].call.reject(new PairingError(503, 'unavailable', { code: 'unavailable' }))
  await vi.advanceTimersByTimeAsync(60_000)
  expect(reads.slice(1).length).toBeGreaterThan(0)
  expect(reads.slice(1).every(r => r.computer === OTHER)).toBe(true)
  expect(reads.filter(r => r.computer === COMPUTER)).toHaveLength(1)
  expect(views).toHaveLength(0)
})

it('reads discarded by a session reset do not answer', async () => {
  let generation = 1
  const { follow, reads, views, failures } = harness({ generation: () => generation })
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  generation += 1
  reads[0].call.resolve(settingUp())
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS)
  expect(views).toHaveLength(0)
  expect(failures).toHaveLength(0)
  expect(reads).toHaveLength(1)
})

it('retries stop at one fixed deadline and never restart it', async () => {
  const { follow, reads, failures } = harness()
  const began = Date.now()
  follow.follow(settingUp())
  let answered = 0
  // Every read fails; answer each one as it is made.
  for (let step = 0; step < 400 && !failures.length; step++) {
    await vi.advanceTimersByTimeAsync(5_000)
    while (answered < reads.length) reads[answered++].call.reject(new PairingError(503, 'unavailable', { code: 'unavailable' }))
    await vi.advanceTimersByTimeAsync(0)
  }
  expect(failures).toHaveLength(1)
  expect(Date.now() - began).toBeGreaterThanOrEqual(MAX_REQUEST_POLL_MS - 35_000)
  expect(Date.now() - began).toBeLessThanOrEqual(MAX_REQUEST_POLL_MS + 35_000)
  const made = reads.length
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS * 3)
  expect(reads).toHaveLength(made)
})

it('a finished or failed pairing arms no timer, a moving one keeps reading', async () => {
  const connectedSettled = view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online' })
  const setupFailed = view({ state: 'redeemed', setup_state: 'setup_failed', setup_error: 'installation_failed', connectivity: 'offline' })
  const verificationFailed = view({ state: 'redeemed', setup_state: 'connected', connectivity: 'offline', enrollments: [{ ...settingUp().enrollments[0], verification_state: 'failed' }] })
  const verificationCancelled = view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online', enrollments: [{ ...settingUp().enrollments[0], verification_state: 'cancelled' }] })
  for (const done of [connectedSettled, setupFailed, verificationFailed, verificationCancelled]) {
    const { follow, reads } = harness()
    follow.follow(done)
    await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS)
    expect(reads).toHaveLength(0)
  }
  const { follow, reads } = harness()
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  expect(reads).toHaveLength(1)
})

it('a live wake during a read waits for it and then reads once more', async () => {
  const { follow, reads, views } = harness()
  follow.follow(view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online' }))
  follow.poke()
  follow.poke()
  follow.poke()
  expect(reads).toHaveLength(1)
  reads[0].call.resolve(view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online' }))
  await vi.advanceTimersByTimeAsync(0)
  expect(views).toHaveLength(1)
  expect(reads).toHaveLength(2)
  reads[1].call.resolve(view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online' }))
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS)
  expect(reads).toHaveLength(2)
})

const enrollmentWith = (verification_state: string) => [{ ...settingUp().enrollments[0], verification_state }]
const terminalViews: [string, PairingView][] = [
  ['setup_failed', view({ state: 'redeemed', setup_state: 'setup_failed', setup_error: 'installation_failed', connectivity: 'offline' })],
  ['failed', view({ state: 'redeemed', setup_state: 'connected', connectivity: 'offline', enrollments: enrollmentWith('failed') })],
  ['cancelled', view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online', enrollments: enrollmentWith('cancelled') })],
  ['expired', view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online', enrollments: enrollmentWith('expired') })],
  ['ownership_lost', view({ state: 'redeemed', setup_state: 'connected', connectivity: 'online', enrollments: enrollmentWith('ownership_lost') })],
  ['request expired', view({ state: 'expired' })],
]

for (const [name, terminal] of terminalViews) {
  it(`on a ${name} view, a live wake and a failed read arm no timer and read no more`, async () => {
    for (const failure of [
      new PairingError(503, 'unavailable', { code: 'unavailable' }),
      new PairingError(429, 'slow down', { code: 'rate_limited', retryAfterSeconds: 10 }),
    ]) {
      const { follow, reads, failures } = harness()
      follow.follow(terminal)
      await vi.advanceTimersByTimeAsync(10_000) // well inside the polling lifetime
      expect(reads).toHaveLength(0)
      follow.poke() // the stream named this pairing
      expect(reads).toHaveLength(1)
      reads[0].call.reject(failure)
      await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS * 2)
      expect(reads).toHaveLength(1)
      expect(vi.getTimerCount()).toBe(0)
      // A rate limit is not a failure; a refused retry of an ordinary failure is reported once.
      expect(failures).toHaveLength(failure.code === 'rate_limited' ? 0 : 1)
    }
  })
}

it('a terminal view read on a live wake that comes back moving resumes polling', async () => {
  const { follow, reads, views } = harness()
  follow.follow(terminalViews[0][1])
  follow.poke()
  reads[0].call.resolve(settingUp())
  await vi.advanceTimersByTimeAsync(0)
  expect(views).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(5_000)
  expect(reads).toHaveLength(2)
})

it('a wake that finds the pairing finished stops polling, and a failure after it does not restart it', async () => {
  const { follow, reads } = harness()
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  reads[0].call.resolve(terminalViews[0][1])
  await vi.advanceTimersByTimeAsync(10_000)
  expect(reads).toHaveLength(1)
  follow.poke()
  reads[1].call.reject(new PairingError(503, 'unavailable', { code: 'unavailable' }))
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS)
  expect(reads).toHaveLength(2)
  expect(vi.getTimerCount()).toBe(0)
})

it('a tab that wakes after the lifetime ran out reads nothing', async () => {
  const { follow, reads, failures } = harness()
  follow.follow(settingUp())
  vi.setSystemTime(Date.now() + 20 * 60 * 1000) // the tab slept; the timer fires late
  await vi.advanceTimersByTimeAsync(5_000)
  expect(reads).toHaveLength(0)
  expect(failures).toHaveLength(0)
  expect(vi.getTimerCount()).toBe(0)
})

it('a Retry-After beyond the lifetime ends the follow instead of reading later', async () => {
  const { follow, reads, failures } = harness()
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  expect(reads).toHaveLength(1)
  reads[0].call.reject(new PairingError(429, 'slow down', { code: 'rate_limited', retryAfterSeconds: 3600 }))
  await vi.advanceTimersByTimeAsync(0)
  expect(vi.getTimerCount()).toBe(0)
  await vi.advanceTimersByTimeAsync(3600 * 1000 * 2)
  expect(reads).toHaveLength(1)
  expect(failures).toHaveLength(0)
})

it('a Retry-After inside the lifetime still waits, and one that reaches the deadline does not', async () => {
  const { follow, reads } = harness()
  follow.follow(settingUp())
  await vi.advanceTimersByTimeAsync(5_000)
  reads[0].call.reject(new PairingError(429, 'slow down', { code: 'rate_limited', retryAfterSeconds: 120 }))
  await vi.advanceTimersByTimeAsync(119_000)
  expect(reads).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(1_000)
  expect(reads).toHaveLength(2)
  // 125 s in; a wait that would land at or past the 600 s deadline never arms.
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS - 130_000)
  const before = reads.length
  reads[before - 1].call.reject(new PairingError(429, 'slow down', { code: 'rate_limited', retryAfterSeconds: 600 }))
  await vi.advanceTimersByTimeAsync(MAX_REQUEST_POLL_MS * 2)
  expect(reads).toHaveLength(before)
})

it('only schedule() arms a timer and only run() reads, so a new path cannot skip the checks', () => {
  const source = readFileSync(new URL('../src/lib/pairingFollow.ts', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
  expect(source.match(/setTimeout\(/g)).toHaveLength(1)
  expect(source.match(/this\.read\(/g)).toHaveLength(1)
  expect(source.match(/pairingStillLive\(/g)!.length).toBeGreaterThanOrEqual(2)
  const scheduleBody = source.slice(source.indexOf('private schedule('), source.indexOf('private async run('))
  expect(scheduleBody).toContain('setTimeout(')
  expect(scheduleBody).toContain('pairingStillLive(last)')
  // The lifetime is checked when arming and again when the timer fires.
  expect(scheduleBody.match(/MAX_REQUEST_POLL_MS/g)!.length).toBeGreaterThanOrEqual(2)
})

class FakeStream {
  listeners = new Map<string, ((event: MessageEvent) => void)[]>()
  closed = false
  onerror: (() => void) | null = null
  addEventListener(type: string, listener: (event: MessageEvent) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
  close() { this.closed = true }
  emit(type: string, data: unknown) { for (const listener of this.listeners.get(type) ?? []) listener({ data: JSON.stringify(data) } as MessageEvent) }
}
function subscribed(scope = pairingEventScope(view())) {
  const stream = new FakeStream()
  const urls: string[] = []
  const wake = vi.fn()
  const stop = subscribePairingEvents(() => scope, wake, url => { urls.push(url); return stream as never })
  return { stream, urls, wake, stop }
}

it('opens the stream in live mode, so opening the page never replays history', () => {
  const { urls } = subscribed()
  expect(urls).toEqual(['/api/events/stream?after=latest'])
})

it('events of other pairings do not wake the page; a burst of its own wakes it once', async () => {
  const { stream, wake } = subscribed()
  stream.emit('agent_pairing.reported', { id: 1, type: 'agent_pairing.reported', after: { computer_id: OTHER, setup_state: 'connected' } })
  stream.emit('agent_pairing.approved', { id: 2, type: 'agent_pairing.approved', after: { request_id: OTHER } })
  stream.emit('agent_pairing.reported', { id: 3, type: 'agent_pairing.reported', after: null })
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS * 4)
  expect(wake).not.toHaveBeenCalled()
  for (let id = 10; id < 60; id++) stream.emit('agent_pairing.reported', { id, type: 'agent_pairing.reported', after: { computer_id: COMPUTER, setup_state: 'connected' } })
  expect(wake).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS)
  expect(wake).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS * 4)
  expect(wake).toHaveBeenCalledTimes(1)
})

it('wakes once after a gap the stream could not bridge, never on the first connection', async () => {
  const { stream, wake } = subscribed()
  stream.emit('stream.ready', { after: 40, resumed: false })
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS * 2)
  expect(wake).not.toHaveBeenCalled()
  stream.emit('stream.ready', { after: 45, resumed: true })
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS * 2)
  expect(wake).not.toHaveBeenCalled()
  stream.emit('stream.ready', { after: 900, resumed: false })
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS * 2)
  expect(wake).toHaveBeenCalledTimes(1)
})

it('closing the stream cancels a wake that is still pending', async () => {
  const { stream, wake, stop } = subscribed()
  stream.emit('agent_pairing.reported', { id: 5, type: 'agent_pairing.reported', after: { computer_id: COMPUTER } })
  stop()
  await vi.advanceTimersByTimeAsync(PAIRING_EVENT_COALESCE_MS * 4)
  expect(stream.closed).toBe(true)
  expect(wake).not.toHaveBeenCalled()
})
