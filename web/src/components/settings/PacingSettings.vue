<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { daysLabel, nightLabel, reserveLabel, when, type CapacitySchedule } from '../../lib/capacity'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import AppIcon from '../AppIcon.vue'
import KeepEditor, { type KeepDraft } from '../agents/KeepEditor.vue'
import ScheduleEditor from '../agents/ScheduleEditor.vue'

// Settings › Accounts and computers › Capacity and load (AEON-786): the person's
// pacing, moved here from the Agents page popover with the same behaviour. Work
// days, Keep for you and nights are three rows; the gears and Keep for you open
// the existing editors, a popover on wide screens and a full-height sheet on
// phones. Controls sit right-aligned on each row and the custom-week radio comes
// before 5, 6 and 7, so a custom week or a longer hint never moves them.
const props = defineProps<{ manage: boolean; ownerKey: string }>()
const capacity = useCapacity(), agents = useAgents()
const manageTip = 'Changing pacing needs permission to manage accounts'
const now = computed(() => agents.now)
const schedule = computed(() => capacity.schedule)
const days = computed(() => daysLabel(schedule.value.week))
const keepLabel = computed(() => {
  const auto = !schedule.value.reserve || schedule.value.reserve === 'auto'
  const levels = capacity.rows.flatMap(row => row.learning?.windows.map(w => w.auto_reserve_percent).filter((n): n is number => !!n) ?? [])
  return auto && levels.length ? 'Auto · learned' : reserveLabel(schedule.value)
})
const root = ref<HTMLElement>()
const heading = ref<HTMLElement>()

