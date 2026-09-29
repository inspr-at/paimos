// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { sessionEnded } from '../src/lib/api.ts'
import {
  DEFAULT_REVIEW_CHOICE, LOOKUP_DEBOUNCE_MS, MIN_POLL_INTERVAL_MS, PUBLIC_PAIRING_GUIDE_PATH, PairingError,
  agentRouteKind, canonicalUserCode, clearPairingClientState, createOngoingLimits, denyPairing,
  describeComputerStatus, describeProgress, disconnectComputer, disconnectConfirm, disconnectEnrollment,
  formatVerification, getPairingGuide, isPublicPairingGuide, listPairingComputers, lookupPairing,
  ongoingLimitError, pairingPermissions, planApproval, planLookup, planOngoingLimits, planPoll,
  registerAgentUrl, sessionFreezeApplies, submitApproval, verificationWarning, defaultSelectedAccountKeys,
  peekPairingCode, presentPublicGuide, rememberPairingCode, takePairingCode,
  isAddHarness, matchOngoingLimit, pairingScopeKey, setHarnessAccount,
  addHarnessTargetProblem, applyComputerListRefresh, discardPairingReads, formatAllowanceMoment,
  ongoingLimitAccounts, pairingReadGeneration, unsupportedVerification,
  type OngoingLimitDraft, type PairingGuide, type PairingView, type RequestedAccount,
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

test('ongoing limits are a separate person allowance and are not part of approval', async () => {
  const draft: OngoingLimitDraft = {
    account_key: 'cursor-1', starts_at: '2026-09-27T18:00:00Z', ends_at: '2026-09-27T23:00:00Z',
    unit: 'requests', allowance: 4, pace_model: 'unrestricted', burst_ratio: 0,
  }
  assert.equal(ongoingLimitError({ ...draft, allowance: 1.5 }), 'Enter valid start and end times, a positive whole allowance, and a burst ratio from 0 to 1.')
  const plan = planApproval({ view: view(), choice: 'ongoing_limits', selectedAccountKeys: ['cursor-1'], permissions: person, drafts: [draft] })
  assert.equal(plan.ok, true)
  if (plan.ok) {
    assert.equal(plan.body.verification, 'connect_only')
    assert.equal('allowance' in plan.body, false)
    assert.equal('expected_revision' in plan.body, false)
  }
  const calls: { url: string; body: unknown }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (String(url).endsWith('/approve')) return jsonResponse(view({ state: 'approved', computer_id: COMPUTER, enrollments: [enrollment()] }))
    return jsonResponse({ id: 'window' })
  }
  await submitApproval({ view: view(), choice: 'ongoing_limits', selectedAccountKeys: ['cursor-1'], permissions: person, drafts: [draft] })
  assert.deepEqual(calls.map(call => call.url), ['/api/agent-pairing/requests/11111111-1111-4111-8111-111111111111/approve'])
  const windows = planOngoingLimits({
    choice: 'ongoing_limits', drafts: [draft], selectedAccountKeys: ['cursor-1'], enrollments: [enrollment()], permissions: person,
  })
  assert.equal(windows.action, 'send')
  if (windows.action === 'send') {
    await createOngoingLimits(windows.windows, person)
    assert.equal(calls[1]?.url, `/api/agent-accounts/${ACCOUNT}/windows`)
    assert.equal((calls[1]?.body as { allowance: number }).allowance, 4)
    assert.equal(JSON.stringify(calls[1]?.body).includes('quota'), false)
  }
  await assert.rejects(createOngoingLimits([{ accountId: ACCOUNT, body: { starts_at: draft.starts_at, ends_at: draft.ends_at, unit: 'requests', allowance: 4, pace_model: 'unrestricted', burst_ratio: 0 } }], agent))
})

