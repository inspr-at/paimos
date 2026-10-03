// SPDX-License-Identifier: AGPL-3.0-only
//
// Theme editor: one explicit state model for every theme mutation and
// lifecycle event (AEON-642). Components never change theme state directly;
// they send events and render the selectors below.
//
// Shape
//   transition(state, event) is a pure reducer. It is the only place that
//   decides what an event does, and it is what the table-driven unit test
//   (tests/theme-editor.unit.test.ts) exercises across every state.
//   createThemeEditor() wraps it for Vue: it holds the state, feeds identity
//   and permission changes in as events, and runs the single pending
//   operation. useThemeEditor() returns one shared editor, so the draft,
//   recoveries and save bar survive navigating away and back, and every card
//   (Themes, Colours, and the Agents card of AEON-643) edits the same draft.
//
// Rules the reducer enforces
//   1. One operation at a time. Starting one (`pending`) takes a fresh token;
//      a result whose token is not the pending one is dropped. Identity changes
//      reset everything, so a late answer for the previous person is ignored.
//   2. Actions bind to the record on screen. Save sends the captured draft and
//      revision; Delete sends the revision captured when the confirmation
//      opened; edit() rejects a draft for another record or revision.
//   3. Conflicts are recoveries per record. A 409 (or 404) stores a recovery
//      under the affected theme id, or under SELECTION for the person's theme
//      choice. Recoveries are never cleared by unrelated work (pagination,
//      another record's success or failure); they resolve only through fresh
//      state for that record: a reload, installing it as the active theme,
//      or a successful write on it. `failure` holds only transient errors and
//      clears when the next operation starts.
//   4. A conflict on the active record or the selection blocks edit, rename
//      and save; a selection conflict also blocks choosing, duplicating and
//      New theme (they would reuse the stale selection revision). Discard
//      resolves an active conflict by reloading.
//   5. Confirmations and rename are reconciled after every event: a delete
//      confirmation closes as soon as its record disappears or changes
//      revision, so a retry always needs a fresh confirmation.
//   6. Reload and re-entering the section refresh only while nothing is
//      unsaved; unsaved edits are never replaced behind the person's back.
//   7. Personal themes need profile write; the workspace default and
//      workspace themes need settings.manage. The default is never deletable.
//
// Adopting it (AEON-643): read `draft`, gate controls with canEdit, and send
// changes with editor.update(draft => { draft.values.agents.ring = 'pulse' }).
// The shared save bar then saves or discards them with the rest of the theme.

import { computed, effectScope, shallowRef, watch, type EffectScope } from 'vue'
import { useSession } from './session'
import { can } from '../lib/authz'
import * as themes from '../lib/themes'
import type { ActiveTheme, ThemeRecord, ThemesPage } from '../lib/themes'

/** Recovery key for the person's theme choice (the selection revision). */
export const SELECTION = '#selection'

export interface Rights { selfWrite: boolean; manage: boolean }
export interface Recovery { key: string; operation: 'selection' | 'save' | 'delete' | 'duplicate'; feedback: string }
export interface Confirmation { id: string; revision: number; name: string }

export type Operation =
  | { kind: 'load' }
  | { kind: 'more'; after: string }
  | { kind: 'choose'; themeID: string; selectionRevision: number }
  | { kind: 'save'; theme: ThemeRecord }
  | { kind: 'readDefault'; themeID: string; selectionRevision: number }
  | { kind: 'duplicate'; source: ThemeRecord; name: string; selectionRevision: number }
  | { kind: 'selectCopy'; copy: ThemeRecord; selectionRevision: number }
  | { kind: 'delete'; target: Confirmation; wasActive: boolean }
  | { kind: 'fallback' }
export interface Pending { token: number; op: Operation }

export interface ThemeEditorState {
  /** `tenant/principal`; empty when signed out. */
  identity: string
  rights: Rights
  items: ThemeRecord[]
  cursor: string | null
  /** The last server-confirmed active theme and selection revision. */
  active: ActiveTheme | null
  /** The working copy of active.theme; equal to it unless there are unsaved edits. */
  draft: ThemeRecord | null
  pending: Pending | null
  /** Last token handed out; never reused, even across identities. */
  token: number
  recoveries: Recovery[]
  failure: string
  message: string
  confirming: Confirmation | null
  renaming: string | null
}

