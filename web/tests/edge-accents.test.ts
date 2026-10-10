// SPDX-License-Identifier: AGPL-3.0-only
// Rule 11 (AGENTS.md): no coloured edge accents. Selection, emphasis and state are
// never marked with a coloured bar or thick border on the left or top edge of a
// row, card, callout, toast or panel. This check reads every style in web/src and
// fails on one-sided inset shadows that are thick or coloured, and on left or top
// borders that are thick or coloured. Neutral 1px dividers stay allowed.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = new URL('../src/', import.meta.url).pathname
// Colours that read as a divider, not an accent.
const NEUTRAL = /^(var\(--(line|line-2|border|paper-line|paper-ink|paper-ink-3|glass-edge|lb-edge|chip-line|surface|surface-2)\)|rgba\((32, ?60, ?61|237, ?244, ?240|255, ?255, ?255|0, ?0, ?0),[^)]*\)|transparent|#fff|#ffffff)$/i
// Deliberate exceptions, each with its reason.
const ALLOW: { file: string; includes: string; why: string }[] = [
  { file: 'styles/base.css', includes: '.check-box:checked::after', why: 'the two rotated strokes draw the checkbox glyph; they are not a row or panel edge' },
  { file: 'views/business/QuoteDocumentView.vue', includes: '.doc-totals .grand', why: 'the totals rule on a printed quote, typography in paper ink' },
  // The line a customer signs on in the printed quote: a document rule, not a UI accent.
  { file: 'components/quotes/editor/QuoteAcceptance.vue', includes: '.quote-signatures > div { border-top', why: 'the printed signature line of a quote' },
  { file: 'components/CommandPalette.vue', includes: '.spinner', why: 'a spinner arc, not an edge' },
  { file: 'components/work/TicketTable.vue', includes: '.spinner', why: 'a spinner arc, not an edge' },
  { file: 'components/work/RelationPicker.vue', includes: '.spinner', why: 'a spinner arc, not an edge' },
]

function files(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return name === 'vendor' ? [] : files(path)
    return /\.(vue|css|ts)$/.test(name) ? [path] : []
  })
}
// Split a box-shadow value on commas outside parentheses.
function layers(value: string) {
  const out: string[] = []
  let depth = 0, start = 0
  for (let i = 0; i < value.length; i++) {
    if (value[i] === '(') depth++
    else if (value[i] === ')') depth--
    else if (value[i] === ',' && depth === 0) { out.push(value.slice(start, i).trim()); start = i + 1 }
  }
  out.push(value.slice(start).trim())
  return out
}
const px = (s: string) => Math.abs(parseFloat(s))

// One inset layer that lights only one edge: "inset Xpx 0 0 colour" or "inset 0 Ypx 0 colour".
export function edgeShadow(layer: string): string | null {
  const m = /^inset\s+(-?[\d.]+)(?:px)?\s+(-?[\d.]+)(?:px)?\s+0(?:px)?(?:\s+0(?:px)?)?\s+(.+)$/.exec(layer)
  if (!m) return null
  const [, x, y, colour] = m
  const sideways = px(x) > 0 && px(y) === 0, vertical = px(x) === 0 && px(y) > 0
  if (!sideways && !vertical) return null
  const width = sideways ? px(x) : px(y)
  const white = /^rgba\(255, ?255, ?255/.test(colour)
  if (width >= 2 && !white) return `a ${width}px ${sideways ? 'side' : 'top or bottom'} edge`
  if (!NEUTRAL.test(colour.trim())) return `a coloured ${sideways ? 'side' : 'top or bottom'} edge (${colour.trim()})`
  return null
}
export function edgeBorder(property: string, value: string): string | null {
  if (/-color$/.test(property)) return NEUTRAL.test(value.trim()) ? null : `a coloured ${property}`
  const width = /(-?[\d.]+)px/.exec(value)
  if (width && px(width[1]) >= 2) return `a ${px(width[1])}px ${property}`
  const colour = value.replace(/^[\d.]+px\s+\w+\s*/, '').trim()
  if (colour && /^(solid|dashed|dotted|double)?$/.test(colour) === false && !NEUTRAL.test(colour)) return `a coloured ${property} (${colour})`
  return null
}

test('no coloured or thick left or top edges anywhere in the web app', () => {
  const problems: string[] = []
  for (const path of files(root)) {
    const file = relative(root, path)
    const text = readFileSync(path, 'utf8'), lines = text.split('\n')
    // Declarations may span lines, so match over the whole file and report the starting line.
    const at = (index: number) => text.slice(0, index).split('\n').length
    const allowed = (n: number) => ALLOW.some(a => a.file === file && lines[n - 1].includes(a.includes))
    lines.forEach((line, i) => { if (/--row-accent/.test(line)) problems.push(`${file}:${i + 1} uses --row-accent`) })
    for (const m of text.matchAll(/box-shadow:\s*([^;}]+)/g)) {
      const n = at(m.index!)
      if (allowed(n)) continue
      for (const layer of layers(m[1].replace(/\s+/g, ' '))) { const bad = edgeShadow(layer); if (bad) problems.push(`${file}:${n} ${bad}: ${layer}`) }
    }
    for (const m of text.matchAll(/(border-(?:left|top)(?:-color|-width)?):\s*([^;}]+)/g)) {
      const n = at(m.index!)
      if (allowed(n) || /^(0|none)\b/.test(m[2].trim())) continue
      const bad = edgeBorder(m[1], m[2].replace(/\s+/g, ' ')); if (bad) problems.push(`${file}:${n} ${bad}: ${m[0].replace(/\s+/g, ' ')}`)
    }
  }
  assert.deepEqual(problems, [], `Rule 11: no coloured edge accents.\n${problems.join('\n')}`)
})

test('the check itself catches edge accents and lets rings and dividers through', () => {
  assert.ok(edgeShadow('inset 3px 0 0 var(--teal)'))
  assert.ok(edgeShadow('inset 0 2px 0 var(--teal)'))
  assert.ok(edgeShadow('inset -4px 0 0 #d69b31'))
  assert.ok(edgeShadow('inset 1px 0 0 var(--gold)'))
  assert.equal(edgeShadow('inset 0 0 0 1px var(--chip-teal-line)'), null)
  assert.equal(edgeShadow('inset 1px 0 0 var(--line-2)'), null)
  assert.equal(edgeShadow('inset 0 1px 0 rgba(255, 255, 255, .35)'), null)
  assert.equal(edgeShadow('0 0 0 1px var(--glass-rim)'), null)
  assert.ok(edgeBorder('border-left', '3px solid var(--aqua)'))
  assert.ok(edgeBorder('border-top', '1px solid var(--gold)'))
  assert.ok(edgeBorder('border-left-color', 'var(--teal)'))
  assert.equal(edgeBorder('border-top', '1px solid var(--line)'), null)
  assert.equal(edgeBorder('border-left', '1px solid var(--line-2)'), null)
})
