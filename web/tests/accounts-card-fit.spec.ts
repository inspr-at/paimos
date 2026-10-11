// SPDX-License-Identifier: AGPL-3.0-only
// AEON-355, kept for the capacity row of AEON-1209 (design AEON-1196): the dial's
// units and the accounts chip stay inside the row from 390 to 2560, with the session
// panel open and closed, and the accounts popover stays inside the window. Long names,
// one unbroken account name, a computer with six accounts and one with one, including
// an account with no reading yet. The row, main and the document have no horizontal
// overflow.
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { NOW, TZ, capacityWorld, type CapacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

test.use({ timezoneId: TZ })
const world: AgentWorld = {
  me: me.id, now: NOW,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
const shots = process.env.AEON355_SHOTS
// Email-shaped account name with no spaces: shown whole or with a middle ellipsis, never spilling.
const UNBROKEN = 'productionreleaseautomation@engineering.example.org'

async function setup(page: Page): Promise<CapacityWorld> {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  work.preferences['ui.agents.sections'] = { dial: true, accounts: true, sessions: true, queued: true }
  await mockWork(page, work, { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld({ longNames: true, unread: true })
  const spare = capacity.accounts.find(account => account.harness === 'codex' && account.label.startsWith('Spare'))
  if (!spare) throw new Error('codex spare account missing from the long-name fixture')
  spare.label = UNBROKEN
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  data.approvals = data.approvals.filter(a => a.decision)
  data.messages = data.messages.filter(m => !m.is_action_request)
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', 'account.manage', 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return capacity
}

function widths() {
  const set = new Set<number>()
  for (let w = 390; w <= 1600; w += 20) set.add(w)
  for (const edge of [601, 616, 720, 721, 1044, 1100, 1264, 1265, 1320, 1400, 1440, 1450, 1920, 2560]) set.add(edge)
  return [...set].sort((a, b) => a - b)
}

async function fit(page: Page) {
  return page.evaluate(() => {
    const root = document.documentElement
    const documentOver = Math.max(root.scrollWidth - root.clientWidth, root.scrollWidth - window.innerWidth)
    const main = document.querySelector('main')
    const mainOver = main ? main.scrollWidth - main.clientWidth : null
    const row = document.querySelector('.cap-row')
    if (!row) return { missing: true as const, documentOver, mainOver, offenders: [], pageOffender: '', popover: '' }
    const edge = row.getBoundingClientRect().right
    const describe = (el: Element) => {
      const cls = (el.getAttribute('class') || '').split(/\s+/).filter(Boolean).slice(0, 3).join('.')
      const text = [...el.childNodes].every(node => node.nodeType === 3) ? (el.textContent || '').trim().slice(0, 48) : ''
      return `${el.tagName.toLowerCase()}${cls ? '.' + cls : ''}${text ? ` "${text}"` : ''}`
    }
    const offenders: { over: number; desc: string }[] = []
    for (const el of row.querySelectorAll('*')) {
      const box = el.getBoundingClientRect(), cs = getComputedStyle(el)
      // The unseen − and + of a tight row keep their place over the neighbours; they are not drawn.
      if ((box.width < 1 && box.height < 1) || Number(cs.opacity) === 0 || el.closest('.pm')) continue
      const over = box.right - edge
      if (over > 1) offenders.push({ over: Math.round(over * 10) / 10, desc: describe(el) })
    }
    offenders.sort((a, b) => b.over - a.over)
    let pageOffender = ''
    if (documentOver > 1) {
      let worst = 0
      for (const el of document.body.querySelectorAll('*')) {
        const box = el.getBoundingClientRect()
        if (box.right > window.innerWidth + 1 && box.right > worst) {
          worst = box.right
          pageOffender = describe(el)
        }
      }
    }
    const pop = document.querySelector('.floating')?.getBoundingClientRect()
    const popover = pop && (pop.left < -0.5 || pop.right > window.innerWidth + 0.5) ? `popover spans ${Math.round(pop.left)}–${Math.round(pop.right)}` : ''
    return { missing: false as const, offenders: offenders.slice(0, 4), documentOver, mainOver, pageOffender, popover }
  })
}

test('the capacity row and the accounts popover fit every width, panel open and closed', async ({ page }) => {
  test.setTimeout(600_000)
  const errors = watchErrors(page)
  await setup(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  const row = page.locator('.cap-row'), chip = row.locator('.acc-chip')
  await expect(chip).toBeVisible()
  await expect(row.locator('.cdial [data-unit="total"]')).toBeVisible()
  if (shots) mkdirSync(shots, { recursive: true })

  const problems: string[] = []
  const sweep = async (label: string) => {
    for (const width of widths()) {
      await page.setViewportSize({ width, height: width < 800 ? 844 : 1000 })
      if (shots && [390, 610, 720, 721, 1100, 1320, 1400, 1440, 1600, 2560].includes(width)) {
        await row.screenshot({ path: `${shots}/${label}-${width}.png` })
      }
      const result = await fit(page)
      if (result.missing) problems.push(`${label} ${width}px has no capacity row`)
      else if (result.offenders?.length) problems.push(`${label} ${width}px spills ${result.offenders.map(o => `${o.desc} (+${o.over}px)`).join('; ')}`)
      if ((result.documentOver ?? 0) > 1) problems.push(`${label} ${width}px document scrolls by ${result.documentOver}px via ${result.pageOffender}`)
      if (result.mainOver == null) problems.push(`${label} ${width}px has no main`)
      else if (result.mainOver > 1) problems.push(`${label} ${width}px main scrolls by ${Math.round(result.mainOver * 10) / 10}px`)
    }
  }
  await sweep('closed')
  // The popover with the long names stays inside the window at the edges of the layouts.
  for (const width of [390, 600, 720, 1100, 1440, 2560]) {
    await page.setViewportSize({ width, height: 900 })
    await chip.click()
    await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toBeVisible()
    const result = await fit(page)
    if (result.popover) problems.push(`${width}px ${result.popover}`)
    if ((result.documentOver ?? 0) > 1) problems.push(`${width}px popover makes the document scroll by ${result.documentOver}px`)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toHaveCount(0)
  }
  await page.goto('/agents/5e000000-0000-4000-8000-000000000001')
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await sweep('panel')
  expect(problems, problems.join('\n')).toEqual([])
  expect(errors).toEqual([])
})
