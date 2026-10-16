// SPDX-License-Identifier: AGPL-3.0-only
// Risk (AEON-1421): switching quotes while a detail read or a customer-link
// revoke is in flight shows or revokes the quote that is no longer on screen.
import { afterEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createMemoryHistory, createRouter, type Router, type RouteRecordRaw } from 'vue-router'
import { defineComponent } from 'vue'
import { confirmAction, confirmState, settleConfirm } from '../src/lib/confirm'
import { lifecycleError, receiptBusy, receiptUrl, RECEIPT_PILL, shortDigest, type AcceptanceNotice, type ConfirmationJob, type PublicLink, type QuoteVersion } from '../src/lib/quotes/lifecycle'
import { dayText, statusOf, STATUS_META } from '../src/lib/quotes/list'
import { deferred, flush, setupSource, sourceModule } from './record-source'

const A = '11111111-1111-4111-8111-111111111111'
const B = '22222222-2222-4222-8222-222222222222'
const stopped: (() => void)[] = []
const messages: string[] = []
type Gate<T> = ReturnType<typeof deferred<T>>
const linkReads: { id: string; version: number; gate: Gate<PublicLink | null> }[] = []
const versionReads: { id: string; gate: Gate<QuoteVersion[]> }[] = []
const noticeReads: Gate<AcceptanceNotice[]>[] = []
const confirmationReads: { id: string; version: number; gate: Gate<ConfirmationJob | null> }[] = []
const revokes: { id: string; version: number; gate: Gate<PublicLink> }[] = []
const creates: { id: string; version: number; gate: Gate<PublicLink> }[] = []

function activeLink(id: string, path: string): PublicLink {
  return { id: `link-${id.slice(0, 8)}`, public_tenant: 'tenant', quote_node_id: id, version: 1, target_content_sha256: 'ab', expires_at: '2030-01-01T00:00:00Z', path }
}
function versionRow(id: string, mark: string): QuoteVersion {
  return { quote_node_id: id, version: 1, currency: 'EUR', title: id, content_sha256: mark.repeat(64), created_by_principal_id: 'p', created_at: '2026-01-01T00:00:00Z', digest_mode: 'document-v2', pricing_mode: 'cent-half-up-v1', total: '10.00' }
}
function receiptJob(id: string, mark: string): ConfirmationJob {
  return { quote_node_id: id, version: 1, state: 'ready', attempts: 1, next_attempt_at: '2030-01-01T00:00:00Z', receipt_sha256: mark.repeat(64), updated_at: '2026-01-02T00:00:00Z' }
}
function projection(id: string, version: number) {
  return { quote_node_id: id, project_node_id: 'p', customer_org_node_id: 'c', current_version: version, state: 'issued' as const, revision: version, archived: false, project_ref: '', classic_status: 'sent' as const }
}
const revokeLink = vi.fn((id: string, version: number) => {
  const gate = deferred<PublicLink>()
  revokes.push({ id, version, gate })
  return gate.promise
})
const createLink = vi.fn((id: string, version: number) => {
  const gate = deferred<PublicLink>()
  creates.push({ id, version, gate })
  return gate.promise
})
const lifecycle = {
  lifecycleError, shortDigest, receiptBusy, receiptUrl, RECEIPT_PILL,
  linkUrl: (link: { path?: string }) => link.path ? `https://quotes.test${link.path}` : '',
  getLink: (id: string, version: number) => { const gate = deferred<PublicLink | null>(); linkReads.push({ id, version, gate }); return gate.promise },
  createLink,
  revokeLink,
  listVersions: (id: string) => { const gate = deferred<QuoteVersion[]>(); versionReads.push({ id, gate }); return gate.promise },
  acceptanceNotices: () => { const gate = deferred<AcceptanceNotice[]>(); noticeReads.push(gate); return gate.promise },
  getConfirmation: (id: string, version: number) => { const gate = deferred<ConfirmationJob | null>(); confirmationReads.push({ id, version, gate }); return gate.promise },
  retryConfirmation: vi.fn(),
  readiness: async () => ({ renderer_available: true, smtp_enabled: true, smtp_configured: true, email_delivery: 'off' }),
}
const toast = (message: string) => { messages.push(message); return messages.length }

function quoteRouter(): Router {
  const empty = defineComponent({ render: () => null })
  const views = (routes: RouteRecordRaw[]): RouteRecordRaw[] => routes.map(route => ({ ...route, ...(route.component ? { component: empty } : {}), ...(route.children ? { children: views(route.children) } : {}) })) as RouteRecordRaw[]
  const session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }, requiresSignIn: false, error: '', refresh: async () => {} }
  return sourceModule<{ router: Router }>('router.ts', {
    './lib/projectSettings': sourceModule('lib/projectSettings.ts', {}),
    'vue-router': { createWebHistory: createMemoryHistory, createRouter: (options: Parameters<typeof createRouter>[0]) => createRouter({ ...options, routes: views(options.routes) }) },
    './lib/brand': { setPageTitle() {} }, './stores/projects': {}, './stores/session': { useSession: () => session }, './stores/workVocabulary': { useWorkVocabulary: () => ({ load: async () => {} }) },
    './lib/api': { sessionEnded: {} }, './lib/authz': {}, './lib/toast': { toast }, './lib/confirm': { settleConfirm },
    './lib/attachLink': { hasAttachFragment: () => false, announceAttachCode() {}, dropAttachCode() {} }, './lib/importDraft': { carriesDraft: () => false }, './lib/identityScope': {}, './lib/signInReturn': { takeSignInReturn: () => '/' },
    './lib/knowledge': {}, './components/work/projectNavigation': {}, './lib/ticketPeek': {},
    './views/ProjectsView.vue': { default: empty }, './views/SignInView.vue': { default: empty }, './views/NotFoundView.vue': { default: empty },
  }).router
}

