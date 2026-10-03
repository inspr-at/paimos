// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, type EffectScope } from 'vue'
import type { ActiveTheme, ThemeRecord, ThemesPage } from '../src/lib/themes'
const mocks = vi.hoisted(() => ({ session: { identity: null as unknown }, permissions: new Set<string>() }))
vi.mock('../src/stores/session', () => ({ useSession: () => mocks.session }))
vi.mock('../src/lib/authz', () => ({ can: (permission: string) => mocks.permissions.has(permission) }))
vi.mock('../src/lib/themes', () => ({ listThemes: vi.fn(), getActiveTheme: vi.fn(), getTheme: vi.fn(), selectTheme: vi.fn(), updateTheme: vi.fn(), duplicateTheme: vi.fn(), deleteTheme: vi.fn() }))
import * as api from '../src/lib/themes'
import { agentTheme, resetAgentTheme, restoreAgentTheme } from '../src/lib/agentTheme'
import {
  SELECTION, canDelete, canEdit, canReload, canSave, createThemeEditor, feedback, initialState, isConflicted, isDirty, transition, useThemeEditor,
  type Operation, type Recovery, type Rights, type ThemeEditorState, type ThemeEvent,
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
const conflictOn = (key: string, operation: Recovery['operation'] = 'delete'): Recovery => ({ key, operation, feedback: `${key} changed elsewhere` })
const named = (theme: ThemeRecord, name: string) => ({ ...theme, name })
const recoloured = (theme: ThemeRecord, light = '#3a5fc4') => ({ ...theme, values: { ...theme.values, primary: { light, dark: null } } })

const loaded = (patch: Partial<ThemeEditorState> = {}): ThemeEditorState => ({
  ...initialState(), identity: ME, rights: MEMBER, items: [DEFAULT, CONTRAST, COPPER, OTHER], cursor: 'next', active: chosen(COPPER), draft: COPPER, token: 100, ...patch,
})
const waiting = (token: number, op: Operation, patch: Partial<ThemeEditorState> = {}) => loaded({ token, pending: { token, op }, ...patch })
const target = (theme: ThemeRecord) => ({ id: theme.id, revision: theme.revision, name: theme.name })

// Every state the editor can be in. Pending states own a unique token, so a
// result event matches exactly one of them.
const STATES: Record<string, ThemeEditorState> = {
  signedOut: initialState(),
  loading: { ...initialState(), identity: ME, rights: MEMBER, token: 1, pending: { token: 1, op: { kind: 'load' } } },
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
  lastPage: loaded({ cursor: null }),
  fallbackFailed: loaded({ active: null, draft: null, items: [DEFAULT, CONTRAST, OTHER], failure: 'Deleted, but your current theme could not be loaded. Reload themes.' }),
  paging: waiting(201, { kind: 'more', after: 'next' }, { confirming: target(OTHER), recoveries: [conflictOn('copper')] }),
  choosing: waiting(202, { kind: 'choose', themeID: 'other', selectionRevision: 12 }, { recoveries: [conflictOn('copper'), conflictOn('other')] }),
  saving: waiting(203, { kind: 'save', theme: named(COPPER, 'Copper edited') }, { draft: named(COPPER, 'Copper edited'), recoveries: [conflictOn('other')] }),
  readingDefault: waiting(204, { kind: 'readDefault', themeID: 'default', selectionRevision: 12 }, { recoveries: [conflictOn('copper')] }),
  duplicating: waiting(205, { kind: 'duplicate', source: CONTRAST, name: 'High contrast copy', selectionRevision: 12 }),
  selectingCopy: waiting(206, { kind: 'selectCopy', copy: COPY, selectionRevision: 12 }, { items: [DEFAULT, CONTRAST, COPPER, OTHER, COPY] }),
  deletingOther: waiting(207, { kind: 'delete', target: target(OTHER), wasActive: false }, { confirming: target(OTHER), recoveries: [conflictOn('copper')] }),
  deletingActive: waiting(208, { kind: 'delete', target: target(COPPER), wasActive: true }, { confirming: target(COPPER) }),
  fallback: waiting(209, { kind: 'fallback' }, { active: null, draft: null, items: [DEFAULT, CONTRAST, OTHER], message: 'Deleted.' }),
  reloading: waiting(210, { kind: 'load' }, { recoveries: [conflictOn('copper'), conflictOn('other'), conflictOn(SELECTION, 'selection')] }),
}
type StateName = keyof typeof STATES

// ---- Expectations -------------------------------------------------------------

type Check = (after: ThemeEditorState, before: ThemeEditorState) => void
const keys = (state: ThemeEditorState) => state.recoveries.map(item => item.key).sort()
const starts = (kind: Operation['kind'], extra: (op: Operation, after: ThemeEditorState, before: ThemeEditorState) => void = () => {}): Check => (after, before) => {
  expect(after.pending?.op.kind).toBe(kind)
  expect(after.pending!.token).toBe(before.token + 1)
  expect(after.failure).toBe('')
  // Starting work never resolves a recovery by itself.
  expect(keys(after)).toEqual(keys(before))
  extra(after.pending!.op, after, before)
}
const resets = (identity: string): Check => (after, before) => {
  expect(after.identity).toBe(identity)
  expect([after.items, after.active, after.draft, after.recoveries, after.confirming, after.renaming, after.failure, after.message]).toEqual([[], null, null, [], null, null, '', ''])
  expect(after.token).toBeGreaterThanOrEqual(before.token)
  if (identity) expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'load' } })
  else expect(after.pending).toBeNull()
}
const each = (names: StateName[], check: Check) => Object.fromEntries(names.map(name => [name, check])) as Partial<Record<StateName, Check>>
const ALL = Object.keys(STATES) as StateName[]
const except = (...names: StateName[]) => ALL.filter(name => !names.includes(name))
// States that may choose, duplicate or create: loaded, nothing pending or unsaved, no selection conflict.
const SELECTABLE: StateName[] = ['clean', 'renaming', 'confirming', 'activeConflict', 'otherConflict', 'confirmingConflict', 'lastPage', 'defaultMember', 'defaultManager']
const EDITABLE_COPPER: StateName[] = ['clean', 'dirty', 'invalid', 'renaming', 'confirming', 'otherConflict', 'confirmingConflict', 'lastPage']
const IDLE_CLEAN: StateName[] = ['clean', 'reader', 'defaultMember', 'defaultManager', 'renaming', 'confirming', 'activeConflict', 'selectionConflict', 'otherConflict', 'confirmingConflict', 'lastPage', 'fallbackFailed']
const result = (token: number, value: unknown): ThemeEvent => ({ type: 'resolved', token, result: value })
const failed = (token: number, status: number | null, message = 'Request failed'): ThemeEvent => ({ type: 'failed', token, status, message })

