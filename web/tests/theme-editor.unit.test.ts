// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive } from 'vue'
import type { ActiveTheme, ThemeRecord, ThemesPage } from '../src/lib/themes'
const mocks = vi.hoisted(() => ({ session: { identity: null as unknown }, permissions: new Set<string>() }))
vi.mock('../src/stores/session', () => ({ useSession: () => mocks.session }))
vi.mock('../src/lib/authz', () => ({ can: (permission: string) => mocks.permissions.has(permission) }))
vi.mock('../src/lib/themes', () => ({ listThemes: vi.fn(), getActiveTheme: vi.fn(), getTheme: vi.fn(), selectTheme: vi.fn(), updateTheme: vi.fn(), duplicateTheme: vi.fn(), deleteTheme: vi.fn() }))
import * as api from '../src/lib/themes'
import { themeCss } from '../src/lib/themeEngine'
import { agentTheme, resetAgentTheme, restoreAgentTheme } from '../src/lib/agentTheme'
import {
  ERROR_CLASSES, EVENT_TYPES, OPERATION_KINDS, PHASES, SELECTION, canChoose, canCreate, canDelete, canDuplicate, canEdit, canLoadMore, canReload, canSave,
  createThemeEditor, errorClass, feedback, initialState, isConflicted, isDirty, notices, phase, transition, useThemeEditor,
  type ErrorClass, type Operation, type Recovery, type Rights, type ThemeEditorState, type ThemeEvent, type Unfinished,
} from '../src/stores/themeEditor'

// ---- Fixtures ----------------------------------------------------------------

const record = (id: string, name: string, scope: ThemeRecord['scope'], revision: number): ThemeRecord => ({
  id, name, scope, revision, tenant_id: 'tenant', owner_principal_id: scope === 'personal' ? 'person' : null, created_at: '', updated_at: '',
  values: { primary: { light: '#0e6f6c', dark: null }, secondary: { light: '#b5642a', dark: null }, recurring_marker: { source: 'secondary', custom: null }, agents: { avatar: 'robot-5', ring: 'still', hover: true, size: 90, palette: 'deutan' } },
})
const DEFAULT = record('default', 'Porcelain', 'default', 2)
const CONTRAST = record('contrast', 'High contrast', 'workspace', 3)
const COPPER = record('copper', 'Copper', 'personal', 4)
const OTHER = record('other', 'Other', 'personal', 9)
const COPY = record('copy', 'High contrast copy', 'personal', 1)
const LATER = record('later', 'Later', 'workspace', 1)
const chosen = (theme: ThemeRecord, revision = 12): ActiveTheme => ({ theme, default_theme_id: 'default', selected_theme_id: theme.id === 'default' ? null : theme.id, revision, fallback_notice: null })
const MEMBER: Rights = { selfWrite: true, manage: false }, MANAGER: Rights = { selfWrite: true, manage: true }, READER: Rights = { selfWrite: false, manage: false }
const ME = 'tenant/person'
// The detached shared editor observes this one session, just as in the app.
mocks.session = reactive({ identity: null })
const conflictOn = (key: string, operation: Recovery['operation'] = 'delete'): Recovery => ({ key, operation, feedback: `${key} changed elsewhere` })
const named = (theme: ThemeRecord, name: string) => ({ ...theme, name })
const recoloured = (theme: ThemeRecord, light = '#3a5fc4') => ({ ...theme, values: { ...theme.values, primary: { light, dark: null } } })

const loaded = (patch: Partial<ThemeEditorState> = {}): ThemeEditorState => ({
  ...initialState(), identity: ME, rights: MEMBER, items: [DEFAULT, CONTRAST, COPPER, OTHER], cursor: 'next', active: chosen(COPPER), draft: COPPER, token: 300, ...patch,
})
const waiting = (token: number, op: Operation, patch: Partial<ThemeEditorState> = {}) => loaded({ token, pending: { token, op }, ...patch })
const target = (theme: ThemeRecord) => ({ id: theme.id, revision: theme.revision, name: theme.name })

const UNFINISHED_COPY: Unfinished = { id: 'copy', name: 'High contrast copy', feedback: 'High contrast copy was created, but could not be selected.' }
const WITH_COPY = [DEFAULT, CONTRAST, COPPER, OTHER, COPY]

// ---- States -------------------------------------------------------------------
// Coverage is checked against the model's own enums below: every PHASES entry
// needs a fixture, every OPERATION_KINDS entry a pending fixture.

const IDLE = {
  signedOut: initialState(),
  loadFailed: { ...initialState(), identity: ME, rights: MEMBER, token: 300, failure: 'Request failed' },
  clean: loaded(),
  dirty: loaded({ draft: named(COPPER, 'Copper edited') }),
  invalid: loaded({ draft: named(COPPER, '   ') }),
  reader: loaded({ rights: READER }),
  defaultMember: loaded({ active: chosen(DEFAULT), draft: DEFAULT }),
  defaultManager: loaded({ rights: MANAGER, active: chosen(DEFAULT), draft: DEFAULT }),
  renaming: loaded({ renaming: 'copper' }),
  confirming: loaded({ confirming: target(OTHER) }),
  activeConflict: loaded({ recoveries: [conflictOn('copper')] }),
  dirtyConflict: loaded({ draft: named(COPPER, 'Copper edited'), recoveries: [conflictOn('copper', 'save')] }),
  selectionConflict: loaded({ recoveries: [conflictOn(SELECTION, 'selection')] }),
  otherConflict: loaded({ recoveries: [conflictOn('other')] }),
  confirmingConflict: loaded({ confirming: target(OTHER), recoveries: [conflictOn('other')] }),
  createConflict: loaded({ recoveries: [conflictOn('default', 'create')] }),
  unfinished: loaded({ items: WITH_COPY, unfinished: [UNFINISHED_COPY] }),
  unfinishedMissing: loaded({ items: WITH_COPY, unfinished: [UNFINISHED_COPY], recoveries: [conflictOn('copy', 'selection')] }),
  lastPage: loaded({ cursor: null }),
  fallbackFailed: loaded({ active: null, draft: null, items: [DEFAULT, CONTRAST, OTHER], failure: 'Deleted, but your current theme could not be loaded. Reload themes.' }),
} satisfies Record<string, ThemeEditorState>

