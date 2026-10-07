// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { fixtures, liveAgent, mockWork, type Fixtures } from './work-fixtures'

const at = (minutes: number) => new Date(Date.parse('2026-09-23T12:00:00Z') + minutes * 60_000).toISOString()
const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const ticket = (id: string, key: string, title: string) => ({ id, key, title, project_id: 'p-pharos' })

function world(): Fixtures {
  const data = fixtures()
  const set = (key: string, eta: Record<string, unknown>) => { data.nodes.find(node => node.key === key)!.eta = eta }
  set('PHAROS-11', { eta_ready_at: at(25), progress_pct: 45, ready_by: 'Ada', ready_reported_at: at(-2) })
  set('PHAROS-12', { eta_ready_at: at(-5), progress_pct: 90, ready_by: 'Beau', ready_reported_at: at(-2) })
  set('PHAROS-13', { eta_ready_at: at(40), progress_pct: 10, ready_by: 'Cleo', ready_reported_at: at(-40), ready_stale: true, eta_stale: true })
  // PHAROS-14 has no stored assignee. Two live workers: Kai started first, Uma later.
  // A stopped Aaa and an earlier coordinator Bea must not become the sort name.
  data.live.push(
    liveAgent({ project_id: 'p-pharos', session_id: 's-bea', name: 'Bea', role: 'coordinator', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 40),
    liveAgent({ project_id: 'p-pharos', session_id: 's-kai', name: 'Zed', display_label: 'Kai', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 20),
    liveAgent({ project_id: 'p-pharos', session_id: 's-uma', name: 'Uma', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 5),
    liveAgent({ project_id: 'p-pharos', session_id: 's-aaa', name: 'Aaa', phase: 'stopped', stopped_at: at(-10), stop_reason: 'completed', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 50),
  )
  // PHAROS-12 is unassigned and has no live worker. PHAROS-11 is Markus, PHAROS-13 is Mira.
  return data
}

const keys = (page: Page) => page.locator('tr.ticket-row:not(.ghost) .key').allTextContents()

test.beforeEach(async ({ page }) => {
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  // These assertions distinguish fresh values from stale estimates. Establish
  // a live stream; the JSON fixture's default stream response is not SSE.
  await page.addInitScript(() => {
    class ConnectedStream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      readyState = 1
      constructor() {
        super()
        queueMicrotask(() => {
          if (this.readyState !== 1) return
          this.onopen?.(new Event('open'))
          this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 40, resumed: false }), lastEventId: '40' }))
        })
      }
      close() { this.readyState = 2 }
    }
    Object.assign(window, { EventSource: ConnectedStream })
  })
})

test('assignee sort uses the live worker when nobody is stored, and progress and ETA stay compact', async ({ page }) => {
  const data = world()
  const sorts: string[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/api/nodes' && url.searchParams.get('sort')) sorts.push(url.searchParams.get('sort')!)
  })
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=assignee')
  await expect(page.getByRole('columnheader', { name: 'Progress' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: 'ETA' })).toBeVisible()
  // Stored names sort as written: Markus (PHAROS-11) before Mira (PHAROS-13).
  // Kai is the lead worker on PHAROS-14. Unassigned PHAROS-12 stays after them.
  await expect.poll(async () => (await keys(page)).slice(0, 4)).toEqual(['PHAROS-14', 'PHAROS-11', 'PHAROS-13', 'PHAROS-12'])

  const fresh = row(page, 'PHAROS-11')
  await expect(fresh.locator('.progress-read .pct')).toHaveText('45%')
  await expect(fresh.locator('.progress-read')).toHaveAttribute('aria-label', '45% done')
  await expect(fresh.locator('.progress-read .bar > i')).toHaveAttribute('style', /width:\s*45%/)
  await expect(fresh.locator('.eta-cell .when')).toHaveText(/^~2\d min$/)
  await expect(fresh.locator('.eta-cell')).toHaveAttribute('data-tip', /45% done/)
  await expect(fresh.locator('.eta-cell .pct')).toBeHidden()
  const overdue = row(page, 'PHAROS-12')
  await expect(overdue.locator('.progress-read .pct')).toHaveText('90%')
  await expect(overdue.locator('.eta-cell')).toHaveClass(/overdue/)
  await expect(overdue.locator('.c-progress .empty')).toHaveCount(0)
  const stale = row(page, 'PHAROS-13')
  await expect(stale.locator('.progress-read')).toHaveClass(/stale/)
  await expect(stale.locator('.progress-read')).toHaveAttribute('aria-label', /^10% done, estimate stale since \d{2}:\d{2}$/)
  await expect(stale.locator('.eta-cell')).toHaveClass(/stale/)
  await expect(stale.locator('.eta-cell svg')).toHaveCount(1)
  await expect(row(page, 'PHAROS-14').locator('.progress-read')).toHaveCount(0)
  await expect(row(page, 'PHAROS-14').locator('.c-assignee .worker-name')).toHaveText('Kai')

  await page.getByRole('columnheader', { name: 'Assignee' }).getByRole('button', { name: 'Assignee' }).click()
  await expect.poll(async () => (await keys(page)).slice(0, 4)).toEqual(['PHAROS-13', 'PHAROS-11', 'PHAROS-14', 'PHAROS-12'])

  await page.getByRole('columnheader', { name: 'Progress' }).getByRole('button', { name: 'Progress' }).click()
  await expect.poll(() => sorts.some(sort => sort.split(',').includes('progress'))).toBe(true)
  await page.getByRole('button', { name: 'Display: Display' }).click()
  const names = page.getByRole('dialog', { name: 'Display options' }).locator('.columns .name')
  const order = await names.allTextContents()
  expect(order.indexOf('Progress')).toBeGreaterThan(-1)
  expect(order.indexOf('Progress')).toBeLessThan(order.indexOf('ETA'))
})

