<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Kind } from '../../lib/api'
import { isIssueKind } from '../../lib/kindConvert'
import { kinds } from '../../lib/useTicket'
import AppIcon, { type IconName } from '../AppIcon.vue'
import { WORK_ICONS, workNoun } from '../../lib/workVocabulary'
import { kindLabel } from '../../lib/work'
import FloatingPanel from './FloatingPanel.vue'

const props = defineProps<{
  levelName?: string; levelIcon?: string;
  ticketKey: string; kind: string | null; position: { index: number; count: number } | null
  mode: 'panel' | 'full'; canWrite: boolean; canMove: boolean; canDelete: boolean; canWorkActions?: boolean
  canRepeat?: boolean
  recurrenceLabel?: string
  canEditRecurrence?: boolean
  // Keys of the tickets followed to get here (oldest first), and the edit state.
  trail?: string[]; editing?: boolean; saving?: boolean; dirty?: boolean
  // The peek dock: a labeled way into the project, and a way back to the view it covered.
  openInProject?: boolean; backLabel?: string
}>()
const emit = defineEmits<{
  copyKey: []; copyLink: []; prev: []; next: []; expand: []; collapse: []; newTab: []; close: []; move: [anchor: HTMLElement]; delete: []; convert: []
  back: [steps: number]; edit: []; save: []; cancel: []; openInProject: []; workActions: []
  repeat: []; editRecurrence: []
}>()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
// The trail shows its last two steps; older ones fold into an ellipsis.
const crumbs = () => (props.trail ?? []).map((key, index, all) => ({ key, steps: all.length - index })).slice(-2)
const moreAnchor = ref<HTMLElement | null>(null)
// Phones fold previous/next into More, so the bar fits in one row.
const phoneQuery = window.matchMedia('(max-width: 600px)')
const phone = ref(phoneQuery.matches)
const onPhone = (event: MediaQueryListEvent) => { phone.value = event.matches }
phoneQuery.addEventListener('change', onPhone)
onBeforeUnmount(() => phoneQuery.removeEventListener('change', onPhone))
const moreButton = ref<HTMLButtonElement>()
function toggleMore(event: MouseEvent) { moreAnchor.value = moreAnchor.value ? null : event.currentTarget as HTMLElement }
function closeMore(restore: boolean) { moreAnchor.value = null; if (restore) moreButton.value?.focus() }
function pick(action: 'copyKey' | 'copyLink' | 'delete' | 'prev' | 'next') {
  moreAnchor.value = null
  if (action === 'prev') emit('prev')
  else if (action === 'next') emit('next')
  else if (action === 'copyKey') emit('copyKey')
  else if (action === 'copyLink') emit('copyLink')
  else emit('delete')
}
function pickMove() { const anchor = moreButton.value ?? null; moreAnchor.value = null; if (anchor) emit('move', anchor) }
function pickConvert() { moreAnchor.value = null; emit('convert') }
function pickView() { moreAnchor.value = null; if (props.mode === 'panel') emit('expand'); else emit('collapse') }
function pickNewTab() { moreAnchor.value = null; emit('newTab') }
function pickWorkActions() { moreAnchor.value = null; emit('workActions') }
const catalog = ref<Kind[]>([])
onMounted(() => { void kinds().then(rows => { catalog.value = rows }).catch(() => {}) })
// The bar folds only when its actions do not fit, measured rather than guessed
// from a width (wider Linux fonts pushed Close out of the default dock): first
// the wide-only actions (also in More), then the position, then Queue's word,
// then Edit's word (its aria-label keeps the name; tablet docks need it).
// Each fit starts unfolded and runs synchronously inside the observer callback,
// so nothing paints in between; folding never changes the bar's own width, so
// it cannot oscillate. data-fold is outside Vue's class patching.
const main = ref<HTMLElement>()
let sizing: ResizeObserver | undefined, changes: MutationObserver | undefined
function fit() {
  const el = main.value, bar = el?.parentElement
  if (!el || !bar) return
  let level = 0
  bar.dataset.fold = '0'
  while (level < 4 && el.clientWidth > 0 && el.scrollWidth > el.clientWidth) bar.dataset.fold = String(++level)
}
onMounted(() => {
  if (!main.value || typeof ResizeObserver === 'undefined') return
  sizing = new ResizeObserver(fit)
  sizing.observe(main.value)
  // Slot content (Queue's count, Follow) and edit mode change the needed width.
  changes = new MutationObserver(fit)
  changes.observe(main.value, { subtree: true, childList: true, characterData: true })
  fit()
})
onBeforeUnmount(() => { sizing?.disconnect(); changes?.disconnect() })
const displayIcon = computed<IconName>(() => WORK_ICONS.includes(props.levelIcon as typeof WORK_ICONS[number]) ? props.levelIcon as IconName : props.kind === 'epic' ? 'epic' : props.kind === 'task' ? 'task' : 'ticket')
const canConvert = computed(() => props.kind !== 'work' && props.canWrite && !!props.kind && isIssueKind(catalog.value.find(kind => kind.slug === props.kind) ?? props.kind))
function focusMore() { moreButton.value?.focus() }
function menuKeys(event: KeyboardEvent) {
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault(); event.stopPropagation()
    items[event.key === 'ArrowDown' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)]?.focus()
  }
}
defineExpose({ closeMore, focusMore })
void props
</script>