// Pending states own unique tokens spaced ten apart, so a result event matches
// exactly one of them and `token - 1` belongs to no state (a stale answer).
const PENDING = {
  loading: { ...initialState(), identity: ME, rights: MEMBER, token: 10, pending: { token: 10, op: { kind: 'load' } } },
  reloading: waiting(20, { kind: 'load' }, { items: WITH_COPY, unfinished: [UNFINISHED_COPY], recoveries: [conflictOn('copper'), conflictOn('other'), conflictOn(SELECTION, 'selection')] }),
  paging: waiting(30, { kind: 'more', after: 'next' }, { confirming: target(OTHER), recoveries: [conflictOn('copper')] }),
  choosing: waiting(40, { kind: 'choose', themeID: 'other', selectionRevision: 12 }, { recoveries: [conflictOn('copper'), conflictOn('other')] }),
  saving: waiting(50, { kind: 'save', theme: named(COPPER, 'Copper edited') }, { draft: named(COPPER, 'Copper edited'), recoveries: [conflictOn('other')] }),
  readingDefault: waiting(60, { kind: 'readDefault', themeID: 'default', selectionRevision: 12 }, { recoveries: [conflictOn('copper')] }),
  duplicating: waiting(70, { kind: 'duplicate', source: CONTRAST, name: 'High contrast copy', selectionRevision: 12 }),
  selectingCopy: waiting(80, { kind: 'selectCopy', copy: COPY, selectionRevision: 12 }, { items: WITH_COPY }),
  // review-642e 2: the active record is already conflicted when the copy's selection fails.
  selectingCopyConflicted: waiting(90, { kind: 'selectCopy', copy: COPY, selectionRevision: 12 }, { items: WITH_COPY, recoveries: [conflictOn('copper')] }),
  deletingOther: waiting(100, { kind: 'delete', target: target(OTHER), wasActive: false }, { confirming: target(OTHER), recoveries: [conflictOn('copper')] }),
  deletingActive: waiting(110, { kind: 'delete', target: target(COPPER), wasActive: true }, { confirming: target(COPPER) }),
  fallback: waiting(120, { kind: 'fallback' }, { active: null, draft: null, items: [DEFAULT, CONTRAST, OTHER], message: 'Deleted.' }),
} satisfies Record<string, ThemeEditorState>

const STATES: Record<string, ThemeEditorState> = { ...IDLE, ...PENDING }
type StateName = keyof typeof IDLE | keyof typeof PENDING
type PendingName = keyof typeof PENDING

// ---- Expectations -------------------------------------------------------------

type Check = (after: ThemeEditorState, before: ThemeEditorState) => void
const keys = (state: ThemeEditorState) => state.recoveries.map(item => item.key).sort()
const starts = (kind: Operation['kind'], extra: (op: Operation, after: ThemeEditorState, before: ThemeEditorState) => void = () => {}): Check => (after, before) => {
  expect(after.pending?.op.kind).toBe(kind)
  expect(after.pending!.token).toBe(before.token + 1)
  expect(after.failure).toBe('')
  // Starting work never resolves a recovery or unfinished work by itself.
  expect(keys(after)).toEqual(keys(before)); expect(after.unfinished).toEqual(before.unfinished)
  extra(after.pending!.op, after, before)
}
const resets = (identity: string): Check => (after, before) => {
  expect(after.identity).toBe(identity)
  expect([after.items, after.active, after.draft, after.recoveries, after.unfinished, after.confirming, after.renaming, after.failure, after.message]).toEqual([[], null, null, [], [], null, null, '', ''])
  expect(after.token).toBeGreaterThanOrEqual(before.token)
  if (identity) expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'load' } })
  else expect(after.pending).toBeNull()
}
const each = (names: StateName[], check: Check) => Object.fromEntries(names.map(name => [name, check])) as Partial<Record<StateName, Check>>
const ALL = Object.keys(STATES) as StateName[]
const except = (...names: StateName[]) => ALL.filter(name => !names.includes(name))
const LOADED_EXTRAS: StateName[] = ['createConflict', 'unfinished', 'unfinishedMissing']
// States that may choose, duplicate or create: loaded, nothing pending or unsaved, no selection conflict.
const SELECTABLE: StateName[] = ['clean', 'renaming', 'confirming', 'activeConflict', 'otherConflict', 'confirmingConflict', 'lastPage', 'defaultMember', 'defaultManager', ...LOADED_EXTRAS]
const EDITABLE_COPPER: StateName[] = ['clean', 'dirty', 'invalid', 'renaming', 'confirming', 'otherConflict', 'confirmingConflict', 'lastPage', ...LOADED_EXTRAS]
const IDLE_CLEAN: StateName[] = ['clean', 'reader', 'defaultMember', 'defaultManager', 'renaming', 'confirming', 'activeConflict', 'selectionConflict', 'otherConflict', 'confirmingConflict', 'lastPage', 'fallbackFailed', 'loadFailed', ...LOADED_EXTRAS]
const result = (token: number, value: unknown): ThemeEvent => ({ type: 'resolved', token, result: value })
const failed = (token: number, status: number | null, message = 'Request failed'): ThemeEvent => ({ type: 'failed', token, status, message })

