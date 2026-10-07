<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../../AppIcon.vue'
import BoardColumn from './BoardColumn.vue'
import NewTray from './NewTray.vue'
import ModelCard from './ModelCard.vue'
import SettingsPopover, { type SettingsMenuItem } from '../SettingsPopover.vue'
import { vClipTip } from '../../../directives/clipTip'
import { can } from '../../../lib/authz'
import { useSession } from '../../../stores/session'
import { useProjects } from '../../../stores/projects'
import { useModelsBoard } from '../../../lib/useModelsBoard'
import { rememberBoardPosition } from '../../../lib/modelsBoardNavigation'
import { boardText, canMove, lineName, localizeColumn, stepTarget, zonesFor, type BoardCard, type BoardColumn as Column, type BoardContext, type BoardZone, type ModelBoardDocument } from '../../../lib/modelsBoard'
const props = withDefaults(defineProps<{ full?: boolean; german?: boolean; context?: BoardContext; projects?: { id: string; name: string }[] }>(), { full: false, german: false })
const emit = defineEmits<{ change: [board: ModelBoardDocument]; context: [context: BoardContext] }>()
const router = useRouter(), route = useRoute(), session = useSession(), projectStore = useProjects()
const root = ref<HTMLElement>(), german = computed(() => props.german)
const text = (en: string, de: string) => boardText(german.value, en, de)
function fromURL(): BoardContext {
  const layer = route.query.layer === 'default' || route.query.layer === 'rules' ? route.query.layer : 'mine'
  const situation = route.query.situation === 'fix' || route.query.situation === 'stuck' ? route.query.situation : 'first'
  return { layer, situation, ...(typeof route.query.project_id === 'string' ? { project: route.query.project_id } : {}) }
}
const context = ref<BoardContext>(props.context ?? fromURL())
watch(() => props.context, value => { if (value) context.value = { ...value } }, { deep: true })
watch(() => route.query, () => { if (!props.context) context.value = fromURL() })
const editor = useModelsBoard(context, german)
const { document: board, busy, loading, error, announcement, editable } = editor
const discovered = ref<BoardCard[]>([])
watch(board, value => { if (value) { const known = new Map(discovered.value.map(card => [card.line, card])); for (const card of value.tray) known.set(card.line, card); discovered.value = [...known.values()] } }, { flush: 'sync' })
const newCards = computed(() => discovered.value.filter(card => !board.value?.profile.dismissed_lines.includes(card.line)))
const columns = computed(() => board.value?.columns.filter(column => !column.hidden).map(column => localizeColumn(column, german.value)) ?? [])
const projects = computed(() => props.projects ?? projectStore.projects.filter(project => !project.archived).map(project => ({ id: project.id, name: project.title })))
const projectLabel = computed(() => projects.value.find(project => project.id === context.value.project)?.name ?? (context.value.project ?? text('Any project', 'Jedes Projekt')))
const projectLabels = computed(() => [...new Set([text('Any project', 'Jedes Projekt'), projectLabel.value, ...projects.value.map(project => project.name)])])
// All catalog lines and four empty zones have space before the first interaction.
// Drops cannot change the board height, including placing a tray line.
const slots = computed(() => {
  const lines = new Set(discovered.value.map(card => card.line))
  for (const column of board.value?.columns ?? []) { for (const zone of zonesFor(context.value.layer)) for (const card of column[zone]) lines.add(card.line); for (const item of column.cant) lines.add(item.line) }
  return lines.size + 1
})
const showLabel = computed(() => context.value.layer === 'mine' ? text(`Mine (${session.identity?.principal.name ?? ''})`, `Meine (${session.identity?.principal.name ?? ''})`) : context.value.layer === 'default' ? text('Workspace default', 'Vorgabe des Arbeitsbereichs') : context.value.project ? text('Project rules', 'Projektregeln') : text('Workspace rules', 'Regeln des Arbeitsbereichs'))
const providerLabel = computed(() => ({ any: context.value.layer === 'mine' && !board.value?.residency.own ? text('As the workspace', 'Wie Arbeitsbereich') : text('Any provider', 'Jeder Anbieter'), eu: text('EU-hosted only', 'Nur EU-gehostet'), local: text('Local only', 'Nur lokal') })[board.value?.residency.effective ?? 'any'])
const note = computed(() => context.value.layer === 'rules' ? context.value.project
  ? text('Tighten only: the project adds pins and Not allowed; workspace rules stay locked.', 'Nur verschärfen: Das Projekt ergänzt Anheftungen und Nicht erlaubt; Regeln des Arbeitsbereichs bleiben gesperrt.')
  : text('Rules for everyone: pin a model to the top or the bottom, or don’t allow it. People order the rest.', 'Regeln für alle: ein Modell oben oder unten anpinnen oder nicht erlauben. Den Rest ordnen die Personen.')
  : context.value.layer === 'default' ? text('What everyone gets until they order a column themselves.', 'Was alle bekommen, bis sie eine Spalte selbst ordnen.')
  : text('Top runs first. Your order wins inside the rules; a lock means a rule set it.', 'Oben läuft zuerst. Ihre Reihenfolge gilt innerhalb der Regeln; ein Schloss heißt, eine Regel hat es gesetzt.'))
