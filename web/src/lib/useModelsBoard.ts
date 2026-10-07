// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { APIError } from './api'
import { can, onAccessChange } from './authz'
import { toast } from './toast'
import { canMove, contextKey, moveOrder, moveRule, orderBody, rulesBody, type BoardCard, type BoardColumn, type BoardContext, type BoardProfile, type BoardZone, type ModelBoardDocument, type RulesDocument } from './modelsBoard'
import { dismissBoardLine, getBoard, getBoardRules, putBoardOrder, putBoardProfile, putBoardRules, resetBoardOrder } from './modelsBoardApi'
import { useSession } from '../stores/session'

export function useModelsBoard(context: Ref<BoardContext>, german: Ref<boolean>) {
  const session = useSession(), document = ref<ModelBoardDocument | null>(null), rules = ref<RulesDocument | null>(null)
  const busy = ref(false), loading = ref(false), error = ref(''), announcement = ref('')
  const owner = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
  const key = computed(() => `${owner.value}/${contextKey(context.value)}`)
  const editable = computed(() => !!owner.value && session.identity?.principal.kind === 'person' && session.authenticationCurrent() && can('models.read', context.value.project) && (context.value.layer === 'mine' || can('model_prefs.manage', context.value.layer === 'rules' ? context.value.project : undefined)))
  const epoch = ref(0), actionKey = computed(() => `${key.value}/${epoch.value}`)
  let generation = 0, read = 0, abort: AbortController | undefined, alive = true
  const text = (en: string, de: string) => german.value ? de : en
  const capture = () => { const started = generation, identity = key.value; return () => alive && started === generation && identity === key.value && session.authenticationCurrent() }
  async function load() {
    const current = capture(), turn = ++read, target = { ...context.value }
    abort?.abort(); abort = new AbortController(); loading.value = true
    try {
      const [board, locks] = await Promise.all([getBoard(target, abort.signal), target.layer === 'rules' ? getBoardRules(target, abort.signal) : Promise.resolve(null)])
      if (!current() || turn !== read) return false
      document.value = board; rules.value = locks; error.value = ''; return true
    } catch (failure) {
      if (current() && turn === read) { document.value = null; rules.value = null; error.value = failure instanceof Error ? failure.message : text('Could not load the board.', 'Das Board konnte nicht geladen werden.') }
      return false
    } finally { if (current() && turn === read) loading.value = false }
  }
  type Template = NonNullable<BoardProfile['template']>
  type Preview = { template: Template; revision: number; key: string; moved: Awaited<ReturnType<typeof putBoardProfile>>['moved'] }
  async function previewTemplate(template: Template): Promise<Preview | null> {
    const board = document.value
    if (!board || !editable.value || busy.value || context.value.layer === 'rules') return null
    const current = capture(), target = { ...context.value }, revision = board.revision, identity = actionKey.value
    busy.value = true; error.value = ''
    try {
      const result = await putBoardProfile(target, { template }, revision, board.person_id, true)
      if (!current() || document.value?.revision !== revision) return null
      return { template, revision, key: identity, moved: result.moved }
    } catch (failure) { if (current()) error.value = failure instanceof Error ? failure.message : text('Could not preview the template.', 'Die Vorlage konnte nicht angezeigt werden.'); return null }
    finally { if (current()) busy.value = false }
  }
  async function applyTemplate(preview: Preview) {
    if (preview.key !== actionKey.value || preview.revision !== document.value?.revision) return false
    return profile({ template: preview.template })
  }
  type Operation = (revision: number) => Promise<{ revision: number }>
  async function write(operation: Operation, undo: Operation | null, rule = false) {
    const board = document.value, locks = rules.value
    if (!board || !editable.value || busy.value || (rule && !locks)) return false
    const current = capture(), target = { ...context.value }
    busy.value = true; error.value = ''
    try {
      const result = await operation(rule ? locks!.revision : board.revision)
      if (!current()) return false
      const loaded = await load()
      if (!current()) return false
      if (!loaded) { error.value = text('Saved, but the board could not be refreshed. Reload before making another change.', 'Gespeichert, aber das Board konnte nicht aktualisiert werden. Vor der nächsten Änderung neu laden.'); return true }
      announcement.value = text('Model order saved.', 'Modellreihenfolge gespeichert.')
      const expires = Date.now() + 10_000
      toast(text('Model preferences saved.', 'Modellpräferenzen gespeichert.'), { timeout: 10_000, action: undo ? { label: text('Undo', 'Rückgängig'), run: () => {
        if (!current() || contextKey(target) !== contextKey(context.value) || Date.now() > expires || !editable.value || busy.value) return
        const revision = rule ? rules.value?.revision : document.value?.revision
        if (revision !== result.revision) { toast(text('The board changed. Undo is no longer available.', 'Das Board wurde geändert. Rückgängig ist nicht mehr verfügbar.'), { tone: 'error' }); return }
        void write(undo, null, rule)
      } } : undefined })
      return true
    } catch (failure) {
      if (!current()) return false
      const message = failure instanceof Error ? failure.message : text('Could not save.', 'Speichern fehlgeschlagen.')
      if (failure instanceof APIError && failure.status === 409) {
        await load()
        if (current()) error.value = text('Changed elsewhere. The board was refreshed; the change was not saved.', 'Andernorts geändert. Das Board wurde aktualisiert; die Änderung wurde nicht gespeichert.')
      } else error.value = message
      return false
    } finally { if (current()) busy.value = false }
  }
  async function move(column: BoardColumn, card: BoardCard, zone: BoardZone, index: number, why = '') {
    if (!canMove(card, context.value)) return false
    const target = { ...context.value }, person = document.value?.person_id ?? null
    if (target.layer === 'rules') {
      if (!rules.value || (zone !== 'free' && !why.trim())) return false
      const before = rulesBody(rules.value, column.column), after = moveRule(before, card.line, zone, index, why.trim())
      if (JSON.stringify(before) === JSON.stringify(after)) return false
      return write(revision => putBoardRules(target, column.column, after, revision), revision => putBoardRules(target, column.column, before, revision), true)
    }
    if (zone !== 'list' && zone !== 'not') return false
    const before = orderBody(column), after = moveOrder(column, card.line, zone, index)
    if (JSON.stringify(before) === JSON.stringify(after)) return false
    return write(revision => putBoardOrder(target, column.column, after, revision, person), column.source === 'own' ? revision => putBoardOrder(target, column.column, before, revision, person) : revision => resetBoardOrder(target, column.column, revision, person))
  }
  async function profile(body: Partial<BoardProfile>) {
    if (context.value.layer === 'rules' && (context.value.project || Object.keys(body).some(field => field !== 'residency'))) return false
    const board = document.value; if (!board) return false
    const target = { ...context.value, ...(context.value.layer === 'rules' ? { layer: 'default' as const } : {}) }, person = board.person_id, previous: Partial<BoardProfile> = {}
    for (const field of Object.keys(body) as (keyof BoardProfile)[]) Object.assign(previous, { [field]: board.profile[field] })
    return write(revision => putBoardProfile(target, body, revision, person), revision => putBoardProfile(target, previous, revision, person))
  }
  async function reset(column: BoardColumn) {
    const board = document.value; if (!board || column.source !== 'own' || context.value.layer === 'rules') return false
    const target = { ...context.value }, before = orderBody(column), person = board.person_id
    return write(revision => resetBoardOrder(target, column.column, revision, person), revision => putBoardOrder(target, column.column, before, revision, person))
  }
  async function dismiss(line: string) {
    const board = document.value; if (!board || context.value.layer === 'rules') return false
    const target = { ...context.value }, before = [...board.profile.dismissed_lines], person = board.person_id
    return write(revision => dismissBoardLine(target, line, revision, person), revision => putBoardProfile(target, { dismissed_lines: before }, revision, person))
  }
  watch(key, () => { generation++; epoch.value++; abort?.abort(); document.value = null; rules.value = null; error.value = ''; announcement.value = ''; busy.value = false; loading.value = false; if (owner.value && can('models.read', context.value.project)) void load() }, { immediate: true, flush: 'sync' })
  const unsubscribe = onAccessChange(() => { generation++; epoch.value++; abort?.abort(); busy.value = false; if (!can('models.read', context.value.project)) document.value = null; else void load() })
  watch(() => can('models.read', context.value.project), allowed => { if (allowed && !document.value && !loading.value) void load() })
  onBeforeUnmount(() => { alive = false; generation++; epoch.value++; abort?.abort(); unsubscribe() })
  return { document, rules, busy, loading, error, announcement, editable, key, actionKey, load, move, profile, reset, dismiss, previewTemplate, applyTemplate }
}
