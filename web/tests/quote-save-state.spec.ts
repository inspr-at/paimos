// SPDX-License-Identifier: AGPL-3.0-only
// QA2 (AEON-120): the quote's save state is a status, and Save is its own action.
// A refused save names what to fix (with the way to it) and offers no retry that
// cannot land; a passing failure can be tried again. Unsaved work from an earlier
// visit is only restored onto a draft of the same version: on an issued version it
// starts the next version or is discarded, and a copy from another version's draft
// or one without changes is never offered.
import { expect, test, type Page } from '@playwright/test'
import { crmData, mockCRM } from './crm-fixtures'
import { Q, mockQuotes, quoteWorld, type QuoteWorld } from './quote-list-fixtures'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

const TAB = 'qa2-tab-session'
async function setup(page: Page) {
  await mockWork(page, fixtures())
  await mockCRM(page, crmData())
  const world = quoteWorld()
  const calls = await mockQuotes(page, world)
  return { world, calls }
}
const saves = (calls: Awaited<ReturnType<typeof setup>>['calls']) => calls.filter(c => c.method === 'PATCH' && c.path.endsWith('/draft'))
async function full(page: Page, id: string) {
  await page.goto(`/business/quotes/${id}`)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await expect(page.locator('.quote-ws')).toHaveAttribute('data-recovery-ready', 'true')
}
const state = (page: Page) => page.locator('.quote-titlebar .save-state')
const saveButton = (page: Page) => page.getByRole('button', { name: 'Save draft' })

// Pause the recovery database open at its success callback. No transaction is
// left running, and the real IndexedDB read resumes only when this test says so.
async function fullWithDeferredRecovery(page: Page, id: string) {
  await page.addInitScript(() => {
    if (!/^\/business\/quotes\/[^/]+$/.test(location.pathname) || 'recoveryGate' in window) return
    const original = indexedDB.open.bind(indexedDB)
    const gate = { started: false, release: () => {} }
    ;(window as unknown as { recoveryGate: typeof gate }).recoveryGate = gate
    const resume = new Promise<void>(resolve => { gate.release = resolve })
    indexedDB.open = (name, version) => {
      const request = original(name, version)
      if (name !== 'aeon-quote-recovery-v1') return request
      Object.defineProperty(request, 'onsuccess', {
        set(callback: ((this: IDBRequest, event: Event) => void) | null) {
          request.addEventListener('success', event => {
            gate.started = true
            void resume.then(() => callback?.call(request, event))
          }, { once: true })
        },
      })
      return request
    }
  })
  await page.goto(`/business/quotes/${id}`)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await expect.poll(() => page.evaluate(() => (window as unknown as { recoveryGate: { started: boolean } }).recoveryGate.started)).toBe(true)
  await expect(page.locator('.quote-ws')).toHaveAttribute('data-recovery-ready', 'false')
  await page.evaluate(() => (window as unknown as { recoveryGate: { release(): void } }).recoveryGate.release())
  await expect(page.locator('.quote-ws')).toHaveAttribute('data-recovery-ready', 'true')
}

// Leaves an unsaved copy in this tab's recovery store, as an earlier visit would have.
async function seedRecovery(page: Page, world: QuoteWorld, quoteId: string, options: { baseVersion?: number; title?: string }) {
  await page.goto('/business/quotes')
  await expect(page.getByRole('grid', { name: 'Quotes' })).toBeVisible()
  const base = structuredClone(world.drafts.get(quoteId)!.document)
  const mine = { ...structuredClone(base), title: options.title ?? base.title }
  await page.evaluate(async ({ quoteId, tenant, principal, tab, base, mine, baseRevision, baseVersion }) => {
    sessionStorage.setItem(`aeon-quote-tab:${tenant}:${principal}:${quoteId}`, tab)
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const req = indexedDB.open('aeon-quote-recovery-v1', 1)
      req.onupgradeneeded = () => { if (!req.result.objectStoreNames.contains('drafts')) req.result.createObjectStore('drafts') }
      req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error)
    })
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction('drafts', 'readwrite')
      tx.objectStore('drafts').put({ tenantId: tenant, principalId: principal, quoteId, sessionId: tab, baseRevision, baseVersion, base, mine, savedAt: Date.now(), schemaVersion: 1 }, [tenant, principal, quoteId, tab].join(':'))
      tx.oncomplete = () => resolve(); tx.onerror = () => reject(tx.error)
    })
    db.close()
  }, { quoteId, tenant: 't1', principal: me.id, tab: TAB, base, mine, baseRevision: world.drafts.get(quoteId)!.revision, baseVersion: options.baseVersion })
}

