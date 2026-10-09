<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1036 (design AEON-1030 draft 6): the dial with its daily limits. Folded it is one
// row as high as the other folded cards; open it is master and detail: the harnesses on
// the left, the selected harness's usage on the right, and one info area under the head
// that "● 11 running (1 waiting) ▾" opens. Low-priority facts live in hover and focus text;
// only a state that needs attention (over pace, at today's limit) keeps a flag.
import { computed, nextTick, reactive, ref, useId, watch } from 'vue'
import { accountRoomCopy, accountRoomWords, limitMode, liveCopy, modeLimit, nextLimitMode, noOwnTip, nowCopy, setLimit, statusCopy, stepLimit, stepTotal, waitingCopy, workingAccountRoom, workingRows, type HarnessLimit, type LimitMode } from '../../lib/agentsWorking'
import { can } from '../../lib/authz'
import { chipTip, dailyOf, dailyView, DEFAULT_DAILY_POINTS, needsAttention, stateWords, usageLine } from '../../lib/dailyLimits'
import { selectedHarness } from '../../lib/dialPrefs'
import { useAgentPlan } from '../../lib/useAgentPlan'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useDialPrefs } from '../../stores/dialPrefs'
import { useSectionPrefs } from '../../stores/sectionPrefs'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import DialDetail from './DialDetail.vue'
import DialInfo from './DialInfo.vue'
import FoldSection from './FoldSection.vue'
import HarnessMark from './HarnessMark.vue'
import WorkingStepper from './WorkingStepper.vue'

const agents = useAgents(), capacity = useCapacity(), session = useSession(), sections = useSectionPrefs(), dialPrefs = useDialPrefs()
const viewer = () => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : ''
const control = useAgentPlan(viewer, key => agents.models.find(m => m.id === key)?.harness)
const { snapshot, plan, saving, waiting, interrupts, save, saveDaily, hold } = control
const folded = computed(() => !sections.open.dial)
const error = computed(() => control.error.value || sections.error || dialPrefs.error)
const root = ref<InstanceType<typeof FoldSection>>()
const infoId = `dial-info-${useId()}`
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
const waitingN = computed(() => waiting.value === null ? null : waiting.value.length)
// "Nothing waiting" is said by the tile itself; only real waiting work, or an unknown queue, needs a reason.
const waits = computed(() => plan.value && snapshot.value && (waitingN.value !== 0) ? waitingCopy(plan.value, snapshot.value, room.value, waiting.value, accountRoom.value.reasons) : '')
const accounts = computed(() => accountRoomCopy(accountRoom.value))
const infoHarnesses = computed(() => rows.value.map(r => ({ key: r.key, label: r.label, places: typeof room.value[r.key] === 'number' ? r.running + room.value[r.key]! : null, words: accountRoomWords(accountRoom.value, r.key) })))

// ---------- Daily limits (AEON-1036): settings and read-only state come with the plan ----------
const defaultPoints = computed(() => snapshot.value?.daily_default_points ?? DEFAULT_DAILY_POINTS)
const views = computed(() => Object.fromEntries(rows.value.map(r => [r.key, dailyView(r.key, dailyOf(control.daily.value, r.key), snapshot.value?.daily_state?.[r.key], defaultPoints.value)])))
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const selected = computed(() => selectedHarness(rows.value.map(r => r.key), dialPrefs.prefs.selected,
  key => needsAttention(views.value[key]!), key => ['on_pace', 'over_pace', 'at_limit', 'unknown'].includes(views.value[key]!.kind)))
