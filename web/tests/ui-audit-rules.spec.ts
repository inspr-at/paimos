// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { domAudit, installLayoutShiftAudit, armLayoutShiftAudit, readLayoutShiftAudit, expectedMockConsole, decorativeVersionContrast } from './ui-audit-rules'

test.use({ viewport: { width: 390, height: 700 } })

test('touch rule uses the actual label and pseudo-element hit area', async ({ page }) => {
  await page.setContent(`<style>
    body { margin: 0; padding: 50px; }
    label { display: flex; align-items: center; width: 140px; height: 44px; }
    input { width: 34px; height: 20px; }
    button { display: block; position: relative; width: 28px; height: 28px; margin: 40px 0; }
    #expanded::before { content: ''; position: absolute; top: 50%; left: 50%; width: 44px; height: 44px; transform: translate(-50%, -50%); }
  </style><label><input id="labelled" type="checkbox">Enabled</label><button id="expanded">E</button><button id="small">S</button>`)
  const small = (await page.evaluate(domAudit)).filter(f => f.kind === 'small-touch-target').map(f => f.selector)
  expect(small).toContain('#small')
  expect(small).not.toContain('#labelled')
  expect(small).not.toContain('#expanded')
})

test('touch rule checks only the undersized axis of a wide control', async ({ page }) => {
  await page.setContent(`<style>
    body { margin: 0; padding: 50px; }
    button { position: relative; width: 89px; height: 28px; }
    #wide::before { content: ''; position: absolute; top: 50%; left: 50%; width: 100%; height: 44px; transform: translate(-50%, -50%); }
    #edge { position: absolute; top: 50px; left: 116px; width: 40px; height: 28px; }
  </style><button id="wide">Wide</button><button id="edge">Edge</button>`)
  const small = (await page.evaluate(domAudit)).filter(f => f.kind === 'small-touch-target').map(f => f.selector)
  expect(small).not.toContain('#wide')
  expect(small).toContain('#edge')
})

test('overlap rule ignores a card action and modal background but keeps peer collisions', async ({ page }) => {
  await page.setContent(`<style>body { margin: 0; padding: 50px; } .card { position: relative; width: 180px; height: 70px; } .card-link { display: block; width: 180px; height: 70px; } .action { position: absolute; right: 0; top: 10px; width: 40px; height: 40px; } .peers { position: relative; margin-top: 30px; height: 80px; } .peers button { position: absolute; width: 60px; height: 50px; } #second { left: 45px; } dialog { position: fixed; top: 300px; } </style><div class="card"><a class="card-link" href="#">Card</a><button class="action">Menu</button></div><div class="peers"><button id="first">First</button><button id="second">Second</button></div>`)
  const overlaps = (await page.evaluate(domAudit)).filter(f => f.kind === 'interactive-overlap')
  expect(overlaps).toHaveLength(1)
  expect(overlaps[0]?.detail).toContain('#second')
  await page.evaluate(() => { const menu = document.createElement('div'); menu.className = 'floating pop'; menu.innerHTML = '<button id="menu">Menu</button>'; Object.assign(menu.style, { position: 'absolute', top: '175px', left: '80px' }); document.body.append(menu) })
  expect((await page.evaluate(domAudit)).filter(f => f.kind === 'interactive-overlap' && f.detail.includes('#menu'))).toEqual([])
  await page.evaluate(() => { const dialog = document.createElement('dialog'); dialog.innerHTML = '<button id="inside">Inside</button>'; document.body.append(dialog); dialog.showModal() })
  expect((await page.evaluate(domAudit)).filter(f => f.kind === 'interactive-overlap')).toEqual([])
  await page.setContent(`<style>body { margin: 0; padding: 50px; } .row { position: relative; width: 200px; height: 70px; } .item-link { display: block; width: 200px; height: 70px; } .row-more { position: absolute; right: 0; top: 10px; width: 44px; height: 44px; } #name { display: inline-block; width: 120px; }</style><div class="row"><a id="row-link" class="item-link" href="#"><span id="name">Project</span></a><button id="row-action" class="row-more">Menu</button></div>`)
  expect((await page.evaluate(domAudit)).filter(f => f.kind === 'interactive-overlap')).toEqual([])
  await page.locator('#name').evaluate(el => { Object.assign((el as HTMLElement).style, { position: 'absolute', left: '158px', top: '12px' }) })
  const covered = (await page.evaluate(domAudit)).filter(f => f.kind === 'interactive-overlap')
  expect(covered).toHaveLength(1)
  expect(covered[0]?.selector).toBe('#row-link'); expect(covered[0]?.detail).toContain('#row-action')
})

