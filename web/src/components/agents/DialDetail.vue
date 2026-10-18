<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1036: the selected harness's usage on the right of the open dial. Reading
// first, settings second (Pace and Boost today as folds; at the limit it follows
// Settings › Models, AEON-1166). Folds open downward only, so nothing above them moves.
// AEON-1497 (approved fuel gauge): the reading is the group's quota outlook: the headline,
// quota left over time and each subscription; today's points follow as the first row.
import { computed, ref, watch } from 'vue'
import { HARNESS_NAME } from '../../lib/capacity'
import {
  boostNumber, boostSummary, detailHeadline, makeBoost, needsAttention, paceSummary, pct, resetsLine, stateWords, storedPace, todayLine, typedPercent, typedPoints,
  type DailySettings, type DailyView, type EnteredAs,
} from '../../lib/dailyLimits'
import type { DialFold } from '../../lib/dialPrefs'
import type { UsageView } from '../../lib/quotaOutlook'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import QuotaUsage from './QuotaUsage.vue'

const props = defineProps<{
  view: DailyView; total: number; defaultPoints: number; tz?: string
  /** AEON-1497: the group's quota outlook in words, its read state and its clock. */
  usage: UsageView; usageState: 'loading' | 'ready' | 'error'; generatedAt?: string; anchorHour?: string
  /** The person's next local midnight: the end of a Boost today. */
  until?: string
  paceOpen: boolean; boostOpen: boolean
  /** The person has the live right to manage accounts; Boost today is refused without it. */
  mayManage: boolean
  /** Only the height of this detail is wanted: folds closed, no ids, nothing focusable. */
  ghost?: boolean
}>()
// A change names the harness it was made for, so a pick made meanwhile never takes it over.
const emit = defineEmits<{ change: [harness: string, settings: DailySettings]; fold: [fold: DialFold]; retry: [] }>()
const harness = computed(() => props.view.harness)
const name = computed(() => HARNESS_NAME[harness.value] ?? harness.value)
const settings = computed(() => props.view.settings)
/** Today's points: the dt says "Today"; a state that needs attention keeps its words and icon. */
const today = computed(() => todayLine(props.view).replace(/^Today: /, ''))
const attention = computed(() => needsAttention(props.view))
/** Vendor-reported reset credits, only when the vendor reports them. */
const resets = computed(() => resetsLine(props.view.account, props.tz))
const open = (fold: DialFold) => !props.ghost && (fold === 'pace' ? props.paceOpen : props.boostOpen)
const change = (patch: Partial<DailySettings>) => emit('change', harness.value, { ...settings.value, ...patch })
const id = (part: string) => props.ghost ? undefined : `dial-${harness.value}-${part}`
const tabbable = (n: 0 | -1 = 0) => props.ghost ? -1 : n

// ---------- Pace ----------
const everything = computed(() => settings.value.pace.mode === 'everything')
const pacePoints = computed(() => settings.value.pace.points_per_day ?? props.defaultPoints)
const paceDraft = ref(''), paceEditing = ref(false), paceNote = ref('')
const paceShown = computed(() => paceEditing.value ? paceDraft.value : String(pacePoints.value))
function paceMode(mode: 'pace' | 'everything') {
  if (settings.value.pace.mode !== mode) change({ pace: { ...settings.value.pace, mode } })
}
function paceFocus() { paceEditing.value = true; paceDraft.value = String(pacePoints.value); paceNote.value = '' }
// A field that kept focus after its Enter starts a new draft with the next key.
function paceInput(event: Event) { paceEditing.value = true; paceNote.value = ''; paceDraft.value = (event.target as HTMLInputElement).value }
function paceCommit() {
  if (!paceEditing.value) return
  const value = typedPoints(paceDraft.value)
  paceEditing.value = false
  if (value === undefined) { paceNote.value = 'Whole numbers from 1 to 50.'; return }
  // The default is followed, not copied: the person's own default may change. The same rule as Settings › Capacity.
  const stored = storedPace(settings.value.pace, { mode: 'own', points: value }, props.defaultPoints).points_per_day
  if (stored !== settings.value.pace.points_per_day) change({ pace: { ...settings.value.pace, points_per_day: stored } })
}
function paceCancel() { paceEditing.value = false; paceNote.value = '' }

