<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { RUNNING_GROUPS, stepCap, stepRow, workingPlan, type RunningSession, type WorkingPreference, type WorkingView } from '../../lib/agentsWorking'
import { usePreference } from '../../lib/preferences'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'

// Agents working (AEON-499): the total you want at once with − and +, how many
// run now, and where they work by ticket area or by model, each with its own
// target. Counts are the live sessions; the targets are this person's own.
const agents = useAgents()
const capacity = useCapacity()
const pref = usePreference<WorkingPreference>('agents.working')

// The ticket area of each running session, read once per ticket.
const areas = reactive<Record<string, string>>({})
const running = computed(() => agents.views.filter(v => (RUNNING_GROUPS as readonly string[]).includes(v.status.group)))
watch(() => running.value.map(v => v.session.ticket_node_id).filter((id): id is string => !!id), ids => {
  for (const id of ids) {
    if (id in areas) continue
    areas[id] = ''
    getNode(id).then(node => { const area = node.fields?.area; areas[id] = typeof area === 'string' ? area : '' }).catch(() => { /* no area: counted under No area set */ })
  }
}, { immediate: true })
const sessions = computed<RunningSession[]>(() => running.value.map(v => ({ harness: v.session.harness, model: v.session.model ?? '', area: v.session.ticket_node_id ? areas[v.session.ticket_node_id] ?? '' : '' })))
const harnesses = computed(() => [...new Set(capacity.rows.map(r => r.harness))])
const routed = computed(() => capacity.rows.some(r => r.routing))
const plan = computed(() => workingPlan({
  pref: pref.value.value, sessions: sessions.value, harnesses: harnesses.value,
  capacityKnown: capacity.rows.some(r => r.primary), roomNow: routed.value ? capacity.pools.reduce((n, p) => n + p.parallelRuns, 0) : null,
}))

