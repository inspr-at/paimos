// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, mockWork, me, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { NodeRecurrence } from '../src/lib/api'
import type { Recurrence } from '../src/lib/recurrences'
import { colourContrast, PORCELAIN } from '../src/lib/themeEngine'
import type { ThemeRecord } from '../src/lib/themes'

const id = '63700000-0000-4000-8000-000000000001'
const provenance: NodeRecurrence = { id, project_id: 'p-pharos', project_key: 'PRJ-17', number: 4, retired: false, trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '09:00', timezone: 'Europe/Vienna' } }
const shots = join(process.cwd(), 'test-results', 'aeon-637')
const row = (page: Page) => page.locator('#row-n-1')
const workspace = (page: Page) => page.locator('.ticket-ws')
async function setup(page: Page, manage = true, locale = 'en-GB') {
  const errors = watchErrors(page), data = fixtures(), recurrenceReads: string[] = []
  data.nodes.find(node => node.id === 'n-1')!.recurrence = structuredClone(provenance)
  data.nodes.find(node => node.id === 'n-1')!.title = 'Regelmäßige Prüfung der Website und ihrer umfangreichen Zugangsberechtigungen'
  data.nodes.find(node => node.id === 'n-4')!.fields = { recurrence_id: id, occurrence_number: 99 }
  await page.addInitScript(value => { Object.defineProperty(navigator, 'language', { get: () => value }) }, locale)
  const calls = await mockWork(page, data)
  await page.route('**/api/me/permissions**', route => {
    const grants = mockEffectivePermissions('member', new URL(route.request().url()).searchParams.get('project_id') || undefined)
    if (manage) grants.workspace.permissions.push('recurrences.manage')
    return route.fulfill({ json: grants })
  })
  await page.route('**/api/recurrences**', route => {
    recurrenceReads.push(route.request().url())
    const path = new URL(route.request().url()).pathname
    const definition: Recurrence = { id, project_id: 'p-pharos', parent_id: 'p-pharos', template: { name: 'Website audit', title: 'Website audit #{{occurrence}}', description: '', acceptance_criteria: [], estimate_hours: 0, priority: 'medium', tags: [], type: 'ticket' }, trigger: provenance.trigger, queue_each: false, overlap_policy: 'skip', catch_up_policy: 'one', paused: false, revision: 1, occurrence_count: 4, next_at: '2026-10-05T07:00:00Z', created_at: '2026-10-01T12:00:00Z', updated_at: '2026-10-01T12:00:00Z' }
    if (path === '/api/recurrences') return route.fulfill({ json: { items: [definition], next_cursor: null } })
    if (path.endsWith('/history')) return route.fulfill({ json: { items: [], next_cursor: null } })
    return route.fulfill({ json: definition })
  })
  return { data, errors, calls, recurrenceReads }
}

test('extreme primary accents keep recurrence glyphs readable without moving controls', async ({ page }, testInfo) => {
  const { errors } = await setup(page, true, 'de-AT')
  const theme: ThemeRecord = { id: 'recurrence-contrast', name: 'Recurrence contrast', scope: 'personal', tenant_id: 't1', owner_principal_id: me.id, revision: 1, created_at: '', updated_at: '',
    values: { ...PORCELAIN, primary: { light: '#ffffff', dark: '#000000' } } }
  await page.route('**/api/themes?*', route => route.fulfill({ json: { items: [theme], next_cursor: null } }))
  await page.route('**/api/me/theme', route => route.fulfill({ json: { theme, default_theme_id: 'default', selected_theme_id: theme.id, revision: 1, fallback_notice: null } }))
  await page.goto('/p/PHAROS?sort=key&group=none')
  await expect.poll(() => page.locator('#aeon-theme').textContent()).toContain('--primary: #000000;')
  const icon = row(page).locator('.ticket-type-icon'), marker = icon.locator('.recurrence-dot'), glyph = marker.locator('svg')
  for (const width of [390, 1024, 1440]) for (const mode of ['dark', 'light'] as const) {
    await page.setViewportSize({ width, height: 1000 })
    await page.evaluate(mode => { document.documentElement.dataset.theme = mode }, mode)
    await expect(glyph).toBeVisible()
    const rendered = await glyph.evaluate(el => {
      const rgba = (value: string) => {
        const canvas = document.createElement('canvas')
        canvas.width = canvas.height = 1
        const context = canvas.getContext('2d', { willReadFrequently: true })!
        context.fillStyle = value
        context.fillRect(0, 0, 1, 1)
        const channels = Array.from(context.getImageData(0, 0, 1, 1).data)
        return { hex: '#' + channels.slice(0, 3).map(channel => channel.toString(16).padStart(2, '0')).join(''), alpha: channels[3] }
      }
      const style = getComputedStyle(el), background = getComputedStyle(el.parentElement!).backgroundColor
      return { stroke: rgba(style.stroke), fill: rgba(background), gold: rgba(style.getPropertyValue('--gold').trim()) }
    })
    expect(rendered.stroke.alpha).toBe(255)
    expect(rendered.fill.alpha).toBe(255)
    expect(rendered.fill).toEqual(rendered.gold)
    expect(colourContrast(rendered.stroke.hex, rendered.fill.hex), `${width} ${mode}: recurrence glyph on its gold fill`).toBeGreaterThanOrEqual(3)
    await expectStableControls({
      controls: { 'type slot': icon, 'recurrence marker': marker, 'ticket title': row(page).locator('.title-link'), 'clicked row': row(page) },
      interactions: [{ name: 'hover marker', run: () => icon.hover() }, { name: 'keyboard focus marker', run: () => icon.focus() }],
      scrollAreas: { list: page.locator('.table-card') },
    })
    await page.screenshot({ path: testInfo.outputPath(`recurrence-contrast-${width}-${mode}.png`) })
  }
  expect(errors).toEqual([])
})

