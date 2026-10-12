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
const ALLOW: { file: string; includes: string; why: string; exact?: boolean }[] = [
  // Frozen approved prose: exact whole lines, so extra icon glyphs still fail.
  { file: "components/settings/ProjectGeneralSettings.vue", includes: "<p class=\"foot\"><AppIcon name=\"external\" :size=\"13\" /><span>Kinds of work and vocabulary are the same in every project. <RouterLink class=\"link-btn quiet\" to=\"/settings/kinds\">Settings › Kinds of work <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></span></p>", exact: true, why: "approved AEON-1227 P1 breadcrumb names the global Kinds of work page; the adjacent chevron is SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<SettingsCard v-else title=\"Work context\" icon=\"shield\" anchor=\"project-work-context\" variant=\"project\"><p class=\"owner-line\">Owners and admins choose where accounts may work in Settings › Accounts.</p><RouterLink class=\"link-btn quiet\" to=\"/settings/accounts\">Settings › Accounts <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></SettingsCard>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<p>Model choice: <RouterLink to=\"/settings/models\">Settings › Models <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></p>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<p>Project rules: <RouterLink to=\"/settings/agent-rules\">Settings › Agent rules <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></p>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<p>Accounts and contexts: <RouterLink to=\"/settings/accounts\">Settings › Accounts <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></p>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<p>Computers: <RouterLink to=\"/settings/computers\">Settings › Computers <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></p>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<p id=\"review\">Cross-family review: <RouterLink to=\"/settings/policies#cross-family-review\">Settings › Policies <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></p>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<p>Status autopilot: <RouterLink to=\"/settings/autopilot#status-autopilot\">Settings › Autopilot <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink></p>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "components/settings/ProjectSettings.vue", includes: "<RouterLink class=\"link-btn quiet\" :to=\"`/settings/access/projects/${encodeURIComponent(project.id)}`\">Settings › Access › Projects <AppIcon name=\"chevron-right\" :size=\"12\" /></RouterLink>", exact: true, why: "approved AEON-1227 P1 prose names the owner of a setting; navigation icons remain SVG" },
  { file: "lib/projectSettingsExplainers.ts", includes: "where: { label: 'Queue records: each ticket’s panel › Delivery. The other machines’ records: no page yet.', go: 'ticket' } },", exact: true, why: "approved AEON-1227 explain.js must be reused as is; breadcrumb and state-transition words in explanatory prose, not icons" },
  { file: "lib/projectSettingsExplainers.ts", includes: "where: { label: 'Each ticket’s panel › Delivery shows its queue round.', go: 'ticket' } },", exact: true, why: "approved AEON-1227 explain.js must be reused as is; breadcrumb and state-transition words in explanatory prose, not icons" },
  { file: "lib/projectSettingsExplainers.ts", includes: "what: 'Chooses the harness, model and account for each round, from Settings › Models, the work context and each account’s quota.',", exact: true, why: "approved AEON-1227 explain.js must be reused as is; breadcrumb and state-transition words in explanatory prose, not icons" },
  { file: "lib/projectSettingsExplainers.ts", includes: "who: 'Each person for themselves, once an admin turns chat-v1 on in Settings › Workspace › Features.' },", exact: true, why: "approved AEON-1227 explain.js must be reused as is; breadcrumb and state-transition words in explanatory prose, not icons" },
  { file: "lib/projectSettingsExplainers.ts", includes: "effect: 'You paste up to 100 pull request numbers. PAIMOS asks GitHub which were merged into main and previews the tickets it would move, e.g. “{K}-{N} → Delivered”. Nothing changes until you apply that preview. Tickets a gate refuses, such as open review findings, stay as they are.',", exact: true, why: "approved AEON-1227 explain.js must be reused as is; breadcrumb and state-transition words in explanatory prose, not icons" },
  { file: "lib/projectSettingsExplainers.ts", includes: "what: 'Moves tickets on by themselves when the evidence is there, e.g. to Done when the pull request merged, following the rules in Settings › Autopilot.',", exact: true, why: "approved AEON-1227 explain.js must be reused as is; breadcrumb and state-transition words in explanatory prose, not icons" },
  { file: "views/ProjectView.vue", includes: "<RouterLink v-if=\"can('nodes.write', project.id)\" class=\"icon-btn sm head-edit\" :to=\"`/p/${encodeURIComponent(project.routeKey)}/settings#f-name`\" aria-label=\"Edit name and description in Settings\" data-tip=\"Edit in Settings › General\"><AppIcon name=\"edit\" :size=\"14\" /></RouterLink>", exact: true, why: "approved AEON-1227 P1 edit tooltip names the General section; the control uses an SVG edit icon" },
  { file: 'lib/serviceTier.ts', includes: 'TIER_NAME[s.tier]} ×${s.price_multiplier}', why: 'frozen numerical price multiplier in approved AEON-436 fragment, not an icon (AEON-612)' },
  { file: 'components/agents/SessionMessages.vue', includes: 'identical posts`">×{{ m.count }}', why: 'multiplicity of identical messages (AEON-267), numerical text, not an icon' },
  { file: 'lib/quotes/prose.ts', includes: 'export const BULLETS', why: 'the bullets a quote prints in its document' },
  { file: 'components/work/AttachmentLightbox.vue', includes: '.width} × ${', why: 'image dimensions, 1200 × 800' },
  { file: 'lib/releaseStats.ts', includes: '${rate(r)}×`', why: 'a multiple in text, "3× the median gap" (AEON-488)' },
  { file: 'lib/modelsSimple.ts', includes: 'Locked in Settings › Models by', why: 'the stored reason of a lock, worded by the approved AEON-999 spec; a sentence in a rule, not an icon (AEON-1011)' },
  { file: 'components/settings/models/KindMenu.vue', includes: 'Settings › Kinds of work', why: 'approved AEON-999 breadcrumb naming where kinds of work are managed; a sentence, not an icon (AEON-1011)' },
  { file: 'components/settings/models/ModelPicker.vue', includes: 'Type to filter · ←→ level', why: 'approved AEON-999 placeholder, worded "level" in the approved Models v2 design (AEON-1144); a field placeholder cannot hold a KeyCap, and the arrows name the keys (AEON-1011, AEON-1160)' },
  { file: 'lib/modelRegistry.ts', includes: "in Settings › Models'", why: 'the retirement reasons stored with a removed or undone model (AEON-1012); a breadcrumb in data the registry keeps, not an icon' },
  { file: 'lib/modelRegistry.ts', includes: '-‹level›${fast}', why: 'approved AEON-1144 registry copy writes Cursor\'s per-level ids as one id with a placeholder (grok-4.7-‹level›); a code id in text, not an icon (AEON-1163)' },
  { file: 'lib/deliveryNumbersText.ts', includes: "const TO = '→'", why: 'approved AEON-994 Delivery copy uses the arrow as the word "to" (run start → run end, PR opened → merged); text, not an icon' },
  { file: 'lib/deliveryFlowText.ts', includes: "export const TO = '→'", why: 'approved AEON-994 Flow copy uses the arrow as the word "to" (a to l, start to end); text, not an icon (AEON-1006)' },
  { file: 'lib/deliveryFlowText.ts', includes: "export const TIMES = '×'", why: 'approved AEON-994 Flow copy: a multiple in text (1x and 2x speed, "3x the target"), not an icon (AEON-1006)' },
  { file: 'lib/deliverySimple.ts', includes: '1 : 0)}× ${text.theTarget}', why: 'a multiple in text, "about 5× the target" (AEON-1003 verdict gap)' },
  { file: 'lib/deliverySimpleText.ts', includes: "const TO = '→'", why: 'approved AEON-994 Simple copy uses the arrow as the word "to" (start → end, PR opened → merged); text, not an icon' },
  { file: 'lib/workKindsCopy.ts', includes: 'Settings › Models', why: 'approved AEON-854 breadcrumb in the kinds lead; it names the Models board in a sentence and is not an icon' },
  { file: 'components/settings/CapacitySection.vue', includes: 'new work <b>follows Settings › Models</b>', why: 'Markus\'s AEON-1144 D1 wording "At the limit follows Settings › Models", copied by the approved AEON-1149 Capacity design (AEON-1175); a breadcrumb in a sentence next to the Models link, not an icon' },
  { file: 'components/agents/DialDetail.vue', includes: '>Settings › Models</RouterLink>', why: 'approved Models v2 design, package D1 (AEON-1144 report section d): the dial says "At the limit: follows Settings › Models" with a link; the breadcrumb is the link text naming the page, not an icon (AEON-1166)' },
  { file: 'lib/dailyLimits.ts', includes: "'new work follows Settings › Models'", why: 'the same approved D1 sentence in the dial\'s at-the-limit words (AEON-1144 report section d); a breadcrumb in text, not an icon (AEON-1166)' },
  { file: 'components/recurrences/RecurringWorkCard.vue', includes: 'Every routine you can see: Agents › Routines<AppIcon name="chevron-right"', why: 'approved AEON-1192 link copy "Every routine you can see: Agents › Routines" names the page in a sentence; its chevron is the SVG after it' },
  { file: 'components/business/areas.ts', includes: "SETUP_PLACE = 'Settings › Business'", why: 'AEON-1189 names the one place that sets Business up, in the link "Set up in Settings › Business" and its sentences; a breadcrumb in text, not an icon' },
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

function allowedGlyphLine(file: string, line: string) {
  return ALLOW.find(a => a.file === file && (a.exact ? line.trim() === a.includes : line.includes(a.includes)))
}

test('no text glyph stands in for an icon anywhere in web/src', () => {
  const used = new Set<string>()
  const found: string[] = []
  for (const path of files(root)) {
    const file = relative(root, path)
    const lines = readFileSync(path, 'utf8').split('\n')
    for (const p of problems(lines.join('\n'))) {
      const allowed = allowedGlyphLine(file, lines[p.line - 1]!)
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
  // Approved prose never exempts a new text icon on the same source line.
  for (const entry of ALLOW.filter(entry => entry.exact)) {
    assert.ok(problems(entry.includes).length > 0)
    assert.equal(allowedGlyphLine(entry.file, entry.includes), entry)
    assert.equal(allowedGlyphLine(entry.file, `${entry.includes}<button>✕</button>`), undefined)
    assert.equal(allowedGlyphLine('unapproved.vue', entry.includes), undefined)
  }
})
