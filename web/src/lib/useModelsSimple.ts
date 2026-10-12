// SPDX-License-Identifier: AGPL-3.0-only
// State and writes of the minimal Models card (AEON-1011). Every write is bound to the person, scope and revision on
// screen; an answer for anything else is dropped, a refusal says so, and Undo only runs against the state it undid.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from './api'
import { can, onAccessRevalidate } from './authz'
import { toast } from './toast'
import { getMembers } from './access'
import { preferenceFailure } from './modelsSettings'
import { useSession } from '../stores/session'
import { deleteNever, dismissLine, getRegistry, getSimple, getTail, getTicketResolution, getWorkspaceRules, putChain, putDismissed, putEffort, putNever, putOrder, putSituationOrder, putWorkspaceRules, resetOrder, resetSituationOrder, type ChainBody } from './modelsSimpleApi'
import {
  ALL_COLUMN, buildEntries, buildRows, chainEfforts, effortLevel, entryFor, lineName, lineRoutes, lockReason, moveItem, movePin, nearestEffort, nextView, orderBody, orderLines, orderWithFirst, pinFirst, pinnedLast, pinnedSteps, rulesBody, sameOrder, ticketWhy, unpin,
  type ChainEnd, type ModelEntry, type RegistryProfile, type RowView, type RulesBody, type Scope, type SimpleDocument, type SimpleNever, type SimpleStep, type TailBoard, type TailColumn, type WhyView,
} from './modelsSimple'
import { situationFailure, situationOrder, situationSteps, type SituationKey, type SituationOrder } from './modelsSimple'