test('progress names a stale estimate when the ETA column is hidden', async ({ page }) => {
  const data = world()
  data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'assignee', 'updated', 'progress'] }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=assignee')
  await expect(page.getByRole('columnheader', { name: 'ETA' })).toHaveCount(0)
  await expect(row(page, 'PHAROS-13').locator('.progress-read')).toHaveAttribute('aria-label', /^10% done, estimate stale since \d{2}:\d{2}$/)
  await expect(row(page, 'PHAROS-11').locator('.progress-read')).toHaveAttribute('aria-label', '45% done')
  await expect(row(page, 'PHAROS-11').locator('.c-eta')).toHaveCount(0)
})

for (const width of [1600, 390] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`progress and ETA columns at ${width}px ${theme}`, async ({ page }, testInfo) => {
      const data = world()
      data.preferences.theme = { choice: theme }
      await page.addInitScript(value => { document.documentElement.dataset.theme = value }, theme)
      await mockWork(page, data)
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await page.goto('/p/PHAROS?sort=assignee')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      await expect(row(page, 'PHAROS-11').locator('.progress-read .pct')).toHaveText('45%')
      await expect(row(page, 'PHAROS-11').locator('.eta-cell .when')).toBeVisible()
      await expect(row(page, 'PHAROS-13').locator('.eta-cell')).toHaveClass(/stale/)
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)
      expect(overflow).toBe(true)
      await page.screenshot({ path: testInfo.outputPath(`list-columns__${width}__${theme}.png`) })
    })
  }
}