export type ThemeEvent =
  | { type: 'identity'; identity: string }
  | { type: 'rights'; rights: Rights }
  | { type: 'enter' }
  | { type: 'leave' }
  | { type: 'reload' }
  | { type: 'more' }
  | { type: 'choose'; id: string }
  | { type: 'edit'; draft: ThemeRecord }
  | { type: 'rename'; id: string }
  | { type: 'renameEnd' }
  | { type: 'save' }
  | { type: 'discard' }
  | { type: 'duplicate'; id: string }
  | { type: 'newTheme' }
  | { type: 'confirmDelete'; id: string }
  | { type: 'cancelDelete' }
  | { type: 'delete' }
  | { type: 'resolved'; token: number; result: unknown }
  | { type: 'failed'; token: number; status: number | null; message: string }

export const initialState = (): ThemeEditorState => ({
  identity: '', rights: { selfWrite: false, manage: false }, items: [], cursor: null, active: null, draft: null,
  pending: null, token: 0, recoveries: [], failure: '', message: '', confirming: null, renaming: null,
})

const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T
const content = (theme: ThemeRecord) => JSON.stringify({ name: theme.name, values: theme.values })
const recoveryFor = (state: ThemeEditorState, key: string) => state.recoveries.find(item => item.key === key)
const stale = (status: number | null) => status === 409 || status === 404

// ---- Selectors: everything the UI may ask. All are pure. -----------------

export const isBusy = (state: ThemeEditorState) => state.pending !== null
export const isDirty = (state: ThemeEditorState) => !!state.draft && !!state.active && content(state.draft) !== content(state.active.theme)
export const isValidName = (name: string) => !!name.trim() && [...name.trim()].length <= 80 && !/[\u0000-\u001f\u007f]/.test(name)
export const isDefault = (state: ThemeEditorState, theme: ThemeRecord) => theme.scope === 'default' || theme.id === state.active?.default_theme_id
export const isEditable = (state: ThemeEditorState, theme: ThemeRecord) => theme.scope === 'personal' ? state.rights.selfWrite : state.rights.manage
/** The active record or the person's selection has an unresolved conflict. */
export const isConflicted = (state: ThemeEditorState) => !!state.active && (!!recoveryFor(state, state.active.theme.id) || !!recoveryFor(state, SELECTION))
const ready = (state: ThemeEditorState) => !!state.active && !state.pending
const canWriteSelection = (state: ThemeEditorState) => ready(state) && !isDirty(state) && state.rights.selfWrite && !recoveryFor(state, SELECTION)
export const canChoose = canWriteSelection
export const canCreate = canWriteSelection
export const canDuplicate = (state: ThemeEditorState, theme: ThemeRecord) => canWriteSelection(state) && !recoveryFor(state, theme.id)
export const canEdit = (state: ThemeEditorState) => !!state.draft && ready(state) && isEditable(state, state.draft) && !isConflicted(state)
export const canRename = (state: ThemeEditorState, theme: ThemeRecord) => canEdit(state) && state.draft!.id === theme.id
export const canSave = (state: ThemeEditorState) => canEdit(state) && isDirty(state) && isValidName(state.draft!.name)
/** Whether a Delete action exists for this row at all. */
export const offersDelete = (state: ThemeEditorState, theme: ThemeRecord) => isEditable(state, theme) && !isDefault(state, theme)
export const canConfirmDelete = (state: ThemeEditorState, theme: ThemeRecord) => offersDelete(state, theme) && ready(state) && !isDirty(state) && !recoveryFor(state, theme.id)
export const canDelete = (state: ThemeEditorState) => {
  const target = state.confirming && state.items.find(item => item.id === state.confirming!.id)
  return !!target && target.revision === state.confirming!.revision && canConfirmDelete(state, target)
}
export const canReload = (state: ThemeEditorState) => !!state.identity && !state.pending && !isDirty(state)
/** Feedback for the status line: a transient failure, else the most relevant unresolved recovery. */
export function feedback(state: ThemeEditorState) {
  if (state.failure) return state.failure
  const own = state.active ? recoveryFor(state, state.active.theme.id) : undefined
  return (own ?? recoveryFor(state, SELECTION) ?? state.recoveries[state.recoveries.length - 1])?.feedback ?? ''
}
export function ownership(state: ThemeEditorState, theme: ThemeRecord) {
  const label = isDefault(state, theme) ? 'Workspace default' : theme.scope === 'workspace' ? 'Workspace' : 'Yours'
  return { label, access: theme.scope === 'personal' ? null : isEditable(state, theme) ? 'you manage it' : 'read-only' }
}

