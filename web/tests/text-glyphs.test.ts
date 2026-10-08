// SPDX-License-Identifier: AGPL-3.0-only
// UG1 (AEON-109): icons are SVG from the app's set (AppIcon, BizIcon, QuoteIcon),
// optically centred in their circle, button or box; never text glyphs such as
// ‹ › ✕ × ▶ ▸ ↑ ↓ ⌘ ↵. This reads every file in web/src (comments aside) and
// fails on such a glyph, on its escape (▶, CSS '\25B6', &rarr;), and on a
// <summary> that would show the browser's text triangle. Document and print
// content keeps its own characters through the allow-list below, each with its
// reason; an entry that no longer matches anything fails too, so it stays tight.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = new URL('../src/', import.meta.url).pathname
// Arrows, chevrons and triangles, crosses and ticks, key symbols, bullets, stars,
// hamburgers and gears: the characters that stand in for an icon.
export const GLYPH = /[‹›«»✕✖✗✘×▶▸►▷▹◀◂◄◁◃▲▴△▵▼▾▽▿↑↓←→↔↕↖↗↘↙↩↪↰↱↲↳↵⏎⌘⌥⇧⇪⌃⌫⌦⎋⇥⇤✓✔☐☑☒•◦▪▫●○◉◎■□★☆⚙✎✏✚➜➔➝➞➟➠➡⟵⟶⟷⋯⋮☰≡]/u
const ENTITY = /&(larr|rarr|uarr|darr|harr|crarr|times|lsaquo|rsaquo|laquo|raquo|bull|check|cross|star|starf|hellip|#x?[0-9a-f]+);/i
// Deliberate exceptions, each with its reason.
const ALLOW: { file: string; includes: string; why: string }[] = [
  { file: 'lib/serviceTier.ts', includes: 'TIER_NAME[s.tier]} ×${s.price_multiplier}', why: 'frozen numerical price multiplier in approved AEON-436 fragment, not an icon (AEON-612)' },
  { file: 'components/agents/SessionMessages.vue', includes: 'identical posts`">×{{ m.count }}', why: 'multiplicity of identical messages (AEON-267), numerical text, not an icon' },
  { file: 'lib/quotes/prose.ts', includes: 'export const BULLETS', why: 'the bullets a quote prints in its document' },
  { file: 'components/business/QuoteLines.vue', includes: 'class="rate-hint">× ', why: 'rate × quantity, a multiplication in text' },
  { file: 'components/work/AttachmentLightbox.vue', includes: '.width} × ${', why: 'image dimensions, 1200 × 800' },
  { file: 'lib/releaseStats.ts', includes: '${rate(r)}×`', why: 'a multiple in text, "3× the median gap" (AEON-488)' },
  { file: 'lib/deliveryNumbersText.ts', includes: "const TO = '→'", why: 'approved AEON-994 Delivery copy uses the arrow as the word "to" (run start → run end, PR opened → merged); text, not an icon' },
  { file: 'lib/workKindsCopy.ts', includes: 'Settings › Models', why: 'approved AEON-854 breadcrumb in the kinds lead; it names the Models board in a sentence and is not an icon' },
]

function files(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return name === 'vendor' ? [] : files(path)
    return /\.(vue|css|ts|js)$/.test(name) ? [path] : []
  })
}
// Comments explain; they are never shown. Blank them out, keeping line numbers.
export function uncommented(source: string) {
  const blank = (text: string) => text.replace(/[^\n]/g, ' ')
  return source
    .replace(/<!--[\s\S]*?-->/g, blank)
    .replace(/\/\*[\s\S]*?\*\//g, blank)
    .replace(/(^|[\s;{}(,])\/\/[^\n]*/g, (match, lead: string) => lead + blank(match.slice(lead.length)))
}
// Escaped code points: ▶ and \u{25B6} in script, '\25B6' in CSS content.
export function escapedGlyphs(line: string) {
  const found: string[] = []
  for (const m of line.matchAll(/\\u\{?([0-9a-fA-F]{4,5})\}?/g)) found.push(String.fromCodePoint(parseInt(m[1]!, 16)))
  for (const m of line.matchAll(/content:[^;]*?\\([0-9a-fA-F]{2,6})/g)) found.push(String.fromCodePoint(parseInt(m[1]!, 16)))
  return found.filter(c => GLYPH.test(c))
}
export function problems(source: string) {
  const out: { line: number; text: string; why: string }[] = []
  uncommented(source).split('\n').forEach((text, i) => {
    const glyph = GLYPH.exec(text)?.[0] ?? escapedGlyphs(text)[0]
    if (glyph) out.push({ line: i + 1, text: text.trim(), why: `the text glyph ${glyph}` })
    const entity = ENTITY.exec(text)?.[0]
    if (entity) out.push({ line: i + 1, text: text.trim(), why: `the entity ${entity}` })
  })
  // A disclosure draws its own chevron; the browser's triangle is a text marker.
  for (const m of source.matchAll(/<summary\b[^>]*>([\s\S]*?)<\/summary>/g)) {
    if (!/<(AppIcon|BizIcon|QuoteIcon)\b/.test(m[1]!)) out.push({ line: source.slice(0, m.index).split('\n').length, text: m[0].slice(0, 80), why: 'a <summary> without an SVG chevron' })
  }
  return out
}

test('no text glyph stands in for an icon anywhere in web/src', () => {
  const used = new Set<string>()
  const found: string[] = []
  for (const path of files(root)) {
    const file = relative(root, path)
    const lines = readFileSync(path, 'utf8').split('\n')
    for (const p of problems(lines.join('\n'))) {
      const allowed = ALLOW.find(a => a.file === file && lines[p.line - 1]!.includes(a.includes))
      if (allowed) { used.add(`${allowed.file}:${allowed.includes}`); continue }
      found.push(`${file}:${p.line} ${p.why}: ${p.text.slice(0, 120)}`)
    }
  }
  assert.deepEqual(found, [], `Draw these with AppIcon, BizIcon or QuoteIcon (or KeyCap for keys), or spell the key as a word in a tooltip:\n${found.join('\n')}`)
  const stale = ALLOW.filter(a => !used.has(`${a.file}:${a.includes}`)).map(a => `${a.file}: ${a.includes}`)
  assert.deepEqual(stale, [], 'allow-list entries that match nothing any more')
})

test('the browser never draws its own disclosure triangle', () => {
  const base = readFileSync(join(root, 'styles/base.css'), 'utf8')
  assert.match(base, /summary \{ list-style: none; \}/)
  assert.match(base, /summary::-webkit-details-marker \{ display: none; \}/)
})

test('the guard catches what it is meant to catch', () => {
  const caught = (source: string) => problems(source).map(p => p.why)
  assert.deepEqual(caught('<button>✕</button>'), ['the text glyph ✕'])
  assert.deepEqual(caught('<kbd class="keycap">⌘</kbd>'), ['the text glyph ⌘'])
  assert.deepEqual(caught(".next::after { content: '\\25B6'; }"), ['the text glyph ▶'])
  assert.deepEqual(caught("const back = '\\u2190'"), ['the text glyph ←'])
  assert.deepEqual(caught('<a>Next &rarr;</a>'), ['the entity &rarr;'])
  assert.deepEqual(caught('<details><summary>More</summary></details>'), ['a <summary> without an SVG chevron'])
  assert.deepEqual(caught('<summary><AppIcon name="chevron-right" class="disclosure-chev" />More</summary>'), [])
  // Comments, words, separators and ellipses are text, not icons.
  assert.deepEqual(caught('// Esc → closes\n<p>Save · Cmd S</p>\n<span>Loading…</span>\n/* ▶ */ const url = "https://x.test/a"'), [])
})