afterEach(() => {
  settleConfirm(false)
  stopped.splice(0).forEach(stop => stop())
  linkReads.length = 0; versionReads.length = 0; noticeReads.length = 0; confirmationReads.length = 0; revokes.length = 0; creates.length = 0; messages.length = 0
  revokeLink.mockClear(); createLink.mockClear()
  vi.unstubAllGlobals()
})

it('AEON-1421: switching quotes drops the other quote’s link read and refuses its revoke', async () => {
  vi.stubGlobal('sessionStorage', { getItem: () => null, setItem() {}, removeItem() {} })
  const linkProps = reactive({ quoteId: A, version: 1, admin: true, acceptable: true, accepted: false })
  const detailProps = reactive({ quoteId: A, offerNo: 'A-1', projection: projection(A, 1), frozen: null, local: 'saved', admin: true, staff: true, customerName: 'Ada', viewing: null as number | null, busy: '' })
  const receiptProps = reactive({ quoteId: A, version: 1, admin: true })
  const link = setupSource('components/quotes/details/QuoteLinkCard.vue', linkProps, {
    '../../../lib/quotes/qr': { quoteQr: () => ({ size: 1, path: '' }) },
    '../../../lib/confirm': { confirmAction },
    '../../../lib/quotes/lifecycle': lifecycle,
    '../../../lib/toast': { toast },
  })
  const details = setupSource('components/quotes/details/QuoteDetails.vue', detailProps, {
    '../../../lib/useAttachments': { useAttachments: () => ({}) },
    '../../../lib/quotes/lifecycle': lifecycle,
    '../../../lib/quotes/list': { dayText, statusOf, STATUS_META },
    '../../../lib/toast': { toast },
  })
  const receipt = setupSource('components/quotes/details/QuoteReceiptCard.vue', receiptProps, {
    '../../../lib/quotes/lifecycle': lifecycle,
    '../../../lib/toast': { toast },
    '../../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
  })
  stopped.push(link.stop, details.stop, receipt.stop)
  expect(linkReads.map(read => [read.id, read.version])).toEqual([[A, 1]])
  expect(versionReads.map(read => read.id)).toEqual([A])
  expect(confirmationReads.map(read => [read.id, read.version])).toEqual([[A, 1]])

  // The revoke confirmation and the three reads are in flight for quote A.
  const revoking = link.state.revoke()
  expect(confirmState.request?.title).toBe('Revoke the customer link?')
  linkProps.quoteId = B; linkProps.version = 2
  detailProps.quoteId = B; detailProps.projection = projection(B, 2)
  receiptProps.quoteId = B; receiptProps.version = 2
  await flush()
  expect(linkReads.map(read => read.id)).toEqual([A, B])
  expect(versionReads.map(read => read.id)).toEqual([A, B])
  expect(confirmationReads.map(read => read.id)).toEqual([A, B])

  // A's answers arrive after B is the quote on screen, and before B's own answers.
  linkReads[0]!.gate.resolve(activeLink(A, '/offers/quote-a'))
  versionReads[0]!.gate.resolve([versionRow(A, 'a')])
  noticeReads[0]!.resolve([{ quote_node_id: A, version: 1, channel: 'public', accepted_at: '2026-01-01T00:00:00Z', confirmation_state: 'ready' }])
  confirmationReads[0]!.gate.resolve(receiptJob(A, 'a'))
  await flush()
  expect(link.state.fresh.value).toBe('')
  expect(link.state.link.value).toBeNull()
  expect(link.state.loaded.value).toBe(false)
  expect(link.state.error.value).toBe('')
  expect(details.state.versions.value).toBeNull()
  expect(details.state.error.value).toBe('')
  expect(receipt.state.job.value).toBeNull()
  expect(receipt.state.loaded.value).toBe(false)
  expect(receipt.state.error.value).toBe('')

  // Confirming the dialog that was opened for A must not revoke A or B.
  settleConfirm(true)
  await revoking
  expect(revokeLink).not.toHaveBeenCalled()
  expect(messages).toEqual([])

  linkReads[1]!.gate.resolve(activeLink(B, '/offers/quote-b'))
  versionReads[1]!.gate.resolve([versionRow(B, 'b')])
  noticeReads[1]!.resolve([{ quote_node_id: B, version: 2, channel: 'public', accepted_at: '2026-02-01T00:00:00Z', confirmation_state: 'ready' }])
  confirmationReads[1]!.gate.reject(new Error('down'))
  await flush()
  expect(link.state.fresh.value).toBe('https://quotes.test/offers/quote-b')
  expect(link.state.link.value).toMatchObject({ quote_node_id: B, path: '/offers/quote-b' })
  expect(link.state.loaded.value).toBe(true)
  expect(link.state.error.value).toBe('')
  expect(details.state.versions.value).toEqual([versionRow(B, 'b')])
  expect(details.state.notices.value).toEqual([{ quote_node_id: B, version: 2, channel: 'public', accepted_at: '2026-02-01T00:00:00Z', confirmation_state: 'ready' }])
  expect(details.state.error.value).toBe('')
  expect(receipt.state.job.value).toBeNull()
  expect(receipt.state.loaded.value).toBe(true)
  expect(receipt.state.error.value).toBe('The receipt could not be read.')
  expect(link.state.fresh.value).not.toContain('/offers/quote-a')
  expect(JSON.stringify(link.state.link.value)).not.toContain('quote-a')
  expect(JSON.stringify(details.state.versions.value)).not.toContain('a'.repeat(64))
  expect(JSON.stringify(details.state.notices.value)).not.toContain(A)

  // A revoke that was confirmed on A, then leaves the screen, still calls A and does not paint B.
  linkProps.quoteId = A; linkProps.version = 1
  await flush()
  linkReads.at(-1)!.gate.resolve(activeLink(A, '/offers/quote-a'))
  await flush()
  expect(link.state.fresh.value).toBe('https://quotes.test/offers/quote-a')
  const inflight = link.state.revoke()
  settleConfirm(true)
  await flush()
  expect(revokeLink).toHaveBeenCalledExactlyOnceWith(A, 1)
  linkProps.quoteId = B; linkProps.version = 2
  await flush()
  linkReads.at(-1)!.gate.resolve(activeLink(B, '/offers/quote-b'))
  await flush()
  revokes[0]!.gate.resolve({ ...activeLink(A, '/offers/quote-a'), revoked_at: '2026-03-01T00:00:00Z' })
  await inflight
  await flush()
  expect(link.state.fresh.value).toBe('https://quotes.test/offers/quote-b')
  expect(link.state.link.value).toMatchObject({ quote_node_id: B })
  expect(link.state.link.value?.revoked_at).toBeUndefined()
  expect(messages).toEqual([])
  expect(revokeLink).toHaveBeenCalledExactlyOnceWith(A, 1)

  // A read already in flight must not replace the link Create just showed, and
  // Create for B must not paint that link once A is the quote on screen.
  linkProps.quoteId = A; linkProps.version = 1
  await flush()
  const stale = linkReads.at(-1)!
  const creating = link.state.create()
  stale.gate.resolve(activeLink(A, '/offers/stale-a'))
  await flush()
  expect(link.state.fresh.value).not.toContain('/offers/stale-a')
  creates[0]!.gate.resolve({ ...activeLink(A, '/offers/fresh-a'), token: 'once' })
  await creating
  await flush()
  expect(createLink).toHaveBeenCalledExactlyOnceWith(A, 1, expect.any(String))
  expect(link.state.fresh.value).toBe('https://quotes.test/offers/fresh-a')
  expect(link.state.loaded.value).toBe(true)
  expect(link.state.link.value).toMatchObject({ quote_node_id: A })
  expect(link.state.link.value?.token).toBeUndefined()
  expect(link.state.link.value?.path).toBeUndefined()

  linkProps.quoteId = B; linkProps.version = 2
  await flush()
  linkReads.at(-1)!.gate.resolve(activeLink(B, '/offers/quote-b'))
  await flush()
  const creatingOther = link.state.create()
  linkProps.quoteId = A; linkProps.version = 1
  await flush()
  creates.at(-1)!.gate.resolve({ ...activeLink(B, '/offers/fresh-b'), token: 'once' })
  await creatingOther
  linkReads.at(-1)!.gate.resolve(activeLink(A, '/offers/quote-a'))
  await flush()
  expect(link.state.fresh.value).toBe('https://quotes.test/offers/quote-a')
  expect(link.state.link.value).toMatchObject({ quote_node_id: A, path: '/offers/quote-a' })
  expect(createLink).toHaveBeenLastCalledWith(B, 2, expect.any(String))
  expect(link.state.fresh.value).not.toContain('fresh-b')

  // Leaving the address refuses the confirmation that is still open.
  const router = quoteRouter()
  await router.push(`/business/quotes?quote=${A}`)
  const pending = confirmAction({ title: 'Revoke the customer link?', confirmLabel: 'Revoke link', danger: true })
  const moved = router.push(`/business/quotes?quote=${B}`)
  await expect(pending).resolves.toBe(false)
  await moved
  expect(router.currentRoute.value.fullPath).toBe(`/business/quotes?quote=${B}`)
  expect(confirmState.request).toBeNull()
  expect(confirmState.resolve).toBeNull()
  settleConfirm(true)
  expect(revokeLink).toHaveBeenCalledExactlyOnceWith(A, 1)
})
