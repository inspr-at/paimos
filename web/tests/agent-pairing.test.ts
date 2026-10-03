// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { sessionEnded } from '../src/lib/api.ts'
import {
  DEFAULT_REVIEW_CHOICE, LOOKUP_DEBOUNCE_MS, MIN_POLL_INTERVAL_MS, PUBLIC_PAIRING_GUIDE_PATH, PairingError,
  agentRouteKind, canonicalUserCode, clearPairingClientState, denyPairing,
  describeComputerStatus, describeEnrollmentDiagnostic, describeEnrollmentStatus, describeHarnessStatus, describeHarnessFix, describeHarnessHint, describeProgress, harnessRecovery, disconnectComputer, disconnectConfirm, disconnectEnrollment,
  formatVerification, getPairingComputer, getPairingGuide, isPublicPairingGuide, listPairingComputers, lookupPairing,
  pairingEventMatches, pairingEventScope, pairingLiveCopy, pairingPermissions, pairingStillLive, planApproval, planLookup, planPoll,
  registerAgentUrl, sessionFreezeApplies, submitApproval, verificationWarning, defaultSelectedAccountKeys,
  peekPairingCode, presentPublicGuide, publicGuideSections, rememberPairingCode, takePairingCode,
  chooseInstallMethod, agentUpdateAdvice, installMethods, readPairingInstallMethod, writePairingInstallMethod,
  isAddHarness, pairingScopeKey, setHarnessAccount,
  addHarnessTargetProblem, applyComputerListRefresh, discardPairingReads, formatAllowanceMoment,
  pairingReadGeneration, unsupportedVerification, verifiableAccountKeys, connectDisabledReason, approvePairedAccounts,
  type PairingGuide, type PairingView, type RequestedAccount,
} from '../src/lib/agentPairing.ts'

const originalFetch = globalThis.fetch
afterEach(() => {
  globalThis.fetch = originalFetch
  sessionEnded.blocked = false
  clearPairingClientState()
})

const REQUEST = '11111111-1111-4111-8111-111111111111'
const TENANT = '22222222-2222-4222-8222-222222222222'
const COMPUTER = '33333333-3333-4333-8333-333333333333'
const ACCOUNT = '44444444-4444-4444-8444-444444444444'
const ACCOUNT_2 = '55555555-5555-4555-8555-555555555555'
const MODEL = '66666666-6666-4666-8666-666666666666'
const RUN = '77777777-7777-4777-8777-777777777777'
const DIGEST = 'ab'.repeat(32)
const SECRET = 'device-secret-must-not-appear'

const person = pairingPermissions({ permissions: ['account.manage', 'account.read'], principalKind: 'person' })
const reader = pairingPermissions({ permissions: ['account.read'], principalKind: 'person' })
const agent = pairingPermissions({ permissions: ['account.manage', 'run.claim'], principalKind: 'agent' })

function account(overrides: Partial<RequestedAccount> = {}): RequestedAccount {
  return { account_key: 'cursor-1', harness: 'cursor', label: 'Cursor work', model_profile_id: MODEL, ...overrides }
}

function view(overrides: Record<string, unknown> = {}): PairingView {
  return {
    request_id: REQUEST,
    tenant_id: TENANT,
    tenant_name: 'INSPR',
    state: 'pending',
    request_digest: DIGEST,
    expires_at: '2099-01-01T00:00:00Z',
    computer_name: 'studio',
    platform: 'darwin',
    arch: 'arm64',
    workspace_path: '/Users/markus/work',
    capabilities: ['managed_runs'],
    requested_accounts: [account()],
    verification: {
      mode: null,
      policy: 'read_only',
      runs_per_account: 1,
      max_parallel_runs: 1,
      max_duration_seconds: 60,
      allowance: 1,
      unit: 'requests',
      expires_at: '2026-09-27T20:30:00Z',
      task: 'Reply exactly AEON_VERIFIED.',
    },
    verification_capabilities: {
      cursor: { supported: true, policy: 'no_tools', reason: '' },
      codex: { supported: true, policy: 'no_tools', reason: '' },
      claude: { supported: true, policy: 'no_tools', reason: '' },
      grok: { supported: true, policy: 'no_tools', reason: '' },
    },
    computer_id: null,
    computer_state: null,
    principal_id: null,
    daemon_id: null,
    runtime_prefix: 'k1',
    local_cleanup: 'pending',
    local_processes: 'unconfirmed',
    enrollments: [],
    ...overrides,
  } as PairingView
}

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })
}

function guidePayload(overrides: Record<string, unknown> = {}): PairingGuide {
  return {
    instance_url: 'https://aeon.example/ignored',
    default_tenant_slug: 'inspr',
    protocol: 'pairing-v1',
    platforms: ['darwin/arm64', 'linux/amd64'],
    version: '260927181849.0.0',
    platform_qualification: 'candidate; consult the exact release service qualification evidence',
    setup_command: "aeon-agentd pair --url 'https://aeon.example'",
    install_available: false,
    install_targets: [],
    ...overrides,
  }
}

test('the Nix guide is additive, parsed as published, and never guesses a paired service option', async () => {
  const managed = {
    command: "aeon-agentd pair --url 'https://other.example'",
    service_option: 'uzumaki.aeon.agentd.enable',
    module_url: 'https://github.com/markus-barta/nixcfg/blob/main/modules/uzumaki/aeon-agentd.nix',
    service_note: 'NIX-583 still needs paired-mode support.',
  }
  globalThis.fetch = async () => jsonResponse(guidePayload({ managed_setup: managed }))
  const guide = await getPairingGuide()
  assert.deepEqual(presentPublicGuide(guide).managedSetup, managed)
  assert.match(presentPublicGuide(guide).note, /signed-in person/)
  assert.equal(presentPublicGuide(guidePayload()).managedSetup, null)
  const annotated = {
    ...managed,
    platform_note: 'Module supports macOS only; it does not configure a Linux service.',
    prerequisite_note: 'Use a reviewed release pin with pair and aeon-agentd on PATH.',
  }
  globalThis.fetch = async () => jsonResponse(guidePayload({ managed_setup: annotated }))
  assert.deepEqual(presentPublicGuide(await getPairingGuide()).managedSetup, annotated)
  for (const field of ['platform_note', 'prerequisite_note']) {
    globalThis.fetch = async () => jsonResponse(guidePayload({ managed_setup: { ...annotated, [field]: ['invalid'] } }))
    await assert.rejects(getPairingGuide(), PairingError)
  }
  globalThis.fetch = async () => jsonResponse(guidePayload({ managed_setup: { ...managed, module_url: 'javascript:alert(1)' } }))
  await assert.rejects(getPairingGuide(), PairingError)
})

test('the public guide is not a session route and stays usable after sign-out', () => {
  assert.equal(agentRouteKind('/agents/register-agent'), 'register-agent')
  assert.equal(agentRouteKind('/agents/usage'), 'usage')
  assert.notEqual(agentRouteKind(PUBLIC_PAIRING_GUIDE_PATH), 'session')
  assert.equal(isPublicPairingGuide('/agents/register-agent'), true)
  assert.equal(sessionFreezeApplies('/agents/register-agent', true), false)
  assert.equal(sessionFreezeApplies('/agents', true), true)
  assert.equal(sessionFreezeApplies('/signin', true), false)
})

test('the guide uses the server origin and stays available when the session is frozen', async () => {
  sessionEnded.blocked = true
  const urls: string[] = []
  globalThis.fetch = async url => {
    urls.push(String(url))
    return jsonResponse({ ...guidePayload(), device_secret: SECRET })
  }
  await assert.rejects(getPairingGuide(), (error: PairingError) => error instanceof PairingError && !error.message.includes(SECRET) && !String(error.next).includes(SECRET))
  assert.deepEqual(urls, ['/api/agent-pairing/guide'])
  assert.equal(sessionEnded.blocked, true)

  globalThis.fetch = async () => jsonResponse(guidePayload({ platforms: ['darwin/arm64'] }))
  const guide = await getPairingGuide()
  assert.equal(registerAgentUrl(guide), 'https://aeon.example/agents/register-agent')
  assert.equal(JSON.stringify(guide).includes('aeon.barta.cm'), false)
  assert.equal(guide.version, '260927181849.0.0')
})

test('pi provider binding survives lookup without widening verification', async () => {
  const requested = account({ harness: 'pi', provider: 'anthropic', label: 'pi / anthropic (local profile)' })
  globalThis.fetch = async () => jsonResponse(view({ requested_accounts: [requested], verification_capabilities: {
    pi: { supported: false, policy: 'unavailable', reason: 'No qualified no-tools policy.' },
  } }))
  const found = await lookupPairing('123456789')
  assert.equal(found.requested_accounts[0]?.provider, 'anthropic')
  assert.equal(unsupportedVerification(found, [requested.account_key])[0]?.harness, 'pi')
})

