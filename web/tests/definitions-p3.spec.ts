// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { writeFile } from 'node:fs/promises'
import { expect, test, type Browser, type Locator, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { reviewPolicy } from './policies-fixtures'
import { definitionChangeWords, definitionDate, definitionDecisionWords, definitionDeskWords, definitionNotice, type Definition, type DefinitionChange, type DefinitionChecks, type DefinitionContext, type DefinitionStatus } from '../src/lib/definitions'

const blocks: DefinitionStatus[] = ['in_progress','qa','done']
const base: DefinitionChecks = {in_progress:[{id:'estimate_set'},{id:'acceptance_present'},{id:'design_attached',condition:'ui'}],qa:[{id:'review_ok_cross_family',condition:'code'}],done:[{id:'release_copy_set'}]}
const newer: DefinitionChecks = {in_progress:[{id:'estimate_set',condition:'code'},{id:'design_attached',condition:'ui'},{id:'no_active_build'}],qa:base.qa,done:base.done}
const changes: DefinitionChange[] = [
  {check_id:'estimate_set',block:'in_progress',kind:'changed',before:{id:'estimate_set'},current:{id:'estimate_set',condition:'code'}},
  {check_id:'acceptance_present',block:'in_progress',kind:'removed',before:{id:'acceptance_present'},current:null},
  {check_id:'no_active_build',block:'in_progress',kind:'new',before:null,current:{id:'no_active_build'}},
]
const copy = (checks: DefinitionChecks, source: 'project' | 'workspace' = 'project'): Definition => ({id:source === 'project' ? '13171317-1317-4317-8317-131713171317' : '00000000-0000-4000-8000-000000001317',revision:source === 'project' ? '2026-10-08T08:00:00Z' : '2026-10-09T08:00:00Z',created_at:'2026-10-08T08:00:00Z',source,copied_from_revision:source === 'project' ? '2026-10-07T08:00:00Z' : null,checks:structuredClone(checks)})
// Run the affected unit file once inside the same guarded remote job, before
// allocating a browser. The approved remote runner exposes Playwright only.
let unitOutput=''
test.beforeAll(()=>{
  test.setTimeout(90_000)
  unitOutput=execFileSync(process.execPath,['node_modules/vitest/vitest.mjs','run','tests/definitions.unit.test.ts','--maxWorkers=1'],{encoding:'utf8',timeout:60_000,maxBuffer:1024*1024})
})
type Check = {assert: string; width: number; theme: string; locale: string; passed: true; [key: string]: unknown}
async function evidence(checks: Check[]) {
  const body=JSON.stringify({ticket:'AEON-1317',head:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),units:{file:'tests/definitions.unit.test.ts',output:unitOutput},checks},null,2)
  await writeFile(test.info().outputPath('checks.json'),body)
  // The guarded remote runner retrieves Markdown and PNG attachments.
  await writeFile(test.info().outputPath('checks.md'),body)
}
async function shot(page: Page,width: number,theme: string,state: string){await page.screenshot({path:test.info().outputPath(`aeon1317-${width}-${theme}-${state}.png`),fullPage:true})}
function barrier(){let entered!:()=>void,release!:()=>void;const pending=new Promise<void>(resolve=>entered=resolve),until=new Promise<void>(resolve=>release=resolve);return {entered,release,pending,until}}
async function navigate(page: Page,path: string){await page.evaluate(path=>(document.querySelector('#app') as unknown as {__vue_app__:{config:{globalProperties:{$router:{push(path:string):Promise<void>}}}}}).__vue_app__.config.globalProperties.$router.push(path),path)}

