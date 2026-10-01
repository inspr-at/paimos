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

test('the liveness check and callback share a turn even when reset is queued during the check', async () => {
  let who = 't1/ada', probe = false
  const opened: string[] = []
  const scope = createScope(() => {
    const observed = who
    if (probe) {
      probe = false
      queueMicrotask(() => { who = 't1/grace'; scope.reset() })
    }
    return observed
  })
  const answer = later<string>()
  const running = scope.run(({ after }) => after(answer.promise, code => { opened.push(`${who}:${code}`) }))
  probe = true
  answer.resolve('ada-only')
  await running
  assert.deepEqual(opened, ['t1/ada:ada-only'], 'Ada’s answer can act only for Ada, never Grace')
})

test('a second stage is checked after a reset queued by the first stage', async () => {
  const { scope } = world()
  const shown: string[] = []
  await scope.run(({ after }) => after(Promise.resolve(), () => {
    queueMicrotask(() => scope.reset())
    return after(Promise.resolve('old code'), code => { shown.push(code) })
  }))
  assert.deepEqual(shown, [])
})

test('a failure handler that resets the scope prevents the settled handler', async () => {
  const { scope } = world()
  let settled = false
  await scope.run(({ after }) => after(Promise.reject(new Error('failed')), () => {}), {
    failed: () => scope.reset(), settled: () => { settled = true },
  })
  assert.equal(settled, false)
})
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
  const out = await scope.run(async ({ after }) => {
    await after(Promise.resolve('one'), value => { seen.push(value) })
    await after('two', value => { seen.push(value) })
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
  const running = scope.run(async ({ after }) => {
    reached.push('before')
    await after(answer.promise, value => { reached.push(`after:${value}`) })   // A's code opening for B would be this line
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
  const running = scope.run(async ({ after }) => {
    await after(first.promise, value => { reached.push(value) })
    await after(second.promise, value => { reached.push(value) })
    await after(undefined, () => { reached.push('end') })
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
  const running = scope.run(async ({ after }) => { await after(answer.promise, value => { reached.push(value) }) })
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
  const runs = answers.map(answer => scope.run(async ({ after, signal }) => { signals.push(signal); await after(answer.promise, value => { reached.push(value) }) }))
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
  const running = scope.run(async ({ after, signal }) => {
    await after(new Promise((_, reject) => signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))), () => {})
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
  const first = lane.run(async ({ after, signal }) => { firstSignal = signal; await after(one.promise, value => { shown.push(value) }) })
  const second = lane.run(async ({ after }) => { await after(two.promise, value => { shown.push(value) }) })
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
  const first = a.run(async ({ after }) => { await after(one.promise, value => { shown.push(value) }) })
  const second = b.run(async ({ after }) => { await after(two.promise, value => { shown.push(value) }) })
  a.cancel()
  one.resolve('cancelled'); two.resolve('kept')
  await Promise.all([first, second])
  assert.deepEqual(shown, ['kept'])
})

test('failures reach the handler only while current; without a handler they are thrown', async () => {
  const { scope, as } = world()
  const seen: string[] = []
  await scope.run(async ({ after }) => { await after(Promise.reject(new Error('boom')), () => {}) }, { failed: e => seen.push((e as Error).message), settled: () => seen.push('settled') })
  assert.deepEqual(seen, ['boom', 'settled'])
  await assert.rejects(scope.run(async ({ after }) => { await after(Promise.reject(new Error('unhandled')), () => {}) }), /unhandled/)
  // A failure that arrives after the person changed is nobody's business.
  const late = later<string>()
  const running = scope.run(async ({ after }) => { await after(late.promise, () => {}) }, { failed: e => seen.push(`late:${(e as Error).message}`) })
  as('t1/grace')
  late.reject(new Error('too late'))
  assert.equal(await running, undefined)
  assert.deepEqual(seen, ['boom', 'settled'])
})

test('a step that turns stale ends the run without a trace', async () => {
  const { scope, as } = world()
  const answer = later<string>()
  const running = scope.run(async ({ after }) => { await after(answer.promise, () => {}) })
  as('t1/grace')
  answer.resolve('x')
  assert.equal(await running, undefined)
  assert.ok(new StaleScopeError() instanceof Error && new StaleScopeError().name === 'StaleScopeError')
})

test('a disposed scope starts nothing and drops everything in flight', async () => {
  const { scope } = world()
  const answer = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ after }) => { await after(answer.promise, value => { reached.push(value) }) })
  scope.dispose()
  answer.resolve('late')
  await running
  let started = false
  await scope.run(async () => { started = true })
  assert.deepEqual(reached, [])
  assert.equal(started, false)
})
