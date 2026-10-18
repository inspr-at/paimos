<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1497 (Tick's approved fuel gauge): the selected harness's usage, reading first. One
// sentence answers how long the pool lasts, then the pool in muted ink and the pace verdict; then
// quota left over time with the burn strip, and each subscription as a battery. The heights are
// reserved, so loading, a refresh and every state keep the controls below in place (0 px).
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { accountRows, chartModel, defaultRange, VERDICT_ICON, type OutlookGroup, type UsageView } from '../../lib/quotaOutlook'
import type { IconName } from '../AppIcon.vue'
import AppIcon from '../AppIcon.vue'
import QuotaChart from './QuotaChart.vue'

const props = defineProps<{
  view: UsageView; state: 'loading' | 'ready' | 'error'
  generatedAt?: string; anchorHour?: string; tz?: string
  /** Only this block's height is wanted: no chart, no ids, nothing focusable. */
  ghost?: boolean
}>()
const emit = defineEmits<{ retry: [] }>()
const group = computed<OutlookGroup | null>(() => props.view.group)
const kind = computed(() => props.view.kind)
const quiet = computed(() => kind.value === 'not_reported')
const none = computed(() => kind.value === 'none')
// Pending: the outlook is being read (or failed); the plan's accounts keep their rows meanwhile.
const pending = computed(() => kind.value === 'pending')
const shown = computed<'loading' | 'ready' | 'error'>(() => pending.value ? (props.state === 'ready' ? 'loading' : props.state) : 'ready')
const reading = computed(() => !pending.value && !quiet.value && !none.value)
const model = computed(() => reading.value && group.value?.pool && props.generatedAt && props.anchorHour ? chartModel(group.value, props.generatedAt, props.anchorHour) : null)
const rows = computed(() => accountRows(group.value, props.tz, props.view.known))
const id = (part: string) => props.ghost ? undefined : `ub-${props.view.harness}-${part}`
const tab = computed(() => props.ghost ? -1 : undefined)

// The range: Past 1–7 days, Ahead 0–7. Until a step, the default follows the headline's moment.
const compact = typeof matchMedia === 'function' && matchMedia('(max-width: 720px)').matches
const touched = ref(false), past = ref(3), ahead = ref(2)
function follow() { if (!touched.value) { const r = defaultRange(props.view, compact); past.value = r.past; ahead.value = r.ahead } }
watch(() => props.view.focus, follow, { immediate: true })
function step(which: 'past' | 'ahead', delta: number) {
  touched.value = true
  if (which === 'past') past.value = Math.max(1, Math.min(7, past.value + delta))
  else ahead.value = Math.max(0, Math.min(7, ahead.value + delta))
}
const steppable = computed(() => !props.ghost && reading.value && !!model.value)
const days = (n: number, word: string) => `${n} ${n === 1 ? 'day' : 'days'} ${word}`
// Phones: the range behind ⋯, a menu that opens above the chart; the list folded to one line.
const menu = ref(false), listOpen = ref(false)
const menuEl = ref<HTMLElement>(), dotsEl = ref<HTMLElement>()
function outside(event: PointerEvent) { if (menu.value && !menuEl.value?.contains(event.target as Node) && !dotsEl.value?.contains(event.target as Node)) menu.value = false }
function escape(event: KeyboardEvent) { if (menu.value && event.key === 'Escape') { menu.value = false; dotsEl.value?.focus() } }
onMounted(() => { document.addEventListener('pointerdown', outside, true); document.addEventListener('keydown', escape) })
onBeforeUnmount(() => { document.removeEventListener('pointerdown', outside, true); document.removeEventListener('keydown', escape) })

const verdictIcon = computed(() => VERDICT_ICON[kind.value] as IconName)
const verdictTone = computed(() => ({ fast: 't-fast', slow: 't-slow', pace: 't-pace' } as Record<string, string>)[kind.value] ?? 't-none')
const chartCount = computed(() => { const n = group.value?.pool?.accounts ?? 0; return n === 2 ? 'both accounts' : n === 1 ? 'one account' : `all ${n} accounts` })
const offShown = computed(() => !!model.value && [...model.value.past.values()].some(b => b.flag))
const accountsWord = computed(() => `${rows.value.length} ${rows.value.length === 1 ? 'account' : 'accounts'}`)
</script>

<template>
  <div class="ub" :data-load="shown">
    <!-- The answer first: one sentence (two lines kept), the pool in muted ink, the pace verdict. -->
    <div v-if="none" class="ub-sum short"><p class="ub-hd one">No accounts yet<span class="ub-of">·</span><RouterLink class="link-btn ln-ic" to="/settings/accounts#add-account" :tabindex="tab">Add an account<AppIcon name="arrow" :size="12" /></RouterLink></p></div>
    <div v-else-if="quiet" class="ub-sum short"><p class="ub-hd one">Quota not reported<span class="ub-of">· {{ accountsWord }}</span></p></div>
    <div v-else-if="shown === 'loading'" class="ub-sum" aria-busy="true"><div class="ub-hd"><span class="sk2"><span class="skeleton" /><span class="skeleton" /></span><span class="sr-only">Reading usage…</span></div><p class="ub-l2"><span class="skeleton" /></p><div class="ub-v"><span class="skeleton" /></div></div>
    <div v-else-if="shown === 'error'" class="ub-sum"><div class="ub-hd"><p class="err" :role="ghost ? undefined : 'alert'"><AppIcon name="alert" :size="15" /><span>Usage couldn’t be read</span><span class="ub-of">·</span><button type="button" class="link-btn" :tabindex="tab" @click="emit('retry')">Try again</button></p></div><p class="ub-l2" /><div class="ub-v" /></div>
    <div v-else class="ub-sum">
      <p class="ub-hd">{{ view.head }}</p>
      <p class="ub-l2" :class="{ stale: view.stale }"><span>{{ view.muted }}</span></p>
      <p class="ub-v"><AppIcon :class="verdictTone" :name="verdictIcon" :size="15" /><span class="vt"><b :class="verdictTone">{{ view.verdict }}</b><template v-if="view.paceLine"> · {{ view.paceLine }}</template></span></p>
    </div>

    <div v-if="!none" class="ub-chart">
      <div class="ub-ch-h">
        <p class="ub-ch-t">Quota left<span v-if="!quiet && group?.pool" class="ub-ch-n"> · {{ chartCount }}</span></p>
        <template v-if="!quiet">
          <div class="ub-steps">
            <div class="ub-step" role="group" aria-label="Days back"><span class="ub-sl">Past</span><button type="button" class="ub-sb" aria-label="One day less back" :disabled="!steppable || past <= 1" :tabindex="tab" @click="step('past', -1)"><AppIcon name="minus" :size="12" /></button><output class="ub-sv">{{ past }} d</output><button type="button" class="ub-sb" aria-label="One day more back" :disabled="!steppable || past >= 7" :tabindex="tab" @click="step('past', 1)"><AppIcon name="plus" :size="12" /></button></div>
            <div class="ub-step" role="group" aria-label="Days ahead"><span class="ub-sl">Ahead</span><button type="button" class="ub-sb" aria-label="One day less ahead" :disabled="!steppable || ahead <= 0" :tabindex="tab" @click="step('ahead', -1)"><AppIcon name="minus" :size="12" /></button><output class="ub-sv">{{ ahead }} d</output><button type="button" class="ub-sb" aria-label="One day more ahead" :disabled="!steppable || ahead >= 7" :tabindex="tab" @click="step('ahead', 1)"><AppIcon name="plus" :size="12" /></button></div>
          </div>
          <button ref="dotsEl" type="button" class="ub-dots" :aria-label="`Range: ${days(past, 'back')}, ${days(ahead, 'ahead')}`" :aria-expanded="!ghost && menu" :disabled="!steppable" :tabindex="tab" @click="menu = !menu"><AppIcon name="more" :size="16" /></button>
        </template>
      </div>
      <div v-if="menu && !ghost && !quiet" ref="menuEl" class="ub-pop" role="group" aria-label="Range">
        <div class="ub-step" role="group" aria-label="Days back"><span class="ub-sl">Past</span><button type="button" class="ub-sb" aria-label="One day less back" :disabled="past <= 1" @click="step('past', -1)"><AppIcon name="minus" :size="12" /></button><output class="ub-sv">{{ past }} d</output><button type="button" class="ub-sb" aria-label="One day more back" :disabled="past >= 7" @click="step('past', 1)"><AppIcon name="plus" :size="12" /></button></div>
        <div class="ub-step" role="group" aria-label="Days ahead"><span class="ub-sl">Ahead</span><button type="button" class="ub-sb" aria-label="One day less ahead" :disabled="ahead <= 0" @click="step('ahead', -1)"><AppIcon name="minus" :size="12" /></button><output class="ub-sv">{{ ahead }} d</output><button type="button" class="ub-sb" aria-label="One day more ahead" :disabled="ahead >= 7" @click="step('ahead', 1)"><AppIcon name="plus" :size="12" /></button></div>
      </div>
      <QuotaChart v-if="model && !ghost" :model="model" :name="view.name" :past="past" :ahead="ahead" :tz="tz" :info-id="id('info')!" />
      <template v-else>
        <div class="ub-plot" :aria-busy="shown === 'loading' || undefined">
          <template v-if="!ghost">
            <p v-if="quiet" class="ub-empty"><span class="ub-rule" aria-hidden="true" />{{ view.name }} doesn’t report quota. Burn appears once it does.</p>
            <template v-else-if="shown === 'loading'"><span class="skeleton sk-line" /><span class="skeleton sk-strip" /></template>
            <p v-else-if="shown === 'error'" class="ub-empty">No readings to draw.</p>
          </template>
        </div>
        <p class="ub-info" />
      </template>
      <ul v-if="!quiet" class="ub-legend" aria-label="Legend">
        <li><svg width="18" height="10" aria-hidden="true"><path d="M1 5h16" class="lg-past" /></svg>left</li>
        <li><svg width="18" height="10" aria-hidden="true"><path d="M1 5h16" class="lg-cur" /></svg>at current pace</li>
        <li><svg width="18" height="10" aria-hidden="true"><path d="M2 5h15" class="lg-safe" /></svg>safe pace</li>
        <li><svg width="12" height="14" aria-hidden="true"><path d="M2 11h4V3h4" class="lg-step" /></svg>refill</li>
        <li><svg width="16" height="10" aria-hidden="true"><rect x="1" y="3" width="14" height="6" class="lg-empty" /><path d="M2 9l5-6M6 9l5-6M10 9l5-6" class="lg-empty-h" /></svg>empty</li>
        <li class="lg-br" aria-hidden="true" />
        <li v-for="r in rows" :key="r.id" class="acc-k"><span class="ub-dot" :style="{ background: r.shade }" />{{ r.name }}</li>
        <li><svg width="14" height="10" aria-hidden="true"><rect x="1" y="0" width="12" height="10" rx="1.5" class="lg-proj" /><path d="M1 8l7-8M5 10l8-9" class="lg-proj-h" /></svg>projected</li>
        <li v-if="offShown"><svg width="16" height="10" aria-hidden="true"><rect x="1" y="3" width="14" height="6" class="lg-off" /><path d="M2 9l5-6M6 9l5-6M10 9l5-6" class="lg-off-h" /></svg>off</li>
      </ul>
    </div>

    <template v-if="!none">
      <button type="button" class="ub-acc-t" :aria-expanded="!ghost && listOpen" :aria-controls="id('list')" :tabindex="tab" @click="listOpen = !listOpen"><span><b>{{ accountsWord }}</b> · {{ listOpen && !ghost ? 'hide' : 'show' }}</span><AppIcon class="ic" name="chevron" :size="14" /></button>
      <ul :id="id('list')" class="ub-list" :class="{ folded: !listOpen || ghost }" aria-label="Subscriptions">
        <li v-for="r in rows" :key="r.id" class="ub-acc" :data-account="r.id">
          <span class="ub-dot" :style="quiet || (r.state === 'not_reported' && !pending) ? undefined : { background: r.shade }" />
          <span class="ub-an">{{ r.name }}</span>
          <template v-if="pending && shown === 'loading'"><span class="ub-meta"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></span><span class="skeleton" /></template>
          <span v-else-if="pending" class="ub-nr"><AppIcon name="ring-dashed" :size="12" />not read</span>
          <span v-else-if="r.state === 'not_reported' || r.state === 'private'" class="ub-nr"><AppIcon name="ring-dashed" :size="12" />{{ r.state === 'private' ? 'Usage private' : 'Quota not reported' }}</span>
          <template v-else>
            <span class="ub-meta">
              <span class="ub-bat"><span class="bat" role="img" :aria-label="`${r.left} % left`"><i :style="{ width: `${r.left}%`, background: r.shade }" /></span><span><b>{{ r.left }} %</b> <span class="bl">left</span></span></span>
              <span class="ub-rs">{{ r.refills }}</span>
              <span class="ub-fc" :class="r.tone">{{ r.forecast }}</span>
            </span>
            <span class="ub-st" :class="`s-${r.state}`"><AppIcon v-if="r.stateIcon" :name="r.stateIcon as IconName" :size="12" />{{ r.stateWord }}</span>
          </template>
        </li>
      </ul>
      <p class="ub-link"><RouterLink class="link-btn ln-ic" to="/settings/accounts" :tabindex="tab">Accounts and limits<AppIcon name="arrow" :size="12" />Settings</RouterLink></p>
    </template>
  </div>
</template>

<style scoped>
p { margin: 0; }
.ub { display: grid; grid-template-columns: minmax(0, 1fr); margin-top: 10px; }
.ub > * { min-width: 0; }
.link-btn { display: inline; padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 500; text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 2px; cursor: pointer; }
@media (hover: hover) { .link-btn:hover { text-decoration-color: currentColor; } }
.link-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
.ln-ic { display: inline-flex; align-items: center; gap: 4px; text-decoration: none; }
@media (hover: hover) { .ln-ic:hover { text-decoration: underline; } }
.t-fast { color: var(--warn-ink); }
.t-slow { color: var(--verdict-slow); }
.t-pace { color: var(--ok); }
.t-none, .quiet { color: var(--ink-3); }
.ub-sum { display: grid; grid-template-rows: calc(2 * 24px) 20px 20px; row-gap: 2px; align-content: start; }
.ub-sum.short { grid-template-rows: 30px; }
.ub-hd { align-self: stretch; overflow: hidden; color: var(--ink); font: 650 17.5px/24px var(--font); letter-spacing: -.005em; text-wrap: pretty; font-variant-numeric: tabular-nums; }
.ub-hd.one { display: flex; align-items: baseline; gap: 8px; white-space: nowrap; }
.ub-hd .ub-of { color: var(--ink-3); font-size: 13px; font-weight: 500; }
.ub-hd .ln-ic { font-size: 13.5px; }
.ub-hd .sk2 { display: grid; gap: 10px; padding-top: 6px; }
.ub-hd .sk2 .skeleton { height: 14px; }
.ub-hd .sk2 .skeleton:last-child { width: 62%; }
.ub-hd .err { display: flex; align-items: center; gap: 6px; padding-top: 2px; color: var(--ink-2); font-size: 13.5px; font-weight: 500; }
.ub-hd .err > svg { color: var(--danger); }
.ub-l2 { display: flex; align-items: center; min-width: 0; overflow: hidden; color: var(--ink-3); font-size: 13px; line-height: 20px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.ub-l2 > span { overflow: hidden; text-overflow: ellipsis; }
.ub-l2 .skeleton { width: 54%; height: 10px; }
.ub-v { display: flex; align-items: center; gap: 7px; min-width: 0; font-size: 13px; line-height: 20px; font-variant-numeric: tabular-nums; }
.ub-v > svg { flex: none; }
.ub-v .vt { min-width: 0; overflow: hidden; color: var(--ink-2); white-space: nowrap; text-overflow: ellipsis; }
.ub-v .vt b { font-weight: 650; }
.ub-v .skeleton { width: 70%; height: 10px; }
.ub-chart { position: relative; margin-top: 14px; }
.ub-ch-h { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 30px; }
.ub-ch-t { min-width: 0; overflow: hidden; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; text-overflow: ellipsis; }
.ub-ch-n { color: var(--ink-3); font-weight: 500; }
.ub-steps { display: flex; align-items: center; gap: 14px; flex: none; }
.ub-step { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 12px; font-variant-numeric: tabular-nums; }
.ub-sl { margin-right: 2px; }
.ub-sb { position: relative; display: grid; place-items: center; flex: none; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-2); box-shadow: inset 0 0 0 1px var(--line-2); cursor: pointer; }
@media (hover: hover) { .ub-sb:hover:not(:disabled) { background: var(--seg-on); color: var(--teal-ink); } }
.ub-sb:disabled { opacity: .4; cursor: default; }
.ub-sb:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.ub-sv { display: inline-block; width: 3.2ch; color: var(--ink); font-weight: 650; text-align: center; }
.ub-dots { display: none; }
.ub-pop { position: absolute; z-index: 20; top: 40px; right: 0; display: grid; gap: 2px; padding: 8px 10px 8px 14px; border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.ub-pop .ub-step { justify-content: space-between; gap: 6px; min-width: 196px; font-size: 13px; color: var(--ink-2); }
.ub-plot { position: relative; height: 226px; margin-top: 2px; border-radius: 8px; }
.ub-plot > .sk-line { position: absolute; left: 38px; right: 0; top: 30px; bottom: 64px; height: auto; border-radius: 8px; }
.ub-plot > .sk-strip { position: absolute; left: 38px; right: 0; bottom: 20px; height: 28px; border-radius: 6px; }
.ub-plot .ub-empty { position: absolute; z-index: 1; left: 38px; right: 0; top: 30px; bottom: 64px; display: grid; place-items: center; padding: 0 16px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line-2); color: var(--ink-3); font-size: 13px; line-height: 1.45; text-align: center; text-wrap: balance; }
.ub-plot .ub-rule { position: absolute; left: 0; right: 0; bottom: -45px; border-top: 1px solid var(--line-2); }
.ub-info { height: 20px; margin-top: 4px; }
.ub-legend { display: flex; flex-wrap: wrap; align-content: start; gap: 2px 14px; min-height: 40px; margin: 4px 0 0; padding: 0; list-style: none; color: var(--ink-2); font-size: 12px; line-height: 19px; }
.ub-legend li { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.ub-legend .lg-br { flex-basis: 100%; height: 0; }
.ub-legend svg { flex: none; overflow: visible; }
.lg-past { fill: none; stroke: var(--teal); stroke-width: 2; stroke-linecap: round; }
.lg-cur { fill: none; stroke: var(--teal); stroke-width: 2; stroke-dasharray: 6 4; }
.lg-safe { fill: none; stroke: var(--ink-3); stroke-width: 1.8; stroke-dasharray: .1 4; stroke-linecap: round; }
.lg-step { fill: none; stroke: var(--ink-2); stroke-width: 1.6; stroke-linejoin: round; stroke-linecap: round; }
.lg-empty { fill: color-mix(in srgb, var(--danger) 14%, transparent); }
.lg-empty-h { fill: none; stroke: var(--danger); stroke-width: 1.1; }
.lg-proj { fill: color-mix(in srgb, var(--ink-3) 38%, transparent); }
.lg-proj-h { fill: none; stroke: var(--ink-3); stroke-width: 1.2; }
.lg-off { fill: color-mix(in srgb, var(--gold) 16%, transparent); }
.lg-off-h { fill: none; stroke: var(--warn-ink); stroke-width: 1.1; }
.ub-dot { flex: none; width: 9px; height: 9px; border-radius: 50%; }
.ub-list { display: grid; margin: 8px 0 0; padding: 0; list-style: none; border-top: 1px solid var(--line); }
.ub-acc { display: grid; grid-template-columns: 9px minmax(76px, 1fr) 64px 104px 130px 54px; align-items: center; gap: 0 7px; height: 34px; border-bottom: 1px solid var(--line); color: var(--ink-2); font-size: 12px; font-variant-numeric: tabular-nums; }
.ub-acc:last-child { border-bottom: 0; }
.ub-an { min-width: 0; overflow: hidden; color: var(--ink); font-weight: 600; white-space: nowrap; text-overflow: ellipsis; }
.ub-bat { display: inline-flex; align-items: center; gap: 8px; white-space: nowrap; }
.ub-bat .bl { color: var(--ink-3); }
.ub-bat b { color: var(--ink); font-weight: 600; }
.bat { position: relative; flex: none; width: 26px; height: 12px; padding: 2px; border-radius: 3px; box-shadow: inset 0 0 0 1.25px var(--ink-3); box-sizing: border-box; }
.bat::after { content: ''; position: absolute; top: 3.5px; right: -3px; width: 2px; height: 5px; border-radius: 0 1.5px 1.5px 0; background: var(--ink-3); }
.bat > i { display: block; height: 100%; min-width: 0; border-radius: 1.5px; }
.ub-rs, .ub-fc { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ub-fc { color: var(--ink-2); }
.ub-fc.t-fast { color: var(--warn-ink); }
.ub-fc.t-slow { color: var(--verdict-slow); }
.ub-fc.t-pace { color: var(--ok); }
.ub-fc.quiet { color: var(--ink-3); }
.ub-st { display: inline-flex; align-items: center; gap: 5px; min-height: 18px; white-space: nowrap; color: var(--ink-3); font-weight: 500; }
.ub-st.s-active { color: var(--teal-ink); font-weight: 600; }
.ub-st.s-held { color: var(--ink-2); }
.ub-acc .skeleton { height: 9px; }
.ub-acc .ub-nr { grid-column: 3 / -1; display: inline-flex; align-items: center; gap: 6px; color: var(--ink-3); }
.ub-acc-t { display: none; }
.ub-link { display: flex; align-items: center; min-height: 30px; margin-top: 6px; font-size: 13px; }
@container working (min-width: 761px) { .ub-acc > .ub-meta { display: contents; } .ub-bat .bl { display: none; } .ub-list.folded { display: grid; } }
/* Phone: the answer, a compact chart with its range behind ⋯, the list folded to one line. */
@container working (max-width: 760px) {
  .ub-sum { grid-template-rows: calc(3 * 23px) 20px 40px; }
  .ub-hd { font-size: 16.5px; line-height: 23px; }
  .ub-v { align-items: flex-start; }
  .ub-v > svg { margin-top: 2px; }
  .ub-v .vt { white-space: normal; }
  .ub-ch-n, .ub-steps { display: none; }
  .ub-dots { display: grid; place-items: center; flex: none; width: 44px; height: 44px; margin: -7px -8px -7px 0; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-2); cursor: pointer; }
  .ub-dots[aria-expanded="true"] { background: var(--row-hover); color: var(--teal-ink); }
  .ub-dots:focus-visible { outline: none; box-shadow: var(--focus-ring); }
  .ub-pop { top: auto; bottom: calc(100% - 2px); }
  .ub-pop .ub-sb { width: 44px; height: 44px; }
  .ub-plot { height: 186px; }
  .ub-plot > .sk-line, .ub-plot .ub-empty { bottom: 57px; }
  .ub-plot > .sk-strip { height: 22px; }
  .ub-legend { min-height: 20px; }
  .ub-legend > li:not(.acc-k) { display: none; }
  .ub-acc-t { display: flex; align-items: center; justify-content: space-between; width: 100%; min-height: 44px; margin-top: 6px; padding: 0; border: 0; border-top: 1px solid var(--line); background: transparent; color: var(--ink-2); font: inherit; font-size: 13px; text-align: left; font-variant-numeric: tabular-nums; cursor: pointer; }
  .ub-acc-t b { color: var(--ink); font-weight: 600; }
  .ub-acc-t .ic { color: var(--ink-3); transition: transform .15s ease; }
  .ub-acc-t[aria-expanded="true"] .ic { transform: rotate(180deg); }
  .ub-acc-t:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 6px; }
  .ub-list { margin-top: 0; }
  .ub-list.folded { display: none; }
  .ub-acc { grid-template-columns: 9px minmax(0, 1fr) auto; grid-template-areas: 'd n s' '. w w'; height: auto; padding: 8px 0; gap: 4px 10px; }
  .ub-acc > .ub-dot { grid-area: d; }
  .ub-acc > .ub-an { grid-area: n; }
  .ub-acc > .ub-st { grid-area: s; }
  .ub-acc > .ub-meta { grid-area: w; display: flex; align-items: center; gap: 4px 12px; flex-wrap: wrap; color: var(--ink-2); }
  .ub-acc > .ub-nr { grid-area: w; }
  .ub-link { min-height: 44px; margin-top: 0; }
  .ub-link .link-btn { min-height: 44px; display: inline-flex; align-items: center; }
}
@media (pointer: coarse) { .ub-sb { width: 44px; height: 44px; } .ub-pop .ub-step { min-height: 44px; } }
@media (prefers-reduced-motion: reduce) { .ub-acc-t .ic { transition: none; } }
</style>
