// SPDX-License-Identifier: AGPL-3.0-only
// Offline UI audit. Run `npm run audit:ui` in web. It writes a deduplicated
// test-results/qa2b-findings.json and screenshots in test-results/qa2b-shots/. Keep audit output
// in this worktree so parallel workers do not overwrite one another's evidence.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { fixtures, liveAgent, me, mockView, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { crmData, HOFER, mockCRM } from './crm-fixtures'
import { Q, mockPublicQuote, mockQuotes, quoteWorld } from './quote-list-fixtures'
import { mockSettings, settingsData, makePng } from './settings-fixtures'
import { mockProfiles, profileWorld, PROFILE } from './profile-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { domAudit, expectedMockConsole, decorativeVersionContrast, installLayoutShiftAudit, armLayoutShiftAudit, readLayoutShiftAudit, type Kind, type Raw } from './ui-audit-rules'
import { mockQuoteEditor, QUOTE_ID } from './quote-inspector-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'
import { JONAS as ACCESS_JONAS, ME as ACCESS_ME, accessWorld, mockAccess } from './access-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

type Finding = Raw & { id: string; route: string; state: string; viewport: string; theme: string; screenshot: string }
type Setup = 'default' | 'editor' | 'journey' | 'public' | 'signed-out' | 'groups' | 'cards' | 'views' | 'live' | 'live-list'
type Scenario = { state: string; route: string; setup?: Setup; act?: (page: Page) => Promise<void> }

const output = resolve(process.cwd(), 'test-results/qa2b-findings.json')
const shotDir = resolve(process.cwd(), 'test-results/qa2b-shots')
const findings = new Map<string, Finding>()
const AUDIT_VIEW = '11111111-aaaa-4aaa-8aaa-0000000000a1'
const widths = process.env.AUDIT_WIDTHS ? process.env.AUDIT_WIDTHS.split(',').map(Number) : [390, 1024, 1280, 1440, 1920]
const themes = process.env.AUDIT_THEMES ? process.env.AUDIT_THEMES.split(',') as ('light' | 'dark')[] : ['light', 'dark'] as const
const quote = '/business/quotes'
const editor = `${quote}/${QUOTE_ID}`
const history = releaseHistory()
const signedOut: Scenario['act'] = async page => { await expect(page.getByLabel('Email address')).toBeVisible() }
const visible = (selector: string): Scenario['act'] => async page => { await expect(page.locator(selector).first()).toBeVisible() }
const listContent = (text: string): Scenario['act'] => async page => { await expect(page.getByText(text, { exact: false }).first()).toBeVisible() }
const inspectorTab = (name: string): Scenario['act'] => async page => {
  await visible('.quote-document')(page)
  const panel = page.getByRole('complementary', { name: 'Format' })
  if (await panel.count() === 0) await page.getByRole('button', { name: 'Format panel' }).click()
  await panel.getByRole('tab', { name }).click()
}

