// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1532 (Tick spec v1): the Markdown editor never shrinks or jumps while you
// type; in split view the text box is as tall as the preview.
// Risk: the box collapses on every keystroke (its scroller clamps scrollTop and
// the page jumps), the split box scrolls inside at 560 px while the preview grows
// past it, deleting lines pulls the page up, the comment and phone editors lose
// their cap, or a refit after a delete (the box's scrollbar going, a late font)
// shrinks a capped box and pulls its panel up. Every input is sampled in the page itself: before the edit
// (beforeinput), right after it (input) and once its frame settled (rAF).
import { test, expect, type Locator, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { controlStability } from './control-stability'

// FIX5: the queue runs the list beside the editor changes. A stale narrow card
// delivery must not overwrite the wider layout and leave Updated hidden.
test('AEON-1532: a queued narrow table resize preserves the columns after the table grows', async ({ page }) => {
  await page.addInitScript(() => {
    const state = { capture: false, latest: 0, replay: null as (() => void) | null }
    Object.assign(window, { aeon1532TableResize: state })
    const Observer = window.ResizeObserver
    window.ResizeObserver = class extends Observer {
      constructor(callback: ResizeObserverCallback) {
        super((entries, observer) => {
          const card = entries.find(entry => entry.target.classList.contains('table-card'))
          if (card) {
            state.latest = card.contentRect.width
            if (state.capture) state.replay = () => callback([card], observer)
          }
          callback(entries, observer)
        })
      }
    }
  })
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'assignee', 'estimate', 'updated'] }
  await mockWork(page, data)
  await page.goto('/p/PHAROS?group=status')
  const table = page.getByRole('grid', { name: 'Tickets' }), card = page.locator('.table-card')
  await expect(table.locator('tr.ticket-row').first()).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
  await expect(card).toHaveAttribute('data-columns-settled', '1')
  const state = () => page.evaluate(() => (window as unknown as { aeon1532TableResize: { latest: number; replay: unknown } }).aeon1532TableResize.latest)
  await page.evaluate(() => { (window as unknown as { aeon1532TableResize: { capture: boolean } }).aeon1532TableResize.capture = true })
  await card.evaluate(el => { el.style.boxSizing = 'content-box'; el.style.width = '760px' })
  await expect.poll(state).toBe(760)
  await page.evaluate(() => {
    const state = (window as unknown as { aeon1532TableResize: { capture: boolean; replay: unknown } }).aeon1532TableResize
    state.capture = false
    if (!state.replay) throw new Error('No narrow card resize delivery was captured')
  })
  await card.evaluate(el => { el.style.width = '1000px' })
  await expect.poll(state).toBe(1000)
  const updated = table.locator('thead th[data-column-id="updated"]')
  await expect(updated).toBeVisible()
  await settle(page)
  const guard = await controlStability(page, {
    key: table.locator('thead th[data-column-id="key"]'),
    title: table.locator('thead th[data-column-id="title"]'),
    status: table.locator('thead th[data-column-id="status"]'),
    updated,
  })
  await guard.check(async () => {
    await page.evaluate(() => {
      const replay = (window as unknown as { aeon1532TableResize: { replay: (() => void) | null } }).aeon1532TableResize.replay
      if (!replay) throw new Error('No narrow card resize delivery was captured')
      replay()
    })
    await settle(page)
  })
  guard.done()
  expect(await card.evaluate(el => el.clientWidth)).toBe(1000)
})

const entry = '/p/PHAROS/knowledge/runbook/deploy-release'
const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
// 300 lines: headings, blank lines and steps long enough to wrap in a column.
const LONG = Array.from({ length: 300 }, (_, i) => i % 30 === 0 ? `## Section ${i / 30 + 1}` : i % 30 === 1 ? '' : `- Step ${i}: check the probe on host ${i % 7} and write down what it said before moving on.`).join('\n')
const lineStart = (text: string, line: number) => text.split('\n').slice(0, line).join('\n').length + (line ? 1 : 0)
// 200 characters with a line break, typed one key at a time.
const TYPED = ('Probe the host again once the pin has moved and write the answer down.\nCompare it with the last release and say what changed in one plain sentence. ').repeat(2).slice(0, 200)

