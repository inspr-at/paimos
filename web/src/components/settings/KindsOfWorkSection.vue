<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'
import SettingsPopover, { type SettingsMenuItem } from './SettingsPopover.vue'
import KindRow from './KindRow.vue'
import KindForm from './KindForm.vue'
import KindsAdvanced from './KindsAdvanced.vue'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { scopeOwner } from '../../lib/identityScope'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { dismiss, toast } from '../../lib/toast'
import { textForKinds } from '../../lib/workKindsCopy'
import { activeKinds, archiveKind, createKind, getSituationLimits, kindWords, listWorkKinds, movedKinds, orderKinds, putSituationLimits, restoreKind, updateKind, validLimit, type KindWords, type SituationLimits, type WorkKind } from '../../lib/workKinds'
const session = useSession()
// English only until AEON-998 gives the app one display language; then this reads that source.
const german = computed(() => false)
const t = computed(() => textForKinds(german.value))
const editable = computed(() => session.identity?.principal.kind === 'person' && can('models.read') && can('model_prefs.manage'))
const readScope = useIdentityScope(() => can('models.read')), writeScope = useIdentityScope(() => editable.value)
const kinds = ref<WorkKind[]>([]), limits = ref<SituationLimits | null>(null), loaded = ref(false), loadingKinds = ref(false), loadingLimits = ref(false), truncated = ref(false)
const kindsError = ref(''), limitsError = ref(''), busy = ref(false)
const kindsRead = readScope.lane(), limitsRead = readScope.lane()
const visible = computed(() => activeKinds(kinds.value)), archived = computed(() => kinds.value.filter(kind => kind.archived_at && !kind.system))
const editor = ref<{ kind: WorkKind | null; stamp: string } | null>(null), editorError = ref(''), editDirty = ref(false)
const menu = ref<{ kind: WorkKind; anchor: HTMLElement } | null>(null), menuOpen = ref(false)
const confirmation = ref<{ kind: WorkKind; anchor: HTMLElement; stamp: string } | null>(null), archiveError = ref('')
const fingerprint = (kind: WorkKind) => JSON.stringify({ ...kindWords(kind), archived_at: kind.archived_at ?? null, position: kind.position })
const snapshot = (kind: WorkKind): WorkKind => ({ ...kind, ...kindWords(kind) })
let changes = 0, disposed = false
function loadKinds() {
  loadingKinds.value = true; kindsError.value = ''
  return kindsRead.run(({ after, signal }) => after(listWorkKinds(signal), result => { kinds.value = result.items; truncated.value = result.truncated; loaded.value = true }), { failed: () => { kindsError.value = t.value('loadError') }, settled: () => { loadingKinds.value = false } })
}
function loadLimits() {
  loadingLimits.value = true; limitsError.value = ''
  return limitsRead.run(({ after, signal }) => after(getSituationLimits(signal), result => { limits.value = result }), { failed: () => { limitsError.value = t.value('limitsError') }, settled: () => { loadingLimits.value = false } })
}
watch(() => scopeOwner(session.identity), () => {
  changes++; kinds.value = []; limits.value = null; loaded.value = false; truncated.value = false; busy.value = false; editor.value = null; editDirty.value = false; menuOpen.value = false; menu.value = null; confirmation.value = null; kindsError.value = ''; limitsError.value = ''
}, { flush: 'sync' })
watch(readScope.owner, owner => { if (owner) { void loadKinds(); void loadLimits() } }, { immediate: true })
watch(editable, allowed => { if (!allowed) { changes++; busy.value = false; menuOpen.value = false; confirmation.value = null } }, { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; changes++ })
function errorMessage(error: unknown) { return (error as { status?: number })?.status === 409 ? t.value('stale') : t.value('failed') }
function change<T>(send: (signal: AbortSignal) => Promise<T>, accept: (result: T) => void, message: string, undo?: (result: T, signal: AbortSignal) => Promise<void>, failure?: (message: string) => void) {
  if (busy.value || loadingKinds.value || loadingLimits.value || !editable.value || disposed) return
  busy.value = true
  const owner = writeScope.owner.value, turn = ++changes
  return writeScope.run(({ after, signal }) => after(send(signal), result => {
    accept(result)
    const toastId = toast(message, { timeout: 10_000, ...(undo ? { action: { label: t.value('undo'), run: () => {
      if (disposed || busy.value || !editable.value || owner !== writeScope.owner.value) return
      dismiss(toastId)
      if (turn !== changes) { toast(t.value('stale'), { tone: 'error' }); return }
      busy.value = true; changes++
      void writeScope.run(({ signal }) => undo(result, signal), { failed: error => toast(errorMessage(error), { tone: 'error' }), settled: () => { busy.value = false } })
    } } } : {}) })
  }), { failed: error => { const message = errorMessage(error); if (failure) failure(message); else toast(message, { tone: 'error' }) }, settled: () => { busy.value = false } })
}
function adoptKind(result: WorkKind) {
  const index = kinds.value.findIndex(kind => kind.id === result.id)
  if (index >= 0) kinds.value[index] = result
  else kinds.value.push(result)
}
async function checkKind(expected: WorkKind, signal: AbortSignal) {
  const page = await listWorkKinds(signal), actual = page.items.find(kind => kind.id === expected.id)
  if (!actual || fingerprint(actual) !== fingerprint(expected)) throw Object.assign(new Error('Changed elsewhere'), { status: 409 })
}
function kindUndo(expected: WorkKind, restore: (signal: AbortSignal) => Promise<WorkKind>, signal: AbortSignal) {
  // Check ownership immediately before the inverse write, including after the
  // freshness read; a person may change while that request is pending.
  return writeScope.run(({ after }) => after(checkKind(expected, signal), () => after(restore(signal), result => { adoptKind(result); toast(t.value('undone')) }))).then(() => {})
}
// The form replaces its row, so focus goes back to a row by id once Vue has drawn it.
function focusKind(slug: string | null) {
  void nextTick(() => (slug ? globalThis.document?.getElementById(`kind-${slug}`) : globalThis.document?.querySelector<HTMLElement>('[data-kind-new]'))?.focus({ preventScroll: true }))
}
// A moved row is drawn again; its buttons stay disabled until the write has settled, so focus returns after that.
function keepFocus(element: HTMLElement) {
  const restore = () => { if (!disposed && element.isConnected && globalThis.document?.activeElement !== element) element.focus({ preventScroll: true }) }
  if (!busy.value) void nextTick(restore)
  else { const stop = watch(busy, value => { if (!value) { stop(); restore() } }, { flush: 'post' }) }
}
function edit(kind: WorkKind | null) {
  if (!editable.value || busy.value) return
  // One form at a time, and opening another never drops words that are not saved yet.
  if (editor.value && editDirty.value) { editorError.value = t.value('finishFirst'); return }
  editorError.value = ''; editDirty.value = false; editor.value = { kind: kind ? snapshot(kind) : null, stamp: kind ? fingerprint(kind) : '' }
}
function closeEditor() {
  const target = editor.value
  editor.value = null; editDirty.value = false; editorError.value = ''
  if (target) focusKind(target.kind?.slug ?? null)
}
function save(words: KindWords) {
  const target = editor.value
  if (!target || !editable.value) return
  const current = target.kind ? kinds.value.find(kind => kind.id === target.kind!.id) : null
  if (target.kind && (!current || fingerprint(current) !== target.stamp)) { editorError.value = t.value('stale'); return }
  const position = Math.max(-1, ...kinds.value.filter(kind => !kind.archived_at && kind.system !== 'other').map(kind => kind.position)) + 1
  const before = target.kind
  const message = before
    ? german.value ? `${words.label} gespeichert. Die Spalte unter Modelle zeigt die neuen Worte.` : `${words.label} saved. Its column on Models shows the new words.`
    : german.value ? `${words.label} ist jetzt eine Art von Arbeit und eine Spalte unter Modelle.` : `${words.label} is a kind of work now, and a column on Models.`
  void change(signal => before ? updateKind(before.id, words, signal) : createKind(words, position, signal), result => { adoptKind(result); editor.value = null; editDirty.value = false; focusKind(result.slug) }, message, (result, signal) => kindUndo(result, undoSignal => before ? updateKind(before.id, kindWords(before), undoSignal) : archiveKind(result.id, undoSignal), signal), message => { editorError.value = message })
}
function askArchive(kind: WorkKind, anchor: HTMLElement) {
  if (!editable.value || busy.value || kind.system) return
  archiveError.value = ''; confirmation.value = { kind: snapshot(kind), anchor, stamp: fingerprint(kind) }
}
const archiveCopy = computed(() => {
  const kind = confirmation.value?.kind
  return kind ? { title: german.value ? `${kind.label} archivieren?` : `Archive ${kind.label}?`, effect: german.value ? `${kind.ticket_count} Tickets behalten ihren Bereich und folgen bei Modellen Alles andere. Die Spalte verschwindet aus Modelle.` : `${kind.ticket_count} tickets keep their area value and follow Everything else for models. Its column leaves Models.`, keeps: german.value ? 'Jederzeit wiederherstellbar.' : 'Restore any time.', action: t.value('archive') } : undefined
})
function archive() {
  const target = confirmation.value
  if (!target || !editable.value || target.kind.system) return
  const current = kinds.value.find(kind => kind.id === target.kind.id)
  if (!current || fingerprint(current) !== target.stamp) { archiveError.value = t.value('stale'); return }
  const rest = visible.value.filter(kind => kind.id !== target.kind.id), next = rest[Math.min(visible.value.findIndex(kind => kind.id === target.kind.id), rest.length - 1)]
  void change(signal => archiveKind(target.kind.id, signal), result => { adoptKind(result); confirmation.value = null; if (next) focusKind(next.slug) }, german.value ? `${target.kind.label} archiviert.` : `${target.kind.label} archived.`, (result, signal) => kindUndo(result, undoSignal => restoreKind(result.id, undoSignal), signal), message => { archiveError.value = message })
}
function restore(kind: WorkKind) {
  const target = snapshot(kind)
  void change(signal => restoreKind(target.id, signal), adoptKind, german.value ? `${target.label} wiederhergestellt.` : `${target.label} restored.`, (result, signal) => kindUndo(result, undoSignal => archiveKind(result.id, undoSignal), signal))
}
function move(kind: WorkKind, direction: -1 | 1, focus?: HTMLElement) {
  if (truncated.value) return
  const slugs = movedKinds(kinds.value, kind.id, direction)
  if (!slugs) return
  const before = [...visible.value.map(kind => kind.slug), ...kinds.value.filter(kind => !kind.archived_at && kind.system === 'review').map(kind => kind.slug)]
  const expected = slugs.filter(slug => kinds.value.find(kind => kind.slug === slug)?.system !== 'review')
  const accept = (items: WorkKind[]) => { kinds.value = [...items, ...kinds.value.filter(kind => kind.archived_at)] }
  void change(signal => orderKinds(slugs, signal), result => { accept(result.items); if (focus) keepFocus(focus) }, german.value ? `${kind.label} verschoben; die Spalten unter Modelle folgen dieser Reihenfolge.` : `${kind.label} moved; the columns on Models follow this order.`, (_result, signal) => writeScope.run(({ after }) => after(listWorkKinds(signal), page => {
    if (page.truncated || JSON.stringify(activeKinds(page.items).map(kind => kind.slug)) !== JSON.stringify(expected)) throw Object.assign(new Error('Changed elsewhere'), { status: 409 })
    return after(orderKinds(before, signal), result => { accept(result.items); toast(t.value('undone')) })
  })).then(() => {}))
}
function setLimit(key: 'small_hours' | 'fix_rounds', value: number) {
  if (!limits.value || !validLimit(value, key === 'small_hours' ? 8 : 6)) return
  const before = { ...limits.value }
  limitsError.value = ''
  const message = key === 'small_hours'
    ? german.value ? `Kleine Arbeit: ${value} h oder weniger.` : `Small work: ${value} h or less.`
    : german.value ? `Festgefahren nach ${value} Korrekturrunden.` : `Stuck after ${value} fix rounds.`
  void change(signal => putSituationLimits({ small_hours: before.small_hours, fix_rounds: before.fix_rounds, revision: before.revision, [key]: value }, signal), result => { limits.value = result }, message, (result, signal) => writeScope.run(({ after }) => after(putSituationLimits({ small_hours: before.small_hours, fix_rounds: before.fix_rounds, revision: result.revision }, signal), restored => { limits.value = restored; toast(t.value('undone')) })).then(() => {}), message => { limitsError.value = message; limits.value = { ...before } })
}
// The ⋯ menu: order and archive. Both stay in the menu so a row shows only what matters.
async function openMenu(kind: WorkKind, anchor: HTMLElement) {
  if (!editable.value || busy.value) return
  if (menuOpen.value && menu.value?.anchor === anchor) { menuOpen.value = false; return }
  // The popover binds to the record first and opens a tick later; a record change while open would close it.
  menu.value = { kind: snapshot(kind), anchor }
  await nextTick()
  if (menu.value?.anchor === anchor && editable.value) menuOpen.value = true
}
const menuContext = computed(() => `${writeScope.owner.value}/${menu.value?.kind.id ?? ''}`)
const menuItems = computed<SettingsMenuItem[]>(() => {
  const kind = menu.value?.kind
  if (!kind) return []
  const movable = kind.system !== 'other', blocked = (direction: -1 | 1) => truncated.value || busy.value || !movedKinds(kinds.value, kind.id, direction)
  return [
    // Spelled keys: arrow glyphs are icons (AEON-109). The approved shortcut stays Alt+Up / Alt+Down.
    ...(movable ? [{ id: 'up', label: t.value('moveUp'), detail: 'Alt+Up', icon: 'arrow-up' as const, disabled: blocked(-1) }, { id: 'down', label: t.value('moveDown'), detail: 'Alt+Down', icon: 'arrow-down' as const, disabled: blocked(1) }] : []),
    { id: 'archive', label: t.value('archiveMenu'), detail: kind.system ? t.value('builtInReason') : german.value ? `${kind.ticket_count} Tickets folgen Alles andere` : `${kind.ticket_count} tickets follow Everything else`, icon: 'archive' as const, disabled: !!kind.system, separated: movable },
  ]
})
function menuSelect(id: string, context: string | undefined) {
  const target = menu.value, kind = target && kinds.value.find(item => item.id === target.kind.id)
  if (!target || !kind || context !== menuContext.value || !editable.value) return
  if (id === 'up' || id === 'down') move(kind, id === 'up' ? -1 : 1, target.anchor)
  else if (id === 'archive') askArchive(kind, target.anchor)
}
</script>
<template>
  <div class="section kinds-section">
    <p class="who"><AppIcon :name="editable ? 'shield' : 'eye'" :size="14" /><span>{{ t(editable ? 'intro' : 'introRead') }}</span></p>
    <SettingsCard :title="t('title')" icon="list" anchor="k-kinds">
      <template #lead>{{ t('lead') }}</template>
      <template #aside><RouterLink class="btn sm" to="/settings/models">{{ t('openModels') }}</RouterLink></template>
      <p v-if="loadingKinds && !loaded" role="status">{{ t('loading') }}</p>
      <p v-else-if="kindsError" class="err" role="alert">{{ kindsError }} <button class="btn sm" type="button" :disabled="busy || loadingKinds" @click="loadKinds">{{ t('retry') }}</button></p>
      <p v-if="truncated" class="err" role="status">{{ t('truncated') }}</p>
      <ol class="kinds" :aria-describedby="editable ? 'kind-help' : undefined">
        <template v-for="(kind, index) in visible" :key="kind.id">
          <KindForm v-if="editor?.kind?.id === kind.id" :kind="editor.kind" :busy="busy" :editable="editable" :error="editorError" :t="t" @close="closeEditor" @save="save" @dirty="editDirty = $event" />
          <KindRow v-else :kind="kind" :index="index" :editable="editable" :busy="busy" :german="german" :t="t" @edit="edit" @menu="openMenu" @move="move" />
        </template>
        <KindForm v-if="editor && !editor.kind" key="new" :kind="null" :busy="busy" :editable="editable" :error="editorError" :t="t" @close="closeEditor" @save="save" @dirty="editDirty = $event" />
      </ol>
      <p v-if="loaded && !visible.length" class="faint">{{ t('empty') }}</p>
      <div v-if="editable" class="kind-foot"><button class="btn sm" type="button" data-kind-new :disabled="busy || loadingKinds || loadingLimits || !loaded" @click="edit(null)"><AppIcon name="plus" :size="14" />{{ t('newKindShort') }}</button><span id="kind-help" class="hint">{{ t('orderHint') }}</span></div>
      <div v-if="archived.length" class="kind-arch"><span>{{ t('archived') }}</span><span v-for="kind in archived" :key="kind.id" class="archived-kind">{{ kind.label }} <button v-if="editable" type="button" class="link-btn" :disabled="busy" @click="restore(kind)">{{ t('restore') }}</button></span></div>
    </SettingsCard>
    <KindsAdvanced :kinds="visible" :limits="limits" :editable="editable" :busy="busy" :loading="loadingLimits" :error="limitsError" :german="german" :t="t" @change="setLimit" @reload="loadLimits" />
    <SettingsPopover v-model:open="menuOpen" :anchor="menu?.anchor ?? null" :label="`${t('actionsFor')} ${menu?.kind.label ?? ''}`" :items="menuItems" :context-key="menuContext" :busy="busy" @select="menuSelect" />
    <SettingsPopover v-if="confirmation" :open="true" :anchor="confirmation?.anchor ?? null" :label="archiveCopy?.title ?? t('archive')" mode="confirmation" :confirmation="archiveCopy" :context-key="`${writeScope.owner.value}/${confirmation?.kind.id ?? ''}`" :busy="busy" :error="archiveError" @update:open="open => { if (!open) confirmation = null }" @submit="archive" />
  </div>
</template>
<style scoped>
.section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.who { display: flex; align-items: flex-start; gap: 8px; font-size: 12.5px; color: var(--ink-2); }.who svg { flex: none; margin-top: 3px; }
.kinds { display: grid; gap: 0; margin: 0; padding: 0; list-style: none; }
.kinds :deep(.kind-edit + .kind) { border-top: 0; }
.kind-foot { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 16px; margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); }
.hint { font-size: 12px; color: var(--ink-3); }
.kind-arch { display: flex; flex-wrap: wrap; gap: 8px 16px; margin-top: 12px; font-size: 12px; color: var(--ink-3); }.archived-kind { overflow-wrap: anywhere; }.err { color: var(--danger); }.faint { color: var(--ink-3); }
@media (pointer: coarse), (max-width: 720px) { button { min-height: 44px; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
