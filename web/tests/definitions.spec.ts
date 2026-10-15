// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'
import type { Definition, DefinitionChecks, DefinitionContext } from '../src/lib/definitions'

const baseline: DefinitionChecks = { in_progress: [{ id: 'estimate_set' }, { id: 'acceptance_present' }, { id: 'design_attached', condition: 'ui' }, { id: 'no_active_build' }], qa: [{ id: 'review_ok_cross_family', condition: 'code' }], done: [{ id: 'release_copy_set' }] }
const definition = (checks: DefinitionChecks, source: 'project' | 'workspace' = 'workspace'): Definition => ({ id: source === 'workspace' ? 'workspace-definition' : 'project-definition', revision: '2026-10-15T01:00:00Z', source, copied_from_revision: source === 'project' ? '2026-10-14T01:00:00Z' : null, checks })
async function setup(page: Page, state: 'default' | 'own' | 'empty' | 'none' | 'fold' = 'default', readOnly = false) {
  const data = fixtures(), errors = watchErrors(page)
  page.on('console', message => { if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) console.log('Definitions browser error:', message.text()) })
  await mockWork(page, data, { admin: !readOnly, readOnly })
  const ws = state === 'none' ? null : definition(structuredClone(baseline))
  const own = state === 'default' || state === 'none' ? null : definition(state === 'empty' ? {} : state === 'fold' ? { done: [{id:'manual:review',label:'Freigabe der vollständig beschriebenen Abnahmekriterien für die Veröffentlichung'}] } : { in_progress: [{id:'estimate_set'}], done: [{id:'manual:review',label:'Release notes reviewed'}] },'project')
  const world = { context: { project_id: 'p-pharos', definition: own ?? ws, workspace_default: ws, copied_from_checks: own ? structuredClone(baseline) : null, can_manage: !readOnly } as DefinitionContext, writes: [] as { method: string; id: string; body: Record<string, unknown> }[], fail: false, held: null as null | { entered: () => void; until: Promise<void> }, errors }
  await page.route('**/api/projects/*/definitions/context', route => {
    const id = new URL(route.request().url()).pathname.split('/')[3]
    return route.fulfill({ json: id === 'p-pharos' ? world.context : { project_id: id, definition: null, workspace_default: null, copied_from_checks: null, can_manage: !readOnly } })
  })
  await page.route('**/api/projects/*/definitions', async route => {
    const request = route.request(), method = request.method(), id = new URL(request.url()).pathname.split('/')[3]!, body = request.postDataJSON()
    world.writes.push({ method, id, body })
    if (world.held) { const hold = world.held; world.held = null; hold.entered(); await hold.until }
    if (world.fail) return route.fulfill({ status: 503, json: { error: 'Definition save unavailable' } })
    if (readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    if (method === 'DELETE') { world.context.definition = ws; return route.fulfill({ json: ws }) }
    const checks = body.checks as DefinitionChecks, previous = world.context.definition
    const saved = { ...definition(checks,'project'), revision: new Date(Date.parse(previous?.revision ?? '2026-10-15T01:00:00Z') + 1000).toISOString(), copied_from_revision: previous?.source === 'project' ? previous.copied_from_revision : ws?.revision ?? null }
    world.context = { ...world.context, definition: saved, copied_from_checks: previous?.source === 'project' ? world.context.copied_from_checks : ws?.checks ?? null }
    return route.fulfill({ json: saved })
  })
  return world
}
async function navigate(page: Page, path: string) {
  await page.evaluate(path => (document.querySelector('#app') as unknown as {__vue_app__: {config: {globalProperties: {$router: {push(path: string): Promise<void>}}}}}).__vue_app__.config.globalProperties.$router.push(path),path)
}

