// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'
import { APIError, RequestFailure, sessionEnded } from '../src/lib/api.ts'
import type { AllowanceWrite } from '../src/lib/agents.ts'
import {
  ABSENT_ALLOWANCE, ENDED_WINDOW, IMPOSSIBLE_DATE, INVALID_ALLOWANCE, NORMALISED_DATE, UNCERTAIN_ALLOWANCE,
  allowanceFailure, beginSave, emptyAllowanceDraft, findSavedAllowance, instantFromCivil, parseCivilTime,
  reviewAllowance, settleSave, windowsOverlap, type CivilTime,
} from '../src/components/agents/allowanceWindow.ts'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch; sessionEnded.blocked = false })

function utcDate(civil: CivilTime): Date {
  return new Date(Date.UTC(civil.year, civil.month - 1, civil.day, civil.hour, civil.minute, civil.second))
}
function utcRead(date: Date): CivilTime {
  return {
    year: date.getUTCFullYear(), month: date.getUTCMonth() + 1, day: date.getUTCDate(),
    hour: date.getUTCHours(), minute: date.getUTCMinutes(), second: date.getUTCSeconds(),
  }
}
const now = Date.parse('2026-09-28T12:00:00.000Z')
function draft(patch: Partial<ReturnType<typeof emptyAllowanceDraft>> = {}) {
  return {
    ...emptyAllowanceDraft(),
    startsLocal: '2026-09-28T13:00',
    endsLocal: '2026-09-28T18:00',
    allowance: 1,
    ...patch,
  }
}
function review(patch: Partial<ReturnType<typeof emptyAllowanceDraft>> = {}, windows: { starts_at: string; ends_at: string; unit: string }[] = [], at = now) {
  return reviewAllowance(draft(patch), windows, at, utcDate, utcRead)
}

test('impossible and normalised local times are rejected before a request exists', () => {
  for (const value of ['2026-02-31T12:00', '2026-04-31T00:00', '2026-02-29T12:00', '2026-13-01T00:00', '2026-01-01T24:00', '2026-01-01T12:60', '2026-01-01T12:00Z', '2026-01-01 12:00']) {
    const parsed = parseCivilTime(value)
    assert.equal(parsed.ok, false)
    if (!parsed.ok) assert.equal(parsed.message, IMPOSSIBLE_DATE)
  }
  assert.equal(parseCivilTime('2028-02-29T12:00').ok, true)
  const shifted = (civil: CivilTime) => utcDate({ ...civil, day: civil.day + 1 })
  const normalised = instantFromCivil({ year: 2026, month: 2, day: 28, hour: 12, minute: 0, second: 0 }, shifted, utcRead)
  assert.equal(normalised.ok, false)
  if (!normalised.ok) assert.equal(normalised.message, NORMALISED_DATE)
  const bad = review({ startsLocal: '2026-02-31T12:00' })
  assert.equal(bad.ok, false)
  if (!bad.ok) assert.equal(bad.message, IMPOSSIBLE_DATE)
})

test('a positive safe allowance, unit, pace and burst are required, and the saved instants stay exact', () => {
  assert.equal(review({ allowance: 1.5 }).ok, false)
  assert.equal(review({ allowance: 0 }).ok, false)
  assert.equal(review({ allowance: Number.MAX_SAFE_INTEGER + 2 }).ok, false)
  assert.equal(review({ burst: 1.00001 }).ok, false)
  assert.equal(review({ burst: -0.1 }).ok, false)
  assert.equal(review({ unit: 'quota' as 'requests' }).ok, false)
  assert.equal(review({ pace: 'vendor' as 'steady' }).ok, false)
  const ok = review()
  assert.equal(ok.ok, true)
  if (!ok.ok) return
  assert.equal(ok.startsAt, '2026-09-28T13:00:00.000Z')
  assert.equal(ok.endsAt, '2026-09-28T18:00:00.000Z')
  assert.equal(ok.body.allowance, 1)
  assert.equal(ok.body.burst_ratio, 0)
  assert.equal(ok.startsInPast, false)
  assert.equal(JSON.stringify(ok.body).includes('quota'), false)
})

test('an ended window is refused locally and a past start is kept, without moving either', () => {
  const ended = review({}, [], Date.parse('2026-09-28T19:00:00.000Z'))
  assert.equal(ended.ok, false)
  if (!ended.ok) assert.equal(ended.message, ENDED_WINDOW)
  const started = review({}, [], Date.parse('2026-09-28T14:00:00.000Z'))
  assert.equal(started.ok, true)
  if (!started.ok) return
  assert.equal(started.startsInPast, true)
  assert.equal(started.startsAt, '2026-09-28T13:00:00.000Z')
})