// Each input event lists the states that accept it and what must hold
// afterwards. Every other state must return the very same object: the event
// is ignored. Results (resolved, failed) are generated further down.
const EVENTS: Record<string, { event: ThemeEvent; accept: Partial<Record<StateName, Check>> }> = {
  // Identity and permissions
  'identity changes': { event: { type: 'identity', identity: 'tenant/someone' }, accept: each(ALL, resets('tenant/someone')) },
  'sign out': { event: { type: 'identity', identity: '' }, accept: each(except('signedOut'), resets('')) },
  'same identity': { event: { type: 'identity', identity: ME }, accept: { signedOut: resets(ME) } },
  'session restoration': { event: { type: 'restore', identity: ME }, accept: each([...IDLE_CLEAN, 'loading', 'reloading'], starts('load')) },
  'restoration for another identity': { event: { type: 'restore', identity: 'tenant/elsewhere' }, accept: {} },
  'permission lost': { event: { type: 'rights', rights: READER }, accept: each(ALL, (after, before) => {
    expect(after.rights).toEqual(READER)
    expect(after.renaming).toBeNull(); expect(after.confirming).toBeNull()
    expect(after.draft).toEqual(before.draft); expect(after.pending).toEqual(before.pending)
    expect(canSave(after)).toBe(false); expect(canEdit(after)).toBe(false)
  }) },
  // Lifecycle: navigation away and back, reload, pagination
  'enter section': { event: { type: 'enter' }, accept: each(IDLE_CLEAN, starts('load', (_, after) => expect(after.confirming).toBeNull())) },
  'reload': { event: { type: 'reload' }, accept: each(IDLE_CLEAN, starts('load', (_, after) => expect(after.confirming).toBeNull())) },
  'leave section': { event: { type: 'leave' }, accept: each(['renaming', 'confirming', 'confirmingConflict', 'paging', 'deletingOther', 'deletingActive'], (after, before) => {
    expect(after.confirming).toBeNull(); expect(after.renaming).toBeNull()
    expect([after.draft, after.recoveries, after.pending]).toEqual([before.draft, before.recoveries, before.pending])
  }) },
  // Pagination needs a loaded editor; without an active theme Reload is the way on.
  'load more': { event: { type: 'more' }, accept: each(['clean', 'dirty', 'invalid', 'reader', 'defaultMember', 'defaultManager', 'renaming', 'confirming', 'activeConflict', 'dirtyConflict', 'selectionConflict', 'otherConflict', 'confirmingConflict', ...LOADED_EXTRAS], starts('more', (op, after, before) => {
    expect(op).toEqual({ kind: 'more', after: 'next' }); expect(after.draft).toEqual(before.draft); expect(after.confirming).toEqual(before.confirming)
  })) },
  // Selection
  'choose another theme': { event: { type: 'choose', id: 'other' }, accept: each(SELECTABLE, starts('choose', op => expect(op).toEqual({ kind: 'choose', themeID: 'other', selectionRevision: 12 }))) },
  'choose the active theme again': { event: { type: 'choose', id: 'copper' }, accept: each(['defaultMember', 'defaultManager'], starts('choose', op => expect(op).toMatchObject({ themeID: 'copper' }))) },
  'choose the unfinished copy': { event: { type: 'choose', id: 'copy' }, accept: each(['unfinished', 'unfinishedMissing'], starts('choose', op => expect(op).toMatchObject({ themeID: 'copy' }))) },
  'choose an unlisted theme': { event: { type: 'choose', id: 'missing' }, accept: {} },
  // Editing and rename
  'edit the draft': { event: { type: 'edit', draft: recoloured(COPPER) }, accept: each(EDITABLE_COPPER, (after, before) => {
    // The edit carries the whole edited copy of the record on screen.
    expect(after.draft).toEqual(recoloured(COPPER))
    expect(isDirty(after)).toBe(true); expect(after.confirming).toBeNull(); expect(after.recoveries).toEqual(before.recoveries)
  }) },
  'edit tries to change ownership': { event: { type: 'edit', draft: { ...recoloured(COPPER), scope: 'workspace', owner_principal_id: null } }, accept: each(EDITABLE_COPPER, after => {
    expect(after.draft).toMatchObject({ scope: 'personal', owner_principal_id: 'person' })
  }) },
  'edit with a stale revision': { event: { type: 'edit', draft: recoloured({ ...COPPER, revision: 3 }) }, accept: {} },
  'edit another record': { event: { type: 'edit', draft: recoloured(OTHER) }, accept: {} },
  'rename the active theme': { event: { type: 'rename', id: 'copper' }, accept: each(EDITABLE_COPPER, after => expect(after.renaming).toBe('copper')) },
  'rename the default as manager': { event: { type: 'rename', id: 'default' }, accept: { defaultManager: after => expect(after.renaming).toBe('default') } },
  'rename an inactive theme': { event: { type: 'rename', id: 'other' }, accept: {} },
  'finish renaming': { event: { type: 'renameEnd' }, accept: { renaming: after => expect(after.renaming).toBeNull() } },
  // Save and discard
  'save': { event: { type: 'save' }, accept: { dirty: starts('save', op => expect(op).toEqual({ kind: 'save', theme: named(COPPER, 'Copper edited') })) } },
  'discard': { event: { type: 'discard' }, accept: {
    ...each(['dirty', 'invalid'], after => { expect(after.draft).toEqual(COPPER); expect(after.message).toBe('Discarded.'); expect(after.pending).toBeNull() }),
    // Discarding is the way out of an active or selection conflict: it reloads.
    ...each(['activeConflict', 'dirtyConflict', 'selectionConflict'], (after, before) => {
      starts('load')(after, before); expect(after.draft).toEqual(COPPER); expect(after.message).toBe('Discarded.')
    }),
  } },
  // Duplicate and New theme
  'duplicate a workspace theme': { event: { type: 'duplicate', id: 'contrast' }, accept: each(SELECTABLE, starts('duplicate', op => expect(op).toEqual({ kind: 'duplicate', source: CONTRAST, name: 'High contrast copy', selectionRevision: 12 }))) },
  'duplicate a conflicted theme': { event: { type: 'duplicate', id: 'other' }, accept: each(SELECTABLE.filter(name => name !== 'otherConflict' && name !== 'confirmingConflict'), starts('duplicate', op => expect(op).toMatchObject({ source: OTHER }))) },
  // A stale workspace default blocks New theme until a reload.
  'new theme': { event: { type: 'newTheme' }, accept: each(SELECTABLE.filter(name => name !== 'createConflict'), starts('readDefault', op => expect(op).toEqual({ kind: 'readDefault', themeID: 'default', selectionRevision: 12 }))) },
  // Delete with its confirmation
  'ask to delete another theme': { event: { type: 'confirmDelete', id: 'other' }, accept: each(['clean', 'renaming', 'confirming', 'activeConflict', 'selectionConflict', 'lastPage', 'defaultMember', 'defaultManager', ...LOADED_EXTRAS], after => expect(after.confirming).toEqual(target(OTHER))) },
  'ask to delete the active theme': { event: { type: 'confirmDelete', id: 'copper' }, accept: each(['clean', 'renaming', 'confirming', 'selectionConflict', 'otherConflict', 'confirmingConflict', 'lastPage', 'defaultMember', 'defaultManager', ...LOADED_EXTRAS], after => expect(after.confirming).toEqual(target(COPPER))) },
  'ask to delete the unfinished copy': { event: { type: 'confirmDelete', id: 'copy' }, accept: { unfinished: after => expect(after.confirming).toEqual(target(COPY)) } },
  'ask to delete a workspace theme': { event: { type: 'confirmDelete', id: 'contrast' }, accept: { defaultManager: after => expect(after.confirming).toEqual(target(CONTRAST)) } },
  'ask to delete the workspace default': { event: { type: 'confirmDelete', id: 'default' }, accept: {} },
  'keep the theme': { event: { type: 'cancelDelete' }, accept: each(['confirming', 'confirmingConflict'], after => expect(after.confirming).toBeNull()) },
  'delete': { event: { type: 'delete' }, accept: { confirming: starts('delete', op => expect(op).toEqual({ kind: 'delete', target: target(OTHER), wasActive: false })) } },
}

// ---- Outcomes: every pending state × success and every error class -------------

/** A representative status for each class; errorClass() is tested separately. */
const STATUS: Record<ErrorClass, number | null> = { missing: 404, conflict: 409, precondition: 412, rejected: 403, server: 503, network: null }
const reason = (kind: ErrorClass) => kind === 'network' ? 'The theme service could not be reached. Check your connection and try again.' : 'Request failed'

/** A notice must survive unrelated work: a page that arrives or fails. */
function durable(state: ThemeEditorState, text: string) {
  // The claim needs a page that really starts; a state that cannot page proves nothing.
  expect(canLoadMore(state), 'durable() needs a state whose pagination can start').toBe(true)
  const paging = transition(state, { type: 'more' })
  expect(paging.pending?.op.kind).toBe('more')
  for (const end of [result(paging.pending!.token, { items: [LATER], next_cursor: null }), failed(paging.pending!.token, 503)]) {
    const after = transition(paging, end)
    expect(feedback(after)).toContain(text)
    // The way out stays visible: Reload, or Discard while edits are unsaved.
    expect(canReload(after) || isDirty(after)).toBe(true)
  }
}
/** A transient failure: shown, nothing persistent added, recoveries kept. */
const fails = (text: string, extra: Check = () => {}): Check => (after, before) => {
  expect(after.pending).toBeNull(); expect(after.failure).toBe(text); expect(notices(after)).toContain(text)
  expect(keys(after)).toEqual(keys(before))
  extra(after, before)
}
/** A recovery on one record: persistent, shown, and it survives pagination. */
const recovers = (key: string, operation: Recovery['operation'], text: string, extra: Check = () => {}): Check => (after, before) => {
  expect(after.pending).toBeNull(); expect(after.failure).toBe('')
  expect(after.recoveries.find(item => item.key === key)).toEqual({ key, operation, feedback: text })
  expect(keys(after)).toEqual([...new Set([...keys(before), key])].sort())
  expect(notices(after)).toContain(text); durable(after, text)
  extra(after, before)
}
/** The copy exists: that is said first, and survives pagination, whatever else failed. */
const created = (check: Check): Check => (after, before) => {
  check(after, before)
  expect(after.unfinished).toEqual([UNFINISHED_COPY]); expect(after.items.map(item => item.id)).toContain('copy')
  expect(notices(after)[0]).toBe(UNFINISHED_COPY.feedback); durable(after, UNFINISHED_COPY.feedback)
}
/** The same check for every class (reads, which have no record to recover). */
const byClass = (check: (kind: ErrorClass) => Check) => Object.fromEntries(ERROR_CLASSES.map(kind => [kind, check(kind)])) as Record<ErrorClass, Check>
/** A write: stale classes recover the record, the rest are transient. */
const write = (stale: { missing: Check; conflict: Check }, transient: (kind: ErrorClass) => Check): Record<ErrorClass, Check> => ({
  missing: stale.missing, conflict: stale.conflict, precondition: stale.conflict,
  rejected: transient('rejected'), server: transient('server'), network: transient('network'),
})

