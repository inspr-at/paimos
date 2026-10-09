// SPDX-License-Identifier: AGPL-3.0-only
// State and writes of the minimal Models card (AEON-1011). Every write is bound to the person, scope and revision on
// screen; an answer for anything else is dropped, a refusal says so, and Undo only runs against the state it undid.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from './api'
import { can, onAccessChange } from './authz'
import { toast } from './toast'
import { getMembers } from './access'
import { preferenceFailure } from './modelsSettings'
import { useSession } from '../stores/session'
import { dismissLine, getRegistry, getSimple, getTail, getWorkspaceRules, putDismissed, putEffort, putOrder, putWorkspaceRules, resetOrder } from './modelsSimpleApi'
import {
  ALL_COLUMN, buildEntries, buildRows, effortLevel, lockReason, movePin, nearestEffort, nextView, orderBody, orderWithFirst, pinFirst, rulesBody, unpin,
  type ModelEntry, type RegistryProfile, type RowView, type RulesBody, type Scope, type SimpleDocument, type TailBoard, type TailColumn,
} from './modelsSimple'

interface Revs { prefs: number; rules: number }
type Step = (revs: Revs) => Promise<Revs>
interface Change { forward: Step[]; backward: Step[]; rules?: boolean; message?: string }
class Unconfirmed extends Error {}

/** An answer that is not the contract is an error, never a half-drawn page. */
const validDocument = (value: SimpleDocument | null | undefined): value is SimpleDocument =>
  !!value && !!value.all && Array.isArray(value.exceptions) && Array.isArray(value.unavailable) && Array.isArray(value.new_lines) && Number.isSafeInteger(value.revision) && Number.isSafeInteger(value.rules_revision)