test('lookup sends only the code and does not approve', async () => {
  assert.equal(canonicalUserCode('123 456 789'), '123-456-789')
  assert.equal(canonicalUserCode('123456789'), '123-456-789')
  assert.equal(canonicalUserCode('12-345-678'), null)
  assert.equal(canonicalUserCode('code 123-456-789'), null)
  const calls: { url: string; body: unknown }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
    assert.equal(init?.credentials, 'same-origin')
    return jsonResponse(view())
  }
  const found = await lookupPairing('123456789')
  assert.equal(found.computer_name, 'studio')
  assert.deepEqual(calls, [{ url: '/api/agent-pairing/lookup', body: { user_code: '123-456-789' } }])
  await assert.rejects(lookupPairing('nope'), (error: PairingError) => error instanceof PairingError && error.next.includes('does not grant'))
  assert.equal(calls.length, 1)
})

test('a frozen session cannot look up a code, and the public guide is the exception', async () => {
  const urls: string[] = []
  globalThis.fetch = async url => { urls.push(String(url)); return jsonResponse(view()) }
  sessionEnded.blocked = true
  await assert.rejects(lookupPairing('123-456-789'), (error: PairingError) => error instanceof PairingError && error.status === 401 && error.next.includes('does not grant'))
  assert.deepEqual(urls, [])
})

test('lookup waits out a 429 and does not send another request', async () => {
  let calls = 0
  globalThis.fetch = async () => {
    calls += 1
    return jsonResponse({ error: 'Slow down', code: 'rate_limited' }, 429, { 'Retry-After': '30' })
  }
  await assert.rejects(lookupPairing('123-456-789'), (error: PairingError) => {
    assert.ok(error instanceof PairingError)
    assert.equal(error.code, 'rate_limited')
    assert.equal(error.retryAfterSeconds, 30)
    assert.match(error.next, /Wait 30 seconds/)
    assert.match(error.next, /will not start a new pairing/)
    return true
  })
  assert.equal(calls, 1)
  const blocked = planLookup({ raw: '123-456-789', explicit: true, now: 1_000, typedAt: 0, inFlight: false, retryAfterUntil: 31_000 })
  assert.equal(blocked.action, 'blocked')
})

test('debounced lookup does not repeat itself or fire while a character is still being typed', () => {
  const waiting = planLookup({ raw: '123', explicit: false, now: 1_000, typedAt: 1_000, inFlight: false })
  assert.equal(waiting.action, 'wait')
  if (waiting.action === 'wait') assert.equal(waiting.delayMs, LOOKUP_DEBOUNCE_MS)
  const send = planLookup({ raw: '123-456-789', explicit: false, now: 1_000 + LOOKUP_DEBOUNCE_MS, typedAt: 1_000, inFlight: false })
  assert.deepEqual(send, { action: 'send', code: '123-456-789' })
  const repeat = planLookup({ raw: '123-456-789', explicit: false, now: 9_000, typedAt: 1_000, inFlight: false, lastSentCode: '123-456-789' })
  assert.equal(repeat.action, 'idle')
  const again = planLookup({ raw: '123-456-789', explicit: true, now: 9_000, typedAt: 1_000, inFlight: false, lastSentCode: '123-456-789' })
  assert.deepEqual(again, { action: 'send', code: '123-456-789' })
  const busy = planLookup({ raw: '123-456-789', explicit: true, now: 9_000, typedAt: 1_000, inFlight: true })
  assert.equal(busy.action, 'wait')
})

test('verification stays on the server terms and is selected by default', () => {
  assert.equal(DEFAULT_REVIEW_CHOICE, 'one_per_harness')
  const now = Date.parse('2026-09-27T20:00:00Z')
  const terms = view().verification!
  assert.equal(verificationWarning(terms), null)
  const text = formatVerification(terms, now)
  assert.match(text, /1 request per selected harness/)
  assert.match(text, /1 run at a time on that harness/)
  assert.match(text, /60 seconds/)
  assert.doesNotMatch(text, /1 requests/)
  assert.doesNotMatch(text, /One at a time/)
  assert.equal(text.includes('2026-09-27T20:30:00'), false)
  assert.match(text, new RegExp(formatAllowanceMoment(terms.expires_at, now).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  assert.match(text, /Read-only/)
  assert.match(text, /No repository changes or privileged actions/)
  assert.match(text, /not the vendor subscription/)
  const mocked = { ...terms, max_duration_seconds: 900, expires_at: '2026-09-27T19:15:00Z' }
  assert.match(formatVerification(mocked, now), /900 seconds/)
  assert.doesNotMatch(formatVerification(mocked, now), /60 seconds/)
  assert.ok(verificationWarning(mocked))
})

test('one account is preselected per harness, and a second candidate stays unselected', () => {
  const accounts = [account(), account({ account_key: 'cursor-2', label: 'Cursor personal' }), account({ account_key: 'codex-1', harness: 'codex', label: 'Codex work' })]
  assert.deepEqual(defaultSelectedAccountKeys(accounts), ['codex-1'])
  const missing = planApproval({ view: view({ requested_accounts: accounts }), choice: 'one_per_harness', selectedAccountKeys: ['codex-1', 'cursor-1', 'cursor-2'], permissions: person })
  assert.equal(missing.ok, false)
  const chosen = planApproval({ view: view({ requested_accounts: accounts }), choice: 'one_per_harness', selectedAccountKeys: ['cursor-2', 'codex-1'], permissions: person })
  assert.equal(chosen.ok, true)
  if (chosen.ok) assert.deepEqual(chosen.body.selected_account_keys, ['cursor-2', 'codex-1'])
})

test('approval sends the reviewed digest and choice, never a secret or an allowance', async () => {
  const bodies: unknown[] = []
  globalThis.fetch = async (_url, init) => {
    bodies.push(JSON.parse(String(init?.body)))
    return jsonResponse(view({ state: 'approved', verification: { ...view().verification, mode: 'one_per_harness' } }))
  }
  await submitApproval({ view: view(), choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person })
  assert.deepEqual(bodies, [{
    request_digest: DIGEST,
    verification: 'one_per_harness',
    selected_account_keys: ['cursor-1'],
  }])
  assert.equal(JSON.stringify(bodies).includes('device_secret'), false)
  assert.equal(JSON.stringify(bodies).includes('allowance'), false)
})

test('a second click joins the same approval, and a different choice waits', async () => {
  let calls = 0
  let release: (response: Response) => void = () => {}
  globalThis.fetch = () => {
    calls += 1
    return new Promise(resolve => { release = resolve })
  }
  const current = view()
  const first = submitApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person })
  const second = submitApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person })
  await assert.rejects(
    submitApproval({ view: current, choice: 'connect_only', selectedAccountKeys: ['cursor-1'], permissions: person }),
    (error: PairingError) => error instanceof PairingError && error.code === 'busy',
  )
  assert.equal(calls, 1)
  release(jsonResponse(view({ state: 'approved', verification: { ...view().verification, mode: 'one_per_harness' } })))
  const [a, b] = await Promise.all([first, second])
  assert.equal(a.state, 'approved')
  assert.equal(b.state, 'approved')
})

test('expiry, denial and a changed approval do not renew authority', async () => {
  let calls = 0
  globalThis.fetch = async () => { calls += 1; return jsonResponse(view()) }
  const expired = view({ state: 'expired' })
  await assert.rejects(submitApproval({ view: expired, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person }), (error: PairingError) => error.next.includes('will not renew'))
  const stale = view({ expires_at: '2020-01-01T00:00:00Z' })
  await assert.rejects(submitApproval({ view: stale, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person, now: Date.parse('2026-09-27T20:00:00Z') }))
  const denied = view({ state: 'denied' })
  await assert.rejects(submitApproval({ view: denied, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person }), (error: PairingError) => error.next.includes('will not reverse'))
  const approved = view({ state: 'approved', verification: { ...view().verification!, mode: 'connect_only' } })
  await assert.rejects(submitApproval({ view: approved, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person }), (error: PairingError) => error.message.includes('different choice'))
  assert.equal(calls, 0)
  assert.equal(describeProgress(expired).renewsAuthority, false)
  assert.equal(describeProgress(denied).renewsAuthority, false)
})

test('a conflict is not retried and a revision is not sent as an unknown field', async () => {
  let calls = 0
  const bodies: unknown[] = []
  globalThis.fetch = async (_url, init) => {
    calls += 1
    bodies.push(JSON.parse(String(init?.body)))
    return jsonResponse({ error: 'Revision changed', code: 'conflict' }, 409)
  }
  const current = view({ revision: 4 })
  await assert.rejects(submitApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person }), (error: PairingError) => {
    assert.equal(error.code, 'conflict')
    assert.match(error.next, /not retried/)
    return true
  })
  assert.equal(calls, 1)
  assert.deepEqual(Object.keys(bodies[0] as object).sort(), ['request_digest', 'selected_account_keys', 'verification'])
})

test('an agent cannot approve, deny or disconnect', async () => {
  let calls = 0
  globalThis.fetch = async () => { calls += 1; return jsonResponse(view()) }
  assert.equal(agent.canApprove, false)
  assert.equal(agent.canForceStop, false)
  assert.equal(reader.canApprove, false)
  assert.equal(reader.canListComputers, true)
  await assert.rejects(submitApproval({ view: view(), choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: agent }))
  await assert.rejects(denyPairing(view(), agent))
  await assert.rejects(disconnectComputer(view({ computer_id: COMPUTER, computer_state: 'connected' }), 'drain', agent))
  assert.equal(calls, 0)
})