type Outcomes = { result: unknown; resolved: Check } & Record<ErrorClass, Check>
const OUTCOMES: Record<PendingName, Outcomes> = {
  loading: {
    result: { page: { items: [DEFAULT, CONTRAST, COPPER, OTHER], next_cursor: 'next' }, active: chosen(COPPER) },
    resolved: after => { expect(after.active).toEqual(chosen(COPPER)); expect(after.draft).toEqual(COPPER); expect(after.cursor).toBe('next'); expect(phase(after)).toBe('clean'); expect(feedback(after)).toBe('') },
    ...byClass(kind => fails(reason(kind), after => { expect(after.active).toBeNull(); expect(phase(after)).toBe('unavailable'); expect(canReload(after)).toBe(true) })),
  },
  reloading: {
    result: { page: { items: [DEFAULT, CONTRAST, { ...COPPER, revision: 5 }, { ...OTHER, revision: 10 }], next_cursor: 'later' }, active: chosen({ ...COPPER, revision: 5 }, 13) },
    resolved: after => {
      // A full snapshot resolves every recovery and all unfinished work.
      expect(after.recoveries).toEqual([]); expect(after.unfinished).toEqual([]); expect(after.draft!.revision).toBe(5); expect(after.cursor).toBe('later')
      expect(canEdit(after)).toBe(true); expect(feedback(after)).toBe('')
    },
    ...byClass(kind => fails(reason(kind), after => { expect(keys(after)).toEqual(['#selection', 'copper', 'other']); expect(after.unfinished).toEqual([UNFINISHED_COPY]); expect(notices(after)[0]).toBe(UNFINISHED_COPY.feedback) })),
  },
  paging: {
    result: { items: [LATER, { ...OTHER, revision: 10 }, { ...COPPER, name: 'Copper newer', revision: 5 }], next_cursor: null } satisfies ThemesPage,
    resolved: after => {
      expect(after.items.map(item => item.id)).toEqual(['default', 'contrast', 'copper', 'other', 'later']); expect(after.cursor).toBeNull()
      // A list row may be newer, but the active record and its draft stay as
      // installed, and pagination never resolves a recovery.
      expect(after.draft).toEqual(COPPER); expect(after.active!.theme).toEqual(COPPER); expect(keys(after)).toEqual(['copper'])
      expect(isConflicted(after)).toBe(true); expect(feedback(after)).toBe('copper changed elsewhere')
      // The open confirmation captured revision 9; the row is now 10, so it closes.
      expect(after.confirming).toBeNull()
    },
    ...byClass(kind => fails(reason(kind), after => { expect(after.cursor).toBe('next'); expect(after.confirming).toEqual(target(OTHER)); expect(notices(after)).toContain('copper changed elsewhere') })),
  },
  choosing: {
    result: chosen(OTHER, 13),
    resolved: after => {
      expect(after.active).toEqual(chosen(OTHER, 13)); expect(after.draft).toEqual(OTHER)
      // Fresh state resolves only the installed record; the old record's recovery stays visible.
      expect(keys(after)).toEqual(['copper']); expect(isConflicted(after)).toBe(false); expect(canEdit(after)).toBe(true)
      expect(feedback(after)).toBe('copper changed elsewhere')
    },
    ...write({
      missing: recovers('other', 'selection', 'Other no longer exists. Reload themes.', after => expect(after.active!.theme.id).toBe('copper')),
      conflict: recovers(SELECTION, 'selection', 'Your theme choice changed elsewhere. Reload themes, then choose again.', after => { expect(isConflicted(after)).toBe(true); expect(canChoose(after)).toBe(false) }),
    }, kind => fails(reason(kind), after => expect(canChoose(after)).toBe(true))),
  },
  saving: {
    result: { ...named(COPPER, 'Copper edited'), revision: 5 },
    resolved: after => {
      expect(after.active!.theme.revision).toBe(5); expect(after.draft).toEqual(after.active!.theme); expect(isDirty(after)).toBe(false)
      expect(after.message).toBe('Saved.'); expect(keys(after)).toEqual(['other'])
    },
    ...write({
      missing: recovers('copper', 'save', 'Copper no longer exists. Discard your edits to continue.', after => { expect(isDirty(after)).toBe(true); expect(canSave(after)).toBe(false) }),
      conflict: recovers('copper', 'save', 'Copper changed elsewhere. Discard your edits to load the current version.', after => { expect(isDirty(after)).toBe(true); expect(canSave(after)).toBe(false) }),
    }, kind => fails(reason(kind), after => { expect(isDirty(after)).toBe(true); expect(canSave(after)).toBe(true); expect(after.message).toBe('') })),
  },
  readingDefault: {
    result: { ...DEFAULT, revision: 3 },
    resolved: (after, before) => {
      expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'duplicate', source: { ...DEFAULT, revision: 3 }, name: 'Porcelain copy', selectionRevision: 12 } })
      expect(after.items.find(item => item.id === 'default')!.revision).toBe(3); expect(keys(after)).toEqual(['copper'])
    },
    ...write({
      missing: recovers('default', 'create', 'The workspace default no longer exists. Reload themes, then create a new theme again.', after => { expect(canCreate(after)).toBe(false); expect(canChoose(after)).toBe(true) }),
      conflict: recovers('default', 'create', 'The workspace default changed elsewhere. Reload themes, then create a new theme again.', after => expect(canCreate(after)).toBe(false)),
    }, kind => fails(`The workspace default could not be read. ${reason(kind)}`, after => expect(canCreate(after)).toBe(true))),
  },
  duplicating: {
    result: COPY,
    resolved: (after, before) => {
      expect(after.items.map(item => item.id)).toContain('copy')
      expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'selectCopy', copy: COPY, selectionRevision: 12 } })
    },
    // review-642d 1: duplication conflicts go through the recovery model.
    ...write({
      missing: recovers('contrast', 'duplicate', 'High contrast no longer exists. Reload themes.', after => { expect(canDuplicate(after, CONTRAST)).toBe(false); expect(isConflicted(after)).toBe(false) }),
      conflict: recovers('contrast', 'duplicate', 'High contrast changed elsewhere. Reload themes, then duplicate it again.', after => { expect(canDuplicate(after, CONTRAST)).toBe(false); expect(isConflicted(after)).toBe(false) }),
    }, kind => fails(reason(kind), after => expect(canDuplicate(after, CONTRAST)).toBe(true))),
  },
  selectingCopy: {
    result: chosen(COPY, 13),
    resolved: after => { expect(after.active!.theme.id).toBe('copy'); expect(after.draft).toEqual(COPY); expect(after.message).toBe('Duplicated.'); expect(after.unfinished).toEqual([]) },
    // review-642e 1: every class keeps the copy visible; a 404 records a recovery on it.
    ...write({
      missing: created(recovers('copy', 'selection', 'High contrast copy was deleted elsewhere. Reload themes.', after => expect(isConflicted(after)).toBe(false))),
      conflict: created(recovers(SELECTION, 'selection', 'Your theme choice changed elsewhere. Reload themes, then choose High contrast copy.', after => expect(isConflicted(after)).toBe(true))),
    }, kind => created(fails(reason(kind)))),
  },
  selectingCopyConflicted: {
    result: chosen(COPY, 13),
    resolved: after => { expect(after.active!.theme.id).toBe('copy'); expect(keys(after)).toEqual(['copper']); expect(isConflicted(after)).toBe(false); expect(feedback(after)).toBe('copper changed elsewhere') },
    // review-642e 2: the older conflict never hides that the copy was created.
    ...write({
      missing: created(recovers('copy', 'selection', 'High contrast copy was deleted elsewhere. Reload themes.', after => expect(notices(after)).toContain('copper changed elsewhere'))),
      conflict: created(recovers(SELECTION, 'selection', 'Your theme choice changed elsewhere. Reload themes, then choose High contrast copy.', after => expect(notices(after)).toContain('copper changed elsewhere'))),
    }, kind => created(fails(reason(kind), after => expect(notices(after)).toContain('copper changed elsewhere')))),
  },
  deletingOther: {
    result: undefined,
    resolved: after => {
      expect(after.items.map(item => item.id)).toEqual(['default', 'contrast', 'copper']); expect(after.confirming).toBeNull()
      expect(after.message).toBe('Deleted.'); expect(keys(after)).toEqual(['copper']); expect(isConflicted(after)).toBe(true)
    },
    // review-642d 2: a conflicting delete keeps its confirmation, but Delete stays off until fresh state.
    ...write({
      missing: recovers('other', 'delete', 'Other was already deleted elsewhere. Reload themes.', after => { expect(after.confirming).toEqual(target(OTHER)); expect(canDelete(after)).toBe(false) }),
      conflict: recovers('other', 'delete', 'Other changed elsewhere. Reload themes, then delete it again if you still want to.', after => { expect(after.confirming).toEqual(target(OTHER)); expect(canDelete(after)).toBe(false) }),
    }, kind => fails(reason(kind), after => { expect(after.confirming).toEqual(target(OTHER)); expect(canDelete(after)).toBe(true) })),
  },
  deletingActive: {
    result: undefined,
    resolved: (after, before) => {
      expect([after.active, after.draft, after.confirming]).toEqual([null, null, null]); expect(after.items.map(item => item.id)).not.toContain('copper')
      expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'fallback' } }); expect(after.message).toBe('Deleted.')
    },
    ...write({
      missing: recovers('copper', 'delete', 'Copper was already deleted elsewhere. Reload themes.', after => { expect(isConflicted(after)).toBe(true); expect(canEdit(after)).toBe(false); expect(canDelete(after)).toBe(false) }),
      conflict: recovers('copper', 'delete', 'Copper changed elsewhere. Reload themes, then delete it again if you still want to.', after => { expect(isConflicted(after)).toBe(true); expect(canEdit(after)).toBe(false); expect(canDelete(after)).toBe(false) }),
    }, kind => fails(reason(kind), after => { expect(after.confirming).toEqual(target(COPPER)); expect(canDelete(after)).toBe(true); expect(after.active!.theme.id).toBe('copper') })),
  },
  fallback: {
    result: chosen(DEFAULT, 13),
    resolved: after => { expect(after.active!.theme.id).toBe('default'); expect(after.message).toBe('Deleted. You are using the workspace default.') },
    ...byClass(() => fails('Deleted, but your current theme could not be loaded. Reload themes.', after => { expect(after.active).toBeNull(); expect(canReload(after)).toBe(true); expect(phase(after)).toBe('unavailable') })),
  },
}