test.describe('touch permission changes', () => {
  test.use({ hasTouch: true })

  for (const width of [1024, 1440]) {
    test(`coarse-pointer list keeps the recurring marker and title still at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      const { errors } = await setup(page)
      await page.goto('/p/PHAROS?sort=key&group=none')
      expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true)
      const icon = row(page).locator('.ticket-type-icon')
      await expect(icon.locator('.recurrence-dot')).toBeVisible()
      await expectStableControls({
        controls: { 'type slot': icon, 'ticket title': row(page).locator('.title-link'), 'clicked row': row(page) },
        interactions: [
          { name: 'hover marker', run: () => icon.hover() },
          { name: 'keyboard focus marker', run: () => icon.focus() },
        ],
        scrollAreas: { list: page.locator('.table-card') },
      })
      expect(errors).toEqual([])
    })
  }

  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) for (const change of ['grant', 'revoke'] as const) {
    test(`${change} keeps the recurring pill and ticket content still at ${width}px in ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ colorScheme: theme })
      const { data, errors } = await setup(page, true, 'de-AT')
      const ticket = data.nodes.find(node => node.id === 'n-1')!
      ticket.project = 'p-aeon'; ticket.parent_id = 'p-aeon'; ticket.key = 'AEON-99'
      let allowed = change === 'revoke'
      let releasePermissions!: () => void
      const permissionBarrier = new Promise<void>(resolve => { releasePermissions = resolve })
      let permissionRequested!: () => void
      const requested = new Promise<void>(resolve => { permissionRequested = resolve })
      await page.route('**/api/me/permissions**', async route => {
        if (new URL(route.request().url()).searchParams.get('project_id') !== 'p-pharos') return route.fallback()
        permissionRequested()
        if (change === 'grant') await permissionBarrier
        const grants = mockEffectivePermissions('member', 'p-pharos')
        if (allowed) grants.workspace.permissions.push('recurrences.manage')
        await route.fulfill({ json: grants })
      })
      try {
        await page.goto('/p/AEON/AEON-99')
        await requested
        expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true)
        const pill = workspace(page).locator('.recurring-pill')
        const line = workspace(page).locator('.recurrence-provenance')
        await expect(line).toContainText('Website audit')
        await expect(workspace(page).locator(`${allowed ? 'a' : 'span'}.recurring-pill`)).toBeVisible()
        mkdirSync(shots, { recursive: true })
        const screenshot = (state: string) => page.screenshot({ path: join(shots, `ticket-touch-${change}-${state}-${width}-${theme}.png`) })
        await screenshot('before')
        await expectStableControls({
          controls: {
            'recurring pill': pill,
            'header actions': workspace(page).getByRole('button', { name: 'More actions', exact: true }),
            'copy key': workspace(page).getByRole('button', { name: 'Copy AEON-99', exact: true }),
            'provenance below the header': line,
          },
          scrollAreas: { ticket: workspace(page) },
          interactions: [{ name: `${change} source management permission`, run: async () => {
            allowed = change === 'grant'
            if (allowed) releasePermissions()
            else await page.evaluate(() => window.dispatchEvent(new Event('focus')))
            await expect(workspace(page).locator(`${allowed ? 'a' : 'span'}.recurring-pill`)).toBeVisible()
            await expect(line.getByRole('link', { name: 'Website audit', exact: true })).toHaveCount(allowed ? 1 : 0)
          } }],
        })
        expect((await pill.boundingBox())!.height).toBeGreaterThanOrEqual(44)
        await expect(pill).toContainText('Wiederkehrend')
        await screenshot('after')
        expect(errors).toEqual([])
      } finally { releasePermissions() }
    })
  }
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`option B has a stable 22 px slot and header at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const locale = width === 390 ? 'de-AT' : 'en-GB'
    const { data, errors, recurrenceReads } = await setup(page, true, locale)
    const label = width === 390 ? 'Wiederkehrend · jeden Montag · Nr. 4' : 'Recurring · every Monday · #4'
    await page.goto('/p/PHAROS?sort=key&group=none')
    await expect(row(page).getByRole('img', { name: label, exact: true })).toBeVisible()
    await expect(page.locator('#row-n-4 .recurrence-dot')).toHaveCount(0)
    for (const icon of await page.locator('.ticket-row .ticket-type-icon').all()) {
      expect((await icon.boundingBox())!.width).toBe(22)
    }
    const icon = row(page).locator('.ticket-type-icon'), title = row(page).locator('.title-link')
    await expect(icon).toHaveAttribute('title', label)
    const geometry = await icon.locator('.recurrence-dot').evaluate(el => ({ width: el.getBoundingClientRect().width, height: el.getBoundingClientRect().height, ring: getComputedStyle(el).boxShadow, gold: getComputedStyle(el).backgroundColor, token: getComputedStyle(document.documentElement).getPropertyValue('--gold').trim() }))
    expect(geometry.width).toBe(11); expect(geometry.height).toBe(11); expect(geometry.ring).toContain('1.5px')
    expect(geometry.gold).toBe(await page.evaluate(() => { const el = document.createElement('span'); el.style.color = 'var(--gold)'; document.body.append(el); const color = getComputedStyle(el).color; el.remove(); return color }))
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `list-${width}-${theme}.png`) })
    await expectStableControls({
      controls: { 'type slot': icon, 'ticket title': title, 'clicked row': row(page) },
      interactions: [
        { name: 'hover marker', run: () => icon.hover() },
        { name: 'keyboard focus marker', run: () => icon.focus() },
        { name: 'ordinary ticket in the same slot', run: async () => { data.nodes.find(node => node.id === 'n-1')!.recurrence = undefined; await page.reload(); await expect(row(page).locator('.recurrence-dot')).toHaveCount(0) } },
        { name: 'restore recurring ticket', run: async () => { data.nodes.find(node => node.id === 'n-1')!.recurrence = structuredClone(provenance); await page.reload(); await expect(row(page).locator('.recurrence-dot')).toBeVisible() } },
      ],
      scrollAreas: { list: page.locator('.table-card') },
    })
    expect(recurrenceReads).toEqual([])
    await page.goto('/p/PHAROS/PHAROS-11')
    const pill = workspace(page).getByRole('link', { name: label, exact: true })
    await expect(pill).toBeVisible()
    await expect(pill).toHaveAttribute('href', `/p/PRJ-17/settings?recurrence=${id}`)
    await page.screenshot({ path: join(shots, `ticket-${width}-${theme}.png`) })
    const more = workspace(page).getByRole('button', { name: 'More actions', exact: true })
    await expectStableControls({ controls: { 'header actions': more, 'copy key': workspace(page).getByRole('button', { name: 'Copy PHAROS-11', exact: true }), 'recurring pill': pill }, interactions: [{ name: 'focus pill', run: () => pill.focus() }, { name: 'open action menu', run: async () => { await more.click(); await expect(page.getByRole('menuitem', { name: /Repeat/ })).toBeVisible() } }, { name: 'close action menu', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('menuitem', { name: /Repeat/ })).toHaveCount(0) } }] })
    const status = workspace(page).getByRole('button', { name: /Status: In progress/ })
    await status.click()
    await page.getByRole('menuitem', { name: 'What do these mean?' }).click()
    const sheet = page.getByRole('dialog', { name: 'What the statuses mean' })
    await expect(sheet.locator('tr.recurring')).toContainText('occurrence number')
    await sheet.locator('tr.recurring').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(shots, `status-help-${width}-${theme}.png`) })
    await expectStableControls({ controls: { 'help close': sheet.getByRole('button', { name: 'Close the status help' }) }, interactions: [{ name: 'focus sample marker', run: () => sheet.locator('tr.recurring .ticket-type-icon').focus() }], scrollAreas: { 'help body': sheet.locator('.sheet-card') } })
    await page.keyboard.press('Escape')
    await pill.click()
    await expect(page).toHaveURL(new RegExp(`/p/PRJ-17/settings\\?recurrence=${id}$`))
    await expect(page.getByRole('heading', { name: 'Recurring work', exact: true })).toBeVisible()
    await expect(page.locator(`#recurrence-${id}`)).toContainText('Website audit')
    await page.screenshot({ path: join(shots, `settings-${width}-${theme}.png`) })
    expect(errors).toEqual([])
  })
}

