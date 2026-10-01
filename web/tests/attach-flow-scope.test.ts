// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// AEON-440: one structural rule for the attach flow. Every async path answers to the
// identity scope (lib/identityScope.ts): it re-checks the person, the workspace and the
// generation in the same turn as each callback, so one person's request can never act
// for the next. A component that awaits on its own would quietly break that, so the
// flow's sources may not await: every async stage uses after(), nothing chains
// promises, and no timer or controller lives outside the scope.
const FLOW = ['AttachPending.vue', 'AttachApproval.vue'].map(name => `../src/components/agents/${name}`)
const script = (path: string) => {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  return source.slice(source.indexOf('<script'), source.indexOf('</script>'))
}
const code = (text: string) => text.split('\n').map(line => line.replace(/(^|\s)\/\/.*$/, '')).join('\n')

for (const path of FLOW) {
  const name = path.split('/').pop()
  test(`${name} continues only through synchronous identity callbacks`, () => {
    const text = code(script(path))
    assert.match(text, /useIdentityScope\(/, 'the component runs in an identity scope')
    assert.match(text, /\bafter\(/, 'async stages use guarded callbacks')
    for (const forbidden of [/\bawait\b/, /\basync\b/, /\bstep\(/, /\.then\(/, /\.catch\(/, /\.finally\(/, /new AbortController/, /\bsetTimeout\(/, /\bsetInterval\(/, /\bqueueMicrotask\(/]) {
      assert.doesNotMatch(text, forbidden, `outside the identity scope: ${forbidden}`)
    }
  })
}

test('the router holds an attach code for the person it arrived for, and permissions are awaited inside the scope', () => {
  const router = code(readFileSync(new URL('../src/router.ts', import.meta.url), 'utf8'))
  assert.match(router, /holdAttachCode\(code, scopeOwner\(/)
  const approval = code(script('../src/components/agents/AttachApproval.vue'))
  assert.match(approval, /takeAttachCode\(link\.owner\.value\)/, 'a link is taken for the current owner')
  assert.match(approval, /after\(ensurePermissions\(\),/, 'permissions resume in a guarded callback')
})