const selectedRow = computed(() => rows.value.find(r => r.key === selected.value))
const infoOpen = computed(() => !folded.value && dialPrefs.prefs.info_open)
const attention = (key: string) => needsAttention(views.value[key]!)
const tipOf = (row: typeof rows.value[number]) => chipTip(views.value[row.key]!, row.summary)
const flagIcon = (key: string) => views.value[key]!.kind === 'at_limit' ? 'pause' : 'alert'
function toggleInfo() {
  // From the folded dial the toggle opens the dial with the area open; open, it shows or hides the area.
  if (folded.value) { dialPrefs.setInfo(true); sections.toggle('dial') } else dialPrefs.setInfo(!dialPrefs.prefs.info_open)
}
function pick(key: string) { dialPrefs.select(key) }
/** A flag, or a phone's icon, opens the dial at that harness. */
function openAt(key: string) {
  pick(key)
  if (folded.value) sections.toggle('dial')
  void nextTick(() => nextTick(() => (root.value?.$el as HTMLElement | undefined)?.querySelector<HTMLElement>(`[data-sel="${key}"]`)?.focus({ preventScroll: true })))
}
function rowClick(event: MouseEvent, key: string) {
  if (!(event.target as HTMLElement).closest('.limit, .f-mode')) pick(key)
}
const rowWords = (key: string) => stateWords(views.value[key]!, true)

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
  <FoldSection ref="root" class="working dial" label="Agents at once" :open="!folded" :tip="folded ? 'Unfold: each harness, its daily limit and usage' : 'Fold: keep the one-line bar'" :class="{ folded, many: rows.length > 3 }" @toggle="sections.toggle('dial')">
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
          <div v-for="row in rows" :key="row.key" class="f-chip" :class="[`is-${row.mode}`, { att: attention(row.key) }]" :data-harness="row.key" role="group" :aria-label="row.label" :aria-describedby="`${infoId}-tip-${row.key}`">
            <button type="button" class="f-mode" :aria-label="cycleLabel(row)" :data-tip="`${cycleLabel(row)}${row.mode === 'none' ? '\n' + noOwnTip(row.key, total) : ''}`" @click="changeMode(row.key, nextMode(row.key))"><HarnessMark :harness="row.key" :size="14" /></button>
            <WorkingStepper :value="row.limit" :effective="row.effective" :label="row.label" :viewer="viewer()" :revision="snapshot.updated_at" :interrupt="interrupts" @step="changeLimit(row.key, $event)" @edit="editLimit(row.key, $event)" @boundary="changeMode(row.key, $event)" @hold="hold" />
            <!-- The flag's room is always kept, so it never moves a neighbour when a state changes. -->
            <span class="f-flag"><button v-if="attention(row.key)" type="button" class="cap-flag" :class="`st-${views[row.key]!.kind}`" :aria-label="`${tipOf(row)}. Open the dial at ${row.label}`" @click="openAt(row.key)"><AppIcon :name="flagIcon(row.key)" :size="12" /></button></span>
            <button type="button" class="f-ph" :class="[attention(row.key) ? `st-${views[row.key]!.kind}` : '']" :aria-label="`${tipOf(row)}. Open the dial at ${row.label}`" @click="openAt(row.key)">
              <HarnessMark :harness="row.key" :size="14" /><span class="f-ph-n"><template v-if="row.mode === 'none'">∞</template><template v-else-if="row.mode === 'off'">off</template><template v-else>{{ row.limit }}</template></span><span class="f-ph-i"><AppIcon v-if="attention(row.key)" :name="flagIcon(row.key)" :size="11" /></span>
            </button>
            <span :id="`${infoId}-tip-${row.key}`" class="chip-tip" role="tooltip">{{ tipOf(row) }}</span>
          </div>
        </div>
        <button type="button" class="f-live wh-tog" :class="{ idle: run === 0 && !waitingN, full: !!waitingN, failed: error }" :aria-expanded="infoOpen" :aria-controls="infoId" :data-tip="error || `${status} Your sessions across projects, including sessions started outside PAIMOS. Other people's sessions appear in the live count above.`" @click="toggleInfo">
          <span class="dotm" aria-hidden="true" />
          <span class="f-lv"><template v-if="error">{{ error }}</template><template v-else><b>{{ run }}</b> running<span v-if="waitingN" class="f-waitn"> ({{ waitingN }} waiting)</span></template></span>
          <AppIcon name="chevron" :size="14" />
        </button>
        <span class="sr-only f-status" role="status">{{ error || `${live} · your agents${saving ? ' · Saving' : ''}` }}</span>
      </template>
      <p v-else class="load-state" role="status">{{ error || 'Reading the total…' }} <button v-if="error" type="button" class="link-btn" @click="control.refresh">Try again</button></p>
    </template>
    <div v-if="plan && snapshot" class="f-pad">
      <DialInfo v-show="infoOpen" :id="infoId" :total="total" :running="run" :waiting="waitingN" :now="now" :waits="waits" :accounts-line="accounts" :harnesses="infoHarnesses" />
      <div class="f-grid">
        <div class="f-left">
          <div class="f-head"><p class="eyebrow">Each harness may use</p><p class="col-note">Limits may add up to more than the total; the {{ total }} at once still caps them.</p></div>
          <ul class="rows f-rows">
            <li v-for="row in rows" :key="row.key" class="row" :class="[`is-${row.mode}`, { sel: row.key === selected }]" :data-key="row.key" @click="rowClick($event, row.key)">
              <button type="button" class="f-mode initial" :aria-label="cycleLabel(row)" :data-tip="cycleLabel(row)" @click="changeMode(row.key, nextMode(row.key))"><HarnessMark :harness="row.key" :size="22" /></button>
              <button type="button" class="f-sel" :data-sel="row.key" :aria-pressed="row.key === selected" aria-controls="dial-detail" :data-tip="`${row.label}: ${row.sub}${usageLine(views[row.key]!) ? ` · ${usageLine(views[row.key]!)}` : ''}. ${rowWords(row.key)}.`">
                <span class="nm">{{ row.label }}</span>
                <span class="sub">{{ row.sub }}<template v-if="usageLine(views[row.key]!)"> · {{ usageLine(views[row.key]!) }}</template></span>
                <span class="cap-stl" :class="`st-${views[row.key]!.kind}`"><AppIcon v-if="['on_pace', 'everything'].includes(views[row.key]!.kind)" name="check" :size="12" /><AppIcon v-else-if="views[row.key]!.kind === 'over_pace'" name="alert" :size="12" /><AppIcon v-else-if="views[row.key]!.kind === 'at_limit'" name="pause" :size="12" /><span>{{ rowWords(row.key) }}</span></span>
              </button>
              <div class="limit">
                <div class="seg" role="radiogroup" :aria-label="`${row.label}: limit`" @keydown="modeKeys($event, row.key)">
                  <button v-for="[mode, label] in modes" :key="mode" type="button" role="radio" :data-mode="mode" :aria-checked="row.mode === mode" :tabindex="row.mode === mode ? 0 : -1" @click="changeMode(row.key, mode)">{{ label }}</button>
                </div>
                <div class="lim-step">
                  <WorkingStepper :value="row.limit" :effective="row.effective" :label="row.label" :viewer="viewer()" :revision="snapshot.updated_at" :interrupt="interrupts" @step="changeLimit(row.key, $event)" @edit="editLimit(row.key, $event)" @boundary="changeMode(row.key, $event)" @hold="hold" />
                  <span class="lim-word" :data-tip="row.mode === 'none' ? noOwnTip(row.key, total) : undefined">{{ row.mode === 'max' ? '' : row.words }}</span>
                </div>
              </div>
            </li>
          </ul>
          <p class="add-h"><RouterLink class="link-btn" to="/settings/accounts"><AppIcon name="plus" :size="11" />Add harness</RouterLink></p>
          <p class="effect"><span class="nw"><b>{{ total === 0 ? 'Nothing new starts' : `Up to ${total}` }}</b>{{ total === 0 ? '' : ' at once' }}</span><template v-for="row in rows" :key="row.key"> · <span class="nw">{{ row.label }} <b>{{ row.summary }}</b></span></template></p>
        </div>
        <div v-if="selectedRow" id="dial-detail" class="f-detail" role="region" :aria-label="`${selectedRow.label}: daily usage`">
          <!-- Every harness's detail, folds closed, is laid in the same cell unseen: the cell keeps the height of the tallest one; folds open downward only. -->
          <div class="f-stack">
            <DialDetail v-for="row in rows" :key="`ghost-${row.key}`" ghost :view="views[row.key]!" :total="total" :default-points="defaultPoints" :tz="snapshot.daily_timezone" :until="snapshot.daily_until" :pace-open="false" :boost-open="false" :may-manage="mayManage" />
            <DialDetail :key="selectedRow.key" :view="views[selectedRow.key]!" :total="total" :default-points="defaultPoints" :tz="snapshot.daily_timezone" :until="snapshot.daily_until"
              :pace-open="dialPrefs.foldOpen(selectedRow.key, 'pace')" :boost-open="dialPrefs.foldOpen(selectedRow.key, 'boost')" :may-manage="mayManage"
              @change="saveDaily" @fold="dialPrefs.toggleFold(selectedRow.key, $event)" />
          </div>
        </div>
      </div>
    </div>
  </FoldSection>