// ---- Reducer ---------------------------------------------------------------

function start(state: ThemeEditorState, op: Operation): ThemeEditorState {
  const token = state.token + 1
  return { ...state, token, pending: { token, op }, failure: '', message: '' }
}
function merge(items: ThemeRecord[], theme: ThemeRecord) {
  const index = items.findIndex(item => item.id === theme.id)
  return index < 0 ? [...items, clone(theme)] : items.map((item, at) => at === index ? clone(theme) : item)
}
const without = (recoveries: Recovery[], ...keys: string[]) => recoveries.filter(item => !keys.includes(item.key))
const recover = (recoveries: Recovery[], recovery: Recovery) => [...without(recoveries, recovery.key), recovery]
/** Fresh active state resolves that record's and the selection's recovery. */
function install(state: ThemeEditorState, fresh: ActiveTheme, message: string): ThemeEditorState {
  const notice = fresh.fallback_notice ? `${fresh.fallback_notice.deleted_theme_name} was deleted. You are using the workspace default.` : ''
  return {
    ...state, pending: null, active: clone(fresh), draft: clone(fresh.theme), items: merge(state.items, fresh.theme),
    recoveries: without(state.recoveries, fresh.theme.id, SELECTION), failure: '', message: notice || message, renaming: null,
  }
}
/** Drop a confirmation or rename that no longer matches the record on screen. */
function reconcile(state: ThemeEditorState): ThemeEditorState {
  let next = state
  if (next.confirming) {
    const target = next.items.find(item => item.id === next.confirming!.id)
    if (!target || target.revision !== next.confirming.revision || !offersDelete(next, target) || isDirty(next)) next = { ...next, confirming: null }
  }
  if (next.renaming && (next.draft?.id !== next.renaming || !isEditable(next, next.draft) || isConflicted(next))) next = { ...next, renaming: null }
  return next
}

function settle(state: ThemeEditorState, op: Operation, result: unknown): ThemeEditorState {
  const done = { ...state, pending: null }
  switch (op.kind) {
    case 'load': {
      // A full snapshot: every listed record and the selection are fresh.
      const { page, active } = result as { page: ThemesPage; active: ActiveTheme }
      return install({ ...done, items: clone(page.items), cursor: page.next_cursor, recoveries: [] }, active, state.message)
    }
    case 'more': {
      // Pagination only adds rows. It never touches the active record, its
      // draft or any recovery.
      const page = result as ThemesPage
      return { ...done, items: page.items.reduce(merge, state.items), cursor: page.next_cursor }
    }
    case 'choose': return install(done, result as ActiveTheme, '')
    case 'save': {
      const saved = result as ThemeRecord
      return { ...done, items: merge(state.items, saved), active: { ...state.active!, theme: clone(saved) }, draft: clone(saved), recoveries: without(state.recoveries, saved.id), message: 'Saved.' }
    }
    case 'readDefault': {
      const source = result as ThemeRecord
      return start({ ...done, items: merge(state.items, source) }, { kind: 'duplicate', source: clone(source), name: copyName(source), selectionRevision: op.selectionRevision })
    }
    case 'duplicate': {
      const copy = result as ThemeRecord
      return start({ ...done, items: merge(state.items, copy) }, { kind: 'selectCopy', copy: clone(copy), selectionRevision: op.selectionRevision })
    }
    case 'selectCopy': return install(done, result as ActiveTheme, 'Duplicated.')
    case 'delete': {
      const next = { ...done, items: state.items.filter(item => item.id !== op.target.id), recoveries: without(state.recoveries, op.target.id), confirming: null, message: 'Deleted.' }
      // The person's choice falls back on the server; read it before editing again.
      return op.wasActive ? { ...start({ ...next, active: null, draft: null }, { kind: 'fallback' }), message: 'Deleted.' } : next
    }
    case 'fallback': return install(done, result as ActiveTheme, 'Deleted. You are using the workspace default.')
  }
}