test('readers and retired sources keep a marker without a management link', async ({ page }) => {
  const { data, errors } = await setup(page, false)
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(workspace(page).getByRole('img', { name: 'Recurring · every Monday · #4' })).toBeVisible()
  await expect(workspace(page).locator('a.recurring-pill')).toHaveCount(0)
  data.nodes.find(node => node.id === 'n-1')!.recurrence!.retired = true
  await page.reload()
  await expect(workspace(page).locator('.recurring-pill')).toBeVisible()
  await expect(workspace(page).locator('a.recurring-pill')).toHaveCount(0)
  await page.goto('/p/PHAROS/PHAROS-14')
  await expect(workspace(page).locator('.recurring-pill')).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a manager cannot follow a retired source and header controls stay still across tickets', async ({ page }) => {
  const { data, errors } = await setup(page)
  data.nodes.find(node => node.id === 'n-1')!.recurrence!.retired = true
  await page.setViewportSize({ width: 390, height: 1000 })
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(workspace(page).getByRole('img', { name: 'Recurring · every Monday · #4' })).toBeVisible()
  await expect(workspace(page).locator('a.recurring-pill')).toHaveCount(0)
  await expectStableControls({ controls: { 'header actions': workspace(page).getByRole('button', { name: 'More actions', exact: true }), 'copy key': workspace(page).locator('.key-chip') }, interactions: [
    { name: 'ordinary ticket in series', run: async () => { await page.goto('/p/PHAROS/PHAROS-14'); await expect(workspace(page).locator('.recurring-pill')).toHaveCount(0) } },
    { name: 'recurring ticket in series', run: async () => { await page.goto('/p/PHAROS/PHAROS-11'); await expect(workspace(page).locator('.recurring-pill')).toBeVisible() } },
  ] })
  expect(errors).toEqual([])
})