// Risk: edits move the control under the pointer, remove a different check,
// merge defaults into a copy, or show a reset which omits what will change.
test('AEON-1316 project definitions copy, edit, undo, conditions and reset stay bound and stable', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/p/PHAROS/settings/definitions')
  const card = page.locator('#definitions'), ready = card.locator('[data-block="in_progress"]'), add = ready.getByRole('button',{name:'Add check to Ready'})
  await expect(card.getByText('Uses the workspace default',{exact:true})).toBeVisible()
  await expect(card.getByRole('button',{name:/More for/})).toHaveCount(0)
  await card.getByRole('button',{name:'Change for this project',exact:true}).click()
  await expect(add).toBeVisible()
  expect(world.writes[0]!.body.expected_revision).toBeNull()
  expect(world.writes[0]!.body.checks).toEqual(baseline)
  const row = ready.locator('[data-check="estimate_set"]'), more = row.getByRole('button',{name:'More for Estimate set'})
  const stable = await controlStability(page,{add,row,action:row.locator('.dr-action')})
  await stable.check(async () => {
    await more.click(); await page.getByRole('menuitem',{name:'Remove',exact:true}).click()
    await expect(row.getByText('Removed',{exact:true})).toBeVisible()
  })
  await stable.check(async () => { await row.getByRole('button',{name:'Undo',exact:true}).click(); await expect(more).toBeVisible() })
  stable.done()
  await more.click(); await page.getByRole('menuitem',{name:'Applies to…',exact:true}).click()
  const form = page.getByRole('dialog',{name:'Estimate set applies to',exact:true}), code = form.getByRole('radio',{name:'Code tickets',exact:true}), label = form.getByRole('textbox',{name:'Required labels (comma-separated)'})
  await expect(form).toBeInViewport()
  const conditions = await controlStability(page,{form,code,label,save:form.getByRole('button',{name:/^Save/})})
  await conditions.check(() => code.check())
  await conditions.check(() => label.fill('ui, security'))
  conditions.done()
  const mod = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? 'Meta' : 'Control')
  await label.press(`${mod}+Enter`)
  await expect(form).toHaveCount(0)
  await expect(row.locator('.dr-l small')).toContainText('workspace default: All tickets')
  expect((world.writes.at(-1)!.body.checks as DefinitionChecks).in_progress![0]).toMatchObject({id:'estimate_set',condition:'code',labels:['ui','security']})
  await more.click(); await page.getByRole('menuitem',{name:'Move down',exact:true}).click()
  await expect(ready.locator('[data-check]').nth(1)).toHaveAttribute('data-check','estimate_set')
  const addStable = await controlStability(page,{add})
  await addStable.check(async () => {
    await add.click(); await page.getByRole('menuitem',{name:'Free-text item… Something a person confirms'}).click()
    const manual = page.getByRole('dialog',{name:'Add a free-text item',exact:true})
    await manual.getByRole('textbox',{name:'What a person confirms'}).fill('Release notes reviewed')
    await manual.getByRole('combobox',{name:'Who ticks this'}).selectOption(me.id)
    await manual.getByRole('button',{name:/^Add/}).click()
    await expect(ready.getByText('Release notes reviewed',{exact:true})).toBeVisible()
  })
  addStable.done()
  expect((world.writes.at(-1)!.body.checks as DefinitionChecks).in_progress!.at(-1)).toMatchObject({label:'Release notes reviewed',owner_id:me.id})
  await card.getByRole('button',{name:'Reset to workspace default…',exact:true}).click()
  const reset = page.getByRole('dialog',{name:'Reset PHAROS’s definition',exact:true})
  await expect(reset).toContainText('Dropped: Release notes reviewed.')
  await expect(reset).toContainText('Uses the workspace version: Estimate set.')
  await reset.getByRole('button',{name:'Cancel',exact:true}).click()
  await expect(card.getByText('PHAROS has its own definition',{exact:true})).toBeVisible()
  await card.getByRole('button',{name:'Reset to workspace default…',exact:true}).click()
  await reset.getByRole('button',{name:/^Reset/}).click()
  await expect(card.getByText('Uses the workspace default',{exact:true})).toBeVisible()
  expect(world.writes.at(-1)).toMatchObject({method:'DELETE',id:'p-pharos',body:{expected_workspace_revision:'2026-10-15T01:00:00Z'}})
  expect(world.errors).toEqual([])
})