test('disconnect defaults to finishing runs, and revoke is a separate confirmation', async () => {
  const connected = view({
    computer_id: COMPUTER, computer_state: 'connected', state: 'redeemed', revision: 7,
    enrollments: [enrollment(), enrollment({ account_id: ACCOUNT_2, account_key: 'codex-1', harness: 'codex', label: 'Codex work' })],
  })
  const finish = disconnectConfirm({ scope: 'computer', computerName: 'studio', mode: 'drain', activeRunCount: 2, otherConnectedCount: 0 })
  assert.equal(finish.confirmLabel, 'Finish runs and disconnect')
  assert.equal(finish.danger, false)
  assert.match(finish.points.join(' '), /Vendor sign-in/)
  const revoke = disconnectConfirm({ scope: 'enrollment', computerName: 'studio', mode: 'revoke_now', activeRunCount: 1, otherConnectedCount: 1, enrollment: { harness: 'cursor', label: 'Cursor work' } })
  assert.equal(revoke.confirmLabel, 'Revoke access now')
  assert.match(revoke.points.join(' '), /unconfirmed/)
  assert.match(revoke.points.join(' '), /Other harnesses/)
  const bodies: unknown[] = []
  globalThis.fetch = async (_url, init) => {
    bodies.push(JSON.parse(String(init?.body)))
    return jsonResponse({ ...connected, computer_state: 'draining' })
  }
  await disconnectComputer(connected, 'drain', person)
  await disconnectEnrollment(connected, ACCOUNT, 'revoke_now', person)
  assert.deepEqual(bodies, [
    { mode: 'drain', expected_revision: 7 },
    { mode: 'revoke_now', expected_revision: 7 },
  ])
  clearPairingClientState()
  let extra = 0
  globalThis.fetch = async () => { extra += 1; return jsonResponse(connected) }
  await assert.rejects(disconnectComputer(view({ computer_id: COMPUTER, computer_state: 'connected', revision: undefined }), 'drain', person), (error: PairingError) => error.code === 'stale_revision')
  assert.equal(extra, 0)
})

test('revocation does not claim that an offline process has stopped', () => {
  const revoked = view({ computer_state: 'revoked', state: 'revoked', local_cleanup: 'pending', local_processes: 'unconfirmed', enrollments: [] })
  const status = describeComputerStatus(revoked, { httpStatus: 401, heartbeatMissing: true })
  assert.equal(status.claimsProcessStopped, false)
  assert.match(status.detail, /unconfirmed/)
  assert.match(status.detail, /does not show that local work has stopped/)
  assert.match(status.next, /unconfirmed/)
  const drained = describeComputerStatus(view({ computer_state: 'revoked', local_cleanup: 'confirmed', local_processes: 'drained' }))
  assert.equal(drained.claimsProcessStopped, true)
  assert.match(drained.detail, /Local cleanup confirmed/)
  const pendingCleanup = describeComputerStatus(view({ computer_state: 'revoked', local_cleanup: 'pending', local_processes: 'drained' }))
  assert.match(pendingCleanup.detail, /Local cleanup pending/)
  assert.equal(pendingCleanup.claimsProcessStopped, true)
  assert.equal(describeProgress(revoked).renewsAuthority, false)
})

test('polling honours the minimum interval, Retry-After and terminal states', () => {
  const started = 0
  assert.deepEqual(planPoll({ startedAt: started, now: 1_000, intervalSeconds: 1, state: 'pending' }), { action: 'wait', delayMs: MIN_POLL_INTERVAL_MS, reason: 'interval' })
  assert.deepEqual(planPoll({ startedAt: started, now: 1_000, rateLimited: true, retryAfterSeconds: 30, state: 'pending' }), { action: 'wait', delayMs: 30_000, reason: 'retry_after' })
  assert.equal(planPoll({ startedAt: started, now: 1_000, state: 'expired' }).action, 'stop')
  assert.equal(planPoll({ startedAt: started, now: 1_000, state: 'denied' }).reason, 'terminal')
  assert.equal(planPoll({ startedAt: started, now: 10 * 60 * 1000, state: 'pending' }).reason, 'lifetime')
  assert.equal(planPoll({ startedAt: 0, now: 1_000, intervalSeconds: 5, state: 'approved', failures: 1 }).reason, 'backoff')
  assert.equal(planPoll({ startedAt: 0, now: 1_000, intervalSeconds: 5, state: 'approved', failures: 1 }).delayMs, 10_000)
  assert.equal(planPoll({ startedAt: 0, now: 1_000, intervalSeconds: 5, state: 'approved', failures: 6 }).delayMs, 30_000)
})

test('a daemon connect reads as Connected while verification is still running', () => {
  const running = view({
    state: 'redeemed', computer_id: COMPUTER, computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    verification: { ...view().verification!, mode: 'one_per_harness' },
    enrollments: [enrollment({ verification_state: 'running' })],
    harness_statuses: { cursor: 'ready' },
  })
  assert.equal(describeProgress(running).phase, 'verify')
  assert.equal(pairingLiveCopy(running).title, 'Connected')
  assert.match(pairingLiveCopy(running).detail, /read-only run/)
  assert.match(pairingLiveCopy(running).next, /will not start another verification/)
  assert.equal(describeHarnessStatus(running, 'cursor'), 'Ready')
  assert.equal(pairingStillLive(running), true)
  const settled = view({
    state: 'redeemed', computer_id: COMPUTER, computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    verification: { ...view().verification!, mode: 'connect_only' },
    enrollments: [enrollment({ verification_state: 'not_selected' })],
  })
  assert.equal(pairingLiveCopy(settled).title, 'Connected')
  assert.match(pairingLiveCopy(settled).next, /Add another harness/)
  assert.equal(pairingStillLive(settled), false)
  assert.equal(pairingStillLive(view()), false)
  const waiting = view({ state: 'approved', computer_id: COMPUTER, computer_state: 'connected', setup_state: 'approved', connectivity: 'unknown' })
  assert.equal(pairingLiveCopy(waiting).title, 'Setting up')
  assert.equal(pairingStillLive(waiting), true)
})

test('a failed setup and a failed or cancelled verification stop the read whatever the connectivity says', () => {
  const base = { state: 'redeemed', computer_id: COMPUTER, computer_state: 'connected', setup_state: 'connected' } as const
  for (const connectivity of ['online', 'offline', 'unknown'] as const) {
    assert.equal(pairingStillLive(view({ ...base, setup_state: 'setup_failed', setup_error: 'installation_failed', connectivity })), false, `setup_failed ${connectivity}`)
    for (const verification_state of ['failed', 'cancelled', 'expired', 'ownership_lost']) {
      const final = view({ ...base, connectivity, enrollments: [enrollment({ verification_state })] })
      assert.equal(pairingStillLive(final), false, `${verification_state} ${connectivity}`)
    }
  }
  // Still moving, or not yet reported: keep reading.
  assert.equal(pairingStillLive(view({ ...base, connectivity: 'offline', enrollments: [enrollment({ verification_state: 'running' })] })), true)
  assert.equal(pairingStillLive(view({ ...base, setup_state: 'provisioning', connectivity: 'unknown', enrollments: [enrollment({ verification_state: 'queued' })] })), true)
  // One failed harness ends the read even while another still runs.
  assert.equal(pairingStillLive(view({ ...base, connectivity: 'online', enrollments: [enrollment({ verification_state: 'failed' }), enrollment({ account_id: ACCOUNT_2, verification_state: 'running' })] })), false)
})

test('a stream event belongs to the page only when it names the followed pairing', () => {
  const followed = view({ state: 'redeemed', computer_id: COMPUTER, enrollments: [enrollment()] })
  const scope = pairingEventScope(followed)
  const event = (after: unknown) => JSON.stringify({ id: 9, type: 'agent_pairing.reported', after })
  assert.equal(pairingEventMatches(event({ computer_id: COMPUTER, setup_state: 'connected' }), scope), true)
  assert.equal(pairingEventMatches(event({ request_id: REQUEST }), scope), true)
  assert.equal(pairingEventMatches(event({ account_id: ACCOUNT }), scope), true)
  assert.equal(pairingEventMatches(event({ account_ids: ['99999999-9999-4999-8999-999999999999', ACCOUNT] }), scope), true)
  assert.equal(pairingEventMatches(event({ computer_id: '99999999-9999-4999-8999-999999999999' }), scope), false)
  assert.equal(pairingEventMatches(event({ request_id: '99999999-9999-4999-8999-999999999999' }), scope), false)
  assert.equal(pairingEventMatches(event({ account_id: ACCOUNT_2 }), scope), false)
  assert.equal(pairingEventMatches(event(null), scope), false)
  assert.equal(pairingEventMatches('not json', scope), false)
  assert.equal(pairingEventMatches(event({ computer_id: COMPUTER }), pairingEventScope(null)), false)
})

test('a response secret is refused and computer lists stay on the person projection', async () => {
  globalThis.fetch = async () => jsonResponse({ ...view(), device_secret: SECRET, runtime_token: 'aeon_k1_' + SECRET })
  await assert.rejects(lookupPairing('123-456-789'), (error: PairingError) => error instanceof PairingError && !`${error.message} ${error.next}`.includes(SECRET))
  globalThis.fetch = async (url, init) => {
    assert.equal(String(url), '/api/agent-pairing/computers')
    assert.equal(init?.method ?? 'GET', 'GET')
    return jsonResponse({ computers: [view({ computer_id: COMPUTER, computer_state: 'connected', state: 'redeemed' })] })
  }
  const computers = await listPairingComputers()
  assert.equal(computers.length, 1)
  assert.equal(JSON.stringify(computers).includes(SECRET), false)
  assert.equal(computers[0]?.runtime_prefix, 'k1')
})

