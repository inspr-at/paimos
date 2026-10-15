// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { writeFile } from 'node:fs/promises'
import { expect, test, type Browser, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'
import { DEFINITION_CATALOG, type Definition, type DefinitionChecks, type DefinitionContext } from '../src/lib/definitions'

const baseline: DefinitionChecks = { in_progress: [{ id: 'estimate_set' }, { id: 'acceptance_present' }, { id: 'design_attached', condition: 'ui' }, { id: 'no_active_build' }], qa: [{ id: 'review_ok_cross_family', condition: 'code' }], done: [{ id: 'release_copy_set' }] }
const definition = (checks: DefinitionChecks, source: 'project' | 'workspace' = 'workspace'): Definition => ({ id: source === 'workspace' ? 'workspace-definition' : 'project-definition', revision: '2026-10-15T01:00:00Z', source, copied_from_revision: source === 'project' ? '2026-10-14T01:00:00Z' : null, checks })
async function setup(page: Page, state: 'default' | 'own' | 'empty' | 'none' | 'fold' = 'default', readOnly = false) {
  const data = fixtures(), errors = watchErrors(page)
  page.on('console', message => { if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) console.log('Definitions browser error:', message.text()) })
  await mockWork(page, data, { admin: !readOnly, readOnly })
  const ws = state === 'none' ? null : definition(structuredClone(baseline))
  const own = state === 'default' || state === 'none' ? null : definition(state === 'empty' ? {} : state === 'fold' ? { done: [{id:'manual:review',label:'Freigabe der vollständig beschriebenen Abnahmekriterien für die Veröffentlichung'}] } : { in_progress: [{id:'estimate_set'}], done: [{id:'manual:review',label:'Release notes reviewed'}] },'project')
  const world = { context: { project_id: 'p-pharos', definition: own ?? ws, workspace_default: ws, copied_from_checks: own ? structuredClone(baseline) : null, can_manage: !readOnly } as DefinitionContext, writes: [] as { method: string; id: string; body: Record<string, unknown> }[], fail: false, held: null as null | { entered: () => void; until: Promise<void> }, readHeld: null as null | { entered: () => void; until: Promise<void> }, errors }
  await page.route('**/api/projects/*/definitions/context', async route => {
    if (world.readHeld) { const hold = world.readHeld; world.readHeld = null; hold.entered(); await hold.until }
    const id = new URL(route.request().url()).pathname.split('/')[3]
    return route.fulfill({ json: id === 'p-pharos' ? world.context : { project_id: id, definition: null, workspace_default: null, copied_from_checks: null, can_manage: !readOnly } })
  })
  await page.route('**/api/projects/*/definitions', async route => {
    const request = route.request(), method = request.method(), id = new URL(request.url()).pathname.split('/')[3]!, body = request.postDataJSON()
    world.writes.push({ method, id, body })
    if (world.held) { const hold = world.held; world.held = null; hold.entered(); await hold.until }
    if (world.fail) return route.fulfill({ status: 503, json: { error: 'Definition save unavailable' } })
    if (readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    const workspace = world.context.workspace_default
    if (method === 'DELETE') { world.context.definition = workspace; return route.fulfill({ json: workspace }) }
    const checks = body.checks as DefinitionChecks, previous = world.context.definition
    const saved = { ...definition(checks,'project'), revision: new Date(Date.parse(previous?.revision ?? '2026-10-15T01:00:00Z') + 1000).toISOString(), copied_from_revision: previous?.source === 'project' ? previous.copied_from_revision : workspace?.revision ?? null }
    world.context = { ...world.context, definition: saved, copied_from_checks: previous?.source === 'project' ? world.context.copied_from_checks : workspace?.checks ?? null }
    return route.fulfill({ json: saved })
  })
  return world
}
async function navigate(page: Page, path: string) {
  await page.evaluate(path => (document.querySelector('#app') as unknown as {__vue_app__: {config: {globalProperties: {$router: {push(path: string): Promise<void>}}}}}).__vue_app__.config.globalProperties.$router.push(path),path)
}

// Risk: edits move the control under the pointer, remove a different check,
// merge defaults into a copy, or show a reset which omits what will change.
// The inherited policy breadcrumb must retain its destination and SVG icons.
test('AEON-1316 project definitions copy, edit, undo, conditions and reset stay bound and stable', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/p/PHAROS/settings/definitions')
  const card = page.locator('#definitions'), ready = card.locator('[data-block="in_progress"]'), add = ready.getByRole('button',{name:'Add check to Ready'})
  await expect(card.getByText('Uses the workspace default',{exact:true})).toBeVisible()
  const policies = card.getByRole('link', { name: /Settings.*Policies/ })
  await expect(policies).toHaveAttribute('href', '/settings/policies#definitions')
  await expect(policies).not.toContainText('›')
  await expect(policies.locator('svg')).toHaveCount(2)
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

type World = Awaited<ReturnType<typeof setup>>
async function evidenceMatrix(browser: Browser, run: (page: Page, world: World, width: number, theme: string) => Promise<void>) {
  test.setTimeout(180_000)
  for (const width of [390,1024,1440]) for (const theme of ['light','dark'] as const) {
    await test.step(`${width}px ${theme}`, async () => {
      const context = await browser.newContext({ viewport:{width,height:1000},hasTouch:width === 390,isMobile:width === 390,colorScheme:theme,reducedMotion:'reduce' })
      try {
        const page = await context.newPage(), world = await setup(page,'own')
        await page.route('**/api/preferences/theme', route => route.fulfill({json:{key:'theme',value:theme}}))
        await run(page,world,width,theme)
        expect(world.errors).toEqual([])
      } finally { await context.close() }
    })
  }
}
function barrier() {
  let entered!: () => void, release!: () => void
  const pending = new Promise<void>(resolve => entered = resolve), until = new Promise<void>(resolve => release = resolve)
  return { entered, release, pending, until }
}
async function position(locator: Locator) {
  return locator.evaluate(el => {
    const rect = el.getBoundingClientRect()
    let x = rect.x + window.scrollX, y = rect.y + window.scrollY
    for (let parent = el.parentElement; parent; parent = parent.parentElement) { x += parent.scrollLeft; y += parent.scrollTop }
    return {x,y,width:rect.width,height:rect.height}
  })
}
async function anchored(menu: Locator, trigger: Locator, page: Page) {
  await expect(menu).toBeVisible()
  await expect(menu).not.toHaveClass(/\bsheet\b/)
  const bounds = await menu.boundingBox(), anchor = await trigger.boundingBox(), viewport = page.viewportSize()!
  expect(bounds).not.toBeNull(); expect(anchor).not.toBeNull()
  expect(bounds!.height).toBeGreaterThan(0)
  expect(bounds!.height).toBeLessThan(viewport.height)
  expect(bounds!.x).toBeGreaterThanOrEqual(0)
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width)
  expect(bounds!.y).toBeGreaterThanOrEqual(0)
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(viewport.height)
  const gap = bounds!.y >= anchor!.y + anchor!.height ? bounds!.y - anchor!.y - anchor!.height : anchor!.y - bounds!.y - bounds!.height
  expect(Math.abs(gap - 6), 'popover stays attached to its trigger').toBeLessThanOrEqual(.5)
}
async function shot(page: Page, name: string, width: number, theme: string) {
  await page.screenshot({path:test.info().outputPath(`definitions-fix4-${name}-${width}-${theme}.png`)})
}

// Item 1 risk: origin copy, action visibility or the first row moves any block
// when a phone creates, copies or resets a definition.
test('AEON-1316 FIX4 phone origin and all block heads stay within 0.5px across first add copy and reset', async ({browser}) => {
  test.setTimeout(120_000)
  const lines = [`AEON-1316 FIX4 · HEAD ${execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()}`]
  for (const theme of ['light','dark'] as const) {
    const context = await browser.newContext({viewport:{width:390,height:1000},hasTouch:true,isMobile:true,colorScheme:theme,reducedMotion:'reduce'})
    try {
      const page = await context.newPage(), world = await setup(page,'none')
      await page.route('**/api/preferences/theme', route => route.fulfill({json:{key:'theme',value:theme}}))
      for (const state of ['none','empty','default'] as const) {
        const ws = state === 'none' ? null : definition(structuredClone(baseline))
        world.context = {project_id:'p-pharos',definition:state === 'empty' ? definition({},'project') : ws,workspace_default:ws,copied_from_checks:state === 'empty' ? structuredClone(baseline) : null,can_manage:true}
        await page.goto('/p/PHAROS/settings/definitions')
        await page.reload()
        const card = page.locator('#definitions'), ready = card.locator('[data-block="in_progress"]'), add = ready.getByRole('button',{name:'Add check to Ready'})
        await expect(card.locator('.origin')).toHaveAttribute('aria-busy','false')
        const controls = {origin:card.locator('.origin'),readyHead:ready.locator('.db-head'),firstRow:ready.locator('.dr').first(),qaHead:card.locator('[data-block="qa"] .db-head'),doneHead:card.locator('[data-block="done"] .db-head')}
        const stable = await controlStability(page,controls), before = await position(controls.firstRow)
        if (state === 'default') {
          await stable.check(async () => { await card.getByRole('button',{name:'Change for this project',exact:true}).click(); await expect(add).toBeVisible() })
        } else {
          const addStable = await controlStability(page,{add})
          await addStable.check(async () => {
            await stable.check(async () => { await add.click(); await page.getByRole('menuitem',{name:'Estimate set All tickets',exact:true}).click(); await expect(ready.locator('[data-check="estimate_set"]')).toBeVisible() })
          })
          addStable.done()
        }
        const after = await position(controls.firstRow)
        lines.push(`${theme} ${state} → ${state === 'default' ? 'copy' : 'first add'}: first-row shift ${Math.abs(after.y-before.y).toFixed(3)}px; all origin/header/row dimensions ≤0.5px PASS`)
        await shot(page,`${state}-after`,390,theme)
        stable.done()
        const resetStable = await controlStability(page,{origin:controls.origin,readyHead:controls.readyHead,firstRow:controls.firstRow})
        await resetStable.check(async () => {
          await card.getByRole('button',{name:state === 'none' ? 'Remove this definition…' : 'Reset to workspace default…',exact:true}).click()
          await page.locator('.definition-reset').getByRole('button',{name:state === 'none' ? /^Remove/ : /^Reset/}).click()
          await expect(card.getByText(state === 'none' ? 'No definition yet' : 'Uses the workspace default',{exact:true})).toBeVisible()
        })
        resetStable.done()
        const reset = await position(controls.firstRow)
        lines.push(`${theme} reset ${state}: first-row shift ${Math.abs(reset.y-before.y).toFixed(3)}px; all origin/header/row dimensions ≤0.5px PASS`)
      }
      expect(world.errors).toEqual([])
    } finally { await context.close() }
  }
  await writeFile(test.info().outputPath('definitions-fix4-phone-pass-lines.md'),lines.join('\n')+'\n')
})

// Item 2 risk: loading or save failures insert empty bands and move a control;
// a menu failure is hidden, or the form's error pushes Save and Cancel.
test('AEON-1316 FIX4 feedback uses the origin toast and fixed form footer without empty bands', async ({browser}) => {
  await evidenceMatrix(browser, async (page,world,width,theme) => {
    const read = barrier(); world.readHeld = read
    await page.goto('/p/PHAROS/settings/definitions'); await read.pending
    const card = page.locator('#definitions')
    await expect(card.locator('.or-l small')).toHaveText('Reading the definition…')
    await expect(card.locator('.definition-feedback')).toHaveCount(0)
    expect(await card.locator('.card-body').evaluate(el => el.firstElementChild?.classList.contains('origin'))).toBe(true)
    read.release()
    const row = card.locator('[data-check="estimate_set"]'), more = row.getByRole('button',{name:'More for Estimate set'})
    await expect(more).toBeVisible()
    const stable = await controlStability(page,{more,origin:card.locator('.origin')})
    world.fail = true
    await stable.check(async () => { await more.click(); await page.getByRole('menuitem',{name:'Remove',exact:true}).click(); await expect(page.locator('.toast.error')).toContainText('Definition save unavailable'); await expect(more).toBeEnabled() })
    stable.done()
    await expect(page.locator('.definition-pop .panel-feedback')).toHaveCount(0)
    await expect(page.locator('.definition-pop')).not.toContainText('Definition save unavailable')
    await page.getByRole('button',{name:'Close check editor',exact:true}).click()
    await page.locator('.toast.error').getByRole('button',{name:'Reload',exact:true}).click()
    await expect(card.locator('.origin')).toHaveAttribute('aria-busy','false')
    await more.click(); await page.getByRole('menuitem',{name:'Applies to…',exact:true}).click()
    const form = page.getByRole('dialog',{name:'Estimate set applies to',exact:true}), save = form.getByRole('button',{name:/^Save/}), cancel = form.getByRole('button',{name:'Cancel',exact:true})
    const formStable = await controlStability(page,{save,cancel,footer:form.locator('footer'),options:form.locator('fieldset')})
    await formStable.check(async () => { await save.click(); await expect(form.locator('footer [role="status"]')).toHaveText('Definition save unavailable'); await expect(save).toBeEnabled() })
    formStable.done()
    await shot(page,'form-error',width,theme)
    world.fail = false
    await save.click(); await expect(form).toHaveCount(0)
    await expect(page.locator('.toast')).toContainText('Definition saved.')
    await shot(page,'no-feedback-band',width,theme)
  })
})

// Item 3 and copy 7a–7d risk: phone menus become full sheets or lose their
// trigger; another block's usual status and the origin topic are ambiguous.
test('AEON-1316 FIX4 Add and row menus stay anchored at every width with the approved copy', async ({browser}) => {
  await evidenceMatrix(browser, async (page,_world,width,theme) => {
    await page.goto('/p/PHAROS/settings/definitions')
    const card = page.locator('#definitions'), add = card.getByRole('button',{name:'Add check to Ready'}), pop = page.locator('.definition-pop')
    await expect(card.getByRole('button',{name:'Workspace default or this project',exact:true})).toBeVisible()
    const stable = await controlStability(page,{add})
    await stable.check(() => add.click()); stable.done()
    await anchored(pop,add,page)
    await expect(pop.getByRole('menuitem',{name:/^Release text set/})).toContainText('Unless hidden from release notes · usually Done')
    await expect(pop.getByRole('menuitem',{name:/^Shipped in a release/})).toContainText('If the project uses releases · usually Done')
    await shot(page,'add-menu',width,theme)
    await pop.getByRole('button',{name:'Close check editor',exact:true}).click()
    const more = card.getByRole('button',{name:'More for Estimate set'})
    const menuStable = await controlStability(page,{more})
    await menuStable.check(() => more.click()); menuStable.done()
    await anchored(pop,more,page)
    await shot(page,'row-menu',width,theme)
    await pop.getByRole('button',{name:'Close check editor',exact:true}).click()
    const doneAdd = card.getByRole('button',{name:'Add check to Done'})
    await doneAdd.click(); await anchored(pop,doneAdd,page)
    await expect(pop.getByRole('menuitem',{name:/^Acceptance criteria written/})).toContainText('All tickets · usually Ready')
    await pop.getByRole('button',{name:'Close check editor',exact:true}).click()
    if (width === 390) {
      for (const dot of await card.locator('.db-dot').all()) await expect(dot).toBeHidden()
      await expect(card.locator('[data-block="in_progress"] h3 > span')).toHaveText('to enter In progress',{useInnerText:true})
    }
  })
})

// Item 4 risk: distinct catalog checks all display the generic checklist icon,
// or a check changes its icon between a row, the Add menu and the fold.
test('AEON-1316 FIX4 catalog SVG icons match rows Add menus and folded checks', async ({browser}) => {
  await evidenceMatrix(browser, async (page,world,width,theme) => {
    const all: DefinitionChecks = {in_progress:[],qa:[],done:[]}
    for (const item of DEFINITION_CATALOG) all[item.block]!.push({id:item.id})
    world.context.definition = definition(all,'project')
    await page.goto('/p/PHAROS/settings/definitions')
    const card = page.locator('#definitions'), signatures = new Map<string,string>()
    const signature = (icon: Locator) => icon.evaluate(el => [...el.children].map(child => child.outerHTML).join(''))
    for (const item of DEFINITION_CATALOG) {
      const icon = card.locator(`[data-check="${item.id}"] > svg`)
      await expect(icon).toHaveAttribute('aria-hidden','true')
      signatures.set(item.id,await signature(icon))
    }
    expect(new Set(signatures.values()).size).toBe(10)
    await expect(card.locator('[data-check="estimate_set"] > svg circle')).toHaveCount(1)
    await expect(card.locator('[data-check="estimate_set"] > svg path')).toHaveAttribute('d','M8 4.6V8l2.2 1.4')
    await expect(card.locator('[data-check="acceptance_present"] > svg path')).toHaveAttribute('d','M5.6 4h8M5.6 8h8M5.6 12h8M2.4 4h.01M2.4 8h.01M2.4 12h.01')
    await expect(card.locator('[data-check="design_attached"] > svg rect')).toHaveCount(1)
    await expect(card.locator('[data-check="blocker_named"] > svg path')).toHaveAttribute('d','m3.9 12.1 8.2-8.2')
    await expect(card.locator('[data-check="ci_green_latest_main"] > svg rect')).toHaveCount(2)
    await expect(card.locator('[data-check="review_ok_cross_family"] > svg path').last()).toHaveAttribute('d','m5.9 8 1.5 1.5 2.8-2.9')
    await expect(card.locator('[data-check="visual_ok"] > svg circle')).toHaveCount(1)
    await expect(card.locator('[data-check="release_copy_set"] > svg path')).toHaveAttribute('d','M2.5 4.5h11M2.5 8h11M2.5 11.5h6.5')
    await expect(card.locator('[data-check="merged"] > svg circle')).toHaveCount(3)
    await expect(card.locator('[data-check="released"] > svg path')).toHaveAttribute('d','M8 2 14 5 8 8 2 5ZM2 8.2l6 3 6-3M2 11.2l6 3 6-3')
    await shot(page,'catalog-icons',width,theme)
    world.context = {...world.context,definition:definition({},'project')}
    await page.reload(); await card.getByRole('button',{name:'Add check to Ready'}).click()
    for (const item of DEFINITION_CATALOG) {
      const entry = page.locator('.definition-pop').getByRole('menuitem').filter({has:page.getByText(item.en,{exact:true})})
      expect(await signature(entry.locator(':scope > svg')),item.id).toBe(signatures.get(item.id))
    }
    world.context = {...world.context,definition:definition({done:[{id:'manual:review',label:'Release notes reviewed'}]},'project'),workspace_default:definition(all),copied_from_checks:all}
    await page.reload(); await card.locator('.fold-head button').click()
    for (const item of DEFINITION_CATALOG) expect(await signature(card.locator(`[data-fold-row="${item.id}"] > svg`)),item.id).toBe(signatures.get(item.id))
    await expect(card.locator('[data-check="manual:review"] > svg circle')).toHaveCount(1)
    await shot(page,'fold-icons',width,theme)
  })
})

// Item 5 risk: a fixed-condition check can be changed to an unrelated condition,
// while removing that selector also accidentally removes kind/label filtering.
test('AEON-1316 FIX4 Applies to offers only All Code UI and keeps fixed conditions as text', async ({browser}) => {
  await evidenceMatrix(browser, async (page,world,width,theme) => {
    const fixed = [{id:'blocker_named',condition:'blocked'}, {id:'release_copy_set',condition:'release_notes'}, {id:'pills_benefits_set',condition:'release_notes'}, {id:'released',condition:'releases'}] as const
    world.context.definition = definition({in_progress:[{id:'estimate_set'},...fixed.map(rule => ({...rule,kinds:['work'],labels:['security']}))]},'project')
    await page.goto('/p/PHAROS/settings/definitions')
    const card = page.locator('#definitions')
    await card.getByRole('button',{name:'More for Estimate set'}).click(); await page.getByRole('menuitem',{name:'Applies to…',exact:true}).click()
    const form = page.locator('.definition-pop')
    await expect(form.getByRole('radio')).toHaveCount(3)
    const stable = await controlStability(page,{options:form.locator('fieldset'),all:form.getByRole('radio',{name:'All tickets',exact:true}),code:form.getByRole('radio',{name:'Code tickets',exact:true}),ui:form.getByRole('radio',{name:'UI tickets',exact:true}),save:form.getByRole('button',{name:/^Save/})})
    for (const [name,detail] of [['All tickets','Every ticket'],['Code tickets','Tickets that change code'],['UI tickets','Tickets labelled ui']]) {
      const option = form.getByRole('radio',{name,exact:true})
      await expect(option).toHaveAccessibleDescription(detail)
      await stable.check(() => option.check())
    }
    stable.done()
    await shot(page,'applies-to',width,theme)
    await form.getByRole('button',{name:/^Save/}).click(); await expect(form).toHaveCount(0)
    for (const rule of fixed) {
      await card.locator(`[data-check="${rule.id}"]`).getByRole('button',{name:/^More for/}).click(); await page.getByRole('menuitem',{name:'Applies to…',exact:true}).click()
      await expect(form.getByRole('radio')).toHaveCount(0)
      const words = rule.condition === 'blocked' ? 'Blocked tickets' : rule.condition === 'releases' ? 'If the project uses releases' : 'Unless hidden from release notes'
      await expect(form.locator('.fixed-condition')).toHaveText(`Applies to${words}`)
      await expect(form.getByRole('textbox',{name:'Ticket kinds (comma-separated)'})).toHaveValue('work')
      await expect(form.getByRole('textbox',{name:'Required labels (comma-separated)'})).toHaveValue('security')
      if (rule.id === 'release_copy_set') await shot(page,'fixed-condition',width,theme)
      await form.getByRole('button',{name:/^Save/}).click(); await expect(form).toHaveCount(0)
      expect((world.writes.at(-1)!.body.checks as DefinitionChecks).in_progress!.find(item => item.id === rule.id)).toMatchObject({...rule,kinds:['work'],labels:['security']})
    }
  })
})

// Item 6 risk: condition and difference words are ellipsized, especially with
// German labels, or folded Add back shrinks its wrapped row under the pointer.
test('AEON-1316 FIX4 condition and difference lines wrap without truncation at every width', async ({browser}) => {
  await evidenceMatrix(browser, async (page,world,width,theme) => {
    world.context.definition = definition({in_progress:[{id:'estimate_set',condition:'code',kinds:['Veroeffentlichungspruefung'],labels:['Barrierefreiheit']}],done:[{id:'manual:review',label:'Freigabe der vollständig beschriebenen Abnahmekriterien für die Veröffentlichung'}]},'project')
    await page.goto('/p/PHAROS/settings/definitions')
    const card = page.locator('#definitions')
    await card.locator('.fold-head button').click()
    const words = card.locator('[data-check="estimate_set"] .dr-l small')
    await expect(words).toContainText('workspace default: All tickets')
    let wrapped = 0
    for (const line of await card.locator('.dr-l small').all()) {
      const layout = await line.evaluate(el => { const css = getComputedStyle(el); return {whiteSpace:css.whiteSpace,overflow:css.textOverflow,clamp:css.webkitLineClamp,width:el.clientWidth,scrollWidth:el.scrollWidth,height:el.clientHeight,scrollHeight:el.scrollHeight,lineHeight:parseFloat(css.lineHeight)} })
      expect(layout.whiteSpace).toBe('normal'); expect(layout.overflow).not.toBe('ellipsis'); expect(layout.clamp).toBe('none')
      expect(layout.scrollWidth).toBeLessThanOrEqual(layout.width+1)
      expect(layout.scrollHeight).toBeLessThanOrEqual(layout.height+1)
      if (layout.height > layout.lineHeight*1.5) wrapped++
    }
    if (width <= 1024) expect(wrapped,'long condition or difference text actually wraps').toBeGreaterThan(0)
    await shot(page,'wrapped-differences',width,theme)
    const row = card.locator('[data-fold-row="acceptance_present"]'), stable = await controlStability(page,{row,header:card.locator('.fold-head')})
    await stable.check(async () => { await row.getByRole('button',{name:'Add back',exact:true}).click(); await expect(row).toContainText('Added back to Ready') }); stable.done()
  })
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
  await expect(page.locator('.toast.error')).toContainText('Definition save unavailable')
  await expect(page.locator('.toast.error').getByRole('button',{name:'Reload',exact:true})).toBeVisible()
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