function invariants(state: ThemeEditorState) {
  if (state.active || state.draft) expect(state.draft?.id).toBe(state.active?.theme.id)
  if (state.confirming) expect(state.items.find(item => item.id === state.confirming!.id)?.revision).toBe(state.confirming.revision)
  if (state.renaming) expect(state.renaming).toBe(state.draft?.id)
  expect(new Set(state.recoveries.map(item => item.key)).size).toBe(state.recoveries.length)
  expect(new Set(state.unfinished.map(item => item.id)).size).toBe(state.unfinished.length)
  // Nothing persistent is ever hidden from the status line.
  for (const item of [...state.recoveries, ...state.unfinished]) expect(feedback(state)).toContain(item.feedback)
  if (state.failure) expect(feedback(state)).toContain(state.failure)
  if (state.identity && !state.pending && !state.active) expect(feedback(state)).not.toBe('')
  if (isConflicted(state)) expect(canEdit(state) || canSave(state)).toBe(false)
  if (!state.identity) expect([state.items, state.active, state.pending]).toEqual([[], null, null])
}

// ---- Completeness: the matrix is generated from the model's own enums ---------

const OUTCOME_NAMES = ['resolved', ...ERROR_CLASSES] as const
const RESULT_EVENTS: ThemeEvent['type'][] = ['resolved', 'failed']
describe('theme editor matrix is complete', () => {
  it('every phase has a fixture', () => {
    expect([...new Set(Object.values(STATES).map(phase))].sort()).toEqual([...PHASES].sort())
  })
  it('every operation kind has a pending fixture with an outcome table', () => {
    expect([...new Set(Object.values(PENDING).map(state => state.pending!.op.kind))].sort()).toEqual([...OPERATION_KINDS].sort())
    expect(Object.keys(OUTCOMES).sort()).toEqual(Object.keys(PENDING).sort())
  })
  it('every pending fixture covers success and every error class', () => {
    for (const name of Object.keys(PENDING) as PendingName[]) for (const outcome of OUTCOME_NAMES) expect(typeof OUTCOMES[name][outcome], `${name} × ${outcome}`).toBe('function')
  })
  it('every event type has at least one case', () => {
    expect([...new Set([...Object.values(EVENTS).map(item => item.event.type), ...RESULT_EVENTS])].sort()).toEqual([...EVENT_TYPES].sort())
  })
  it('pending tokens are unique and their predecessors belong to no state', () => {
    const tokens = Object.values(PENDING).map(state => state.pending!.token)
    expect(new Set(tokens).size).toBe(tokens.length)
    for (const token of tokens) expect(Object.values(STATES).some(state => state.pending?.token === token - 1)).toBe(false)
  })
  it('errorClass sorts every status into one class', () => {
    const cases: [number | null, ErrorClass][] = [[null, 'network'], [404, 'missing'], [410, 'missing'], [409, 'conflict'], [412, 'precondition'], [400, 'rejected'], [403, 'rejected'], [422, 'rejected'], [500, 'server'], [502, 'server'], [503, 'server']]
    for (const [status, kind] of cases) expect(errorClass(status), String(status)).toBe(kind)
    for (const kind of ERROR_CLASSES) expect(errorClass(STATUS[kind])).toBe(kind)
  })
})

// ---- The matrix: every input event in every state ----------------------------