function fail(state: ThemeEditorState, op: Operation, status: number | null, message: string): ThemeEditorState {
  const done = { ...state, pending: null }
  const reason = message || 'The theme operation failed. Please try again.'
  const recovery = (key: string, operation: Recovery['operation'], text: string) => ({ ...done, recoveries: recover(state.recoveries, { key, operation, feedback: text }) })
  switch (op.kind) {
    case 'choose':
      if (status === 409) return recovery(SELECTION, 'selection', 'Your theme choice changed elsewhere. Reload themes, then choose again.')
      if (status === 404) return recovery(op.themeID, 'selection', `${nameOf(state, op.themeID)} no longer exists. Reload themes.`)
      break
    case 'save':
      if (stale(status)) return recovery(op.theme.id, 'save', status === 404 ? `${op.theme.name} no longer exists. Discard your edits to continue.` : `${op.theme.name} changed elsewhere. Discard your edits to load the current version.`)
      break
    case 'readDefault': return { ...done, failure: `The workspace default could not be read. ${reason}` }
    case 'duplicate':
      if (stale(status)) return recovery(op.source.id, 'duplicate', status === 404 ? `${op.source.name} no longer exists. Reload themes.` : `${op.source.name} changed elsewhere. Reload themes, then duplicate it again.`)
      break
    case 'selectCopy':
      // The copy exists either way; never let the failure hide that.
      if (status === 409) return recovery(SELECTION, 'selection', `The copy was created, but could not be selected: your theme choice changed elsewhere. Reload themes, then choose ${op.copy.name}.`)
      return { ...done, failure: `The copy was created, but could not be selected. ${reason}` }
    case 'delete':
      // The confirmation stays open to show what failed; Delete stays
      // disabled until a reload brings the record back fresh.
      if (stale(status)) return recovery(op.target.id, 'delete', status === 404 ? `${op.target.name} was already deleted elsewhere. Reload themes.` : `${op.target.name} changed elsewhere. Reload themes, then delete it again if you still want to.`)
      break
    case 'fallback': return { ...done, failure: 'Deleted, but your current theme could not be loaded. Reload themes.' }
  }
  return { ...done, failure: reason }
}

const copyName = (theme: ThemeRecord) => `${[...theme.name].slice(0, 73).join('')} copy`
const nameOf = (state: ThemeEditorState, id: string) => state.items.find(item => item.id === id)?.name ?? 'This theme'