// ---------- Boost today ----------
const boost = computed(() => settings.value.boost_today)
const asLocal = ref<EnteredAs>('used')
const as = computed<EnteredAs>(() => boost.value?.entered_as ?? asLocal.value)
const boostEditable = computed(() => props.mayManage && props.view.editable && !!props.until)
const boostDraft = ref(''), boostEditing = ref(false), boostNote = ref('')
const boostShown = computed(() => boostEditing.value ? boostDraft.value : boost.value ? pct(boostNumber(boost.value, as.value)) : '')
const boostPlaceholder = computed(() => props.view.limit === null ? '' : pct(as.value === 'used' ? props.view.limit : 100 - props.view.limit))
const boostHelp = computed(() => {
  if (boostNote.value) return boostNote.value
  if (!boostEditable.value) return props.mayManage ? 'Only the owner of each account can change today’s limit.' : 'Changing today’s limit needs the right to manage accounts.'
  const several = props.view.accounts.length > 1
  return boost.value ? `Up to ${pct(boost.value.limit_used_pct)} % used · ${pct(100 - boost.value.limit_used_pct)} % left today${several ? ', on each account' : ''}. Ends at midnight.` : 'For today only; it can raise or lower the limit and ends at midnight. Type it as used or left; both are restated.'
})
function boostFocus() { boostEditing.value = true; boostDraft.value = boost.value ? pct(boostNumber(boost.value, as.value)) : ''; boostNote.value = '' }
function boostInput(event: Event) { boostEditing.value = true; boostNote.value = ''; boostDraft.value = (event.target as HTMLInputElement).value }
function boostCommit() {
  if (!boostEditing.value) return
  const text = boostDraft.value.trim(), value = typedPercent(text)
  boostEditing.value = false
  if (!boostEditable.value) return
  if (text === '') { if (boost.value) change({ boost_today: null }); return }
  if (value === undefined) { boostNote.value = 'Whole percentages from 0 to 100.'; return }
  const next = makeBoost(value, as.value, props.until!)
  if (!boost.value || boost.value.limit_used_pct !== next.limit_used_pct || boost.value.entered_as !== next.entered_as) change({ boost_today: next })
}
function boostCancel() { boostEditing.value = false; boostNote.value = '' }
function boostAs(value: EnteredAs) {
  asLocal.value = value
  if (boost.value && boostEditable.value && boost.value.entered_as !== value) change({ boost_today: { ...boost.value, entered_as: value } })
}
function boostClear() { if (boost.value) change({ boost_today: null }) }

// A new harness starts from its own drafts.
watch(harness, () => { paceEditing.value = false; paceNote.value = ''; boostEditing.value = false; boostNote.value = ''; asLocal.value = 'used' })
// Pace has its home in Settings › Capacity, at this harness's row; the inline edit writes the
// same value (AEON-1176 P6, folded into AEON-1209).
const paceHome = computed(() => `/settings/capacity#pace-${harness.value}`)
</script>

