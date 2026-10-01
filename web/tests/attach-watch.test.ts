// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { appendWatchText, watchText } from '../src/lib/attachWatch.ts'

test('watch accepts inert text but rejects terminal controls and bidi formatting', () => {
  assert.equal(watchText(JSON.stringify('<script>read me</script>\n\t**text**')), '<script>read me</script>\n\t**text**')
  for (const text of ['\u001b[2J', '\u0000', '\u202eexe.txt', '\u2066hidden']) assert.equal(watchText(JSON.stringify(text)), null)
})
test('watch bounds decoded UTF-8 bytes and rejects non-text envelopes', () => {
  assert.equal(watchText(JSON.stringify('界'.repeat(6000))), null)
  assert.equal(watchText(JSON.stringify('x'.repeat(16_385))), null)
  for (const raw of ['{}', '[]', 'null', '123', '"unterminated', 'x'.repeat(100_001)]) assert.equal(watchText(raw), null)
})
test('watch keeps only the bounded current view', () => {
  const result = appendWatchText('old\n' + 'a'.repeat(65_532), 'new\n')
  assert.equal(result.length, 65_536)
  assert.equal(result.startsWith('old'), false)
  assert.equal(result.endsWith('new\n'), true)
})

test('explicit status-only attachment is distinct from default conversation consent', async () => {
  const { metadataOnlyAttach } = await import('../src/lib/attachWatch.ts')
  assert.equal(metadataOnlyAttach({ mode: 'lease' }), true)
  assert.equal(metadataOnlyAttach({}), false)
  assert.equal(metadataOnlyAttach({ mode: 'unknown' }), false)
})

test('the terminal link fills in nine digits and nothing else', async () => {
  const { attachCodeFromHash, formatAttachCode } = await import('../src/lib/attachWatch.ts')
  assert.equal(attachCodeFromHash('#attach=123456789'), '123456789')
  assert.equal(formatAttachCode('123456789'), '123 456 789')
  for (const hash of ['', '#', '#attach=', '#attach=12345678', '#attach=1234567890', '#attach=12345678a', '#attach=123456789&x=1', '#attach=123456789/', '#x=123456789', '#attach=%31%32%33%34%35%36%37%38%39', '?attach=123456789', '#attach=123 456 789', '#attach=１２３４５６７８９', '#attach=<img src=x>']) {
    assert.equal(attachCodeFromHash(hash), null, hash)
  }
})

test('a waiting request reads as waiting, approved, expired or cancelled', async () => {
  const { attachOutcome } = await import('../src/lib/attachWatch.ts')
  const now = Date.parse('2026-09-30T12:00:00Z')
  const soon = '2026-09-30T12:05:00Z', past = '2026-09-30T11:59:59Z'
  assert.equal(attachOutcome({ state: 'pending', expires_at: soon }, now), 'waiting')
  assert.equal(attachOutcome({ state: 'approved', expires_at: soon }, now), 'approved')
  // The client clock may pass the expiry before the next poll says so.
  assert.equal(attachOutcome({ state: 'pending', expires_at: past }, now), 'expired')
  assert.equal(attachOutcome({ state: 'approved', expires_at: past }, now), 'expired')
  assert.equal(attachOutcome({ state: 'unreachable', expires_at: soon }, now), 'expired')
  assert.equal(attachOutcome({ state: 'detached', expires_at: soon }, now), 'cancelled')
  // A watch that became a session is a session, never a pending request.
  for (const state of ['active', 'confirmed_exited'] as const) assert.equal(attachOutcome({ state, expires_at: soon }, now), null)
})

test('listing pending attaches is a plain same-origin read', async () => {
  const { listPendingAttach } = await import('../src/lib/attachWatch.ts')
  const original = globalThis.fetch
  try {
    const calls: { url: string; method?: string; body?: unknown }[] = []
    globalThis.fetch = async (url, init) => {
      calls.push({ url: String(url), method: init?.method, body: init?.body })
      return new Response(JSON.stringify({ requests: [{ request_id: 'r1', state: 'pending' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    const found = await listPendingAttach()
    assert.equal(found[0]?.request_id, 'r1')
    assert.deepEqual(calls, [{ url: '/api/agent-pairing/attach/pending', method: undefined, body: undefined }])
    globalThis.fetch = async () => new Response(JSON.stringify({}), { status: 200 })
    assert.deepEqual(await listPendingAttach(), [])
    globalThis.fetch = async () => new Response('{}', { status: 403 })
    await assert.rejects(listPendingAttach())
  } finally { globalThis.fetch = original }
})

test('an attach fragment never survives into a return path, and a held code is taken once', async () => {
  const link = await import('../src/lib/attachLink.ts')
  const { safeReturnPath, expiredSignIn } = await import('../src/lib/signInReturn.ts')
  assert.equal(link.stripAttachCode('/agents#attach=123456789'), '/agents')
  assert.equal(link.stripAttachCode('/agents?x=1#attach=123456789'), '/agents?x=1')
  assert.equal(link.stripAttachCode('/agents#attach=123456789&y'), '/agents')
  assert.equal(link.stripAttachCode('/agents#other'), '/agents#other')
  for (const path of ['/agents#attach=123456789', '/agents/s1?x=1#attach=123456789']) assert.ok(!/123456789/.test(safeReturnPath(path)), path)
  assert.equal(safeReturnPath('/agents#attach=123456789'), '/agents')
  assert.equal(safeReturnPath('//evil.example/#attach=123456789'), '/')
  assert.deepEqual(expiredSignIn('/agents#attach=123456789'), { path: '/signin', query: { error: 'expired', return: '/agents' } })

  assert.equal(link.hasAttachFragment('#attach=1'), true)
  assert.equal(link.hasAttachFragment('#attachment'), false)
  const heard: string[] = []
  const stop = link.onAttachCode(() => heard.push('heard'))
  link.announceAttachCode()
  assert.deepEqual(heard, [], 'nothing held, nobody is told')
  link.holdAttachCode('123456789', 't1/ada')
  assert.deepEqual(heard, [], 'holding is silent until the navigation has settled')
  link.announceAttachCode()
  assert.deepEqual(heard, ['heard'])
  assert.equal(link.takeAttachCode('t1/ada'), '123456789')
  assert.equal(link.takeAttachCode('t1/ada'), null, 'one code opens one review')
  link.holdAttachCode('987654321', 't1/ada'); link.dropAttachCode()
  assert.equal(link.takeAttachCode('t1/ada'), null, 'a code held for a session that ended is gone')
  stop()
  link.holdAttachCode('111111111', 't1/ada'); link.announceAttachCode()
  assert.deepEqual(heard, ['heard'])
  link.dropAttachCode()
})

test('a held attach code stays with the person it arrived for', async () => {
  const link = await import('../src/lib/attachLink.ts')
  // Held while one person was signed in: another person, another workspace or nobody never gets it, and it is gone afterwards.
  for (const other of ['t1/grace', 't2/ada', '']) {
    link.holdAttachCode('123456789', 't1/ada')
    assert.equal(link.takeAttachCode(other), null, `not for ${other || 'nobody'}`)
    assert.equal(link.takeAttachCode('t1/ada'), null, 'and the refusal consumed it')
  }
  // Held before anybody was signed in (a fresh tab): it goes to whoever that tab signs in, once.
  link.holdAttachCode('123456789', '')
  assert.equal(link.takeAttachCode(''), null, 'nobody is signed in yet, so nothing is handed out')
  link.holdAttachCode('123456789', '')
  assert.equal(link.takeAttachCode('t2/grace'), '123456789')
  assert.equal(link.takeAttachCode('t2/grace'), null)
})
