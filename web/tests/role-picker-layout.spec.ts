// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, COORDINATOR } from './access-fixtures'
import { expectStableControls } from './helpers/stable'
import type { FloatingList } from './helpers/floating-lists'

const LONG_SUBJECT = 'Jonas Weber-Oberhauser-Kieslinger · Projektbüro für grenzüberschreitende Entwicklungszusammenarbeit, Infrastrukturverantwortung und Qualitätssicherung'
const LONG_DETAIL = 'Verantwortlich für projektübergreifende Entwicklungszusammenarbeit, Qualitätskontrolle und die langfristige Betreuung der gemeinsam betriebenen Infrastruktur. Dieser vollständige Entscheidungskontext muss auch auf dem Telefon lesbar bleiben.'

async function openRoles(page: Page, extra = 3, longNames = false) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  const subject = longNames ? LONG_SUBJECT : 'workstation-agents'
  world.agents.find(agent => agent.principal_id === COORDINATOR)!.name = subject
  if (longNames) world.roles.find(role => role.key === 'admin')!.description = LONG_DETAIL
  const viewer = world.roles.find(role => role.key === 'viewer')!
  for (let index = 0; index < extra; index++) world.roles.push({ ...viewer, id: `runtime-${index}`, key: `paired_${index}`, name: 'Paired computer runtime', description: '', builtin: false })
  for (const [index, name] of ['Studio workstation', 'Studio laptop', 'Build computer'].entries()) {
    if (index >= extra) break
    world.agents.push({ principal_id: `paired-principal-${index}`, name, workspace_role: `runtime-${index}`, last_seen_at: null, service: false, paired_computer: true, connected_computer: true })
  }
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  const row = page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: subject })
  await row.getByRole('button', { name: `Role of ${subject}: Member. Change`, exact: true }).click()
  const picker = page.getByRole('dialog', { name: `Role of ${subject}`, exact: true })
  await expect(picker).toBeVisible()
  return { world, picker, subject }
}

async function insideViewport(locator: Locator, page: Page) {
  const bounds = (await locator.boundingBox())!
  const viewport = page.viewportSize()!
  expect(bounds.width).toBeGreaterThan(0)
  expect(bounds.height).toBeGreaterThan(0)
  expect(bounds.x).toBeGreaterThanOrEqual(0)
  expect(bounds.y).toBeGreaterThanOrEqual(0)
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width + .5)
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(viewport.height + .5)
}

