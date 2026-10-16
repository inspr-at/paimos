// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1441 risks: the capability stays in the address or reaches a request
// line; loading shifts the sheet when the draft arrives; a toggle, the owner
// list or a date moves a control; the expired answer reveals the draft; the
// create sends other choices than those on screen, or the result is not the
// project it made. Every state is drawn at 1440 and 390, light and dark, in
// both languages where text length matters (screenshots for the visual hold).
import { test, expect, type Browser, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

const CAPABILITY = 'Q2FwYWJpbGl0eS1mb3ItQUVPTi0xNDQxLXRlc3RzXzA'
const POINTS = ['Online-Zahlung schon zum Start oder erst später?', 'Tagesliste für die Backstube: Ausdruck oder Tablet?', 'Abholerinnerung per SMS oder per E-Mail?', 'Was passiert mit nicht abgeholten Bestellungen?']
const lena = { id: '22222222-2222-4222-8222-222222222222', name: 'Lena Huber' }
const draft = (language: 'de' | 'en') => ({
  status: 'ready', language, handed_over_at: '2026-10-16T07:42:00Z', expires_at: '2026-10-16T08:12:00Z',
  source: { product: 'aithema', host: 'start.example.com' }, title: 'Vorbestell-App für die Bäckerei',
  summary: 'Eine Bäckerei mit zwei Filialen möchte, dass Stammkundinnen und Stammkunden Brot und Gebäck bis 18 Uhr am Vortag vorbestellen und am Morgen ohne Warten abholen. Bezahlt wird vorerst bei der Abholung, eine Online-Zahlung ist für später denkbar. Die Backstube braucht eine einfache Tagesliste, damit weniger übrig bleibt und nichts fehlt.',
  facts: ['Zwei Filialen, gebacken wird in der Hauptfiliale', 'Rund 120 Vorbestellungen pro Woche, heute per Telefon', 'Bestellschluss 18 Uhr am Vortag, Abholung ab 6:30 Uhr', 'Etwa 40 Artikel, das Sortiment wechselt saisonal', 'An der Theke steht ein Tablet für das Personal', 'Die Kasse hat keine Schnittstelle, nur einen CSV-Export'],
  open_points: POINTS, messages: 23,
})

type Gate = { open: () => void; until: Promise<void> }
const gate = (): Gate => { let open!: () => void; const until = new Promise<void>(resolve => { open = resolve }); return { open, until } }

async function setup(page: Page, options: { language?: 'de' | 'en'; gone?: boolean; denied?: boolean; holdRead?: Gate; holdCreate?: Gate; theme: 'light' | 'dark' }) {
  const data = fixtures(), errors = watchErrors(page)
  // Workspace admins and built-in members read members; the mock grants it to admin.
  await mockWork(page, data, { admin: true })
  const seen = { reads: [] as { header: string | null; url: string }[], creates: [] as Record<string, unknown>[] }
  await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: options.theme } }))
  await page.route('**/api/members', route => route.fulfill({ json: { people: [
    { principal_id: me.id, name: me.name, status: 'active' }, { principal_id: lena.id, name: lena.name, status: 'active' },
    { principal_id: '33333333-3333-4333-8333-333333333333', name: 'Former Colleague', status: 'deactivated' },
  ], agents: [], invites: [], imported: [], owner_count: 1 } }))
  await page.route('**/api/import/project-draft', async route => {
    seen.reads.push({ header: route.request().headers()['aeon-import-draft'] ?? null, url: route.request().url() })
    if (options.holdRead) await options.holdRead.until
    if (options.gone) return route.fulfill({ status: 404, json: { error: 'This handoff has expired.', code: 'draft_gone' } })
    if (options.denied) return route.fulfill({ status: 403, json: { error: 'permission denied' } })
    return route.fulfill({ json: draft(options.language ?? 'en') })
  })
  await page.route('**/api/import/project-draft/create', async route => {
    const body = route.request().postDataJSON() as { name: string; key: string; open_points: number[] }
    seen.creates.push({ ...body, header: route.request().headers()['aeon-import-draft'] })
    if (options.holdCreate) await options.holdCreate.until
    const at = '2026-10-16T08:00:00Z', id = 'p-vafdb'
    const project = { id, key: `${body.key}-1`, kind_id: 'k-project', title: body.name, body: '', state: 'active', fields: { project_key: body.key }, parent_id: null, position: '1', created_at: at, updated_at: at }
    data.projects.push({ id, key: project.key, title: body.name, state: 'active', classic: body.key, description: '', last: at })
    body.open_points.forEach((index, n) => data.nodes.push({ id: `n-vafdb-${n}`, key: `${body.key}-${n + 2}`, kind_slug: 'ticket', title: POINTS[index]!, state: 'new', project: id, parent_id: id, body: '', fields: {}, created_at: at, updated_at: at }))
    return route.fulfill({ status: 201, json: { project, tickets: body.open_points.length, replayed: false } })
  })
  return { data, errors, seen }
}