async function setup(page: Page,theme: 'light' | 'dark',locale='en',viewer=false){
  await mockDecisionDesk(page,{emptyDesk:true,theme})
  const settings=settingsData();settings.profile.locale=locale==='de'?'de-AT':'en-GB';await mockSettings(page,settings)
  const world={workspace:copy(newer,'workspace'),context:{project_id:'p-frozen',definition:copy(base),workspace_default:null,copied_from_checks:structuredClone(base),pending_changes:structuredClone(changes),can_manage:!viewer,admins:[{id:settings.profile.principal_id,name:'Markus Barta'}]} as DefinitionContext,writes:[] as {path:string;body:Record<string,unknown>}[],fail:false,held:null as ReturnType<typeof barrier>|null,summaryFail:false,readOnly:viewer}
  world.context.workspace_default=world.workspace
  await page.addInitScript(() => {
    const targets=new Set<EventTarget>(),add=EventTarget.prototype.addEventListener
    EventTarget.prototype.addEventListener=function(type,...args){if(type==='definition.saved')targets.add(this);return add.call(this,type,...args)}
    Object.assign(window,{definitionChanged:()=>{for(const target of targets)target.dispatchEvent(new MessageEvent('definition.saved',{data:'{}'}))}})
  })
  const errors: string[]=[]
  page.on('pageerror',error=>errors.push(error.message))
  await page.route('**/api/**',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname
    if(path==='/api/me/permissions')return route.fulfill({json:mockEffectivePermissions(world.readOnly?'viewer':'admin',url.searchParams.get('project_id')??undefined)})
    if(path==='/api/preferences/theme')return route.fulfill({json:{key:'theme',value:{choice:theme}}})
    if(path==='/api/settings/review-policy')return route.fulfill({json:reviewPolicy()})
    if(path==='/api/decision-desk/projection'){
      const count=world.context.pending_changes?.length??0,href='/p/PRJ-26/settings/definitions?definition_changes=1#definition-changes'
      const items=count&&!world.readOnly?[{id:world.context.definition!.id,kind:'definition_change',project_id:'p-frozen',revision:1,title:definitionDeskWords('PRJ-26',count),created_at:world.workspace.revision,held:false,can_decide:true,href,source:href,definition_change:{project_key:'PRJ-26',pending_count:count}}]:[]
      return route.fulfill({json:{items,counts:{open:items.length,held:0,chores:0},has_more:false,as_of:'2026-10-19T08:00:00Z'}})
    }
    if(path==='/api/settings/definitions/context'){
      if(world.summaryFail)return route.fulfill({status:503,json:{error:'Project differences unavailable'}})
      return route.fulfill({json:{definition:world.workspace,can_manage:!world.readOnly,admins:world.context.admins,projects:[{project_id:'p-frozen',key:'PRJ-26',difference_count:3,pending_count:world.context.pending_changes?.length??0},{project_id:'p-aeon',key:'AEON',difference_count:2,pending_count:0},{project_id:'p-glint',key:'GLINT',difference_count:2,pending_count:0}],differ_count:3,uses_default:['PHAROS'],next_cursor:null}})
    }
    if(path==='/api/settings/definitions'&&request.method()==='PUT'){
      const body=request.postDataJSON();world.writes.push({path,body})
      if(world.fail)return route.fulfill({status:503,json:{error:'Default save unavailable'}})
      if(body.expected_revision!==world.workspace.revision)return route.fulfill({status:409,json:{error:'stale workspace'}})
      world.workspace={...world.workspace,checks:body.checks,revision:new Date(Date.parse(world.workspace.revision)+1000).toISOString()};world.context.workspace_default=world.workspace
      return route.fulfill({json:world.workspace})
    }
    if(/^\/api\/projects\/[^/]+\/definitions\/context$/.test(path)){
      const id=path.split('/')[3]
      return route.fulfill({json:id==='p-frozen'?world.context:{project_id:id,definition:world.workspace,workspace_default:world.workspace,copied_from_checks:null,pending_changes:[],can_manage:!world.readOnly,admins:world.context.admins}})
    }
    if(path==='/api/projects/p-frozen/definitions/decisions'){
      const body=request.postDataJSON();world.writes.push({path,body})
      if(world.held){const hold=world.held;world.held=null;hold.entered();await hold.until}
      if(world.fail)return route.fulfill({status:503,json:{error:'Decision save unavailable'}})
      if(world.readOnly)return route.fulfill({status:403,json:{error:'Only project admins decide'}})
      const current=world.context.definition!,pending=world.context.pending_changes!,change=pending.find(change=>change.check_id===body.check_id)
      if(!change||body.expected_revision!==current.revision||body.expected_workspace_revision!==world.workspace.revision)return route.fulfill({status:409,json:{error:'Definition changed'}})
      const checks=structuredClone(current.checks)
      if(body.decision==='accept'){
        for(const block of blocks)checks[block]=(checks[block]??[]).filter(rule=>rule.id!==change.check_id)
        if(change.current)(checks[change.block]??=[]).push(change.current)
      }
      world.context={...world.context,definition:{...current,checks,revision:new Date(Date.parse(current.revision)+1000).toISOString()},pending_changes:pending.filter(row=>row!==change)}
      return route.fulfill({json:world.context})
    }
    if(path==='/api/projects/p-frozen/definitions'&&request.method()==='PUT'){
      const body=request.postDataJSON();world.writes.push({path,body});world.context={...world.context,definition:{...world.context.definition!,checks:body.checks,revision:new Date(Date.parse(world.context.definition!.revision)+1000).toISOString()}}
      return route.fulfill({json:world.context.definition})
    }
    return route.fallback()
  })
  return {world,errors}
}