// The actual shared grid as an across-project host; no test-only product route.
async function mountGroups(page: Page, folded = false) {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/tickets')
  await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  await page.evaluate(async folded => {
    const vuePath = '/node_modules/.vite/deps/vue.js'
    const tablePath = '/src/components/work/TicketTable.vue'
    const listPath = '/src/lib/ticketList.ts'
    const { createApp, h, reactive } = await import(vuePath)
    const { default: Table } = await import(tablePath)
    const iconPath = '/src/components/AppIcon.vue'
    const { default: Icon } = await import(iconPath)
    const { groupRows, selectLoadedGroup } = await import(listPath)
    const projects = [{ id: 'p-a', key: 'AEON', title: 'Gemeinsame Ticketliste mit langem deutschen Projektnamen' }, { id: 'p-b', key: 'PHAROS', title: 'Pharos' }]
    const make = (id: number, project = projects[0], long = false) => ({
      id: `host-${id}`, key: `${long ? 'LONGPROJECTKEY' : project.key}-${id}`, title: `Vorgeschlagene Änderung ${id}`,
      project_key: project.key, project, kind_slug: 'work', kind_id: 'work', kind_label: 'Work', body: '', fields: {}, state: 'open',
      priority: null, assignee: null, parent: null, parent_id: project.id, children_count: 0, is_leaf: true,
      position: String(id), created_at: '2026-09-23T10:00:00Z', updated_at: '2026-09-23T10:00:00Z',
    })
    const initial = [make(1, projects[0], true), make(2), make(3), make(4, projects[1]), make(5, projects[1])]
    const state = reactive({
      groups: groupRows(initial, 'project', { 'p-a': 120 }), group: 'project', rowsById: new Map(initial.map(row => [row.id, row])),
      cursorId: null, openId: null, query: '', sort: [], density: 'comfortable', loading: false, loadingMore: false,
      groupHandler: true, error: '', moreError: '', hasMore: false, filtered: false, hidingClosed: false, collapsed: new Set<string>(folded ? ['p-a', 'p-b'] : []), total: 122,
      scrollRoot: null, now: Date.now(), showAssignee: false, creating: false, selectable: true, selected: new Set<string>(),
      prefs: { visible: [], widths: {} as Record<string, number> },
      extraColumns: [
        { id: 'attention-kind', label: 'Art', sort: null, width: 140, min: 96, max: 240 },
        { id: 'suggestion', label: 'Vorschlag', sort: null, width: 180, min: 150, max: 340 },
        { id: 'actions', label: 'Aktionen', sort: null, width: 190, min: 160, max: 260, end: true },
      ],
    })
    const copies: { id: string; project_key: string }[] = []
    const loads: string[] = []
    const widths: Record<string, number>[] = []
    document.querySelector<HTMLElement>('#app')!.style.display = 'none'
    const root = document.createElement('main')
    root.id = 'table-host'; root.style.cssText = 'margin:24px;min-width:0'
    document.body.append(root)
    const table = createApp({ render: () => h(Table, {
      ...state,
      onToggleGroup: (key: string) => { const next = new Set(state.collapsed); if (next.has(key)) next.delete(key); else next.add(key); state.collapsed = next },
      onSelectGroup: state.groupHandler ? (key: string, on: boolean) => { state.selected = selectLoadedGroup(state.groups.find(group => group.key === key), state.selected, on) } : undefined,
      onSelect: (row: { id: string }) => { const next = new Set(state.selected); if (next.has(row.id)) next.delete(row.id); else if (next.size < 100) next.add(row.id); state.selected = next },
      onSelectAll: (on: boolean) => { state.selected = on ? new Set([...state.rowsById.keys()].slice(0, 100)) : new Set() },
      onCopy: (row: { id: string; project_key: string }) => copies.push(row),
      onWidths: (value: Record<string, number>) => { widths.push(value); state.prefs = { ...state.prefs, widths: value } },
      onMoreInGroup: (key: string) => {
        loads.push(key)
        const group = state.groups.find(group => group.key === key)
        const added = Array.from({ length: 50 }, (_, index) => make(state.rowsById.size + index + 1, projects[0], index === 0))
        group.rows.push(...added); group.loaded = group.rows.length; group.hasMore = group.loaded < group.total
        state.rowsById = new Map([...state.rowsById, ...added.map(row => [row.id, row] as const)])
      },
    }, {
      'group-end': ({ group }: { group: { key: string } }) => [h('button', { type: 'button', class: 'btn sm', 'aria-label': `Apply all in ${group.key}` }, 'Alle anwenden'), h('button', { type: 'button', class: 'icon-btn', 'aria-label': `More for ${group.key}` }, [h(Icon, { name: 'more', size: 16 })])],
      'cell-attention-kind': () => h('span', 'Vorgeschlagene Änderung'),
      'cell-suggestion': () => h('span', 'Offen → In Arbeit'),
      'cell-actions': ({ row }: { row: { id: string } }) => h('button', { class: 'btn sm', type: 'button', onClick: () => { state.rowsById.get(row.id).title = 'Angewendet · Rückgängig' } }, 'Anwenden'),
    }) })
    table._context.provides = (document.querySelector('#app') as unknown as { __vue_app__: { _context: { provides: object } } }).__vue_app__._context.provides
    table.mount(root)
    Object.assign(window, { tableGroups: { state, copies, loads, widths } })
  }, folded)
  await expect(page.locator('#table-host .group-row')).toHaveCount(2)
}

const hostState = (page: Page) => page.evaluate(() => {
  const host = (window as unknown as { tableGroups: { state: { selected: Set<string> }; copies: { project_key: string }[]; loads: string[]; widths: Record<string, number>[] } }).tableGroups
  return { selected: [...host.state.selected], copies: host.copies.map(row => row.project_key), loads: [...host.loads], widths: [...host.widths] }
})

