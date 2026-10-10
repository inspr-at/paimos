<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { scopeFor, textState, type InspectorTab } from '../../../lib/quotes/inspector'
import type { QuoteEditor } from '../../../lib/quotes/editor'
import type { SectionActions } from '../../../lib/quotes/sectionActions'
import { toast } from '../../../lib/toast'
import InspectorDocument from './InspectorDocument.vue'
import InspectorSection from './InspectorSection.vue'
import InspectorText from './InspectorText.vue'
import QuoteIcon from './QuoteIcon.vue'

// The format inspector: three scopes (Text, Section, Document) with one tab
// style, a header that always names what the active tab is about, and controls
// that act on the selection the document keeps while you use them. It reads
// P3's editor and calls its commands; it owns no document state of its own.
//
// CSP parity: the real policy is "default-src 'self'; img-src 'self' blob: data:".
// Nothing here needs inline scripts, eval or another origin.
const props = defineProps<{
  editor: QuoteEditor; version: number; offerNo: string; editable: boolean; admin: boolean; actions: SectionActions; mode: 'dock' | 'overlay' | 'sheet'
  markPage?: number | null; reveal?: { tab: InspectorTab; target: string; n: number } | null
}>()
const emit = defineEmits<{ close: []; jump: [sectionId: string] }>()
// Opening on what is selected: text, a heading's section, or the document.
const tab = ref<InspectorTab>(props.editor.selection.text ? 'text' : props.editor.selection.sectionId ? 'section' : 'document')
let picked = false
const doc = computed(() => { void props.version; return props.editor.document })
const selection = computed(() => { void props.version; return props.editor.selection })
const text = computed(() => { void props.version; return textState(doc.value, selection.value.text, props.editor.typingBits) })
// The section the Section tab is about: where the text is, or the heading in focus;
// it stays on the last one while focus visits the inspector.
const lastSection = ref<string | null>(null)
watch(() => selection.value.text?.sectionId ?? selection.value.sectionId ?? null, id => { if (id) lastSection.value = id }, { immediate: true })
const sectionId = computed(() => lastSection.value && doc.value.sections.some(s => s.id === lastSection.value) ? lastSection.value : null)
const scope = computed(() => scopeFor(tab.value, doc.value, sectionId.value ?? undefined, text.value, props.offerNo))
// Text in the page leads to the Text tab and a heading to the Section tab, unless
// you chose a tab yourself; a tab with nothing to show hands over either way.
watch(() => [!!selection.value.text, selection.value.sectionId ?? null] as const, ([hasText, section], before) => {
  if (before && hasText === before[0] && section === before[1]) return
  if (hasText && (!picked || tab.value === 'text')) tab.value = 'text'
  else if (!hasText && section && (!picked || tab.value === 'text')) tab.value = 'section'
})
// Something on the paper asked for its settings (the footer mark): show them.
watch(() => props.reveal?.n, async () => {
  const reveal = props.reveal
  if (!reveal) return
  tab.value = reveal.tab; picked = true
  await nextTick()
  const target = document.getElementById(reveal.target)
  target?.scrollIntoView({ block: 'nearest', behavior: 'auto' })
  target?.querySelector<HTMLElement>('input')?.focus({ preventScroll: true })
}, { immediate: true })
const TABS: { id: InspectorTab; label: string }[] = [{ id: 'text', label: 'Text' }, { id: 'section', label: 'Section' }, { id: 'document', label: 'Document' }]
function choose(id: InspectorTab) { tab.value = id; picked = true }
function tabKeys(event: KeyboardEvent, index: number) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (index + (event.key === 'ArrowRight' ? 1 : -1) + 3) % 3
  choose(TABS[next]!.id)
  ;((event.currentTarget as HTMLElement).parentElement?.children[next] as HTMLElement | undefined)?.focus()
}
function run(command: () => void) {
  if (!props.editable) return
  try { command() } catch (e) { toast(e instanceof Error ? e.message : 'That did not work.', { tone: 'error' }) }
}
function keys(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  if (!(event.metaKey || event.ctrlKey) || event.altKey || target.closest('input, textarea, [contenteditable="true"]')) return
  const key = event.key.toLowerCase()
  if (key === 'b' || key === 'i') { event.preventDefault(); run(() => props.editor.setMarks(key === 'b' ? 'bold' : 'italic')) }
}
</script>