test('touch rule lets an open menu cover controls and exempts dates in the paper’s text', async ({ page }) => {
  await page.setContent(`<style>
    body { margin: 0; padding: 50px; }
    button { position: relative; width: 80px; height: 28px; }
    button::before { content: ''; position: absolute; top: 50%; left: 50%; width: 100%; height: 44px; transform: translate(-50%, -50%); }
    .floating.pop { position: absolute; top: 80px; left: 40px; width: 200px; height: 80px; background: white; }
    .quote-document { margin-top: 160px; font-size: 7px; line-height: 9px; }
    .quote-document button { width: 26px; height: 7px; padding: 0; }
    .quote-document button::before { content: none; }
  </style><button id="facet">Status</button><div class="floating pop"><button id="item">Open</button></div>
  <div class="quote-document"><p>Datum <span class="date-picker as-paper"><button id="paper-date">24.09.</button></span></p><p><button id="paper-other">Other</button></p></div>`)
  const small = (await page.evaluate(domAudit)).filter(f => f.kind === 'small-touch-target').map(f => f.selector)
  expect(small).not.toContain('#facet')
  expect(small).not.toContain('#paper-date')
  expect(small).toContain('#paper-other')
})

test('a sideways strip is assessed as it stands; a control under a vertical scroller’s edge waits', async ({ page }) => {
  await page.setContent(`<style>
    body { margin: 0; padding: 40px; }
    .strip { display: flex; width: 300px; overflow-x: auto; }
    .strip a { flex-shrink: 0; display: block; width: 100px; height: 32px; }
    .below { height: 40px; background: #eee; }
    .scroller { margin-top: 20px; height: 120px; overflow-y: auto; }
    .scroller .fill { height: 100px; }
    .scroller button { display: block; width: 80px; height: 32px; }
  </style><div class="strip"><a id="tab" href="#">Tab</a></div><div class="below"></div>
  <div class="scroller"><div class="fill"></div><button id="edge">Edge</button><div class="fill"></div></div>`)
  const small = (await page.evaluate(domAudit)).filter(f => f.kind === 'small-touch-target').map(f => f.selector)
  expect(small).toContain('#tab')
  expect(small).not.toContain('#edge')
})

test('partially visible controls wait until they can be assessed after scrolling', async ({ page }) => {
  await page.setContent('<style>body { margin: 0; } button { position: absolute; top: 680px; left: 50px; width: 32px; height: 32px; }</style><button id="edge">Edge</button>')
  expect((await page.evaluate(domAudit)).filter(f => f.kind === 'small-touch-target')).toEqual([])
})

test('a top-layer modal escapes its page clipping while its own scroller still rejects oversized children', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.setContent('<style>body{margin:0}main{width:300px;overflow:hidden}dialog{width:800px;max-width:none}#body{width:600px;overflow:auto}#content{width:400px;height:60px}</style><main><dialog><div id="body"><div id="content">A deliberately bounded modal body</div></div></dialog></main>')
  await page.locator('dialog').evaluate(el => (el as HTMLDialogElement).showModal())
  expect((await page.evaluate(domAudit)).filter(row => row.kind === 'horizontal-overflow')).toEqual([])
  await page.locator('#content').evaluate(el => { el.style.width = '900px' })
  const errors = (await page.evaluate(domAudit)).filter(row => row.kind === 'horizontal-overflow')
  expect(errors).toHaveLength(1)
  expect(errors[0]!.selector).toBe('#content')
})

test('known fixture responses and decorative labelled versions are filtered precisely', () => {
  expect(expectedMockConsole('sign in', 'Failed to load resource: the server responded with a status of 401 (Unauthorized) http://127.0.0.1:5175/api/me')).toBe(true)
  expect(expectedMockConsole('projects', 'Failed to load resource: the server responded with a status of 404 (Not Found) http://127.0.0.1:5175/api/me')).toBe(false)
  expect(expectedMockConsole('agents', 'Uncaught TypeError http://127.0.0.1:5175/api/models')).toBe(false)
  expect(decorativeVersionContrast('.yy[aria-hidden="true"]', 'color-contrast', true)).toBe(true)
  expect(decorativeVersionContrast('.yy[aria-hidden="true"]', 'color-contrast', false)).toBe(false)
  expect(decorativeVersionContrast('.yy[aria-hidden="true"]', 'aria-required-attr', true)).toBe(false)
})

// Wait for the browser to report this particular movement, rather than guessing
// how many milliseconds layout and PerformanceObserver delivery will take.
async function moveAuditControl(page: Page, top: number): Promise<number> {
  return page.evaluate(top => new Promise<number>(resolve => {
    const control = document.querySelector<HTMLElement>('.audit-control')!
    const observer = new PerformanceObserver(list => {
      const entries = (list.getEntries() as (PerformanceEntry & { value: number; hadRecentInput: boolean; sources?: { node?: Node }[] })[])
        .filter(entry => !entry.hadRecentInput && entry.sources?.some(source => source.node === control))
      if (!entries.length) return
      observer.disconnect()
      resolve(entries.reduce((score, entry) => score + entry.value, 0))
    })
    observer.observe({ type: 'layout-shift' })
    control.style.top = `${top}px`
  }), top)
}