test('AEON-913: host groups select loaded rows, paginate, link across projects and keep controls still', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mountGroups(page)
  const host = page.locator('#table-host')
  const first = host.locator('#row-group-p-a')
  await page.evaluate(() => { (window as unknown as { tableGroups: { state: { groups: { icon?: string }[] } } }).tableGroups.state.groups[0].icon = 'ticket' })
  await expect(first.locator('svg.group-icon')).toHaveCount(1)
  const check = first.getByRole('checkbox')
  const fold = first.locator('.group-toggle')
  const stable = await controlStability(page, { fold, check, head: first, apply: first.getByRole('button', { name: 'Apply all in p-a' }), more: first.getByRole('button', { name: 'More for p-a' }) })
  await stable.check(async () => { await check.check(); await expect(check).toBeChecked() })
  expect((await hostState(page)).selected).toEqual(['host-1', 'host-2', 'host-3'])
  await stable.check(async () => { await host.getByRole('checkbox', { name: 'Select LONGPROJECTKEY-1', exact: true }).uncheck(); await expect(check).not.toBeChecked(); expect(await check.evaluate(el => (el as HTMLInputElement).indeterminate)).toBe(true) })
  await stable.check(async () => { await fold.click(); await expect(host.locator('#row-host-1')).toHaveCount(0) })
  await stable.check(async () => { await check.check(); await expect(check).toBeChecked() })
  expect((await hostState(page)).selected).toHaveLength(3)
  await stable.check(async () => { await first.getByRole('button', { name: 'Expand AEON' }).click(); await expect(host.locator('#row-host-1')).toBeVisible() })
  stable.done()
  await expect(host.locator('#row-host-1 .title-link')).toHaveAttribute('href', '/p/AEON/LONGPROJECTKEY-1')
  await expect(host.locator('#row-host-4 .title-link')).toHaveAttribute('href', '/p/PHAROS/PHAROS-4')
  await host.getByRole('button', { name: 'Copy PHAROS-4', exact: true }).click()
  expect((await hostState(page)).copies).toEqual(['PHAROS'])
  await expect(host.locator('.group-more').first()).toContainText('3 of 120 shown')
  for (let i = 0; i < 2; i++) await host.getByRole('button', { name: 'Show 50 more in AEON' }).click()
  await expect(host.locator('.group-more').first()).toContainText('103 of 120 shown')
  expect((await hostState(page)).loads).toEqual(['p-a', 'p-a'])
  await check.click()
  expect((await hostState(page)).selected).toHaveLength(100)
  expect(await check.evaluate(el => (el as HTMLInputElement).indeterminate)).toBe(true)
  await check.click()
  expect((await hostState(page)).selected).toHaveLength(0)
  // At the bottom of a long list, collapsing must also survive scroll clamping.
  const last = host.locator('#row-group-p-b')
  await last.evaluate(el => el.scrollIntoView({ block: 'center' }))
  const lastTop = (await last.boundingBox())!.y
  await last.getByRole('button', { name: 'Collapse PHAROS' }).click()
  await expect.poll(async () => (await last.boundingBox())!.y).toBeCloseTo(lastTop, 0)
  await last.getByRole('button', { name: 'Expand PHAROS' }).click()
  await expect(host.locator('.fold-space')).toHaveCount(0)
  // The same clamp can occur in the project's own scrolling frame.
  await page.evaluate(() => {
    const root = document.getElementById('table-host')!
    root.style.height = '600px'; root.style.overflow = 'auto'
    ;(window as unknown as { tableGroups: { state: { scrollRoot: HTMLElement } } }).tableGroups.state.scrollRoot = root
    root.scrollTop = root.scrollHeight
  })
  const framedTop = (await last.boundingBox())!.y
  await last.getByRole('button', { name: 'Collapse PHAROS' }).click()
  await expect.poll(async () => (await last.boundingBox())!.y).toBeCloseTo(framedTop, 0)
  await last.getByRole('button', { name: 'Expand PHAROS' }).click()
  await expect(host.locator('.fold-space')).toHaveCount(0)
})