describe('theme editor state model', () => {
  const cells = Object.keys(STATES).flatMap(state => Object.keys(EVENTS).map(event => [state, event] as const))
  it.each(cells)('%s × %s', (stateName, eventName) => {
    const before = STATES[stateName]!, { event, accept } = EVENTS[eventName]!
    const after = transition(before, event)
    const check = accept[stateName as StateName]
    if (check) { expect(after).not.toBe(before); check(after, before) }
    else expect(after).toBe(before)
    expect(after.token).toBeGreaterThanOrEqual(before.token)
    invariants(after)
  })

  // Every result: each pending state × success and each error class, sent with
  // that state's own token. Exactly the owning state changes.
  const outcomes = (Object.keys(PENDING) as PendingName[]).flatMap(name => OUTCOME_NAMES.map(outcome => [name, outcome] as const))
  const eventFor = (name: PendingName, outcome: typeof OUTCOME_NAMES[number], token = PENDING[name].pending!.token): ThemeEvent =>
    outcome === 'resolved' ? result(token, OUTCOMES[name].result) : failed(token, STATUS[outcome])
  it.each(outcomes)('%s → %s', (name, outcome) => {
    const before = PENDING[name], after = transition(before, eventFor(name, outcome))
    expect(after).not.toBe(before)
    OUTCOMES[name][outcome](after, before)
    invariants(after)
  })
  it.each(outcomes)('%s → %s is ignored by every other state', (name, outcome) => {
    const event = eventFor(name, outcome)
    for (const [other, state] of Object.entries(STATES)) if (other !== name) expect(transition(state, event), other).toBe(state)
  })
  it.each(outcomes)('%s → %s with a stale token is ignored', (name, outcome) => {
    const before = PENDING[name]
    expect(transition(before, eventFor(name, outcome, before.pending!.token - 1))).toBe(before)
  })
})

// AEON-870: the Load more control and the reducer share one condition, so the
// control is never enabled for a press that does nothing.
describe('theme editor pagination control', () => {
  it.each(Object.keys(STATES))('%s: Load more is enabled exactly when pagination starts', name => {
    const before = STATES[name]!, after = transition(before, { type: 'more' })
    expect(canLoadMore(before)).toBe(after !== before)
    if (after !== before) expect(after.pending?.op).toEqual({ kind: 'more', after: before.cursor })
  })
})

// ---- The Vue runner: real promises, identity and permission wiring ----------

afterEach(() => { mocks.session.identity = null })
async function editor(options: { manage?: boolean } = {}) {
  vi.resetAllMocks()
  mocks.permissions = new Set(['profile.write', ...(options.manage ? ['settings.manage'] : [])])
  vi.mocked(api.listThemes).mockResolvedValue({ items: [DEFAULT, CONTRAST, COPPER, OTHER], next_cursor: 'next' })
  vi.mocked(api.getActiveTheme).mockResolvedValue(chosen(COPPER))
  mocks.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }
  resetAgentTheme(ME)
  const created = useThemeEditor()
  await created.idle()
  return created
}
const conflict = (message = 'Changed') => Object.assign(new Error(message), { status: 409 })

describe('theme editor runner', () => {
  it('loads for the person and feeds permissions in as events', async () => {
    const theme = await editor()
    expect(theme.active.value).toEqual(chosen(COPPER)); expect(theme.canEdit.value).toBe(true)
    expect(theme.editable(DEFAULT)).toBe(false)
    // can() is not reactive in this mock; a new identity object re-evaluates it.
    mocks.permissions.add('settings.manage'); mocks.session.identity = { ...(mocks.session.identity as object) }
    await nextTick(); expect(theme.editable(DEFAULT)).toBe(true)
  })
  it('state is frozen, so a direct draft write cannot bypass the model', async () => {
    const theme = await editor()
    expect(() => { (theme.draft.value as ThemeRecord).name = 'Sneaky' }).toThrow(TypeError)
    theme.update(draft => { draft.name = 'Through the model' })
    expect(theme.dirty.value).toBe(true); expect(theme.draft.value!.name).toBe('Through the model')
  })
  it('an identity change drops a delayed save result', async () => {
    const theme = await editor()
    let release!: (record: ThemeRecord) => void
    vi.mocked(api.updateTheme).mockImplementation(() => new Promise(resolve => { release = resolve }))
    theme.update(draft => { draft.name = 'Old person draft' })
    const saving = theme.save()
    mocks.session.identity = null; await nextTick()
    release({ ...COPPER, name: 'Old person draft', revision: 5 }); await saving
    expect(theme.state.value).toMatchObject({ identity: '', draft: null, items: [], message: '', pending: null })
  })
  it('review-642d: reload after a delete conflict closes the stale confirmation; the retry needs a fresh one', async () => {
    const theme = await editor()
    vi.mocked(api.deleteTheme).mockRejectedValueOnce(conflict())
    theme.confirmDelete(OTHER); await theme.remove()
    expect(theme.confirming.value).toEqual(target(OTHER)); expect(theme.canDelete.value).toBe(false)
    expect(theme.error.value).toMatch(/^Other changed elsewhere/)
    vi.mocked(api.listThemes).mockResolvedValue({ items: [DEFAULT, CONTRAST, COPPER, { ...OTHER, revision: 10 }], next_cursor: null })
    await theme.load()
    expect(theme.confirming.value).toBeNull(); expect(theme.error.value).toBe('')
    await theme.remove()
    expect(api.deleteTheme).toHaveBeenCalledTimes(1)
    vi.mocked(api.deleteTheme).mockResolvedValueOnce(undefined)
    theme.confirmDelete(theme.items.value.find(item => item.id === 'other')!); await theme.remove()
    expect(api.deleteTheme).toHaveBeenLastCalledWith({ id: 'other', revision: 10, name: 'Other' })
    expect(theme.message.value).toBe('Deleted.')
  })
  it('review-642d: both duplication stages keep their conflict through pagination until a reload', async () => {
    const theme = await editor()
    vi.mocked(api.duplicateTheme).mockRejectedValueOnce(conflict())
    await theme.duplicate(CONTRAST)
    vi.mocked(api.listThemes).mockResolvedValueOnce({ items: [LATER], next_cursor: null })
    await theme.more()
    expect(theme.error.value).toBe('High contrast changed elsewhere. Reload themes, then duplicate it again.')
    expect(theme.canReload.value).toBe(true); expect(theme.canDuplicate(CONTRAST)).toBe(false)
    vi.mocked(api.duplicateTheme).mockResolvedValueOnce(COPY); vi.mocked(api.selectTheme).mockRejectedValueOnce(conflict())
    await theme.duplicate(OTHER)
    expect(theme.error.value).toBe('High contrast copy was created, but could not be selected. Your theme choice changed elsewhere. Reload themes, then choose High contrast copy. High contrast changed elsewhere. Reload themes, then duplicate it again.')
    expect(theme.conflict.value).toBe(true); expect(theme.canChoose.value).toBe(false); expect(theme.items.value.map(item => item.id)).toContain('copy')
    await theme.load()
    expect([theme.error.value, theme.conflict.value, theme.canChoose.value]).toEqual(['', false, true])
  })
  it('review-642e 1: a copy whose selection 404s keeps its recovery through pagination until a reload', async () => {
    const theme = await editor()
    vi.mocked(api.duplicateTheme).mockResolvedValueOnce(COPY)
    vi.mocked(api.selectTheme).mockRejectedValueOnce(Object.assign(new Error('Not found'), { status: 404 }))
    await theme.duplicate(CONTRAST)
    const expected = ['High contrast copy was created, but could not be selected.', 'High contrast copy was deleted elsewhere. Reload themes.']
    expect(theme.notices.value).toEqual(expected)
    vi.mocked(api.listThemes).mockResolvedValueOnce({ items: [LATER], next_cursor: null })
    await theme.more()
    expect(theme.notices.value).toEqual(expected); expect(theme.canReload.value).toBe(true)
    await theme.load()
    expect([theme.error.value, theme.unfinished.value]).toEqual(['', []])
  })
  it('review-642e 2: an existing conflict never hides that the copy was created', async () => {
    const theme = await editor()
    vi.mocked(api.deleteTheme).mockRejectedValueOnce(conflict())
    theme.confirmDelete(COPPER); await theme.remove(); theme.cancelDelete()
    expect(theme.conflict.value).toBe(true)
    vi.mocked(api.duplicateTheme).mockResolvedValueOnce(COPY); vi.mocked(api.selectTheme).mockRejectedValueOnce(conflict())
    await theme.duplicate(OTHER)
    expect(theme.notices.value).toEqual([
      'High contrast copy was created, but could not be selected.',
      'Copper changed elsewhere. Reload themes, then delete it again if you still want to.',
      'Your theme choice changed elsewhere. Reload themes, then choose High contrast copy.',
    ])
  })
  it('an unreachable server keeps the copy visible until it is chosen', async () => {
    const theme = await editor()
    vi.mocked(api.duplicateTheme).mockResolvedValueOnce(COPY); vi.mocked(api.selectTheme).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    await theme.duplicate(CONTRAST)
    expect(theme.error.value).toBe('High contrast copy was created, but could not be selected. The theme service could not be reached. Check your connection and try again.')
    vi.mocked(api.listThemes).mockResolvedValueOnce({ items: [LATER], next_cursor: null })
    await theme.more()
    expect(theme.error.value).toBe('High contrast copy was created, but could not be selected.')
    vi.mocked(api.selectTheme).mockResolvedValueOnce(chosen(COPY, 13))
    await theme.choose(theme.items.value.find(item => item.id === 'copy')!)
    expect([theme.error.value, theme.unfinished.value, theme.active.value!.theme.id]).toEqual(['', [], 'copy'])
  })
  it('the shared default needs settings.manage; members duplicate it instead', async () => {
    const member = await editor()
    vi.mocked(api.selectTheme).mockResolvedValueOnce(chosen(DEFAULT, 13))
    await member.choose(DEFAULT)
    expect(member.canEdit.value).toBe(false); expect(member.ownership(DEFAULT)).toEqual({ label: 'Workspace default', access: 'read-only' })
    member.update(draft => { draft.name = 'Changed default' }); await member.save()
    expect(api.updateTheme).not.toHaveBeenCalled(); expect(member.offersDelete(DEFAULT)).toBe(false)
    const manager = await editor({ manage: true })
    expect(manager.ownership(CONTRAST)).toEqual({ label: 'Workspace', access: 'you manage it' }); expect(manager.offersDelete(DEFAULT)).toBe(false)
  })
})