test('the save state is a status beside a separate Save that is ready only when there is something to save', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const { calls } = await setup(page)
  await full(page, Q.draft)
  await expect(state(page)).toHaveText('Saved')
  await expect(state(page)).toHaveAttribute('role', 'status')
  await expect(saveButton(page)).toBeDisabled()
  // The status is never a button: no control reads "Saved" or "Not saved".
  await expect(page.getByRole('button', { name: /^(Saved|Not saved|Unsaved changes)$/ })).toHaveCount(0)
  const title = page.getByRole('textbox', { name: 'Angebotstitel' })
  await title.click()
  await page.keyboard.press('End')
  await page.keyboard.type(' 2027')
  await expect(state(page)).toHaveText('Unsaved changes')
  await expect.poll(() => saves(calls).length).toBe(1)
  await expect(state(page)).toHaveText('Saved')
  await expect(saveButton(page)).toBeDisabled()
  expect(errors).toEqual([])
})

test('a refused save names what to fix and where, offers no retry, and saves once it is fixed', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { world, calls } = await setup(page)
  const second = world.drafts.get(Q.draft)!.document.sections[1]!
  let refused = ''
  await page.route(`**/api/quotes/${Q.draft}/draft`, async route => {
    if (route.request().method() === 'PATCH' && !refused) {
      refused = (route.request().postDataJSON() as { mutation_id: string }).mutation_id
      return route.fulfill({ status: 400, json: { error: 'invalid document', fields: [{ path: 'sections[1].heading', message: 'must not be empty' }] } })
    }
    return route.fallback()
  })
  await full(page, Q.draft)
  const title = page.getByRole('textbox', { name: 'Angebotstitel' })
  await title.click()
  await page.keyboard.press('End')
  await page.keyboard.type(' A')
  const notice = page.locator('.save-failure')
  await expect(notice).toBeVisible()
  await expect(state(page)).toHaveText('Not saved')
  await expect(notice).toContainText('One part of this draft needs a fix before it can be saved.')
  await expect(notice.getByRole('list', { name: 'What to fix' })).toContainText(`Section 2 “${second.heading}”: heading`)
  await expect(notice).toContainText('Must not be empty.')
  // No blind retry: the same document would be refused again.
  await expect(notice.getByRole('button', { name: 'Try again' })).toHaveCount(0)
  await expect(saveButton(page)).toBeDisabled()
  await notice.getByRole('button', { name: /^Show Section 2/ }).click()
  await expect.poll(() => page.evaluate(id => !!document.activeElement?.closest(`[data-section-id="${id}"]`), second.id)).toBe(true)
  // Editing again (the fix) saves by itself, as a new mutation.
  await title.click()
  await page.keyboard.press('End')
  await page.keyboard.type('B')
  await expect(state(page)).toHaveText('Saved')
  await expect(notice).toHaveCount(0)
  const sent = saves(calls)
  expect(sent).toHaveLength(1)
  expect(sent[0]!.body).toMatchObject({ document: { title: 'Relaunch des Kundenportals AB' } })
  expect((sent[0]!.body as { mutation_id: string }).mutation_id).not.toBe(refused)
})

test('a passing server failure can be tried again, and the retry lands', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls } = await setup(page)
  let fail = true
  await page.route(`**/api/quotes/${Q.draft}/draft`, async route => {
    if (route.request().method() === 'PATCH' && fail) { fail = false; return route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } }) }
    return route.fallback()
  })
  await full(page, Q.draft)
  await page.getByRole('textbox', { name: 'Angebotstitel' }).fill('Retry evidence')
  const notice = page.locator('.save-failure')
  await expect(notice).toContainText('The server could not save it just now.')
  await expect(saveButton(page)).toBeEnabled()
  await notice.getByRole('button', { name: 'Try again' }).click()
  await expect(state(page)).toHaveText('Saved')
  await expect(notice).toHaveCount(0)
  expect(saves(calls).at(-1)?.body).toMatchObject({ document: { title: 'Retry evidence' } })
})