function apply(state: ThemeEditorState, event: ThemeEvent): ThemeEditorState {
  switch (event.type) {
    case 'identity': {
      if (event.identity === state.identity) return state
      const fresh: ThemeEditorState = { ...initialState(), identity: event.identity, rights: state.rights, token: state.token }
      return event.identity ? start(fresh, { kind: 'load' }) : fresh
    }
    case 'rights': return { ...state, rights: { ...event.rights } }
    case 'enter':
    case 'reload': return canReload(state) ? start({ ...state, confirming: null }, { kind: 'load' }) : state
    case 'leave': return state.confirming || state.renaming ? { ...state, confirming: null, renaming: null } : state
    case 'more': return state.cursor && !state.pending && state.identity ? start(state, { kind: 'more', after: state.cursor }) : state
    case 'choose':
      if (!canChoose(state) || event.id === state.active!.theme.id || !state.items.some(item => item.id === event.id)) return state
      return start(state, { kind: 'choose', themeID: event.id, selectionRevision: state.active!.revision })
    case 'edit': {
      const draft = state.draft
      if (!canEdit(state) || !draft || event.draft.id !== draft.id || event.draft.revision !== draft.revision) return state
      return { ...state, draft: { ...clone(event.draft), scope: draft.scope, owner_principal_id: draft.owner_principal_id, tenant_id: draft.tenant_id }, message: '' }
    }
    case 'rename': {
      const theme = state.items.find(item => item.id === event.id)
      return theme && canRename(state, theme) ? { ...state, renaming: event.id } : state
    }
    case 'renameEnd': return state.renaming ? { ...state, renaming: null } : state
    case 'save': return canSave(state) ? start(state, { kind: 'save', theme: clone(state.draft!) }) : state
    case 'discard': {
      if (!ready(state) || !(isDirty(state) || isConflicted(state))) return state
      const reset = { ...state, draft: clone(state.active!.theme), renaming: null, failure: '', message: 'Discarded.' }
      // Discarding is the way out of a conflict on the active record.
      return isConflicted(state) ? { ...start(reset, { kind: 'load' }), message: 'Discarded.' } : reset
    }
    case 'duplicate': {
      const source = state.items.find(item => item.id === event.id)
      if (!source || !canDuplicate(state, source)) return state
      return start(state, { kind: 'duplicate', source: clone(source), name: copyName(source), selectionRevision: state.active!.revision })
    }
    case 'newTheme':
      // Read the default fresh (it may sit on a later page), then duplicate it.
      return canCreate(state) ? start(state, { kind: 'readDefault', themeID: state.active!.default_theme_id, selectionRevision: state.active!.revision }) : state
    case 'confirmDelete': {
      const theme = state.items.find(item => item.id === event.id)
      return theme && canConfirmDelete(state, theme) ? { ...state, confirming: { id: theme.id, revision: theme.revision, name: theme.name } } : state
    }
    case 'cancelDelete': return state.confirming && !state.pending ? { ...state, confirming: null } : state
    case 'delete':
      if (!canDelete(state)) return state
      return start(state, { kind: 'delete', target: { ...state.confirming! }, wasActive: state.active!.theme.id === state.confirming!.id })
    case 'resolved':
      return state.pending?.token === event.token ? settle(state, state.pending.op, event.result) : state
    case 'failed':
      return state.pending?.token === event.token ? fail(state, state.pending.op, event.status, event.message) : state
  }
}

/** The theme editor's pure transition function. Unchanged state is returned as the same object. */
export function transition(state: ThemeEditorState, event: ThemeEvent): ThemeEditorState {
  const next = apply(state, event)
  return next === state ? state : reconcile(next)
}

// ---- Vue runner ------------------------------------------------------------

function execute(op: Operation): Promise<unknown> {
  switch (op.kind) {
    case 'load': return Promise.all([themes.listThemes(), themes.getActiveTheme()]).then(([page, active]) => ({ page, active }))
    case 'more': return themes.listThemes(op.after)
    case 'choose': return themes.selectTheme(op.themeID, op.selectionRevision)
    case 'save': return themes.updateTheme(op.theme)
    case 'readDefault': return themes.getTheme(op.themeID)
    case 'duplicate': return themes.duplicateTheme(op.source, op.name)
    case 'selectCopy': return themes.selectTheme(op.copy.id, op.selectionRevision)
    case 'delete': return themes.deleteTheme(op.target)
    case 'fallback': return themes.getActiveTheme()
  }
}

function freeze<T>(value: T): T {
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    Object.freeze(value)
    Object.values(value).forEach(freeze)
  }
  return value
}

/**
 * A theme editor bound to the current session. Every method sends one event;
 * the returned promise settles when the operation it started (including
 * follow-up stages such as selecting a fresh copy) has finished.
 */