test('deny is person-only and does not call the server once the request is already denied', async () => {
  let calls = 0
  globalThis.fetch = async (url, init) => {
    calls += 1
    assert.equal(String(url), `/api/agent-pairing/requests/${REQUEST}/deny`)
    assert.equal(init?.method, 'POST')
    return jsonResponse(view({ state: 'denied' }))
  }
  await denyPairing(view(), person)
  assert.equal(calls, 1)
  clearPairingClientState()
  await denyPairing(view({ state: 'denied' }), person)
  assert.equal(calls, 1)
})

test('approval alone is not a connected computer', () => {
  const approved = view({ state: 'redeemed', computer_state: 'connected', enrollments: [enrollment()] })
  const progress = describeProgress(approved)
  assert.equal(progress.phase, 'setup')
  assert.match(progress.detail, /does not show the daemon is connected/)
  assert.equal(progress.renewsAuthority, false)
  const reported = view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    verification: { ...view().verification!, mode: 'connect_only' },
  })
  assert.equal(describeProgress(reported).phase, 'connected')
  const unverified = view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    verification: { ...view().verification!, mode: 'one_per_harness' },
    enrollments: [enrollment()],
  })
  assert.notEqual(describeProgress(unverified).phase, 'connected')
  const failed = describeProgress(view({
    setup_state: 'connected', connectivity: 'online', state: 'redeemed', computer_state: 'connected',
    enrollments: [enrollment({ verification_state: 'failed', verification_error: 'timed out' })],
  }))
  assert.equal(failed.phase, 'verify')
  assert.match(failed.detail, /timed out/)
  assert.match(failed.next, /not refilled/)
  assert.equal(describeComputerStatus(approved).stateLabel, 'Setup unconfirmed')
})

test('the public guide uses the server address and stores only a human code', () => {
  const published = guidePayload({ platforms: ['darwin/arm64'], instance_url: 'https://aeon.example' })
  const presented = presentPublicGuide(published)
  const lead = [...presented.steps, presented.address, presented.note].join('\n')
  assert.match(presented.address, /https:\/\/aeon\.example\/agents\/register-agent/)
  assert.equal(presented.steps.some(step => step.includes('Open this address')), false)
  assert.match(publicGuideSections(published)[0]!.paragraphs.join('\n'), /Share this address: https:\/\/aeon\.example\/agents\/register-agent/)
  assert.match(presented.setupCommand, /pair --url 'https:\/\/aeon\.example'/)
  assert.match(presented.installNote, /not published a verified installer/)
  assert.equal(lead.includes(presented.setupCommand), false)
  assert.equal(lead.includes('aeon.barta.cm'), false)
  assert.equal(lead.includes('curl'), false)
  assert.equal(JSON.stringify(presented).includes('inspr-at/paimos/releases'), false)
  assert.match(presented.note, /does not grant access/)
  assert.match(presented.manualParagraphs.join('\n'), /darwin\/arm64/)
  const four = ['darwin/arm64', 'darwin/amd64', 'linux/arm64', 'linux/amd64'].map(item => {
    const [platform, arch] = item.split('/')
    return {
      platform: platform!, arch: arch!, service: platform === 'darwin' ? 'launchd-user' as const : 'systemd-user' as const,
      qualification: 'candidate', artifact_url: `https://example.com/${item}`, checksums_url: 'https://example.com/SHA256SUMS',
      command: `install-${item}-only`,
    }
  })
  const manual = presentPublicGuide(guidePayload({ install_available: true, install_targets: four }))
  assert.equal(manual.targets.length, 4)
  for (const target of manual.targets) assert.equal(manual.steps.join('\n').includes(target.command), false)
  const memory = new Map<string, string>()
  const previous = globalThis.sessionStorage
  Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: {
    getItem: (key: string) => memory.get(key) ?? null,
    setItem: (key: string, value: string) => { memory.set(key, value) },
    removeItem: (key: string) => { memory.delete(key) },
    clear: () => memory.clear(), key: () => null, length: 0,
  } })
  try {
    assert.equal(rememberPairingCode('not-a-code'), false)
    assert.equal(rememberPairingCode('123456789'), true)
    assert.equal(peekPairingCode(), '123-456-789')
    assert.equal([...memory.values()].join(','), '123-456-789')
    assert.equal(takePairingCode(), '123-456-789')
    assert.equal(peekPairingCode(), null)
  } finally {
    Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: previous })
  }
})

test('add harness follows existing_computer_id, and a harness can be left out', () => {
  assert.equal(isAddHarness(view({ computer_id: COMPUTER })), false)
  assert.equal(isAddHarness(view({ state: 'pending', existing_computer_id: COMPUTER, computer_id: null })), true)
  assert.match(describeProgress(view({ existing_computer_id: COMPUTER })).detail, /adds a harness/)
  const accounts = [account(), account({ account_key: 'codex-1', harness: 'codex', label: 'Codex work' })]
  const both = ['cursor-1', 'codex-1']
  assert.deepEqual(setHarnessAccount(accounts, both, 'cursor', null), ['codex-1'])
  const plan = planApproval({ view: view({ requested_accounts: accounts }), choice: 'one_per_harness', selectedAccountKeys: ['codex-1'], permissions: person })
  assert.equal(plan.ok, true)
  if (plan.ok) assert.deepEqual(plan.body.selected_account_keys, ['codex-1'])
})

test('disconnect scope includes the revision and enrollments that were reviewed', () => {
  const first = view({
    revision: 3, computer_state: 'connected',
    enrollments: [enrollment()],
  })
  const added = view({
    revision: 4, computer_state: 'connected',
    enrollments: [enrollment(), enrollment({ account_id: ACCOUNT_2, account_key: 'codex-1', harness: 'codex', label: 'Codex' })],
  })
  assert.notEqual(pairingScopeKey(first), pairingScopeKey(added))
  const open = describeComputerStatus(view({
    computer_state: 'revoked', local_cleanup: 'confirmed', local_processes: 'drained', accounting_state: 'unconfirmed',
    enrollments: [enrollment({ local_processes: 'drained', accounting_state: 'unconfirmed', active_run_ids: [RUN] })],
  }))
  assert.match(open.detail, /Run accounting is unconfirmed/)
  assert.equal(open.detail.includes('settled'), false)
})

test('a redeemed approval says setup is underway until the computer reports it finished', () => {
  const redeemed = describeProgress(view({ state: 'redeemed', computer_state: 'connected' }))
  assert.equal(redeemed.phase, 'setup')
  assert.match(redeemed.title, /consumed/)
  assert.match(redeemed.detail, /Setup is underway/)
  assert.equal(redeemed.detail.includes('already finished pairing'), false)
})

test('a session reset drops an in-flight lookup and keeps the human code', async () => {
  const memory = new Map<string, string>()
  const previous = globalThis.sessionStorage
  Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: {
    getItem: (key: string) => memory.get(key) ?? null,
    setItem: (key: string, value: string) => { memory.set(key, value) },
    removeItem: (key: string) => { memory.delete(key) },
    clear: () => memory.clear(), key: () => null, length: 0,
  } })
  try {
    assert.equal(rememberPairingCode('123-456-789'), true)
    let release: (response: Response) => void = () => {}
    globalThis.fetch = () => new Promise(resolve => { release = resolve })
    const pending = lookupPairing('123-456-789')
    const before = pairingReadGeneration()
    discardPairingReads()
    assert.notEqual(pairingReadGeneration(), before)
    assert.equal(peekPairingCode(), '123-456-789')
    release(jsonResponse(view({ tenant_name: 'Other studio' })))
    await assert.rejects(pending, (error: PairingError) => error instanceof PairingError && error.code === 'session_reset' && !String(error.message).includes('Other studio'))
    assert.equal(peekPairingCode(), '123-456-789')
    takePairingCode()
  } finally {
    Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: previous })
  }
})

test('add harness connect stays blocked until the code matches the opened computer', () => {
  const opened = view({ state: 'pending', existing_computer_id: ACCOUNT, computer_name: 'studio' })
  const mismatch = addHarnessTargetProblem({ view: opened, requestedComputerId: COMPUTER, targetName: 'laptop' })
  assert.equal(mismatch?.code, 'mismatch')
  const fresh = addHarnessTargetProblem({ view: view({ computer_name: 'studio' }), requestedComputerId: COMPUTER, targetName: 'laptop' })
  assert.equal(fresh?.code, 'missing')
  const renamed = addHarnessTargetProblem({ view: opened, requestedComputerId: ACCOUNT, targetName: 'laptop' })
  assert.equal(renamed?.code, 'name')
  assert.match(renamed?.message ?? '', /laptop/)
  assert.match(renamed?.message ?? '', /studio/)
  const plan = planApproval({
    view: opened, choice: 'connect_only', selectedAccountKeys: ['cursor-1'], permissions: person,
    requestedComputerId: COMPUTER, targetComputerName: 'laptop',
  })
  assert.equal(plan.ok, false)
  const matched = planApproval({
    view: opened, choice: 'connect_only', selectedAccountKeys: ['cursor-1'], permissions: person,
    requestedComputerId: ACCOUNT, targetComputerName: 'studio',
  })
  assert.equal(matched.ok, true)
})