<template>
  <header class="panel-bar" :class="[mode, { 'has-trail': !!trail?.length, 'has-peek-actions': !!(openInProject || backLabel) }]">
    <div ref="main" class="panel-bar-main">
    <button v-if="mode === 'full'" type="button" class="icon-btn sm flat" aria-label="Back to the list" data-tip="Back to the list · Esc" @click="emit('close')"><AppIcon name="chevron-left" :size="16" /></button>
    <template v-if="trail?.length">
      <button type="button" class="icon-btn sm flat back-btn" :aria-label="`Back to ${trail[trail.length - 1]}`" aria-keyshortcuts="Alt+ArrowLeft" :data-tip="`Back to ${trail[trail.length - 1]} · ${mac ? 'Option' : 'Alt'} Left arrow`" @click="emit('back', 1)"><AppIcon name="arrow-left" :size="15" /></button>
      <nav class="trail" aria-label="Followed tickets">
        <span v-if="trail.length > 1" class="trail-more" :class="{ always: trail.length > 2 }" aria-hidden="true">…</span>
        <span v-for="crumb in crumbs()" :key="crumb.key + crumb.steps" class="crumb-item">
          <button type="button" class="crumb mono" :data-tip="`Back to ${crumb.key}`" @click="emit('back', crumb.steps)">{{ crumb.key }}</button>
          <AppIcon name="chevron-right" :size="12" class="crumb-sep" />
        </span>
      </nav>
    </template>
    <button type="button" class="key-chip" :aria-label="`Copy ${ticketKey}`" :data-tip="`Copy ${ticketKey}`" @click="emit('copyKey')">
      <AppIcon v-if="kind" :name="displayIcon" :data-tip="levelName" :size="12" :class="kind" />
      <span>{{ ticketKey }}</span>
      <AppIcon name="copy" :size="11" class="copy-glyph" />
    </button>
    <span v-if="position" class="position mono">{{ position.index + 1 }} / {{ position.count }}</span>
    <div class="nav">
      <button type="button" class="icon-btn sm flat" aria-label="Previous ticket" aria-keyshortcuts="k" data-tip="Previous · k" :disabled="!position || position.index === 0" @click="emit('prev')"><AppIcon name="chevron-up" :size="15" /></button>
      <button type="button" class="icon-btn sm flat" aria-label="Next ticket" aria-keyshortcuts="j" data-tip="Next · j" :disabled="!position || position.index >= position.count - 1" @click="emit('next')"><AppIcon name="chevron" :size="15" /></button>
    </div>
    <span class="spacer" />
    <template v-if="editing">
      <span v-if="dirty" class="unsaved" aria-live="polite">Unsaved</span>
      <button type="button" class="btn sm ghost" :disabled="saving" aria-keyshortcuts="Escape" data-tip="Cancel · Esc" @click="emit('cancel')">Cancel</button>
      <button type="button" class="btn sm primary" :disabled="saving" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" :data-tip="`Save · ${mac ? 'Cmd' : 'Ctrl'} Enter`" @click="emit('save')"><AppIcon name="check" :size="13" />{{ saving ? 'Saving…' : 'Save' }}</button>
    </template>
    <template v-else>
    <slot name="queue" />
    <button v-if="canWrite" type="button" class="btn sm edit-btn" aria-label="Edit" aria-keyshortcuts="e" data-tip="Edit title, text and properties · e" @click="emit('edit')"><AppIcon name="edit" :size="13" /><span class="edit-word">Edit</span></button>
    <button v-if="mode === 'panel'" type="button" class="icon-btn sm flat wide-only" aria-label="Open as full page" data-tip="Full page · f" @click="emit('expand')"><AppIcon name="expand" :size="14" /></button>
    <button v-else type="button" class="icon-btn sm flat wide-only" aria-label="Show beside the list" data-tip="Side panel · f" @click="emit('collapse')"><AppIcon name="collapse" :size="14" /></button>
    <button type="button" class="icon-btn sm flat wide-only" aria-label="Open in a new tab" data-tip="Open in new tab" @click="emit('newTab')"><AppIcon name="external" :size="14" /></button>
    <button ref="moreButton" type="button" class="icon-btn sm flat" aria-label="More actions" aria-haspopup="menu" :aria-expanded="!!moreAnchor" data-tip="More" @click="toggleMore"><AppIcon name="more" :size="15" /></button>
    <button v-if="mode === 'panel'" type="button" class="icon-btn sm flat" aria-label="Close ticket details" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
    </template>
    <FloatingPanel v-if="moreAnchor" :anchor="moreAnchor" :width="220" align="end" :label="`Actions for ${ticketKey}`" @close="closeMore">
      <div class="more-menu" role="menu" :aria-label="`Actions for ${ticketKey}`" @keydown="menuKeys">
        <template v-if="phone && position">
          <button type="button" role="menuitem" class="menu-item" :disabled="position.index === 0" @click="pick('prev')"><AppIcon name="chevron-up" :size="14" />Previous ticket</button>
          <button type="button" role="menuitem" class="menu-item" :disabled="position.index >= position.count - 1" @click="pick('next')"><AppIcon name="chevron" :size="14" />Next ticket</button>
          <div class="menu-sep" role="separator" />
        </template>
        <button type="button" role="menuitem" class="menu-item" data-autofocus @click="pick('copyLink')"><AppIcon name="link" :size="14" />Copy link</button>
        <button type="button" role="menuitem" class="menu-item" @click="pick('copyKey')"><AppIcon name="copy" :size="14" />Copy key</button>
        <button v-if="kind && ['work', 'epic', 'ticket', 'task'].includes(kind)" type="button" role="menuitem" class="menu-item" :disabled="!canRepeat" aria-keyshortcuts="Shift+R" :data-tip="canRepeat ? 'Repeat · Shift R' : 'Needs the Manage recurring work permission'" @click="closeMore(false); emit('repeat')"><AppIcon name="repeat" :size="14" /><span>Repeat…<small v-if="!canRepeat" style="display: block; font-size: 11.5px; color: var(--ink-3)">Needs the Manage recurring work permission</small></span></button>
        <button v-if="recurrenceLabel" type="button" role="menuitem" class="menu-item" :disabled="!canEditRecurrence" :data-tip="!canEditRecurrence ? 'Needs the Manage recurring work permission' : undefined" @click="closeMore(false); emit('editRecurrence')"><AppIcon name="edit" :size="14" /><span>Edit {{ recurrenceLabel }}…</span></button>
        <button type="button" role="menuitem" class="menu-item" @click="pickView"><AppIcon :name="mode === 'panel' ? 'expand' : 'collapse'" :size="14" />{{ mode === 'panel' ? 'Open as full page' : 'Show beside the list' }}</button>
        <button type="button" role="menuitem" class="menu-item" @click="pickNewTab"><AppIcon name="external" :size="14" />Open in a new tab</button>
        <button v-if="canWorkActions" type="button" role="menuitem" class="menu-item" @click="pickWorkActions"><AppIcon name="task" :size="14" />Work actions</button>
        <button v-if="canConvert" type="button" role="menuitem" class="menu-item" @click="pickConvert"><AppIcon name="refresh" :size="14" />Convert to…</button>
        <button v-if="canMove" type="button" role="menuitem" class="menu-item" @click="pickMove"><AppIcon name="epic" :size="14" />Move to another parent…</button>
        <div v-if="canMove || canDelete" class="menu-sep" role="separator" />
        <button v-if="canDelete" type="button" role="menuitem" class="menu-item danger" @click="pick('delete')"><AppIcon name="trash" :size="14" />Delete {{ workNoun(levelName || kindLabel(kind || 'ticket')) }}…</button>
      </div>
    </FloatingPanel>
    </div>
    <div v-if="openInProject || backLabel" class="peek-actions">
      <button v-if="backLabel" type="button" class="btn sm ghost" @click="emit('close')"><AppIcon name="arrow-left" :size="14" />{{ backLabel }}</button>
      <button v-if="openInProject" type="button" class="btn sm" @click="emit('openInProject')"><AppIcon name="folder" :size="14" />Open in project</button>
    </div>
    <div v-if="$slots.marker" class="header-marker"><slot name="marker" /></div>
  </header>
