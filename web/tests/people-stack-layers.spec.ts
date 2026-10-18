// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1403 (Markus, 2026-10-15): the faces in a projects-page people stack
// overlap on purpose, and each face is one unit: its disc, image, rim and the
// surface ring around it. The face on top covers the face behind it whole, so
// neither the image nor the outline of the face behind shows through it (an
// agent's tinted disc once let the photo behind it shine through, between its
// outline and its robot). Every face shows the same rim, photos included.
// Hit testing alone cannot see a face that lets the one behind shine through,
// so the colours are read from screenshots as well.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

test.use({ deviceScaleFactor: 2 })

const MIRA = '22222222-2222-4222-8222-222222222222'
const lead = { id: 'a1a1a1a1-0000-4000-8000-000000000001', name: 'aeon-lead', kind: 'agent' as const }
const builder = { id: 'a1a1a1a1-0000-4000-8000-000000000002', name: 'pharos-builder', kind: 'agent' as const }
const photo = { id: MIRA, name: 'Mira Holm', kind: 'person' as const, has_avatar: true }
const initials = { id: me.id, name: me.name, kind: 'person' as const }
// Markus's report (35 %: bot, photo; 84 %: bot, bot, photo), and a photo and
// initials over a bot. Newest first: the first face is on top.
const STACKS: Record<string, { id: string; name: string; kind: 'person' | 'agent'; has_avatar?: boolean }[]> = {
  'p-pharos': [lead, photo],
  'p-aeon': [lead, builder, photo],
  'p-frozen': [photo, initials, lead],
}
const tip = (id: string) => `Recently active: ${STACKS[id]!.map(person => person.name).join(', ')}`
// A loud picture: anything of it showing through a face on top is unmistakable.
const PICTURE = '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="#d81b60"/></svg>'

async function mockStacks(page: Page) {
  const data = fixtures()
  // Lists narrower than 1500 px show People only when chosen.
  data.preferences.projects = { columns: { visible: ['open', 'doing', 'done', 'progress', 'people', 'activity'] } }
  await mockWork(page, data)
  // Registered after the API mock, so they answer first.
  await page.route('**/api/people/*/avatar/*', route => route.fulfill({ contentType: 'image/svg+xml', body: PICTURE }))
  await page.route(url => url.pathname === '/api/projects', route => route.request().method() !== 'GET' ? route.fallback() : route.fulfill({ json: {
    items: data.projects.filter(p => p.state !== 'archived').map(p => ({
      id: p.id, key: p.key, title: p.title, state: p.state, open: 4, in_progress: 2, done: 9, cancelled: 0, archived_count: 0, total: 15,
      status_counts: [], status_counts_truncated: false, last_activity: p.last, people: STACKS[p.id] ?? [],
    })),
  } }))
}

type Point = { x: number; y: number }
type Face = { left: number; right: number; top: number; bottom: number; cx: number; cy: number }

// The colour of the device pixel under each point (viewport CSS pixels), from one screenshot.
async function colours(page: Page, clip: { x: number; y: number; width: number; height: number }, points: Point[], path: string) {
  const png = await page.screenshot({ clip, path })
  return page.evaluate(async ({ b64, clip, points }) => {
    const image = new Image(); image.src = `data:image/png;base64,${b64}`; await image.decode()
    const canvas = new OffscreenCanvas(image.width, image.height), context = canvas.getContext('2d')!
    context.drawImage(image, 0, 0)
    const { data, width } = context.getImageData(0, 0, image.width, image.height), scale = image.width / clip.width
    return points.map(point => {
      const i = (Math.floor((point.y - clip.y) * scale) * width + Math.floor((point.x - clip.x) * scale)) * 4
      return [data[i]!, data[i + 1]!, data[i + 2]!]
    })
  }, { b64: png.toString('base64'), clip, points })
}
const distance = (a: number[], b: number[]) => Math.max(...a.map((channel, i) => Math.abs(channel - b[i]!)))

