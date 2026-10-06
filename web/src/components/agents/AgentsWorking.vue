<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { accountRoomCopy, accountRoomDetail, limitMode, liveCopy, modeLimit, nextLimitMode, noOwnTip, nowCopy, setLimit, statusCopy, stepLimit, stepTotal, waitingCopy, workingAccountRoom, workingRows, type HarnessLimit, type LimitMode } from '../../lib/agentsWorking'
import { useAgentPlan } from '../../lib/useAgentPlan'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSectionPrefs } from '../../stores/sectionPrefs'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import FoldSection from './FoldSection.vue'
import HarnessMark from './HarnessMark.vue'
import WorkingStepper from './WorkingStepper.vue'

const agents = useAgents(), capacity = useCapacity(), session = useSession(), sections = useSectionPrefs()
const viewer = () => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : ''
const control = useAgentPlan(viewer, key => agents.models.find(m => m.id === key)?.harness)
const { snapshot, plan, saving, waiting, interrupts, save, hold } = control
const folded = computed(() => !sections.open.dial)
const error = computed(() => control.error.value || sections.error)
// A remembered numeric ceiling is a convenience, not a minimum or reservation.
const memo = reactive<Record<string, number>>({})
watch(viewer, () => { for (const key of Object.keys(memo)) delete memo[key] })
const ownAccounts = computed(() => new Set(agents.accounts.filter(a => a.owner_person_id
  ? a.owner_person_id === snapshot.value?.principal_id || a.owner_person_id === session.identity?.principal.id
  : a.registered_by_principal_id === snapshot.value?.principal_id || a.registered_by_principal_id === session.identity?.principal.id).map(a => a.id)))
