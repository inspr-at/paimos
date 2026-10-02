// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData } from './business-fixtures'
import { expectStableControls } from './helpers/stable'

for (const width of [390, 1440]) {
  test(`ticket placement choices and confirmation hold still at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const data = fixtures(), node = data.nodes.find(n => n.id === 'n-2')!
    Object.assign(node.fields, { area: 'backend', area_source: 'suggested', area_confirmed: false, complexity: 'M', complexity_source: 'suggested', complexity_confirmed: false })
    const revision = node.updated_at
    const calls = await mockWork(page, data)
    await page.route('**/api/work-kinds?**', route => {
      expect(new URL(route.request().url()).searchParams.get('project_id')).toBe('p-pharos')
      return route.fulfill({ json: {
      items: ['backend', 'security', 'firmware', 'review', 'other'].map((slug, position) => ({ id: slug, slug, label: slug === 'firmware' ? 'Firmware and embedded hardware' : slug, position, project_id: slug === 'firmware' ? 'p-pharos' : null })), next_cursor: null,
      } })
    })
    await page.goto('/p/PHAROS/PHAROS-12')
    const ws = page.getByRole('complementary', { name: 'Ticket details' })
    const area = ws.getByRole('combobox', { name: 'Kind of work', exact: true })
    const complexity = ws.getByRole('combobox', { name: 'Complexity', exact: true })
    const areaRow = area.locator('..'), complexityRow = complexity.locator('..')
    await expect(area).toBeEnabled()
    await expect(area.locator('option')).toHaveText(['Unspecified', 'backend', 'security', 'Firmware and embedded hardware'])
    await expectStableControls({
      controls: { area, complexity, areaRow, complexityRow }, scrollAreas: { properties: ws.locator('.ws-props') },
      interactions: [
        { name: 'confirm suggested kind', run: async () => { await areaRow.getByRole('button', { name: 'Confirm' }).click(); await expect(areaRow.getByRole('button')).toHaveCount(0); await expect(area).toBeEnabled() } },
        { name: 'choose security', run: async () => { await area.selectOption('security'); await expect(area).toHaveValue('security'); await expect(area).toBeEnabled() } },
        { name: 'choose longer project kind', run: async () => { await area.selectOption('firmware'); await expect(area).toHaveValue('firmware'); await expect(area).toBeEnabled() } },
        { name: 'choose complex', run: async () => { await complexity.selectOption('L'); await expect(complexity).toHaveValue('L'); await expect(complexity).toBeEnabled() } },
      ],
    })
    const writes = calls.filter(call => call.method === 'PATCH' && call.path === '/api/nodes/n-2')
    expect(writes).toHaveLength(4)
    expect(writes[0]!.headers['if-unmodified-since']).toBe(revision)
    const first = writes[0]!.body as { fields: Record<string, unknown> }
    expect(first.fields.area).toBe('backend')
    expect(first.fields).not.toHaveProperty('area_source')
    expect((writes[1]!.body as typeof first).fields.area).toBe('security')
    expect((writes[2]!.body as typeof first).fields.area).toBe('firmware')
    expect((writes[3]!.body as typeof first).fields.complexity).toBe('L')
  })
}

for (const width of [390, 1440]) {
 test(`logged hours load below stationary placement controls at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  const data=fixtures(), node=data.nodes.find(n=>n.id==='n-2')!
  Object.assign(node.fields,{area:'backend',complexity:'M'})
  await mockWork(page,data)
  await page.route('**/api/plugins',route=>route.fulfill({json:businessData().plugins}))
  let releaseTotals!: () => void
  const totalsReady=new Promise<void>(resolve=>{releaseTotals=resolve})
  let sawRequest!: () => void
  const requested=new Promise<void>(resolve=>{sawRequest=resolve})
  await page.route('**/api/nodes/n-2/time-totals**',async route=>{
   sawRequest()
   await totalsReady
   await route.fulfill({json:{duration_seconds:3600,amounts:[]}})
  })
  await page.route('**/api/work-kinds?**',route=>route.fulfill({json:{items:[{id:'backend',slug:'backend',label:'Backend',position:0}],next_cursor:null}}))
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws=page.getByRole('complementary',{name:'Ticket details'})
  const area=ws.getByRole('combobox',{name:'Kind of work',exact:true})
  const complexity=ws.getByRole('combobox',{name:'Complexity',exact:true})
  await expect(area).toBeEnabled()
  await requested
  await expect(ws.locator('.ticket-hours')).toHaveCount(0)
  await expectStableControls({
   controls:{area,complexity,areaRow:area.locator('..'),complexityRow:complexity.locator('..')},
   scrollAreas:{properties:ws.locator('.ws-props')},
   interactions:[{name:'logged hours arrive',run:async()=>{releaseTotals();await expect(ws.locator('.ticket-hours')).toBeVisible();await expect(ws.locator('.ticket-hours')).toContainText('1h')}}],
  })
 })
}