export function useModelsSimple() {
  const session = useSession()
  const scope = ref<Scope>('me')
  const doc = ref<SimpleDocument | null>(null), workspace = ref<SimpleDocument | null>(null), tail = ref<TailBoard | null>(null)
  const profiles = ref<RegistryProfile[]>([])
  const loading = ref(false), busy = ref(false), error = ref(''), failed = ref(false), announcement = ref('')
  const draft = ref<string | null>(null)
  // Who locked a row, by name: only people who may see the member list can name others; the rest read "an admin".
  const names = ref(new Map<string, string>())
  const owner = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
  const isPerson = computed(() => !!owner.value && session.identity?.principal.kind === 'person' && session.authenticationCurrent())
  // Agents read the page like anyone; only a person's session writes.
  const readable = computed(() => !!owner.value && session.authenticationCurrent() && can('models.read'))
  const admin = computed(() => isPerson.value && can('model_prefs.manage'))
  const editable = computed(() => isPerson.value && readable.value && (scope.value === 'me' || admin.value))
  const key = computed(() => `${owner.value}/${scope.value}`)
  const epoch = ref(0), actionKey = computed(() => `${key.value}/${epoch.value}`)
  let generation = 0, read = 0, abort: AbortController | undefined, alive = true
  const capture = () => { const started = generation, identity = key.value; return () => alive && started === generation && identity === key.value && session.authenticationCurrent() }

  const entries = computed(() => buildEntries(profiles.value))
  const labels = computed(() => new Map((tail.value?.columns ?? []).map(column => [column.column, column.label] as const)))
  const rows = computed<RowView[]>(() => doc.value ? buildRows({ doc: doc.value, workspace: workspace.value, scope: scope.value, canEdit: editable.value, entries: entries.value, labels: labels.value, draft: draft.value }) : [])
  const kinds = computed(() => (tail.value?.columns ?? []).filter(column => column.column !== ALL_COLUMN && !column.column.startsWith('review:')))
  const freeKinds = computed(() => kinds.value.filter(column => !rows.value.some(row => row.column === column.column)))
  const next = computed(() => nextView({ next: doc.value?.next ?? null, rows: rows.value.filter(row => !row.draft), entries: entries.value, labels: labels.value }))
  // A new line is announced only when the registry can already place it; proposals wait in the registry card.
  const news = computed(() => (doc.value?.new_lines ?? []).map(item => ({ entry: entries.value.find(entry => entry.line === item.line) ?? null, canDo: item.can_do })).filter((item): item is { entry: ModelEntry; canDo: string[] } => !!item.entry)[0] ?? null)
  const columnOf = (column: string): TailColumn | undefined => tail.value?.columns.find(item => item.column === column)
  /** What a column cannot do with a line, in the server's own words. */
  const cantReason = (column: string, line: string) => columnOf(column)?.cant.find(item => item.line === line)?.reason ?? ''

  async function load(): Promise<boolean> {
    const current = capture(), turn = ++read, target = scope.value, previous = doc.value?.person_id
    abort?.abort(); abort = new AbortController(); loading.value = true
    try {
      // The stored order behind each pick must be the one the page shows: ask again once if a write landed between the reads.
      for (let attempt = 0; attempt < 2; attempt++) {
        const [simple, board, registry, workspaceDoc] = await Promise.all([getSimple(target, abort.signal), getTail(target, abort.signal), getRegistry(abort.signal), target === 'me' ? getSimple('default', abort.signal).catch(() => null) : Promise.resolve(null)])
        if (!current() || turn !== read) return false
        if (!validDocument(simple) || !Array.isArray(board?.columns) || !Array.isArray(registry)) throw new Error('Models couldn’t load.')
        if (simple.revision !== board.revision) continue
        const changedPerson = previous !== undefined && previous !== simple.person_id
        doc.value = simple; tail.value = board; profiles.value = registry; workspace.value = workspaceDoc; error.value = ''; failed.value = false
        void nameLockOwners(simple, current)
        if (changedPerson) { generation++; epoch.value++; busy.value = false; error.value = 'Your identity changed. The page was refreshed; the old change and Undo were discarded.'; return false }
        return true
      }
      throw new Error('Models changed while loading. Try again.')
    } catch (failure) {
      if (current() && turn === read) { doc.value = null; tail.value = null; workspace.value = null; failed.value = true; error.value = failure instanceof Error ? failure.message : 'Models couldn’t load.' }
      return false
    } finally { if (current() && turn === read) loading.value = false }
  }

  async function nameLockOwners(simple: SimpleDocument, current: () => boolean) {
    const unknown = [simple.all, ...simple.exceptions].map(row => row.lock?.by).filter((id): id is string => !!id && !names.value.has(id))
    if (!unknown.length || !can('members.read')) return
    try {
      const members = await getMembers()
      if (current() && Array.isArray(members?.people)) names.value = new Map(members.people.map(person => [person.principal_id, person.name] as const))
    } catch { /* The lock still says it was set for everyone; the name is a courtesy. */ }
  }
  const advanced = (result: { revision: number }, before: number) => {
    if (!Number.isSafeInteger(result.revision) || result.revision <= before) throw new Unconfirmed()
    return result.revision
  }
  const order = (target: Scope, person: string | null, column: string, body: { rank: string[]; not: string[] }): Step => async revs => ({ ...revs, prefs: advanced(await putOrder(target, column, body, revs.prefs, person), revs.prefs) })
  const reset = (target: Scope, person: string | null, column: string): Step => async revs => ({ ...revs, prefs: advanced(await resetOrder(target, column, revs.prefs, person), revs.prefs) })
  const effortStep = (target: Scope, person: string | null, column: string, effort: string | null): Step => async revs => ({ ...revs, prefs: advanced(await putEffort(target, column, effort, revs.prefs, person), revs.prefs) })
  const rulesStep = (column: string, body: RulesBody): Step => async revs => ({ ...revs, rules: advanced(await putWorkspaceRules(column, body, revs.rules), revs.rules) })

  const saved = () => scope.value === 'default' ? 'Saved for everyone' : 'Saved'
  /** Runs the steps in order against the revisions on screen, refreshes, and offers Undo for ten seconds. */
  async function commit(change: Change, finish: (ok: boolean) => void = () => {}): Promise<boolean> {
    const state = doc.value
    if (!state || !editable.value || busy.value) return false
    const current = capture(), shown = actionKey.value
    busy.value = true; error.value = ''
    let revs: Revs = { prefs: state.revision, rules: state.rules_revision }, done = 0
    try {
      // A person or scope that changed mid-way ends the change here: no later write is made for them.
      for (const step of change.forward) { if (!current()) return false; revs = await step(revs); done++ }
      if (!current()) return false
      const end = revs, loaded = await load()
      finish(true)
      if (!current()) return false
      if (!loaded) { if (!error.value) error.value = 'Saved, but the page could not be refreshed. Reload before making another change.'; return true }
      announcement.value = change.message ?? saved()
      const expires = Date.now() + 10_000
      toast(change.message ?? saved(), { timeout: 10_000, action: change.backward.length ? { label: 'Undo', run: () => {
        const now = doc.value
        if (!current() || actionKey.value !== shown || Date.now() > expires || !now || busy.value) return
        if (now.revision !== end.prefs || (change.rules && now.rules_revision !== end.rules)) { toast('The page changed. Undo is no longer available.', { tone: 'error' }); return }
        void commit({ forward: change.backward, backward: [], rules: change.rules, message: 'Undone' })
      } } : undefined })
      return true
    } catch (failure) {
      finish(false)
      if (!current()) return false
      const partial = done > 0
      if (failure instanceof Unconfirmed) {
        await load()
        if (current()) error.value = 'Could not confirm the save. Reload before making another change; Undo is unavailable.'
      } else if (failure instanceof APIError && failure.status === 409) {
        await load()
        if (current()) error.value = partial ? 'Changed elsewhere. Only part of the change was saved; the page was refreshed.' : 'Changed elsewhere. The page was refreshed; the change was not saved.'
      } else {
        const message = failure instanceof APIError && [403, 428].includes(failure.status) ? preferenceFailure(failure.status) : failure instanceof Error ? failure.message : 'Could not save.'
        if (partial) await load()
        if (current()) error.value = partial ? `Only part of the change was saved: ${message}` : message
      }
      return false
    } finally { if (current()) busy.value = false }
  }

  const person = () => doc.value?.person_id ?? null
  const conflict = 'Changed elsewhere. The page was refreshed; the change was not saved.'
  /** Refresh first, then say why: a refresh clears the line it replaces. */
  async function refreshWith(message: string) { const current = capture(); await load(); if (current()) error.value = message }
  /** Scope, generation and revisions from the moment the person acts. A later await must not commit for someone else. */
  function seenNow() {
    const state = doc.value, current = capture(), prefs = state?.revision, rulesRev = state?.rules_revision, target = scope.value
    return { current, same: () => !!doc.value && current() && scope.value === target && doc.value.revision === prefs && doc.value.rules_revision === rulesRev }
  }
  // The board calls a saved order "own" on both Just me and For everyone. "default" is the workspace order a person follows, not an order For everyone stored.
  const storedOrder = (column: TailColumn) => column.source === 'own'
  /** An order's existence is not a stored native effort. Skip the write only when that effort is the one on screen. */
  const carriesStoredEffort = (column: TailColumn, wanted: string | null) => !!wanted && column.stored_effort === wanted
  /** The native effort the column's own order stored; null (or absent) means the order inherits one. Undo restores this, never the effort on screen: that one may only be inherited from Default. */
  const storedEffort = (column: TailColumn) => column.stored_effort ?? null

  /** Pick a model (and optionally its level) for a row; a draft row or a row without an order of its own gets one. */
  async function pick(row: RowView, entry: ModelEntry, effort: string | null = null): Promise<boolean> {
    const state = doc.value, column = columnOf(row.column)
    if (!state || !editable.value || !row.editable || !column) return false
    const seen = seenNow()
    const target = scope.value, who = person(), had = storedOrder(column)
    const wanted = effort ?? nearestEffort(entry, row.effort, effortLevel(row.entry, row.effort))
    // Choosing exactly what the default already gives, for everyone, leaves nothing to store.
    if (row.draft && target === 'default' && entry.line === state.all.line && wanted === state.all.effort) { draft.value = null; return true }
    const before = orderBody(column)
    const forward: Step[] = []
    const backward: Step[] = had ? [order(target, who, row.column, before), effortStep(target, who, row.column, storedEffort(column))] : [reset(target, who, row.column)]
    let rules = false
    if (target === 'default' && row.lock && row.line && row.line !== entry.line) {
      try {
        const document = await getWorkspaceRules()
        if (!seen.same() || scope.value !== 'default') return false
        if (document.revision !== state.rules_revision) { await refreshWith(conflict); return false }
        const previous = rulesBody(document, row.column)
        // The pin moves first, so the replacement's effort is checked against the new model.
        forward.push(rulesStep(row.column, movePin(previous, row.line, entry.line)))
        backward.unshift(rulesStep(row.column, previous))
        rules = true
      } catch (failure) {
        if (!seen.current()) return false
        await refreshWith(failure instanceof APIError ? conflict : 'Could not read the lock.')
        return false
      }
    }
    if (!seen.same()) return false
    forward.push(order(target, who, row.column, orderWithFirst(column, entry.line)))
    if (wanted && !carriesStoredEffort(column, wanted)) forward.push(effortStep(target, who, row.column, wanted))
    return commit({ forward, backward, rules }, ok => { if (ok) draft.value = null })
  }

  /** Remove an exception, or reset a person's own row: the kind follows the default again. */
  async function clear(row: RowView): Promise<boolean> {
    const state = doc.value, column = columnOf(row.column)
    if (!state || !editable.value || row.isDefault && scope.value === 'default' || !column) return false
    const seen = seenNow()
    const target = scope.value, who = person()
    const before = orderBody(column), forward: Step[] = [reset(target, who, row.column)]
    // A reset deletes the order and its effort with it; a stored effort comes back, an inherited one stays inherited.
    const stored = storedEffort(column)
    const backward: Step[] = [order(target, who, row.column, before), ...(stored ? [effortStep(target, who, row.column, stored)] : [])]
    let rules = false
    if (target === 'default' && row.lock && row.line) {
      try {
        const document = await getWorkspaceRules()
        if (!seen.same() || scope.value !== 'default') return false
        if (document.revision !== state.rules_revision) { await refreshWith(conflict); return false }
        const previous = rulesBody(document, row.column)
        forward.push(rulesStep(row.column, unpin(previous, row.line)))
        backward.unshift(rulesStep(row.column, previous))
        rules = true
      } catch {
        if (!seen.current()) return false
        await refreshWith(conflict)
        return false
      }
    }
    if (!seen.same()) return false
    return commit({ forward, backward, rules })
  }

  /** Members can't change a locked row: a workspace pin of the pick to the top of its column. */
  async function lock(row: RowView, on: boolean): Promise<boolean> {
    const state = doc.value
    if (!state || scope.value !== 'default' || !can('model_prefs.manage') || !row.line) return false
    const seen = seenNow()
    try {
      const document = await getWorkspaceRules()
      if (!seen.same() || scope.value !== 'default') return false
      if (document.revision !== state.rules_revision) { await refreshWith(conflict); return false }
      const previous = rulesBody(document, row.column), name = session.identity?.principal.name || 'an admin'
      const next = on ? pinFirst(previous, row.line, lockReason(name)) : unpin(previous, row.line)
      return commit({ forward: [rulesStep(row.column, next)], backward: [rulesStep(row.column, previous)], rules: true })
    } catch {
      if (!seen.current()) return false
      await refreshWith(conflict)
      return false
    }
  }

  async function dismiss(line: string): Promise<boolean> {
    const state = doc.value, board = tail.value
    if (!state || !board || !editable.value) return false
    const target = scope.value, who = person(), before = [...board.profile.dismissed_lines]
    const undo: Step = async revs => ({ ...revs, prefs: advanced(await putDismissed(target, before, revs.prefs, who), revs.prefs) })
    return commit({ forward: [async revs => ({ ...revs, prefs: advanced(await dismissLine(target, line, revs.prefs, who), revs.prefs) })], backward: [undo] })
  }

  /** "Use it for…": a new model takes a kind of work at its highest level; nothing else changes. */
  async function use(entry: ModelEntry, column: string): Promise<boolean> {
    if (!editable.value || busy.value || !columnOf(column)) return false
    if (!rows.value.some(row => row.column === column)) draft.value = column
    const row = rows.value.find(item => item.column === column)
    if (!row) return false
    const done = await pick(row, entry, entry.efforts.at(-1)?.name ?? null)
    if (draft.value === column) draft.value = null
    return done
  }
  function startException(column: string) { if (editable.value && columnOf(column)) draft.value = column }
  function discardException() { draft.value = null }
  function setScope(value: Scope) { if (value === 'default' && !can('model_prefs.manage')) return; scope.value = value }
  function dropError() { error.value = '' }

  watch(key, () => { generation++; epoch.value++; abort?.abort(); doc.value = null; workspace.value = null; tail.value = null; draft.value = null; error.value = ''; failed.value = false; announcement.value = ''; busy.value = false; loading.value = false; if (owner.value && can('models.read')) void load() }, { immediate: true, flush: 'sync' })
  const unsubscribe = onAccessChange(() => {
    generation++; epoch.value++; abort?.abort(); busy.value = false
    if (scope.value === 'default' && !can('model_prefs.manage')) { scope.value = 'me'; return }
    if (!can('models.read')) { doc.value = null; return }
    void load()
  })
  watch(() => can('models.read'), allowed => { if (allowed && !doc.value && !loading.value && owner.value) void load() })
  onBeforeUnmount(() => { alive = false; generation++; epoch.value++; abort?.abort(); unsubscribe() })

  return { scope, doc, rows, names, entries, labels, kinds, freeKinds, next, news, tail, loading, busy, error, failed, announcement, draft, admin, editable, readable, owner, actionKey, cantReason, load, pick, use, clear, lock, dismiss, startException, discardException, setScope, dropError }
}
