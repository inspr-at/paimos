// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1532 (Tick spec v1): the Markdown editor never shrinks or jumps while you
// type; in split view the text box is as tall as the preview.
// Risk: the box collapses on every keystroke (its scroller clamps scrollTop and
// the page jumps), the split box scrolls inside at 560 px while the preview grows
// past it, deleting lines pulls the page up, or the comment and phone editors
// lose their cap. Every input is sampled in the page itself: before the edit
// (beforeinput), right after it (input) and once its frame settled (rAF).
import { test, expect, type Locator, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { controlStability } from './control-stability'

const entry = '/p/PHAROS/knowledge/runbook/deploy-release'
const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
// 300 lines: headings, blank lines and steps long enough to wrap in a column.
const LONG = Array.from({ length: 300 }, (_, i) => i % 30 === 0 ? `## Section ${i / 30 + 1}` : i % 30 === 1 ? '' : `- Step ${i}: check the probe on host ${i % 7} and write down what it said before moving on.`).join('\n')
const lineStart = (text: string, line: number) => text.split('\n').slice(0, line).join('\n').length + (line ? 1 : 0)
// 200 characters with a line break, typed one key at a time.
const TYPED = ('Probe the host again once the pin has moved and write the answer down.\nCompare it with the last release and say what changed in one plain sentence. ').repeat(2).slice(0, 200)

interface Sample { before: number; during: number; after: number; topBefore: number; topDuring: number; topAfter: number; added: boolean; scroll: number; client: number; preview: number | null; caret: number; view: number }
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
    const view = () => scroller === document.scrollingElement ? { top: 0, bottom: innerHeight } : { top: scroller.getBoundingClientRect().top + scroller.clientTop, bottom: scroller.getBoundingClientRect().top + scroller.clientTop + scroller.clientHeight }
    const log: Sample[] = []
    let start = { height: 0, top: 0, length: 0 }
    area.addEventListener('beforeinput', () => { start = { height: area.offsetHeight, top: scroller.scrollTop, length: area.value.length } })
    area.addEventListener('input', () => {
      const from = start, during = area.offsetHeight, topDuring = scroller.scrollTop, added = area.value.length >= from.length
      requestAnimationFrame(() => log.push({
        before: from.height, during, after: area.offsetHeight, topBefore: from.top, topDuring, topAfter: scroller.scrollTop, added,
        scroll: area.scrollHeight, client: area.clientHeight, preview: preview?.offsetParent ? preview.offsetHeight : null, caret: lineBottom(area.selectionEnd), view: view().bottom,
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
  return { form, box, above }
}

for (const { width, theme } of [{ width: 1440, theme: 'light' }, { width: 1440, theme: 'dark' }, { width: 1024, theme: 'light' }, { width: 390, theme: 'light' }] as const) {
  test(`AEON-1532: typing in a 300-line knowledge entry at ${width} ${theme} never shrinks the box or moves the page`, async ({ page }) => {
    test.setTimeout(90_000)
    const errors = watchErrors(page)
    const { box, above } = await openEntry(page, width, theme)
    const split = width >= 1100
    const editor = page.locator('.md-editor').filter({ has: box })
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
      const hidden = worst(run.samples.map(s => s.caret - s.view))
      checks.add('end', 'textarea height never drops while adding text (px)', shrink(run.samples) <= .5, { shrinkPx: shrink(run.samples), inputs: run.samples.length })
      checks.add('end', 'page never scrolls up; only caret-into-view scrolls down', up <= .5 && run.end >= run.top - .5, { upPx: up, before: run.top, after: run.end })
      checks.add('end', 'the caret line stays in view', hidden <= 1, { belowViewPx: hidden })
      if (split) {
        checks.add('end', 'textarea and preview heights equal ±1 px', unequal(run.samples) <= 1, { maxDiffPx: unequal(run.samples) })
        checks.add('end', 'textarea scrollHeight ≤ clientHeight', inner(run.samples) <= 0, { maxOverflowPx: inner(run.samples) })
      } else {
        checks.add('end', 'box keeps its 560 px cap', worst(run.samples.map(s => s.after)) <= 560.5, { maxHeight: worst(run.samples.map(s => s.after)) })
      }
    })
    checks.add('end', 'movementPx 0 for the title, slug, status and Text label', true, { tolerancePx: .5 })
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
  // M2, middle: plenty of text below, so the row simply shrinks.
  await guard.check(async () => {
    const run = await typeAt(page, box, lineStart(LONG, 100), lineStart(LONG, 150), .4, () => page.keyboard.press('Backspace'))
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
  guard.done()
  await checks.save('delete-1440-light')
})

test('AEON-1532: the comment editor grows to its 560 px cap without ever collapsing', async ({ page }) => {
  test.setTimeout(90_000)
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
  await page.goto('/p/PHAROS/PHAROS-12?view=full')
  await expect(page.getByRole('article', { name: 'Ticket details' }).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await page.keyboard.press('e')
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
