// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { domAudit, installLayoutShiftAudit, armLayoutShiftAudit, readLayoutShiftAudit, expectedMockConsole, decorativeVersionContrast } from './ui-audit-rules'
import { fixtures, mockWork } from './work-fixtures'

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

test('known fixture responses and decorative labelled versions are filtered precisely', () => {
  expect(expectedMockConsole('sign in', 'Failed to load resource: the server responded with a status of 401 (Unauthorized) http://127.0.0.1:5175/api/me')).toBe(true)
  expect(expectedMockConsole('projects', 'Failed to load resource: the server responded with a status of 404 (Not Found) http://127.0.0.1:5175/api/me')).toBe(false)
  expect(expectedMockConsole('agents', 'Uncaught TypeError http://127.0.0.1:5175/api/models')).toBe(false)
  expect(decorativeVersionContrast('.yy[aria-hidden="true"]', 'color-contrast', true)).toBe(true)
  expect(decorativeVersionContrast('.yy[aria-hidden="true"]', 'color-contrast', false)).toBe(false)
  expect(decorativeVersionContrast('.yy[aria-hidden="true"]', 'aria-required-attr', true)).toBe(false)
})

test('layout shift audit arms after navigation and resets startup movement', async ({ page }) => {
  await page.addInitScript(installLayoutShiftAudit)
  await page.goto('/')
  await page.evaluate(() => { (window as unknown as { auditShift: number }).auditShift = 0.5 })
  await page.evaluate(armLayoutShiftAudit)
  expect(await page.evaluate(() => (window as unknown as { auditShift: number; auditShiftArmed: boolean }).auditShift)).toBe(0)
  expect(await page.evaluate(() => (window as unknown as { auditShiftArmed: boolean }).auditShiftArmed)).toBe(true)
  expect(await page.evaluate(readLayoutShiftAudit)).toEqual({ score: 0, sources: [] })
  expect(await page.evaluate(() => (window as unknown as { auditShiftArmed: boolean }).auditShiftArmed)).toBe(false)
})