export function createThemeEditor() {
  const session = useSession()
  // State is frozen: writing to `draft` directly throws instead of silently
  // bypassing the model. Change it with edit() or update().
  const state = shallowRef<ThemeEditorState>(freeze(initialState()))
  let waiters: (() => void)[] = []
  function dispatch(event: ThemeEvent): boolean {
    const before = state.value, after = transition(before, event)
    if (after === before) return false
    state.value = freeze(after)
    if (after.pending && after.pending !== before.pending) void run(after.pending)
    if (!after.pending) { const ready = waiters; waiters = []; ready.forEach(resolve => resolve()) }
    return true
  }
  async function run(pending: Pending) {
    try { dispatch({ type: 'resolved', token: pending.token, result: await execute(pending.op) }) }
    catch (caught) {
      const status = (caught as { status?: unknown } | null)?.status
      dispatch({ type: 'failed', token: pending.token, status: typeof status === 'number' ? status : null, message: caught instanceof Error ? caught.message : '' })
    }
  }
  /** Resolves once no operation is pending. */
  const idle = () => state.value.pending ? new Promise<void>(resolve => waiters.push(resolve)) : Promise.resolve()
  const send = (event: ThemeEvent) => { dispatch(event); return idle() }

  const identity = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
  const rights = computed<Rights>(() => {
    const person = session.identity?.principal.kind === 'person'
    return { selfWrite: person && (can('profile.write') || can('profile.portal_write')), manage: person && can('settings.manage') }
  })
  watch(rights, value => dispatch({ type: 'rights', rights: value }), { immediate: true })
  watch(identity, value => dispatch({ type: 'identity', identity: value }), { immediate: true })

  const view = <T>(select: (current: ThemeEditorState) => T) => computed(() => select(state.value))
  return {
    /** The whole state, read-only, for diagnostics and tests. */
    state: computed(() => state.value),
    items: view(current => current.items),
    cursor: view(current => current.cursor),
    active: view(current => current.active),
    draft: view(current => current.draft),
    confirming: view(current => current.confirming),
    renaming: view(current => current.renaming),
    message: view(current => current.message),
    busy: view(isBusy),
    dirty: view(isDirty),
    valid: view(current => !!current.draft && isValidName(current.draft.name)),
    conflict: view(isConflicted),
    error: view(feedback),
    selfWrite: view(current => current.rights.selfWrite),
    canEdit: view(canEdit),
    canSave: view(canSave),
    canCreate: view(canCreate),
    canChoose: view(canChoose),
    canDelete: view(canDelete),
    canReload: view(canReload),
    editable: (theme: ThemeRecord) => isEditable(state.value, theme),
    canDuplicate: (theme: ThemeRecord) => canDuplicate(state.value, theme),
    canRename: (theme: ThemeRecord) => canRename(state.value, theme),
    offersDelete: (theme: ThemeRecord) => offersDelete(state.value, theme),
    canConfirmDelete: (theme: ThemeRecord) => canConfirmDelete(state.value, theme),
    ownership: (theme: ThemeRecord) => ownership(state.value, theme),

    /** The section became visible: refresh unless edits are unsaved. */
    enter: () => send({ type: 'enter' }),
    /** The section is hidden: close transient UI, keep the draft. */
    leave: () => { dispatch({ type: 'leave' }) },
    load: () => send({ type: 'reload' }),
    more: () => send({ type: 'more' }),
    choose: (theme: ThemeRecord) => send({ type: 'choose', id: theme.id }),
    /** Replace the draft with an edited copy of the same record and revision. */
    edit: (draft: ThemeRecord) => { dispatch({ type: 'edit', draft }) },
    /** Edit a mutable copy of the draft. */
    update: (recipe: (draft: ThemeRecord) => void) => {
      const current = state.value.draft
      if (!current) return
      const copy = clone(current); recipe(copy); dispatch({ type: 'edit', draft: copy })
    },
    rename: (theme: ThemeRecord) => { dispatch({ type: 'rename', id: theme.id }) },
    renameEnd: () => { dispatch({ type: 'renameEnd' }) },
    save: () => send({ type: 'save' }),
    discard: () => send({ type: 'discard' }),
    duplicate: (theme: ThemeRecord) => send({ type: 'duplicate', id: theme.id }),
    newTheme: () => send({ type: 'newTheme' }),
    confirmDelete: (theme: ThemeRecord) => { dispatch({ type: 'confirmDelete', id: theme.id }) },
    cancelDelete: () => { dispatch({ type: 'cancelDelete' }) },
    remove: () => send({ type: 'delete' }),
    idle,
  }
}
export type ThemeEditor = ReturnType<typeof createThemeEditor>

let shared: { scope: EffectScope; editor: ThemeEditor } | null = null
/** The shared editor for the signed-in person (see the header comment). */
export function useThemeEditor(): ThemeEditor {
  if (!shared) {
    const scope = effectScope(true)
    shared = { scope, editor: scope.run(createThemeEditor)! }
  }
  return shared.editor
}
