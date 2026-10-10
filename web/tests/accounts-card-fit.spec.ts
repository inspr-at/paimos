// SPDX-License-Identifier: AGPL-3.0-only
// AEON-355, kept for the status line and grid of AEON-782: Accounts and
// computers stays inside itself from 390 to 2560, with the session panel open
// and closed. Long names, one unbroken account name, a computer with six
// accounts and one with one, including an account with no reading yet. Names
// stay on one line. The section, main and the document have no horizontal
// overflow. Sprint and Hold badges moved to Settings with the account menu.
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
    const card = document.querySelector('.acc-section')
    if (!card) return { missing: true as const, documentOver, mainOver, offenders: [], pageOffender: '' }
    const edge = card.getBoundingClientRect().right
    const describe = (el: Element) => {
      const cls = (el.getAttribute('class') || '').split(/\s+/).filter(Boolean).slice(0, 3).join('.')
      const text = [...el.childNodes].every(node => node.nodeType === 3) ? (el.textContent || '').trim().slice(0, 48) : ''
      return `${el.tagName.toLowerCase()}${cls ? '.' + cls : ''}${text ? ` "${text}"` : ''}`
    }
    const offenders: { over: number; desc: string }[] = []
    for (const el of card.querySelectorAll('*')) {
      const box = el.getBoundingClientRect()
      if (box.width < 1 && box.height < 1) continue
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
    // A flex item's own box is one rect even when its text wraps, so the line
    // count comes from the text range.
    const names = [...card.querySelectorAll('.mx-name')].map(el => {
      const range = document.createRange()
      range.selectNodeContents(el)
      const rects = [...range.getClientRects()].filter(r => r.width > 0.5 && r.height > 0.5)
      const lines = new Set(rects.map(r => Math.round(r.top))).size
      return { name: (el.textContent || '').trim(), lines }
    })
    return { missing: false as const, offenders: offenders.slice(0, 4), documentOver, mainOver, pageOffender, card: Math.round(edge), names }
  })
}

test('accounts and computers fit every width, panel open and closed', async ({ page }) => {
  test.setTimeout(600_000)
  const errors = watchErrors(page)
  await setup(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  const card = page.getByRole('region', { name: 'Accounts and computers' })
  await expect(card.getByRole('table')).toBeVisible()
  // Computers and accounts are columns and rows of one grid: a computer with six accounts and one with one.
  await expect(card.getByRole('columnheader')).toHaveCount(3)
  await expect(card.getByRole('row')).toHaveCount(1 + (await card.getByRole('rowheader').count()))
  await expect(card.getByRole('rowheader').first().getByRole('link')).toBeVisible()
  if (shots) mkdirSync(shots, { recursive: true })

  const problems: string[] = []
  const sweep = async (label: string) => {
    for (const width of widths()) {
      await page.setViewportSize({ width, height: width < 800 ? 844 : 1000 })
      if (shots && [390, 610, 720, 721, 1100, 1320, 1400, 1440, 1600, 2560].includes(width)) {
        await card.screenshot({ path: `${shots}/${label}-${width}.png` })
      }
      const result = await fit(page)
      if (result.missing) problems.push(`${label} ${width}px has no accounts card`)
      else if (result.offenders?.length) problems.push(`${label} ${width}px spills ${result.offenders.map(o => `${o.desc} (+${o.over}px)`).join('; ')}`)
      if ((result.documentOver ?? 0) > 1) problems.push(`${label} ${width}px document scrolls by ${result.documentOver}px via ${result.pageOffender}`)
      if (result.mainOver == null) problems.push(`${label} ${width}px has no main`)
      else if (result.mainOver > 1) problems.push(`${label} ${width}px main scrolls by ${Math.round(result.mainOver * 10) / 10}px`)
      if (!result.missing) {
        for (const n of result.names) {
          if (n.lines !== 1) problems.push(`${label} ${width}px ${n.name} is ${n.lines} lines`)
        }
      }
    }
  }
  await sweep('closed')
  await page.goto('/agents/5e000000-0000-4000-8000-000000000001')
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await sweep('panel')
  expect(problems, problems.join('\n')).toEqual([])
  expect(errors).toEqual([])
})