interface Sample { before: number; during: number; after: number; topBefore: number; topDuring: number; topAfter: number; added: boolean; scroll: number; client: number; preview: number | null; caret: number; lead: number; view: number }
interface Probe { log: Sample[]; place(start: number, end: number, at: number): void; top(): number }
type Win = Window & { aeon1532: Probe }

// Samples every edit of one box. The caret oracle is the test's own detached twin
// with the box's text metrics; the scroller is the page's (the app's main column).
async function instrument(box: Locator) {
  await box.evaluate(node => {
    const area = node as HTMLTextAreaElement
    const scroller = (() => {
      for (let parent = area.parentElement; parent; parent = parent.parentElement) {
        const overflow = getComputedStyle(parent).overflowY
        if ((overflow === 'auto' || overflow === 'scroll') && parent.scrollHeight > parent.clientHeight) return parent
      }
      return document.scrollingElement as HTMLElement
    })()
    const preview = area.closest('.md-editor')?.querySelector<HTMLElement>('.md-preview') ?? null
    const style = getComputedStyle(area)
    const twin = document.createElement('textarea')
    for (const name of ['fontFamily', 'fontSize', 'fontWeight', 'lineHeight', 'letterSpacing', 'fontVariantLigatures', 'fontFeatureSettings', 'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft', 'borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth', 'boxSizing', 'whiteSpace', 'overflowWrap', 'wordBreak', 'tabSize'] as const) twin.style[name] = style[name]
    Object.assign(twin.style, { position: 'fixed', left: '-10000px', top: '0', height: '0', minHeight: '0', overflow: 'hidden', visibility: 'hidden', borderStyle: 'solid' })
    document.body.append(twin)
    const pad = parseFloat(style.paddingBottom), border = parseFloat(style.borderTopWidth)
    // The bottom of the line holding `index`, in viewport coordinates.
    const lineBottom = (index: number) => {
      twin.style.width = `${area.clientWidth + parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth)}px`
      twin.value = area.value.slice(0, index)
      return area.getBoundingClientRect().top + border + twin.scrollHeight - pad - area.scrollTop
    }
    // The caret the browser scrolls into view spans the font's ascent and descent,
    // centred in the line: the line box less its half-leading on each side.
    const metrics = document.createElement('canvas').getContext('2d')!
    const halfLeading = () => {
      metrics.font = `${style.fontStyle} ${style.fontWeight} ${style.fontSize} ${style.fontFamily}`
      const glyphs = metrics.measureText('M')
      return (parseFloat(style.lineHeight) - glyphs.fontBoundingBoxAscent - glyphs.fontBoundingBoxDescent) / 2
    }
    const view = () => scroller === document.scrollingElement ? { top: 0, bottom: innerHeight } : { top: scroller.getBoundingClientRect().top + scroller.clientTop, bottom: scroller.getBoundingClientRect().top + scroller.clientTop + scroller.clientHeight }
    const log: Sample[] = []
    let start = { height: 0, top: 0, length: 0 }
    area.addEventListener('beforeinput', () => { start = { height: area.offsetHeight, top: scroller.scrollTop, length: area.value.length } })
    area.addEventListener('input', () => {
      const from = start, during = area.offsetHeight, topDuring = scroller.scrollTop, added = area.value.length >= from.length
      requestAnimationFrame(() => log.push({
        before: from.height, during, after: area.offsetHeight, topBefore: from.top, topDuring, topAfter: scroller.scrollTop, added,
        scroll: area.scrollHeight, client: area.clientHeight, preview: preview?.offsetParent ? preview.offsetHeight : null, caret: lineBottom(area.selectionEnd), lead: halfLeading(), view: view().bottom,
      }))
    })
    ;(window as unknown as Win).aeon1532 = {
      log,
      top: () => scroller.scrollTop,
      // Selects [start, end) and scrolls so the line at `start` sits at `at` of the visible height.
      // A capped box is first brought fully into view, then scrolled inside.
      place(from: number, to: number, at: number) {
        area.focus({ preventScroll: true })
        area.setSelectionRange(from, to)
        const frame = view()
        if (area.scrollHeight > area.clientHeight + 1) {
          scroller.scrollTop += area.getBoundingClientRect().top - frame.top - 60
          area.scrollTop += lineBottom(from) - (area.getBoundingClientRect().top + area.clientHeight * at)
        } else {
          scroller.scrollTop += lineBottom(from) - (frame.top + (frame.bottom - frame.top) * at)
        }
      },
    }
  })
}
const settle = (page: Page) => page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))))
async function typeAt(page: Page, box: Locator, start: number, end: number, at: number, keys: () => Promise<void>) {
  await box.evaluate((_, [from, to, where]) => { const probe = (window as unknown as Win).aeon1532; probe.place(from, to, where); probe.log.length = 0 }, [start, end, at])
  await settle(page)
  const top = await page.evaluate(() => (window as unknown as Win).aeon1532.top())
  await keys()
  await settle(page)
  const samples = await page.evaluate(() => (window as unknown as Win).aeon1532.log.slice())
  expect(samples.length, 'sampled inputs').toBeGreaterThan(0)
  return { top, samples, end: await page.evaluate(() => (window as unknown as Win).aeon1532.top()) }
}