// Concrete URLs for every router record, including aliases, redirects and the
// catch-all. Extra states exercise controls that have no dedicated route.
const scenarios: Scenario[] = [
  { state: 'projects', route: '/', act: visible('main') },
  { state: 'project', route: '/p/PHAROS', act: visible('tr.ticket-row:not(.ghost)') },
  { state: 'ticket panel', route: '/p/PHAROS/PHAROS-11', act: visible('.ticket-ws') },
  { state: 'workspace redirect', route: '/workspace', act: visible('main') },
  { state: 'legacy project redirect', route: '/projects/p-pharos/journey/plan', setup: 'journey', act: visible('main') },
  { state: 'project journey', route: '/p/PHAROS?view=journey', setup: 'journey', act: visible('main') },
  { state: 'business overview', route: '/business', act: visible('main') },
  { state: 'customers', route: '/business/customers', act: listContent('Bäckerei Hofer') },
  { state: 'customer page', route: `/business/customers/${HOFER}`, act: visible('main') },
  { state: 'quotes', route: quote, act: listContent('Onlineshop Erweiterung Weihnachten') },
  { state: 'full quote workspace', route: editor, setup: 'editor', act: visible('.quote-document') },
  { state: 'hours', route: '/business/hours', act: visible('main') },
  { state: 'rates', route: '/business/rates', act: visible('main') },
  { state: 'costs redirect', route: '/business/costs', act: visible('main') },
  { state: 'cost units redirect', route: '/business/cost-units', act: visible('main') },
  { state: 'invalid quote redirect', route: '/business/quotes/not-a-uuid', act: visible('main') },
  { state: 'quote child redirect', route: `${editor}/old`, act: visible('main') },
  { state: 'parked crm redirect', route: '/business/crm/old', act: visible('main') },
  { state: 'crm redirect', route: '/crm', act: visible('main') },
  { state: 'agents', route: '/agents', act: visible('main') },
  { state: 'agent session', route: '/agents/5e000000-0000-4000-8000-000000000001', act: visible('main') },
  { state: 'runs redirect', route: '/runs/example', act: visible('main') },
  { state: 'approvals redirect', route: '/approvals', act: visible('main') },
  { state: 'pacing redirect', route: '/pacing', act: visible('main') },
  { state: 'release history route', route: `/releases/${history.current}`, act: async page => { await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible() } },
  { state: 'settings redirect', route: '/settings', act: visible('main') },
  { state: 'document profiles', route: '/settings/business/profiles', act: visible('main') },
  { state: 'document profile', route: `/settings/business/profiles/${PROFILE.steel}`, act: visible('main') },
  ...(['personal', 'workspace', 'business', 'projects'] as const).map(section => ({ state: `settings ${section}`, route: `/settings/${section}`, act: visible('main') })),
  { state: 'sign in', route: '/signin', setup: 'signed-out', act: signedOut },
  { state: 'public quote', route: '/offers/sel-demo/tok-example', setup: 'public', act: visible('#pq-title') },
  { state: 'not found', route: '/unknown/audit', act: visible('main') },
  { state: 'dock quote workspace', route: `${quote}?quote=${Q.draft}`, act: visible('.quote-dock') },
  { state: 'inspector Text', route: editor, setup: 'editor', act: inspectorTab('Text') },
  { state: 'inspector Section', route: editor, setup: 'editor', act: inspectorTab('Section') },
  { state: 'inspector Document', route: editor, setup: 'editor', act: inspectorTab('Document') },
  { state: 'release history sheet', route: '/', act: async page => { await visible('main')(page); await page.getByRole('button', { name: /^Release history, version / }).click(); await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible() } },
  { state: 'new quote dialog', route: quote, act: async page => { await listContent('Onlineshop Erweiterung Weihnachten')(page); await page.getByRole('button', { name: 'New quote' }).first().click(); await expect(page.getByRole('dialog', { name: 'New quote' })).toBeVisible() } },
  { state: 'crop dialog', route: '/settings/personal', act: async page => { await visible('main')(page); const chooser = page.waitForEvent('filechooser'); await page.getByRole('button', { name: /^(Add a photo|Change your photo)$/ }).click(); await (await chooser).setFiles({ name: 'audit.png', mimeType: 'image/png', buffer: makePng(240, 240) }); await expect(page.getByRole('dialog', { name: 'Crop your photo' })).toBeVisible() } },
  { state: 'link dialog', route: `${quote}/${Q.issued}`, act: async page => { await visible('.quote-document')(page); const details = page.getByRole('complementary', { name: 'Details' }); if (!await details.isVisible()) await page.getByRole('button', { name: 'Details' }).click(); await expect(details).toBeVisible(); const revoke = details.getByRole('button', { name: 'Revoke link' }); if (!await revoke.isVisible()) { const create = details.getByRole('button', { name: 'Create link' }); await expect(create).toBeVisible(); await create.click(); await expect(revoke).toBeVisible() } await revoke.click(); await expect(page.getByRole('dialog', { name: 'Revoke the customer link?' })).toBeVisible() } },
  { state: 'quote row menu', route: quote, act: async page => { await listContent('Onlineshop Erweiterung Weihnachten')(page); const row = page.locator(`#quote-${Q.issued}`); await row.hover(); await row.getByRole('button', { name: /^Actions for / }).click(); await expect(page.getByRole('menu')).toBeVisible() } },
  { state: 'account menu', route: '/', act: async page => { await visible('main')(page); await page.getByRole('button', { name: /^Account for/ }).click(); await expect(page.getByRole('menu', { name: 'Account' })).toBeVisible() } },
  { state: 'app menu', route: '/', act: async page => { await visible('main')(page); await page.getByRole('button', { name: 'App and workspace' }).click(); await expect(page.getByRole('menu', { name: 'App and workspace' })).toBeVisible() } },
  { state: 'command palette', route: '/', act: async page => { await visible('main')(page); await page.keyboard.press('Control+k'); await expect(page.getByRole('dialog', { name: 'Search and commands' })).toBeVisible() } },
  { state: 'knowledge', route: '/p/PHAROS/knowledge', act: visible('.k-row') },
  { state: 'knowledge entry', route: '/p/PHAROS/knowledge/runbook/deploy-release', act: visible('.e-body') },
  { state: 'knowledge kind redirect', route: '/p/PHAROS/knowledge/recipe/old', act: visible('.k-row') },
  { state: 'knowledge edit', route: '/p/PHAROS/knowledge/external-system/hetzner', act: async page => { await visible('.e-where')(page); await page.getByRole('button', { name: /^Edit/ }).click(); await expect(page.getByRole('form', { name: 'Edit hetzner' })).toBeVisible() } },
  { state: 'knowledge new entry dialog', route: '/p/PHAROS/knowledge', act: async page => { await visible('.k-row')(page); await page.getByRole('button', { name: 'New knowledge entry' }).click(); await expect(page.getByRole('dialog', { name: 'New knowledge entry' })).toBeVisible() } },
  { state: 'knowledge across projects', route: '/knowledge?q=deploy', act: visible('.kp-row') },
  { state: 'knowledge entry page direct', route: '/p/PHAROS/knowledge/runbook/deploy-release', act: visible('.e-body') },
  { state: 'knowledge docked entry', route: '/p/PHAROS/knowledge?entry=runbook/deploy-release', act: async page => { if ((page.viewportSize()?.width ?? 0) >= 1200) await visible('.entry-page.dock .e-body')(page); else await visible('.e-body')(page) } },
  // AEON-136: project groups, the chip row, cards, menus, the move dialog and selection.
  { state: 'projects groups', route: '/', setup: 'groups', act: visible('.group-head') },
  { state: 'projects cards', route: '/', setup: 'cards', act: visible('.card') },
  { state: 'projects live agents cards', route: '/', setup: 'live', act: visible('.card .live-chip') },
  { state: 'projects live agents list', route: '/', setup: 'live-list', act: visible('.project-item .live-chip') },
  { state: 'projects live agents details', route: '/', setup: 'live', act: async page => { await visible('.card .live-chip')(page); await page.locator('.card .live-chip').first().click(); await expect(page.getByRole('dialog', { name: /^Active agents on/ })).toBeVisible() } },
  { state: 'projects group menu', route: '/', setup: 'groups', act: async page => { await visible('.group-head')(page); await page.getByRole('button', { name: 'Actions for group Focus' }).click(); await expect(page.getByRole('menu')).toBeVisible() } },
  { state: 'projects display', route: '/', setup: 'groups', act: async page => { await visible('.group-head')(page); await page.getByRole('button', { name: /^Display/ }).click(); await expect(page.getByRole('dialog', { name: 'Display options' })).toBeVisible() } },
  { state: 'projects move dialog', route: '/', setup: 'cards', act: async page => { await visible('.card')(page); await page.locator('.card-link').first().focus(); await page.keyboard.press('m'); await expect(page.getByRole('dialog', { name: /^Move .* to a group$/ })).toBeVisible() } },
  // U22: the view bar with an own view that has changes (Save) and a shared one.
  { state: 'project saved views', route: `/p/PHAROS?priority=high,medium&v=${AUDIT_VIEW}`, setup: 'views', act: visible('.view-bar .changes .save') },
  { state: 'projects selection', route: '/', setup: 'cards', act: async page => { await visible('.card')(page); await page.locator('.card-link').first().focus(); await page.keyboard.press('x'); await expect(page.getByRole('toolbar', { name: /selected project/ })).toBeVisible() } },
  // AEON-148: Settings -> Access on the mocked authz contract.
  { state: 'access people', route: '/settings/access/people', act: visible('table.people') },
  // Opened from the person's sheet, which sits at the top at every width: the table row
  // would first scroll the page on a phone and put the list under the sticky header.
  { state: 'access role picker', route: `/settings/access/people/${ACCESS_JONAS}`, act: async page => { const sheet = page.getByRole('dialog', { name: /, access$/ }); await expect(sheet).toBeVisible(); await sheet.getByRole('button', { name: 'Change role', exact: true }).first().click(); await page.getByRole('radio', { name: /^Admin/ }).click() } },
  { state: 'access person', route: `/settings/access/people/${ACCESS_ME}`, act: async page => { await expect(page.getByRole('dialog', { name: /, access$/ })).toBeVisible() } },
  { state: 'access invite', route: '/settings/access/invites', act: async page => { await visible('.invites')(page); await page.getByRole('button', { name: 'Invite people' }).click(); await expect(page.getByRole('dialog', { name: 'Invite people' })).toBeVisible() } },
  { state: 'access roles', route: '/settings/access/roles', act: visible('.roles') },
  { state: 'access role composer', route: '/settings/access/roles/new?from=role-member', act: visible('.perm') },
  { state: 'access project', route: '/settings/access/projects/p-pharos', act: visible('.members') },
  { state: 'access agents', route: '/settings/access/agents', act: visible('.agents') },
  { state: 'access log', route: '/settings/access/audit', act: visible('.event') },
]