// Each event lists the states that accept it and what must hold afterwards.
// Every other state must return the very same object: the event is ignored.
const EVENTS: Record<string, { event: ThemeEvent; accept: Partial<Record<StateName, Check>> }> = {
  // Identity and permissions
  'identity changes': { event: { type: 'identity', identity: 'tenant/someone' }, accept: each(ALL, resets('tenant/someone')) },
  'sign out': { event: { type: 'identity', identity: '' }, accept: each(except('signedOut'), resets('')) },
  'same identity': { event: { type: 'identity', identity: ME }, accept: { signedOut: resets(ME) } },
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
  'load more': { event: { type: 'more' }, accept: each(['clean', 'dirty', 'invalid', 'reader', 'defaultMember', 'defaultManager', 'renaming', 'confirming', 'activeConflict', 'dirtyConflict', 'selectionConflict', 'otherConflict', 'confirmingConflict', 'fallbackFailed'], starts('more', (op, after, before) => {
    expect(op).toEqual({ kind: 'more', after: 'next' }); expect(after.draft).toEqual(before.draft); expect(after.confirming).toEqual(before.confirming)
  })) },
  // Selection
  'choose another theme': { event: { type: 'choose', id: 'other' }, accept: each(SELECTABLE, starts('choose', op => expect(op).toEqual({ kind: 'choose', themeID: 'other', selectionRevision: 12 }))) },
  'choose the active theme again': { event: { type: 'choose', id: 'copper' }, accept: each(['defaultMember', 'defaultManager'], starts('choose', op => expect(op).toMatchObject({ themeID: 'copper' }))) },
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
  'new theme': { event: { type: 'newTheme' }, accept: each(SELECTABLE, starts('readDefault', op => expect(op).toEqual({ kind: 'readDefault', themeID: 'default', selectionRevision: 12 }))) },
  // Delete with its confirmation
  'ask to delete another theme': { event: { type: 'confirmDelete', id: 'other' }, accept: each(['clean', 'renaming', 'confirming', 'activeConflict', 'selectionConflict', 'lastPage', 'defaultMember', 'defaultManager'], after => expect(after.confirming).toEqual(target(OTHER))) },
  'ask to delete the active theme': { event: { type: 'confirmDelete', id: 'copper' }, accept: each(['clean', 'renaming', 'confirming', 'selectionConflict', 'otherConflict', 'confirmingConflict', 'lastPage', 'defaultMember', 'defaultManager'], after => expect(after.confirming).toEqual(target(COPPER))) },
  'ask to delete a workspace theme': { event: { type: 'confirmDelete', id: 'contrast' }, accept: { defaultManager: after => expect(after.confirming).toEqual(target(CONTRAST)) } },
  'ask to delete the workspace default': { event: { type: 'confirmDelete', id: 'default' }, accept: {} },
  'keep the theme': { event: { type: 'cancelDelete' }, accept: each(['confirming', 'confirmingConflict'], after => expect(after.confirming).toBeNull()) },
  'delete': { event: { type: 'delete' }, accept: { confirming: starts('delete', op => expect(op).toEqual({ kind: 'delete', target: target(OTHER), wasActive: false })) } },
  // Results: each matches exactly one pending state by token.
  'page arrives': { event: result(201, { items: [LATER, { ...OTHER, revision: 10 }, { ...COPPER, name: 'Copper newer', revision: 5 }], next_cursor: null } satisfies ThemesPage), accept: { paging: after => {
    expect(after.items.map(item => item.id)).toEqual(['default', 'contrast', 'copper', 'other', 'later']); expect(after.cursor).toBeNull()
    // A list row may be newer, but the active record and its draft stay as installed,
    // and pagination never resolves a recovery.
    expect(after.draft).toEqual(COPPER); expect(after.active!.theme).toEqual(COPPER); expect(keys(after)).toEqual(['copper'])
    expect(isConflicted(after)).toBe(true); expect(feedback(after)).toBe('copper changed elsewhere')
    // The open confirmation captured revision 9; the row is now 10, so it closes.
    expect(after.confirming).toBeNull()
  } } },
  'page fails': { event: failed(201, 500, 'List failed'), accept: { paging: after => { expect(after.failure).toBe('List failed'); expect(keys(after)).toEqual(['copper']); expect(after.cursor).toBe('next') } } },
  'choice saved': { event: result(202, chosen(OTHER, 13)), accept: { choosing: after => {
    expect(after.active).toEqual(chosen(OTHER, 13)); expect(after.draft).toEqual(OTHER)
    // Fresh state resolves only the installed record; the old record's recovery stays visible.
    expect(keys(after)).toEqual(['copper']); expect(isConflicted(after)).toBe(false); expect(canEdit(after)).toBe(true)
    expect(feedback(after)).toBe('copper changed elsewhere')
  } } },
  'choice conflicts': { event: failed(202, 409), accept: { choosing: after => { expect(keys(after)).toEqual(['#selection', 'copper', 'other']); expect(after.active!.theme.id).toBe('copper'); expect(isConflicted(after)).toBe(true) } } },
  'chosen theme is gone': { event: failed(202, 404), accept: { choosing: after => { expect(after.recoveries.find(item => item.key === 'other')!.feedback).toBe('Other no longer exists. Reload themes.') } } },
  'choice fails': { event: failed(202, 500, 'Unavailable'), accept: { choosing: after => { expect(after.failure).toBe('Unavailable'); expect(keys(after)).toEqual(['copper', 'other']) } } },
  'theme saved': { event: result(203, { ...named(COPPER, 'Copper edited'), revision: 5 }), accept: { saving: after => {
    expect(after.active!.theme.revision).toBe(5); expect(after.draft).toEqual(after.active!.theme); expect(isDirty(after)).toBe(false)
    expect(after.message).toBe('Saved.'); expect(keys(after)).toEqual(['other'])
  } } },
  'save conflicts': { event: failed(203, 409), accept: { saving: after => {
    expect(isDirty(after)).toBe(true); expect(canSave(after)).toBe(false); expect(keys(after)).toEqual(['copper', 'other'])
    expect(feedback(after)).toBe('Copper changed elsewhere. Discard your edits to load the current version.')
  } } },
  'saved theme is gone': { event: failed(203, 404), accept: { saving: after => expect(feedback(after)).toBe('Copper no longer exists. Discard your edits to continue.') } },
  'save fails': { event: failed(203, 500, 'Write failed'), accept: { saving: after => { expect(isDirty(after)).toBe(true); expect(canSave(after)).toBe(true); expect(after.failure).toBe('Write failed'); expect(after.message).toBe('') } } },
  'default read': { event: result(204, { ...DEFAULT, revision: 3 }), accept: { readingDefault: (after, before) => {
    expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'duplicate', source: { ...DEFAULT, revision: 3 }, name: 'Porcelain copy', selectionRevision: 12 } })
    expect(after.items.find(item => item.id === 'default')!.revision).toBe(3); expect(keys(after)).toEqual(['copper'])
  } } },
  'default read fails': { event: failed(204, 500, 'Unavailable'), accept: { readingDefault: after => { expect(after.pending).toBeNull(); expect(after.failure).toBe('The workspace default could not be read. Unavailable') } } },
  'copy created': { event: result(205, COPY), accept: { duplicating: (after, before) => {
    expect(after.items.map(item => item.id)).toContain('copy')
    expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'selectCopy', copy: COPY, selectionRevision: 12 } })
  } } },
  // review-642d 1: duplication conflicts go through the recovery model.
  'duplicate source conflicts': { event: failed(205, 409), accept: { duplicating: after => {
    expect(keys(after)).toEqual(['contrast']); expect(after.failure).toBe(''); expect(isConflicted(after)).toBe(false)
    expect(feedback(after)).toBe('High contrast changed elsewhere. Reload themes, then duplicate it again.'); expect(canReload(after)).toBe(true)
  } } },
  'duplicate fails': { event: failed(205, 500, 'Unavailable'), accept: { duplicating: after => { expect(after.failure).toBe('Unavailable'); expect(keys(after)).toEqual([]) } } },
  'copy selected': { event: result(206, chosen(COPY, 13)), accept: { selectingCopy: after => { expect(after.active!.theme.id).toBe('copy'); expect(after.draft).toEqual(COPY); expect(after.message).toBe('Duplicated.') } } },
  'copy selection conflicts': { event: failed(206, 409), accept: { selectingCopy: after => {
    expect(keys(after)).toEqual(['#selection']); expect(isConflicted(after)).toBe(true); expect(after.items.map(item => item.id)).toContain('copy')
    expect(feedback(after)).toMatch(/^The copy was created, but could not be selected: .*Reload themes, then choose High contrast copy\.$/)
  } } },
  'copy selection fails': { event: failed(206, 500, 'Unavailable'), accept: { selectingCopy: after => { expect(after.failure).toBe('The copy was created, but could not be selected. Unavailable'); expect(keys(after)).toEqual([]) } } },
  'other theme deleted': { event: result(207, undefined), accept: { deletingOther: after => {
    expect(after.items.map(item => item.id)).toEqual(['default', 'contrast', 'copper']); expect(after.confirming).toBeNull()
    expect(after.message).toBe('Deleted.'); expect(keys(after)).toEqual(['copper']); expect(isConflicted(after)).toBe(true)
  } } },
  // review-642d 2: a conflicting delete keeps its confirmation, but Delete stays off until fresh state.
  'delete conflicts': { event: failed(207, 409), accept: { deletingOther: after => {
    expect(after.confirming).toEqual(target(OTHER)); expect(canDelete(after)).toBe(false); expect(keys(after)).toEqual(['copper', 'other'])
    expect(feedback(after)).toBe('copper changed elsewhere')
  } } },
  'deleted theme is gone': { event: failed(207, 404), accept: { deletingOther: after => expect(after.recoveries.find(item => item.key === 'other')!.feedback).toBe('Other was already deleted elsewhere. Reload themes.') } },
  'delete fails': { event: failed(207, 500, 'Unavailable'), accept: { deletingOther: after => { expect(after.failure).toBe('Unavailable'); expect(after.confirming).toEqual(target(OTHER)); expect(canDelete(after)).toBe(true) } } },
  'active theme deleted': { event: result(208, undefined), accept: { deletingActive: (after, before) => {
    expect([after.active, after.draft, after.confirming]).toEqual([null, null, null]); expect(after.items.map(item => item.id)).not.toContain('copper')
    expect(after.pending).toEqual({ token: before.token + 1, op: { kind: 'fallback' } }); expect(after.message).toBe('Deleted.')
  } } },
  'fallback read': { event: result(209, chosen(DEFAULT, 13)), accept: { fallback: after => { expect(after.active!.theme.id).toBe('default'); expect(after.message).toBe('Deleted. You are using the workspace default.') } } },
  'fallback read fails': { event: failed(209, 500), accept: { fallback: after => { expect(after.active).toBeNull(); expect(after.failure).toMatch(/^Deleted, but/); expect(canReload(after)).toBe(true) } } },
  'reload arrives': { event: result(210, { page: { items: [DEFAULT, CONTRAST, { ...COPPER, revision: 5 }, { ...OTHER, revision: 10 }], next_cursor: 'later' }, active: chosen({ ...COPPER, revision: 5 }, 13) }), accept: { reloading: after => {
    // A full snapshot resolves every recovery.
    expect(after.recoveries).toEqual([]); expect(after.draft!.revision).toBe(5); expect(after.cursor).toBe('later'); expect(canEdit(after)).toBe(true)
  } } },
  'reload fails': { event: failed(210, null, 'Offline'), accept: { reloading: after => { expect(after.failure).toBe('Offline'); expect(keys(after)).toEqual(['#selection', 'copper', 'other']) } } },
  'late result for an older operation': { event: result(150, chosen(OTHER, 99)), accept: {} },
  'late failure for an older operation': { event: failed(150, 409), accept: {} },
}

