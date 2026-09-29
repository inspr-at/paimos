// SPDX-License-Identifier: AGPL-3.0-only
// AEON-340: Blocked and New must stay apart in both dark themes. The pair
// #e7a36a / #e2b45a was close enough to read as one colour.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const css = readFileSync(new URL('../src/styles/tokens.css', import.meta.url), 'utf8')

function oklab(hex: string): [number, number, number] {
  const channel = (value: number) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  const [r, g, b] = [0, 2, 4].map(i => channel(parseInt(hex.slice(1 + i, 3 + i), 16) / 255))
  const l = Math.cbrt(0.4122214708 * r! + 0.5363325363 * g! + 0.0514459929 * b!)
  const m = Math.cbrt(0.2119034982 * r! + 0.6806995451 * g! + 0.1073969566 * b!)
  const s = Math.cbrt(0.0883024619 * r! + 0.2817188376 * g! + 0.6299787005 * b!)
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ]
}

const apart = (a: string, b: string) => {
  const [p, q] = [oklab(a), oklab(b)]
  return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]) * 100
}

test('both dark themes use the same Blocked copper, apart from New', () => {
  const pairs = [...css.matchAll(/--st-new:\s*(#[0-9a-f]{6}); --st-backlog:[^;]+; --st-blocked:\s*(#[0-9a-f]{6})/g)]
  assert.equal(pairs.length, 2)
  for (const [, neu, blocked] of pairs) {
    assert.equal(neu, '#e2b45a')
    assert.equal(blocked, '#ee6e45')
    assert.ok(apart(neu!, blocked!) >= 10, `distance ${apart(neu!, blocked!).toFixed(1)}`)
  }
  assert.ok(apart('#e2b45a', '#e7a36a') < 8)
})