<template>
  <aside class="inspector" :class="mode" aria-label="Format" @keydown="keys">
    <header class="inspector-head">
      <h2 class="inspector-title">Format</h2>
      <button v-if="mode !== 'dock'" type="button" class="icon-btn sm flat close" aria-label="Close format" data-tip="Close" @click="emit('close')"><QuoteIcon name="close" :size="15" /></button>
    </header>
    <div class="tabs" role="tablist" aria-label="Format scope">
      <button
        v-for="(t, i) in TABS" :id="`inspector-tab-${t.id}`" :key="t.id" type="button" role="tab" class="tab" :aria-selected="tab === t.id" :aria-controls="`inspector-panel-${t.id}`" :tabindex="tab === t.id ? 0 : -1"
        @mousedown.prevent @click="choose(t.id)" @keydown="tabKeys($event, i)"
      >{{ t.label }}</button>
    </div>
    <div :id="`inspector-panel-${tab}`" class="panel" role="tabpanel" :aria-labelledby="`inspector-tab-${tab}`">
      <header v-if="scope" class="scope">
        <p class="scope-eyebrow">{{ scope.eyebrow }}</p>
        <p class="scope-title">{{ scope.title }}</p>
        <p class="scope-detail">{{ scope.detail }}</p>
      </header>
      <template v-if="tab === 'text'">
        <InspectorText v-if="text" :editor="editor" :state="text" :editable="editable" @run="run" />
        <div v-else class="empty">
          <span class="empty-icon" aria-hidden="true"><QuoteIcon name="list-none" :size="18" /></span>
          <p class="empty-title">No text selected</p>
          <p>Click into a section’s text, or select a passage, to set its style, list and numbering.</p>
        </div>
      </template>
      <InspectorSection v-else-if="tab === 'section'" :editor="editor" :document="doc" :section-id="sectionId" :editable="editable" :actions="actions" @run="run" @jump="id => emit('jump', id)" />
      <InspectorDocument v-else :editor="editor" :document="doc" :editable="editable" :admin="admin" :mark-page="markPage" @run="run" />
    </div>
  </aside>
</template>

<style scoped>
.inspector { display: flex; flex-direction: column; min-height: 0; height: 100%; background: var(--surface-raised-2); color: var(--ink); }
.inspector-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 44px; padding: 10px 16px 4px; }
.inspector-title { font: 600 14px/1.3 var(--font); color: var(--ink); }
.tabs { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 2px; margin: 4px 16px 0; padding: 3px; border-radius: 10px; background: var(--seg-bg); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--shadow-color) 8%, transparent); }
.tab { height: 30px; padding: 0 8px; border: 0; border-radius: 7px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
@media (max-width: 600px) { .tab { height: 44px; } }
@media (hover: hover) { .tab:hover { color: var(--ink); background: var(--row-hover); } }
.tab[aria-selected="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px color-mix(in srgb, var(--shadow-color) 14%, transparent), inset 0 0 0 1px var(--glass-edge); }
.tab:focus-visible { box-shadow: var(--focus-ring); }
.panel { flex: 1; min-height: 0; overflow: auto; padding: 14px 16px 20px; overscroll-behavior: contain; }
.scope { display: grid; gap: 1px; margin-bottom: 12px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); }
.scope-eyebrow { font: 500 10px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.scope-title { font-size: 14px; font-weight: 650; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scope-detail { font-size: 12px; color: var(--ink-2); }
.empty { display: grid; justify-items: start; gap: 6px; padding: 8px 2px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.empty-icon { display: grid; place-items: center; width: 36px; height: 36px; margin-bottom: 4px; border-radius: 10px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.empty-title { font-size: 14px; font-weight: 650; color: var(--ink); }
.close { margin-right: -6px; }
@media (max-width: 600px) { .close { width: 44px; height: 44px; } .close::before { content: none; } }
</style>
