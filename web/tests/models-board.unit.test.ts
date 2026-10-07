// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { APIError, api } from '../src/lib/api'
import { boardFixture, boardPin } from './models-board-fixtures'
import { canMove, moveOrder, moveRule, orderBody, rulesBody, stepTarget } from '../src/lib/modelsBoard'
import { getBoard, getBoardRules, putBoardOrder, putBoardProfile, putBoardRules, resetBoardOrder } from '../src/lib/modelsBoardApi'
import { useModelsBoard } from '../src/lib/useModelsBoard'
import { toasts, resetToasts } from '../src/lib/toast'
const state = vi.hoisted(() => ({ session: { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } as { tenant: { id: string }; principal: { id: string; kind: string } } | null, authenticationCurrent: () => true }, cleanups: [] as (() => void)[], access: undefined as (() => void) | undefined }))
vi.mock('../src/stores/session', () => ({ useSession: () => state.session }))
vi.mock('../src/lib/authz', () => ({ can: () => true, onAccessChange: (listener: () => void) => { state.access = listener; return () => {} } }))
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onBeforeUnmount: (cleanup: () => void) => state.cleanups.push(cleanup) }))
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
vi.mock('../src/lib/modelsBoardApi', async original => ({ ...await original<typeof import('../src/lib/modelsBoardApi')>(), getBoard: vi.fn(), getBoardRules: vi.fn(), putBoardOrder: vi.fn(), putBoardProfile: vi.fn(), putBoardRules: vi.fn(), resetBoardOrder: vi.fn() }))
const settle = async () => { for (let index = 0; index < 12; index++) await Promise.resolve() }
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { resolve, promise } }
beforeEach(() => { state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []; vi.resetAllMocks(); resetToasts(); state.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }; vi.mocked(getBoard).mockResolvedValue(boardFixture()); vi.mocked(getBoardRules).mockResolvedValue({ revision: 2, rules: [boardPin] }) })
it('keeps rules out of personal writes, moves across Not allowed and retains fixed pin ownership', () => {
  const column = boardFixture().columns.find(column => column.column === 'frontend')!, context = { layer: 'mine' as const, situation: 'first' as const }
  expect(canMove(column.list[0]!, context)).toBe(false)
  expect(orderBody(column).rank).not.toContain(boardPin.line)
  const last = column.list.at(-1)!
  expect(stepTarget(column, last.line, 1, context)).toEqual({ zone: 'not', index: 0 })
  const result = moveOrder(column, last.line, 'not', 0)
  expect(result.not).toEqual([last.line]); expect(result.rank).not.toContain(last.line)
})
it('carries inherited workspace rules and reasons into project writes and never permits cross-family moves', () => {
  const before = rulesBody({ revision: 2, rules: [boardPin] }, 'frontend')
  const after = moveRule(before, 'openai:sol', 'bottom', 0, 'Conserve this account')
  expect(after.top).toEqual([{ line: boardPin.line, why: boardPin.why }]); expect(after.bottom).toEqual([{ line: 'openai:sol', why: 'Conserve this account' }])
  const column = boardFixture().columns.find(column => column.column === 'frontend')!
  expect(canMove(column.top[0]!, { layer: 'rules', situation: 'first', project: 'project' })).toBe(false)
  expect(canMove(boardFixture().columns[4]!.not[0]!, { layer: 'rules', situation: 'first' })).toBe(false)
})
it('saves exactly one captured order with the person and revision then undoes an inherited order by deleting it', async () => {
  const editor = useModelsBoard(ref({ layer: 'mine', situation: 'first' }), ref(false)); await settle()
  const column = editor.document.value!.columns.find(column => column.column === 'backend')!, next = boardFixture(); next.revision = 4
  vi.mocked(putBoardOrder).mockResolvedValue({ person_id: next.person_id, revision: 4, dry_run: false, moved: [], profile: next.profile }); vi.mocked(getBoard).mockResolvedValue(next)
  await editor.move(column, column.list[1]!, 'list', 0)
  expect(putBoardOrder).toHaveBeenCalledTimes(1); expect(putBoardOrder).toHaveBeenCalledWith({ layer: 'mine', situation: 'first' }, 'backend', expect.objectContaining({ rank: ['anthropic:sonnet', 'openai:sol', 'anthropic:opus', 'openai:astra', 'anthropic:fable'] }), 3, next.person_id)
  vi.mocked(resetBoardOrder).mockResolvedValue({ person_id: next.person_id, revision: 5, dry_run: false, moved: [], profile: next.profile })
  toasts.at(-1)!.actions[0]!.run(); await settle()
  expect(resetBoardOrder).toHaveBeenCalledWith({ layer: 'mine', situation: 'first' }, 'backend', 4, next.person_id)
})
it('rejects a held write and its follow-up read after a context change', async () => {
  const context = ref({ layer: 'mine' as const, situation: 'first' as const, project: 'A' }), editor = useModelsBoard(context, ref(false)); await settle()
  const column = editor.document.value!.columns.find(column => column.column === 'backend')!, held = deferred<Awaited<ReturnType<typeof putBoardOrder>>>()
  vi.mocked(putBoardOrder).mockReturnValue(held.promise)
  const saving = editor.move(column, column.list[1]!, 'list', 0)
  context.value = { ...context.value, project: 'B' }; await settle()
  const reads = vi.mocked(getBoard).mock.calls.length
  held.resolve({ person_id: 'old-person', revision: 4, dry_run: false, moved: [], profile: boardFixture().profile }); await saving
  expect(getBoard).toHaveBeenCalledTimes(reads); expect(toasts).toHaveLength(0); expect(editor.busy.value).toBe(false)
})
it('refreshes a conflict without a success toast and blocks edits after a saved write whose refresh fails', async () => {
  const editor = useModelsBoard(ref({ layer: 'mine', situation: 'first' }), ref(false)); await settle()
  let column = editor.document.value!.columns.find(column => column.column === 'backend')!
  vi.mocked(putBoardOrder).mockRejectedValue(new APIError(409, 'stale_revision')); await editor.move(column, column.list[1]!, 'list', 0)
  expect(editor.error.value).toContain('not saved'); expect(toasts).toHaveLength(0)
  column = editor.document.value!.columns.find(column => column.column === 'backend')!
  vi.mocked(putBoardOrder).mockResolvedValue({ person_id: 'person', revision: 4, dry_run: false, moved: [], profile: boardFixture().profile }); vi.mocked(getBoard).mockRejectedValue(new Error('offline'))
  await editor.move(column, column.list[1]!, 'list', 0); expect(editor.error.value).toContain('Saved, but'); expect(editor.document.value).toBeNull(); expect(toasts).toHaveLength(0)
})
it('expires Undo and rejects it after another revision or identity boundary', async () => {
  const editor = useModelsBoard(ref({ layer: 'mine', situation: 'first' }), ref(false)); await settle()
  const next = boardFixture(); next.revision = 4
  vi.mocked(putBoardProfile).mockResolvedValue({ person_id: next.person_id, revision: 4, dry_run: false, moved: [], profile: next.profile }); vi.mocked(getBoard).mockResolvedValue(next)
  const now = vi.spyOn(Date, 'now').mockReturnValue(1000)
  await editor.profile({ residency: 'eu' }); now.mockReturnValue(11_001); toasts.at(-1)!.actions[0]!.run(); await settle(); expect(putBoardProfile).toHaveBeenCalledTimes(1)
  now.mockRestore(); await editor.profile({ residency: 'local' }); editor.document.value!.revision = 99; toasts.at(-1)!.actions[0]!.run(); await settle(); expect(putBoardProfile).toHaveBeenCalledTimes(2)
  await editor.profile({ residency: 'eu' }); state.access!(); toasts.at(-1)!.actions[0]!.run(); await settle(); expect(putBoardProfile).toHaveBeenCalledTimes(3)
})
it('sends the person precondition on the real HTTP order client and surfaces a server rejection', async () => {
  const { putBoardOrder: requestOrder } = await vi.importActual<typeof import('../src/lib/modelsBoardApi')>('../src/lib/modelsBoardApi')
  vi.mocked(api).mockResolvedValue(new Response(JSON.stringify({ error: 'person_changed' }), { status: 409 }))
  await expect(requestOrder({ layer: 'mine', situation: 'first' }, 'review:openai', { rank: [], not: [] }, 7, 'canonical-person')).rejects.toMatchObject({ status: 409, message: 'person_changed' })
  expect(api).toHaveBeenCalledWith('/model-preferences/orders/review%3Aopenai/first?for=me', expect.objectContaining({ headers: { 'Content-Type': 'application/json', 'If-Prefs-Person': 'canonical-person' }, body: JSON.stringify({ rank: [], not: [], revision: 7 }) }))
})