// ---- Runtime appearance (AEON-643) ---------------------------------------------
//
// The runner publishes every server-confirmed active theme to the runtime agent
// appearance. Each mutation commits through one deferred response, so every
// lifecycle event is placed between the request and its answer without sleeps.

const avatar = (theme: ThemeRecord, value: ThemeRecord['values']['agents']['avatar'], revision = theme.revision): ThemeRecord =>
  ({ ...theme, revision, values: { ...theme.values, primary: { light: ({ 'robot-1': '#0e6f6c', sprite: '#3a5fc4', orbit: '#8547b0', quill: '#bf3d6d', 'robot-3': '#b5642a', 'robot-4': '#5b52c9', 'robot-2': '#2f7a5a' } as Record<string, string>)[value] ?? '#0e6f6c', dark: null }, agents: { ...theme.values.agents, avatar: value } } })
const R_COPPER = avatar(COPPER, 'robot-1'), R_OTHER = avatar(OTHER, 'sprite'), R_DEFAULT = avatar(DEFAULT, 'orbit')
const R_COPY = avatar(COPY, 'quill'), R_SAVED = avatar(COPPER, 'sprite', 5), R_BOB = avatar(record('bob', 'Bob', 'personal', 1), 'robot-3')
const R_FRESH = avatar(COPPER, 'robot-4', 6), R_AGAIN = avatar(COPPER, 'robot-2', 7)
let afterCommit: (() => Promise<void>) | null = null
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (failure: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const settled = async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); await nextTick() }
type Mutation = {
  /** Mocks every call; returns the deferred that commits the mutation. */
  arrange: () => ReturnType<typeof deferred<unknown>>
  act: (theme: ReturnType<typeof createThemeEditor>) => Promise<void>
  committed: ThemeRecord['values']['agents']['avatar'] | null
  /** The editor's active theme once committed. */
  shows: string
}
const MUTATIONS: Record<string, Mutation> = {
  'choose': {
    arrange: () => { const d = deferred<unknown>(); vi.mocked(api.selectTheme).mockReturnValue(d.promise as Promise<ActiveTheme>); return d },
    act: theme => theme.choose(R_OTHER), committed: 'sprite', shows: 'other',
  },
  'save': {
    arrange: () => { const d = deferred<unknown>(); vi.mocked(api.updateTheme).mockReturnValue(d.promise as Promise<ThemeRecord>); return d },
    act: theme => { theme.update(draft => { draft.values.agents.avatar = 'sprite'; draft.values.primary = { ...R_SAVED.values.primary } }); return theme.save() }, committed: 'sprite', shows: 'copper',
  },
  'duplicate and select': {
    arrange: () => { const d = deferred<unknown>(); vi.mocked(api.duplicateTheme).mockResolvedValue(R_COPY); vi.mocked(api.selectTheme).mockReturnValue(d.promise as Promise<ActiveTheme>); return d },
    act: theme => theme.duplicate(R_OTHER), committed: 'quill', shows: 'copy',
  },
  'new theme': {
    arrange: () => { const d = deferred<unknown>(); vi.mocked(api.getTheme).mockResolvedValue(R_DEFAULT); vi.mocked(api.duplicateTheme).mockResolvedValue(R_COPY); vi.mocked(api.selectTheme).mockReturnValue(d.promise as Promise<ActiveTheme>); return d },
    act: theme => theme.newTheme(), committed: 'quill', shows: 'copy',
  },
  'delete the active theme': {
    arrange: () => {
      const d = deferred<unknown>(); vi.mocked(api.deleteTheme).mockReturnValue(d.promise as Promise<void>)
      vi.mocked(api.getActiveTheme).mockResolvedValue({ ...chosen(R_DEFAULT), selected_theme_id: 'copper', fallback_notice: { deleted_theme_id: 'copper', deleted_theme_name: 'Copper' } })
      return d
    },
    act: theme => { theme.confirmDelete(R_COPPER); return theme.remove() }, committed: 'orbit', shows: 'default',
  },
  'delete another theme': {
    arrange: () => { const d = deferred<unknown>(); vi.mocked(api.deleteTheme).mockReturnValue(d.promise as Promise<void>); return d },
    act: theme => { theme.confirmDelete(R_OTHER); return theme.remove() }, committed: 'robot-1', shows: 'copper',
  },
  'reload': {
    arrange: () => { const d = deferred<unknown>(); vi.mocked(api.getActiveTheme).mockReturnValue(d.promise.then(() => chosen(R_FRESH, 13))); return d },
    act: theme => theme.load(), committed: 'robot-4', shows: 'copper',
  },
}
const answer = (name: string) => name === 'save' ? R_SAVED : name.startsWith('delete') || name === 'reload' ? undefined : chosen(name === 'choose' ? R_OTHER : R_COPY, 13)