async function installMocks(page: Page, setup: Setup) {
  if (setup === 'signed-out') {
    await page.route('**/api/**', route => new URL(route.request().url()).pathname === '/api/me'
      ? route.fulfill({ status: 401, json: { error: 'unauthorized', dev_mode: true } })
      : route.fulfill({ status: 404, json: { error: 'not found' } }))
    return
  }
  const data = fixtures()
  if (setup === 'groups' || setup === 'cards') {
    data.preferences['project-groups'] = { groups: [{ id: 'g:focus', name: 'Focus' }, { id: 'g:later', name: 'Later' }], place: { 'p-pharos': 'g:focus' }, hidden: ['archived', 'g:later'] }
    if (setup === 'cards') data.preferences.projects = { view: 'cards' }
  }
  // AEON-184: agents at work on two projects, one of them with company.
  if (setup === 'live' || setup === 'live-list') {
    data.preferences.projects = { view: setup === 'live' ? 'cards' : 'list' }
    data.live.push(
      liveAgent({ project_id: 'p-pharos', session_id: 's-live-1', principal_id: '33333333-3333-4333-8333-333333333333', name: 'pharos-session', ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', project_id: 'p-pharos' } }, 16),
      liveAgent({ project_id: 'p-pharos', session_id: 's-live-2', principal_id: '44444444-4444-4444-8444-444444444444', name: 'camy', harness: 'codex', ticket: null }, 3),
      liveAgent({ project_id: 'p-aeon', harness: 'grok', ticket: { id: 'n-a1', key: 'AEON-1', title: 'Aeon foundation', project_id: 'p-aeon' } }, 40),
    )
  }
  if (setup === 'views') data.views.push(
    mockView({ id: AUDIT_VIEW, name: 'High priority', filters: { priority: 'high' } }),
    mockView({ id: '11111111-aaaa-4aaa-8aaa-0000000000a2', name: 'Bugs to fix', owner_principal_id: '22222222-2222-4222-8222-222222222222', shared: true }),
  )
  await mockWork(page, data)
  await mockProjectGroups(page, data, groupsWorld({ clients: setup === 'groups' || setup === 'cards' ? ['p-aeon'] : undefined }))
  if (setup === 'public') { await mockPublicQuote(page, { acceptable: true }); return }
  await mockAgents(page, agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  } }))
  await mockBusiness(page, businessData())
  await mockCRM(page, crmData())
  await mockQuotes(page, quoteWorld())
  await mockSettings(page, settingsData({ photo: true }), { photo: true, people: ['22222222-2222-4222-8222-222222222222'] })
  await mockProfiles(page, profileWorld())
  if (setup === 'journey') await mockJourney(page, journeyWorld())
  await mockKnowledge(page, knowledgeWorld())
  await mockReleases(page, history)
  if (setup === 'editor') await mockQuoteEditor(page)
  await mockAccess(page, accessWorld(), { also: mockEffectivePermissions('admin').workspace.permissions })
}