test('an agent cannot approve, deny, disconnect or set limits', async () => {
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

test('a lost allowance response is reconciled and is not posted again', async () => {
  const draft: OngoingLimitDraft = {
    account_key: 'cursor-1', starts_at: '2026-09-27T18:00:00.000Z', ends_at: '2026-09-27T23:00:00.000Z',
    unit: 'requests', allowance: 4, pace_model: 'unrestricted', burst_ratio: 0,
  }
  const body = {
    starts_at: '2026-09-27T18:00:00.000Z', ends_at: '2026-09-27T23:00:00.000Z',
    unit: 'requests' as const, allowance: 4, pace_model: 'unrestricted' as const, burst_ratio: 0,
  }
  const posts: string[] = []
  globalThis.fetch = async (url, init) => {
    const path = String(url)
    if (path.endsWith('/windows')) {
      posts.push(path)
      return jsonResponse({ error: 'gateway' }, 503)
    }
    assert.equal(path, '/api/agent-accounts')
    assert.equal(init?.method ?? 'GET', 'GET')
    return jsonResponse([{ id: ACCOUNT, windows: [{ id: 'window', account_id: ACCOUNT, used: 0, reserved: 0, ...body }] }])
  }
  const saved = await createOngoingLimits([{ accountId: ACCOUNT, body }], person)
  assert.deepEqual(saved, { created: [], reconciled: [ACCOUNT] })
  assert.deepEqual(posts, [`/api/agent-accounts/${ACCOUNT}/windows`])
  clearPairingClientState()
  globalThis.fetch = async () => { throw new TypeError('network down') }
  await assert.rejects(createOngoingLimits([{ accountId: ACCOUNT, body }], person), (error: PairingError) => {
    assert.equal(error.code, 'allowance_uncertain')
    assert.equal(error.message.includes('Set allowance again'), false)
    assert.match(error.next, /lost response is not the same as an unsaved allowance/)
    return true
  })
  assert.equal(await matchOngoingLimit(ACCOUNT, body), 'unknown')
})

test('a session reset stops the allowance batch before reconciliation or another write', async () => {
  const body = {
    starts_at: '2026-09-27T18:00:00Z', ends_at: '2026-09-27T23:00:00Z',
    unit: 'requests' as const, allowance: 4, pace_model: 'unrestricted' as const, burst_ratio: 0,
  }
  for (const status of [200, 503]) {
    const calls: string[] = []
    let release: (response: Response) => void = () => {}
    globalThis.fetch = (url) => {
      calls.push(String(url))
      return new Promise(resolve => { release = resolve })
    }
    const pending = createOngoingLimits([{ accountId: ACCOUNT, body }, { accountId: ACCOUNT_2, body }], person)
    discardPairingReads()
    release(jsonResponse({ id: 'window', ...body }, status))
    await assert.rejects(pending, (error: PairingError) => error.code === 'session_reset')
    assert.deepEqual(calls, [`/api/agent-accounts/${ACCOUNT}/windows`])
  }
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

test('the first allowance save uses only this request’s selected accounts', () => {
  const oldAccount = enrollment({ account_id: ACCOUNT, account_key: 'cursor-old', harness: 'cursor', label: 'Old Cursor' })
  const selected = enrollment({ account_id: ACCOUNT_2, account_key: 'claude-1', harness: 'claude', label: 'Claude work' })
  const rows = ongoingLimitAccounts({
    pending: false,
    selectedKeys: ['claude-1'],
    grantedKeys: ['claude-1'],
    limitsNow: true,
    showAll: false,
    requested: [account({ account_key: 'claude-1', harness: 'claude', label: 'Claude work' })],
    enrollments: [oldAccount, selected],
  })
  assert.deepEqual(rows.map(item => item.key), ['claude-1'])
  const later = ongoingLimitAccounts({
    pending: false,
    selectedKeys: ['claude-1'],
    grantedKeys: ['claude-1'],
    limitsNow: false,
    showAll: true,
    requested: [],
    enrollments: [oldAccount, selected],
  })
  assert.deepEqual(later.map(item => item.key).sort(), ['claude-1', 'cursor-old'])
  const plan = planOngoingLimits({
    choice: 'ongoing_limits',
    drafts: [{ account_key: 'claude-1', starts_at: '2026-09-27T18:00:00Z', ends_at: '2026-09-27T23:00:00Z', unit: 'requests', allowance: 2, pace_model: 'unrestricted', burst_ratio: 0 }],
    selectedAccountKeys: rows.map(item => item.key),
    enrollments: [oldAccount, selected],
    permissions: person,
  })
  assert.equal(plan.action, 'send')
  if (plan.action === 'send') assert.deepEqual(plan.windows.map(item => item.accountId), [ACCOUNT_2])
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
  const quiet = planApproval({ view: unspoken, choice: 'one_per_harness', selectedAccountKeys: ['cursor-1'], permissions: person })
  assert.equal(quiet.ok, false)
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


test('Homebrew commands are additive, bounded and published by this instance', async () => {
  const command = "brew install inspr-at/tap/aeon-agentd\naeon-agentd pair --url 'https://other.example'"
  globalThis.fetch = async () => jsonResponse(guidePayload({ homebrew_command: command }))
  assert.equal(presentPublicGuide(await getPairingGuide()).homebrewCommand, command)
  assert.equal(presentPublicGuide(guidePayload()).homebrewCommand, '')
  for (const bad of [[], 'x'.repeat(4001)]) {
    globalThis.fetch = async () => jsonResponse(guidePayload({ homebrew_command: bad }))
    await assert.rejects(getPairingGuide)
  }
})