test('layout shift audit arms after navigation and resets startup movement', async ({ page }) => {
  // A live app's load event and fonts.ready do not await API-driven rendering.
  // This rule test owns its document; route/state audits cover app rendering.
  const apiRequests: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (path.startsWith('/api/') && apiRequests.length < 10) apiRequests.push(path)
  })
  await page.route('**/audit-layout-shift', route => route.fulfill({
    contentType: 'text/html',
    body: '<!doctype html><style>body { margin: 0; padding: 40px; } .audit-control { position: relative; top: 0; width: 280px; height: 44px; }</style><button class="audit-control">Audit control</button>',
  }))
  await page.addInitScript(installLayoutShiftAudit)
  for (let navigation = 0; navigation < 20; navigation++) {
    await page.goto('/audit-layout-shift')
    await page.evaluate(async () => {
      await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    })
    expect(await moveAuditControl(page, 10), 'the fixture must produce real startup movement').toBeGreaterThan(0)
    await page.evaluate(() => {
      const audit = window as unknown as { auditShift: number; auditShiftSources: string[] }
      audit.auditShift = 0.5
      audit.auditShiftSources = ['previous measurement']
    })
    await page.evaluate(armLayoutShiftAudit)
    const armed = await page.evaluate(() => {
      const audit = window as unknown as { auditShift: number; auditShiftArmed: boolean; auditShiftSources: string[] }
      return { score: audit.auditShift, armed: audit.auditShiftArmed, sources: audit.auditShiftSources }
    })
    expect(armed.sources, armed.sources.join(' | ')).toEqual([])
    expect(armed.score).toBe(0)
    expect(armed.armed).toBe(true)
    expect(await page.evaluate(readLayoutShiftAudit)).toEqual({ score: 0, sources: [] })
    expect(await page.evaluate(() => (window as unknown as { auditShiftArmed: boolean }).auditShiftArmed)).toBe(false)
  }
  // Chromium ignores movement below three CSS pixels. Use four pixels so the
  // observer receives a real entry while the CLS score still stays below 0.001.
  await page.evaluate(armLayoutShiftAudit)
  const score = await moveAuditControl(page, 14)
  const shifted = await page.evaluate(readLayoutShiftAudit)
  expect(score).toBeGreaterThan(0)
  expect(score).toBeLessThan(0.001)
  expect(shifted.score, shifted.sources.join(' | ')).toBe(score)
  expect(shifted.sources).toHaveLength(1)
  expect(shifted.sources[0]).toContain('audit-control')
  expect(apiRequests, 'the audit rule test must not depend on a running backend').toEqual([])
})

test('layout shift audit filters delayed entries by start time without rounding away small shifts', async ({ page }) => {
  type Entry = { startTime: number; value: number; hadRecentInput: boolean }
  type Audit = { auditShift: number; auditShiftArmed: boolean; auditShiftSources: string[]; auditShiftSince: number; deliver: (entries: Entry[]) => void; observed: PerformanceObserverInit }
  await page.evaluate(() => {
    const audit = window as unknown as Audit
    // Delivery is an explicit barrier: startup entries arrive only after arm.
    window.PerformanceObserver = class {
      constructor(callback: (list: { getEntries: () => Entry[] }) => void) {
        audit.deliver = entries => callback({ getEntries: () => entries })
      }
      observe(options: PerformanceObserverInit) { audit.observed = options }
    } as unknown as typeof PerformanceObserver
  })
  await page.evaluate(installLayoutShiftAudit)
  expect(await page.evaluate(() => (window as unknown as Audit).observed)).toEqual({ type: 'layout-shift', buffered: true })
  await page.evaluate(armLayoutShiftAudit)
  await page.evaluate(() => {
    const audit = window as unknown as Audit
    audit.deliver([
      { startTime: audit.auditShiftSince - 1, value: 0.00029219, hadRecentInput: false },
      { startTime: audit.auditShiftSince + 1, value: 0.5, hadRecentInput: true },
    ])
  })
  expect(await page.evaluate(() => {
    const audit = window as unknown as Audit
    return { score: audit.auditShift, sources: audit.auditShiftSources }
  })).toEqual({ score: 0, sources: [] })
  await page.evaluate(() => {
    const audit = window as unknown as Audit
    audit.deliver([{ startTime: audit.auditShiftSince, value: 0.00029219, hadRecentInput: false }])
  })
  const shifted = await page.evaluate(readLayoutShiftAudit)
  expect(shifted.score).toBe(0.00029219)
  expect(shifted.sources).toHaveLength(1)
  await page.evaluate(() => {
    const audit = window as unknown as Audit
    audit.deliver([{ startTime: audit.auditShiftSince + 1, value: 0.5, hadRecentInput: false }])
  })
  expect(await page.evaluate(readLayoutShiftAudit)).toEqual(shifted)
})
