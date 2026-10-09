<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1036: the selected harness's usage on the right of the open dial. Reading
// first (week used and left, today's points, the bar, the pace state), settings
// second (Pace and Boost today as folds, the choice at the limit), accounts
// read-only. Folds open downward only, so nothing above them moves.
import { computed, ref, watch } from 'vue'
import { HARNESS_NAME } from '../../lib/capacity'
import {
  atLimitWords, boostNumber, boostSummary, dailyBar, detailHeadline, makeBoost, paceSummary, pct, resetsLine, stateWords, todayLine, typedPercent, typedPoints, weekParts,
  type AtLimit, type DailySettings, type DailyView, type EnteredAs,
} from '../../lib/dailyLimits'
import type { DialFold } from '../../lib/dialPrefs'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'

const props = defineProps<{
  view: DailyView; total: number; defaultPoints: number; tz?: string
  /** The person's next local midnight: the end of a Boost today. */
  until?: string
  paceOpen: boolean; boostOpen: boolean
  /** The person has the live right to manage accounts; Boost today is refused without it. */
  mayManage: boolean
  /** Only the height of this detail is wanted: folds closed, no ids, nothing focusable. */
  ghost?: boolean
}>()
const emit = defineEmits<{ change: [settings: DailySettings]; fold: [fold: DialFold] }>()
const harness = computed(() => props.view.harness)
const name = computed(() => HARNESS_NAME[harness.value] ?? harness.value)
const settings = computed(() => props.view.settings)
const bar = computed(() => dailyBar(props.view))
const week = computed(() => weekParts(props.view, props.tz))
const resets = computed(() => resetsLine(props.view.account, props.tz))
const open = (fold: DialFold) => !props.ghost && (fold === 'pace' ? props.paceOpen : props.boostOpen)
const change = (patch: Partial<DailySettings>) => emit('change', { ...settings.value, ...patch })
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
  // The default is followed, not copied: the person's own default may change.
  const stored = value === props.defaultPoints ? null : value
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

// ---------- At the limit ----------
const atLimit = (value: AtLimit) => { if (settings.value.at_limit !== value) change({ at_limit: value }) }
// A new harness starts from its own drafts.
watch(harness, () => { paceEditing.value = false; paceNote.value = ''; boostEditing.value = false; boostNote.value = ''; asLocal.value = 'used' })
const SETTINGS = '/settings/accounts'
const active = computed(() => props.view.account?.account_id)
</script>