type Lifecycle = (theme: ReturnType<typeof createThemeEditor>) => Promise<{ runtime: string | null; shows: string | null } | 'committed'> | { runtime: string | null; shows: string | null } | 'committed'
const signIn = (id: string) => ({ tenant: { id: 'tenant' }, principal: { id, kind: 'person' } })
const newcomer = (theme: ThemeRecord) => {
  vi.mocked(api.listThemes).mockResolvedValue({ items: [theme], next_cursor: null })
  vi.mocked(api.getActiveTheme).mockResolvedValue(chosen(theme))
}
// What happens between the request and its answer. 'committed' expects the
// mutation's own result; otherwise the expected runtime and active theme.
const LIFECYCLES: Record<string, { between: Lifecycle; fails?: boolean }> = {
  'while the card stays mounted': { between: () => 'committed' },
  'after navigating away': { between: theme => { theme.leave(); return 'committed' } },
  'after navigating away and back while pending': { between: theme => {
    // Returning while the write is pending neither restarts nor replaces it.
    const before = theme.state.value.pending
    theme.leave(); void theme.enter()
    expect(theme.state.value.pending).toBe(before)
    return 'committed'
  } },
  'when the session restores during a pending operation': { between: theme => {
    const before = theme.state.value.pending
    const reads = vi.mocked(api.getActiveTheme).mock.calls.length
    const restoring = restoreAgentTheme(ME)
    // The same model cannot start an independent read over a pending write.
    if (before?.op.kind === 'load') {
      expect(theme.state.value.pending).toEqual({ token: before.token + 1, op: { kind: 'load' } })
      expect(api.getActiveTheme).toHaveBeenCalledTimes(reads + 1)
    } else {
      expect(theme.state.value.pending).toBe(before)
      expect(api.getActiveTheme).toHaveBeenCalledTimes(reads)
    }
    afterCommit = async () => { await restoring }
    return 'committed'
  } },
  'after signing out': { between: () => { mocks.session.identity = null; resetAgentTheme(); return { runtime: null, shows: null } } },
  'after another person signs in': { between: () => {
    newcomer(R_BOB); mocks.session.identity = signIn('bob'); resetAgentTheme('tenant/bob')
    return { runtime: 'robot-3', shows: 'bob' }
  } },
  'after the same person signs out and in again': { between: () => {
    // Synchronously, before any watcher flush: the old answer must still be dropped.
    newcomer(R_AGAIN); mocks.session.identity = null; resetAgentTheme(); mocks.session.identity = signIn('person'); resetAgentTheme(ME)
    return { runtime: 'robot-2', shows: 'copper' }
  } },
  'when the write fails': { between: () => ({ runtime: 'robot-1', shows: 'copper' }), fails: true },
}

describe('runtime appearance follows every confirmed theme', () => {
  const cache = new Map<string, string>()
  beforeEach(() => {
    cache.clear()
    vi.stubGlobal('localStorage', { getItem: (key: string) => cache.get(key) ?? null, setItem: (key: string, value: string) => cache.set(key, value), removeItem: (key: string) => cache.delete(key) })
  })
  afterEach(() => { vi.unstubAllGlobals(); afterCommit = null })
  const cells = Object.keys(MUTATIONS).flatMap(mutation => Object.keys(LIFECYCLES).map(lifecycle => [mutation, lifecycle] as const))
  it.each(cells)('%s %s', async (mutationName, lifecycleName) => {
    const theme = await editor()
    // The workspace's own fixtures, with distinct avatars per record.
    vi.mocked(api.listThemes).mockResolvedValue({ items: [R_DEFAULT, CONTRAST, R_COPPER, R_OTHER], next_cursor: 'next' })
    vi.mocked(api.getActiveTheme).mockResolvedValue(chosen(R_COPPER))
    await theme.load()
    expect(agentTheme.value?.avatar).toBe('robot-1')
    const mutation = MUTATIONS[mutationName]!, lifecycle = LIFECYCLES[lifecycleName]!
    const response = mutation.arrange()
    const pending = mutation.act(theme)
    await settled()
    expect(theme.busy.value).toBe(true)
    const expected = await lifecycle.between(theme)
    if (lifecycle.fails) response.reject(Object.assign(new Error('Write failed'), { status: 500 }))
    else response.resolve(answer(mutationName))
    await pending; await theme.idle(); await settled()
    const want = expected === 'committed' ? { runtime: mutation.committed, shows: mutation.shows } : expected
    expect(agentTheme.value?.avatar ?? null).toBe(want.runtime)
    expect(theme.active.value?.theme.id ?? null).toBe(want.shows)
    // Runtime and the editor never disagree about the confirmed theme.
    if (theme.active.value) {
      expect(agentTheme.value).toEqual(theme.active.value.theme.values.agents)
      expect(JSON.parse(cache.get('aeon.theme.v1')!)).toEqual({ principal: theme.state.value.identity, css: themeCss(theme.active.value.theme.values) })
    } else expect(cache.has('aeon.theme.v1')).toBe(false)
    if (afterCommit) {
      // Restoration cannot undo the operation's confirmed theme.
      await afterCommit(); afterCommit = null
      expect(agentTheme.value?.avatar).toBe(mutation.committed)
    }
    if (lifecycle.fails) expect(theme.error.value).not.toBe('')
  })

  it('an unsaved draft never reaches the runtime', async () => {
    const theme = await editor()
    expect(agentTheme.value?.avatar).toBe('robot-5')
    theme.update(draft => { draft.values.agents.avatar = 'sprite' })
    theme.leave()
    expect(agentTheme.value?.avatar).toBe('robot-5')
  })

  it('a settings card unmounting never stops the shared editor', async () => {
    vi.resetAllMocks()
    mocks.permissions = new Set(['profile.write'])
    resetAgentTheme(ME)
    vi.mocked(api.listThemes).mockResolvedValue({ items: [R_DEFAULT, R_COPPER, R_OTHER], next_cursor: null })
    vi.mocked(api.getActiveTheme).mockResolvedValue(chosen(R_COPPER))
    mocks.session.identity = signIn('person')
    const card = effectScope()
    const theme = card.run(useThemeEditor)!
    await theme.idle()
    const response = deferred<ActiveTheme>()
    vi.mocked(api.selectTheme).mockReturnValue(response.promise)
    const choosing = theme.choose(R_OTHER)
    card.stop()
    response.resolve(chosen(R_OTHER, 13)); await choosing
    expect(agentTheme.value?.avatar).toBe('sprite')
    expect(theme.active.value?.theme.id).toBe('other')
  })
})