async function matrix(browser: Browser,run:(page:Page,width:number,theme:'light'|'dark')=>Promise<void>){
  for(const width of [1440,1024,390])for(const theme of ['light','dark'] as const){const context=await browser.newContext({viewport:{width,height:1000},hasTouch:width===390,isMobile:width===390,colorScheme:theme,reducedMotion:'reduce'});try{await run(await context.newPage(),width,theme)}finally{await context.close()}}
}
async function geometry(rows: Locator){return rows.evaluateAll(elements=>elements.map(el=>{const r=el.getBoundingClientRect();let y=r.y+window.scrollY;for(let p=el.parentElement;p;p=p.parentElement)if(p!==document.scrollingElement)y+=p.scrollTop;return {id:el.getAttribute('data-check'),x:r.x,y,width:r.width,height:r.height}}))}

// Risks: deciding any of the six actions moves another row, changes the copy
// before confirmation, loses focus, or removes the final notice under a pointer.
test('AEON-1317 default decisions preserve every row notice copy and focus in all viewports',async({browser})=>{
  test.setTimeout(300_000);const checks:Check[]=[]
  await matrix(browser,async(page,width,theme)=>{
    const {world,errors}=await setup(page,theme)
    const record=(assert:string,detail:Record<string,unknown>={})=>checks.push({assert,width,theme,locale:'en',passed:true,...detail})
    for(const choice of ['accept','keep'] as const){
      world.context={...world.context,definition:copy(base),pending_changes:structuredClone(changes)}
      await page.goto('/p/PRJ-26/settings/definitions')
      const card=page.locator('#definitions'),notice=card.locator('#definition-changes'),rows=card.locator('.dblock [data-check]')
      await expect(rows).toHaveCount(6);await expect(card.locator('[data-waiting="true"]')).toHaveCount(3)
      await expect(notice).toContainText('3 changes wait');expect(world.context.definition!.checks).toEqual(base)
      record('notice count equals waiting rows and copy retains enforced checks',{waiting:3})
      const ordinary=card.locator('[data-check="design_attached"]'),start=await geometry(rows)
      for(const row of await card.locator('[data-block="in_progress"] [data-check]').all())expect((await row.boundingBox())!.height).toBe((await ordinary.boundingBox())!.height)
      const labels=await rows.locator('.dr-l').evaluateAll(elements=>elements.map(el=>el.getBoundingClientRect().x))
      expect(Math.max(...labels)-Math.min(...labels)).toBeLessThanOrEqual(1)
      record('pending and ordinary row height and reserved-slot alignment',{alignmentPx:Math.max(...labels)-Math.min(...labels)})
      await shot(page,width,theme,'D3')
      const allControls:Record<string,Locator>={notice}
      for(let i=0;i<await rows.count();i++)allControls[`row${i}`]=rows.nth(i)
      const stable=await controlStability(page,allControls)
      await stable.check(async()=>{await ordinary.getByRole('button',{name:'More for Approved design attached'}).click();await page.getByRole('menuitem',{name:'Remove',exact:true}).click();await expect(ordinary).toContainText('Removed')})
      await stable.check(async()=>{await ordinary.getByRole('button',{name:'Undo',exact:true}).click();await expect(ordinary).not.toContainText('Removed')})
      expect(world.context.definition!.checks).toEqual(base)
      record('P2 row menu removal and Undo preserve the row and stored copy during a waiting-change visit',{movementPx:0})
      for(const change of changes){
        const row=card.locator(`[data-check="${change.check_id}"]`),action=row.locator(`[data-decision="${choice}"]`),words=definitionDecisionWords(change,change.before??undefined)
        const firstNext=await card.locator('[data-waiting="true"]').evaluateAll((elements,id)=>{const index=elements.findIndex(el=>el.getAttribute('data-check')===id);return elements[(index+1)%elements.length]?.getAttribute('data-check')},change.check_id)
        const before=structuredClone(world.context.definition!.checks),requestRevision=world.context.definition!.revision
        await expect(action).toHaveText(words[choice==='accept'?0:1])
        await stable.check(async()=>{await action.click();await expect(row).toHaveAttribute('data-waiting','false')})
        expect(await geometry(rows),'exactly zero movement of every row').toEqual(start)
        const body=world.writes.at(-1)!.body
        expect(body).toEqual({check_id:change.check_id,decision:choice,expected_revision:requestRevision,expected_workspace_revision:world.workspace.revision})
        const stored=blocks.flatMap(block=>world.context.definition!.checks[block]??[]).find(rule=>rule.id===change.check_id)
        if(choice==='keep')expect(world.context.definition!.checks).toEqual(before)
        else if(change.kind==='removed'){expect(stored).toBeUndefined();await expect(row.getByRole('button',{name:'Undo',exact:true})).toBeVisible();await expect(row).toContainText('Removed')}
        else{expect(stored).toEqual(change.current);if(change.kind==='new')await expect(row).toContainText('Added for PRJ-26')}
        const remaining=world.context.pending_changes!.length
        await expect(card.locator('[data-waiting="true"]')).toHaveCount(remaining)
        if(remaining)await expect(card.locator(`[data-check="${firstNext}"] [data-decision="accept"]`)).toBeFocused()
        else await expect(row).toBeFocused()
        record(`decision ${change.kind} ${choice}: zero movement, unchanged height, stored copy and focus`,{movementPx:0,remaining})
      }
      stable.done();await expect(notice.locator('.notice-copy > span').first()).toHaveText('All changes decided.')
      record('last decision preserves the notice box and says All changes decided.',{movementPx:0})
      await shot(page,width,theme,'D4')
      if(choice==='accept'){
        const removed=card.locator('[data-check="acceptance_present"]'),undoStable=await controlStability(page,allControls)
        await undoStable.check(async()=>{await removed.getByRole('button',{name:'Undo',exact:true}).click();await expect(removed).not.toContainText('Removed')});undoStable.done()
        expect(world.context.definition!.checks.in_progress!.some(rule=>rule.id==='acceptance_present')).toBe(true)
        record('removed ghost Undo restores the same copy check without row movement',{movementPx:0})
      }
      await page.reload();await expect(card.locator('#definition-changes')).toHaveCount(0);record('next visit omits a completed notice')
    }
    expect(errors).toEqual([])
  });await evidence(checks)
})

