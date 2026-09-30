// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createScope, scopeOwner, StaleScopeError } from '../src/lib/identityScope.ts'

// A promise the test settles by hand, so "still on its way" is exact.
function later<T = void>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))
function world(first = 't1/ada') {
  let who = first
  const scope = createScope(() => who)
  return { scope, as: (next: string) => { who = next } }
}

test('the owner key is the workspace and the person, empty for nobody', () => {
  assert.equal(scopeOwner({ tenant: { id: 't1' }, principal: { id: 'ada' } }), 't1/ada')
  assert.equal(scopeOwner(null), '')
  assert.equal(scopeOwner(undefined), '')
})

test('a run resumes each step while the person is current and reports its value', async () => {
  const { scope } = world()
  const seen: string[] = []
  const settled: string[] = []
  const out = await scope.run(async ({ step }) => {
    seen.push(await step(Promise.resolve('one')))
    seen.push(await step('two'))
    return seen.length
  }, { settled: () => settled.push('settled') })
  assert.deepEqual(seen, ['one', 'two'])
  assert.equal(out, 2)
  assert.deepEqual(settled, ['settled'])
})

test('nothing starts for nobody', async () => {
  const { scope } = world('')
  let started = false
  assert.equal(await scope.run(async () => { started = true }), undefined)
  assert.equal(started, false)
})

test('a continuation after another person took over never runs, however late the answer comes', async () => {
  const { scope, as } = world()
  const answer = later<string>()
  const reached: string[] = []
  const handled: string[] = []
  const running = scope.run(async ({ step }) => {
    reached.push('before')
    const value = await step(answer.promise)
    reached.push(`after:${value}`)   // A's code opening for B would be this line
  }, { failed: () => handled.push('failed'), settled: () => handled.push('settled') })
  as('t1/grace')
  answer.resolve('ada-only')
  assert.equal(await running, undefined)
  assert.deepEqual(reached, ['before'])
  assert.deepEqual(handled, [], 'neither handler runs for a run that is not current')
})

test('every await is checked, not only the first', async () => {
  const { scope, as } = world()
  const first = later<string>(), second = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ step }) => {
    reached.push(await step(first.promise))
    reached.push(await step(second.promise))
    reached.push('end')
  })
  first.resolve('first')
  await tick()
  as('t2/ada')                      // another workspace, the same person id
  second.resolve('second')
  await running
  assert.deepEqual(reached, ['first'])
})

test('the same person coming back in another generation does not revive a run', async () => {
  const { scope, as } = world()
  const answer = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ step }) => { reached.push(await step(answer.promise)) })
  as(''); scope.reset(); as('t1/ada')   // signed out and back in: a new generation
  answer.resolve('old')
  await running
  assert.deepEqual(reached, [])
})

test('reset aborts the signal of every run and drops their answers', async () => {
  const { scope } = world()
  const signals: AbortSignal[] = []
  const answers = [later<string>(), later<string>()]
  const reached: string[] = []
  const runs = answers.map(answer => scope.run(async ({ step, signal }) => { signals.push(signal); reached.push(await step(answer.promise)) }))
  assert.deepEqual(signals.map(s => s.aborted), [false, false])
  scope.reset()
  assert.deepEqual(signals.map(s => s.aborted), [true, true])
  for (const answer of answers) answer.resolve('late')
  await Promise.all(runs)
  assert.deepEqual(reached, [])
})

test('an aborted request that rejects is dropped quietly, not reported as a failure', async () => {
  const { scope } = world()
  const failures: unknown[] = []
  const running = scope.run(async ({ step, signal }) => {
    await step(new Promise((_, reject) => signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))))
  }, { failed: error => failures.push(error) })
  scope.reset()
  assert.equal(await running, undefined)
  assert.deepEqual(failures, [])
})

test('a lane lets only its newest run answer, and aborts the one it replaces', async () => {
  const { scope } = world()
  const lane = scope.lane()
  const one = later<string>(), two = later<string>()
  const shown: string[] = []
  let firstSignal!: AbortSignal
  const first = lane.run(async ({ step, signal }) => { firstSignal = signal; shown.push(await step(one.promise)) })
  const second = lane.run(async ({ step }) => { shown.push(await step(two.promise)) })
  assert.equal(firstSignal.aborted, true)
  two.resolve('new'); one.resolve('old')
  await Promise.all([first, second])
  assert.deepEqual(shown, ['new'])
})

test('lanes are independent and cancel drops only the one lane', async () => {
  const { scope } = world()
  const a = scope.lane(), b = scope.lane()
  const one = later<string>(), two = later<string>()
  const shown: string[] = []
  const first = a.run(async ({ step }) => { shown.push(await step(one.promise)) })
  const second = b.run(async ({ step }) => { shown.push(await step(two.promise)) })
  a.cancel()
  one.resolve('cancelled'); two.resolve('kept')
  await Promise.all([first, second])
  assert.deepEqual(shown, ['kept'])
})

test('failures reach the handler only while current; without a handler they are thrown', async () => {
  const { scope, as } = world()
  const seen: string[] = []
  await scope.run(async ({ step }) => { await step(Promise.reject(new Error('boom'))) }, { failed: e => seen.push((e as Error).message), settled: () => seen.push('settled') })
  assert.deepEqual(seen, ['boom', 'settled'])
  await assert.rejects(scope.run(async ({ step }) => { await step(Promise.reject(new Error('unhandled'))) }), /unhandled/)
  // A failure that arrives after the person changed is nobody's business.
  const late = later<string>()
  const running = scope.run(async ({ step }) => { await step(late.promise) }, { failed: e => seen.push(`late:${(e as Error).message}`) })
  as('t1/grace')
  late.reject(new Error('too late'))
  assert.equal(await running, undefined)
  assert.deepEqual(seen, ['boom', 'settled'])
})

test('a step that turns stale ends the run without a trace', async () => {
  const { scope, as } = world()
  const answer = later<string>()
  const running = scope.run(async ({ step }) => { await step(answer.promise) })
  as('t1/grace')
  answer.resolve('x')
  assert.equal(await running, undefined)
  assert.ok(new StaleScopeError() instanceof Error && new StaleScopeError().name === 'StaleScopeError')
})

test('a disposed scope starts nothing and drops everything in flight', async () => {
  const { scope } = world()
  const answer = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ step }) => { reached.push(await step(answer.promise)) })
  scope.dispose()
  answer.resolve('late')
  await running
  let started = false
  await scope.run(async () => { started = true })
  assert.deepEqual(reached, [])
  assert.equal(started, false)
})