it('deduplicates a project restriction of a workspace rule without dropping inherited locks', () => {
  const projectNot = { ...boardPin, scope: 'project' as const, project_id: 'project', lock: 'not' as const, why: 'Do not use for this project' }
  expect(rulesBody({ revision: 2, rules: [projectNot, boardPin] }, 'frontend')).toEqual({ top: [], bottom: [], not: { [boardPin.line]: projectNot.why } })
})
it('discards a delayed template preview and its approval after the observed revision changes', async () => {
  const context = ref({ layer: 'mine' as const, situation: 'first' as const }), editor = useModelsBoard(context, ref(false)); await settle()
  const held = deferred<Awaited<ReturnType<typeof putBoardProfile>>>()
  vi.mocked(putBoardProfile).mockReturnValue(held.promise)
  const previewing = editor.previewTemplate('best')
  editor.document.value!.revision = 9
  held.resolve({ person_id: 'person', revision: 3, dry_run: true, moved: [], profile: boardFixture().profile })
  expect(await previewing).toBeNull()
  expect(await editor.applyTemplate({ template: 'best', revision: 3, key: editor.actionKey.value, moved: [] })).toBe(false)
  expect(putBoardProfile).toHaveBeenCalledTimes(1)
})
it('drops a late board read across an identity boundary', async () => {
  const old = deferred<Awaited<ReturnType<typeof getBoard>>>()
  vi.mocked(getBoard).mockReturnValueOnce(old.promise)
  const context = ref({ layer: 'mine' as const, situation: 'first' as const }), editor = useModelsBoard(context, ref(false)); await settle()
  state.session.identity = { tenant: { id: 'new-tenant' }, principal: { id: 'new-person', kind: 'person' } }
  context.value = { ...context.value, situation: 'fix' }; await settle()
  const latest = editor.document.value
  old.resolve({ ...boardFixture(), person_id: 'old-person' }); await settle()
  expect(editor.document.value).toBe(latest)
  expect(editor.document.value?.person_id).not.toBe('old-person')
})
it('refuses a new Not allowed on the workspace default and still stores Mine and rules bans', async () => {
  const refused = useModelsBoard(ref({ layer: 'default', situation: 'first' }), ref(false)); await settle()
  const column = refused.document.value!.columns.find(column => column.column === 'backend')!
  const line = column.list.at(-1)!.line
  expect(await refused.move(column, column.list.at(-1)!, 'not', 0)).toBe(false)
  expect(putBoardOrder).not.toHaveBeenCalled(); expect(putBoardRules).not.toHaveBeenCalled()
  expect(refused.error.value).toBe('Not allowed is a rule: switch Show to Workspace rules.')
  state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
  const german = useModelsBoard(ref({ layer: 'default', situation: 'first' }), ref(true)); await settle()
  const germanColumn = german.document.value!.columns.find(column => column.column === 'backend')!
  expect(await german.move(germanColumn, germanColumn.list.at(-1)!, 'not', 0)).toBe(false)
  expect(german.error.value).toBe('Nicht erlaubt ist eine Regel: Zeigen auf Regeln des Arbeitsbereichs stellen.')
  expect(putBoardOrder).not.toHaveBeenCalled()
  state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
  const mine = useModelsBoard(ref({ layer: 'mine', situation: 'first' }), ref(false)); await settle()
  const mineColumn = mine.document.value!.columns.find(column => column.column === 'backend')!
  const saved = boardFixture(); saved.revision = 4
  vi.mocked(putBoardOrder).mockResolvedValue({ person_id: saved.person_id, revision: 4, dry_run: false, moved: [], profile: saved.profile })
  vi.mocked(getBoard).mockResolvedValue(saved)
  expect(await mine.move(mineColumn, mineColumn.list.at(-1)!, 'not', 0)).toBe(true)
  expect(putBoardOrder).toHaveBeenCalledWith({ layer: 'mine', situation: 'first' }, 'backend', expect.objectContaining({ not: [line] }), 3, saved.person_id)
  state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
  vi.mocked(putBoardOrder).mockReset()
  const rules = useModelsBoard(ref({ layer: 'rules', situation: 'first' }), ref(false)); await settle()
  const rulesColumn = rules.document.value!.columns.find(column => column.column === 'backend')!
  vi.mocked(putBoardRules).mockResolvedValue({ revision: 3, rules: [boardPin] })
  vi.mocked(getBoard).mockResolvedValue(saved)
  expect(await rules.move(rulesColumn, rulesColumn.list[0]!, 'not', 0, 'Kept by a rule')).toBe(true)
  expect(putBoardOrder).not.toHaveBeenCalled()
  expect(putBoardRules).toHaveBeenCalledWith({ layer: 'rules', situation: 'first' }, 'backend', expect.objectContaining({ not: { [rulesColumn.list[0]!.line]: 'Kept by a rule' } }), 2)
})
it('cannot edit as an agent or use profile writes to bypass project rule ownership', async () => {
  state.session.identity!.principal.kind = 'agent'
  const editor = useModelsBoard(ref({ layer: 'mine', situation: 'first' }), ref(false)); await settle()
  const column = editor.document.value!.columns.find(column => column.column === 'backend')!
  expect(await editor.move(column, column.list[1]!, 'list', 0)).toBe(false)
  expect(putBoardOrder).not.toHaveBeenCalled()
  state.session.identity!.principal.kind = 'person'
  const project = useModelsBoard(ref({ layer: 'rules', situation: 'first', project: 'project' }), ref(false)); await settle()
  expect(await project.profile({ residency: 'local' })).toBe(false)
  expect(putBoardProfile).not.toHaveBeenCalled()
})