async function context(browser: Browser, width: number, theme: 'light' | 'dark') {
  return browser.newContext({ viewport: { width, height: width < 600 ? 844 : 900 }, hasTouch: width < 600, colorScheme: theme, reducedMotion: 'reduce', timezoneId: 'Europe/Vienna', locale: 'en-GB' })
}

const sheet = (page: Page) => page.locator('dialog.project-settings-sheet')

// Approved craft rules, checked on the drawn sheet: no italics, readable text
// (4.5:1 against the sheet), and 44 px targets on touch screens.
async function craft(page: Page, touch: boolean) {
  const result = await sheet(page).evaluate((dialog, touch) => {
    const rgb = (value: string) => {
      const numbers = value.match(/[\d.]+/g)?.map(Number) ?? []
      return value.startsWith('color(srgb') ? numbers.slice(0, 3).map(n => n * 255) : numbers.slice(0, 3)
    }
    const lum = (c: number[]) => { const [r, g, b] = c.map(v => { const s = v / 255; return s <= .03928 ? s / 12.92 : ((s + .055) / 1.055) ** 2.4 }); return .2126 * r! + .7152 * g! + .0722 * b! }
    const ratio = (a: number[], b: number[]) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x! + .05) / (y! + .05) }
    const background = rgb(getComputedStyle(dialog).backgroundColor)
    const low: string[] = [], italic: string[] = [], small: string[] = []
    for (const el of dialog.querySelectorAll<HTMLElement>('*')) {
      const style = getComputedStyle(el)
      if (style.fontStyle === 'italic') italic.push(el.className || el.tagName)
      const own = [...el.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent!.trim())
      if (own && el.offsetParent && !el.closest('.btn.primary, [aria-hidden="true"], .keycap, .avatar') && ratio(rgb(style.color), background) < 4.5) low.push(`${el.className || el.tagName}: ${el.textContent!.trim().slice(0, 30)}`)
    }
    if (touch) for (const el of dialog.querySelectorAll<HTMLElement>('button, .tk-all, .tk, input.field')) {
      const box = el.getBoundingClientRect()
      if (box.width && box.height && box.height < 43.5 && !el.matches('.link-btn')) small.push(`${el.id || el.className}: ${box.height}`)
    }
    return { low, italic, small }
  }, touch)
  expect(result.italic, 'no italics').toEqual([])
  expect(result.low, 'contrast 4.5:1').toEqual([])
  expect(result.small, '44 px targets').toEqual([])
}