async function focusAudit(page: Page): Promise<Raw[]> {
  const result: Raw[] = [], seen = new Set<string>()
  await page.evaluate(() => {
    const baseline = new WeakMap<Element, string>()
    for (const el of document.querySelectorAll('button, a[href], input, select, textarea, [tabindex], [contenteditable]')) {
      if (el === document.activeElement) continue
      const cs = getComputedStyle(el)
      baseline.set(el, `${cs.outlineStyle}|${cs.outlineWidth}|${cs.outlineColor}|${cs.boxShadow}`)
    }
    ;(window as unknown as { auditFocusBaseline: WeakMap<Element, string> }).auditFocusBaseline = baseline
  })
  for (let i = 0; i < 180; i++) {
    await page.keyboard.press('Tab')
    const item = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      if (!el || el === document.body) return null
      const cs = getComputedStyle(el), rect = el.getBoundingClientRect()
      const parts: string[] = []
      for (let node: Element | null = el; node && node !== document.body; node = node.parentElement) {
        if (node.id) { parts.unshift(`#${CSS.escape(node.id)}`); break }
        const index = node.parentElement ? [...node.parentElement.children].indexOf(node) + 1 : 1
        parts.unshift(`${node.tagName.toLowerCase()}:nth-child(${index})`)
      }
      const path = parts.join(' > ')
      const now = `${cs.outlineStyle}|${cs.outlineWidth}|${cs.outlineColor}|${cs.boxShadow}`
      const before = (window as unknown as { auditFocusBaseline: WeakMap<Element, string> }).auditFocusBaseline.get(el)
      return { path, visible: rect.width > 0 && rect.height > 0, changed: before === undefined || before !== now }
    })
    if (!item) break
    if (seen.has(item.path)) break
    seen.add(item.path)
    if (item.visible && !item.changed)
      result.push({ kind: 'invisible-focus', severity: 'serious', selector: item.path, detail: 'Tab focus did not change outline or box shadow' })
    if (result.length >= 30) break
  }
  return result
}