interface Revs { prefs: number; rules: number }
type Step = (revs: Revs) => Promise<Revs>
interface Change { forward: Step[]; backward: Step[]; rules?: boolean; message?: string; announce?: string }
/** One edit of a row's fallback (AEON-1161): indexes count chain steps after the pick. */
export type ChainChange =
  | { kind: 'move'; from: number; to: number }
  | { kind: 'add'; line: string; effort: string | null }
  | { kind: 'step'; index: number; line: string; effort: string | null }
  | { kind: 'remove'; index: number }
  | { kind: 'end'; end: ChainEnd }
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
  const routes = computed(() => lineRoutes(profiles.value))
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

  async function load(retain = false): Promise<boolean> {
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
      if (current() && turn === read) { if (!retain) { doc.value = null; tail.value = null; workspace.value = null }; failed.value = true; error.value = failure instanceof Error ? failure.message : 'Models couldn’t load.' }
      return false
    } finally { if (current() && turn === read) loading.value = false }
  }

  async function nameLockOwners(simple: SimpleDocument, current: () => boolean) {
    const unknown = [simple.all, ...simple.exceptions].flatMap(row => [row.lock?.by, ...(row.never ?? []).map(never => never.by)]).filter((id): id is string => !!id && !names.value.has(id))
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
  const order = (target: Scope, person: string | null, column: string, body: Parameters<typeof putOrder>[2]): Step => async revs => ({ ...revs, prefs: advanced(await putOrder(target, column, body, revs.prefs, person), revs.prefs) })
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
      announcement.value = change.announce ?? change.message ?? saved()
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
      } else if (failure instanceof APIError && failure.status === 423) {
        // A workspace lock freezes the pick and its order for members; the page shows the locked row again.
        await load()
        if (current()) error.value = `${partial ? 'Only part of the change was saved. ' : ''}This row is locked for everyone; the change was not saved.`
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
  /**
   * The ending the column's own order stored, or '' for none. Undo writes this back, never the row's ending on screen:
   * that one may only be the column's, and a personal order that stores Wait drops the workspace's pinned tail.
   */
  const storedEnd = (column: TailColumn): ChainEnd | '' => column.stored?.end === 'wait' || column.stored?.end === 'default' ? column.stored.end : ''
  /**
   * A column's own order as stored: rank, exclusions, ending (or none) and every native level, then the order's own
   * level. A reset deletes all of them, so its Undo writes this back (AEON-1161).
   */
  const restore = (target: Scope, person: string | null, column: TailColumn): Step[] => {
    const body = orderBody(column), level = storedEffort(column)
    const efforts = Object.fromEntries(Object.entries(column.stored?.efforts ?? {}).filter(([line]) => body.rank.includes(line)))
    return [order(target, person, column.column, { ...body, efforts, end: storedEnd(column) }), ...(level ? [effortStep(target, person, column.column, level)] : [])]
  }

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
    const forward: Step[] = [reset(target, who, row.column)]
    // A reset deletes the whole order; Undo writes it back as stored, an inherited level stays inherited. A row without
    // an order of its own has none to bring back.
    const backward: Step[] = storedOrder(column) ? restore(target, who, column) : []
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

  const chainStep = (target: Scope, person: string | null, column: string, body: ChainBody): Step => async revs => ({ ...revs, prefs: advanced(await putChain(target, column, body, revs.prefs, person), revs.prefs) })
  /** The workspace's own row for a column, as a member's personal order is compared against it. */
  const sharedRow = (column: string) => workspace.value ? [workspace.value.all, ...workspace.value.exceptions].find(row => row.column === column) ?? null : null

  /** Steps at the end of a row's order that a workspace pin keeps there: only an admin on For everyone moves or removes them. */
  const fixedSteps = (row: RowView) => scope.value === 'default' && admin.value ? 0 : pinnedSteps(row, pinnedLast(columnOf(row.column)))

  /**
   * Edit a row's fallback: move, add or change a step at its own level, take one out, or choose how it ends. Every
   * edit saves at once with Undo. On Just me a workspace row becomes the person's own order ("yours · reset"); an
   * order that ends up as the workspace's again goes back to following it. Levels the row inherits stay inherited.
   * Steps a workspace pin keeps last (migrated pins to the bottom) stay last; when an admin on For everyone moves or
   * removes one, its pin is lifted in the same change, so the order alone decides its place.
   */
  async function editChain(row: RowView, change: ChainChange): Promise<boolean> {
    const state = doc.value, column = columnOf(row.column), base = rows.value[0]
    if (!state || !editable.value || busy.value || !row.editable || row.draft || !row.line || !column || !base) return false
    const seen = seenNow()
    // The pinned steps are the last ones; from `start` on, a step is kept there by a workspace rule.
    const start = row.chain.length - pinnedSteps(row, pinnedLast(column))
    let chain: SimpleStep[] = row.chain, end = row.end, levels = false, announce: string | undefined, lift: string[] = []
    if (change.kind === 'move') {
      if (change.from === change.to || !chain[change.from] || !chain[change.to]) return false
      // A move into or out of the pinned steps lifts their pins: the order then keeps the place it shows.
      if (change.from >= start || change.to >= start) lift = chain.slice(start).map(step => step.line)
      chain = moveItem(chain, change.from, change.to)
      announce = `${lineName(entries.value, chain[change.to]!.line)} moved to step ${change.to + 2} of ${chain.length + 1}.`
    } else if (change.kind === 'add' || change.kind === 'step') {
      if (change.kind === 'step' && !chain[change.index]) return false
      // A line runs at most once per order.
      if (orderLines(row).some((line, index) => line === change.line && !(change.kind === 'step' && index === change.index + 1))) return false
      const step = { line: change.line, effort: change.effort }
      // A new step joins before the pinned steps, where it will run.
      chain = change.kind === 'add' ? [...chain.slice(0, start), step, ...chain.slice(start)] : chain.map((item, index) => index === change.index ? step : item)
      levels = true
    } else if (change.kind === 'remove') {
      if (!chain[change.index]) return false
      if (change.index >= start) lift = [chain[change.index]!.line]
      chain = chain.filter((_, index) => index !== change.index)
    } else {
      if (row.isDefault || change.end === end) return false
      end = change.end
    }
    const target = scope.value, who = person(), had = storedOrder(column), level = storedEffort(column)
    // Pins are workspace rules: only an admin on For everyone lifts one.
    if (lift.length && !(target === 'default' && admin.value)) return false
    const pins: { forward: Step[]; backward: Step[] } = { forward: [], backward: [] }
    if (lift.length) {
      try {
        const document = await getWorkspaceRules()
        if (!seen.same() || scope.value !== 'default') return false
        if (document.revision !== state.rules_revision) { await refreshWith(conflict); return false }
        const previous = rulesBody(document, row.column)
        pins.forward.push(rulesStep(row.column, { ...previous, bottom: previous.bottom.filter(pin => !lift.includes(pin.line)) }))
        pins.backward.push(rulesStep(row.column, previous))
      } catch (failure) {
        if (!seen.current()) return false
        await refreshWith(failure instanceof APIError ? conflict : 'Could not read the workspace rules.')
        return false
      }
    }
    if (!seen.same()) return false
    const known = (line: string) => !!entryFor(entries.value, line)
    // The pick's level is written only when the row holds its own; otherwise it keeps following the default.
    const ownPick = row.isDefault || column.stored_effort != null || !!row.entry && row.effort !== nearestEffort(row.entry, base.effort, effortLevel(base.entry, base.effort))
    const efforts = (steps: SimpleStep[]) => chainEfforts({ pick: row.line, pickEffort: row.effort, ownPick, steps, known })
    const rank = (steps: SimpleStep[]) => [row.line!, ...steps.map(step => step.line)]
    const shared = target === 'me' && row.mine ? sharedRow(row.column) : null
    const followsAgain = !!shared && !orderBody(column).not.length && sameOrder({ line: row.line, effort: row.effort, chain, end }, { line: shared.line, effort: shared.effort, chain: shared.chain ?? [], end: shared.end ?? (row.isDefault ? 'wait' : 'default') })
    // A write with levels replaces the order's own level, the one other rows inherit; the order's stored level is written
    // back right after it, so a chain edit never changes what the rest of the page follows.
    const write = (body: ChainBody): Step[] => [chainStep(target, who, row.column, body), ...(body.efforts && level ? [effortStep(target, who, row.column, level)] : [])]
    const forward: Step[] = followsAgain ? [reset(target, who, row.column)] : write({ rank: rank(chain), ...(levels ? { efforts: efforts(chain) } : {}), ...(change.kind === 'end' ? { end } : {}) })
    // Undo writes back what the row showed: a level a removal or a reset dropped, and the ending its order stored (or none).
    const previous: ChainBody = { rank: rank(row.chain), ...(levels || change.kind === 'remove' || followsAgain ? { efforts: efforts(row.chain) } : {}), ...(change.kind === 'end' || followsAgain ? { end: storedEnd(column) } : {}) }
    const backward: Step[] = had ? write(previous) : [reset(target, who, row.column)]
    return commit({ forward: [...pins.forward, ...forward], backward: [...pins.backward, ...backward], rules: lift.length > 0, announce })
  }

  /** Where "Allow again" may be offered: a workspace rule for admins on For everyone, a saved exclusion on its own order. */
  function canAllow(row: RowView, never: SimpleNever): boolean {
    if (never.at) return admin.value && scope.value === 'default' && editable.value
    const column = columnOf(row.column)
    return row.editable && !!column && storedOrder(column) && orderBody(column).not.includes(never.line)
  }
  /** Lift a "Never for …" line; Undo sets it again with the same reason. */
  async function allowAgain(row: RowView, never: SimpleNever): Promise<boolean> {
    const column = columnOf(row.column)
    if (!doc.value || busy.value || !column || !row.never.some(item => item.line === never.line) || !canAllow(row, never)) return false
    const announce = `${lineName(entries.value, never.line)} is allowed again for ${row.isDefault ? 'all work' : row.label}.`
    if (never.at) {
      const lift: Step = async revs => ({ ...revs, rules: advanced(await deleteNever(row.column, never.line, revs.rules), revs.rules) })
      const restore: Step = async revs => ({ ...revs, rules: advanced(await putNever(row.column, never.line, never.why.trim() || 'Not allowed', revs.rules), revs.rules) })
      return commit({ forward: [lift], backward: [restore], rules: true, announce })
    }
    const target = scope.value, who = person(), body = orderBody(column)
    return commit({ forward: [order(target, who, row.column, { rank: body.rank, not: body.not.filter(line => line !== never.line) })], backward: [order(target, who, row.column, body)], announce })
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
  /**
   * A workspace situation order (Expert, For everyone): the new order, or null for "Same as the row" (review: automatic).
   * `seen` is the revision on screen when the person opened the cell. Undo writes back the whole order the page showed,
   * its exclusions included, or removes it again.
   */
  async function situation(column: string, key: SituationKey, next: SituationOrder | null, seen: number): Promise<boolean> {
    const state = doc.value
    if (!state || scope.value !== 'default' || !admin.value || !columnOf(column)) return false
    // The order was read from the page at this revision; a refresh in between means it may no longer be what was changed.
    if (state.revision !== seen) { error.value = conflict; return false }
    const shown = state.situations?.[column]?.[key] ?? null
    if (!next && !shown) return false
    const before = situationOrder(situationSteps(shown), shown?.end, shown?.not)
    const write = (body: SituationOrder | null): Step => async revs => ({ ...revs, prefs: advanced(await (body ? putSituationOrder(column, key, body, revs.prefs) : resetSituationOrder(column, key, revs.prefs)), revs.prefs) })
    const current = capture(), done = await commit({ forward: [write(next)], backward: [write(before)] })
    // The server answers with codes; say what they mean.
    if (!done && current()) { const plain = situationFailure(error.value); if (plain) error.value = plain }
    return done
  }
  /** Why the ticket a person clicked runs where it does: its own resolution, read for the identity on screen. A caller
   *  whose link can change passes `still`, so an answer or a failure for a ticket that is no longer asked about is dropped. */
  async function explain(ticket: string, still: () => boolean = () => true): Promise<WhyView | null> {
    const identity = capture(), current = () => identity() && still()
    try {
      const resolution = await getTicketResolution(ticket)
      if (!current()) return null
      if (!resolution || !Array.isArray(resolution.ladder)) throw new Error('invalid resolution')
      return ticketWhy({ ticket, resolution, profiles: profiles.value, entries: entries.value })
    } catch {
      if (current()) error.value = `Couldn’t load why ${ticket} runs on its model. Open it again from the ticket.`
      return null
    }
  }
  function startException(column: string) { if (editable.value && columnOf(column)) draft.value = column }
  function discardException() { draft.value = null }
  function setScope(value: Scope) { if (value === 'default' && !can('model_prefs.manage')) return; scope.value = value }
  function dropError() { error.value = '' }

  watch(key, () => { generation++; epoch.value++; abort?.abort(); doc.value = null; workspace.value = null; tail.value = null; draft.value = null; error.value = ''; failed.value = false; announcement.value = ''; busy.value = false; loading.value = false; if (owner.value && can('models.read')) void load() }, { immediate: true, flush: 'sync' })
  const unsubscribe = onAccessRevalidate({ reset: () => {
    generation++; epoch.value++; abort?.abort(); busy.value = false; loading.value = false
    doc.value = null; workspace.value = null; tail.value = null; draft.value = null; names.value = new Map(); profiles.value = []; error.value = ''; announcement.value = ''
  }, refresh: () => {
    if (scope.value === 'default' && !can('model_prefs.manage')) { scope.value = 'me'; return }
    if (can('models.read') && !busy.value) void load(true)
  } })
  watch(() => can('model_prefs.manage'), allowed => { if (!allowed && scope.value === 'default') scope.value = 'me' })
  watch(() => can('models.read'), allowed => { if (allowed && !doc.value && !loading.value && owner.value) void load() })
  onBeforeUnmount(() => { alive = false; generation++; epoch.value++; abort?.abort(); unsubscribe() })

  return { scope, doc, workspace, rows, names, entries, routes, labels, kinds, freeKinds, next, news, tail, loading, busy, error, failed, announcement, draft, admin, editable, readable, owner, actionKey, cantReason, load, pick, use, clear, lock, dismiss, editChain, fixedSteps, canAllow, allowAgain, explain, situation, startException, discardException, setScope, dropError }
}