// ---------- Direct settings: one write at a time, failures said as failures ----------
const busy = ref(false)
async function run(work: () => Promise<void>, done?: string) {
  if (busy.value || !props.manage) return
  const owner = props.ownerKey
  busy.value = true
  try { await work(); if (done && owner === props.ownerKey) toast(done) }
  catch (e) { if (owner === props.ownerKey) toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { if (owner === props.ownerKey) busy.value = false }
}
const setPreset = (n: 5 | 6 | 7) => { if (days.value.preset !== n) void run(() => capacity.setPreset(n)) }
const toggleNights = () => void run(() => capacity.setNights(!schedule.value.nights))
function daysKey(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  // A custom week sits left of 5: ArrowRight steps onto 5, ArrowLeft stays.
  const from = days.value.preset
  if (from === null && event.key === 'ArrowLeft') return
  const next = (from === null ? 5 : Math.max(5, Math.min(7, from + (event.key === 'ArrowRight' ? 1 : -1)))) as 5 | 6 | 7
  setPreset(next)
  void nextTick(() => root.value?.querySelector<HTMLElement>(`.days [data-v="${next}"]`)?.focus())
}

// ---------- The editors behind the gears and Keep for you ----------
type EditorKind = 'week' | 'night' | 'keep'
const editor = ref<{ kind: EditorKind; owner: string } | null>(null)
const editorRef = ref<{ root?: HTMLElement | null; dirty: () => boolean; focusTitle: () => void }>()
const editorStyle = ref<Record<string, string>>({})
const sheet = ref(false)
const saving = ref(false)
let opener: HTMLElement | null = null
let release: (() => void) | undefined
const phoneQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 720px)') : null
async function openEditor(kind: EditorKind, event: Event) {
  if (!props.manage) return
  if (editor.value?.kind === kind) { closeEditor(); return }
  if (editor.value) closeEditor(false)
  opener = event.currentTarget as HTMLElement
  release = claimSettingsPopover(() => closeEditor(false))
  sheet.value = !!phoneQuery?.matches
  editor.value = { kind, owner: props.ownerKey }
  editorStyle.value = { visibility: 'hidden' }
  await nextTick()
  if (!sheet.value) {
    place()
    await nextTick()
    editorRef.value?.root?.scrollIntoView({ block: 'nearest', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    editorRef.value?.focusTitle()
  }
}
/** Under the control that opened it, kept inside the viewport. */
function place() {
  const el = editorRef.value?.root
  if (!el || !root.value || !opener) return
  const box = root.value.getBoundingClientRect(), anchor = opener.getBoundingClientRect(), w = el.offsetWidth
  const left = Math.max(12 - box.left, Math.min(anchor.right - box.left - w, innerWidth - 12 - w - box.left))
  editorStyle.value = { top: `${anchor.bottom - box.top + 8}px`, left: `${left}px`, maxHeight: `${Math.max(440, innerHeight - anchor.bottom - 64)}px` }
}
let sheetReturn: HTMLElement | null = null
function closeEditor(focus = true) {
  if (!editor.value) return
  const onSheet = sheet.value, back = opener
  editor.value = null
  editorStyle.value = {}
  opener = null
  release?.(); release = undefined
  if (onSheet) { sheetReturn = focus ? back : null; return }
  if (focus && back?.isConnected) back.focus({ preventScroll: true })
}
// A save belongs to the person who opened the editor; a switch of person or
// workspace drops it, and its result is not reported to the next person.
async function saveEditor(next: CapacitySchedule) {
  const owner = editor.value?.owner
  if (!owner || owner !== props.ownerKey || saving.value) return
  saving.value = true
  try { await capacity.saveSchedule(next); if (owner !== props.ownerKey) return; closeEditor(); toast("Saved. Today's plan follows the new schedule.") }
  catch (e) { if (owner === props.ownerKey) toast(e instanceof Error ? e.message : 'The schedule did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}
async function saveKeep(draft: KeepDraft) {
  const owner = editor.value?.owner
  if (!owner || owner !== props.ownerKey || saving.value) return
  saving.value = true
  try {
    await capacity.saveKeep(draft)
    if (owner !== props.ownerKey) return
    closeEditor()
    toast(draft.away ? `Saved. Agents use everything until ${when(draft.away, now.value)}.` : draft.reserve === 'off' ? 'Saved. Agents may use everything.' : 'Saved. Agents leave you room while you work.')
  } catch (e) { if (owner === props.ownerKey) toast(e instanceof Error ? e.message : 'Keep for you did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}
watch(() => props.ownerKey, () => { closeEditor(false); busy.value = false })
watch(() => props.manage, allowed => { if (!allowed) closeEditor(false) })

// ---------- Phone sheet: the page behind it is inert ----------
const inerted: { el: HTMLElement; inert: boolean; hidden: string | null }[] = []
function setBackground(off: boolean) {
  if (off) {
    const host = document.querySelector('.pacing-sheet-host')
    for (const el of [...document.body.children] as HTMLElement[]) {
      if (host && el.contains(host)) continue
      inerted.push({ el, inert: el.inert, hidden: el.getAttribute('aria-hidden') })
      el.inert = true
      el.setAttribute('aria-hidden', 'true')
    }
  } else {
    for (const { el, inert, hidden } of inerted.splice(0)) {
      el.inert = inert
      if (hidden === null) el.removeAttribute('aria-hidden'); else el.setAttribute('aria-hidden', hidden)
    }
  }
}
watch(() => !!editor.value && sheet.value, async open => {
  await nextTick()
  setBackground(false)
  const back = sheetReturn
  sheetReturn = null
  if (open) setBackground(true)
  else if (back?.isConnected) back.focus({ preventScroll: true })
})

// ---------- Outside clicks and resizes ----------
function outside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.isConnected || !editor.value || sheet.value) return
  if (!editorRef.value?.root?.contains(target) && !opener?.contains(target) && !editorRef.value?.dirty()) closeEditor(false)
}
function reflow() { if (editor.value && !sheet.value) place() }
onMounted(() => { document.addEventListener('click', outside, true); window.addEventListener('resize', reflow) })
onBeforeUnmount(() => { document.removeEventListener('click', outside, true); window.removeEventListener('resize', reflow); release?.(); setBackground(false) })

/** The plan card's Change and the #capacity-and-load link land here. */
function reveal() {
  root.value?.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  const first = root.value?.querySelector<HTMLElement>('.days [aria-checked="true"]:not(:disabled)') ?? root.value?.querySelector<HTMLElement>('.days button:not(:disabled)')
  if (first) first.focus({ preventScroll: true }); else heading.value?.focus({ preventScroll: true })
}
defineExpose({ reveal })
</script>

<template>
  <section id="capacity-and-load" ref="root" class="zone pacing" aria-labelledby="pacing-title">
    <div class="zone-head"><h3 id="pacing-title" ref="heading" tabindex="-1">Capacity and load</h3><p>When agents work, and how much of every limit they leave you</p></div>
    <div class="rows">
      <div class="setting days">
        <span class="lbl"><b id="days-lbl">Work days a week</b><small :class="{ custom: days.preset === null }">{{ days.hint }}</small></span>
        <span class="ctl">
          <span class="seg" role="radiogroup" aria-labelledby="days-lbl" @keydown="daysKey">
            <!-- First, so it grows leftward: 5, 6 and 7 keep their place when a custom week comes or goes. -->
            <button v-if="days.preset === null" type="button" role="radio" data-v="custom" aria-checked="true" tabindex="0" :disabled="!manage" @click="openEditor('week', $event)">{{ days.custom }}</button>
            <button
              v-for="n in ([5, 6, 7] as const)" :key="n" type="button" role="radio" :data-v="n" :aria-checked="days.preset === n" :tabindex="(days.preset ?? 5) === n && days.preset !== null ? 0 : -1"
              :disabled="!manage" :aria-disabled="busy" :data-tip="manage ? undefined : manageTip" @click="setPreset(n)"
            >{{ n }}</button>
          </span>
          <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'week'" aria-label="Customize work week" :data-tip="manage ? 'Customize work week' : manageTip" :disabled="!manage" @click="openEditor('week', $event)"><AppIcon name="gear" :size="15" /></button>
        </span>
      </div>
      <div class="setting keep">
        <span class="lbl"><b id="keep-lbl">Keep for you</b><small>How much of every limit agents leave you while you work</small></span>
        <span class="ctl">
          <button
            class="keep-btn" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'keep'" aria-labelledby="keep-lbl keep-val" :disabled="!manage"
            :data-tip="manage ? 'How much of every limit agents leave you while you work' : manageTip" @click="openEditor('keep', $event)"
          ><span id="keep-val">{{ keepLabel }}</span><AppIcon name="chevron" :size="14" /></button>
        </span>
      </div>
      <div class="setting nights">
        <span class="lbl"><b id="nights-lbl">Agents at night</b><small :class="{ off: !schedule.nights }">{{ nightLabel(schedule) }}</small></span>
        <span class="ctl">
          <button class="tog" type="button" role="switch" :aria-checked="schedule.nights" aria-labelledby="nights-lbl" :disabled="!manage" :aria-disabled="busy" :data-tip="manage ? undefined : manageTip" @click="toggleNights" />
          <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'night'" aria-label="Customize night and shifts" :data-tip="manage ? 'Customize night and shifts' : manageTip" :disabled="!manage" @click="openEditor('night', $event)"><AppIcon name="gear" :size="15" /></button>
        </span>
      </div>
    </div>

    <template v-if="editor && !sheet">
      <KeepEditor
        v-if="editor.kind === 'keep'" ref="editorRef" :schedule="schedule" :away="capacity.away" :pool-reserves="capacity.poolReserves" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="capacity.pools"
        :timezone="capacity.timezone" :saving="saving" :style="editorStyle" @close="closeEditor()" @save="saveKeep"
      />
      <ScheduleEditor
        v-else ref="editorRef" :key="editor.kind" :kind="editor.kind === 'night' ? 'night' : 'week'" :schedule="schedule" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="capacity.pools" :timezone="capacity.timezone" :saving="saving"
        :style="editorStyle" @close="closeEditor()" @save="saveEditor"
      />
    </template>
    <Teleport to="body">
      <div v-if="editor && sheet" class="pacing-sheet-host">
        <div class="scrim" @click="closeEditor()" />
        <KeepEditor
          v-if="editor.kind === 'keep'" ref="editorRef" :schedule="schedule" :away="capacity.away" :pool-reserves="capacity.poolReserves" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="capacity.pools"
          :timezone="capacity.timezone" :saving="saving" @close="closeEditor()" @save="saveKeep"
        />
        <ScheduleEditor
          v-else ref="editorRef" :key="editor.kind" :kind="editor.kind === 'night' ? 'night' : 'week'" :schedule="schedule" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="capacity.pools" :timezone="capacity.timezone" :saving="saving"
          @close="closeEditor()" @save="saveEditor"
        />
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.pacing { position: relative; margin-top: 30px; }
.zone-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 10px; }
.zone-head h3 { font-size: 18px; font-weight: 600; }
.zone-head h3:focus { outline: none; }
.zone-head p { font-size: 12px; color: var(--ink-3); }
.rows { border-top: 1px solid var(--line); }
.setting { display: flex; align-items: center; gap: 8px 16px; min-height: 60px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.lbl { flex: 1; min-width: 0; }
.lbl b { display: block; color: var(--ink); font-size: 13.5px; font-weight: 600; }
.lbl small { display: block; margin-top: 2px; color: var(--ink-3); font-size: 12px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.lbl small.custom { color: var(--ink-2); }
.lbl small.off { opacity: .55; text-decoration: line-through; text-decoration-color: var(--line-2); }
/* Right-aligned: the gear, Keep for you and 5, 6, 7 never move; a custom-week radio sits before 5 and grows leftward. */
.ctl { display: inline-flex; align-items: center; justify-content: flex-end; gap: 10px; flex: none; }
.seg button { min-width: 32px; padding: 0 10px; font-variant-numeric: tabular-nums; }
.seg button[data-v="custom"] { padding: 0 12px; white-space: nowrap; }
.seg button:disabled { cursor: default; }
.seg button:disabled:not([aria-checked="true"]) { opacity: .6; }
.tog { position: relative; display: inline-flex; align-items: center; flex: none; width: 38px; height: 22px; padding: 0; border: 0; border-radius: 999px; background: var(--line-2); box-shadow: inset 0 1px 2px rgba(0, 0, 0, .12); cursor: pointer; }
.tog::after { content: ''; position: absolute; left: 3px; width: 16px; height: 16px; border-radius: 50%; background: #fff; box-shadow: 0 1px 3px rgba(0, 0, 0, .28); transition: transform .18s ease; }
.tog[aria-checked="true"] { background: linear-gradient(180deg, #1a8683, #0e6f6c); }
.tog[aria-checked="true"]::after { transform: translateX(16px); }
.tog:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.tog:disabled { cursor: default; opacity: .6; }
.gear { display: inline-grid; place-items: center; flex: none; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .gear:hover:not(:disabled) { background: var(--row-hover); color: var(--teal-ink); } }
.gear[aria-expanded="true"] { background: var(--row-selected); color: var(--teal-ink); }
.gear:disabled { opacity: .45; cursor: default; }
.gear:focus-visible { box-shadow: var(--focus-ring); }
.keep-btn { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px 0 12px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.keep-btn svg { color: var(--ink-3); }
@media (hover: hover) { .keep-btn:hover:not(:disabled) { background: var(--row-hover); } }
.keep-btn[aria-expanded="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.keep-btn:disabled { opacity: .6; cursor: default; }
.keep-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (prefers-reduced-motion: reduce) { .tog::after { transition: none; } }
.pacing-sheet-host { position: fixed; inset: 0; z-index: 80; }
.scrim { position: absolute; inset: 0; background: var(--scrim); }
/* Phone: the label on its own line, the controls below it, right-aligned as on wide screens. */
@media (max-width: 720px) {
  .setting { flex-wrap: wrap; }
  .lbl { flex-basis: 100%; }
  .ctl { margin-left: auto; }
  .seg button { height: 38px; min-width: 44px; }
  .gear { width: 44px; height: 44px; }
  .keep-btn { height: 44px; }
}
@media (pointer: coarse) { .seg button, .keep-btn { min-height: 44px; } .gear { width: 44px; height: 44px; } .tog { margin: 11px 3px; } }
</style>
