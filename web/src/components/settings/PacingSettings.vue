<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { clockLabel, daysLabel, hourLabel, nightLabel, reserveLabel, reserveLevel, weekdayIn, when, type CapacitySchedule } from '../../lib/capacity'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import AppIcon from '../AppIcon.vue'
import KeepEditor, { type KeepDraft } from '../agents/KeepEditor.vue'
import ScheduleEditor from '../agents/ScheduleEditor.vue'

// Settings › Capacity › When agents work (AEON-786; AEON-1175, design AEON-1149): the
// person's pacing, moved here from the Agents page popover with the same behaviour.
// A read-only ribbon shows the day at a glance; work days, nights and Keep for you
// are three rows below it. The gears and Keep for you open the existing editors, a
// popover on wide screens and a full-height sheet on phones. Controls sit
// right-aligned on each row and the custom-week radio comes before 5, 6 and 7, so a
// custom week or a longer hint never moves them.
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
// ---------- The day at a glance (AEON-1175): read-only, from the schedule and the clock ----------
const DAY_NAMES = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
const ribbon = computed(() => {
  const s = schedule.value, zone = capacity.timezone || s.timezone, at = new Date(now.value).toISOString()
  const [h, m] = clockLabel(at, zone).split(':').map(Number)
  const hour = (h ?? 0) + (m ?? 0) / 60, today = s.week[weekdayIn(at, zone)]
  // Today's hours, or the first work day's when today is off: the ribbon is one day.
  const span = today?.on ? today : s.week.find(d => d.on) ?? null
  // A learned Keep for you differs per account, so the ribbon names no number for it.
  const keep = reserveLevel(s), auto = !s.reserve || s.reserve === 'auto'
  // Hour labels next to "now" step aside; at either end of the day "now" keeps inside the ribbon.
  const near = (x: number) => Math.abs(x - hour) < 1.6
  const ticks = [{ at: 0, label: '00' }, ...(span ? [{ at: span.start, label: hourLabel(span.start) }, { at: span.end, label: hourLabel(span.end) }] : []), { at: 24, label: '24' }]
  return {
    days: DAY_NAMES.map((name, i) => ({ name, on: !!s.week[i]?.on })),
    span: span && { start: span.start, end: span.end },
    nights: s.nights, keep: !keep ? '' : auto && keepLabel.value === 'Auto · learned' ? 'room' : `${auto ? '~' : ''}${keep} %`,
    now: hour, nowLabel: `now ${clockLabel(at, zone)}`, nowEdge: hour < 1.6 ? 'start' : hour > 22.4 ? 'end' : '',
    ticks: ticks.filter((t, i) => !near(t.at) && ticks.findIndex(u => u.at === t.at) === i),
  }
})
const at24 = (hour: number) => `${(hour / 24) * 100}%`
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
/** Choose a viewport position once on open; content grows below the controls. */
function place() {
  const el = editorRef.value?.root
  if (!el || !root.value || !opener) return
  const box = root.value.getBoundingClientRect(), anchor = opener.getBoundingClientRect(), w = el.offsetWidth
  const left = Math.max(12 - box.left, Math.min(anchor.right - box.left - w, innerWidth - 12 - w - box.left))
  const footerGap = 88, viewportTop = 72
  const height = Math.min(el.offsetHeight, Math.max(0, innerHeight - viewportTop - footerGap))
  const below = anchor.bottom + 8
  const top = below + height <= innerHeight - footerGap ? below : Math.max(viewportTop, anchor.top - height - 8)
  editorStyle.value = { top: `${top - box.top}px`, left: `${left}px`, maxHeight: `${Math.max(0, innerHeight - top - footerGap)}px` }
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
    <div class="zone-head"><h3 id="pacing-title" ref="heading" tabindex="-1">When agents work</h3><p>Alongside you in your hours, alone at night if you let them</p></div>
    <div class="pace-body">
      <div class="rib" aria-hidden="true" data-ribbon>
        <div class="rib-days"><span v-for="d in ribbon.days" :key="d.name" :class="{ on: d.on }">{{ d.name }}</span></div>
        <div class="rib-bar">
          <template v-if="ribbon.span">
            <span class="night" :class="{ off: !ribbon.nights }" :style="{ width: at24(ribbon.span.start) }"><AppIcon name="moon" :size="12" /><span class="lg">{{ ribbon.nights ? 'agents at night' : 'off' }}</span></span>
            <span class="day" :style="{ width: at24(ribbon.span.end - ribbon.span.start) }"><AppIcon name="sun" :size="12" /><span class="rt">your hours<span class="lg"> · agents work alongside you</span><template v-if="ribbon.keep"><span class="lg"> and</span> leave you {{ ribbon.keep }}</template></span></span>
            <span class="night" :class="{ off: !ribbon.nights }" :style="{ width: at24(24 - ribbon.span.end) }"><AppIcon name="moon" :size="12" /><span class="lg">{{ ribbon.nights ? 'night' : 'off' }}</span></span>
          </template>
          <span v-else class="night" :class="{ off: !ribbon.nights }" style="width: 100%"><AppIcon name="moon" :size="12" />{{ ribbon.nights ? 'no work days · agents at night' : 'no work days' }}</span>
          <i class="rib-now" :style="{ left: at24(ribbon.now) }" />
        </div>
        <div class="rib-h"><span v-for="t in ribbon.ticks" :key="t.at" :style="{ left: at24(t.at) }">{{ t.label }}</span><span class="now" :class="ribbon.nowEdge" :style="{ left: at24(ribbon.now) }">{{ ribbon.nowLabel }}</span></div>
      </div>
      <div class="rows">
        <div class="setting days">
          <AppIcon class="ico" name="calendar" :size="16" />
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
        <div class="setting nights">
          <AppIcon class="ico" name="moon" :size="16" />
          <span class="lbl"><b id="nights-lbl">Agents at night</b><small :class="{ off: !schedule.nights }">{{ nightLabel(schedule) }}</small></span>
          <span class="ctl">
            <button class="tog" type="button" role="switch" :aria-checked="schedule.nights" aria-labelledby="nights-lbl" :disabled="!manage" :aria-disabled="busy" :data-tip="manage ? undefined : manageTip" @click="toggleNights" />
            <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'night'" aria-label="Customize night and shifts" :data-tip="manage ? 'Customize night and shifts' : manageTip" :disabled="!manage" @click="openEditor('night', $event)"><AppIcon name="gear" :size="15" /></button>
          </span>
        </div>
        <div class="setting keep">
          <AppIcon class="ico" name="hand" :size="16" />
          <span class="lbl"><b id="keep-lbl">Keep for you</b><small>How much of every limit agents leave you while you work</small></span>
          <span class="ctl">
            <button
              class="keep-btn" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'keep'" aria-labelledby="keep-lbl keep-val" :disabled="!manage"
              :data-tip="manage ? 'How much of every limit agents leave you while you work' : manageTip" @click="openEditor('keep', $event)"
            ><span id="keep-val">{{ keepLabel }}</span><AppIcon name="chevron" :size="14" /></button>
          </span>
        </div>
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
/* The day at a glance: read-only, hairlines and full tints, the current time as two ink ticks. */
.pace-body { container: pacing / inline-size; }
.rib { margin: 2px 0 14px; padding: 14px 16px 10px; border-radius: 14px; background: var(--surface-sunken); }
.rib-days { display: flex; gap: 4px; margin-bottom: 10px; }
.rib-days span { display: grid; place-items: center; min-width: 34px; height: 20px; padding: 0 4px; font: 500 11px/1 var(--mono); color: var(--ink-3); opacity: .6; }
.rib-days span.on { color: var(--teal-ink); font-weight: 600; opacity: 1; box-shadow: inset 0 -2px 0 color-mix(in srgb, var(--teal) 45%, transparent); }
.rib-bar { position: relative; display: flex; height: 30px; border-radius: 8px; overflow: hidden; box-shadow: inset 0 0 0 1px var(--line); }
.rib-bar > span { display: flex; align-items: center; gap: 6px; min-width: 0; padding: 0 10px; font-size: 11.5px; font-weight: 600; white-space: nowrap; overflow: hidden; }
.rib-bar .night { background: color-mix(in srgb, var(--ink) 9%, transparent); color: var(--ink-2); }
.rib-bar .night.off { background: repeating-linear-gradient(90deg, var(--line) 0 3px, transparent 3px 7px); color: var(--ink-3); font-weight: 500; }
.rib-bar .day { background: color-mix(in srgb, var(--teal) 14%, transparent); color: var(--teal-ink); }
.rib-bar svg { flex: none; }
.rib-bar .rt { min-width: 0; overflow: hidden; }
.rib-now { position: absolute; top: 0; bottom: 0; width: 2px; margin-left: -1px; }
.rib-now::before, .rib-now::after { content: ''; position: absolute; left: 0; width: 2px; height: 6px; border-radius: 1px; background: var(--ink); }
.rib-now::before { top: 0; }
.rib-now::after { bottom: 0; }
.rib-h { position: relative; height: 16px; margin-top: 4px; font: 500 10.5px/16px var(--mono); color: var(--ink-3); }
.rib-h span { position: absolute; transform: translateX(-50%); white-space: nowrap; }
.rib-h span[style*="left: 0%"], .rib-h .now.start { transform: none; }
.rib-h span[style*="left: 100%"], .rib-h .now.end { transform: translateX(-100%); }
.rib-h .now { color: var(--ink-2); }
.rows { border-top: 1px solid var(--line); }
.setting { display: grid; grid-template-columns: 28px minmax(0, 1fr) auto; align-items: center; column-gap: 12px; min-height: 64px; padding: 8px 2px; border-bottom: 1px solid var(--line); }
.setting:last-child { border-bottom: 0; }
.setting > .ico { justify-self: center; color: var(--ink-3); }
.lbl { display: grid; gap: 2px; min-width: 0; }
.lbl b { display: block; color: var(--ink); font-size: 13.5px; font-weight: 600; }
.lbl small { display: block; color: var(--ink-3); font-size: 12.5px; line-height: 1.45; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.lbl small.custom { color: var(--ink-2); }
.lbl small.off { opacity: .55; text-decoration: line-through; text-decoration-color: var(--line-2); }
/* Right-aligned: the gear, Keep for you and 5, 6, 7 never move; a custom-week radio sits before 5 and grows leftward. */
.ctl { display: inline-flex; align-items: center; justify-content: flex-end; gap: 10px; }
.seg button { min-width: 34px; padding: 0 11px; font-variant-numeric: tabular-nums; }
.seg button[data-v="custom"] { padding: 0 12px; white-space: nowrap; }
.seg button:disabled { cursor: default; }
.seg button:disabled:not([aria-checked="true"]) { opacity: .6; }
.tog { position: relative; display: inline-flex; align-items: center; flex: none; width: 38px; height: 22px; padding: 0; border: 0; border-radius: 999px; background: var(--line-2); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--shadow-black) 12%, transparent); cursor: pointer; }
.tog::after { content: ''; position: absolute; left: 3px; width: 16px; height: 16px; border-radius: 50%; background: var(--switch-knob); box-shadow: 0 1px 3px color-mix(in srgb, var(--shadow-black) 28%, transparent); transition: transform .18s ease; }
.tog[aria-checked="true"] { background: linear-gradient(180deg, var(--primary-hi), var(--primary) 60%, var(--primary-lo)); }
.tog[aria-checked="true"]::after { background: var(--switch-knob); transform: translateX(16px); }
.tog:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.tog:disabled { cursor: default; opacity: .6; }
.gear { display: inline-grid; place-items: center; flex: none; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .gear:hover:not(:disabled) { background: var(--row-hover); color: var(--teal-ink); } }
.gear[aria-expanded="true"] { background: var(--row-selected); color: var(--teal-ink); }
.gear:disabled { opacity: .45; cursor: default; }
.gear:focus-visible { box-shadow: var(--focus-ring); }
.keep-btn { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px 0 12px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.keep-btn svg { color: var(--ink-3); }
@media (hover: hover) { .keep-btn:hover:not(:disabled) { background: var(--row-hover); } }
.keep-btn[aria-expanded="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.keep-btn:disabled { opacity: .6; cursor: default; }
.keep-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (prefers-reduced-motion: reduce) { .tog::after { transition: none; } }
.pacing-sheet-host { position: fixed; inset: 0; z-index: 80; }
.scrim { position: absolute; inset: 0; background: var(--scrim); }
/* Narrow: the label beside its icon, the controls on their own line under it (design AEON-1149 phone). */
@container pacing (max-width: 560px) {
  .rib { padding: 12px 12px 8px; }
  .rib-days span { min-width: 0; flex: 1; }
  .rib-bar .lg { display: none; }
  .rib-bar > span { padding: 0 6px; }
  .setting { grid-template-columns: 24px minmax(0, 1fr); row-gap: 8px; }
  .setting .ctl { grid-column: 2; justify-content: flex-start; }
}
@media (max-width: 720px) {
  .seg button { height: 38px; min-width: 44px; }
  .gear { width: 44px; height: 44px; }
  .keep-btn { height: 44px; }
}
/* Touch: the switch takes a 44 px target and draws its track inside it, as the account panel's switch does. */
@media (pointer: coarse) {
  .seg button, .keep-btn { min-height: 44px; }
  .gear { width: 44px; height: 44px; }
  .tog, .tog[aria-checked="true"] { width: 50px; height: 44px; background: transparent; box-shadow: none; }
  .tog::before { content: ''; position: absolute; left: 6px; top: 11px; width: 38px; height: 22px; border-radius: 999px; background: var(--line-2); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--shadow-black) 12%, transparent); }
  .tog[aria-checked="true"]::before { background: linear-gradient(180deg, var(--primary-hi), var(--primary) 60%, var(--primary-lo)); box-shadow: none; }
  .tog::after { left: 9px; }
  .tog:focus-visible { box-shadow: none; }
  .tog:focus-visible::before { box-shadow: var(--focus-ring); }
}
</style>