// Every assert lands in a checks list (written as JSON for the review) before it is enforced.
interface Check { state: string; width: number; theme: string; check: string; ok: boolean; detail: unknown }
function checker(width: number, theme: string) {
  const items: Check[] = []
  return {
    items,
    add(state: string, check: string, ok: boolean, detail: unknown) { items.push({ state, width, theme, check, ok, detail }) },
    async save(name: string) {
      const json = JSON.stringify(items, null, 2) + '\n'
      await writeFile(test.info().outputPath(`aeon1532-checks-${name}.json`), json)
      // The remote runner copies back PNG and Markdown only: the same checks as Markdown.
      await writeFile(test.info().outputPath(`aeon1532-checks-${name}.md`), `# AEON-1532 checks ${name}\n\n\`\`\`json\n${json}\`\`\`\n`)
      for (const item of items) expect(item.ok, `${item.state}: ${item.check} ${JSON.stringify(item.detail)}`).toBe(true)
    },
  }
}
const worst = (values: number[]) => values.reduce((max, value) => Math.max(max, value), 0)
// Adding text never shrinks the box, neither during the input nor once it settled.
const shrink = (samples: Sample[]) => worst(samples.filter(s => s.added).flatMap(s => [s.before - s.during, s.before - s.after]))
const moved = (samples: Sample[], top: number) => worst(samples.flatMap(s => [Math.abs(s.topDuring - top), Math.abs(s.topAfter - top)]))
const unequal = (samples: Sample[]) => worst(samples.map(s => Math.abs(s.after - (s.preview ?? s.after))))
const inner = (samples: Sample[]) => worst(samples.map(s => s.scroll - s.client))

async function openEntry(page: Page, width: number, theme: 'light' | 'dark') {
  await page.setViewportSize({ width, height: width <= 400 ? 844 : 900 })
  await page.emulateMedia({ colorScheme: theme })
  await mockWork(page, fixtures())
  const world = knowledgeWorld()
  world.entries.find(item => item.id === 'k-deploy')!.body = LONG
  await mockKnowledge(page, world)
  await page.goto(entry)
  await expect(page.locator('.e-body')).toBeVisible()
  await page.keyboard.press('e')
  const form = page.getByRole('form', { name: 'Edit deploy-release' })
  await expect(form).toBeVisible()
  const box = form.getByRole('textbox', { name: 'Text, Markdown' })
  await expect(box).toHaveValue(LONG)
  await instrument(box)
  const above = { title: form.getByLabel('Title'), slug: form.getByLabel('Slug'), status: form.getByRole('radio', { name: 'Proposed' }), 'text label': page.locator('#e-body-label') }
  // `has` resolves inside each .md-editor, so it takes the page-rooted box, never `box` (which starts at the form).
  const editor = form.locator('.md-editor', { has: page.getByRole('textbox', { name: 'Text, Markdown' }) })
  return { form, box, editor, above }
}