type Menu = 'move' | 'tray' | 'show' | 'project' | 'providers' | 'columns' | 'column' | 'rule-line'
const templateOpen = ref(false), templatePreview = ref<Awaited<ReturnType<typeof editor.previewTemplate>>>(null)
async function previewTemplate(template: 'best' | 'balanced' | 'save', event: MouseEvent) {
  menuOpen.value = false; reasonOpen.value = false; templateOpen.value = false
  const trigger = event.currentTarget as HTMLElement
  const preview = await editor.previewTemplate(template)
  if (preview && trigger.isConnected) { anchor.value = trigger; templatePreview.value = preview; templateOpen.value = true }
}
async function applyTemplate() { if (templatePreview.value && await editor.applyTemplate(templatePreview.value)) templateOpen.value = false }
const menuOpen = ref(false), menu = ref<Menu>('move'), anchor = ref<HTMLElement | null>(null), reasonOpen = ref(false), why = ref('')
const active = ref<{ column?: Column; card?: BoardCard; zone?: BoardZone; key: string; revision: number; rulesRevision?: number }>()
const pending = ref<{ column: Column; card: BoardCard; zone: BoardZone; index: number }>()
const menuLabel = computed(() => menu.value === 'move' || menu.value === 'tray' ? text('Move to…', 'Verschieben nach…') : menu.value === 'show' ? text('Show', 'Zeigen') : menu.value === 'project' ? text('Project', 'Projekt') : menu.value === 'providers' ? text('Providers', 'Anbieter') : text('Columns', 'Spalten'))
function bind(type: Menu, event: MouseEvent, column?: Column, card?: BoardCard, zone?: BoardZone) {
  if ((!board.value && type !== 'show' && type !== 'project') || busy.value) return
  menu.value = type; active.value = { column, card, zone, key: editor.actionKey.value, revision: board.value?.revision ?? -1, rulesRevision: editor.rules.value?.revision }
  anchor.value = event.currentTarget as HTMLElement; menuOpen.value = true; error.value = ''
}
const items = computed<SettingsMenuItem[]>(() => {
  const current = active.value, column = current?.column, card = current?.card
  if (menu.value === 'show') return [
    { id: 'mine', label: text('Mine', 'Meine'), icon: 'user' }, { id: 'default', label: text('Workspace default', 'Vorgabe des Arbeitsbereichs'), icon: 'layers' },
    { id: 'rules', label: text('Workspace rules', 'Regeln des Arbeitsbereichs'), icon: 'lock' },
    ...(projects.value.length ? [{ id: 'project-rules', label: text('Project rules', 'Projektregeln'), icon: 'folder' as const }] : []),
  ]
  if (menu.value === 'project') return [...(context.value.layer === 'rules' ? [] : [{ id: '', label: text('Any project', 'Jedes Projekt'), icon: 'folders' as const }]), ...projects.value.map(project => ({ id: project.id, label: project.name, icon: 'folder' as const }))]
  if (menu.value === 'providers') return editable.value && !(context.value.layer === 'rules' && context.value.project) ? [
    { id: 'inherit', label: text('Whatever the workspace allows', 'Was der Arbeitsbereich erlaubt'), icon: 'layers' }, { id: 'eu', label: text('EU-hosted only', 'Nur EU-gehostet'), icon: 'server' }, { id: 'local', label: text('Local only', 'Nur lokal'), icon: 'monitor' },
  ] : [{ id: 'read-providers', label: providerLabel.value, detail: context.value.layer === 'rules' && context.value.project ? text('Project provider limits are read only in this release.', 'Anbietergrenzen des Projekts sind in diesem Release nur lesbar.') : text('Provider limits apply before a model can run.', 'Anbietergrenzen gelten, bevor ein Modell laufen kann.'), icon: 'info' }]
  if (menu.value === 'columns') return [...(board.value?.columns.filter(value => value.hidden && !value.fixed).map(value => ({ id: value.column, label: value.label, icon: 'plus' as const })) ?? []), ...(can('model_prefs.manage') ? [{ id: 'create-kind', label: text('New kind of work…', 'Neue Art von Arbeit…'), icon: 'plus' as const, separated: true }] : [])]
  if (menu.value === 'rule-line') {
    const cards = new Map<string, BoardCard>()
    for (const value of board.value?.columns ?? []) for (const zone of zonesFor(context.value.layer)) for (const item of value[zone]) cards.set(item.line, item)
    for (const item of newCards.value) cards.set(item.line, item)
    return [...cards.values()].filter(item => !column || ![...column.top, ...column.bottom, ...column.not].some(value => value.line === item.line)).map(item => ({ id: item.line, label: lineName(item), icon: 'plus' as const }))
  }
  if (menu.value === 'column' && context.value.layer === 'rules') return [{ id: 'add-rule', label: text('Add a rule…', 'Regel ergänzen…'), icon: 'plus' }]
  if (menu.value === 'column') return column ? [{ id: 'hide', label: text('Hide this column', 'Diese Spalte ausblenden'), detail: text('Its order still applies.', 'Ihre Reihenfolge gilt weiterhin.'), icon: 'eye-off' }] : []
  if (menu.value === 'tray') return [...columns.value.filter(value => !value.cant.some(item => item.line === card?.line) && !value.not.some(item => item.line === card?.line)).map(value => ({ id: value.column, label: value.label, icon: 'columns' as const })), { id: 'dismiss-line', label: text('Dismiss', 'Ausblenden'), icon: 'eye-off' }]
  if (!column || !card) return []
  if (!editable.value || !canMove(card, context.value)) return [{ id: 'lock', label: text('Locked', 'Gesperrt'), detail: [card.lock?.why, card.lock?.who, card.lock?.at, card.lock?.scope].filter(Boolean).join(' · '), icon: 'lock' }]
  if (context.value.layer === 'rules') return [ { id: 'top', label: text('Pin to top', 'Oben anpinnen'), icon: 'pin' }, { id: 'free', label: text('No rule', 'Keine Regel'), icon: 'minus' }, { id: 'bottom', label: text('Pin to bottom', 'Unten anpinnen'), icon: 'pin' }, { id: 'not', label: text('Not allowed', 'Nicht erlaubt'), icon: 'not' } ]
  return current.zone === 'not' ? [{ id: 'allow', label: text('Allow again', 'Wieder erlauben'), icon: 'check' }] : [
    { id: 'first', label: text('Move to the top', 'Ganz nach oben'), icon: 'to-top' }, { id: 'up', label: text('Move up', 'Nach oben'), icon: 'arrow-up' }, { id: 'down', label: text('Move down', 'Nach unten'), icon: 'arrow-down' }, { id: 'last', label: text('Move to the bottom', 'Ganz nach unten'), icon: 'arrow-down' }, { id: 'not', label: text('Not allowed', 'Nicht erlaubt'), icon: 'not', separated: true },
  ]
})
function currentAction() { return !!active.value && active.value.key === editor.actionKey.value && active.value.revision === (board.value?.revision ?? -1) && active.value.rulesRevision === editor.rules.value?.revision && !busy.value }
async function setContext(value: BoardContext) {
  menuOpen.value = false; reasonOpen.value = false; context.value = value; emit('context', value)
  await router.replace({ query: { ...route.query, layer: value.layer, project_id: value.project || undefined, situation: value.situation } })
}
async function focusCard(column: string, line: string) {
  await nextTick()
  root.value?.querySelector<HTMLElement>(`[data-column="${CSS.escape(column)}"] [data-line="${CSS.escape(line)}"]`)?.focus({ preventScroll: true })
}
async function move(column: Column, card: BoardCard, zone: BoardZone, index: number, eventAnchor?: HTMLElement) {
  if (!editable.value || !canMove(card, context.value) || busy.value) return
  if (context.value.layer === 'rules' && zone !== 'free' && (!card.lock || card.lock.value !== zone)) {
    active.value = { column, card, key: editor.actionKey.value, revision: board.value!.revision, rulesRevision: editor.rules.value?.revision }
    pending.value = { column, card, zone, index }; why.value = ''; anchor.value = eventAnchor ?? root.value?.querySelector<HTMLElement>(`[data-column="${CSS.escape(column.column)}"] [data-line="${CSS.escape(card.line)}"]`) ?? anchor.value
    menuOpen.value = false; reasonOpen.value = true; return
  }
  const saved = await editor.move(column, card, zone, index, card.lock?.why ?? '')
  if (saved) await focusCard(column.column, card.line)
}
async function select(id: string) {
  if (!currentAction()) { menuOpen.value = false; return }
  const current = active.value!; menuOpen.value = false
  if (menu.value === 'show') { await setContext({ ...context.value, layer: id === 'project-rules' || id === 'rules' ? 'rules' : id as 'mine' | 'default', project: id === 'rules' ? undefined : id === 'project-rules' ? context.value.project ?? projects.value[0]?.id : context.value.project }); return }
  if (menu.value === 'project') { await setContext({ ...context.value, project: id || undefined }); return }
  if (menu.value === 'providers') { if (id === 'read-providers') return; await editor.profile({ residency: id === 'inherit' ? null : id as 'eu' | 'local' }); return }
  if (menu.value === 'column' && id === 'add-rule') { menu.value = 'rule-line'; await nextTick(); menuOpen.value = true; return }
  if (menu.value === 'rule-line') {
    const card = [...(newCards.value), ...(board.value?.columns.flatMap(value => zonesFor(context.value.layer).flatMap(zone => value[zone])) ?? [])].find(value => value.line === id)
    if (card) { active.value = { ...current, card: { ...card, lock: undefined } }; menu.value = 'move'; await nextTick(); menuOpen.value = true }; return
  }
  if (menu.value === 'column') { await editor.profile({ hidden_kinds: [...(board.value?.profile.hidden_kinds ?? []), current.column!.column] }); return }
  if (menu.value === 'columns') { if (id === 'create-kind') { await router.push('/settings/kinds'); return }; await editor.profile({ hidden_kinds: board.value!.profile.hidden_kinds.filter(value => value !== id) }); return }
  if (menu.value === 'tray') { if (id === 'dismiss-line') { await editor.dismiss(current.card!.line); return }; const column = columns.value.find(value => value.column === id); if (column) await move(column, { ...current.card!, lock: undefined }, 'list', column.list.filter(card => !card.lock).length); return }
  if (id === 'lock') return
  const column = current.column!, card = current.card!
  const own = column.list.filter(value => !value.lock), index = own.findIndex(value => value.line === card.line)
  if (id === 'up' || id === 'down') { const target = stepTarget(column, card.line, id === 'up' ? -1 : 1, context.value); if (target) await move(column, card, target.zone, target.index); return }
  const zone: BoardZone = context.value.layer === 'rules' ? id as BoardZone : id === 'not' ? 'not' : 'list'
  const at = context.value.layer === 'rules' && (zone === 'top' || zone === 'bottom') ? column[zone].length : id === 'last' ? own.length - 1 : id === 'first' ? 0 : id === 'allow' ? own.length : index
  await move(column, card, zone, at)
}
async function saveReason() {
  if (!pending.value || !currentAction() || !why.value.trim()) { if (!why.value.trim()) error.value = text('One sentence is required.', 'Ein Satz ist erforderlich.'); return }
  const action = pending.value
  if (await editor.move(action.column, action.card, action.zone, action.index, why.value)) { reasonOpen.value = false; pending.value = undefined; await focusCard(action.column.column, action.card.line) }
}
async function keys(column: Column, card: BoardCard, event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.isComposing) return
  if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key)) return
  if (event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) { event.preventDefault(); const target = stepTarget(column, card.line, event.key === 'ArrowUp' ? -1 : 1, context.value); if (target) await move(column, card, target.zone, target.index); return }
  if (event.altKey) return
  event.preventDefault()
  if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { const adjacent = columns.value[columns.value.indexOf(column) + (event.key === 'ArrowLeft' ? -1 : 1)]; if (adjacent) { const next = adjacent.list[0] ?? adjacent.top[0] ?? adjacent.not[0]; if (next) await focusCard(adjacent.column, next.line) }; return }
  const cards = zonesFor(context.value.layer).flatMap(zone => column[zone]), index = cards.findIndex(value => value.line === card.line)
  const next = cards[index + (event.key === 'ArrowUp' ? -1 : 1)]; if (next) await focusCard(column.column, next.line)
}
type Drag = { column?: Column; card: BoardCard; element: HTMLElement; pointer: number; x: number; y: number; dx: number; dy: number; key: string; revision: number; rulesRevision?: number }
let press: Drag | undefined, suppressClick = false
const dragging = ref(''), ghost = ref<{ card: BoardCard; left: number; top: number; width: number }>(), target = ref<{ column: string; zone: BoardZone; index: number }>()
function startPress(column: Column | undefined, card: BoardCard, event: PointerEvent) {
  if (event.button !== 0 || event.pointerType === 'touch' || !editable.value || busy.value || !canMove(card, context.value) || context.value.layer === 'rules' && card.lock) return
  const element = event.currentTarget as HTMLElement, rect = element.getBoundingClientRect()
  press = { column, card, element, pointer: event.pointerId, x: event.clientX, y: event.clientY, dx: event.clientX - rect.left, dy: event.clientY - rect.top, key: editor.actionKey.value, revision: board.value!.revision, rulesRevision: editor.rules.value?.revision }
}
function dragMove(event: PointerEvent) {
  if (!press || event.pointerId !== press.pointer || press.key !== editor.actionKey.value || press.revision !== board.value?.revision) return
  if (!ghost.value && Math.hypot(event.clientX - press.x, event.clientY - press.y) < 5) return
  event.preventDefault(); suppressClick = true
  const width = press.element.getBoundingClientRect().width
  dragging.value = `${press.column?.column ?? 'tray'}/${press.card.line}`
  ghost.value = { card: press.card, width, left: event.clientX - press.dx, top: event.clientY - press.dy }
  const zone = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[data-zone]'), element = zone?.closest<HTMLElement>('[data-column]')
  target.value = undefined
  if (!zone || !element || !root.value?.contains(element) || (press.column && element.dataset.column !== press.column.column)) return
  const column = columns.value.find(value => value.column === element.dataset.column), zoneName = zone.dataset.zone as BoardZone
  if (!column || !zonesFor(context.value.layer).includes(zoneName) || !press.column && (zoneName === 'not' || column.cant.some(item => item.line === press!.card.line) || column.not.some(card => card.line === press!.card.line))) return
  const eligible = [...zone.querySelectorAll<HTMLElement>('[data-card]')].filter(item => item.dataset.card !== press!.card.line && canMove(column[zoneName].find(card => card.line === item.dataset.card)!, context.value))
  const index = eligible.findIndex(item => { const rect = item.getBoundingClientRect(); return event.clientY < rect.top + rect.height / 2 })
  target.value = { column: column.column, zone: zoneName, index: index < 0 ? eligible.length : index }
}
function clearDrag() { suppressClick = false; press = undefined; dragging.value = ''; ghost.value = undefined; target.value = undefined }
function finishDrag(event: PointerEvent) {
  if (!press || event.pointerId !== press.pointer) return
  const action = press, destination = target.value, started = !!ghost.value
  clearDrag(); suppressClick = started
  if (started && destination && action.key === editor.actionKey.value && action.revision === board.value?.revision) { const column = columns.value.find(value => value.column === destination.column); if (column) void move(column, action.card, destination.zone, destination.index, action.element) }
  if (started) setTimeout(() => { suppressClick = false }, 0)
}
function clickCapture(event: MouseEvent) { if (suppressClick) { event.preventDefault(); event.stopImmediatePropagation(); suppressClick = false } }
function escapeDrag(event: KeyboardEvent) { if (event.key === 'Escape' && ghost.value) { event.preventDefault(); event.stopImmediatePropagation(); clearDrag() } }
async function fullScreen(event: MouseEvent) { rememberBoardPosition(event.currentTarget as HTMLElement, session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : ''); await router.push({ path: '/settings/models/board', query: { ...route.query, layer: context.value.layer, project_id: context.value.project, situation: context.value.situation } }) }
watch(editor.key, () => { discovered.value = [] }, { flush: 'sync' })
watch(editor.actionKey, () => { menuOpen.value = false; reasonOpen.value = false; templateOpen.value = false; templatePreview.value = null; pending.value = undefined; active.value = undefined; why.value = ''; clearDrag() }, { flush: 'sync' })
watch(board, value => { if (value) emit('change', value) })
onMounted(() => { if (!props.projects) void projectStore.load(); document.addEventListener('pointermove', dragMove, { passive: false }); document.addEventListener('pointerup', finishDrag); document.addEventListener('pointercancel', clearDrag); document.addEventListener('click', clickCapture, true); window.addEventListener('keydown', escapeDrag, true) })
onBeforeUnmount(() => { clearDrag(); document.removeEventListener('pointermove', dragMove); document.removeEventListener('pointerup', finishDrag); document.removeEventListener('pointercancel', clearDrag); document.removeEventListener('click', clickCapture, true); window.removeEventListener('keydown', escapeDrag, true) })
defineExpose({ editor, context, previewTemplate, closePopover: () => { menuOpen.value = false; reasonOpen.value = false } })
</script>
<template>
  <div ref="root" class="boardwrap" :class="{ full }" data-model-board :data-board-ready="!!board" :aria-busy="busy || loading">
    <div class="bhead">
      <span class="dd"><span>{{ text('Show', 'Zeigen') }}</span><button type="button" class="scope-btn" :aria-label="`${text('Show', 'Zeigen')}: ${showLabel}`" aria-haspopup="menu" data-board-show @click="bind('show', $event)"><span class="select-value"><span v-clip-tip="showLabel">{{ showLabel }}</span><span v-for="label in [text(`Mine (${session.identity?.principal.name ?? ''})`, `Meine (${session.identity?.principal.name ?? ''})`), text('Workspace default', 'Vorgabe des Arbeitsbereichs'), text('Workspace rules', 'Regeln des Arbeitsbereichs'), text('Project rules', 'Projektregeln')]" :key="label" class="reserve" aria-hidden="true">{{ label }}</span></span><AppIcon name="chevron" :size="14" /></button></span>
      <span v-if="context.layer !== 'rules' || context.project" class="dd"><span>{{ text('in', 'in') }}</span><button type="button" class="scope-btn" :aria-label="text('Project', 'Projekt')" aria-haspopup="menu" data-board-project @click="bind('project', $event)"><span class="select-value"><span v-clip-tip="projectLabel">{{ projectLabel }}</span><span v-for="label in projectLabels" :key="label" class="reserve" aria-hidden="true">{{ label }}</span></span><AppIcon name="chevron" :size="14" /></button></span>
      <span class="dd"><span>{{ text('Providers', 'Anbieter') }}</span><button type="button" class="scope-btn" :aria-label="`${text('Providers', 'Anbieter')}: ${providerLabel}`" aria-haspopup="menu" data-board-providers @click="bind('providers', $event)"><span class="select-value"><span v-clip-tip="providerLabel">{{ providerLabel }}</span><span v-for="label in [text('As the workspace', 'Wie Arbeitsbereich'), text('Any provider', 'Jeder Anbieter'), text('EU-hosted only', 'Nur EU-gehostet'), text('Local only', 'Nur lokal')]" :key="label" class="reserve" aria-hidden="true">{{ label }}</span></span><AppIcon name="chevron" :size="14" /></button></span>
      <span class="grow" />
      <span class="bacts"><RouterLink class="btn sm ghost" to="/settings/kinds"><AppIcon name="list" :size="14" />{{ text('Kinds of work', 'Arten von Arbeit') }}</RouterLink><button v-if="editable && context.layer !== 'rules'" type="button" class="btn sm" aria-haspopup="menu" data-board-columns @click="bind('columns', $event)"><AppIcon name="plus" :size="14" />{{ text('Column', 'Spalte') }}</button><button v-if="!full" type="button" class="btn sm" data-models-fullscreen @click="fullScreen"><AppIcon name="expand" :size="14" />{{ text('Full screen', 'Vollbild') }}</button></span>
    </div>
    <p class="layer-note"><span>{{ note }}</span><span v-if="!editable" class="ro"><AppIcon name="lock" :size="12" />{{ text('Read only: admins change this.', 'Nur lesen: Admins ändern das.') }}</span><span v-else class="bhelp">{{ text('Drag, or focus a card and press Alt+↑/↓', 'Ziehen, oder Karte fokussieren und Alt+↑/↓') }}</span></p>
    <div class="feedback" aria-live="polite"><span v-if="error" role="alert">{{ error }}</span><button v-if="!board && !loading" type="button" class="link-btn" @click="editor.load()">{{ text('Reload', 'Neu laden') }}</button></div>
    <template v-if="board">
      <p v-if="board.residency.effective !== 'any' && columns.length && columns.every(column => !column.list.length)" class="provider-note" role="status"><AppIcon name="server" :size="14" />{{ text(`${providerLabel}: no route qualifies today, so this work would wait. Every card is held under Not allowed.`, `${providerLabel}: Heute erfüllt keine Route das, diese Arbeit würde warten. Jede Karte steht unter Nicht erlaubt.`) }}</p>
      <NewTray :cards="context.layer === 'rules' ? [] : newCards" :columns="board.columns" :editable="editable && !busy" :german="german" @open="(card, event) => bind('tray', event, undefined, card)" @press="(card, event) => startPress(undefined, card, event)" />
      <div class="board" data-board-scroll :style="{ '--board-columns': columns.length, '--board-slots': slots, '--board-head': full ? '120px' : '72px' }">
        <BoardColumn v-for="column in columns" :key="column.column" :column="column" :context="context" :editable="editable" :busy="busy" :german="german" :full="full" :inherited="context.layer === 'mine' && (board.profile.scope === 'workspace' || !board.profile.template)" :template="board.profile.template" :dragging="dragging" :target="target" @open="(value, card, zone, event) => bind('move', event, value, card, zone)" @keys="keys" @press="startPress" @menu="(column, event) => bind('column', event, column)" @reset="editor.reset" />
      </div>
      <p v-if="!columns.length" class="state">{{ text('No columns to show.', 'Keine Spalten zum Anzeigen.') }}</p>
    </template>
    <p v-else class="state">{{ loading ? text('Loading the model board…', 'Modellboard wird geladen…') : text('The model board is unavailable.', 'Das Modellboard ist nicht verfügbar.') }}</p>
    <p class="sr-only">{{ text('Alt+Up and Alt+Down move the card. Enter opens Move to.', 'Alt+Pfeil hoch und runter verschieben die Karte. Eingabe öffnet Verschieben nach.') }}</p><p class="sr-only" aria-live="polite">{{ announcement }}</p>
    <SettingsPopover v-model:open="menuOpen" :anchor="anchor" :label="menuLabel" :items="items" :context-key="editor.actionKey.value" @select="select" />
    <SettingsPopover v-model:open="reasonOpen" :anchor="anchor" :label="text('Why this rule?', 'Warum diese Regel?')" mode="form" :context-key="editor.actionKey.value" :hint="text('People see this next to the lock', 'Personen sehen das neben dem Schloss')" :save-label="text('Save reason', 'Grund speichern')" :error="error" :busy="busy" @submit="saveReason"><template #default="{ hintId }"><label class="reason-label">{{ pending ? `${lineName(pending.card)} · ${pending.column.label}` : '' }}<input v-model="why" class="field" maxlength="1000" :aria-describedby="hintId" :placeholder="text('One sentence', 'Ein Satz')" /></label></template></SettingsPopover>
    <SettingsPopover v-model:open="templateOpen" :anchor="anchor" :label="text('Apply template', 'Vorlage anwenden')" mode="form" :context-key="editor.actionKey.value" :hint="text('Columns you ordered yourself stay.', 'Selbst geordnete Spalten bleiben.')" :save-label="text('Apply', 'Anwenden')" :error="error" :busy="busy" @submit="applyTemplate"><p>{{ text('Now → After', 'Jetzt → Danach') }}</p><dl class="template-diff"><template v-for="change in templatePreview?.moved" :key="change.column"><dt>{{ board?.columns.find(column => column.column === change.column)?.label ?? change.column }}</dt><dd>{{ change.before.map(line => lineName({ line, version: '' })).join(' → ') }}<br />{{ change.after.map(line => lineName({ line, version: '' })).join(' → ') }}</dd></template></dl><p v-if="!templatePreview?.moved.length">{{ text('No column changes.', 'Keine Spaltenänderungen.') }}</p></SettingsPopover>
    <Teleport to="body"><div v-if="ghost" class="drag-ghost" data-board-ghost aria-hidden="true" :style="{ left: `${ghost.left}px`, top: `${ghost.top}px`, width: `${ghost.width}px` }"><ModelCard :card="ghost.card" :movable="false" :german="german" /></div></Teleport>
  </div>