test('unsupported verification blocks the selected harness and connect only does not', () => {
  const published = {
    claude: { supported: true, policy: 'no_tools', reason: '' },
    codex: { supported: false, policy: 'unavailable', reason: 'Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.' },
    cursor: { supported: false, policy: 'unavailable', reason: 'Cursor ask mode and an isolated config do not enforce a no-tools policy.' },
    grok: { supported: false, policy: 'unavailable', reason: 'Native Grok guided account identity is unavailable.' },
  }
  const current = view({
    verification_capabilities: published,
    requested_accounts: [
      account({ account_key: 'claude-1', harness: 'claude', label: 'Claude work' }),
      account({ account_key: 'codex-1', harness: 'codex', label: 'Codex work' }),
      account(),
    ],
  })
  const blocked = unsupportedVerification(current, ['claude-1', 'codex-1', 'cursor-1'])
  assert.deepEqual(blocked.map(item => item.harness), ['codex', 'cursor'])
  // AEON-400: excluding unsupported harnesses is an explicit selection change;
  // the approval still verifies all remaining selections under the existing contract.
  const retained = verifiableAccountKeys(current, ['claude-1', 'codex-1', 'cursor-1'])
  assert.deepEqual(retained, ['claude-1'])
  assert.deepEqual(verifiableAccountKeys(current, ['codex-1', 'cursor-1', 'unknown']), [])
  assert.deepEqual(verifiableAccountKeys(current, []), [])
  const mixed = planApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: ['claude-1', 'codex-1', 'cursor-1'], permissions: person })
  assert.equal(mixed.ok, false)
  const withoutVerification = planApproval({ view: current, choice: 'connect_only', selectedAccountKeys: ['claude-1', 'codex-1', 'cursor-1'], permissions: person })
  assert.deepEqual(withoutVerification, { ok: true, body: { request_digest: DIGEST, verification: 'connect_only', selected_account_keys: ['claude-1', 'codex-1', 'cursor-1'] } })
  assert.deepEqual(planApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: retained, permissions: person }), {
    ok: true, body: { request_digest: DIGEST, verification: 'one_per_harness', selected_account_keys: ['claude-1'] },
  })
  for (const permissions of [reader, agent]) {
    assert.equal(planApproval({ view: current, choice: 'connect_only', selectedAccountKeys: ['claude-1', 'codex-1'], permissions }).ok, false)
    assert.equal(planApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: retained, permissions }).ok, false)
  }
  assert.match(blocked[0]?.reason ?? '', /Codex/)
  assert.doesNotMatch(blocked.map(item => item.reason).join(' '), /installation failed/i)
  const denied = planApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: ['codex-1'], permissions: person })
  assert.equal(denied.ok, false)
  if (!denied.ok) assert.match(denied.next, /Turn verification off/)
  const allowed = planApproval({ view: current, choice: 'one_per_harness', selectedAccountKeys: ['claude-1'], permissions: person })
  assert.equal(allowed.ok, true)
  const connectOnly = planApproval({ view: current, choice: 'connect_only', selectedAccountKeys: ['codex-1', 'cursor-1'], permissions: person })
  assert.equal(connectOnly.ok, true)
  if (connectOnly.ok) assert.equal(connectOnly.body.verification, 'connect_only')
  const unspoken = view({ verification_capabilities: undefined })
  assert.deepEqual(verifiableAccountKeys(unspoken, ['cursor-1']), [])
  const quiet = planApproval({ view: unspoken, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person })
  assert.equal(quiet.ok, false)
})

test('every disabled Connect state gives an actionable reason; mixed verification is a choice', () => {
  const ready = { busy: '', canApprove: true, selectedAccountKeys: ['claude-1', 'codex-1'], targetProblem: null }
  assert.equal(connectDisabledReason(ready), null)
  assert.equal(connectDisabledReason({ ...ready, selectedAccountKeys: [] }), 'Choose at least one harness.')
  assert.equal(connectDisabledReason({ ...ready, canApprove: false }), 'Only a signed-in person who can manage accounts can connect or deny this computer.')
  assert.equal(connectDisabledReason({ ...ready, busy: 'approve' }), 'Connecting this computer…')
  assert.equal(connectDisabledReason({ ...ready, busy: 'deny' }), 'Denying this request…')
  assert.equal(connectDisabledReason({ ...ready, busy: 'lookup' }), 'Wait for the current action to finish.')
  for (const code of ['missing', 'mismatch', 'name'] as const) {
    const targetProblem = { code, message: 'Computer mismatch', next: 'Review the computer.' }
    assert.equal(connectDisabledReason({ ...ready, targetProblem }), code === 'name'
      ? 'The computer name must match the computer you opened.'
      : 'Use the matching code, or review this as a new computer.')
  }
})

test('old connect-only enrollment does not block a new verified harness, and a later run is not verification', () => {
  const oldOnly = enrollment({ account_id: ACCOUNT, account_key: 'codex-1', harness: 'codex', verification_state: 'not_selected', verification_run_id: null, active_run_ids: [RUN] })
  const verified = enrollment({ account_id: ACCOUNT_2, account_key: 'claude-1', harness: 'claude', label: 'Claude work', verification_state: 'completed', verification_run_id: RUN, active_run_ids: [] })
  const added = view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    verification: { ...view().verification!, mode: 'one_per_harness' },
    enrollments: [oldOnly, verified],
  })
  assert.equal(describeProgress(added).phase, 'connected')
  assert.equal(describeProgress(added).title, 'Connected')
  const unavailable = describeProgress(view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    setup_error: 'verification_unavailable',
    enrollments: [enrollment({ verification_state: 'unavailable', verification_error: 'verification_unavailable' })],
  }))
  assert.equal(unavailable.title, 'Verification unavailable')
  assert.equal(unavailable.detail.toLowerCase().includes('installation'), false)
  assert.match(unavailable.detail, /stays paired/)
  const later = describeProgress(view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    verification: { ...view().verification!, mode: 'connect_only' },
    enrollments: [enrollment({ verification_state: 'not_selected', active_run_ids: [RUN] })],
  }))
  assert.notEqual(later.title, 'Verification is running')
  assert.equal(later.phase, 'connected')
})

test('a failed computer refresh keeps the list and does not claim cleanup', () => {
  const current = [view({ computer_id: COMPUTER, computer_state: 'connected', local_cleanup: 'pending', local_processes: 'unconfirmed' })]
  const failed = applyComputerListRefresh(current, { ok: false })
  assert.equal(failed.failed, true)
  assert.equal(failed.computers[0]?.local_cleanup, 'pending')
  assert.equal(failed.computers[0]?.local_processes, 'unconfirmed')
  assert.equal(failed.computers.length, 1)
  const replaced = applyComputerListRefresh(current, { ok: true, computers: [] })
  assert.equal(replaced.failed, false)
  assert.equal(replaced.computers.length, 0)
})

function enrollment(overrides: Record<string, unknown> = {}) {
  return {
    account_id: ACCOUNT,
    account_key: 'cursor-1',
    harness: 'cursor',
    label: 'Cursor work',
    model_profile_id: MODEL,
    state: 'connected',
    local_cleanup: 'pending',
    verification_run_id: RUN,
    active_run_ids: [],
    ...overrides,
  }
}