// Risks: workspace card order/differences are wrong, inherited rows are editable
// or stale, an empty default claims enforcement, or viewer actions leak.
test('AEON-1317 workspace defaults differences empty inherited and viewer match the approved states',async({browser})=>{
  test.setTimeout(300_000);const checks:Check[]=[]
  await matrix(browser,async(page,width,theme)=>{
    const {world,errors}=await setup(page,theme),record=(assert:string)=>checks.push({assert,width,theme,locale:'en',passed:true})
    await page.goto('/settings/policies#definitions');const card=page.locator('#definitions')
    await expect(card.getByRole('heading',{name:'Definitions',exact:true})).toBeVisible()
    expect(await page.locator('#cross-family-review').evaluate((review)=>!!review.parentElement?.querySelector('#definitions')&&(review.compareDocumentPosition(review.parentElement!.querySelector('#definitions')!)&Node.DOCUMENT_POSITION_FOLLOWING)!==0)).toBe(true)
    const toggle=card.getByRole('button',{name:'3 projects differ',exact:true}),stable=await controlStability(page,{toggle,firstRow:card.locator('[data-check]').first()})
    await stable.check(()=>toggle.click());stable.done()
    const list=card.locator('#definition-project-differences');await expect(list.locator('li')).toHaveCount(3)
    await expect(card).toContainText('PHAROS uses this default');await expect(list).toContainText('3 changes wait');await expect(list).toContainText('Differs in 2 places')
    for(const line of await list.locator('li a').all()){expect((await line.boundingBox())!.height).toBeGreaterThanOrEqual(44);await expect(line).toHaveAttribute('href',/\/settings\/definitions$/)}
    record('D1 workspace editor follows Cross-family review; exact differing count, inherited names and touch-sized project links')
    await shot(page,width,theme,'D1')
    // A real workspace edit is sent through the shared editor and appears on an inherited project.
    const add=card.getByRole('button',{name:'Add check to Done'});await add.click();await page.getByRole('menuitem',{name:/^Shipped in a release/}).click()
    await expect(card.locator('[data-check="released"]')).toBeVisible()
    expect(world.writes.at(-1)!.path).toBe('/api/settings/definitions')
    await navigate(page,'/p/PHAROS/settings/definitions');await expect(card.locator('[data-check="released"]')).toBeVisible()
    await expect(card.locator('.drs button')).toHaveCount(0)
    world.workspace={...world.workspace,checks:{...world.workspace.checks,done:[...(world.workspace.checks.done??[]),{id:'merged'}]},revision:'2026-10-10T08:00:00Z'};world.context.workspace_default=world.workspace
    await page.evaluate(()=>(window as unknown as {definitionChanged:()=>void}).definitionChanged());await expect(card.locator('[data-check="merged"]')).toBeVisible()
    expect(world.context.definition!.checks).toEqual(base)
    record('D5 default edit applies immediately with read-only inherited rows, including a live edit while the page is open');await shot(page,width,theme,'D5')
    world.workspace={...world.workspace,checks:{}};await navigate(page,'/settings/policies#definitions')
    await expect(card).toContainText('No workspace default. Tickets move freely in every project that has no definition of its own.')
    await expect(card.locator('.enforce')).toHaveCount(0);record('D2 empty default has the catalog explanation and no enforcement claim');await shot(page,width,theme,'D2')
    world.workspace=copy(newer,'workspace');world.context.workspace_default=world.workspace;world.readOnly=true;world.context.can_manage=false
    await page.goto('/p/PRJ-26/settings/definitions?as=mia')
    await expect(card.locator('[data-waiting="true"]')).toHaveCount(3)
    await expect(card.locator('[data-decision], .drs button')).toHaveCount(0)
    await expect(card.locator('.lock-line')).toContainText('Markus Barta')
    const starts=await card.locator('[data-check] .dr-l').evaluateAll(elements=>elements.map(el=>el.getBoundingClientRect().x))
    expect(Math.max(...starts)-Math.min(...starts)).toBeLessThanOrEqual(1)
    await expect(card.locator('[data-check] .dr-more-sp')).toHaveCount(6)
    record('D7 waiting notes remain, actions are absent and reserved slots align within one pixel');await shot(page,width,theme,'D7')
    const design=await card.locator('.default-notice').evaluate(el=>{const css=getComputedStyle(el);return {left:parseFloat(css.borderLeftWidth),top:parseFloat(css.borderTopWidth),shadow:css.boxShadow,italic:css.fontStyle,icons:el.querySelectorAll('svg').length}})
    expect(design).toEqual({left:0,top:0,shadow:'none',italic:'normal',icons:1});record('notice uses a history SVG and a plain rule line without coloured accents pills glows or italics')
    expect(errors).toEqual([])
  });await evidence(checks)
})