function invariants(state: ThemeEditorState) {
  if (state.active || state.draft) expect(state.draft?.id).toBe(state.active?.theme.id)
  if (state.confirming) expect(state.items.find(item => item.id === state.confirming!.id)?.revision).toBe(state.confirming.revision)
  if (state.renaming) expect(state.renaming).toBe(state.draft?.id)
  expect(new Set(state.recoveries.map(item => item.key)).size).toBe(state.recoveries.length)
  if (state.failure || state.recoveries.length) expect(feedback(state)).not.toBe('')
  if (isConflicted(state)) expect(canEdit(state) || canSave(state)).toBe(false)
  if (!state.identity) expect([state.items, state.active, state.pending]).toEqual([[], null, null])
}

// ---- The matrix: every event in every state ----------------------------------

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
})

// ---- The Vue runner: real promises, identity and permission wiring ----------

let scope: EffectScope | undefined
afterEach(() => { scope?.stop(); scope = undefined })
async function editor(options: { manage?: boolean } = {}) {
  vi.resetAllMocks()
  mocks.permissions = new Set(['profile.write', ...(options.manage ? ['settings.manage'] : [])])
  mocks.session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
  resetAgentTheme(ME)
  vi.mocked(api.listThemes).mockResolvedValue({ items: [DEFAULT, CONTRAST, COPPER, OTHER], next_cursor: 'next' })
  vi.mocked(api.getActiveTheme).mockResolvedValue(chosen(COPPER))
  scope = effectScope()
  const created = scope.run(createThemeEditor)!
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
    expect(theme.error.value).toMatch(/^The copy was created, but could not be selected/)
    expect(theme.conflict.value).toBe(true); expect(theme.canChoose.value).toBe(false); expect(theme.items.value.map(item => item.id)).toContain('copy')
    await theme.load()
    expect([theme.error.value, theme.conflict.value, theme.canChoose.value]).toEqual(['', false, true])
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
  ({ ...theme, revision, values: { ...theme.values, agents: { ...theme.values.agents, avatar: value } } })
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
    act: theme => { theme.update(draft => { draft.values.agents.avatar = 'sprite' }); return theme.save() }, committed: 'sprite', shows: 'copper',
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
  'after the session first restores an older appearance': { between: () => {
    // The session's own /me/theme read starts first and answers only after the commit.
    const late = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn(() => late.promise))
    const restoring = restoreAgentTheme(ME)
    afterCommit = async () => { late.resolve(new Response(JSON.stringify(chosen(R_COPPER)))); await restoring }
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
    if (theme.active.value) expect(agentTheme.value).toEqual(theme.active.value.theme.values.agents)
    if (afterCommit) {
      // The older read answers last and must not undo the confirmed theme.
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
    mocks.session = reactive({ identity: signIn('person') })
    resetAgentTheme(ME)
    vi.mocked(api.listThemes).mockResolvedValue({ items: [R_DEFAULT, R_COPPER, R_OTHER], next_cursor: null })
    vi.mocked(api.getActiveTheme).mockResolvedValue(chosen(R_COPPER))
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
