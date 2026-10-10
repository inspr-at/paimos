// SPDX-License-Identifier: AGPL-3.0-only
// Evaluate registration/parameter loops, never test bodies or lifecycle hooks.
const suites = []
globalThis.__tierCases = []
export function test(name, ...args) {
  if (typeof name !== 'string') throw new Error('Tier collection requires named tests')
  const location = new Error().stack.split('\n').find(line => /\/web\/tests\/.*\.test\.ts:/.test(line))
  const line = Number(location?.match(/:(\d+):\d+\)?$/)?.[1])
  globalThis.__tierCases.push({ name: [...suites, name].join(' '), leaf: name, line })
}
export const it = test
export function describe(name, ...args) {
  suites.push(name)
  const fn = args.at(-1)
  if (typeof fn !== 'function') throw new Error('Suite requires a callback')
  try {
    const result=fn()
    if(result&&typeof result.then==='function')throw new Error('Async registration suites require explicit collector support')
  } finally { suites.pop() }
}
for (const fn of [test, describe]) {
  fn.skip = fn
  fn.todo = fn
  fn.only = () => { throw new Error('Focused tests are forbidden') }
}
export const before = () => {}, after = () => {}, beforeEach = () => {}, afterEach = () => {}
export default test