test('harness status is scoped, optional and never claims readiness from stale evidence', async () => {
  const connected = view({ state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online', harness_statuses: { claude: 'blocked', codex: 'ready' } })
  assert.equal(describeHarnessStatus(connected, 'claude'), 'Needs attention')
  assert.equal(describeHarnessStatus(connected, 'codex'), 'Ready')
  assert.equal(describeHarnessStatus(connected, 'cursor'), '')
  assert.equal(describeComputerStatus(connected).stateLabel, 'Connected')
  assert.equal(describeProgress(connected).phase, 'connected')
  assert.equal(describeHarnessStatus({ ...connected, connectivity: 'offline' }, 'codex'), 'Last reported: ready')
  assert.equal(describeHarnessStatus({ ...connected, computer_state: 'revoked' }, 'codex'), '')
  assert.equal(describeHarnessStatus({ ...connected, computer_state: 'draining' }, 'codex'), '')
  globalThis.fetch = async () => jsonResponse({ computers: [connected] })
  assert.deepEqual((await listPairingComputers())[0]?.harness_statuses, { claude: 'blocked', codex: 'ready' })
  globalThis.fetch = async () => jsonResponse({ computers: [view({ harness_statuses: { claude: 'raw local diagnostics', future: 'ready', codex: 'ready' } })] })
  assert.deepEqual((await listPairingComputers())[0]?.harness_statuses, { codex: 'ready' })
  globalThis.fetch = async () => jsonResponse({ computers: [view()] })
  assert.equal((await listPairingComputers())[0]?.harness_statuses, undefined)
})

test('harness details whitelist reasons and derive fixed commands without exposing diagnostics', async () => {
  const report = view({ computer_state: 'connected', connectivity: 'online', harness_statuses: { claude: 'blocked', codex: 'ready' }, harness_details: { claude: { state: 'blocked', reason: 'repin_pending', fix: 'local-only command' }, codex: { state: 'ready' } } })
  globalThis.fetch = async () => jsonResponse({ computers: [report] })
  let parsed = (await listPairingComputers())[0]!
  assert.equal(describeHarnessStatus(parsed, 'claude'), 'Waiting for repin')
  assert.equal(describeHarnessFix(parsed, 'claude'), '')
  assert.equal(describeHarnessStatus({ ...parsed, connectivity: 'offline' }, 'claude'), 'Last reported: waiting for repin')
  assert.equal(JSON.stringify(parsed).includes('local-only'), false)
  report.harness_details = { claude: { state: 'blocked', reason: 'dependency_invalid', fix: 'local-only command' } }
  parsed = (await listPairingComputers())[0]!
  assert.equal(describeHarnessFix(parsed, 'claude'), 'aeon-agentd repin --harness claude')
  assert.equal(describeHarnessFix({ ...parsed, computer_state: 'revoked' }, 'claude'), '')
  report.harness_details = { claude: { state: 'blocked', reason: 'future-code', fix: 'local-only command' }, codex: { state: 'ready', reason: 'dependency_invalid' }, future: { state: 'ready' } }
  parsed = (await listPairingComputers())[0]!
  assert.deepEqual(parsed.harness_details, {})
  assert.equal(describeHarnessStatus(parsed, 'claude'), 'Needs attention')
  report.harness_statuses = { claude: 'ready' }
  report.harness_details = { claude: { state: 'blocked', reason: 'dependency_invalid' } }
  parsed = (await listPairingComputers())[0]!
  assert.equal(describeHarnessStatus(parsed, 'claude'), 'Ready')
  assert.equal(describeHarnessFix(parsed, 'claude'), '')
})

test('web fixes and labels follow the shared daemon/server vocabulary', async () => {
  // Written by internal/agentsetup TestRecoveryFixIsOneSharedVocabulary from RecoveryFix.
  const table = JSON.parse(readFileSync(new URL('../../internal/agentsetup/testdata/harness_recovery.json', import.meta.url), 'utf8')) as {
    harnesses: string[]; reasons: string[]; fixes: Record<string, Record<string, { kind: string; command: string }>>
  }
  assert.ok(table.reasons.includes('pin_drifted') && table.reasons.includes('future_reason'))
  for (const harness of table.harnesses) {
    for (const reason of table.reasons) {
      assert.deepEqual(harnessRecovery(harness, reason), table.fixes[harness]?.[reason], `${harness}/${reason}`)
      const report = view({ computer_state: 'connected', connectivity: 'online', harness_statuses: { [harness]: 'blocked' }, harness_details: { [harness]: { state: 'blocked', reason, fix: 'untrusted' } } })
      globalThis.fetch = async () => jsonResponse({ computers: [report] })
      const parsed = (await listPairingComputers())[0]!
      const label = describeHarnessStatus(parsed, harness)
      // Every known code has its own words; only a newer code is shown raw.
      assert.equal(label.startsWith('Needs attention'), reason === 'future_reason', `${harness}/${reason}: ${label}`)
      assert.equal(label.includes('future_reason'), reason === 'future_reason')
      assert.equal(describeHarnessFix(parsed, harness), table.fixes[harness]?.[reason]?.command ?? '')
      assert.equal(JSON.stringify(parsed).includes('untrusted'), false)
    }
  }
  const drifted = view({ computer_state: 'connected', connectivity: 'online', setup_state: 'connected', harness_statuses: { claude: 'blocked', codex: 'ready' }, harness_details: { claude: { state: 'blocked', reason: 'pin_drifted' }, codex: { state: 'ready' } } })
  globalThis.fetch = async () => jsonResponse({ computers: [drifted] })
  let parsed = (await listPairingComputers())[0]!
  assert.equal(describeComputerStatus(parsed).stateLabel, 'Connected')
  assert.equal(describeHarnessStatus(parsed, 'claude'), 'Pin changed')
  assert.equal(describeHarnessFix(parsed, 'claude'), 'aeon-agentd repin --harness claude')
  assert.equal(describeHarnessStatus(parsed, 'codex'), 'Ready')
  assert.equal(describeHarnessFix(parsed, 'codex'), '')
  // A newer state is omitted from the closed legacy map but stays visible.
  const future = view({ computer_state: 'connected', connectivity: 'online', harness_statuses: { codex: 'ready' }, harness_details: { claude: { state: 'future_state', reason: 'future_reason' }, codex: { state: 'ready' } } })
  globalThis.fetch = async () => jsonResponse({ computers: [future] })
  parsed = (await listPairingComputers())[0]!
  assert.equal(describeHarnessStatus(parsed, 'claude'), 'Needs attention · future_reason')
  assert.equal(describeHarnessFix(parsed, 'claude'), '')
  assert.equal(describeHarnessHint(parsed, 'claude'), '')
  for (const [reason, hint] of [['repin_pending', 'Retries automatically.'], ['cli_unavailable', 'Restore the approved executable, then retry.'], ['pin_drifted', '']]) {
    const held = view({ computer_state: 'connected', connectivity: 'online', harness_statuses: { claude: 'blocked' }, harness_details: { claude: { state: 'blocked', reason } } })
    globalThis.fetch = async () => jsonResponse({ computers: [held] })
    assert.equal(describeHarnessHint((await listPairingComputers())[0]!, 'claude'), hint)
  }
})

test('a ready harness keeps a blocked sibling account visible', async () => {
  const blocked = '88888888-8888-4888-8888-888888888888'
  const enrollments = [
    enrollment({ account_id: ACCOUNT_2, account_key: 'codex-healthy', harness: 'codex', label: 'Healthy' }),
    enrollment({ account_id: blocked, account_key: 'codex-blocked', harness: 'codex', label: 'Blocked' }),
  ]
  const report = view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    harness_statuses: { codex: 'ready' },
    enrollments,
    harness_details: { codex: { state: 'ready', reason: 'dependency_invalid', fix: { kind: 'restart', command: 'untrusted' }, attention_accounts: [
      { account_id: blocked, reason: 'pin_drifted' },
      { account_id: 'not-a-uuid', reason: 'pin_drifted' },
      { account_id: ACCOUNT_2, reason: '' },
    ] } },
  })
  globalThis.fetch = async () => jsonResponse({ computers: [report] })
  const parsed = (await listPairingComputers())[0]!
  assert.deepEqual(parsed.harness_details?.codex?.attention_accounts, [{ account_id: blocked, reason: 'pin_drifted' }])
  assert.equal(parsed.harness_details?.codex?.reason, undefined)
  assert.equal(JSON.stringify(parsed).includes('untrusted'), false)
  assert.equal(describeComputerStatus(parsed).stateLabel, 'Connected')
  assert.equal(describeHarnessStatus(parsed, 'codex'), 'Pin changed')
  assert.equal(describeHarnessHint(parsed, 'codex'), '1 of 2 accounts needs attention')
  assert.equal(describeHarnessFix(parsed, 'codex'), 'aeon-agentd add-harness --harness codex')
  assert.equal(describeEnrollmentStatus(parsed, { account_id: ACCOUNT_2, harness: 'codex' }), 'Ready')
  assert.equal(describeEnrollmentStatus(parsed, { account_id: blocked, harness: 'codex' }), 'Pin changed')
  assert.equal(describeHarnessStatus({ ...parsed, connectivity: 'offline' }, 'codex'), 'Last reported: pin changed')
  assert.equal(describeEnrollmentStatus({ ...parsed, connectivity: 'offline' }, { account_id: ACCOUNT_2, harness: 'codex' }), 'Last reported: ready')
  const mixed = view({
    state: 'redeemed', computer_state: 'connected', connectivity: 'online',
    harness_statuses: { codex: 'ready' },
    enrollments,
    harness_details: { codex: { state: 'ready', attention_accounts: [
      { account_id: blocked, reason: 'pin_drifted' },
      { account_id: ACCOUNT_2, reason: 'login_required' },
    ] } },
  })
  globalThis.fetch = async () => jsonResponse({ computers: [mixed] })
  const both = (await listPairingComputers())[0]!
  assert.equal(both.harness_details?.codex?.state, 'ready')
  assert.equal(both.harness_details?.codex?.attention_accounts, undefined)
  assert.equal(describeHarnessStatus(both, 'codex'), 'Ready')
  assert.equal(describeHarnessHint(both, 'codex'), '')
  const unknown = view({
    state: 'redeemed', computer_state: 'connected', connectivity: 'online',
    harness_statuses: { codex: 'ready' },
    enrollments,
    harness_details: { codex: { state: 'ready', attention_accounts: [{ account_id: blocked, reason: 'future_reason' }] } },
  })
  globalThis.fetch = async () => jsonResponse({ computers: [unknown] })
  const future = (await listPairingComputers())[0]!
  assert.equal(describeHarnessStatus(future, 'codex'), 'Needs attention · future_reason')
  assert.equal(describeHarnessFix(future, 'codex'), '')
  assert.equal(describeHarnessHint(future, 'codex'), '1 of 2 accounts needs attention')
  assert.equal(parsed.harness_details?.codex?.attention_count, 1)
  assert.equal(parsed.harness_details?.codex?.attention_truncated, undefined)
})