<template>
  <div class="cd" :class="{ ghost }" :aria-hidden="ghost || undefined" :inert="ghost || undefined" :data-detail="ghost ? undefined : harness">
    <h3 class="cd-h"><HarnessMark :harness="harness" :size="16" /><span>{{ view.kind === 'none' ? `${name} · no account yet` : detailHeadline(view) }}</span></h3>

    <template v-if="view.kind === 'none'">
      <p class="cd-wk">No account of yours is linked to {{ name }} yet, so there is no daily limit to read or keep.</p>
      <dl class="cd-dl">
        <dt><span class="eyebrow">Account</span></dt>
        <dd><RouterLink class="link-btn cd-add" :to="`${SETTINGS}#add-account`" :tabindex="tabbable()"><AppIcon name="plus" :size="11" />Add account</RouterLink></dd>
      </dl>
    </template>
    <template v-else-if="view.kind === 'no_limit'">
      <p class="cd-wk">{{ name }} accounts here are billed by use and have no allowance window, so there is nothing to keep a daily limit on.</p>
      <dl class="cd-dl">
        <dt><span class="eyebrow">At the limit</span></dt><dd class="cd-quiet">Nothing to reach; the {{ total }} at once and the account still cap it.</dd>
        <dt><span class="eyebrow">{{ view.accounts.length > 1 ? 'Accounts' : 'Account' }}</span></dt>
        <dd><span class="cd-acc1">{{ view.accounts.map(a => a.label).join(' · ') }}</span><RouterLink class="link-btn cd-add" :to="`${SETTINGS}#add-account`" :tabindex="tabbable()"><AppIcon name="plus" :size="11" />Add account</RouterLink></dd>
      </dl>
    </template>
    <template v-else>
      <div class="cd-read">
        <p class="cd-wk"><b v-if="week.strong">{{ week.strong }}</b>{{ week.strong ? ' ' : '' }}{{ week.rest.trim() }}</p>
        <p class="cd-td">{{ todayLine(view) }}</p>
        <div v-if="bar" class="cap-bar" :class="{ 'has-tick': bar.tick }" role="img" :aria-label="bar.label">
          <span v-if="bar.teal" class="cb-teal" :style="{ width: `${bar.teal}%` }" />
          <span v-if="bar.gold" class="cb-gold" :style="{ width: `${bar.gold}%` }" />
          <span v-if="bar.red" class="cb-red" :style="{ width: `${bar.red}%` }" />
          <span v-if="bar.free" class="cb-free" :style="{ width: `${bar.free}%` }" />
          <b v-if="bar.tick" class="cb-tick" :style="{ left: `${bar.tick.at}%` }"><span>{{ bar.tick.label }}</span></b>
        </div>
        <div v-else class="cap-bar cap-bar-none" aria-hidden="true" />
        <p class="cap-st" :class="`st-${view.kind}`"><AppIcon :name="view.kind === 'over_pace' ? 'alert' : view.kind === 'at_limit' ? 'pause' : view.kind === 'unknown' ? 'info' : 'check'" :size="14" /><span>{{ stateWords(view) }}</span></p>
        <p v-if="resets" class="cap-rs"><AppIcon name="refresh" :size="13" /><span>{{ resets }}</span></p>
      </div>

      <dl class="cd-dl">
        <dt><button type="button" class="cd-fh" :aria-expanded="open('pace')" :aria-controls="id('pace')" :tabindex="tabbable()" @click="emit('fold', 'pace')"><AppIcon class="cap-chev" name="chevron-right" :size="12" /><span class="eyebrow">Pace</span></button></dt>
        <dd :class="{ open: open('pace') }">
          <p class="cd-sum" @click="emit('fold', 'pace')">{{ paceSummary(view) }}</p>
          <div v-if="open('pace')" :id="id('pace')" class="cd-fb" role="group" aria-label="Pace">
            <label class="cap-opt"><input type="radio" :name="`use-${harness}`" :checked="!everything" @change="paceMode('pace')"><span>Stay on pace</span></label>
            <p class="cap-pp"><input class="field n" :value="paceShown" :disabled="everything" inputmode="numeric" maxlength="2" aria-label="Percentage points a day" @focus="paceFocus" @input="paceInput" @blur="paceCommit" @keydown.enter.prevent="paceCommit" @keydown.esc.stop.prevent="paceCancel"> percentage points a day
              <span class="cap-def">{{ settings.pace.points_per_day === null ? 'default' : `default is ${defaultPoints}` }} · <RouterLink class="link-btn" :to="SETTINGS">edit</RouterLink></span></p>
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
        <dt><span :id="id('at-label')" class="eyebrow">At the limit</span></dt>
        <dd>
          <span class="seg" role="radiogroup" :aria-labelledby="id('at-label')"><button type="button" role="radio" :aria-checked="settings.at_limit === 'ladder'" :tabindex="tabbable(settings.at_limit === 'ladder' ? 0 : -1)" @click="atLimit('ladder')">Follow the model ladder</button><button type="button" role="radio" :aria-checked="settings.at_limit === 'wait'" :tabindex="tabbable(settings.at_limit === 'wait' ? 0 : -1)" @click="atLimit('wait')">Wait</button></span>
          <p class="cap-note">{{ settings.at_limit === 'ladder' ? `${view.next ? `${atLimitWords(view)}.` : 'New work moves on to the next model for the work at hand.'} ` : 'New work waits until midnight. Running agents finish. ' }}<RouterLink v-if="settings.at_limit === 'ladder'" class="link-btn" to="/settings/models" :tabindex="tabbable()">Models</RouterLink></p>
        </dd>
        <dt><span class="eyebrow">{{ view.accounts.length > 1 ? 'Accounts' : 'Account' }}</span></dt>
        <dd>
          <ol v-if="view.accounts.length > 1" class="cd-acc">
            <li v-for="a in view.accounts" :key="a.account_id" :class="{ now: a.account_id === active }"><span class="an">{{ a.label }}</span><span class="au">{{ a.details_redacted || a.used_pct === null ? 'usage private' : `${pct(a.used_pct)} % used${a.limit_used_pct !== null ? ` of ${pct(a.limit_used_pct)} %` : ''}` }}</span><span class="af">{{ a.floor_pct === null || a.details_redacted ? '' : `floor ${pct(a.floor_pct)} %` }}</span></li>
          </ol>
          <span v-else class="cd-acc1">{{ view.account?.label }}</span>
          <p class="cd-links"><RouterLink v-if="view.accounts.length > 1" class="link-btn" :to="SETTINGS" :tabindex="tabbable()">Per-account settings</RouterLink><RouterLink class="link-btn cd-add" :to="`${SETTINGS}#add-account`" :tabindex="tabbable()"><AppIcon name="plus" :size="11" />Add account</RouterLink></p>
        </dd>
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
.cd-read { display: grid; gap: 7px; max-width: 540px; margin-top: 12px; }
.cd-wk, .cd-td { color: var(--ink-2); font-size: 13.5px; line-height: 1.45; font-variant-numeric: tabular-nums; }
.cd > .cd-wk { margin-top: 12px; max-width: 540px; }
.cd-wk b { color: var(--ink); font-weight: 650; }
/* Today's bar: teal up to today's share, gold over it, red past the limit; the hairline tick marks the pace. */
.cap-bar { position: relative; display: flex; height: 10px; margin: 4px 0; border-radius: 5px; background: var(--line); }
.cap-bar.has-tick { margin-bottom: 18px; }
.cap-bar-none { background: var(--line); opacity: .6; }
.cap-bar > span { height: 100%; }
.cap-bar > span:first-child { border-radius: 5px 0 0 5px; }
.cap-bar > span:last-of-type { border-radius: 0 5px 5px 0; }
.cap-bar > span:only-of-type { border-radius: 5px; }
.cb-teal { background: var(--teal); opacity: .8; }
.cb-gold { background: var(--cap-over-fill); }
.cb-red { background: var(--cap-lim-fill); }
.cb-free { background: transparent; }
.cb-tick { position: absolute; top: -3px; bottom: -3px; width: 1.5px; margin-left: -.75px; background: var(--ink-2); }
.cb-tick span { position: absolute; top: 15px; left: 50%; transform: translateX(-50%); color: var(--ink-3); font-size: 11px; font-weight: 500; white-space: nowrap; }
/* Over pace and at the limit: a full tint, an icon and words; never colour alone, never an edge bar. */
.cap-st { display: inline-flex; align-items: center; gap: 6px; justify-self: start; min-height: 26px; padding: 0 10px 0 8px; border-radius: 999px; font-size: 12.5px; font-weight: 600; }
.cap-st.st-on_pace, .cap-st.st-everything { padding-left: 0; color: var(--ok); }
.cap-st.st-unknown { padding-left: 0; color: var(--ink-3); font-weight: 500; }
.cap-st.st-over_pace { background: var(--cap-over-bg); color: var(--cap-over-ink); }
.cap-st.st-at_limit { background: var(--cap-lim-bg); color: var(--cap-lim-ink); }
.cap-rs { display: flex; align-items: center; gap: 6px; color: var(--ink-2); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.cap-rs svg { flex: none; color: var(--ink-3); }
.cd-dl { display: grid; grid-template-columns: 128px minmax(0, 1fr); margin: 16px 0 0; border-top: 1px solid var(--line); }
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
.cd-acc1 { color: var(--ink); font-size: 13.5px; font-weight: 600; }
.cd-add { display: inline-flex; align-items: center; gap: 4px; margin-left: 14px; font-weight: 500; text-decoration: none; }
.cd-acc1 + .cd-add, .cd-links .cd-add:first-child { margin-left: 0; }
.cd-add:hover { text-decoration: underline; }
.cd-acc { display: grid; gap: 2px; max-width: 420px; margin: 0; padding: 0; list-style: none; counter-reset: acc; }
.cd-acc li { display: grid; grid-template-columns: 16px minmax(0, 1fr) auto auto; gap: 12px; align-items: baseline; padding: 4px 6px; border-radius: 8px; color: var(--ink-2); font-size: 12.5px; font-variant-numeric: tabular-nums; counter-increment: acc; }
.cd-acc li::before { content: counter(acc); color: var(--ink-3); font-family: var(--mono); font-size: 11px; }
.cd-acc li.now { background: var(--surface-sunken); }
.cd-acc .an { overflow: hidden; color: var(--ink); font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.cd-acc .af { color: var(--ink-3); }
.cd-links { display: flex; align-items: center; gap: 2px; margin-top: 8px; }
.cd-links .link-btn:first-child { font-weight: 500; }
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
  .cd-add, .cd-links .link-btn, .cap-note .link-btn, .cap-today .link-btn, .cap-def .link-btn { min-height: 44px; min-width: 44px; display: inline-flex; align-items: center; }
  .cd-acc li { grid-template-columns: 16px minmax(0, 1fr) auto; }
  .cd-acc .af { grid-column: 2 / -1; }
}
@media (prefers-reduced-motion: reduce) { .cap-chev { transition: none; } }
</style>