// Risk: the empty and inherited states imply enforcement that is absent,
// read-only viewers get edit controls, or folded Add back moves its own row.
test('AEON-1316 definition states and folded left-out checks fit phone, tablet and desktop in both themes', async ({ browser }) => {
  test.setTimeout(90_000)
  for (const width of [390,1024,1440]) {
    const context = await browser.newContext({ viewport:{width,height:1000},isMobile:width === 390,hasTouch:width === 390 }), page = await context.newPage()
    try {
      const world = await setup(page,'fold')
      await page.goto('/p/PHAROS/settings/definitions')
      const card = page.locator('#definitions'), show = card.locator('.fold-head button')
      await expect(show).toBeVisible()
      const stable = await controlStability(page,{show})
      await stable.check(() => show.click()); stable.done()
      const row = card.locator('[data-fold-row="estimate_set"]')
      const folded = await controlStability(page,{row,header:card.locator('.fold-head')})
      await folded.check(async () => { await row.getByRole('button',{name:'Add back',exact:true}).click(); await expect(row).toContainText('Added back to Ready') })
      folded.done()
      await expect(card.locator('[data-block="in_progress"]')).toContainText('1 check')
      await expect(card.locator('[data-block="in_progress"]')).not.toContainText('Tickets enter In progress freely.')
      for (const theme of ['light','dark']) {
        await page.evaluate(theme => document.documentElement.dataset.theme = theme,theme)
        await page.screenshot({path:test.info().outputPath(`definitions-own-${width}-${theme}.png`),fullPage:true})
      }
      await card.locator('[data-check="manual:review"]').getByRole('button',{name:/^More for/}).click()
      await page.getByRole('menuitem',{name:'Applies to…',exact:true}).click()
      const form = page.getByRole('dialog',{name:/applies to$/}), radio = form.getByRole('radio',{name:'Code tickets',exact:true})
      await expect(form).toBeInViewport()
      const formStable = await controlStability(page,{frame:form,radio,group:form.locator('fieldset'),save:form.getByRole('button',{name:/^Save/}),cancel:form.getByRole('button',{name:'Cancel',exact:true})})
      await formStable.check(() => radio.check()); formStable.done()
      for (const theme of ['light','dark']) {
        await page.evaluate(theme => document.documentElement.dataset.theme = theme,theme)
        await page.screenshot({path:test.info().outputPath(`definitions-conditions-${width}-${theme}.png`)})
      }
      await form.getByRole('button',{name:'Cancel',exact:true}).click()
      await card.getByRole('button',{name:'Reset to workspace default…',exact:true}).click()
      const reset = page.getByRole('dialog',{name:'Reset PHAROS’s definition',exact:true})
      await expect(reset).toBeInViewport()
      for (const theme of ['light','dark']) {
        await page.evaluate(theme => document.documentElement.dataset.theme = theme,theme)
        await page.screenshot({path:test.info().outputPath(`definitions-reset-${width}-${theme}.png`)})
      }
      await reset.getByRole('button',{name:'Cancel',exact:true}).click()
      const ws = world.context.workspace_default
      for (const state of ['default','empty','readonly','none'] as const) {
        world.context = {project_id:'p-pharos',definition:state === 'default' ? ws : state === 'none' ? null : definition(state === 'empty' ? {} : structuredClone(baseline),'project'),workspace_default:state === 'none' ? null : ws,copied_from_checks:null,can_manage:state !== 'readonly'}
        await page.reload()
        await expect(card.getByRole('heading',{name:'Definitions',exact:true})).toBeVisible()
        await expect(card.locator('.origin')).toBeVisible()
        if (state === 'default') await expect(card.getByText('Uses the workspace default',{exact:true})).toBeVisible()
        if (state === 'empty' || state === 'none') await expect(card.locator('.enforce')).toHaveCount(0)
        if (state === 'readonly') await expect(card.getByRole('button',{name:/Add check|More for|Reset to/})).toHaveCount(0)
        for (const theme of ['light','dark']) {
          await page.evaluate(theme => document.documentElement.dataset.theme = theme,theme)
          await page.screenshot({path:test.info().outputPath(`definitions-${state}-${width}-${theme}.png`)})
        }
      }
      expect(world.errors).toEqual([])
    } finally { await context.close() }
  }
  const context = await browser.newContext(), page = await context.newPage()
  try {
    const world = await setup(page,'empty')
    await page.goto('/p/PHAROS/settings/definitions')
    const card = page.locator('#definitions')
    await expect(card.getByText('No checks',{exact:true})).toHaveCount(3)
    await expect(card.locator('.enforce')).toHaveCount(0)
    await expect(card.getByRole('button',{name:'Show',exact:true})).toHaveCount(0)
    world.context = {...world.context,definition:null,workspace_default:null,copied_from_checks:null}
    await page.reload()
    await expect(card.getByText('No definition yet',{exact:true})).toBeVisible()
    const add = card.getByRole('button',{name:'Add check to Ready'})
    const stable = await controlStability(page,{add})
    await stable.check(async () => { await add.click(); await page.getByRole('menuitem',{name:'Estimate set All tickets',exact:true}).click(); await expect(card.getByText('PHAROS has its own definition',{exact:true})).toBeVisible() })
    stable.done()
    expect(world.writes.at(-1)!.body.expected_revision).toBeNull()
    expect(world.writes.at(-1)!.body.copied_from_revision).toBeNull()
  } finally { await context.close() }
  const readContext = await browser.newContext(), readPage = await readContext.newPage()
  try {
    const world = await setup(readPage,'own',true)
    await readPage.goto('/p/PHAROS/settings/definitions')
    await expect(readPage.locator('#definitions .lock-line')).toBeVisible()
    await expect(readPage.locator('#definitions').getByRole('button',{name:/Add check|More for|Reset to/})).toHaveCount(0)
    expect(world.writes).toEqual([])
  } finally { await readContext.close() }
})