</template>

<style scoped>
.panel-bar { container: panel-bar / inline-size; display: flex; flex-direction: column; align-items: stretch; flex-shrink: 0; padding: 0 10px 0 14px; border-bottom: 1px solid var(--line); }
.panel-bar-main { display: flex; align-items: center; gap: 6px; height: 52px; min-width: 0; }
.panel-bar.full { padding-left: 8px; }
.panel-bar.full .panel-bar-main { height: 44px; }
.peek-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 0 0 8px; }
.peek-actions .btn { min-width: 0; }
.peek-actions .btn svg { flex: none; }
.header-marker { display: flex; align-items: center; padding: 0 0 8px; }
.key-chip { display: inline-flex; flex-shrink: 0; align-items: center; gap: 6px; height: 26px; white-space: nowrap; padding: 0 9px 0 10px; border: 0; border-radius: 7px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 12px/1 var(--mono); letter-spacing: .03em; font-variant-ligatures: none; }
.key-chip:hover { box-shadow: inset 0 0 0 1px var(--teal); }
.key-chip:active { filter: brightness(.97); }
.key-chip:focus-visible { box-shadow: var(--focus-ring); }
.key-chip .epic { color: var(--gold); }
.copy-glyph { opacity: .45; }
.key-chip:hover .copy-glyph { opacity: .9; }
.position { flex-shrink: 0; margin-left: 6px; font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.nav { display: inline-flex; gap: 2px; margin-left: 2px; }
.nav .icon-btn:disabled { opacity: .35; }
.spacer { flex: 1; }
.back-btn { margin-right: -2px; }
.trail { display: inline-flex; align-items: center; gap: 2px; min-width: 0; }
.crumb { height: 24px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-2); font: 500 11.5px/1 var(--mono); white-space: nowrap; font-variant-ligatures: none; }
.crumb:hover { color: var(--teal-ink); background: var(--row-hover); }
.crumb:focus-visible { box-shadow: var(--focus-ring); }
.crumb-sep { flex-shrink: 0; color: var(--ink-3); }
.crumb-item { display: inline-flex; align-items: center; gap: 2px; }
.trail-more { display: none; padding: 0 2px; color: var(--ink-3); }
.trail-more.always { display: inline; }
.edit-btn { gap: 6px; margin-right: 4px; }
.unsaved { font-size: 12px; color: var(--gold-ink); font-weight: 600; margin-right: 4px; }
.more-menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item svg { color: var(--ink-2); }
@media (hover: hover) { .menu-item:hover:not(:disabled) { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item.danger, .menu-item.danger svg { color: var(--danger); }
.menu-item.danger:hover:not(:disabled) { background: var(--danger-bg); }
.menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }
/* Narrow headers keep Back; breadcrumb text must not compete with the current
   key and actions, even when the surrounding viewport is wide. */
@container panel-bar (max-width: 640px) {
  .has-trail .position, .trail { display: none; }
}
/* A narrow dock uses More for these actions, independently of viewport width. */
@container panel-bar (max-width: 420px) {
  .wide-only, .position { display: none; }
}
/* Measured folds (see fit); each level keeps the ones before it. */
.panel-bar:is([data-fold="1"], [data-fold="2"], [data-fold="3"], [data-fold="4"]) .wide-only, .panel-bar:is([data-fold="2"], [data-fold="3"], [data-fold="4"]) .position { display: none; }
.panel-bar:is([data-fold="3"], [data-fold="4"]) :deep(.q-word), .panel-bar:is([data-fold="3"], [data-fold="4"]) :deep(.q-key) { display: none; }
.panel-bar[data-fold="4"] .edit-word { display: none; }
@media (max-width: 720px) {
  .panel-bar { padding: 0 6px 0 12px; }
  .panel-bar-main { height: 56px; }
  .panel-bar .icon-btn { width: 44px; height: 44px; }
  .peek-actions .btn { min-height: 40px; }
  .position { display: none; }
  .wide-only { display: none; }
  .nav { gap: 0; }
  .trail, .position { display: none; }
  .edit-btn { height: 40px; }
}
@media (max-width: 600px) { .nav { display: none; } }
</style>