</template>
<style scoped>
.boardwrap { min-width: 0; max-width: 100%; margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--line); }.boardwrap.full { margin: 0; padding: 0; border: 0; }.bhead { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; }.grow { flex: 1; }.dd, .bacts { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 8px; min-width: 0; }.dd > span { font-size: 12.5px; color: var(--ink-2); }.scope-btn { display: inline-flex; align-items: center; justify-content: space-between; gap: 8px; max-width: 100%; min-height: 32px; padding: 0 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface-raised); color: var(--ink); font-size: 13px; }.select-value { display: grid; min-width: 0; text-align: left; }.select-value > span { grid-area: 1 / 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }.select-value .reserve { visibility: hidden; pointer-events: none; }.scope-btn svg { flex: none; }.layer-note { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px; min-height: 24px; margin: 8px 0 0; color: var(--ink-2); font-size: 12px; }.layer-note > span:first-child { flex: 1 1 320px; }.ro, .bhelp { display: inline-flex; align-items: center; gap: 6px; color: var(--ink-3); }.feedback { position: relative; height: 0; z-index: 4; }.feedback > span { position: absolute; top: 4px; left: 0; padding: 8px 12px; max-width: min(480px, 100%); border: 1px solid var(--line); border-radius: 8px; background: var(--surface-raised); box-shadow: var(--shadow-pop); color: var(--danger); font-size: 12px; }.board { display: grid; grid-template-columns: repeat(var(--board-columns, 8), minmax(176px, 1fr)); gap: 12px; margin-top: 16px; padding: 2px 2px 8px; overflow-x: auto; align-items: stretch; scrollbar-width: thin; overscroll-behavior-x: contain; }.full .board { grid-template-columns: repeat(var(--board-columns, 8), minmax(168px, 1fr)); gap: 8px; }.full :deep(.bcol) { padding: 10px; }.full :deep(.mc) { grid-template-columns: 16px minmax(0, 1fr) 12px; column-gap: 4px; padding: 6px 6px 6px 4px; }.provider-note { display: flex; align-items: center; gap: 8px; margin-top: 12px; font-size: 12px; color: var(--ink-2); }.state { padding: 24px 0; color: var(--ink-3); }.template-diff { font-size: 12px; line-height: 1.5; }.template-diff dt { margin-top: 8px; font-weight: 600; }.template-diff dd { margin: 4px 0; overflow-wrap: anywhere; color: var(--ink-2); }.reason-label { display: grid; gap: 8px; font-size: 12px; }.drag-ghost { position: fixed; z-index: 90; pointer-events: none; transform: rotate(-1deg); }.drag-ghost :deep(.mc) { box-shadow: var(--shadow-pop); }@media (max-width: 860px) { .board, .full .board { grid-template-columns: repeat(var(--board-columns, 8), minmax(min(80vw, 280px), 1fr)); scroll-snap-type: x mandatory; }.grow { display: none; }.bacts { flex-basis: 100%; }.scope-btn, .btn, .link-btn { min-height: 44px; }.layer-note > span:first-child { flex-basis: 100%; } }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