async function shoot(page: Page, testInfo: TestInfo, state: string, width: number, theme: string) {
  await page.screenshot({ path: testInfo.outputPath(`${state}-${width}-${theme}.png`) })
}

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1441: from START, loading to created at ${width} ${theme}`, async ({ browser }, testInfo) => {
    test.setTimeout(120_000)
    const ctx = await context(browser, width, theme)
    try {
      const page = await ctx.newPage()
      const read = gate(), create = gate()
      const world = await setup(page, { holdRead: read, holdCreate: create, theme })
      await page.goto(`/new-project#draft=${CAPABILITY}&lang=en`)
      // The capability leaves the address at once and travels only in a header.
      await expect(sheet(page).getByRole('heading', { name: 'New project from START' })).toBeVisible()
      expect(page.url()).not.toContain(CAPABILITY)
      expect(new URL(page.url()).pathname).toBe('/new-project')
      await expect(sheet(page).locator('.tk.sk')).toHaveCount(4)
      await expect(page.locator('#np-go')).toBeDisabled()
      await shoot(page, testInfo, 'loading', width, theme)

      // Loading to ready: 0 px for every control when the draft has four points.
      const along = width < 600 ? sheet(page).locator('.wcl-toggle') : sheet(page).locator('#np-wcl-h')
      const arrive = await controlStability(page, {
        name: sheet(page).locator('.np-f').nth(0), key: sheet(page).locator('.np-f').nth(1), owner: page.locator('#np-owner'), date: page.locator('#np-date'),
        all: page.locator('#np-tk-all'), fourth: sheet(page).locator('.tk-list > li').nth(3), along, cancel: sheet(page).getByRole('button', { name: /^Cancel/ }), create: page.locator('#np-go'),
      })
      await arrive.check(async () => { read.open(); await expect(page.locator('#np-name')).toHaveValue('Vorbestell-App für die Bäckerei') })
      arrive.done()
      expect(world.seen.reads.every(r => r.header === CAPABILITY && !r.url.includes(CAPABILITY))).toBe(true)
      await expect(page.locator('#np-key')).toHaveValue('VAFDB')
      await expect(sheet(page).locator('.np-from')).toHaveText('From your conversation on start.example.com · 16 Oct, 09:42')
      await expect(sheet(page).locator('.cnt')).toHaveText('4 of 4')
      await expect(page.locator('#np-owner')).toContainText(me.name)
      await craft(page, width < 600)
      await shoot(page, testInfo, 'ready', width, theme)

      // Choices never move a control: points, the parent box, the owner list, a date.
      const stable = await controlStability(page, {
        name: page.locator('#np-name'), key: page.locator('#np-key'), owner: page.locator('#np-owner'), date: page.locator('#np-date'),
        all: page.locator('#np-tk-all'), second: sheet(page).locator('.tk-list .check-box').nth(1), along, cancel: sheet(page).getByRole('button', { name: /^Cancel/ }), create: page.locator('#np-go'),
      })
      await stable.check(() => sheet(page).locator('.tk-list .check-box').nth(1).click())
      await expect(sheet(page).locator('.cnt')).toHaveText('3 of 4')
      expect(await page.locator('#np-tk-all').evaluate(box => (box as HTMLInputElement).indeterminate)).toBe(true)
      await stable.check(() => page.locator('#np-tk-all').click())
      await expect(sheet(page).locator('.cnt')).toHaveText('4 of 4')
      await stable.check(() => sheet(page).locator('.tk-list .check-box').nth(1).click())
      await stable.check(() => page.locator('#np-owner').click())
      await expect(sheet(page).getByRole('menuitemradio')).toHaveCount(2)
      await shoot(page, testInfo, 'owner', width, theme)
      // Escape closes the list first and keeps the sheet; the list opens again.
      await page.keyboard.press('Escape')
      await expect(sheet(page).getByRole('menuitemradio')).toHaveCount(0)
      await expect(sheet(page)).toBeVisible()
      await expect(page.locator('#np-owner')).toBeFocused()
      await page.locator('#np-owner').click()
      await stable.check(async () => { await sheet(page).getByRole('menuitemradio', { name: /Lena Huber/ }).click(); await expect(page.locator('#np-owner')).toContainText('Lena Huber') })
      await stable.check(async () => {
        await sheet(page).locator('input[type="date"]').evaluate(input => { (input as HTMLInputElement).value = '2026-11-30'; input.dispatchEvent(new Event('change', { bubbles: true })) })
        await expect(page.locator('#np-date')).toContainText('30 Nov 2026')
      })
      await stable.check(async () => {
        if (width < 600) await along.click()
        else await sheet(page).getByRole('button', { name: 'Show all' }).click()
        await expect(sheet(page).locator('.pv-sum')).toBeVisible()
      })
      await shoot(page, testInfo, 'chosen', width, theme)

      // Creating: fields lock, the button keeps its width; Cancel stays usable.
      await stable.check(async () => { await page.locator('#np-go').click(); await expect(page.locator('#np-go')).toHaveAttribute('aria-busy', 'true') })
      stable.done()
      await expect(page.locator('#np-name')).toBeDisabled()
      await expect(sheet(page).getByRole('button', { name: /^Cancel/ })).toBeEnabled()
      await shoot(page, testInfo, 'creating', width, theme)
      expect(world.seen.creates).toEqual([{ name: 'Vorbestell-App für die Bäckerei', key: 'VAFDB', owner_id: lena.id, target_date: '2026-11-30', open_points: [0, 2, 3], header: CAPABILITY }])

      create.open()
      await expect(page).toHaveURL(/\/p\/VAFDB\/tickets$/)
      await expect(page.getByText('Project created from START · 3 tickets')).toBeVisible()
      await expect(sheet(page)).toHaveCount(0)
      await shoot(page, testInfo, 'created', width, theme)
      expect(await page.evaluate(() => sessionStorage.getItem('aeon.importDraft'))).toBeNull()
      expect(world.errors).toEqual([])
    } finally { await ctx.close() }
  })

  test(`AEON-1441: expired handoff and German at ${width} ${theme}`, async ({ browser }, testInfo) => {
    test.setTimeout(90_000)
    const ctx = await context(browser, width, theme)
    try {
      const page = await ctx.newPage()
      const world = await setup(page, { gone: true, theme })
      await page.goto(`/new-project#draft=${CAPABILITY}&lang=en`)
      // One message for expired, used and not yours; nothing of the draft shows.
      await expect(sheet(page).getByRole('heading', { name: 'This handoff has expired.' })).toBeVisible()
      await expect(sheet(page).getByText('Start again from START.')).toBeVisible()
      await expect(sheet(page).locator('.np-from')).toHaveCount(0)
      await expect(sheet(page).locator('form')).toHaveCount(0)
      await craft(page, width < 600)
      await shoot(page, testInfo, 'expired', width, theme)
      await sheet(page).getByRole('button', { name: 'Go to Projects' }).click()
      await expect(page).toHaveURL(/\/$/)
      await expect(sheet(page)).toHaveCount(0)

      // A person who may not create projects is told so, not shown a draft.
      const denied = await ctx.newPage()
      const refused = await setup(denied, { denied: true, theme })
      await denied.goto(`/new-project#draft=${CAPABILITY}&lang=en`)
      await expect(sheet(denied).getByRole('heading', { name: 'You may not create projects here.' })).toBeVisible()
      await expect(sheet(denied).locator('form')).toHaveCount(0)

      const de = await ctx.newPage()
      const german = await setup(de, { language: 'de', theme })
      await de.goto(`/new-project#draft=${CAPABILITY}&lang=de`)
      await expect(sheet(de).getByRole('heading', { name: 'Neues Projekt aus START' })).toBeVisible()
      await expect(de.locator('#np-key')).toHaveValue('VAFDB')
      await expect(sheet(de).locator('.np-from')).toHaveText('Aus deinem Gespräch auf start.example.com · 16. Okt., 09:42')
      for (const text of ['Was du anlegst', 'Kürzel', 'Verantwortlich', 'Für jeden offenen Punkt ein Ticket anlegen', '4 von 4']) await expect(sheet(de).getByText(text, { exact: true })).toBeVisible()
      await expect(de.locator('#np-go')).toContainText('Projekt anlegen')
      await craft(de, width < 600)
      await shoot(de, testInfo, 'de', width, theme)
      expect([...world.errors, ...german.errors, ...refused.errors.filter(error => !/403/.test(error))]).toEqual([])
    } finally { await ctx.close() }
  })
}
