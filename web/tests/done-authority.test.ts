// SPDX-License-Identifier: AGPL-3.0-only
// AEON-437: the server is the only authority for Done. `finished` is a required
// boolean in every session, live and ticket payload, derived in SQL
// (aeon_session_finished); the web reads it and never derives Done from the
// percent or the stop reason. This guard reads the sources, so a second
// derivation, a fallback or an optional payload flag fails here before a screen
// can disagree with another.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const read = (file: string) => readFileSync(new URL(`../src/${file}`, import.meta.url), 'utf8')
// Comments explain the rule and may name what it forbids; only code is checked.
const code = (file: string) => read(file).replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1')
const block = (file: string, declaration: string) => {
  const match = code(file).match(new RegExp(`${declaration} \\{[\\s\\S]*?\\n\\}`))
  assert.ok(match, `${declaration} not found in ${file}`)
  return match[0]
}

const DONE_FILES = ['lib/agentSignals.ts', 'lib/agentState.ts', 'lib/liveAgents.ts', 'lib/eta.ts', 'lib/agentAppearance.ts', 'components/agents/activity.ts']

test('nothing in the web reads the clean-exit reason to decide a finish', () => {
  for (const file of DONE_FILES) {
    assert.doesNotMatch(code(file), /process_exited|CLEAN_EXIT|finishedStop/, `${file} derives a finish from the exit reason`)
  }
})

test('the agent state is Done only through the server finished flag', () => {
  const signals = code('lib/agentSignals.ts')
  const results = signals.match(/result\(\s*'done'/g) ?? []
  assert.equal(results.length, 1, 'exactly one place may produce the done state')
  assert.match(signals, /if \(evidence\.finished\) return result\('done'\)/)
  for (const file of DONE_FILES) {
    assert.doesNotMatch(code(file), /(return|state\s*[:=])\s*'done'/, `${file} assigns the done state itself`)
  }
  // The stopped branch in assessAgentState is the only place that may name a stop reason for a label.
  const stopped = signals.slice(signals.indexOf("evidence.phase === 'stopped'"), signals.indexOf('const heartbeat = heartbeatEvidence'))
  assert.doesNotMatch(stopped, /progress_pct|\b100\b/, 'the stopped branch reads the percent')
})

test('the ticket ETA reads Done only from the server finished flag', () => {
  const eta = code('lib/eta.ts')
  const done = [...eta.matchAll(/\bdone\s*:\s*([^,\n}]+)/g)].map(match => match[1]!.trim())
  assert.deepEqual(done, ['boolean', '!!input.finished'], 'the done field is the server flag, never a percent')
  assert.doesNotMatch(eta, /finished\s*=\s*[^;\n]*(pct|progress)/, 'finished is never computed from the percent')
})

test('no fallback stands behind the finished flag', () => {
  for (const file of ['lib/agentSignals.ts', 'lib/agentState.ts', 'lib/liveAgents.ts', 'lib/eta.ts', 'lib/agents.ts']) {
    assert.doesNotMatch(code(file), /\bfinished\s*(\?\?|\|\|)|\?\?\s*[\w.!()]*\bfinished\b/, `${file} falls back from the finished flag`)
  }
})

test('the finished flag is a required boolean in every payload type a screen renders', () => {
  for (const [file, declaration] of [
    ['lib/agents.ts', 'export interface HarnessSession'],
    ['lib/liveAgents.ts', 'export interface LiveAgent'],
    ['lib/agentSignals.ts', 'export interface StateEvidence'],
    ['lib/eta.ts', 'export interface TicketEta'],
  ] as const) {
    const type = block(file, declaration)
    assert.match(type, /\bfinished: boolean\b/, `${declaration} must require finished`)
    assert.doesNotMatch(type, /\bfinished\?/, `${declaration} must not make finished optional`)
  }
})