// Risk: the Desk leaks a project administration decision, its link misses the
// notice or first action, or its item survives the last decision.
test('AEON-1317 Desk opens waiting definitions and resolves only after the last admin decision',async({browser})=>{
  test.setTimeout(180_000);const checks:Check[]=[]
  await matrix(browser,async(page,width,theme)=>{
    const {world,errors}=await setup(page,theme)
    await page.goto('/decision-desk')
    const row=page.getByTestId(`desk-row-d:${world.context.definition!.id}`)
    await expect(row).toContainText('PRJ-26: 3 changes to the workspace default wait for a decision')
    await shot(page,width,theme,'D6')
    const open=row.getByRole('link',{name:'Open',exact:true}),stable=await controlStability(page,{row,open})
    await stable.check(async()=>{await open.focus()});stable.done()
    await open.click();await expect(page).toHaveURL(/\/p\/PRJ-26\/settings\/definitions\?definition_changes=1#definition-changes$/)
    await expect(page.locator('#definition-changes')).toBeInViewport()
    await expect(page.locator('[data-check="estimate_set"] [data-decision="accept"]')).toBeFocused()
    checks.push({assert:'D6 admin item Open lands on the notice with the first waiting action focused',width,theme,locale:'en',passed:true})
    for(const change of changes){await page.locator(`[data-check="${change.check_id}"] [data-decision="keep"]`).click();await expect(page.locator(`[data-check="${change.check_id}"]`)).toHaveAttribute('data-waiting','false')}
    await navigate(page,'/decision-desk');await expect(row).toHaveCount(0)
    checks.push({assert:'Desk resolves when no changes wait',width,theme,locale:'en',passed:true})
    world.readOnly=true;world.context.pending_changes=structuredClone(changes);await page.reload();await expect(row).toHaveCount(0)
    checks.push({assert:'Desk item is absent for a read-only project viewer',width,theme,locale:'en',passed:true});expect(errors).toEqual([])
  });await evidence(checks)
})

// Risk: new German copy is missing from the actual UI, dates/counts break at a
// line edge, or long translated actions overflow the phone.
test('AEON-1317 German definitions copy covers every P3 string and singular notices',async({browser})=>{
  test.setTimeout(180_000);const checks:Check[]=[]
  for(const width of [1440,390]){
    const context=await browser.newContext({viewport:{width,height:1000},hasTouch:width===390,isMobile:width===390,reducedMotion:'reduce'}),page=await context.newPage()
    try{
      const {world,errors}=await setup(page,'light','de')
      await page.goto('/p/PRJ-26/settings/definitions')
      const card=page.locator('#definitions'),date=definitionDate(world.workspace.revision,'de')
      await expect(card.locator('.notice-copy > span').first()).toHaveText(definitionNotice('PRJ-26',3,date,'de'))
      const snapshot:Record<string,unknown>={notice:await card.locator('.notice-copy > span').first().textContent(),rows:[]}
      for(const change of changes){const row=card.locator(`[data-check="${change.check_id}"]`);await expect(row).toContainText(definitionChangeWords(change,date,'de'));for(const word of definitionDecisionWords(change,change.before??undefined,'de'))await expect(row.getByRole('button',{name:word,exact:true})).toBeVisible();(snapshot.rows as unknown[]).push(await row.innerText())}
      await shot(page,width,'light','D3-de')
      for(const id of ['estimate_set','acceptance_present']){await card.locator(`[data-check="${id}"] [data-decision="keep"]`).click();await expect(card.locator(`[data-check="${id}"]`)).toHaveAttribute('data-waiting','false')}
      await expect(card.locator('.notice-copy > span').first()).toHaveText(definitionNotice('PRJ-26',1,date,'de'))
      await card.locator('[data-check="no_active_build"] [data-decision="keep"]').click();await expect(card.locator('.notice-copy > span').first()).toHaveText('Alle Änderungen entschieden.')
      snapshot.decided=await card.locator('.notice-copy > span').first().textContent()
      await navigate(page,'/settings/policies#definitions');await card.getByRole('button',{name:'3 Projekte weichen ab',exact:true}).click()
      await expect(card).toContainText('PHAROS nutzt diesen Standard');await expect(card).toContainText('Weicht an 2 Stellen ab')
      world.context.pending_changes=structuredClone(changes);await page.reload();await card.getByRole('button',{name:'3 Projekte weichen ab',exact:true}).click();await expect(card).toContainText('3 Änderungen warten')
      world.context.pending_changes=structuredClone(changes.slice(0,2));await page.reload();await card.getByRole('button',{name:'3 Projekte weichen ab',exact:true}).click();await expect(card).toContainText('2 Änderungen warten');snapshot.differ=await card.locator('.differ').innerText();world.context.pending_changes=structuredClone(changes);await shot(page,width,'light','D1-de')
      world.workspace={...world.workspace,checks:{}};await page.reload();await expect(card).toContainText('Kein Workspace-Standard. Tickets bewegen sich frei in jedem Projekt ohne eigene Definition.');snapshot.empty=await card.locator('.origin').innerText()
      await shot(page,width,'light','D2-de')
      await navigate(page,'/decision-desk');const row=page.getByTestId(`desk-row-d:${world.context.definition!.id}`);await expect(row).toContainText(definitionDeskWords('PRJ-26',3,'de'));await expect(row.getByRole('link',{name:'Öffnen',exact:true})).toBeVisible();snapshot.desk=await row.innerText();await shot(page,width,'light','D6-de')
      expect(await page.evaluate(()=>document.documentElement.scrollWidth-document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
      checks.push({assert:'all section 4 German strings render in the locale snapshot including singular plural dates and Desk Open',width,theme:'light',locale:'de',passed:true,snapshot});expect(errors).toEqual([])
    }finally{await context.close()}
  }await evidence(checks)
})

// Risk: a failure removes a waiting action, or an old project's delayed result
// overwrites the next record and reports success there. Barriers prove overlap.
test('AEON-1317 failed stale and identity-bound decisions never claim success',async({page})=>{
  await page.setViewportSize({width:1440,height:1000})
  const {world,errors}=await setup(page,'light');await page.goto('/p/PRJ-26/settings/definitions')
  const action=page.locator('[data-check="estimate_set"] [data-decision="accept"]')
  world.fail=true;await action.click();await expect(page.locator('.toast.error')).toContainText('Decision save unavailable');await expect(action).toBeEnabled();expect(world.context.pending_changes).toHaveLength(3);expect(world.context.definition!.checks).toEqual(base)
  world.fail=false;world.workspace={...world.workspace,revision:'2026-10-10T08:00:00Z'};await action.click();await expect(page.locator('.toast.error')).toContainText('The definition or workspace default changed.');expect(world.context.pending_changes).toHaveLength(3)
  world.context.workspace_default=world.workspace;await page.locator('.toast.error').getByRole('button',{name:'Reload',exact:true}).click();await expect(action).toBeEnabled()
  const hold=barrier();world.held=hold;await action.click();await hold.pending
  await navigate(page,'/p/PHAROS/settings/definitions');await expect(page.locator('#definition-changes')).toHaveCount(0);hold.release()
  await expect(page.locator('#definitions .origin')).toContainText('Uses the workspace default');await expect(page.getByText('Decision saved.',{exact:true})).toHaveCount(0)
  expect(world.writes.every(write=>write.path==='/api/projects/p-frozen/definitions/decisions')).toBe(true)
  await navigate(page,'/settings/policies#definitions')
  const workspaceCard=page.locator('#definitions'),first=workspaceCard.locator('[data-check="estimate_set"]')
  await expect(first).toBeVisible();world.fail=true
  const stable=await controlStability(page,{first,head:workspaceCard.locator('[data-block="in_progress"] .db-head')})
  await stable.check(async()=>{await first.getByRole('button',{name:'More for Estimate set'}).click();await page.getByRole('menuitem',{name:'Remove',exact:true}).click();await expect(page.locator('.toast.error')).toContainText('Default save unavailable')});stable.done()
  await expect(first).not.toContainText('Removed');await page.getByRole('button',{name:'Close check editor',exact:true}).click()
  world.fail=false;world.summaryFail=true
  await workspaceCard.getByRole('button',{name:'Add check to Done'}).click();await page.getByRole('menuitem',{name:/^Shipped in a release/}).click()
  await expect(workspaceCard.locator('[data-check="released"]')).toBeVisible();await expect(page.locator('.toast.error')).toContainText('Project differences unavailable')
  await expect(page.getByText('Workspace default saved.',{exact:true})).toHaveCount(0)
  expect(errors).toEqual([]);await evidence([{assert:'failed decisions retain copy and waiting rows; barrier-held results cannot cross project identity; failed and partial workspace saves report errors without shifting rows',width:page.viewportSize()!.width,theme:'light',locale:'en',passed:true}])
})
