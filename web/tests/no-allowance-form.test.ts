// SPDX-License-Identifier: AGPL-3.0-only
// AEON-384 acceptance: no UI string asks for a start, end, unit, pace, burst or
// micros. Limits are observed from the vendor; a limit by hand is one sentence
// ("Let agents use at most 20% of this account per day"). This scans web/src for
// what a form asks: labels, legends, placeholders, the accessible names of
// inputs, selects and textareas, and option labels, plus any UI text that
// mentions a burst or micros at all. Other domains that use these words for
// something else are listed with the reason they may.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const SRC = new URL('../src/', import.meta.url).pathname
const ASKS = /\b(starts?|ends?|units?|pace|pacing|burst|micros)\b/i
const NEVER = /\b(burst|micros)\b/i
// Other domains that use these words for something else. Each is not an
// account, an allowance or a limit, so it may keep asking.
const ELSEWHERE: [RegExp, string][] = [
  [/^(components|views)\/business\//, 'a cost unit is the business ledger\'s cost centre for time and quotes'],
  [/^components\/quotes\//, 'quote documents number lists and expire links'],
  [/^lib\/ticketList\.ts$/, 'a ticket has start and end dates'],
  [/^components\/agents\/ScheduleEditor\.vue$/, 'the approved work-week editor: when nights start and end, and their reduced rate'],
]

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sources(path)
    return /\.(vue|ts)$/.test(name) ? [path] : []
  })
}
// Comments explain code; they are not UI.
const uncomment = (text: string) => text.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
const plain = (html: string) => html.replace(/\{\{[\s\S]*?\}\}/g, ' ').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim()
const template = (vue: string) => /<template>([\s\S]*)<\/template>/.exec(vue)?.[1] ?? ''

/** What a form asks: every prompt a person reads before typing or choosing. */
export function prompts(file: string, text: string): string[] {
  const out: string[] = []
  const code = uncomment(text)
  if (file.endsWith('.vue')) {
    const t = template(code)
    for (const m of t.matchAll(/<(label|legend|option)\b[^>]*>([\s\S]*?)<\/\1>/g)) out.push(plain(m[2]))
    for (const m of t.matchAll(/\splaceholder="([^"]*)"/g)) out.push(m[1])
    for (const m of t.matchAll(/<(input|select|textarea)\b([^>]*)>/g)) {
      for (const a of m[2].matchAll(/:?aria-label="([^"]*)"/g)) out.push(a[1].replace(/\$\{[^}]*\}/g, ' '))
    }
  }
  // Option lists built in code: { value: …, label: 'Tokens' }.
  for (const m of code.matchAll(/\bvalue:\s*[^,}]+,\s*label:\s*(['"`])([^'"`]*)\1/g)) out.push(m[2])
  return out.filter(Boolean)
}
/** UI text that may mention anything: template text and string literals. */
export function uiText(file: string, text: string): string[] {
  const code = uncomment(text)
  const out = file.endsWith('.vue') ? [plain(template(code))] : []
  for (const m of code.matchAll(/(['"`])((?:\\.|(?!\1)[^\\\n])*)\1/g)) out.push(m[2].replace(/\$\{[^}]*\}/g, ' '))
  return out
}

test('the scan finds a form that asks for them', () => {
  const form = `<template><label>Starts <input v-model="a"></label><select aria-label="Unit"><option value="cost_micros">Cost in micros</option></select>
    <input placeholder="Burst ratio"></template>`
  assert.deepEqual(prompts('x.vue', form).filter(p => ASKS.test(p)), ['Starts', 'Cost in micros', 'Burst ratio', 'Unit'])
  assert.deepEqual(prompts('x.ts', `const PACE = [{ value: 'steady', label: 'Steady pace' }]`), ['Steady pace'])
  assert.equal(uiText('x.ts', `const u = 'cost_micros'; const b = w.burst_ratio`).some(s => NEVER.test(s)), false)
  assert.equal(uiText('x.ts', `toast('Burst allowed')`).some(s => NEVER.test(s)), true)
  assert.equal(uiText('x.ts', 'const c = `$${micros / 1e6}`').some(s => NEVER.test(s)), false)
})

test('no UI string in web/src asks for a start, end, unit, pace, burst or micros', () => {
  const asked: string[] = []
  for (const path of sources(SRC)) {
    const text = readFileSync(path, 'utf8')
    const file = relative(SRC, path)
    if (ELSEWHERE.some(([where]) => where.test(file))) continue
    for (const p of prompts(path, text)) {
      // AEON-500's exact approved switch label describes test coverage; it
      // does not ask for an allowance end date. Keep the exception this narrow.
      if (file === 'components/settings/DeveloperSection.vue' && p === 'Show the flow controls (not yet tested end to end)') continue
      if (ASKS.test(p)) asked.push(`${file}: asks "${p}"`)
    }
    for (const s of uiText(path, text)) if (NEVER.test(s)) asked.push(`${file}: says "${s.slice(0, 80)}"`)
  }
  assert.deepEqual(asked, [])
})