test('the notices sit with the same space above and below them', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await page.route(`**/api/quotes/${Q.draft}/draft`, async route => route.request().method() === 'PATCH' ? route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } }) : route.fallback())
  await full(page, Q.draft)
  await page.getByRole('textbox', { name: 'Angebotstitel' }).fill('Spacing evidence')
  await expect(page.locator('.save-failure')).toBeVisible()
  const gaps = await page.evaluate(() => {
    const box = document.querySelector('.quote-notices')!.getBoundingClientRect()
    const notice = document.querySelector('.save-failure')!.getBoundingClientRect()
    return { above: Math.round(notice.top - box.top), below: Math.round(box.bottom - notice.bottom) }
  })
  expect(gaps.below).toBe(gaps.above)
})

test('an issued version never offers to restore work onto itself: it revises into the next version with it, or discards it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const { world, calls } = await setup(page)
  await seedRecovery(page, world, Q.issued, { baseVersion: 0, title: 'Filial-Tablets, Kassen und Schulung' })
  await full(page, Q.issued)
  const notice = page.locator('.quote-notices .notice').filter({ hasText: 'You have unsaved work from before version 1 was issued.' })
  await expect(notice).toBeVisible()
  await expect(page.getByRole('button', { name: 'Restore my work' })).toHaveCount(0)
  await expect(notice.getByRole('button', { name: 'Discard' })).toBeVisible()
  await notice.getByRole('button', { name: 'Revise as version 2 with my work' }).click()
  await page.getByRole('dialog', { name: 'Revise as version 2?' }).getByRole('button', { name: 'Revise' }).click()
  // The new draft carries the work and saves it; the issued version stays as it was.
  await expect(page.getByRole('textbox', { name: 'Angebotstitel' })).toHaveText('Filial-Tablets, Kassen und Schulung')
  await expect.poll(() => saves(calls).at(-1)?.body).toMatchObject({ document: { title: 'Filial-Tablets, Kassen und Schulung' } })
  expect(world.versions.get(Q.issued)![0]!.document.title).toBe('Filial-Tablets und Kassenanbindung')
  await expect(page.getByRole('button', { name: 'Restore my work' })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('discarding the work from before an issue leaves the version as it is and does not ask again', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { world, calls } = await setup(page)
  await seedRecovery(page, world, Q.issued, { baseVersion: 0, title: 'Discarded evidence' })
  await full(page, Q.issued)
  const notice = page.locator('.quote-notices .notice').filter({ hasText: 'You have unsaved work' })
  await notice.getByRole('button', { name: 'Discard' }).click()
  await expect(notice).toHaveCount(0)
  expect(saves(calls)).toHaveLength(0)
  await expect(page.getByText('Your unsaved work from before is discarded. The issued version is unchanged.')).toBeVisible()
  await page.goto('/business/quotes')
  await fullWithDeferredRecovery(page, Q.issued)
  await expect(page.locator('.quote-notices .notice').filter({ hasText: 'You have unsaved work' })).toHaveCount(0)
})

test('a copy without changes, or from another version’s draft, is never offered; a matching one is', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { world } = await setup(page)
  // Nothing changed: an issued quote that was only read leaves nothing to restore.
  await seedRecovery(page, world, Q.issued, { baseVersion: 0 })
  await fullWithDeferredRecovery(page, Q.issued)
  await expect(page.locator('.quote-notices .notice')).toHaveCount(0)
  // From the draft of another version: stale, never shown.
  await seedRecovery(page, world, Q.draft, { baseVersion: 3, title: 'From another version' })
  await fullWithDeferredRecovery(page, Q.draft)
  await expect(page.getByRole('button', { name: 'Restore my work' })).toHaveCount(0)
  // From this draft: offered.
  await seedRecovery(page, world, Q.draft, { baseVersion: 0, title: 'From this draft' })
  await fullWithDeferredRecovery(page, Q.draft)
  await page.getByRole('button', { name: 'Restore my work' }).click()
  await expect(page.getByRole('textbox', { name: 'Angebotstitel' })).toHaveText('From this draft')
})