<template>
  <div class="cd" :class="{ ghost }" :aria-hidden="ghost || undefined" :inert="ghost || undefined" :data-detail="ghost ? undefined : harness">
    <h3 class="cd-h"><HarnessMark :harness="harness" :size="16" /><span>{{ view.kind === 'none' ? `${name} · no account yet` : detailHeadline(view) }}</span></h3>

    <QuotaUsage :view="usage" :state="usageState" :generated-at="generatedAt" :anchor-hour="anchorHour" :tz="tz" :ghost="ghost" @retry="emit('retry')" />
    <template v-if="view.kind === 'no_limit'">
      <dl class="cd-dl">
        <dt><span class="eyebrow">At the limit</span></dt><dd class="cd-quiet">Nothing to reach; the {{ total }} at once and the account still cap it.</dd>
      </dl>
    </template>
    <template v-else-if="view.kind !== 'none'">
      <dl class="cd-dl">
        <dt><span class="eyebrow">Today</span></dt>
        <dd><p class="cd-td">{{ today }}<span v-if="attention" class="cap-st" :class="`st-${view.kind}`"><AppIcon :name="view.kind === 'at_limit' ? 'pause' : 'alert'" :size="13" /><span>{{ stateWords(view) }}</span></span></p><p v-if="resets" class="cap-rs"><AppIcon name="refresh" :size="13" /><span>{{ resets }}</span></p></dd>
        <dt><button type="button" class="cd-fh" :aria-expanded="open('pace')" :aria-controls="id('pace')" :tabindex="tabbable()" @click="emit('fold', 'pace')"><AppIcon class="cap-chev" name="chevron-right" :size="12" /><span class="eyebrow">Pace</span></button></dt>
        <dd :class="{ open: open('pace') }">
          <p class="cd-sum" @click="emit('fold', 'pace')">{{ paceSummary(view) }}</p>
          <div v-if="open('pace')" :id="id('pace')" class="cd-fb" role="group" aria-label="Pace">
            <label class="cap-opt"><input type="radio" :name="`use-${harness}`" :checked="!everything" @change="paceMode('pace')"><span>Stay on pace</span></label>
            <p class="cap-pp"><input class="field n" :value="paceShown" :disabled="everything" inputmode="numeric" maxlength="2" aria-label="Percentage points a day" @focus="paceFocus" @input="paceInput" @blur="paceCommit" @keydown.enter.prevent="paceCommit" @keydown.esc.stop.prevent="paceCancel"> percentage points a day
              <span class="cap-def">{{ settings.pace.points_per_day === null ? 'default' : `default is ${defaultPoints}` }} · <RouterLink class="link-btn" :to="paceHome" :tabindex="tabbable()">in Capacity</RouterLink></span></p>
            <label class="cap-opt"><input type="radio" :name="`use-${harness}`" :checked="everything" @change="paceMode('everything')"><span>Use everything before the reset</span></label>
            <p class="cap-note" aria-live="polite">{{ paceNote }}</p>
          </div>
        </dd>
        <dt><button type="button" class="cd-fh" :aria-expanded="open('boost')" :aria-controls="id('boost')" :tabindex="tabbable()" @click="emit('fold', 'boost')"><AppIcon class="cap-chev" name="chevron-right" :size="12" /><span class="eyebrow">Boost today</span></button></dt>
        <dd :class="{ open: open('boost') }">
          <p class="cd-sum" @click="emit('fold', 'boost')">{{ boostSummary(boost) }}</p>
          <div v-if="open('boost')" :id="id('boost')" class="cd-fb" role="group" aria-label="Boost today">
            <p class="cap-today">Allow up to <input class="field n" :value="boostShown" :placeholder="boostPlaceholder" :disabled="!boostEditable" inputmode="numeric" maxlength="3" aria-label="Allow up to, in percent" :aria-describedby="id('boost-note')" @focus="boostFocus" @input="boostInput" @blur="boostCommit" @keydown.enter.prevent="boostCommit" @keydown.esc.stop.prevent="boostCancel"> %
              <span class="seg" role="radiogroup" aria-label="The number is"><button type="button" role="radio" :aria-checked="as === 'used'" :disabled="!boostEditable" @click="boostAs('used')">used</button><button type="button" role="radio" :aria-checked="as === 'left'" :disabled="!boostEditable" @click="boostAs('left')">left</button></span> today<button v-if="boost" type="button" class="link-btn" :disabled="!boostEditable" @click="boostClear">Clear</button></p>
            <p :id="id('boost-note')" class="cap-note" :class="{ failed: boostNote }" aria-live="polite">{{ boostHelp }}</p>
          </div>
        </dd>
        <!-- AEON-1166: Models decides at the limit; its order continues, or waits where it ends in Wait. -->
        <dt><span class="eyebrow">At the limit</span></dt>
        <dd><p class="cd-line">Follows <RouterLink class="link-btn" to="/settings/models" :tabindex="tabbable()">Settings › Models</RouterLink></p></dd>
      </dl>
    </template>
  </div>
</template>

<style scoped>
p { margin: 0; }
.cd { min-width: 0; }
.link-btn { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 500; text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 2px; cursor: pointer; }
.link-btn:hover { text-decoration-color: currentColor; }
.link-btn:disabled { opacity: .55; cursor: default; }
.link-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }

.cd-h { display: flex; align-items: center; gap: 9px; margin: 0; color: var(--ink); font: 650 16px/1.35 var(--font); }
.cd-h :deep(.mark) { flex: none; }
/* Over pace and at the limit after today's points: an icon and words in the state's ink; never colour alone, never a fill or an edge bar. */
.cap-st { display: inline-flex; align-items: center; gap: 5px; margin-left: 4px; font-size: 13px; font-weight: 600; }
.cap-st::before { content: '·'; margin-right: 1px; color: var(--ink-3); font-weight: 400; }
.cap-st svg { flex: none; }
.cap-st.st-over_pace { color: var(--cap-over-ink); }
.cap-st.st-at_limit { color: var(--cap-lim-ink); }
.cap-rs { display: flex; align-items: center; gap: 6px; margin-top: 2px; color: var(--ink-2); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.cap-rs svg { flex: none; color: var(--ink-3); }
.cd-dl { display: grid; grid-template-columns: 128px minmax(0, 1fr); margin: 14px 0 0; border-top: 1px solid var(--line); }
.cd-dl > dt, .cd-dl > dd { margin: 0; padding: 10px 0; border-bottom: 1px solid var(--line); }
.cd-dl > dt:last-of-type, .cd-dl > dd:last-of-type { border-bottom: 0; }
.cd-dl > dt { display: flex; align-items: flex-start; padding-top: 16px; }
.cd-dl > dt > .eyebrow { margin: 0; line-height: 1.5; }
.cd-fh { display: inline-flex; align-items: center; gap: 6px; margin: -4px 0 0 -4px; padding: 4px 6px 4px 4px; border: 0; border-radius: 6px; background: transparent; cursor: pointer; }
.cd-fh .eyebrow { margin: 0; }
.cd-fh:focus-visible, .cd-sum:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.cap-chev { flex: none; color: var(--ink-3); transition: transform .15s ease; }
.cd-fh[aria-expanded="true"] .cap-chev { transform: rotate(90deg); }
.cd-sum { display: flex; align-items: center; min-height: 30px; color: var(--ink-2); font-size: 13.5px; text-align: left; cursor: pointer; font-variant-numeric: tabular-nums; }
.cd-dl dd.open .cd-sum { color: var(--ink); font-weight: 600; }
.cd-line, .cd-td { display: flex; align-items: center; flex-wrap: wrap; gap: 0 5px; min-height: 30px; color: var(--ink-2); font-size: 13.5px; }
.cd-td { font-variant-numeric: tabular-nums; }
.cd-fb { padding: 6px 0 4px; }
.cap-opt { display: flex; align-items: center; gap: 8px; min-height: 30px; color: var(--ink); font-size: 13px; cursor: pointer; }
.cap-opt input { accent-color: var(--teal); margin: 0; }
.cap-pp { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin: 0 0 6px 22px; color: var(--ink-2); font-size: 12.5px; }
.cap-def { color: var(--ink-3); }
.cap-today { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; color: var(--ink-2); font-size: 13px; }
.cap-pp .field.n, .cap-today .field.n { box-sizing: border-box; width: 5.5ch; min-width: 5.5ch; min-height: 30px; padding: 0 4px; text-align: center; font-variant-numeric: tabular-nums; }
.cap-today .link-btn { margin-left: auto; }
.cd-dl .seg { display: inline-flex; }
.cd-dl .seg button { min-height: 30px; padding: 0 14px; }
.cap-today .seg button { min-height: 26px; padding: 0 12px; }
.cap-note { min-height: 18px; margin-top: 7px; color: var(--ink-2); font-size: 12.5px; line-height: 1.45; font-variant-numeric: tabular-nums; }
.cap-note b { color: var(--ink); font-weight: 650; }
.cap-note.failed { color: var(--danger); }
.cd-quiet { color: var(--ink-3); font-size: 13px; }
@container working (max-width: 900px) {
  .cd-dl { grid-template-columns: minmax(0, 1fr); }
  .cd-dl > dt { padding: 10px 0 0; border-bottom: 0; }
  .cd-dl > dd { padding-top: 4px; }
}
@container working (max-width: 760px) {
  .cd-h { font-size: 15px; }
  .cd-fh, .cd-sum { min-height: 44px; margin-left: -4px; }
  .cd-sum { margin-left: 0; }
  .cd-dl .seg { display: flex; }
  .cd-dl .seg button { flex: 1 1 auto; min-height: 44px; padding: 0 12px; }
  .cap-today .seg button { min-height: 44px; }
  .cap-opt { min-height: 44px; }
  .cap-opt input { width: 20px; height: 20px; }
  .cap-pp .field.n, .cap-today .field.n { min-height: 44px; width: 7ch; min-width: 7ch; }
  .cap-note .link-btn, .cap-today .link-btn, .cap-def .link-btn, .cd-line .link-btn { min-height: 44px; min-width: 44px; display: inline-flex; align-items: center; }
}
@media (prefers-reduced-motion: reduce) { .cap-chev { transition: none; } }
</style>