</template>

<style scoped>
/* AEON-781: the dial in the fold pattern. Only the chevron moved, from the right end to the start. */
.working { border-radius: 20px; container: working / inline-size; min-width: 0; }
/* Folded, the head is one row as high as the other folded cards; it wraps only when it must. */
.working :deep(.fs-head) { flex-wrap: nowrap; align-content: flex-start; align-items: center; gap: 6px 12px; min-height: 54px; padding: 8px 18px 8px 8px; }
/* The low-priority details of a chip show above the next card. */
.working:has(.f-chip:hover, .f-chip:focus-within) { z-index: 5; }
.f-dial { display: flex; flex: none; align-items: center; gap: 8px; min-width: 0; color: var(--ink); font: 600 16px/1.3 var(--font); letter-spacing: -.005em; white-space: nowrap; }
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
/* Folded: the harnesses sit after the sentence in the same row. */
.f-chips { display: flex; align-items: center; flex: none; gap: 6px 12px; order: 0; }
.f-chip { position: relative; display: flex; align-items: center; gap: 2px; border-radius: 999px; }
.f-chip + .f-chip::before { content: ''; position: absolute; width: 1px; height: 12px; left: -7px; background: var(--line-2); }
.f-vsep { flex: none; width: 1px; height: 18px; margin: 0 2px 0 0; background: var(--line-2); }
/* The flag: attention only, a full tint with an icon, never colour alone and never an edge bar. */
.f-flag { display: grid; place-items: center; flex: none; width: 24px; height: 22px; }
.cap-flag { display: grid; place-items: center; flex: none; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 50%; cursor: pointer; }
.cap-flag.st-over_pace, .f-ph.st-over_pace { background: var(--cap-over-bg); color: var(--cap-over-ink); }
.cap-flag.st-at_limit, .f-ph.st-at_limit { background: var(--cap-lim-bg); color: var(--cap-lim-ink); }
.cap-flag:focus-visible, .f-ph:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* The phone's icon with its count: not shown while the stepper is. */
.f-ph { display: none; }
/* The low-priority details: hover or keyboard focus on the chip. */
.chip-tip { display: none; position: absolute; z-index: 30; top: calc(100% + 8px); left: 0; width: max-content; max-width: 300px; padding: 7px 10px; border-radius: 8px; background: var(--tip-bg); color: var(--tip-ink); font-size: 12px; font-weight: 500; line-height: 1.4; white-space: normal; box-shadow: 0 8px 24px -8px rgba(16, 35, 39, .35); pointer-events: none; }
.f-chip:hover .chip-tip, .f-chip:focus-within .chip-tip { display: block; }
/* The live state: one toggle, right-aligned with the chevron last, so the brackets come and go without moving anything else. */
.f-live { display: flex; align-items: center; flex: 0 1 auto; gap: 7px; min-width: 0; min-height: 32px; margin: 0 0 0 auto; padding: 0 8px 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font: 500 13.5px/1.3 var(--font); white-space: nowrap; font-variant-numeric: tabular-nums; cursor: pointer; }
.f-live:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .f-live:hover { background: var(--row-hover); color: var(--ink); } }
.f-live > svg { flex: none; color: var(--ink-3); transition: transform .15s ease; }
.f-live[aria-expanded="true"] > svg { transform: rotate(180deg); }
.f-lv { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.f-live b { color: var(--ink); font-weight: 650; }
.f-waitn { color: var(--ink-2); }
.dotm { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--ok); }
.f-live.full .dotm { background: var(--gold); }
.f-live.idle .dotm { background: var(--st-backlog); }
.f-live.failed { color: var(--danger); }
.f-live.failed .f-lv { max-width: 42ch; }
.f-live.failed .dotm { background: var(--danger); }
.load-state { flex: 1 1 0; min-width: 0; color: var(--ink-3); font-size: 13.5px; }
/* Narrower cards, or many harnesses: the chips take their own line under the sentence; the live state stays on the first. */
@container working (max-width: 1040px) {
  .working :deep(.fs-head) { flex-wrap: wrap; row-gap: 4px; }
  .f-chips { order: 3; flex: 1 1 100%; flex-wrap: wrap; padding-left: 40px; }
  .f-vsep { display: none; }
}
/* More than three harnesses: the chips take their own line, as on a narrow card. */
@container working (min-width: 761px) {
  .many .f-chips { order: 3; flex: 1 1 100%; flex-wrap: wrap; padding-left: 40px; }
  .many .f-vsep { display: none; }
  .many :deep(.fs-head) { flex-wrap: wrap; row-gap: 4px; }
}
/* Below the head: one info area, then master and detail. */
.f-pad { padding: 14px 20px 18px; border-top: 1px solid var(--line); }
.f-grid { display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr); gap: 0 28px; align-items: start; }
@container working (max-width: 1300px) { .f-grid { grid-template-columns: minmax(0, 1.75fr) minmax(0, 1fr); } }
@container working (max-width: 980px) { .f-grid { grid-template-columns: minmax(0, 1fr); gap: 0; } }
.f-head { display: grid; gap: 2px; }
.working .f-head .eyebrow { margin: 0; }
p { margin: 0; }
.col-note { color: var(--ink-3); font-size: 12.5px; line-height: 1.45; }
/* The master: fixed-height rows, so the controls line up in one column; the usage lines live in the left column only. */
.rows { gap: 0; margin: 6px 0 0; display: grid; padding: 0; list-style: none; }
.row { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; align-items: center; gap: 14px; height: 88px; padding: 9px 6px; border-radius: 0; border-top: 1px solid var(--line); box-sizing: border-box; cursor: pointer; transition: background-color .15s ease; }
.row:first-child { border-top-color: transparent; }
.row .limit { cursor: default; }
@media (hover: hover) { .row:not(.sel):hover { background: var(--row-hover); border-radius: 12px; } }
.row.sel { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); border-radius: 12px; border-top-color: transparent; }
.row.sel + .row, .row:hover + .row { border-top-color: transparent; }
.f-mode.initial { width: 30px; height: 30px; margin-left: -4px; }
.row.is-off .f-mode { color: var(--ink-3); opacity: .6; }
.f-sel { display: grid; justify-items: start; gap: 2px; min-width: 0; width: calc(100% + 12px); margin: -6px -6px; padding: 6px 6px; box-sizing: border-box; border: 0; border-radius: 8px; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer; }
.f-sel:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.f-sel .nm { color: var(--ink); font: 650 14px/1.3 var(--font); }
.f-sel .sub { max-width: 100%; overflow: hidden; color: var(--ink-3); font-size: 12.5px; line-height: 1.35; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
/* The pace state: on pace is a quiet check; over pace (gold) and at the limit (red) are full tints with an icon and words. */
.cap-stl { display: inline-flex; align-items: center; gap: 5px; max-width: 100%; min-height: 22px; margin-top: 2px; padding: 0 8px 0 6px; border-radius: 999px; font-size: 12px; font-weight: 600; white-space: nowrap; }
.cap-stl > span { overflow: hidden; text-overflow: ellipsis; }
.cap-stl svg { flex: none; }
.cap-stl.st-on_pace, .cap-stl.st-everything { padding-left: 0; color: var(--ok); }
.cap-stl.st-over_pace { background: var(--cap-over-bg); color: var(--cap-over-ink); }
.cap-stl.st-at_limit { background: var(--cap-lim-bg); color: var(--cap-lim-ink); }
.cap-stl.st-no_limit, .cap-stl.st-none, .cap-stl.st-unknown { padding-left: 0; color: var(--ink-3); font-weight: 500; }
.limit { display: flex; align-items: center; gap: 12px; }
.limit .seg { flex: none; }
.limit .seg button { height: 28px; padding: 0 11px; }
.lim-step { display: flex; align-items: center; gap: 10px; height: 32px; }
/* The word keeps the room of the longest one, so switching a mode never moves a neighbour. */
.lim-word { width: 96px; color: var(--ink-3); font-size: 12px; line-height: 1.3; font-variant-numeric: tabular-nums; }
.add-h { margin: 4px 0 0 44px; }
.link-btn { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 500; text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 2px; cursor: pointer; }
.link-btn:hover { text-decoration-color: currentColor; }
.link-btn:disabled { opacity: .55; cursor: default; }
.link-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
.add-h .link-btn { display: inline-flex; align-items: center; gap: 4px; text-decoration: none; }
.add-h .link-btn:hover { text-decoration: underline; }
.effect { margin-top: 10px; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; font-variant-numeric: tabular-nums; }
.effect b { color: var(--ink); font-weight: 650; }
.nw { white-space: nowrap; }
.f-chip.is-off :deep(.mark), .row.is-off :deep(.mark) { opacity: .6; }
/* The detail: the selected harness. Every harness's closed detail sits in the same cell, so its height is the tallest one's. */
.f-detail { min-width: 0; padding-left: 28px; box-shadow: -1px 0 0 var(--line); }
.f-stack { display: grid; }
.f-stack > * { grid-area: 1 / 1; min-width: 0; }
.f-stack > .ghost { visibility: hidden; pointer-events: none; }
@container working (max-width: 980px) { .f-detail { margin-top: 18px; padding: 18px 0 0; box-shadow: 0 -1px 0 var(--line); } }
@container working (max-width: 460px) { .f-opt { display: none; } }
/* Phone: line 1 the sentence; line 2 the harnesses as icons with counts, and the running state on the right. */
@container working (max-width: 760px) {
  .working :deep(.fs-head) { flex-wrap: wrap; row-gap: 2px; gap: 6px 8px; padding: 6px 10px 6px 4px; }
  .f-dial { gap: 6px; font-size: 14px; }
  .f-bot { width: 20px; height: 20px; }
  .f-chips { order: 3; flex: 0 1 auto; flex-wrap: wrap; gap: 0; padding: 0; }
  .f-vsep, .f-chip + .f-chip::before { display: none; }
  .f-chip :deep(.f-pm), .f-chip .f-mode, .f-chip .f-flag, .chip-tip { display: none !important; }
  .f-ph { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-width: 44px; min-height: 44px; padding: 0 6px; border: 0; border-radius: 12px; background: transparent; color: var(--ink); font: 650 13.5px/1 var(--font); font-variant-numeric: tabular-nums; cursor: pointer; }
  .f-ph-i { display: grid; place-items: center; flex: none; width: 11px; height: 11px; }
  .f-chip.is-off .f-ph { color: var(--ink-3); }
  .f-live { order: 4; min-height: 44px; padding: 0 6px; }
  .f-chips, .f-live { align-self: flex-start; }
  .f-lv { overflow: visible; max-width: none; text-align: right; line-height: 1.2; font-size: 12.5px; white-space: normal; }
  .f-live.failed .f-lv { max-width: none; }
  .f-live .f-waitn { display: block; }
  .f-pad { padding: 12px 12px 14px; }
  /* Rows: name, running and limit, pace state; then the controls across the full width. Same height for every row. */
  .row { height: 186px; grid-template-columns: 44px minmax(0, 1fr); grid-template-rows: 70px auto; align-items: start; gap: 8px 6px; padding: 10px 6px; }
  .f-sel { grid-column: 2; width: calc(100% + 12px); margin: 0 -6px; padding: 4px 6px; min-height: 66px; align-content: start; }
  .f-mode.initial { width: 44px; height: 44px; margin: 0; }
  .limit { grid-column: 1 / -1; display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; }
  .limit .seg { display: flex; }
  .limit .seg button { flex: 1; min-height: 44px; height: 34px; }
  .lim-step { width: 100%; height: 44px; }
  .lim-word { width: auto; }
  .add-h { margin-left: 0; }
  .add-h .link-btn { min-height: 44px; min-width: 44px; display: inline-flex; align-items: center; }
}
@container working (max-width: 600px) {
  /* The agent glyph makes room for the steppers on phones; the sentence and the live state carry the meaning. */
  .f-bot { display: none; }
}
@media (pointer: coarse) {
  .f-mode, .f-mode.initial { width: 44px; height: 44px; margin-left: 0; }
  .working :deep(.fs-head) { padding: 6px 10px 6px 4px; }
  .f-live { min-height: 44px; }
  .row { grid-template-columns: 44px minmax(0, 1fr); }
  .limit .seg button { height: 44px; }
}
@media (prefers-reduced-motion: reduce) { .row, .f-live > svg { transition: none; } }
</style>