test('AEON-913: automatic fitting, saved widths and resize controls preserve host columns', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mountGroups(page, true)
  const host = page.locator('#table-host')
  const keyWidth = () => host.getByRole('separator', { name: 'Resize Key column' }).getAttribute('aria-valuenow').then(Number)
  await expect.poll(keyWidth).toBeGreaterThan(118)
  const foldedKeyWidth = await keyWidth()
  await host.getByRole('button', { name: 'Expand AEON' }).click()
  expect(await keyWidth()).toBe(foldedKeyWidth)
  const before = await host.getByRole('separator').evaluateAll(elements => elements.map(el => Number(el.getAttribute('aria-valuenow'))))
  await host.locator('#row-host-1 [data-column-id="actions"] button').click()
  await expect(host.locator('#row-host-1 .title-text')).toHaveText('Angewendet · Rückgängig')
  expect(await host.getByRole('separator').evaluateAll(elements => elements.map(el => Number(el.getAttribute('aria-valuenow'))))).toEqual(before)
  const resize = host.getByRole('separator', { name: 'Resize Vorschlag column' })
  const start = Number(await resize.getAttribute('aria-valuenow'))
  await resize.press('ArrowRight')
  await expect(resize).toHaveAttribute('aria-valuenow', String(start + 16))
  await host.getByRole('button', { name: 'Show 50 more in AEON' }).click()
  await expect(resize).toHaveAttribute('aria-valuenow', String(start + 16))
  expect(await keyWidth()).toBeGreaterThanOrEqual(before[0])
  await resize.dblclick()
  expect(Number(await resize.getAttribute('aria-valuenow'))).toBeLessThan(start + 16)
  expect((await hostState(page)).widths.length).toBeGreaterThanOrEqual(2)
  // A drag remains a real saved choice too.
  const box = await resize.boundingBox(); expect(box).not.toBeNull()
  const fitted = Number(await resize.getAttribute('aria-valuenow'))
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
  await page.mouse.down(); await page.mouse.move(box!.x + box!.width / 2 + 24, box!.y + box!.height / 2); await page.mouse.up()
  await expect(resize).toHaveAttribute('aria-valuenow', String(fitted + 24))
  const titleResize = host.getByRole('separator', { name: 'Resize Title column' })
  const automaticTitle = Number(await titleResize.getAttribute('aria-valuenow'))
  await titleResize.press('ArrowLeft')
  await expect(titleResize).toHaveAttribute('aria-valuenow', String(automaticTitle - 16))
  await expect(resize).toHaveAttribute('aria-valuenow', String(fitted + 24))
  await titleResize.dblclick()
  await expect(titleResize).toHaveAttribute('aria-valuenow', String(automaticTitle))
  // Existing hosts handle individual select events rather than selectGroup.
  await page.evaluate(() => { (window as unknown as { tableGroups: { state: { groupHandler: boolean } } }).tableGroups.state.groupHandler = false })
  const groupCheck = host.locator('#row-group-p-a').getByRole('checkbox')
  await groupCheck.check()
  expect((await hostState(page)).selected).toHaveLength(53)
  await groupCheck.uncheck()
  expect((await hostState(page)).selected).toHaveLength(0)
})

for (const width of [1440, 1280, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-913: grouped host remains stable at ${width}px ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width < 720 ? 844 : 900 })
    await mountGroups(page)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme; (window as unknown as { tableGroups: { state: Record<string, unknown> } }).tableGroups.state.locale = 'de' }, theme)
    const first = page.locator('#table-host #row-group-p-a')
    const controls = { fold: first.locator('.group-toggle'), check: first.locator('.group-check-target'), head: first, apply: first.getByRole('button', { name: 'Apply all in p-a' }) }
    const stable = await controlStability(page, controls)
    await stable.check(async () => { await first.getByRole('checkbox').check(); await expect(first.getByRole('checkbox')).toBeChecked() })
    await stable.check(async () => { await controls.fold.click(); await expect(page.locator('#table-host #row-host-1')).toHaveCount(0) })
    await stable.check(async () => { await controls.fold.click(); await expect(page.locator('#table-host #row-host-1')).toBeVisible() })
    stable.done()
    // Headers must contain their actions instead of painting over the next row.
    const headBox = await first.boundingBox()
    const actionBox = await first.locator('.group-end').boundingBox()
    const rowBox = await page.locator('#table-host #row-host-1').boundingBox()
    expect(actionBox!.y + actionBox!.height).toBeLessThanOrEqual(headBox!.y + headBox!.height + .5)
    expect(rowBox!.y).toBeGreaterThanOrEqual(headBox!.y + headBox!.height - .5)
    if (width < 720) {
      expect((await controls.fold.boundingBox())!.height).toBeGreaterThanOrEqual(44)
      expect((await controls.check.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    }
    await page.screenshot({ path: testInfo.outputPath(`aeon-913-tablegroups-${width}-${theme}.png`), fullPage: true })
  })
}