async function deleteFiftyLines(page: Page, box: Locator, guard: Awaited<ReturnType<typeof controlStability>>, checks: ReturnType<typeof checker>) {
  // M2, middle: plenty of text below, so the row simply shrinks.
  await guard.check(async () => {
    const run = await typeAt(page, box, lineStart(await box.inputValue(), 100), lineStart(await box.inputValue(), 150), .4, () => page.keyboard.press('Backspace'))
    const [sample] = run.samples
    checks.add('delete-middle', 'page scrollTop unchanged (px)', moved(run.samples, run.top) <= .5 && Math.abs(run.end - run.top) <= .5, { movedPx: moved(run.samples, run.top), before: run.top, after: run.end })
    checks.add('delete-middle', 'the row shrank with the text', sample.after < sample.before - 500, { before: sample.before, after: sample.after })
    checks.add('delete-middle', 'textarea and preview heights equal ±1 px', unequal(run.samples) <= 1, { maxDiffPx: unequal(run.samples) })
    checks.add('delete-middle', 'textarea scrollHeight ≤ clientHeight', inner(run.samples) <= 0, { maxOverflowPx: inner(run.samples) })
  })
  // M2, end: the last 50 lines go while the view shows the end of the page; a
  // collapsing row would clamp scrollTop here.
  await guard.check(async () => {
    const text = await box.inputValue(), lines = text.split('\n').length
    const run = await typeAt(page, box, lineStart(text, lines - 50) - 1, text.length, .3, () => page.keyboard.press('Backspace'))
    checks.add('delete-end', 'page scrollTop unchanged (px)', moved(run.samples, run.top) <= .5 && Math.abs(run.end - run.top) <= .5, { movedPx: moved(run.samples, run.top), before: run.top, after: run.end })
    checks.add('delete-end', 'textarea and preview heights equal ±1 px', unequal(run.samples) <= 1, { maxDiffPx: unequal(run.samples) })
    checks.add('delete-end', 'textarea scrollHeight ≤ clientHeight', inner(run.samples) <= 0, { maxOverflowPx: inner(run.samples) })
  })
  checks.add('delete', 'movementPx 0 for the title, slug, status and Text label', true, { tolerancePx: .5 })
}