test('six blocked accounts of seven keep the full count and are not labeled ready', async () => {
  const ids = Array.from({ length: 7 }, (_, index) => `88888888-8888-4888-8888-88888888888${index}`)
  const healthy = ids[0]!
  const blocked = ids.slice(1)
  const enrollments = ids.map((account_id, index) => enrollment({
    account_id, account_key: `codex-${index}`, harness: 'codex', label: index ? `Blocked ${index}` : 'Healthy',
  }))
  const full = view({
    state: 'redeemed', computer_state: 'connected', setup_state: 'connected', connectivity: 'online',
    harness_statuses: { codex: 'ready' },
    enrollments,
    harness_details: { codex: { state: 'ready', attention_count: 6, attention_accounts: blocked.map(account_id => ({ account_id, reason: 'pin_drifted' })) } },
  })
  globalThis.fetch = async () => jsonResponse({ computers: [full] })
  const parsed = (await listPairingComputers())[0]!
  assert.equal(parsed.harness_details?.codex?.attention_count, 6)
  assert.equal(parsed.harness_details?.codex?.attention_truncated, undefined)
  assert.equal(parsed.harness_details?.codex?.attention_accounts?.length, 6)
  assert.equal(describeHarnessHint(parsed, 'codex'), '6 of 7 accounts need attention')
  assert.equal(describeEnrollmentStatus(parsed, { account_id: healthy, harness: 'codex' }), 'Ready')
  for (const account_id of blocked) {
    const label = describeEnrollmentStatus(parsed, { account_id, harness: 'codex' })
    assert.equal(label.includes('Ready'), false, label)
    assert.equal(label, 'Pin changed')
  }
  const omitted = blocked[5]!
  const truncated = view({
    state: 'redeemed', computer_state: 'connected', connectivity: 'online',
    harness_statuses: { codex: 'ready' },
    enrollments,
    harness_details: { codex: {
      state: 'ready', attention_count: 6, attention_truncated: true,
      attention_accounts: blocked.slice(0, 5).map(account_id => ({ account_id, reason: 'pin_drifted' })),
    } },
  })
  globalThis.fetch = async () => jsonResponse({ computers: [truncated] })
  const capped = (await listPairingComputers())[0]!
  assert.equal(capped.harness_details?.codex?.attention_count, 6)
  assert.equal(capped.harness_details?.codex?.attention_truncated, true)
  assert.equal(describeHarnessHint(capped, 'codex'), '6 of 7 accounts need attention')
  assert.equal(describeEnrollmentStatus(capped, { account_id: omitted, harness: 'codex' }), 'Needs attention')
  assert.equal(describeEnrollmentStatus(capped, { account_id: healthy, harness: 'codex' }).includes('Ready'), false)
  for (const account_id of blocked.slice(0, 5)) {
    assert.equal(describeEnrollmentStatus(capped, { account_id, harness: 'codex' }), 'Pin changed')
  }
  const legacy = view({
    state: 'redeemed', computer_state: 'connected', connectivity: 'online',
    harness_statuses: { codex: 'ready' },
    enrollments,
    harness_details: { codex: { state: 'ready', attention_accounts: blocked.slice(0, 5).map(account_id => ({ account_id, reason: 'login_required' })) } },
  })
  globalThis.fetch = async () => jsonResponse({ computers: [legacy] })
  const old = (await listPairingComputers())[0]!
  assert.equal(describeHarnessHint(old, 'codex'), 'At least 5 of 7 accounts need attention')
  assert.equal(describeEnrollmentStatus(old, { account_id: omitted, harness: 'codex' }).includes('Ready'), false)
})

function attentionAccountId(index: number): string {
  return `88888888-8888-4888-8888-${String(index).padStart(12, '0')}`
}

test('legacy, count-only and truncation-only attention never call an omitted account ready', async () => {
  async function parsedAttention(total: number, blockedCount: number, listed: number, detail: Record<string, unknown>) {
    const ids = Array.from({ length: total }, (_, index) => attentionAccountId(index))
    const healthy = ids[0]!
    const blocked = ids.slice(1, 1 + blockedCount)
    const enrollments = ids.map((account_id, index) => enrollment({
      account_id, account_key: `codex-${index}`, harness: 'codex', label: index ? `Blocked ${index}` : 'Healthy',
    }))
    const computer = view({
      state: 'redeemed', computer_state: 'connected', connectivity: 'online',
      harness_statuses: { codex: 'ready' },
      enrollments,
      harness_details: {
        codex: {
          state: 'ready',
          attention_accounts: blocked.slice(0, listed).map(account_id => ({ account_id, reason: 'pin_drifted' })),
          ...detail,
        },
      },
    })
    globalThis.fetch = async () => jsonResponse({ computers: [computer] })
    const parsed = (await listPairingComputers())[0]!
    return { healthy, blocked, parsed }
  }

  const legacy = await parsedAttention(7, 6, 5, {})
  assert.equal(legacy.parsed.harness_details?.codex?.attention_count, 5)
  assert.equal(legacy.parsed.harness_details?.codex?.attention_truncated, true)
  assert.equal(describeHarnessHint(legacy.parsed, 'codex'), 'At least 5 of 7 accounts need attention')
  assert.equal(describeEnrollmentStatus(legacy.parsed, { account_id: legacy.blocked[5]!, harness: 'codex' }), 'Needs attention')
  assert.equal(describeEnrollmentStatus(legacy.parsed, { account_id: legacy.healthy, harness: 'codex' }).includes('Ready'), false)

  const countOnly = await parsedAttention(7, 6, 5, { attention_count: 6 })
  assert.equal(countOnly.parsed.harness_details?.codex?.attention_count, 6)
  assert.equal(countOnly.parsed.harness_details?.codex?.attention_truncated, true)
  assert.equal(describeHarnessHint(countOnly.parsed, 'codex'), '6 of 7 accounts need attention')
  assert.equal(describeEnrollmentStatus(countOnly.parsed, { account_id: countOnly.blocked[5]!, harness: 'codex' }), 'Needs attention')
  assert.equal(describeEnrollmentStatus(countOnly.parsed, { account_id: countOnly.healthy, harness: 'codex' }).includes('Ready'), false)
  assert.equal(describeEnrollmentStatus(countOnly.parsed, { account_id: countOnly.blocked[0]!, harness: 'codex' }), 'Pin changed')

  const flagOnly = await parsedAttention(7, 6, 5, { attention_truncated: true })
  assert.equal(flagOnly.parsed.harness_details?.codex?.attention_count, 5)
  assert.equal(flagOnly.parsed.harness_details?.codex?.attention_truncated, true)
  assert.equal(describeHarnessHint(flagOnly.parsed, 'codex'), 'At least 5 of 7 accounts need attention')
  assert.equal(describeEnrollmentStatus(flagOnly.parsed, { account_id: flagOnly.blocked[5]!, harness: 'codex' }), 'Needs attention')

  for (const [total, blockedCount] of [[5, 4], [6, 5], [7, 6], [32, 31], [33, 32]] as const) {
    const current = await parsedAttention(total, blockedCount, blockedCount, { attention_count: blockedCount })
    assert.equal(current.parsed.harness_details?.codex?.attention_truncated, undefined, `total ${total}`)
    assert.equal(current.parsed.harness_details?.codex?.attention_count, blockedCount)
    assert.equal(describeEnrollmentStatus(current.parsed, { account_id: current.healthy, harness: 'codex' }), 'Ready', `total ${total}`)
    assert.equal(describeHarnessHint(current.parsed, 'codex').startsWith('At least '), false, `total ${total}`)
  }

  const capped34 = await parsedAttention(34, 33, 32, { attention_count: 33, attention_truncated: true })
  assert.equal(describeEnrollmentStatus(capped34.parsed, { account_id: capped34.blocked[32]!, harness: 'codex' }), 'Needs attention')
  assert.equal(describeEnrollmentStatus(capped34.parsed, { account_id: capped34.healthy, harness: 'codex' }).includes('Ready'), false)

  const capped65 = await parsedAttention(65, 64, 32, { attention_count: 64, attention_truncated: true })
  assert.equal(capped65.parsed.harness_details?.codex?.attention_count, 64)
  assert.equal(capped65.parsed.harness_details?.codex?.attention_truncated, true)
  assert.equal(describeEnrollmentStatus(capped65.parsed, { account_id: capped65.blocked[32]!, harness: 'codex' }), 'Needs attention')
  assert.equal(describeEnrollmentStatus(capped65.parsed, { account_id: capped65.healthy, harness: 'codex' }).includes('Ready'), false)
})

test('pairing account approval is selected, idempotent and never creates windows', async () => {
  const calls: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input))
    return new Response(null, { status: 204 })
  }) as typeof fetch
  const paired = view({ state: 'approved', enrollments: [enrollment(), enrollment({ account_id: ACCOUNT_2, account_key: 'other' })] })
  const key = paired.enrollments[0]!.account_key
  assert.deepEqual(await approvePairedAccounts(paired, [key], person), [ACCOUNT])
  assert.deepEqual(await approvePairedAccounts(paired, [key], person), [ACCOUNT])
  assert.deepEqual(calls, [`/api/agent-accounts/${ACCOUNT}/capacity/approve`, `/api/agent-accounts/${ACCOUNT}/capacity/approve`])
  await assert.rejects(() => approvePairedAccounts(paired, [key], agent), /Only a person/)
  await assert.rejects(() => approvePairedAccounts(paired, ['missing'], person), /not connected/)
  assert.equal(calls.length, 2)
})