// Risk: failed saves optimistically alter rules, or a late result overwrites
// another project's definition and reports success there.
test('AEON-1316 failed and stale project definition writes never claim success', async ({page}) => {
  const world = await setup(page,'own')
  await page.goto('/p/PHAROS/settings/definitions')
  const row = page.locator('[data-check="estimate_set"]')
  await expect(row).toBeVisible()
  world.fail = true
  await row.getByRole('button',{name:'More for Estimate set'}).click()
  await page.getByRole('menuitem',{name:'Remove',exact:true}).click()
  await expect(page.locator('.definition-feedback')).toContainText('Definition save unavailable')
  await expect(row.getByText('Removed',{exact:true})).toHaveCount(0)
  await expect(page.getByText('Definition saved.',{exact:true})).toHaveCount(0)
  await page.getByRole('button',{name:'Close check editor',exact:true}).click()
  world.fail = false
  let entered!: () => void, release!: () => void
  const pending = new Promise<void>(resolve => entered = resolve), until = new Promise<void>(resolve => release = resolve)
  world.held = {entered,until}
  await row.getByRole('button',{name:'More for Estimate set'}).click(); await page.getByRole('menuitem',{name:'Remove',exact:true}).click()
  await pending
  await navigate(page,'/p/AEON/settings/definitions')
  await expect(page.locator('#definitions').getByText('No definition yet',{exact:true})).toBeVisible()
  release()
  await expect(page.locator('#definitions').getByText('No definition yet',{exact:true})).toBeVisible()
  await expect(page.getByText('Definition saved.',{exact:true})).toHaveCount(0)
  expect(world.writes.every(w => w.id === 'p-pharos')).toBe(true)
  expect(world.errors).toEqual([])
})