async function checkStack(page: Page, stack: Locator, size: number, shot: string) {
  const faces = stack.locator('.faces > .avatar')
  const count = await faces.count()
  expect(count).toBeGreaterThanOrEqual(2)
  const boxes: Face[] = []
  for (let i = 0; i < count; i++) {
    const box = (await faces.nth(i).boundingBox())!
    boxes.push({ left: box.x, right: box.x + box.width, top: box.y, bottom: box.y + box.height, cx: box.x + box.width / 2, cy: box.y + box.height / 2 })
    // The faces keep their size and overlap (0 px change).
    expect(Math.abs(box.width - size)).toBeLessThanOrEqual(.5)
    expect(Math.abs(box.height - size)).toBeLessThanOrEqual(.5)
    if (i) expect(Math.abs(box.x - boxes[i - 1]!.left - size * .8)).toBeLessThanOrEqual(.5)
  }
  // Per overlapping pair: `over` lies inside the upper face, over the face behind
  // it; `free` is the same distance from the upper face's centre, over nothing.
  // `ring` and `bare` lie in the upper face's surface ring, over the face behind
  // and over nothing.
  const pairs = boxes.slice(0, -1).map((upper, i) => {
    const lower = boxes[i + 1]!
    const over = { x: upper.right - 2.5, y: upper.cy }
    expect(over.x).toBeGreaterThan(lower.left + 1)
    return { upper: i, over, free: { x: upper.cx, y: upper.top + 2.5 }, ring: { x: upper.right + 1, y: upper.cy }, bare: { x: upper.cx, y: upper.top - 1 } }
  })
  // The face on top is the one hit where faces overlap.
  const hits = await stack.evaluate((root, pairs) => {
    const list = [...root.querySelectorAll('.faces > .avatar')]
    return pairs.map(pair => list.findIndex(face => face.contains(document.elementFromPoint(pair.over.x, pair.over.y))))
  }, pairs)
  expect(hits).toEqual(pairs.map(pair => pair.upper))

  const left = Math.floor(boxes[0]!.left - 6), top = Math.floor(Math.min(...boxes.map(b => b.top)) - 6)
  const clip = { x: left, y: top, width: Math.ceil(boxes.at(-1)!.right + 6 - left), height: Math.ceil(Math.max(...boxes.map(b => b.bottom)) + 6 - top) }
  // Zoomed (2x) shots of every stack, for the visual check.
  const rgb = await colours(page, clip, pairs.flatMap(pair => [pair.over, pair.free, pair.ring, pair.bare]), test.info().outputPath(`${shot}.png`))
  pairs.forEach((pair, i) => {
    const [over, free, ring, bare] = rgb.slice(i * 4, i * 4 + 4) as number[][]
    // Nothing of the face behind shows through the face on top…
    expect.soft(distance(over!, free!), `${shot}: face ${pair.upper + 1} lets the face behind it show through`).toBeLessThanOrEqual(3)
    // …and its surface ring runs on, unbroken, over the face behind.
    expect.soft(distance(ring!, bare!), `${shot}: face ${pair.upper + 1}'s ring breaks over the face behind it`).toBeLessThanOrEqual(3)
  })
  // Every face draws the same rim, above its image.
  const rims = await faces.evaluateAll(list => list.map(face => getComputedStyle(face, '::after').boxShadow))
  expect(rims[0]).toContain('inset')
  expect(new Set(rims).size).toBe(1)
}

const LAYOUTS = [
  { width: 1440, height: 900, views: [{ view: 'list', size: 20 }, { view: 'cards', size: 22 }] },
  { width: 390, height: 844, views: [{ view: 'cards', size: 22 }] },
] as const

for (const layout of LAYOUTS) {
  test(`AEON-1403 overlapping faces stack as whole units at ${layout.width}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: layout.width, height: layout.height })
    await mockStacks(page)
    for (const { view, size } of layout.views) {
      for (const colorScheme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
        await page.goto(`/?view=${view}`)
        await expect.poll(() => page.evaluate(() => getComputedStyle(document.documentElement).colorScheme)).toBe(colorScheme)
        for (const id of Object.keys(STACKS)) {
          const stack = page.locator(`.people[data-tip="${tip(id)}"]`)
          await stack.scrollIntoViewIfNeeded()
          await expect(stack.locator('.avatar.loaded')).toHaveCount(1)
          await checkStack(page, stack, size, `${layout.width}-${view}-${colorScheme}-${id}`)
        }
      }
    }
    expect(errors).toEqual([])
  })
}