test('account approval stops between accounts when the signed-in scope changes', async () => {
  let finish!: (response: Response) => void
  let calls = 0
  globalThis.fetch = (async () => {
    calls++
    return new Promise<Response>(resolve => { finish = resolve })
  }) as typeof fetch
  const paired = view({ state: 'approved', enrollments: [enrollment(), enrollment({ account_id: ACCOUNT_2, account_key: 'other' })] })
  const pending = approvePairedAccounts(paired, paired.enrollments.map(e => e.account_key), person)
  discardPairingReads()
  finish(new Response(null, { status: 204 }))
  await assert.rejects(pending, (error: PairingError) => error.code === 'session_reset')
  assert.equal(calls, 1)
})

test('Homebrew commands are additive, bounded and published by this instance', async () => {
  const command = "brew install inspr-at/tap/aeon-agentd\nenv \"$(brew --prefix)/bin/aeon-agentd\" pair --url 'https://other.example'"
  globalThis.fetch = async () => jsonResponse(guidePayload({ homebrew_command: command }))
  assert.equal(presentPublicGuide(await getPairingGuide()).homebrewCommand, command)
  assert.equal(presentPublicGuide(guidePayload()).homebrewCommand, '')
  for (const bad of [[], 'x'.repeat(4001)]) {
    globalThis.fetch = async () => jsonResponse(guidePayload({ homebrew_command: bad }))
    await assert.rejects(getPairingGuide)
  }
  globalThis.fetch = async () => jsonResponse(guidePayload({ homebrew_formula_current: 'yes' }))
  assert.equal(presentPublicGuide(await getPairingGuide()).homebrewCommand, '')
})

test('Homebrew is always offered alongside the pinned direct download and the saved install choice', () => {
  const command = `brew install inspr-at/tap/aeon-agentd\nenv "$(brew --prefix)/bin/aeon-agentd" pair --url 'https://aeon.example'`
  const target = {
    platform: 'darwin' as const, arch: 'arm64' as const, service: 'launchd-user' as const, qualification: 'candidate',
    artifact_url: 'https://example.com/darwin', checksums_url: 'https://example.com/SHA256SUMS', command: 'install-darwin-only',
  }
  for (const legacyHint of [true, false, null, undefined]) {
    const presented = presentPublicGuide(guidePayload({
      homebrew_command: command, homebrew_formula_current: legacyHint,
      install_available: true, install_targets: [target],
    }))
    assert.equal(presented.homebrewCommand, command)
    assert.deepEqual(installMethods(presented), ['manual', 'homebrew'])
    assert.equal(chooseInstallMethod(installMethods(presented), ''), 'manual')
    assert.deepEqual(presented.targets, [target])
  }

  const managed = {
    command: 'env "$HOME/.nix-profile/bin/aeon-agentd" pair --url \'https://aeon.example\'',
    service_option: 'services.aeon.enable', module_url: 'https://example.test/module.nix', service_note: 'Needs a paired-service update.',
    platform_note: 'Service module: macOS only.',
  }
  const nix = presentPublicGuide(guidePayload({ managed_setup: managed }))
  assert.equal(nix.nixLabel, 'macOS · Nix')
  assert.equal(nix.nixHint, 'Nix or Home Manager on this Mac? Choose macOS · Nix.')
  assert.equal(presentPublicGuide(guidePayload({ managed_setup: { ...managed, platform_note: 'Service module: Linux only.' } })).nixLabel, 'Linux · Nix')
  assert.equal(presentPublicGuide(guidePayload({ managed_setup: { ...managed, platform_note: 'Service module: macOS and Linux.' } })).nixLabel, 'Nix / Home Manager')

  const memory = new Map<string, string>()
  const storage = {
    getItem: (key: string) => memory.get(key) ?? null,
    setItem: (key: string, value: string) => { memory.set(key, value) },
  }
  assert.equal(readPairingInstallMethod(storage), '')
  writePairingInstallMethod(storage, 'nix')
  assert.equal(chooseInstallMethod(['homebrew', 'manual', 'nix'], readPairingInstallMethod(storage)), 'nix')
  assert.equal(chooseInstallMethod(['manual', 'nix'], 'homebrew'), 'manual')
  writePairingInstallMethod({ setItem() { throw new Error('blocked') } }, 'nix')
  assert.equal(readPairingInstallMethod({ getItem() { throw new Error('blocked') } }), '')
})

test('pairing readiness names unbound harnesses and bounded account checks', () => {
  const computer = view({ computer_state: 'connected', connectivity: 'online',
    harness_details: { codex: { state: 'blocked', reason: 'binding_missing' } },
    harness_statuses: { codex: 'blocked' },
  })
  assert.match(describeHarnessHint(computer, 'codex'), /Codex was approved but isn't set up/)
  assert.equal(describeHarnessFix(computer, 'codex'), 'aeon-agentd add-harness --harness codex')
  for (const [reason, expected] of [['probe_pending', /60 seconds/], ['probe_timeout', /60 seconds/], ['capacity_capture', /10 seconds/], ['capacity_timeout', /10 seconds/]] as const) {
    computer.harness_details = { codex: { state: reason.endsWith('timeout') ? 'blocked' : 'checking', reason } }
    computer.harness_statuses = { codex: computer.harness_details.codex!.state as 'blocked' | 'checking' }
    assert.match(describeHarnessStatus(computer, 'codex'), expected)
  }
})

test('verification refusal names the harness and cause without claiming pairing failed', () => {
  const computer = view({ computer_state: 'connected', connectivity: 'online', state: 'redeemed', setup_state: 'connected',
    enrollments: [{ account_id: ACCOUNT, harness: 'claude', state: 'connected', verification_state: 'failed', verification_error: 'verification_unavailable', verification_reason: 'adapter_unsupported' }],
  })
  const hint = describeHarnessHint(computer, 'claude')
  assert.match(hint, /Claude verification couldn't run/)
  assert.match(hint, /installed adapter cannot enforce safe verification/)
  assert.match(hint, /computer is paired/)
  assert.match(describeProgress(computer).detail, /Claude verification couldn't run/)
})

test('only incompatible computers display update advice', async () => {
  const advice = 'Update aeon-agentd to at least 261001072608.0.0.'
  for (const status of ['compatible', 'unknown', 'update_required', 'protocol_mismatch'] as const) {
    const action = ['update_required', 'protocol_mismatch'].includes(status) ? advice : ''
    const computer = view({ agent_compatibility: { status, action } })
    globalThis.fetch = async () => jsonResponse(computer)
    const parsed = await getPairingComputer(COMPUTER)
    assert.equal(agentUpdateAdvice(parsed), ['update_required', 'protocol_mismatch'].includes(status) ? advice : '')
  }
  assert.equal(agentUpdateAdvice(view()), '')
})


test('AEON-623: safe probe diagnostics survive parsing and select the exact repair', async () => {
  const detail = 'the default Claude profile is not private (requires mode 0700)'
  const report = view({ computer_state: 'connected', connectivity: 'online', harness_statuses: { claude: 'blocked' }, harness_details: { claude: { state: 'blocked', reason: 'probe_failed', reason_detail: detail, fix: { kind: 'restart', command: 'untrusted command' } } } })
  globalThis.fetch = async () => jsonResponse({ computers: [report] })
  const parsed = (await listPairingComputers())[0]!
  assert.equal(parsed.harness_details?.claude?.reason_detail, detail)
  assert.equal(describeHarnessFix(parsed, 'claude'), 'chmod 700 "$HOME/.claude"')
  assert.equal(describeEnrollmentDiagnostic(parsed, { account_id: ACCOUNT, harness: 'claude' })?.hint, `Claude: sign-in check failed: ${detail}.`)
  assert.equal(JSON.stringify(parsed).includes('untrusted command'), false)
  report.harness_details!.claude!.reason_detail = 'arbitrary local command output'
  const rejected = (await listPairingComputers())[0]!
  assert.equal(rejected.harness_details?.claude?.reason_detail, undefined)
  assert.equal(describeHarnessFix(rejected, 'claude'), 'aeon-agentd setup')
})

test('AEON-623: a ready sibling does not inherit another account probe repair', () => {
  const report = view({ computer_state: 'connected', connectivity: 'online', harness_statuses: { claude: 'ready' }, harness_details: { claude: { state: 'ready', attention_accounts: [{ account_id: ACCOUNT, reason: 'probe_failed', reason_detail: 'the default Claude profile is not private (requires mode 0700)' }], attention_count: 1 } } })
  assert.equal(describeEnrollmentDiagnostic(report, { account_id: ACCOUNT, harness: 'claude' })?.command, 'chmod 700 "$HOME/.claude"')
  assert.equal(describeEnrollmentDiagnostic(report, { account_id: ACCOUNT_2, harness: 'claude' }), null)
  assert.equal(describeEnrollmentStatus(report, { account_id: ACCOUNT_2, harness: 'claude', verification_state: 'queued' }), 'Verification queued')
  assert.equal(describeEnrollmentStatus(report, { account_id: ACCOUNT_2, harness: 'claude', verification_state: 'failed' }), 'Verification failed')
})