test('overlap is reported and does not rewrite the span; the server still receives it', () => {
  const existing = { starts_at: '2026-09-28T12:00:00.000Z', ends_at: '2026-09-28T16:00:00.000Z', unit: 'requests' }
  assert.equal(windowsOverlap([existing], 'requests', Date.parse('2026-09-28T16:00:00.000Z'), Date.parse('2026-09-28T17:00:00.000Z')), false)
  const hit = review({}, [existing])
  assert.equal(hit.ok, true)
  if (!hit.ok) return
  assert.equal(hit.overlap, true)
  assert.equal(hit.startsAt, '2026-09-28T13:00:00.000Z')
  const otherUnit = review({ unit: 'tokens' }, [existing])
  assert.equal(otherUnit.ok, true)
  if (otherUnit.ok) assert.equal(otherUnit.overlap, false)
})

test('a busy or switched account does not send, and a finished save does not land on another account', () => {
  const ok = review()
  assert.equal(ok.ok, true)
  if (!ok.ok) return
  assert.equal(beginSave({ busy: true, editingId: 'a', accountId: 'a', generation: 1, body: ok.body, now }).action, 'ignore')
  assert.equal(beginSave({ busy: false, editingId: 'b', accountId: 'a', generation: 1, body: ok.body, now }).action, 'ignore')
  const send = beginSave({ busy: false, editingId: 'a', accountId: 'a', generation: 2, body: ok.body, now })
  assert.equal(send.action, 'send')
  const stale = settleSave({
    outcome: 'saved', message: '', accountName: 'Pi on hsb1', startedAccountId: 'a', startedGeneration: 2, currentAccountId: 'b', currentGeneration: 3,
  })
  assert.equal(stale.closeForm, false)
  assert.equal(stale.refresh, true)
  assert.equal(stale.formMessage, null)
  assert.match(stale.status ?? '', /Pi on hsb1/)
  const lost = settleSave({
    outcome: 'uncertain', message: UNCERTAIN_ALLOWANCE, accountName: 'Pi on hsb1', startedAccountId: 'a', startedGeneration: 2, currentAccountId: 'b', currentGeneration: 3,
  })
  assert.equal(lost.refresh, false)
  assert.equal(lost.uncertain, false)
  assert.equal(lost.formMessage, null)
  assert.match(lost.status ?? '', /not sent again/)
  const same = settleSave({
    outcome: 'uncertain', message: UNCERTAIN_ALLOWANCE, accountName: 'Pi on hsb1', startedAccountId: 'a', startedGeneration: 2, currentAccountId: 'a', currentGeneration: 2,
  })
  assert.equal(same.uncertain, true)
  assert.equal(same.formMessage, UNCERTAIN_ALLOWANCE)
  assert.equal(same.formMessage?.includes('Set allowance again'), false)
})

test('authorization and overlap stay server errors, and a lost response is not a silent retry', () => {
  assert.equal(allowanceFailure(new APIError(403, 'permission denied')).kind, 'forbidden')
  assert.equal(allowanceFailure(new APIError(409, 'allowance windows overlap')).message, 'allowance windows overlap')
  assert.equal(allowanceFailure(new APIError(503, 'gateway')).message, UNCERTAIN_ALLOWANCE)
  assert.equal(allowanceFailure(new RequestFailure('network')).kind, 'uncertain')
  assert.equal(allowanceFailure(new TypeError('failed')).message, UNCERTAIN_ALLOWANCE)
  const rejected = beginSave({
    busy: false, editingId: 'a', accountId: 'a', generation: 1,
    body: { ...draftBody(), allowance: 1.2 },
    now,
  })
  assert.equal(rejected.action, 'invalid')
  if (rejected.action === 'invalid') assert.equal(rejected.message, INVALID_ALLOWANCE)
})

function draftBody(): AllowanceWrite {
  return { starts_at: '2026-09-28T13:00:00.000Z', ends_at: '2026-09-28T18:00:00.000Z', unit: 'requests', allowance: 1, pace_model: 'unrestricted', burst_ratio: 0 }
}

test('checking a lost allowance reads the account and does not post', async () => {
  const body = draftBody()
  const calls: { method: string; url: string }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({ method: init?.method ?? 'GET', url: String(url) })
    if (String(url).endsWith('/windows')) return new Response(JSON.stringify({ error: 'no' }), { status: 500 })
    return new Response(JSON.stringify([{ id: 'account', windows: [] }]), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  assert.equal(await findSavedAllowance('account', body), 'absent')
  globalThis.fetch = async (url, init) => {
    calls.push({ method: init?.method ?? 'GET', url: String(url) })
    return new Response(JSON.stringify([{ id: 'account', windows: [{ id: 'w', account_id: 'account', used: 0, reserved: 0, ...body }] }]), { status: 200 })
  }
  assert.equal(await findSavedAllowance('account', body), 'saved')
  globalThis.fetch = async () => { throw new TypeError('down') }
  assert.equal(await findSavedAllowance('account', body), 'unknown')
  assert.equal(calls.some(call => call.method === 'POST'), false)
  assert.equal(ABSENT_ALLOWANCE, 'This allowance is not on the account. You can send it again.')
})