for (const { width, theme } of [{ width: 1440, theme: 'light' }, { width: 1440, theme: 'dark' }, { width: 1024, theme: 'light' }, { width: 390, theme: 'light' }] as const) {
  test(`AEON-1532: typing in a 300-line knowledge entry at ${width} ${theme} never shrinks the box or moves the page`, async ({ page }) => {
    test.setTimeout(90_000)
    const errors = watchErrors(page)
    const { box, editor, above } = await openEntry(page, width, theme)
    const split = width >= 1100
    await expect(editor).toHaveCount(1)
    if (split) await expect(editor).toHaveClass(/\bsplit\b/)
    else await expect(editor).not.toHaveClass(/\bsplit\b/)
    const checks = checker(width, theme)
    const guard = await controlStability(page, above)
    // M1, middle: the caret line sits mid-view; the page must not move at all.
    const middle = lineStart(LONG, 150)
    await guard.check(async () => {
      const run = await typeAt(page, box, middle, middle, .5, () => page.keyboard.type(TYPED))
      checks.add('middle', 'textarea height never drops while adding text (px)', shrink(run.samples) <= .5, { shrinkPx: shrink(run.samples), inputs: run.samples.length })
      checks.add('middle', 'page scrollTop unchanged (px)', moved(run.samples, run.top) <= .5 && Math.abs(run.end - run.top) <= .5, { movedPx: moved(run.samples, run.top), before: run.top, after: run.end })
      if (split) {
        checks.add('middle', 'textarea and preview heights equal ±1 px', unequal(run.samples) <= 1, { maxDiffPx: unequal(run.samples) })
        checks.add('middle', 'textarea scrollHeight ≤ clientHeight', inner(run.samples) <= 0, { maxOverflowPx: inner(run.samples) })
      } else {
        checks.add('middle', 'box keeps its 560 px cap', worst(run.samples.map(s => s.after)) <= 560.5, { maxHeight: worst(run.samples.map(s => s.after)) })
      }
    })
    checks.add('middle', 'movementPx 0 for the title, slug, status and Text label', true, { tolerancePx: .5 })
    await page.screenshot({ path: test.info().outputPath(`aeon1532-${width}-${theme}-middle.png`) })
    // M1, end: the caret sits near the bottom edge; only the browser's caret-into-view may scroll, never up.
    await guard.check(async () => {
      const run = await typeAt(page, box, (await box.inputValue()).length, (await box.inputValue()).length, .93, () => page.keyboard.type(TYPED))
      const up = worst(run.samples.map(s => s.topBefore - s.topAfter))
      // The browser's own caret-into-view (Tick spec v1 §3.3) brings the caret into
      // view, not the line's leading below it; the app never scrolls on its own.
      const hidden = worst(run.samples.map(s => s.caret - s.lead - s.view))
      const lead = run.samples.map(s => s.lead)
      checks.add('end', 'textarea height never drops while adding text (px)', shrink(run.samples) <= .5, { shrinkPx: shrink(run.samples), inputs: run.samples.length })
      checks.add('end', 'page never scrolls up; only caret-into-view scrolls down', up <= .5 && run.end >= run.top - .5, { upPx: up, before: run.top, after: run.end })
      checks.add('end', 'the caret stays in view', hidden <= 1, { belowViewPx: hidden, lineBoxBelowViewPx: worst(run.samples.map(s => s.caret - s.view)) })
      checks.add('end', 'half-leading measured from the font (0–3 px)', lead.every(value => value >= 0 && value <= 3), { halfLeadingPx: [Math.min(...lead), Math.max(...lead)] })
      if (split) {
        checks.add('end', 'textarea and preview heights equal ±1 px', unequal(run.samples) <= 1, { maxDiffPx: unequal(run.samples) })
        checks.add('end', 'textarea scrollHeight ≤ clientHeight', inner(run.samples) <= 0, { maxOverflowPx: inner(run.samples) })
      } else {
        checks.add('end', 'box keeps its 560 px cap', worst(run.samples.map(s => s.after)) <= 560.5, { maxHeight: worst(run.samples.map(s => s.after)) })
      }
    })
    checks.add('end', 'movementPx 0 for the title, slug, status and Text label', true, { tolerancePx: .5 })
    // One retained browser proof covers real input growth and both deletion
    // positions; runtime sizing checks do not claim physical scroll geometry.
    if (width === 1440 && theme === 'light') await deleteFiftyLines(page, box, guard, checks)
    guard.done()
    await page.screenshot({ path: test.info().outputPath(`aeon1532-${width}-${theme}-end.png`) })
    checks.add('page', 'no page errors', errors.length === 0, { errors })
    await checks.save(`${width}-${theme}`)
  })
}

test('AEON-1532: deleting 50 lines keeps the page where it is, in the middle and at the end', async ({ page }) => {
  test.setTimeout(90_000)
  const { box, above } = await openEntry(page, 1440, 'light')
  const checks = checker(1440, 'light')
  const guard = await controlStability(page, above)
  await deleteFiftyLines(page, box, guard, checks)
  guard.done()
  await checks.save('delete-1440-light')
})

// Posts a comment in the ticket panel and opens it for editing.
async function editComment(page: Page) {
  // The mock stamps new comments with its own clock (2026-09-23T12:00Z); the page
  // runs on that clock too, so the comment is inside its 15-minute edit window.
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-13')
  const ws = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(ws.getByRole('heading', { name: 'Run the disposable Hetzner end-to-end check' })).toBeVisible()
  await page.keyboard.press('c')
  await page.keyboard.type('Ran it on hsb-lab.')
  await page.keyboard.press(`${mod}+Enter`)
  const comment = ws.locator('.entry.comment').filter({ hasText: 'Ran it on' })
  await expect(comment).toBeVisible()
  await comment.getByRole('button', { name: 'Edit comment' }).click()
  const box = ws.getByLabel('Comment, Markdown')
  await expect(box).toBeVisible()
  return box
}