const accountRoom = computed(() => {
  const own = capacity.rows.filter(r => ownAccounts.value.has(r.id))
  const known = capacity.loaded && !capacity.stale && agents.accountsState === 'ready'
  return workingAccountRoom(own, agents.now, known, [...Object.keys(plan.value?.limits ?? {}), ...Object.keys(snapshot.value?.running ?? {})])
})
const room = computed(() => accountRoom.value.room)
const roomNow = computed(() => accountRoom.value.total)
const rows = computed(() => plan.value && snapshot.value ? workingRows(plan.value, snapshot.value, room.value, waiting.value) : [])
const modes = [['none', 'No limit'], ['max', 'At most'], ['off', 'Off']] as const
const total = computed(() => plan.value?.total ?? 0)
const run = computed(() => snapshot.value?.running_total ?? 0)
const live = computed(() => liveCopy(total.value, run.value, roomNow.value, accountRoom.value.full))
const status = computed(() => statusCopy(total.value, run.value, roomNow.value, accountRoom.value.full))
const now = computed(() => nowCopy(total.value, run.value, roomNow.value))
const waits = computed(() => plan.value && snapshot.value ? waitingCopy(plan.value, snapshot.value, room.value, waiting.value, accountRoom.value.reasons) : '')
const accounts = computed(() => accountRoomCopy(accountRoom.value))
const accountDetail = computed(() => rows.value.map(row => accountRoomDetail(accountRoom.value, row.key)).join(' · '))
function changeTotal(delta: number) {
  if (plan.value) save(stepTotal(plan.value, delta))
}
function changeLimit(key: string, delta: number) {
  if (!plan.value) return
  const row = rows.value.find(r => r.key === key)
  if (row) editLimit(key, stepLimit(plan.value, key, row.effective, delta).limits[key]!)
}
function editLimit(key: string, value: HarnessLimit) {
  if (!plan.value) return
  const before = plan.value.limits[key]
  if (typeof before === 'number') memo[key] = before
  if (typeof value === 'number') memo[key] = value
  save(setLimit(plan.value, key, value))
}
function editTotal(value: HarnessLimit) {
  if (plan.value && typeof value === 'number') save({ ...plan.value, total: value })
}
function changeMode(key: string, mode: LimitMode) {
  if (!plan.value || limitMode(plan.value.limits[key]) === mode) return
  const before = plan.value.limits[key]
  if (typeof before === 'number') memo[key] = before
  save(setLimit(plan.value, key, modeLimit(before, mode, memo[key])))
}
const nextMode = (key: string) => nextLimitMode(plan.value?.limits[key])
function cycleLabel(row: typeof rows.value[number]) {
  const mode = nextMode(row.key), next = mode === 'none' ? 'no own limit' : mode === 'off' ? 'off' : `at most ${Math.max(1, memo[row.key] ?? (typeof row.limit === 'number' ? row.limit : 2))}`
  const now = row.mode === 'none' ? `up to ${row.effective} now, no own limit` : row.mode === 'max' ? `at most ${row.shown}` : 'off · no new starts'
  return `${row.label}: ${now}, ${row.running} running. Switch to ${next}.`
}
function modeKeys(event: KeyboardEvent, key: string) {
  if (event.metaKey || event.ctrlKey || event.altKey || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
  event.preventDefault()
  const current = modes.findIndex(m => m[0] === limitMode(plan.value?.limits[key]))
  const next = modes[(current + (event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? 2 : 1)) % 3]![0]
  changeMode(key, next)
  ;(event.currentTarget as HTMLElement).querySelector<HTMLButtonElement>(`[data-mode="${next}"]`)?.focus()
}
</script>

<template>
  <FoldSection class="working dial" label="Agents at once" :open="!folded" :tip="folded ? 'Unfold: each harness, accounts, waiting work' : 'Fold: keep the one-line bar'" :class="{ folded, many: rows.length > 3 }" @toggle="sections.toggle('dial')">
    <template #head>
      <template v-if="plan && snapshot">
        <div class="f-dial">
          <svg class="f-bot" :class="{ busy: run > 0 }" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><g class="bob">
            <path class="stalk" d="M12 7.6V3.9" /><circle class="tip" cx="12" cy="2.5" r="1.65" />
            <rect class="ear" x="2.3" y="11.7" width="2.3" height="4.8" rx="1.15" /><rect class="ear" x="19.4" y="11.7" width="2.3" height="4.8" rx="1.15" />
            <rect class="head" x="4.4" y="7.6" width="15.2" height="12.6" rx="4.8" />
            <template v-if="run"><g class="look"><g class="blink"><rect class="eye" x="8.2" y="11.9" width="2.4" height="3.3" rx="1.2" /><rect class="eye" x="13.4" y="11.9" width="2.4" height="3.3" rx="1.2" /></g></g><path class="smile" d="M10.5 17.1q1.5 1 3 0" /></template>
            <path v-else class="rest" d="M8.2 13.2q1.2 1.2 2.4 0M13.4 13.2q1.2 1.2 2.4 0M11 17.1h2" />
          </g></svg>
          <span>Run up to</span>
          <WorkingStepper :value="total" :effective="total" :viewer="viewer()" :revision="snapshot.updated_at" :interrupt="interrupts" label="Agents at once" total @step="changeTotal" @edit="editTotal" @hold="hold" />
          <span class="f-unit"><span><span class="f-opt">{{ total === 1 ? 'agent ' : 'agents ' }}</span>at once.</span><span class="f-ghost" aria-hidden="true"><span class="f-opt">{{ 'agents ' }}</span>at once.</span></span>
        </div>
        <div v-if="folded" class="f-chips" role="group" aria-label="Each harness">
          <span class="f-vsep" aria-hidden="true" />
          <div v-for="row in rows" :key="row.key" class="f-chip" :class="`is-${row.mode}`" :data-harness="row.key">
            <button type="button" class="f-mode" :aria-label="cycleLabel(row)" :data-tip="`${cycleLabel(row)}${row.mode === 'none' ? '\n' + noOwnTip(row.key, total) : ''}`" @click="changeMode(row.key, nextMode(row.key))"><HarnessMark :harness="row.key" :size="14" /></button>
            <WorkingStepper :value="row.limit" :effective="row.effective" :label="row.label" :viewer="viewer()" :revision="snapshot.updated_at" :interrupt="interrupts" @step="changeLimit(row.key, $event)" @edit="editLimit(row.key, $event)" @boundary="changeMode(row.key, $event)" @hold="hold" />
          </div>
        </div>
        <p class="f-live" :data-tip="error || status" :class="{ failed: error }" role="status"><span class="live-mark" aria-hidden="true" /><span>{{ error || live }}</span><span v-if="saving" class="sr-only">Saving</span></p>
      </template>
      <p v-else class="load-state" role="status">{{ error || 'Reading the total…' }} <button v-if="error" type="button" class="link-btn" @click="control.refresh">Try again</button></p>
    </template>
    <div v-if="plan && snapshot" class="f-body">
        <div class="f-left">
          <div class="f-head"><p class="eyebrow">Each harness may use</p><p class="col-note">Limits may add up to more than the total; the {{ total }} at once still caps them.</p></div>
          <ul class="rows f-rows">
            <li v-for="row in rows" :key="row.key" class="row" :class="`is-${row.mode}`" :data-key="row.key">
              <button type="button" class="f-mode initial" :aria-label="cycleLabel(row)" :data-tip="cycleLabel(row)" @click="changeMode(row.key, nextMode(row.key))"><HarnessMark :harness="row.key" :size="22" /></button>
              <div class="label"><p class="name">{{ row.label }}</p><p class="sub" :data-tip="row.sub" tabindex="0">{{ row.sub }}</p></div>
              <div class="limit">
                <div class="seg" role="radiogroup" :aria-label="`${row.label}: limit`" @keydown="modeKeys($event, row.key)">
                  <button v-for="[mode, label] in modes" :key="mode" type="button" role="radio" :data-mode="mode" :aria-checked="row.mode === mode" :tabindex="row.mode === mode ? 0 : -1" @click="changeMode(row.key, mode)">{{ label }}</button>
                </div>
                <div class="lim-step">
                  <WorkingStepper :value="row.limit" :effective="row.effective" :label="row.label" :viewer="viewer()" :revision="snapshot.updated_at" :interrupt="interrupts" @step="changeLimit(row.key, $event)" @edit="editLimit(row.key, $event)" @boundary="changeMode(row.key, $event)" @hold="hold" />
                </div>
                <span class="lim-word" :data-tip="row.mode === 'none' ? noOwnTip(row.key, total) : undefined">{{ row.mode === 'max' ? '' : row.words }}</span>
              </div>
            </li>
          </ul>
          <p class="effect"><span class="nw"><b>{{ total === 0 ? 'Nothing new starts' : `Up to ${total}` }}</b>{{ total === 0 ? '' : ' at once' }}</span><template v-for="row in rows" :key="row.key"> · <span class="nw">{{ row.label }} <b>{{ row.summary }}</b></span></template></p>
        </div>
        <div class="f-info">
          <div class="f-head"><p class="eyebrow f-info-head"><AppIcon name="info" :size="13" />What happens</p><p class="col-note">Read-only: it follows the dial and the accounts.</p></div>
          <dl class="f-right">
            <dt class="eyebrow">Now</dt><dd class="f-now">{{ now }}</dd>
            <dt class="eyebrow">Accounts</dt><dd>{{ accounts }}<span class="f-sub">{{ accountDetail }}</span></dd>
            <dt class="eyebrow">Waiting</dt><dd class="f-wait">{{ waits }}</dd>
            <dt class="eyebrow">Checks</dt><dd class="f-sub-dd">Before every start: fewer running than the total, the harness not off or at its limit, and room on an account. Lowering the total or turning a harness off never stops anyone; running agents finish.</dd>
          </dl>
        </div>
    </div>
  </FoldSection>
</template>

<style scoped>
/* AEON-781: the dial in the fold pattern. Only the chevron moved, from the right end to the start. */
.working { border-radius: 20px; container: working / inline-size; min-width: 0; }
/* Lines stack from the top: chips wrapping onto a second line never move the first. */
.working :deep(.fs-head) { flex-wrap: wrap; align-content: flex-start; gap: 6px 12px; min-height: 0; padding: 13px 18px 13px 8px; }
.f-dial { display: flex; align-items: center; gap: 8px; min-width: 0; color: var(--ink); font: 600 16px/1.3 var(--font); letter-spacing: -.005em; white-space: nowrap; }
/* Revision 10: the shared stepper keeps its round buttons and value slot in every mode. */
.f-bot { flex: none; width: 22px; height: 22px; margin-right: 3px; overflow: visible; --rim: var(--teal); --face: color-mix(in srgb, var(--teal) 9%, var(--surface-raised)); }
.f-bot:not(.busy) { --rim: var(--ink-3); --face: var(--surface-raised); }
.f-bot .stalk { fill: none; stroke: var(--rim); stroke-width: 1.5; stroke-linecap: round; }
.f-bot .tip { fill: var(--rim); }
.f-bot:not(.busy) .tip { opacity: .55; }
.f-bot .ear { fill: var(--rim); opacity: .75; }
.f-bot .head { fill: var(--face); stroke: var(--rim); stroke-width: 1.5; }
.f-bot .eye { fill: var(--ink); }
.f-bot .smile, .f-bot .rest { fill: none; stroke: var(--ink); stroke-width: 1.2; stroke-linecap: round; }
.f-bot .rest { stroke: var(--ink-2); }
@media (prefers-reduced-motion: no-preference) {
  .f-bot.busy .bob { animation: f-hover 2.8s ease-in-out infinite; }
  .f-bot.busy .look { animation: f-look 7.4s ease-in-out infinite; }
  .f-bot.busy .blink { transform-box: fill-box; transform-origin: center; animation: f-blink 4.6s linear infinite; }
  .f-bot.busy .tip { animation: f-tip 1.9s ease-in-out infinite; }
}
@keyframes f-hover { 0%, 100% { transform: translateY(.5px); } 50% { transform: translateY(-1.6px); } }
@keyframes f-look { 0%, 34%, 100% { transform: translateX(0); } 40%, 56% { transform: translateX(.8px); } 62%, 80% { transform: translateX(-.6px); } 86% { transform: translateX(0); } }
@keyframes f-blink { 0%, 90%, 100% { transform: scaleY(1); } 93% { transform: scaleY(.12); } 96% { transform: scaleY(1); } }
@keyframes f-tip { 0%, 100% { opacity: .6; } 50% { opacity: 1; } }
/* "agent" or "agents": the room of the longer word is kept, so nothing after it moves. */
.f-unit { display: inline-grid; }
.f-unit > * { grid-area: 1 / 1; }
.f-ghost { visibility: hidden; }
/* Fixed width per harness and per part: a mode change or a step never moves a neighbour. */
.f-mode { display: grid; place-items: center; flex: none; width: 26px; height: 28px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink); cursor: pointer; }
.f-mode > svg { width: 14px; height: 14px; fill: currentColor; stroke: none; }
.f-mode:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Off dims the mark; the shared stepper owns its value state. */
.f-chip.is-off .f-mode { color: var(--ink-3); opacity: .6; }
@media (hover: hover) { .f-mode:hover { background: var(--row-hover); } }
/* Folded, the head keeps every harness: the chips sit after the sentence, or on their own line below it. */
.f-chips { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 24px; order: 3; flex: 1 1 100%; padding-left: 40px; }
.f-chip { display: flex; align-items: center; gap: 2px; width: 112px; border-radius: 999px; transition: background-color .6s ease; position: relative; }
.f-chip + .f-chip::before { content: ''; position: absolute; width: 1px; height: 12px; left: -12px; background: var(--line-2); }
.f-vsep { flex: none; width: 1px; height: 18px; margin-right: -10px; background: var(--line-2); display: none; }
.live-mark { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 16%, transparent); }
.f-live { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex: 1 1 0; min-width: 0; height: 20px; margin: 0 0 0 auto; color: var(--ink-2); font-size: 13.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.f-live > span:nth-child(2) { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.f-live b { color: var(--ink); font-weight: 650; }
.f-live.failed { color: var(--danger); }
.failed .live-mark { background: var(--danger); }
.load-state { flex: 1 1 0; min-width: 0; color: var(--ink-3); font-size: 13.5px; }
@container working (min-width: 1120px) {
  .f-chips { order: 0; flex: none; max-width: 410px; padding-left: 0; }
  .f-vsep { display: block; }
  .many .f-chips { order: 3; flex: 1 1 100%; max-width: none; padding-left: 40px; }
  .many .f-vsep { display: none; }
}
/* The details below the head: two columns on wide cards. */
.f-body { display: grid; grid-template-columns: minmax(0, 1fr); gap: 18px 0; padding: 14px 20px 18px; border-top: 1px solid var(--line); }
.f-head { display: grid; gap: 2px; }
.row:first-child { border-top: 0; }
.initial { width: 22px; height: 22px; background: none; box-shadow: none; }
.f-info { display: grid; gap: 14px; align-content: start; padding-top: 16px; border-top: 1px solid var(--line); }
.working .f-info-head { display: flex; align-items: center; gap: 6px; }
.f-info-head svg { flex: none; color: var(--ink-3); }
.f-right { display: grid; grid-template-columns: 82px minmax(0, 1fr); gap: 12px 14px; align-content: start; margin: 0; }
.working .f-right dt { margin: 0; padding-top: 2px; line-height: 1.6; }
.working .f-head .eyebrow { margin: 0; }
.f-right dd { margin: 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; font-variant-numeric: tabular-nums; text-wrap: pretty; }
.f-right dd b { color: var(--ink); font-weight: 650; }
.f-sub { display: block; color: var(--ink-3); font-size: 12.5px; }
.f-right .f-sub-dd { color: var(--ink-3); font-size: 12.5px; }
@container working (min-width: 900px) {
  .f-body { grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr); }
  .f-left { padding-right: 32px; }
  .f-info { padding: 0 0 4px 32px; border-top: 0; border-left: 1px solid var(--line); }
}
@container working (max-width: 460px) { .f-opt { display: none; } }
p { margin: 0; }
.rows { gap: 0; margin-top: 6px; display: grid; padding: 0; list-style: none; }
.row { grid-template-columns: 22px minmax(0, 1fr) auto; gap: 14px; padding: 9px 6px; border-radius: 0; background: none; border-top: 1px solid var(--line); display: grid; align-items: center; min-height: 76px; box-sizing: border-box; }
.label { min-width: 0; }
.name { color: var(--ink); font: 650 14.5px/1.3 var(--font); }
.sub { overflow: hidden; color: var(--ink-3); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
.limit { display: grid; grid-template-columns: max-content max-content; align-items: start; gap: 6px 8px; }
.limit .seg { flex: none; }
.limit .seg button { height: 28px; padding: 0 11px; }
.lim-step { display: flex; align-items: center; height: 32px; }
.lim-word { grid-column: 1 / -1; min-height: 18px; color: var(--ink-3); font-size: 12.5px; line-height: 18px; font-variant-numeric: tabular-nums; }
.col-note { color: var(--ink-3); font-size: 12.5px; line-height: 1.45; }
.effect { margin-top: 10px; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; font-variant-numeric: tabular-nums; }
.effect b { color: var(--ink); font-weight: 650; }
.nw { white-space: nowrap; }
.f-chip.is-off :deep(.mark), .row.is-off :deep(.mark) { opacity: .6; }
@container working (max-width: 760px) {
  .working :deep(.fs-head) { gap: 6px 8px; padding: 6px 10px 10px 4px; }
  .f-dial { gap: 6px; font-size: 14px; }
  .f-bot { width: 20px; height: 20px; }
  /* Phone: fixed chips spread over the line, and the mark sits between − and the value (− mark n +), still cycling. */
  .f-chips { padding: 0; display: grid; grid-template-columns: repeat(3, 104px); justify-content: space-between; gap: 6px 0; }
  .f-chip { width: 104px; gap: 2px; }
  .f-chip :deep(.f-pm) { display: contents; }
  .f-chip :deep(.pm.dec) { order: 1; }
  .f-chip .f-mode { order: 2; width: 26px; height: 32px; }
  .f-chip :deep(.value-slot) { order: 3; }
  .f-chip :deep(.pm.inc) { order: 4; }
  .f-chip:nth-of-type(3n + 1)::before { content: none; }
  .f-chip + .f-chip::before { left: -8px; }
  .f-live { order: 4; flex: 1 1 100%; justify-content: flex-start; margin: 0; padding-left: 10px; font-size: 13px; }
  .f-body { padding: 12px 12px 14px; }
  /* Phone: the switch takes the full width; the limit or its words sit on their own line under it. */
  .limit { grid-column: 1 / -1; grid-template-columns: minmax(0, 1fr) max-content; gap: 6px 8px; }
  .sub { min-height: 0; display: block; white-space: nowrap; }
  .f-right { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  .working .f-right dt { padding-top: 0; }
  .working .f-right dd + dt { margin-top: 10px; }
  .row { grid-template-columns: 22px minmax(0, 1fr); gap: 6px 10px; padding: 10px 4px; min-height: 134px; grid-template-rows: 34px auto; }
  .limit .seg { grid-column: 1 / -1; display: flex; }
  .limit .seg button { flex: 1; padding: 0 11px; height: 34px; }
  .limit .seg button::before { content: none; }
  .lim-step { grid-column: 1; height: 32px; }
  .lim-word { grid-column: 2; align-self: center; }
}
@container working (max-width: 600px) {
  /* The agent glyph makes room for the steppers on phones; the sentence and the live state carry the meaning. */
  .f-bot { display: none; }
}
@container working (max-width: 320px) {
  .f-chips { grid-template-columns: repeat(2, 104px); justify-content: start; column-gap: 24px; }
  .f-chip:nth-of-type(3n + 1)::before { content: ''; }
  .f-chip:nth-of-type(2n + 1)::before { content: none; }
}
@media (pointer: coarse) {
  .f-mode { width: 44px; height: 44px; }
  .working :deep(.fs-head) { padding: 6px 10px 10px 4px; }
  .f-dial { flex-wrap: wrap; }
  .f-dial .f-unit { flex: none; }
  .f-chips, .many .f-chips { order: 3; flex: 1 1 100%; display: grid; grid-template-columns: repeat(auto-fit, minmax(184px, 1fr)); gap: 6px 12px; max-width: none; padding: 0; }
  .f-vsep, .many .f-vsep, .f-chip + .f-chip::before { display: none; }
  .f-chip { width: 184px; gap: 0; }
  .f-chip :deep(.f-pm) { display: contents; }
  .f-chip :deep(.pm.dec) { order: 1; }
  .f-chip .f-mode { order: 2; width: 44px; height: 44px; }
  .f-chip :deep(.value-slot) { order: 3; }
  .f-chip :deep(.pm.inc) { order: 4; }
  .f-live { order: 4; flex: 1 1 100%; justify-content: flex-start; margin: 0; padding-left: 10px; }
  .row { grid-template-columns: 44px minmax(0, 1fr); }
  .limit { grid-column: 1 / -1; grid-template-columns: minmax(0, 1fr) max-content; }
  .limit .seg { grid-column: 1 / -1; display: flex; }
  .limit .seg button { height: 44px; flex: 1; }
  .lim-step { grid-column: 1 / -1; height: 44px; }
  .lim-word { grid-column: 1 / -1; align-self: start; }
}
@media (prefers-reduced-motion: reduce) { .f-chip { transition: none; } }
</style>