async function insidePanel(control: Locator, panel: Locator, page: Page) {
  await insideViewport(control, page)
  const box = (await control.boundingBox())!, frame = (await panel.boundingBox())!
  expect(box.x).toBeGreaterThanOrEqual(frame.x - .5)
  expect(box.y).toBeGreaterThanOrEqual(frame.y - .5)
  expect(box.x + box.width).toBeLessThanOrEqual(frame.x + frame.width + .5)
  expect(box.y + box.height).toBeLessThanOrEqual(frame.y + frame.height + .5)
}

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
  test(`role choices use the screen and stay still at ${width} ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const errors = watchErrors(page)
    const { world, picker, subject } = await openRoles(page, 3, true)
    const body = picker.locator('.picker-body'), options = picker.getByRole('radiogroup')
    const preview = picker.getByRole('region', { name: 'What changes' })
    const admin = options.getByRole('radio', { name: /^Admin/ })
    const member = options.getByRole('radio', { name: /^Member/ })
    const apply = picker.locator('.actions .primary')
    await expect(member).toBeFocused()
    await insideViewport(picker, page)
    const frame = (await picker.boundingBox())!
    if (width === 390) { expect(frame.width).toBe(390); expect(frame.height).toBe(844) }
    else { expect(frame.width).toBeGreaterThan(width * .6); expect(frame.width).toBeLessThanOrEqual(960) }
    // The complete list is laid out, with no independent 262px clip box.
    expect(await options.evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
    const rowHeights = await options.getByRole('radio').evaluateAll(rows => rows.map(row => row.getBoundingClientRect().height))
    expect(new Set(rowHeights)).toEqual(new Set([76]))
    await expect(options.getByRole('radio', { name: /^Paired computer runtime/ })).toHaveCount(3)
    await expect(options.getByRole('radio', { name: /^Paired computer runtime/ }).locator('.desc')).toHaveText(['Studio workstation', 'Studio laptop', 'Build computer'])
    const title = picker.locator('.title')
    await expect(title).toHaveText(`Workspace role · ${subject}`)
    expect(await title.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
    expect(await title.evaluate(el => getComputedStyle(el).webkitLineClamp)).toBe('2')
    const titleMetrics = await title.evaluate(el => ({ height: el.clientHeight, line: parseFloat(getComputedStyle(el).lineHeight) }))
    expect(titleMetrics.height).toBeGreaterThan(titleMetrics.line * 1.5)
    const bothActions = async () => {
      await insidePanel(apply, picker, page)
      await insidePanel(picker.getByRole('button', { name: 'Cancel', exact: true }), picker, page)
    }
    await bothActions()
    const scrolling = await body.evaluate(el => el.scrollHeight > el.clientHeight + 1)
    await expectStableControls({
      controls: { ...(width === 390 || scrolling ? { frame: picker } : {}), options, admin, member, row: admin, preview, actions: picker.locator('.actions'), apply, cancel: picker.getByRole('button', { name: 'Cancel', exact: true }) },
      scrollAreas: { picker, body, options, preview },
      interactions: [admin, member, admin].map((role, index) => ({ name: `pick role ${index + 1}`, run: async () => {
        const scrollBefore = await body.evaluate(el => el.scrollTop)
        await role.click()
        await expect(role).toHaveAttribute('aria-checked', 'true')
        expect(await body.evaluate(el => el.scrollTop), 'pointer selection leaves scrolling alone').toBe(scrollBefore)
        await bothActions()
      } })),
    })
    await expect(apply).toHaveAccessibleName(`Give ${subject} Admin`)
    await expect(apply.locator('.action-label > span').last()).toHaveText('Give Admin')
    for (let index = 0; index < await options.getByRole('radio').count(); index++) {
      const role = options.getByRole('radio').nth(index)
      await role.scrollIntoViewIfNeeded()
      await expectStableControls({
        controls: { frame: picker, group: options, row: role, preview, actions: picker.locator('.actions'), apply, cancel: picker.getByRole('button', { name: 'Cancel', exact: true }) },
        scrollAreas: { picker, body, options, preview },
        interactions: [{ name: `select every role ${index}`, run: async () => {
          await role.click()
          await expect(role).toHaveAttribute('aria-checked', 'true')
          await bothActions()
        } }],
      })
    }
    await admin.click()
    await insideViewport(apply, page)
    // Keyboard-focus the selected row: its complete decision text is visible.
    await page.keyboard.press('Tab')
    await page.keyboard.press('Shift+Tab')
    await expect(admin).toBeFocused()
    await expect(page.locator('.tooltip')).toContainText(LONG_DETAIL)
    const shots = join(process.cwd(), 'test-results', 'aeon-624', 'r4')
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `aeon-624-role-picker-${width}-${theme}.png`) })
    await picker.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect(world.calls.filter(call => call.method === 'PUT' && call.path.endsWith('/workspace-role'))).toHaveLength(0)
    expect(errors).toEqual([])
  })
}

test('long role lists and short viewports keep actions visible and keyboard rows reachable', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 480 })
  const { picker } = await openRoles(page, 30)
  const body = picker.locator('.picker-body'), options = picker.getByRole('radiogroup')
  const apply = picker.locator('.actions .primary')
  expect(await body.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(100)
  await insideViewport(apply, page)
  const row = options.getByRole('radio', { name: /^Member/ })
  await expectStableControls({
    controls: { frame: picker, options, row, apply, actions: picker.locator('.actions') },
    scrollAreas: { body, options },
    interactions: [{ name: 'keyboard reveal another role', run: async () => {
      await row.press('ArrowDown')
      await expect(options.getByRole('radio', { name: /^Viewer/ })).toHaveAttribute('aria-checked', 'true')
    } }, { name: 'scroll to the last role', run: async () => { await options.getByRole('radio').last().scrollIntoViewIfNeeded() } }],
  })
  await insideViewport(apply, page)
  const last = options.getByRole('radio').last()
  await expectStableControls({
    controls: { frame: picker, options, row: last, apply, actions: picker.locator('.actions'), preview: picker.getByRole('region', { name: 'What changes' }) },
    scrollAreas: { body, options },
    interactions: [{ name: 'pick the last role', run: async () => {
      await last.click()
      await expect(last).toHaveAttribute('aria-checked', 'true')
    } }],
  })
  await insideViewport(apply, page)
  await page.keyboard.press('Escape')
  await expect(picker).toHaveCount(0)
})

test('a description distinguishes duplicate role names when it is present in the payload', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 1000 })
  const { world, picker } = await openRoles(page, 4)
  await picker.getByRole('button', { name: 'Cancel', exact: true }).click()
  world.roles.find(role => role.id === 'runtime-3')!.description = 'Runtime for the studio workstation'
  await page.reload()
  await page.getByRole('button', { name: /^Role of workstation-agents:/ }).click()
  await expect(picker.getByRole('radio', { name: /Runtime for the studio workstation/ })).toHaveCount(1)
})

for (const width of [1440, 390]) test(`a rejected role change and another choice keep actions still at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const { world, picker } = await openRoles(page, 0)
  const requests: unknown[] = []
  await page.route(`**/api/members/${COORDINATOR}/workspace-role`, route => {
    requests.push(route.request().postDataJSON())
    return route.fulfill({ status: 403, json: { error: 'forbidden', reason: 'Role assignment refused: access changed.' } })
  })
  const options = picker.getByRole('radiogroup'), preview = picker.getByRole('region', { name: 'What changes' })
  const admin = options.getByRole('radio', { name: /^Admin/ }), member = options.getByRole('radio', { name: /^Member/ })
  const apply = picker.locator('.actions .primary')
  await expectStableControls({
    controls: { ...(width === 390 ? { frame: picker } : {}), options, admin, member, row: admin, preview, apply, cancel: picker.getByRole('button', { name: 'Cancel', exact: true }), actions: picker.locator('.actions') },
    scrollAreas: { picker, body: picker.locator('.picker-body'), preview },
    interactions: [
      { name: 'pick Admin', run: async () => { await admin.click(); await expect(admin).toHaveAttribute('aria-checked', 'true') } },
      { name: 'refused write', run: async () => { await apply.click(); await expect(picker.getByRole('alert')).toContainText('Role assignment refused: access changed.') } },
      { name: 'clear refusal with another choice', run: async () => { await member.click(); await expect(picker.getByRole('alert')).toHaveCount(0); await expect(member).toHaveAttribute('aria-checked', 'true') } },
    ],
  })
  expect(requests).toEqual([{ role_id: 'role-admin' }])
  expect(world.agents.find(agent => agent.principal_id === COORDINATOR)!.workspace_role).toBe('role-member')
})

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) for (const kind of ['choice', 'group', 'epic', 'option', 'label', 'relation', 'facet', 'business'] as const) {
  test(`${kind} floating choices scroll as one panel at ${width} ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: 1000 })
    const errors = watchErrors(page)
    const data = fixtures()
    const epic = data.nodes.find(node => node.kind_slug === 'epic')!
    data.nodes.push(...Array.from({ length: 16 }, (_, index) => ({ ...epic, id: `extra-epic-${index}`, key: `PHAROS-${index + 100}`, title: `Projektübergreifende Entwicklungszusammenarbeit und Qualitätsverantwortung ${index + 1}`, state: 'open' })))
    await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
    await page.evaluate(async kind => {
      const path = '/tests/helpers/floating-lists.ts'
      const { mountFloatingList } = await import(/* @vite-ignore */ path)
      mountFloatingList(kind as FloatingList, true)
    }, kind)
    if (kind === 'facet') await page.getByRole('button', { name: 'Choices', exact: true }).click()
    const panel = page.locator('.floating[role="dialog"]')
    await expect(panel).toBeVisible()
    const list = panel.locator('.menu, .options, .picker-list')
    const rows = list.locator('[role="option"], [role="menuitemradio"], [role="checkbox"], :scope > label')
    await expect.poll(() => rows.count()).toBeGreaterThanOrEqual(8)
    expect(await list.evaluate(el => el.scrollHeight - el.clientHeight), 'no nested clipping').toBeLessThanOrEqual(1)
    await insideViewport(panel, page)
    if (kind === 'label') await insideViewport(panel.getByRole('button', { name: 'Apply', exact: true }), page)
    await rows.last().scrollIntoViewIfNeeded()
    const last = (await rows.last().boundingBox())!, bounds = (await panel.boundingBox())!
    expect(last.y).toBeGreaterThanOrEqual(bounds.y)
    expect(last.y + last.height).toBeLessThanOrEqual(bounds.y + bounds.height)
    if (kind === 'label') await expectStableControls({
      controls: { panel, group: list, row: rows.last(), apply: panel.getByRole('button', { name: 'Apply', exact: true }) },
      scrollAreas: { panel, list },
      interactions: [{ name: 'choose the last label', run: async () => {
        await rows.last().click()
        await expect(rows.last()).toHaveAttribute('aria-checked', 'true')
      } }],
    })
    expect(await panel.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    // Measure navigation/disclosure while the real parent keeps the menu
    // open. Selection is checked separately because it unmounts the controls.
    const row = rows.last()
    const search = panel.locator('input:not([type="checkbox"])')
    const read = panel.locator('.read-name').last()
    await expectStableControls({
      controls: { panel, group: list, row, ...(await search.count() ? { search } : {}), ...(await read.count() ? { read } : {}), ...(kind === 'label' ? { apply: panel.getByRole('button', { name: 'Apply', exact: true }) } : {}) },
      scrollAreas: { panel, list },
      interactions: [{ name: `read ${kind} option before choosing`, run: async () => {
        await page.keyboard.press('ArrowRight')
        if (await read.count()) {
          await read.focus()
          await expect(page.locator('.tooltip')).toContainText('Projektübergreifende Entwicklungszusammenarbeit')
        } else if (kind === 'choice') {
          // Settings choices already wrap the whole name without an ellipsis.
          await row.focus()
          await expect(row.locator('.label')).toContainText('Projektübergreifende Entwicklungszusammenarbeit')
          expect(await row.locator('.label').evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
        } else {
          if (await row.getByRole('checkbox').count()) await row.getByRole('checkbox').focus()
          else await row.focus()
          await expect(page.locator('.tooltip')).toContainText('Projektübergreifende Entwicklungszusammenarbeit')
        }
      } }],
    })
    const shots = join(process.cwd(), 'test-results', 'aeon-624', 'r4')
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `aeon-624-${kind}-${width}-${theme}.png`) })
    const before = await page.evaluate(() => (window as unknown as { __floatingEvents: unknown[] }).__floatingEvents.length)
    if (kind === 'label' || kind === 'facet') {
      await expectStableControls({
        controls: { panel, group: list, row, ...(await search.count() ? { search } : {}), ...(kind === 'label' ? { apply: panel.getByRole('button', { name: 'Apply', exact: true }) } : {}) },
        scrollAreas: { panel, list },
        interactions: [{ name: `choose ${kind} option`, run: () => row.click() }],
      })
    } else await row.click()
    if (kind === 'label') await expect(row).toHaveAttribute('aria-checked', 'false')
    else {
      await expect.poll(() => page.evaluate(() => (window as unknown as { __floatingEvents: unknown[] }).__floatingEvents.length)).toBe(before + 1)
      if (kind !== 'facet') {
        await expect(panel).toHaveCount(0)
        await expect(page.locator('.tooltip')).toHaveCount(0)
      }
    }
    expect(errors).toEqual([])
  })
}

for (const key of ['Enter', 'Space']) test(`keyboard Cancel with ${key} makes zero role writes`, async ({ page }) => {
  const { world, picker } = await openRoles(page, 0)
  await picker.getByRole('radio', { name: /^Admin/ }).click()
  await page.keyboard.press('Tab')
  await expect(picker.getByRole('region', { name: 'What changes' })).toBeFocused()
  await page.keyboard.press('Tab')
  const cancel = picker.getByRole('button', { name: 'Cancel', exact: true })
  await expect(cancel).toBeFocused()
  await cancel.press(key)
  await expect(picker).toHaveCount(0)
  expect(world.calls.filter(call => call.method === 'PUT' && call.path.endsWith('/workspace-role'))).toEqual([])
  expect(world.agents.find(agent => agent.principal_id === COORDINATOR)!.workspace_role).toBe('role-member')
  await expect(page.getByRole('button', { name: 'Role of workstation-agents: Member. Change', exact: true })).toBeFocused()
})

test('touch and keyboard expose complete role names, descriptions and refusal reasons', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, reducedMotion: 'reduce' })
  const page = await context.newPage()
  try {
    const { world, picker } = await openRoles(page, 1, true)
    await picker.getByRole('button', { name: 'Cancel', exact: true }).click()
    const runtime = world.roles.find(role => role.id === 'runtime-0')!
    runtime.name = 'Laufzeitverantwortung für projektübergreifende Entwicklungszusammenarbeit und Infrastrukturqualität'
    runtime.description = LONG_DETAIL
    world.agents = world.agents.filter(agent => agent.principal_id !== 'paired-principal-0')
    world.people[0]!.workspace_role = 'role-admin'
    await page.reload()
    await page.getByRole('button', { name: `Role of ${LONG_SUBJECT}: Member. Change`, exact: true }).click()
    const row = picker.getByRole('radio', { name: /^Laufzeitverantwortung/ })
    await row.tap()
    await expect(page.locator('.tooltip')).toHaveText(`${runtime.name}\n${LONG_DETAIL}`)
    await insideViewport(page.locator('.tooltip'), page)
    const desc = row.locator('.desc')
    expect(await desc.evaluate(el => getComputedStyle(el).webkitLineClamp)).toBe('2')
    expect(await desc.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
    const refused = picker.getByRole('radio', { name: /^Owner/ })
    await refused.tap()
    await expect(page.locator('.tooltip')).toContainText('you do not hold')
    await expect(picker.locator('.actions .primary')).toBeDisabled()
    await expect(picker.getByRole('region', { name: 'What changes' })).toContainText('you do not hold')
    await page.keyboard.press('Tab')
    await page.keyboard.press('Shift+Tab')
    await expect(refused).toBeFocused()
    await expect(page.locator('.tooltip')).toContainText('you do not hold')
    await row.focus()
    await expect(page.locator('.tooltip')).toContainText(LONG_DETAIL)
    await expect(page.locator('.tooltip')).toContainText(runtime.name)
  } finally { await context.close() }
})

for (const width of [390, 1024, 1440]) test(`long German heading wraps to two lines at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const { picker } = await openRoles(page, 0, true)
  const title = picker.locator('.title')
  await expect(title).toHaveText(`Workspace role · ${LONG_SUBJECT}`)
  expect(await title.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
  expect(await title.evaluate(el => getComputedStyle(el).webkitLineClamp)).toBe('2')
  await page.keyboard.press('Shift+Tab')
  await expect(title).toBeFocused()
  await expect(page.locator('.tooltip')).toHaveText(`Workspace role · ${LONG_SUBJECT}`)
  await insideViewport(page.locator('.tooltip'), page)
  await picker.getByRole('button', { name: 'Cancel', exact: true }).click()
  const place = 'Projektübergreifende Entwicklungszusammenarbeit und langfristige Infrastrukturqualität'
  await page.evaluate(async ({ subject, place }) => {
    const { mountProjectRolePicker } = await import(/* @vite-ignore */ '/tests/helpers/floating-lists.ts')
    mountProjectRolePicker(subject, place)
  }, { subject: LONG_SUBJECT, place })
  const project = page.getByRole('dialog', { name: `Role of ${LONG_SUBJECT} on ${place}`, exact: true })
  await expect(project).toBeVisible()
  await expect(project.locator('.title')).toHaveText(`Project role · ${LONG_SUBJECT} on ${place}`)
  expect(await project.locator('.title').evaluate(el => getComputedStyle(el).webkitLineClamp)).toBe('2')
  await expect(project.getByRole('radio', { name: /^Member/ })).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect(project.locator('.title')).toBeFocused()
  await expect(page.locator('.tooltip')).toContainText(place)
  await insideViewport(page.locator('.tooltip'), page)
})

test('keyboard focus reveals the complete role decision text', async ({ page }) => {
  const { picker } = await openRoles(page, 0, true)
  const admin = picker.getByRole('radio', { name: /^Admin/ })
  await admin.click()
  await page.keyboard.press('Tab')
  await page.keyboard.press('Shift+Tab')
  await expect(admin).toBeFocused()
  await expect(page.locator('.tooltip')).toHaveText(`Admin\n${LONG_DETAIL}`)
  await page.mouse.move(1, 1)
  await expect(admin).toBeFocused()
  await expect(page.locator('.tooltip')).toHaveText(`Admin\n${LONG_DETAIL}`)
})

test('facet search stays still when Clear appears and disappears', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { mountFloatingList } = await import(/* @vite-ignore */ '/tests/helpers/floating-lists.ts')
    mountFloatingList('facet')
  })
  await page.getByRole('button', { name: 'Choices', exact: true }).click()
  const panel = page.getByRole('dialog', { name: 'Filter by choices', exact: true })
  const group = panel.getByRole('group'), row = group.locator('label').first()
  await expectStableControls({
    controls: { panel, group, row, search: panel.getByRole('textbox') }, scrollAreas: { panel, group },
    interactions: [true, false].map(selected => ({ name: `filter selected ${selected}`, run: async () => {
      await row.click()
      await expect(row.getByRole('checkbox')).toBeChecked({ checked: selected })
    } })),
  })
})

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) test(`registered hostname stays inside its panel at ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ colorScheme: theme })
  await mockWork(page, fixtures())
  const registeredHost = 'entwicklungsarbeitsplatz-fuer-projektuebergreifende-qualitaetsverantwortung.infrastruktur.inspr.at'
  const writes: unknown[] = []
  await page.route('**/api/me/host-labels', route => {
    if (route.request().method() === 'PUT') { writes.push(route.request().postDataJSON()); return route.fulfill({ json: { host: registeredHost, label: registeredHost } }) }
    return route.fulfill({ json: [{ host: registeredHost, label: 'Entwicklungsarbeitsplatz' }] })
  })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await page.evaluate(async host => {
    const { mountSessionHost } = await import(/* @vite-ignore */ '/tests/helpers/floating-lists.ts')
    mountSessionHost(host)
  }, registeredHost)
  await page.getByRole('button', { name: `Your name for this computer: ${registeredHost}`, exact: true }).click()
  const panel = page.getByRole('dialog', { name: 'Your name for this computer', exact: true })
  const input = panel.getByRole('textbox'), reset = panel.getByRole('button', { name: 'Use registered name', exact: true })
  await expect(input).toHaveValue('Entwicklungsarbeitsplatz')
  expect(await panel.evaluate(el => el.scrollWidth - el.clientWidth), 'long registered hostname never overflows the panel').toBeLessThanOrEqual(1)
  const controls = { input, save: panel.getByRole('button', { name: 'Save', exact: true }), reset, cancel: panel.getByRole('button', { name: 'Cancel', exact: true }) }
  for (const control of Object.values(controls)) await insidePanel(control, panel, page)
  await expectStableControls({ controls, scrollAreas: { panel }, interactions: [{ name: 'type the full hostname', run: () => input.fill(registeredHost) }] })
  const shots = join(process.cwd(), 'test-results', 'aeon-624', 'r4')
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: join(shots, `aeon-624-session-host-${width}-${theme}.png`) })
  await reset.click()
  await expect(panel).toHaveCount(0)
  expect(writes).toEqual([{ host: registeredHost, label: null }])
})