// One test per screen so CI shards and workers run the audit in parallel. The
// serial loop was about 26 minutes on one runner. UI_AUDIT_AGGREGATE=1 (the
// local npm run audit:ui, workers=1) still writes the combined findings file.
const aggregate = process.env.UI_AUDIT_AGGREGATE === '1'
const save = () => { if (aggregate) writeFileSync(output, `${JSON.stringify([...findings.values()], null, 2)}\n`) }
const selected = scenarios.filter(s => !process.env.AUDIT_FILTER || s.state.includes(process.env.AUDIT_FILTER))
if (!selected.length) {
  test('audit filter matched nothing', () => { throw new Error('AUDIT_FILTER matched no states') })
}
if (aggregate) { mkdirSync(shotDir, { recursive: true }); save() }

for (const scenario of selected) {
  test(`offline route and state audit: ${scenario.state}`, async ({ browser }) => {
    test.setTimeout(180_000)
    const unreachable: string[] = []
    for (const theme of themes) for (const width of widths) {
      const context = await browser.newContext({ viewport: { width, height: 900 }, colorScheme: theme, reducedMotion: 'reduce' })
      const page = await context.newPage()
      page.setDefaultTimeout(7000)
      const errors: Raw[] = []
      page.on('console', message => {
        const detail = `${message.text()} ${message.location().url}`.slice(0, 300)
        if (message.type() === 'error' && !expectedMockConsole(scenario.state, detail)) errors.push({ kind: 'console', severity: 'serious', selector: 'window.console', detail })
      })
      page.on('pageerror', error => errors.push({ kind: 'unhandled-rejection', severity: 'serious', selector: 'window', detail: error.message.slice(0, 300) }))
      await page.addInitScript(installLayoutShiftAudit)
      await page.addInitScript(() => {
        window.addEventListener('unhandledrejection', event => { (window as unknown as { auditRejections: string[] }).auditRejections ??= []; (window as unknown as { auditRejections: string[] }).auditRejections.push(String(event.reason)) })
      })
      let raw: Raw[] = []
      try {
        await installMocks(page, scenario.setup ?? 'default')
        await page.goto(scenario.route)
        // Initial hydration is part of navigation, not a late content shift.
        // Start measuring when the document has loaded and route rendering begins.
        await page.evaluate(armLayoutShiftAudit)
        if (scenario.act) await scenario.act(page)
        await page.waitForTimeout(200)
        raw.push(...await page.evaluate(domAudit))
        const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
        for (const v of axe.violations.filter(v => v.impact === 'serious' || v.impact === 'critical')) for (const node of v.nodes.slice(0, 10)) {
          const target = node.target.join(' ')
          const labelledVersion = v.id === 'color-contrast' && await page.locator(target).evaluate(el => !!el.closest('.calendar-version[aria-label], .version-coordinate[aria-label]')).catch(() => false)
          if (decorativeVersionContrast(target, v.id, labelledVersion)) continue
          raw.push({ kind: 'axe', severity: v.impact as Raw['severity'], selector: target, detail: `${v.id}: ${v.help}; ${node.failureSummary?.slice(0, 250) ?? ''}` })
        }
        const shift = await page.evaluate(readLayoutShiftAudit)
        if (shift.score > 0.02) raw.push({ kind: 'layout-shift', severity: 'moderate', selector: 'document', detail: `CLS after load: ${shift.score.toFixed(4)}; ${shift.sources.slice(0, 5).join('; ')}` })
        const rejections = await page.evaluate(() => (window as unknown as { auditRejections?: string[] }).auditRejections ?? [])
        for (const rejection of rejections) raw.push({ kind: 'unhandled-rejection', severity: 'serious', selector: 'window', detail: rejection.slice(0, 300) })
        // Tabbing is a deliberate interaction and can scroll long pages. Measure
        // load stability first so focus exploration cannot inflate CLS.
        raw.push(...await focusAudit(page))
      } catch (error) { raw.push({ kind: 'scenario-error', severity: 'serious', selector: 'document', detail: String(error).slice(0, 500) }) }
      raw.push(...errors)
      for (const row of raw) if (row.kind === 'scenario-error') unreachable.push(`${width} ${theme}: ${row.detail}`)
      if (aggregate) {
        const newRows = raw.filter(row => !findings.has(`${row.kind}|${scenario.route}|${scenario.state}|${theme}|${row.selector}|${row.detail.replace(/\d+(?:\.\d+)?/g, '#')}`))
        let shot = ''
        if (newRows.length) {
          shot = join(shotDir, `${scenario.state.replace(/[^a-z0-9]+/gi, '-').toLowerCase()}-${width}-${theme}.png`)
          try { await page.screenshot({ path: shot, fullPage: true, animations: 'disabled' }) } catch { shot = '' }
        }
        for (const row of raw) {
          const key = `${row.kind}|${scenario.route}|${scenario.state}|${theme}|${row.selector}|${row.detail.replace(/\d+(?:\.\d+)?/g, '#')}`
          const old = findings.get(key)
          if (old) { if (!old.viewport.split(',').includes(String(width))) old.viewport += `,${width}`; continue }
          findings.set(key, { ...row, id: `${row.kind}-${createHash('sha1').update(key).digest('hex').slice(0, 10)}`, route: scenario.route, state: scenario.state, viewport: String(width), theme, screenshot: shot })
        }
        save()
      }
      console.log(`audited ${scenario.state} ${width} ${theme}: ${raw.length} observations`)
      await context.close()
    }
    expect(unreachable, 'every audit state must be reachable').toEqual([])
  })
}