for (const projection of ['retired', 'withheld'] as const) {
  test(`authoritative GET ${projection} provenance refreshes a cached list row at the same revision`, async ({ page }) => {
    const { data, errors, calls } = await setup(page)
    await page.goto('/p/PHAROS?sort=key&group=none')
    await expect(row(page).locator('.recurrence-dot')).toBeVisible()
    const ticket = data.nodes.find(node => node.id === 'n-1')!
    await page.route('**/api/nodes/n-1', route => {
      if (route.request().method() !== 'GET') return route.fallback()
      const { recurrence: _source, project: _project, kind_slug: kind, ...node } = ticket
      return route.fulfill({ json: { ...node, kind_id: `k-${kind}`, position: '0', deleted_at: null, ...(projection === 'retired' ? { recurrence: { ...provenance, retired: true } } : {}) } })
    })
    await row(page).locator('.title-link').click()
    await expect(workspace(page)).toBeVisible()
    if (projection === 'retired') {
      await expect(workspace(page).locator('.recurring-pill')).toBeVisible()
      await expect(workspace(page).locator('a.recurring-pill')).toHaveCount(0)
      await expect(workspace(page).locator('.recurrence-provenance')).toContainText('a deleted recurrence')
    } else {
      await expect(workspace(page).locator('.recurring-pill')).toHaveCount(0)
      await expect(workspace(page).locator('.recurrence-provenance')).toHaveCount(0)
      await expect(row(page).locator('.recurrence-dot')).toHaveCount(0)
    }
    expect(calls.filter(call => call.path === '/api/nodes' && call.method === 'GET').length).toBeGreaterThan(0)
    expect(errors).toEqual([])
  })
}