test('AEON-1532: the comment editor grows to its 560 px cap without ever collapsing', async ({ page }) => {
  test.setTimeout(90_000)
  const box = await editComment(page)
  await instrument(box)
  const checks = checker(1440, 'light')
  const keys = Array.from({ length: 32 }, (_, i) => `Follow-up ${i + 1}: the probe answered in time.\n`).join('')
  const run = await typeAt(page, box, (await box.inputValue()).length, (await box.inputValue()).length, .5, () => page.keyboard.type(keys))
  const last = run.samples.at(-1)!
  checks.add('comment', 'textarea height never drops while adding text (px)', shrink(run.samples) <= .5, { shrinkPx: shrink(run.samples), inputs: run.samples.length })
  checks.add('comment', 'stops at the 560 px cap and scrolls inside', Math.abs(last.after - 560) <= .5 && last.scroll > last.client, { height: last.after, scrollHeight: last.scroll, clientHeight: last.client })
  checks.add('comment', 'panel never scrolls up; only caret-into-view scrolls down', worst(run.samples.map(s => s.topBefore - s.topAfter)) <= .5, { before: run.top, after: run.end })
  await page.screenshot({ path: test.info().outputPath('aeon1532-1440-light-comment.png') })
  await checks.save('comment-1440-light')
})

test('AEON-1532: the full ticket page writes the description in one row as tall as its preview', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  // The title paints from the ticket payload while this project's grants are
  // still in flight. e in that gap must open the editor once the answer lands.
  let releasePermissions!: () => void
  const permissionsHeld = new Promise<void>(resolve => { releasePermissions = resolve })
  await page.route('**/api/me/permissions**', async route => {
    if (new URL(route.request().url()).searchParams.has('project_id')) await permissionsHeld
    await route.fallback()
  })
  await page.goto('/p/PHAROS/PHAROS-12?view=full')
  await expect(page.getByRole('article', { name: 'Ticket details' }).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await page.keyboard.press('e')
  releasePermissions()
  const editor = page.locator('.edit-form .md-editor').first()
  await expect(editor).toHaveClass(/split/)
  const box = editor.getByLabel('Description, Markdown')
  await box.fill(LONG.split('\n').slice(0, 120).join('\n'))
  await instrument(box)
  const checks = checker(1440, 'light')
  const run = await typeAt(page, box, lineStart(LONG, 60), lineStart(LONG, 60), .5, () => page.keyboard.type(TYPED.slice(0, 60)))
  checks.add('ticket', 'description box and preview heights equal ±1 px', unequal(run.samples) <= 1, { maxDiffPx: unequal(run.samples) })
  checks.add('ticket', 'no 560 px cap and no inner scroll', run.samples.at(-1)!.after > 1000 && inner(run.samples) <= 0, { height: run.samples.at(-1)!.after, maxOverflowPx: inner(run.samples) })
  checks.add('ticket', 'page scrollTop unchanged (px)', moved(run.samples, run.top) <= .5, { movedPx: moved(run.samples, run.top) })
  await checks.save('ticket-1440-light')
})

// Classic scrollbars (Linux, Windows, macOS set to always show them) take width
// from the box. A delete that takes a capped box back under its cap keeps it
// tall while the panel sits at its end; then the box's scrollbar goes and the
// text rewraps wider, and a late font rewraps it too. Neither refit may shrink
// the box by more than the slack below, or the panel clamps its scrollTop.
// Headless Chromium hides every scrollbar and macOS overlays them, so this
// test launches its own browser that keeps scrollbars (a launch option cannot
// change per test), and the box gets a classic 14 px one of its own.
interface Edge { top: number; max: number; height: number; width: number; bar: number; overflow: number; inView: boolean }
const edge = (box: Locator) => box.evaluate((node): Edge => {
  const area = node as HTMLTextAreaElement, panel = (window as unknown as { aeon1532Panel: HTMLElement }).aeon1532Panel
  const style = getComputedStyle(area), rect = area.getBoundingClientRect(), view = panel.getBoundingClientRect()
  return {
    top: panel.scrollTop, max: panel.scrollHeight - panel.clientHeight, height: area.offsetHeight, width: area.clientWidth,
    bar: area.offsetWidth - area.clientWidth - parseFloat(style.borderLeftWidth) - parseFloat(style.borderRightWidth),
    overflow: area.scrollHeight - area.clientHeight, inView: rect.top >= view.top + panel.clientTop && rect.bottom <= view.top + panel.clientTop + panel.clientHeight,
  }
})
// Fills the box past its cap, scrolls the panel to its end, selects everything
// after the first 8 lines and deletes it with one Backspace.
async function deleteAtEnd(page: Page, box: Locator) {
  await box.fill(Array.from({ length: 40 }, (_, i) => `Follow-up ${i + 1}: the probe answered in time.`).join('\n'))
  await settle(page)
  await box.evaluate(node => {
    const area = node as HTMLTextAreaElement
    const panel = (() => {
      for (let parent = area.parentElement; parent; parent = parent.parentElement) {
        const overflow = getComputedStyle(parent).overflowY
        if ((overflow === 'auto' || overflow === 'scroll') && parent.scrollHeight > parent.clientHeight) return parent
      }
      return document.scrollingElement as HTMLElement
    })()
    ;(window as unknown as { aeon1532Panel: HTMLElement }).aeon1532Panel = panel
    area.focus({ preventScroll: true })
    area.setSelectionRange(area.value.split('\n').slice(0, 8).join('\n').length, area.value.length)
    area.scrollTop = 0
    panel.scrollTop = panel.scrollHeight
  })
  await settle(page)
  const before = await edge(box)
  await page.keyboard.press('Backspace')
  await settle(page)
  return { before, after: await edge(box) }
}

