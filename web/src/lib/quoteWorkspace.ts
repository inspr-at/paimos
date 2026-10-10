// SPDX-License-Identifier: AGPL-3.0-only
// One live quote per open workspace: the P6 edit session, its presence lease and
// the editor with its undo history. The docked panel and the full page borrow
// the same entry, so moving between them keeps unsaved work, history and the
// presence session instead of leaving and rejoining. An entry nobody holds is
// closed after a short grace period (long enough for a route change).
import { ref, shallowRef, watch, type Ref, type ShallowRef } from 'vue'
import { APIError } from './api'
import { QuoteSession, type SessionView } from './quoteSession'
import { QuotePresence, type PresenceSnapshot } from './quotePresence'
import type { RecoveryDraft } from './quoteRecovery'
import { QuoteEditor } from './quotes/editor'
import { getQuote, getVersion, type QuoteProjection, type QuoteVersion } from './quotes/lifecycle'
import { useSession } from '../stores/session'

export interface LiveQuote {
  readonly quoteId: string
  readonly session: QuoteSession
  readonly view: ShallowRef<SessionView | null>
  readonly presence: ShallowRef<PresenceSnapshot | null>
  readonly recovery: ShallowRef<RecoveryDraft | null>
  // Layout can render before the IndexedDB recovery decision is complete.
  readonly recoveryReady: Ref<boolean>
  readonly projection: ShallowRef<QuoteProjection | null>
  // The frozen document of the current version once the quote is issued.
  readonly frozen: ShallowRef<QuoteVersion | null>
  readonly error: Ref<string>
  readonly draftMissing: Ref<boolean>
  editor: QuoteEditor | null
  presenceClient: QuotePresence | null
  canSaveNow: (() => boolean) | null
  // Settles once the draft is open and any unsaved copy from an earlier visit is read.
  readonly ready: Promise<void>
  refresh(): Promise<void>
}
interface Entry { quote: LiveQuote; holders: number; timer: number | undefined; dispose(): void }
const entries = new Map<string, Entry>()
const keyOf = (scope: Scope, quoteId: string) => `${scope.tenantId}:${scope.principalId}:${quoteId}`
export interface Scope { tenantId: string; principalId: string }

function open(scope: Scope, quoteId: string): Entry {
  const session = new QuoteSession({ quoteId, tenantId: scope.tenantId, principalId: scope.principalId })
  let markReady: () => void = () => {}
  const quote: LiveQuote = {
    quoteId, session,
    view: shallowRef(null), presence: shallowRef(null), recovery: shallowRef(null), projection: shallowRef(null), frozen: shallowRef(null),
    error: ref(''), draftMissing: ref(false), recoveryReady: ref(false), editor: null, presenceClient: null, canSaveNow: null,
    ready: new Promise<void>(resolve => { markReady = resolve }),
    refresh,
  }
  let closed = false
  let autosaveTimer: number | undefined
  const unsubscribe = session.subscribe(next => {
    quote.view.value = { ...next }
    window.clearTimeout(autosaveTimer)
    if (!closed && next.local === 'dirty' && next.remote === 'current') {
      autosaveTimer = window.setTimeout(() => { if (quote.canSaveNow?.() !== false) void session.save() }, 700)
    }
  })
  async function refresh() {
    const projection = await getQuote(quoteId)
    if (closed) return
    quote.projection.value = projection
    if (projection.state !== 'draft') session.onPresence({ sessions: quote.presence.value?.sessions ?? [], draft_revision: session.view.baseRevision, quote_revision: projection.revision, state: projection.state })
    const current = quote.frozen.value
    if (projection.state !== 'draft' && projection.current_version > 0 && current?.version !== projection.current_version) {
      const version = await getVersion(quoteId, projection.current_version)
      if (!closed) quote.frozen.value = version
    } else if (projection.state === 'draft') quote.frozen.value = null
  }
  void (async () => {
    try {
      const [recovery] = await Promise.all([
        session.open().catch(e => { if (e instanceof APIError && e.status === 404) { quote.draftMissing.value = true; return null } throw e }),
        refresh(),
      ])
      if (closed) return
      quote.recovery.value = recovery
      quote.recoveryReady.value = true
      markReady()
      // Opening the draft marks it clean; an issued quote stays read-only whichever answered first.
      const known = quote.projection.value
      if (known && known.state !== 'draft') session.onPresence({ sessions: [], draft_revision: session.view.baseRevision, quote_revision: known.revision, state: known.state })
      const presence = new QuotePresence(quoteId, scope.principalId,
        snapshot => { quote.presence.value = snapshot; session.onPresence(snapshot) },
        notice => {
          session.onNotice(notice)
          // Issued, accepted or revised elsewhere: the projection follows.
          if (notice.state && notice.state !== quote.projection.value?.state) void refresh().catch(() => {})
        })
      quote.presenceClient = presence
      await presence.start(session.view.baseRevision).catch(() => { presence.stop(); if (quote.presenceClient === presence) quote.presenceClient = null })
    } catch (cause) {
      markReady()
      if (!closed) quote.error.value = cause instanceof APIError && cause.status === 404 ? 'This quote does not exist, or it is not shared with you.' : cause instanceof Error ? cause.message : 'The quote could not be opened.'
    }
  })()
  return {
    quote, holders: 0, timer: undefined,
    dispose() { closed = true; window.clearTimeout(autosaveTimer); unsubscribe(); quote.presenceClient?.stop(); quote.presenceClient = null; session.dispose() },
  }
}

// Signing out (or in as someone else) closes every live quote at once.
let watching = false
function watchIdentity() {
  if (watching) return
  watching = true
  const session = useSession()
  watch(() => session.identity?.principal.id ?? null, (id, before) => { if (id !== before) closeAllQuotes() })
}
export function acquireQuote(scope: Scope, quoteId: string): LiveQuote {
  watchIdentity()
  const key = keyOf(scope, quoteId)
  let entry = entries.get(key)
  if (!entry) { entry = open(scope, quoteId); entries.set(key, entry) }
  window.clearTimeout(entry.timer); entry.timer = undefined
  entry.holders++
  return entry.quote
}
export function releaseQuote(scope: Scope, quote: LiveQuote, graceMs = 2500) {
  const key = keyOf(scope, quote.quoteId)
  const entry = entries.get(key)
  if (!entry || entry.quote !== quote) return
  entry.holders = Math.max(0, entry.holders - 1)
  if (entry.holders > 0) return
  window.clearTimeout(entry.timer)
  entry.timer = window.setTimeout(() => {
    if (entries.get(key) !== entry || entry.holders > 0) return
    entries.delete(key); entry.dispose()
  }, graceMs)
}
// A quote whose session cannot continue (revised into a new draft): closed now,
// so the next acquire opens a fresh one.
export function dropQuote(scope: Scope, quote: LiveQuote) {
  const key = keyOf(scope, quote.quoteId)
  const entry = entries.get(key)
  if (!entry || entry.quote !== quote) return
  window.clearTimeout(entry.timer); entries.delete(key); entry.dispose()
}
// Closing a quote for good (signing out): nothing lingers.
export function closeAllQuotes() {
  for (const [key, entry] of entries) { window.clearTimeout(entry.timer); entries.delete(key); entry.dispose() }
}