test('editable metadata cannot invent ticket provenance or a source-edit action', async ({ page }) => {
  const { errors, recurrenceReads } = await setup(page)
  await page.goto('/p/PHAROS/PHAROS-14')
  await expect(workspace(page)).toBeVisible()
  await expect(workspace(page).locator('.recurrence-provenance')).toHaveCount(0)
  await workspace(page).getByRole('button', { name: 'More actions', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /Repeat/ })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true })).toHaveCount(0)
  expect(recurrenceReads).toEqual([])
  expect(errors).toEqual([])
})

test('a linked source outside page one is selected directly and stays selected when more rows arrive', async ({ page }) => {
  const { errors } = await setup(page)
  const definition = (sourceId: string, name: string, projectId = 'p-pharos'): Recurrence => ({ id: sourceId, project_id: projectId, parent_id: projectId, template: { name, title: name, description: '', acceptance_criteria: [], estimate_hours: 0, priority: 'medium', tags: [], type: 'ticket' }, trigger: provenance.trigger, queue_each: false, overlap_policy: 'skip', catch_up_policy: 'one', paused: false, revision: 1, occurrence_count: 4, next_at: null, created_at: '2026-10-01T12:00:00Z', updated_at: '2026-10-01T12:00:00Z' })
  const target = definition(id, 'Linked source beyond page one')
  let gets = 0
  await page.route('**/api/recurrences**', route => {
    const url = new URL(route.request().url()), path = url.pathname
    if (path === '/api/recurrences') return route.fulfill({ json: url.searchParams.has('after') ? { items: [target], next_cursor: null } : { items: Array.from({ length: 100 }, (_, index) => definition(`first-${index}`, `First page ${index}`)), next_cursor: 'page-two' } })
    if (path === `/api/recurrences/${id}`) { gets++; return route.fulfill({ json: target }) }
    if (path.endsWith('/history')) return route.fulfill({ json: { items: [], next_cursor: null } })
    if (path.endsWith('/preview')) return route.fulfill({ json: { times: [], trigger_kind: 'time' } })
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.goto(`/p/PHAROS/settings?recurrence=${id}`)
  const history = page.getByRole('region', { name: 'Recurrence history' })
  await expect(history.getByRole('heading', { name: target.template.name, exact: true })).toBeVisible()
  expect(gets).toBe(1)
  await expect(page.locator('#recurrence-first-0')).not.toHaveClass(/selected/)
  await expectStableControls({ controls: { 'New recurring work': page.getByRole('button', { name: 'New…', exact: true }), 'first page row': page.locator('#recurrence-first-0'), 'first page actions': page.locator('#recurrence-first-0 .row-actions') }, interactions: [{ name: 'load the second page', run: async () => {
    await page.getByRole('button', { name: 'More recurring work', exact: true }).click()
    await expect(page.locator(`#recurrence-${id}`)).toHaveClass(/selected/)
    await expect(history.getByRole('heading', { name: target.template.name, exact: true })).toBeVisible()
  } }] })
  expect(errors).toEqual([])
})

test('moved ticket provenance and source editing use the receipt source project despite spoofed metadata', async ({ page }) => {
  const { data, errors } = await setup(page)
  const ticket = data.nodes.find(node => node.id === 'n-1')!
  ticket.project = 'p-aeon'; ticket.parent_id = 'p-aeon'; ticket.key = 'AEON-99'
  ticket.fields.recurrence_id = 'editable-fake'; ticket.fields.occurrence_number = 99
  await page.goto('/p/AEON/AEON-99')
  const line = workspace(page).locator('.recurrence-provenance')
  await expect(line).toContainText('#4')
  await expect(line.getByRole('link', { name: 'Website audit' })).toHaveAttribute('href', `/p/PRJ-17/settings?recurrence=${id}`)
  await workspace(page).getByRole('button', { name: 'More actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true }).click()
  const editor = page.getByRole('dialog', { name: 'Edit recurring work', exact: true })
  await expect(editor).toBeVisible()
  await expect(editor.getByRole('button', { name: 'Parent', exact: true })).toContainText('No parent (top level of the project)')
  await expect(editor.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue('Website audit')
  expect(errors).toEqual([])
})

test('withheld provenance closes an open source editor and clears its source-edit action', async ({ page }) => {
  await page.addInitScript(() => {
    const streams: Stream[] = []
    class Stream extends EventTarget {
      readyState = 1
      onerror: (() => void) | null = null
      close() { this.readyState = 2 }
      constructor() {
        super(); streams.push(this)
        queueMicrotask(() => this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 40, resumed: false }), lastEventId: '40' })))
      }
    }
    Object.assign(window, { EventSource: Stream, aeon637Streams: streams })
  })
  const { data, errors } = await setup(page)
  const ticket = data.nodes.find(node => node.id === 'n-1')!
  ticket.fields.recurrence_id = id; ticket.fields.occurrence_number = 4
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(workspace(page).locator('.recurrence-provenance')).toContainText('Website audit')
  const more = workspace(page).getByRole('button', { name: 'More actions', exact: true })
  await more.click()
  await page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true }).click()
  const editor = page.getByRole('dialog', { name: 'Edit recurring work', exact: true })
  await expect(editor).toBeVisible()
  ticket.recurrence = undefined
  await page.evaluate(() => {
    const streams = (window as unknown as { aeon637Streams: EventTarget[] }).aeon637Streams
    if (!streams.length) throw new Error('No live stream to exercise the refresh')
    for (const stream of streams) stream.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 41, resumed: false }), lastEventId: '41' }))
  })
  await expect(editor).toHaveCount(0)
  await expect(workspace(page).locator('.recurrence-provenance')).toHaveCount(0)
  await more.click()
  await expect(page.getByRole('menuitem', { name: /Repeat/ })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a source link for another project exposes the error without selecting a substitute', async ({ page }) => {
  const { errors } = await setup(page)
  await page.goto(`/p/AEON/settings?recurrence=${id}`)
  await expect(page.getByRole('alert')).toContainText('This recurrence belongs to another project.')
  await expect(page.getByRole('region', { name: 'Recurrence history' })).toHaveCount(0)
  expect(errors).toEqual([])
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`delayed source permissions keep the edit action and stable header at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const { data, errors, recurrenceReads } = await setup(page, true, 'de-AT')
    const ticket = data.nodes.find(node => node.id === 'n-1')!
    ticket.project = 'p-aeon'; ticket.parent_id = 'p-aeon'; ticket.key = 'AEON-99'
    let releasePermissions!: () => void
    const permissionBarrier = new Promise<void>(resolve => { releasePermissions = resolve })
    let permissionRequested!: () => void
    const requested = new Promise<void>(resolve => { permissionRequested = resolve })
    await page.route('**/api/me/permissions**', async route => {
      if (new URL(route.request().url()).searchParams.get('project_id') !== 'p-pharos') return route.fallback()
      permissionRequested()
      await permissionBarrier
      const grants = mockEffectivePermissions('member', 'p-pharos')
      grants.workspace.permissions.push('recurrences.manage')
      await route.fulfill({ json: grants })
    })
    try {
      await page.goto('/p/AEON/AEON-99')
      await requested
      const line = workspace(page).locator('.recurrence-provenance')
      await expect(line).toContainText('Website audit')
      await expect(line.getByRole('link', { name: 'Website audit' })).toHaveCount(0)
      const more = workspace(page).getByRole('button', { name: 'More actions', exact: true })
      await more.click()
      await expect(page.getByRole('menuitem', { name: /Repeat/ })).toBeVisible()
      await expect(page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true })).toBeVisible()
      await expect(page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true })).toBeDisabled()
      await page.keyboard.press('Escape')
      const sourceReads = () => recurrenceReads.filter(url => new URL(url).pathname === `/api/recurrences/${id}`)
      expect(sourceReads()).toHaveLength(1)
      await expectStableControls({
        controls: { 'header actions': more, 'copy key': workspace(page).getByRole('button', { name: 'Copy AEON-99', exact: true }), 'recurring pill': workspace(page).locator('.recurring-pill') },
        interactions: [{ name: 'grant delayed source permissions', run: async () => {
          releasePermissions()
          await expect(line.getByRole('link', { name: 'Website audit' })).toBeVisible()
          await expect(workspace(page).locator('a.recurring-pill')).toBeVisible()
        } }],
      })
      await more.click()
      const edit = page.getByRole('menuitem', { name: 'Edit Website audit…', exact: true })
      await expect(edit).toBeVisible()
      await expect(edit).toBeEnabled()
      // Permission loading uses the already loaded details, with no extra read.
      expect(sourceReads()).toHaveLength(1)
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: join(shots, `ticket-delayed-permissions-${width}-${theme}.png`) })
      await edit.click()
      const editor = page.getByRole('dialog', { name: 'Edit recurring work', exact: true })
      await expect(editor).toBeVisible()
      await expect(editor.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue('Website audit')
      expect(sourceReads()).toHaveLength(2)
      expect(errors).toEqual([])
    } finally { releasePermissions() }
  })
}