// The stored total decides before the control first shows: no flash of "no target set".
const prefReady = ref(false)
void pref.ready.then(() => { prefReady.value = true })
const save = (next: WorkingPreference) => pref.save(next)
const stepTotal = (delta: number) => save(stepCap(pref.value.value, delta, plan.value.from))
const stepTarget = (key: string, delta: number) => save(stepRow(pref.value.value, plan.value.view, key, delta, plan.value.from))
const setView = (view: WorkingView) => { if (plan.value.view !== view) save({ ...(pref.value.value ?? {}), view }) }
function tabKeys(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const next: WorkingView = plan.value.view === 'area' ? 'model' : 'area'
  setView(next)
  ;(event.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-view="${next}"]`)?.focus()
}
</script>

<template>
  <section v-if="prefReady" class="working glass-card" aria-labelledby="working-title">
    <div class="total">
      <p id="working-title" class="eyebrow">Agents working at once</p>
      <div class="stepper">
        <button type="button" class="step big" aria-label="Fewer agents at once" :disabled="!plan.canFewer" @click="stepTotal(-1)"><AppIcon name="minus" :size="18" /></button>
        <span class="big-num" :class="{ unset: plan.cap === null }" aria-live="polite" :aria-label="plan.cap === null ? 'Target: not set yet' : `Target: ${plan.cap} agents at once`">{{ plan.cap ?? '—' }}</span>
        <button type="button" class="step big" aria-label="More agents at once" :disabled="!plan.canMore" @click="stepTotal(1)"><AppIcon name="plus" :size="18" /></button>
        <AppIcon name="sparkle" :size="16" class="sparkle s1" aria-hidden="true" />
        <AppIcon name="sparkle" :size="11" class="sparkle s2" aria-hidden="true" />
      </div>
      <p class="now"><strong>{{ plan.running }} running</strong> now · {{ plan.runningLine }}</p>
      <div v-if="plan.capDots.length" class="dots" aria-hidden="true"><span v-for="(on, i) in plan.capDots" :key="i" class="dot" :class="{ on }" /></div>
    </div>

    <div class="where">
      <div class="where-head">
        <p class="eyebrow">Where they work</p>
        <div class="seg tabs" role="tablist" aria-label="Break down by" @keydown="tabKeys">
          <button
            v-for="t in ([['area', 'By area'], ['model', 'By model']] as const)" :key="t[0]" type="button" role="tab" :data-view="t[0]"
            :aria-selected="plan.view === t[0]" :tabindex="plan.view === t[0] ? 0 : -1" @click="setView(t[0])"
          >{{ t[1] }}</button>
        </div>
      </div>
      <ul class="rows" role="list">
        <li v-for="row in plan.rows" :key="row.key" class="row" :data-key="row.key">
          <span class="initial" :class="{ mark: plan.view === 'model' }" aria-hidden="true"><HarnessMark v-if="plan.view === 'model'" :harness="row.key" :size="16" /><template v-else>{{ row.initial }}</template></span>
          <div class="label">
            <p class="name">{{ row.label }}</p>
            <p class="sub" :title="row.sub">{{ row.sub }}</p>
          </div>
          <div class="row-dots" aria-hidden="true"><span v-for="(on, i) in row.dots" :key="i" class="dot" :class="{ on }" /></div>
          <div class="row-step">
            <button type="button" class="step" :aria-label="`Fewer agents on ${row.label}`" :disabled="!row.canDec" @click="stepTarget(row.key, -1)"><AppIcon name="minus" :size="14" /></button>
            <span class="target" aria-live="polite" :aria-label="`${row.label}: target ${row.target}`">{{ row.target }}</span>
            <button type="button" class="step" :aria-label="`More agents on ${row.label}`" :disabled="!row.canInc" @click="stepTarget(row.key, 1)"><AppIcon name="plus" :size="14" /></button>
          </div>
        </li>
      </ul>
      <p class="spare">
        <span v-if="plan.spare.length" class="spare-lbl">{{ plan.rows.length ? 'Also' : 'Set a target for' }}</span>
        <button
          v-for="area in plan.spare" :key="area.key" type="button" class="spare-btn" :aria-label="`More agents on ${area.label}`"
          :disabled="plan.full" :data-tip="plan.full ? 'Every agent is assigned; raise the total first' : undefined" @click="stepTarget(area.key, 1)"
        ><AppIcon name="plus" :size="12" />{{ area.label }}</button>
      </p>
      <p class="foot">{{ plan.footLine }}</p>
    </div>
  </section>
</template>

<style scoped>
.working { display: flex; gap: 28px; min-width: 0; padding: 22px 24px; border-radius: 24px; container: working / inline-size; }
.eyebrow { margin: 0; }
.total { position: relative; display: flex; flex-direction: column; gap: 10px; flex: none; width: 272px; box-sizing: border-box; padding-right: 28px; border-right: 1px solid var(--line); }
.stepper { position: relative; display: flex; align-items: center; gap: 14px; margin-top: 4px; }
.step { display: grid; place-items: center; flex: none; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 50%; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--teal-ink); cursor: pointer; transition: transform .12s ease, background .15s ease; }
.step.big { width: 48px; height: 48px; box-shadow: inset 0 0 0 1px var(--line-2), 0 6px 14px -8px color-mix(in srgb, var(--teal) 50%, transparent); }
@media (hover: hover) { .step:hover:not(:disabled) { background: var(--row-hover); } }
.step:active:not(:disabled) { transform: scale(.94); }
.step:disabled { opacity: .35; cursor: default; }
.step:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.big-num { width: 96px; flex: none; text-align: center; font: 800 64px/1 var(--font); letter-spacing: -.04em; font-variant-numeric: tabular-nums; background-image: linear-gradient(100deg, var(--teal-ink) 0%, var(--teal) 38%, var(--aqua) 47%, var(--aqua-wash) 50%, var(--aqua) 53%, var(--teal) 62%, var(--teal-ink) 100%); background-size: 320% 100%; -webkit-background-clip: text; background-clip: text; color: transparent; -webkit-text-fill-color: transparent; }
.big-num.unset { background: none; color: var(--ink-3); -webkit-text-fill-color: currentColor; font-weight: 600; }
.sparkle { position: absolute; pointer-events: none; color: var(--aqua); filter: drop-shadow(0 0 6px color-mix(in srgb, var(--aqua) 90%, transparent)); animation: twinkle 2.8s ease-in-out infinite; }
.sparkle.s1 { left: 58px; top: -10px; }
.sparkle.s2 { left: 150px; bottom: -4px; color: var(--brand-gold); animation-delay: .9s; }
@keyframes twinkle { 0%, 100% { opacity: .15; transform: scale(.55) rotate(0deg); } 45% { opacity: 1; transform: scale(1) rotate(20deg); } 60% { opacity: .9; transform: scale(.92) rotate(25deg); } }
.now { margin: 2px 0 0; color: var(--ink-2); font-size: 14px; }
.now strong { color: var(--ink); }
.dots, .row-dots { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; max-width: 300px; }
.row-dots { gap: 3px; }
.dot { display: block; width: 10px; height: 10px; border-radius: 50%; background: transparent; box-shadow: inset 0 0 0 1.5px color-mix(in srgb, var(--teal) 38%, transparent); }
.dot.on { background: var(--teal); box-shadow: 0 0 0 2px var(--surface-raised); animation: pulse 2.2s ease-in-out infinite; }
@keyframes pulse { 0%, 100% { box-shadow: 0 0 0 2px var(--surface-raised); } 50% { box-shadow: 0 0 0 2px var(--surface-raised), 0 0 0 5px color-mix(in srgb, var(--teal) 14%, transparent); } }
@media (prefers-reduced-motion: reduce) { .sparkle, .dot.on { animation: none; } .step { transition: none; } }

.where { display: flex; flex-direction: column; flex: 1; min-width: 0; }
.where-head { display: flex; align-items: center; gap: 12px; }
.tabs { margin-left: auto; }
.tabs button { height: 30px; padding: 0 13px; font-size: 13px; }
.tabs button[aria-selected="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: var(--shadow-btn); }
.tabs button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.rows { height: 240px; overflow: auto; scrollbar-gutter: stable; align-content: start; display: grid; gap: 4px; margin: 12px 0 0; padding: 0; list-style: none; }
.row { height: 56px; min-height: 56px; box-sizing: border-box; display: grid; grid-template-columns: 34px minmax(0, 1fr) auto 150px; align-items: center; gap: 14px; padding: 9px 10px; border-radius: 14px; background: color-mix(in srgb, var(--surface-raised) 55%, transparent); }
.initial { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 11px; background: color-mix(in srgb, var(--teal) 10%, transparent); color: var(--teal-ink); font: 700 13px/1 var(--font); }
.initial.mark { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.label { min-width: 0; }
.name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin: 0; color: var(--ink); font: 650 14.5px/1.3 var(--font); }
.sub { margin: 1px 0 0; overflow: hidden; color: var(--ink-3); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
.row-step { display: flex; align-items: center; justify-content: flex-end; gap: 6px; }
.target { min-width: 28px; text-align: center; color: var(--ink); font: 700 17px/1 var(--font); font-variant-numeric: tabular-nums; }
.spare { height: 64px; overflow: auto; align-content: start; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin: 10px 2px 0; color: var(--ink-3); font-size: 12.5px; }
.spare-lbl { margin-right: 2px; }
.spare-btn { display: inline-flex; align-items: center; gap: 5px; height: 28px; padding: 0 10px; border: 0; border-radius: 999px; background: color-mix(in srgb, var(--teal) 8%, transparent); color: var(--teal-ink); font: 600 12.5px/1 var(--font); cursor: pointer; }
@media (hover: hover) { .spare-btn:hover:not(:disabled) { background: color-mix(in srgb, var(--teal) 14%, transparent); } }
.spare-btn:disabled { opacity: .5; cursor: default; }
.spare-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.foot { height: 38px; overflow: auto; margin: 10px 2px 0; color: var(--ink-3); font-size: 12.5px; line-height: 1.5; }

@container working (max-width: 760px) {
  .row { grid-template-columns: 34px minmax(0, 1fr) auto; }
  .row-dots { display: none; }
}
@media (max-width: 720px) {
  .working { flex-direction: column; gap: 18px; padding: 18px 16px; }
  .total { width: auto; padding: 0 0 18px; border-right: 0; border-bottom: 1px solid var(--line); }
  .stepper { justify-content: center; }
  .now { text-align: center; }
  .dots { justify-content: center; max-width: none; }
  .where-head { flex-wrap: wrap; }
  .step { width: 40px; height: 40px; }
  .step.big { width: 52px; height: 52px; }
  .row { padding: 8px; gap: 10px; }
}
</style>