test('AEON-1532: deleting a capped comment back under its cap keeps the panel at its end when its scrollbar goes or a font arrives', async ({ playwright, baseURL }) => {
  test.setTimeout(90_000)
  const browser = await playwright.chromium.launch({ headless: true, args: ['--disable-gpu'], ignoreDefaultArgs: ['--hide-scrollbars'] })
  try {
    const page = await browser.newPage({ baseURL, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const box = await editComment(page)
    const checks = checker(1440, 'light')
    const start = (state: string, { before }: { before: Edge }) => {
      checks.add(state, 'panel scrolled to its end (no slack below)', before.top >= 1 && Math.abs(before.max - before.top) <= 1, { scrollTop: before.top, max: before.max })
      checks.add(state, 'box at its 560 px cap, scrolling inside, fully in view', Math.abs(before.height - 560) <= .5 && before.overflow > 0 && before.inView, before)
    }
    const still = (state: string, check: string, from: Edge, to: Edge) => checks.add(state, check, Math.abs(to.top - from.top) <= .5 && to.height >= from.height - .5, { before: from, after: to })
    // No scrollbar on the box: the delete keeps its width, so only the font refits.
    const bar = await page.addStyleTag({ content: '.md-area { scrollbar-width: none; }' })
    const font = await deleteAtEnd(page, box)
    start('font', font)
    checks.add('font', 'the delete took the text under the cap and kept the width', font.after.overflow <= 0 && Math.abs(font.before.bar) <= 1 && font.after.width === font.before.width, font.after)
    still('font', 'panel scrollTop and box height unchanged after the delete', font.before, font.after)
    await page.evaluate(() => document.fonts.dispatchEvent(new Event('loadingdone')))
    await settle(page)
    still('font', 'panel scrollTop and box height unchanged after a late font', font.after, await edge(box))
    // A classic scrollbar: the delete removes it, the box widens and refits.
    await bar.evaluate(node => { node.textContent = '.md-area { scrollbar-width: auto; scrollbar-color: auto; } .md-area::-webkit-scrollbar { width: 14px; } .md-area::-webkit-scrollbar-thumb { background: rgb(128 128 128 / .6); }' })
    const scrollbar = await deleteAtEnd(page, box)
    start('scrollbar', scrollbar)
    checks.add('scrollbar', 'the scrollbar took width, then went with the overflow', scrollbar.before.bar >= 10 && Math.abs(scrollbar.after.bar) <= 1 && scrollbar.after.overflow <= 0 && scrollbar.after.width - scrollbar.before.width >= 10, { before: scrollbar.before, after: scrollbar.after })
    still('scrollbar', 'panel scrollTop and box height unchanged once the box widened', scrollbar.before, scrollbar.after)
    checks.add('page', 'no page errors', errors.length === 0, { errors })
    await page.screenshot({ path: test.info().outputPath('aeon1532-1440-light-comment-scrollbar.png') })
    await checks.save('comment-scrollbar-1440-light')
  } finally { await browser.close() }
})
