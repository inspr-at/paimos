<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Capacity (AEON-1171, AEON-1175; design AEON-1149): how much agents may use,
// and when. The one-time plan card is this part's only Needs-you item. "How much agents
// may use each day" carries the rule sentence and the bar's legend, the default pace,
// and a row per harness: its pace (one stored value, also edited inline on the Agents
// dial), a bar per account, Boost today while it runs, and what happens at the limit.
// "When agents work" is PacingSettings. #pace-<harness> focuses that harness's row;
// #when (and the older #capacity-and-load) focuses the pacing editors.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRefs, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { HARNESS_NAME, hidesCapacityLimit, POOL_ORDER } from '../../lib/capacity'
import { dailyOf, DEFAULT_DAILY_POINTS, pct, typedPoints, type PaceChoice, type PlanRead } from '../../lib/dailyLimits'
import { capacityCaption } from '../../lib/quotaBar'
import { toast } from '../../lib/toast'
import { useAccountsSettings } from '../../lib/useAccountsSettings'
import { useProfile } from '../../stores/profile'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import AccountsPanel from './AccountsPanel.vue'
import PacingSettings from './PacingSettings.vue'
import QuotaBar from './QuotaBar.vue'
import QuotaLegend from './QuotaLegend.vue'

const ctx = useAccountsSettings('capacity')
const { manage, owner, planCard, planBusy, confirmPlan, cardSecondary, accounts, capacity, agents, quota, agentsPlan, paceBusy } = toRefs(ctx)
const route = useRoute(), router = useRouter()
const pacing = ref<InstanceType<typeof PacingSettings>>()
onMounted(() => { ctx.bindPacing(() => pacing.value?.reveal()); void useProfile().load() })
onBeforeUnmount(() => { ctx.bindPacing(null) })
// The Agents page's pacing line and the old Accounts and computers link land here (AEON-721).
watch(() => route.hash, async hash => {
  if (hash !== '#when' && hash !== '#capacity-and-load') return
  await nextTick(); pacing.value?.reveal(); void router.replace({ path: route.path, query: route.query, hash: '' })
}, { immediate: true })

// ---------- How much agents may use each day ----------
// The legend names Keep for you whenever the person keeps something, as in Accounts.
const keeps = computed(() => capacity.value.schedule.reserve !== 'off')
const defaultPoints = computed(() => agentsPlan.value?.daily_default_points ?? DEFAULT_DAILY_POINTS)
const name = (harness: string) => HARNESS_NAME[harness] ?? harness
const rank = (harness: string) => { const i = POOL_ORDER.indexOf(harness); return i < 0 ? POOL_ORDER.length : i }
const PACE_WORDS = { everything: 'Use everything', points: (n: number) => `+${n} percentage points a day` }
const harnesses = computed(() => {
  const plan = agentsPlan.value, groups = new Map<string, typeof accounts.value>()
  for (const a of accounts.value) groups.set(a.harness, [...(groups.get(a.harness) ?? []), a])
  return [...groups].sort(([x], [y]) => rank(x) - rank(y) || x.localeCompare(y)).map(([key, list]) => {
    const state = plan?.daily_state?.[key]
    // The plan's order is the order agents use the accounts in; accounts outside the person's plan follow.
    const order = (a: (typeof list)[number]) => state?.accounts.find(d => a.records.some(r => r.id === d.account_id))?.order ?? Infinity
    const rows = [...list].sort((a, b) => order(a) - order(b)).map(account => {
      const q = quota.value(account)
      return {
        account, q, floor: q.daily && !q.daily.details_redacted ? q.daily.floor_pct : null,
        caption: capacityCaption(q.bar, { period: q.period, resetsAt: q.daily?.resets_at ?? q.window?.reading.resets_at, now: agents.value.now, timezone: capacity.value.timezone, low: q.low, vendor: account.vendor, atLimit: q.atLimit }),
      }
    })
    // Billed by use, or a harness without a quota reader: nothing to pace.
    const blind = state?.state === 'no_limit' || (hidesCapacityLimit(key) && rows.every(r => !r.q.bar.known))
    const settings = dailyOf(plan?.daily, key)
    const boost = settings.boost_today && Date.parse(settings.boost_today.until) > agents.value.now ? settings.boost_today : null
    return {
      key, name: name(key), rows, blind, settings, boost,
      // Pace is the person's plan: only a harness with an account in it has one to set.
      paced: !blind && !!state && !!plan,
      label: settings.pace.mode === 'everything' ? PACE_WORDS.everything : PACE_WORDS.points(settings.pace.points_per_day ?? defaultPoints.value),
      follows: settings.pace.mode === 'pace' && settings.pace.points_per_day === null,
      sub: blind ? 'no daily limit' : rows.length > 1 ? `${rows.length} accounts${state ? ' · used in this order' : ''}` : '',
    }
  })
})
// The picker keeps one width whatever it says (AEON-541): its widest label is laid under the shown one.
const pickerSizers = computed(() => [PACE_WORDS.everything, `${PACE_WORDS.points(50)} · default`])
// At the limit, new work follows Settings › Models; a harness still set to wait on the dial says so (until AEON-1166).
const waiters = computed(() => {
  const names = harnesses.value.filter(h => h.paced && h.settings.at_limit === 'wait').map(h => h.name)
  if (!names.length) return ''
  const list = names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names.at(-1)}` : names[0]
  return ` ${list} ${names.length > 1 ? 'wait' : 'waits'} until midnight instead, as set on the Agents dial.`
})

// ---------- The default pace: typed, saved on Enter or leaving the field, with Undo ----------
const defDraft = ref(''), defEditing = ref(false)
const defShown = computed(() => defEditing.value ? defDraft.value : String(defaultPoints.value))
function defFocus() { defEditing.value = true; defDraft.value = String(defaultPoints.value) }
function defInput(event: Event) { defEditing.value = true; defDraft.value = (event.target as HTMLInputElement).value }
function defCommit() {
  if (!defEditing.value) return
  defEditing.value = false
  const value = typedPoints(defDraft.value)
  if (value === undefined) { toast('Use a whole number from 1 to 50 for the default pace.', { tone: 'error' }); return }
  if (value !== defaultPoints.value) void ctx.saveDefaultPace(value)
}
// Esc leaves the field first, keeping what is stored.
function defCancel(event: KeyboardEvent) { defEditing.value = false; (event.target as HTMLInputElement).blur() }
watch(owner, () => { defEditing.value = false })

// ---------- The pace picker: bound to the plan as it was when it opened ----------
const picker = ref<{ harness: string; anchor: HTMLElement; basis: PlanRead; owner: string } | null>(null)
const ownDraft = ref(''), ownNote = ref('')
const pickerPace = computed(() => picker.value ? dailyOf(picker.value.basis.daily, picker.value.harness).pace : null)
const pickerDefault = computed(() => picker.value?.basis.daily_default_points ?? DEFAULT_DAILY_POINTS)
const pickerAccounts = computed(() => harnesses.value.find(h => h.key === picker.value?.harness)?.rows.length ?? 0)
function openPicker(harness: string, event: Event) {
  const basis = agentsPlan.value
  if (!manage.value || !basis || paceBusy.value) return
  if (picker.value?.harness === harness) { closePicker(true); return }
  const pace = dailyOf(basis.daily, harness).pace, def = basis.daily_default_points ?? DEFAULT_DAILY_POINTS
  picker.value = { harness, anchor: event.currentTarget as HTMLElement, basis, owner: owner.value }
  ownDraft.value = String(pace.mode === 'pace' && pace.points_per_day !== null ? pace.points_per_day : Math.min(50, def + 5))
  ownNote.value = ''
}
function closePicker(restore: boolean) {
  const anchor = picker.value?.anchor
  picker.value = null
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
const ownField = ref<HTMLInputElement>()
function choose(mode: PaceChoice['mode']) {
  const p = picker.value
  if (!p || p.owner !== owner.value) { closePicker(false); return }
  let choice: PaceChoice
  if (mode === 'own') {
    const points = typedPoints(ownDraft.value)
    if (points === undefined) { ownNote.value = 'Whole numbers from 1 to 50.'; ownField.value?.focus(); return }
    choice = { mode, points }
  } else choice = { mode }
  closePicker(true)
  void ctx.savePace(p.harness, choice, p.basis)
}
/** Arrow keys move between the three choices; Enter or Space picks one. */
function pickerKeys(event: KeyboardEvent) {
  if ((event.key !== 'ArrowDown' && event.key !== 'ArrowUp') || !(event.target as HTMLElement).matches('[role="radio"]')) return
  const radios = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="radio"]')]
  const at = radios.indexOf(event.target as HTMLElement)
  event.preventDefault()
  radios[(at + (event.key === 'ArrowDown' ? 1 : -1) + radios.length) % radios.length]?.focus()
}
watch([owner, manage], () => closePicker(false))

// #pace-<harness> (the dial's "in Capacity") focuses that harness's row once it is there.
const paceHash = computed(() => /^#pace-([a-z0-9_-]+)$/.exec(route.hash)?.[1] ?? '')
watch([paceHash, harnesses], async ([harness]) => {
  if (!harness || !harnesses.value.some(h => h.key === harness)) return
  await nextTick()
  const row = document.getElementById(`pace-${harness}`)
  if (!row) return
  row.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  row.focus({ preventScroll: true })
  void router.replace({ path: route.path, query: route.query, hash: '' })
}, { immediate: true })
</script>

<template>
  <AccountsPanel :ctx="ctx">
    <template #head>
      <header class="page-head"><h2>How much agents may use, and when</h2><p>Your plan for every account you can use. Agents pace each account so its week lasts; the Agents dial shows it live.</p></header>
      <div v-if="planCard" class="plan-card" role="region" aria-label="The plan">
        <p><b>{{ planCard.title }}</b> {{ planCard.text }}</p>
        <span class="pc-actions">
          <button class="btn" type="button" :disabled="planBusy" @click="cardSecondary">{{ planCard.secondary }}</button>
          <button class="btn primary" type="button" :disabled="planBusy" @click="confirmPlan('auto')">{{ planCard.primary }}</button>
        </span>
      </div>
    </template>
    <section id="daily" class="zone daily" aria-labelledby="daily-title">
      <div class="zone-head"><h3 id="daily-title">How much agents may use each day</h3><p>Per harness, for every account it uses</p></div>
      <div class="rule"><AppIcon name="info" :size="15" /><div><p>Agents stop at whichever comes first: <b>today’s limit</b>, what’s <b>kept for you</b> while you work, or the account’s <b>floor</b>. Warnings never stop agents.</p><QuotaLegend :keep="keeps" /></div></div>
      <div class="dl-def">
        <span class="t"><b id="def-pace-label">Default pace</b><small>Stay on pace: each day agents may use this much more of an account’s week. Every harness follows it unless it has its own.</small></span>
        <span class="pp"><span class="plus" aria-hidden="true">+</span><input
          id="def-pace" class="field n" :value="defShown" inputmode="numeric" maxlength="2" autocomplete="off" aria-label="Default pace, percentage points a day" :disabled="!manage || !agentsPlan" :aria-busy="paceBusy === 'default' || undefined"
          @focus="defFocus" @input="defInput" @blur="defCommit" @keydown.enter.prevent="defCommit" @keydown.esc.stop.prevent="defCancel"
        >percentage points a day</span>
      </div>
      <div v-for="h in harnesses" :id="`pace-${h.key}`" :key="h.key" class="hz" tabindex="-1" :data-harness="h.key">
        <div class="hz-h">
          <HarnessMark :harness="h.key" />
          <span class="hz-n"><b>{{ h.name }}</b><span v-if="h.sub" class="hz-s">{{ h.sub }}</span></span>
          <template v-if="h.paced">
            <button
              v-if="manage" type="button" class="pick-s" :data-pace="h.key" aria-haspopup="dialog" :aria-expanded="picker?.harness === h.key" :aria-busy="paceBusy === h.key || undefined"
              :aria-label="`Daily limit for ${h.name}: ${h.label}${h.follows ? ', the default' : ''}. Change`" @click="openPicker(h.key, $event)"
            ><span class="pick-w"><span class="pick-t">{{ h.label }}<span v-if="h.follows" class="pe"> · default</span></span><span v-for="w in pickerSizers" :key="w" class="pick-t sizer" aria-hidden="true">{{ w }}</span></span><AppIcon name="chevron" :size="13" /></button>
            <span v-else class="pick-s ro" :data-pace="h.key">{{ h.label }}<span v-if="h.follows" class="pe"> · default</span></span>
          </template>
        </div>
        <p v-if="h.blind" class="hz-none">{{ h.name }} doesn’t report its quota, so there is nothing to pace. The agent count on the dial still caps it.</p>
        <div v-else class="hz-acc">
          <div v-for="r in h.rows" :key="r.account.id" class="ab" :data-account="r.account.records[0]?.id">
            <p class="ab-who"><b>{{ r.account.identity }}</b><span v-if="r.floor !== null">floor {{ pct(r.floor) }} % · <button type="button" class="link-btn quiet" :aria-label="`Change the floor of ${r.account.vendor} · ${r.account.identity} in its account panel`" @click="ctx.show('account', r.account.id, $event)">change</button></span></p>
            <QuotaBar size="md" :bar="r.q.bar" :label="r.q.label" />
            <p class="ab-cap" :class="r.caption.tone" data-capacity-caption><AppIcon v-if="r.caption.icon" :name="r.caption.icon" :size="12" />{{ r.caption.text }}</p>
          </div>
        </div>
        <p v-if="h.boost" class="boost"><AppIcon name="bolt" :size="12" />Boost today: up to {{ pct(h.boost.limit_used_pct) }} % used · ends at midnight · set on the Agents dial</p>
      </div>
      <p v-if="!accounts.length && capacity.loaded" class="empty">No vendor accounts are connected yet. Each one gets a row here once it is.</p>
      <div class="cap-foot">
        <p class="cap-line"><AppIcon name="route" :size="14" /><span>At the limit, new work <b>follows Settings › Models</b>: the next model in its order runs, or the work waits.{{ waiters }}</span><RouterLink class="link-btn quiet go" to="/settings/models">Models<AppIcon name="chevron-right" :size="12" /></RouterLink></p>
        <p class="cap-line"><AppIcon name="bolt" :size="14" /><span>Need more today? <b>Boost today</b> on the Agents dial changes today only and ends at midnight.</span><RouterLink class="link-btn quiet go" to="/agents">Agents<AppIcon name="chevron-right" :size="12" /></RouterLink></p>
      </div>
    </section>
    <PacingSettings ref="pacing" :manage="manage" :owner-key="owner" />
    <FloatingPanel v-if="picker && pickerPace" :anchor="picker.anchor" :width="360" align="end" :label="`Daily limit for ${name(picker.harness)}`" cycle field-escape @close="closePicker">
      <div class="pp-pop" role="radiogroup" aria-labelledby="pp-title" @keydown="pickerKeys">
        <h3 id="pp-title">Daily limit for {{ name(picker.harness) }}</h3>
        <button type="button" class="ch-o" role="radio" :aria-checked="pickerPace.mode === 'pace' && pickerPace.points_per_day === null" :data-autofocus="pickerPace.mode === 'pace' && pickerPace.points_per_day === null || undefined" :data-setpace="`${picker.harness}:default`" @click="choose('default')"><span class="r" /><span class="t">Follow the default</span><span class="d">+{{ pickerDefault }} percentage points a day</span></button>
        <div class="ch-o own" :class="{ on: pickerPace.mode === 'pace' && pickerPace.points_per_day !== null }">
          <button type="button" class="ch-b" role="radio" :aria-checked="pickerPace.mode === 'pace' && pickerPace.points_per_day !== null" :data-autofocus="pickerPace.mode === 'pace' && pickerPace.points_per_day !== null || undefined" :data-setpace="`${picker.harness}:own`" @click="choose('own')"><span class="r" /><span class="t">Its own pace</span></button>
          <span class="d">+<input id="own-pace" ref="ownField" v-model="ownDraft" class="field n" inputmode="numeric" maxlength="2" autocomplete="off" aria-label="Own pace, percentage points a day" aria-describedby="own-pace-note" @input="ownNote = ''" @keydown.enter.prevent="choose('own')">percentage points a day</span>
        </div>
        <button type="button" class="ch-o" role="radio" :aria-checked="pickerPace.mode === 'everything'" :data-autofocus="pickerPace.mode === 'everything' || undefined" :data-setpace="`${picker.harness}:everything`" @click="choose('everything')"><span class="r" /><span class="t">Use everything before the reset</span><span class="d">No daily limit. The floor and what’s kept for you still hold.</span></button>
        <p id="own-pace-note" class="pp-note" aria-live="polite">{{ ownNote }}</p>
        <p class="pp-foot">Applies to {{ pickerAccounts > 1 ? `all ${pickerAccounts} ${name(picker.harness)} accounts, in their order` : `the ${name(picker.harness)} account` }}. The Agents dial edits this same value; Boost today, also on the dial, changes today only.</p>
      </div>
    </FloatingPanel>
  </AccountsPanel>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
<style scoped>
/* How much agents may use each day (design AEON-1149): the rule, the default pace, a row per harness. */
.daily { container: daily / inline-size; }
.rule { display: flex; align-items: flex-start; gap: 10px; padding: 12px 14px; border-radius: 12px; background: var(--surface-sunken); font-size: 13px; line-height: 1.55; color: var(--ink-2); }
.rule > svg { flex: none; margin-top: 3px; color: var(--ink-3); }
.rule p { margin: 0; }
.rule b { color: var(--ink); font-weight: 600; }
.rule :deep(.legend) { margin-top: 8px; }
.dl-def { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; column-gap: 20px; min-height: 72px; margin: 16px 0 6px; padding: 12px 16px; border-radius: 14px; background: var(--surface-raised); box-shadow: 0 0 0 1px var(--line-2), var(--shadow-btn); }
.dl-def .t { display: grid; gap: 2px; min-width: 0; }
.dl-def .t b { font-size: 14px; font-weight: 650; color: var(--ink); }
.dl-def .t small { font-size: 12.5px; line-height: 1.45; color: var(--ink-3); }
.pp { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--ink-2); white-space: nowrap; }
.pp .field.n { box-sizing: border-box; width: 56px; height: 36px; padding: 0 6px; text-align: center; font: 600 14px/1 var(--font); font-variant-numeric: tabular-nums; }
.pp .plus { font: 600 14px/1 var(--font); color: var(--ink); }
.field.n:disabled { opacity: .6; }
.hz { padding: 14px 0 16px; border-bottom: 1px solid var(--line); scroll-margin-top: 16px; }
.hz:focus { outline: none; }
.hz:focus-visible { border-radius: 10px; box-shadow: var(--focus-ring); }
.hz-h { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; align-items: center; column-gap: 12px; min-height: 40px; }
.hz-n { display: grid; min-width: 0; }
.hz-n b { font-size: 14px; font-weight: 650; color: var(--ink); }
.hz-s { font-size: 12px; font-weight: 500; color: var(--ink-3); }
.pick-s { display: inline-flex; align-items: center; gap: 8px; height: 36px; padding: 0 10px 0 12px; border: 0; border-radius: 10px; background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font-size: 13px; font-weight: 600; white-space: nowrap; }
@media (hover: hover) { .pick-s:not(.ro):hover { box-shadow: var(--field-inset), 0 0 0 1px var(--line-2); } }
.pick-s[aria-expanded="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--teal), 0 0 0 4px var(--row-selected); }
.pick-s:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.pick-s[aria-busy="true"] { cursor: progress; }
.pick-s .pe { font-weight: 500; color: var(--ink-3); }
.pick-s svg { flex: none; color: var(--ink-3); }
.pick-s.ro { background: transparent; box-shadow: inset 0 0 0 1px var(--line); cursor: default; }
/* The shown label and the widest one share a cell: the picker never changes width. */
.pick-w { display: inline-grid; justify-items: end; }
.pick-w > .pick-t { grid-area: 1 / 1; }
.pick-t.sizer { visibility: hidden; }
.hz-acc { display: grid; gap: 4px; margin: 10px 0 0 36px; }
.ab { display: grid; padding: 6px 0 8px; }
.ab-who { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 10px; min-width: 0; margin: 0; font-size: 12px; color: var(--ink-3); }
.ab-who b { font-size: 12.5px; font-weight: 600; color: var(--ink); overflow-wrap: anywhere; }
.ab .qb { margin-top: 20px; }
.ab-cap { display: flex; align-items: center; gap: 6px; min-width: 0; margin: 7px 0 0; font-size: 12px; color: var(--ink-2); }
.ab-cap svg { flex: none; }
.ab-cap.warn { color: var(--warn-ink); font-weight: 600; }
.hz-none { margin: 4px 0 0 36px; font-size: 12.5px; color: var(--ink-3); }
.boost { display: inline-flex; align-items: center; gap: 6px; margin: 8px 0 0 36px; padding: 4px 10px; border-radius: 999px; background: var(--row-selected); font-size: 12px; color: var(--ink-2); }
.boost svg { color: var(--teal-ink); }
.cap-foot { display: grid; gap: 8px; margin-top: 4px; padding-top: 14px; }
.cap-line { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 13px; color: var(--ink-2); }
.cap-line > svg { flex: none; color: var(--ink-3); }
.cap-line b { color: var(--ink); font-weight: 600; }
.link-btn { border: 0; padding: 0; background: none; color: var(--teal-ink); font: inherit; font-size: 12.5px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link-btn.quiet { color: var(--ink-2); font-weight: 500; text-decoration-color: var(--line-2); }
@media (hover: hover) { .link-btn.quiet:hover { color: var(--teal-ink); text-decoration-color: currentColor; } }
.link-btn:focus-visible { outline: none; border-radius: 4px; box-shadow: var(--focus-ring); }
.go { display: inline-flex; align-items: center; gap: 2px; margin-left: auto; white-space: nowrap; }
.daily .link-btn { display: inline-flex; align-items: center; justify-content: center; min-height: 24px; min-width: 24px; padding: 0 3px; margin: -2px -3px; }
.daily .go { margin-left: auto; }
@container daily (max-width: 560px) {
  .dl-def { grid-template-columns: minmax(0, 1fr); row-gap: 10px; }
  .hz-h { grid-template-columns: 24px minmax(0, 1fr); row-gap: 10px; }
  .hz-h > .pick-s { grid-column: 2; justify-self: start; }
  .pick-w { justify-items: start; }
  .hz-acc, .hz-none, .boost { margin-left: 0; }
  .cap-line { align-items: flex-start; flex-wrap: wrap; }
  .cap-line > svg { margin-top: 3px; }
  .cap-line > span { flex: 1 1 200px; }
}
@media (pointer: coarse) {
  .daily .link-btn { min-height: 44px; min-width: 44px; padding: 0 8px; margin: -12px -8px; }
  .pick-s { min-height: 44px; }
  .pp .field.n { height: 44px; }
}

/* The picker: three choices, the sentence of each in place; the note line is reserved. */
.pp-pop { display: grid; }
.pp-pop h3 { margin: 0; padding: 8px 10px 4px; font-size: 13px; font-weight: 650; color: var(--ink); }
.ch-o, .ch-b { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 2px 10px; width: 100%; padding: 10px; border: 0; border-radius: 9px; background: transparent; color: var(--ink); text-align: left; }
.ch-o.own { padding: 0 0 10px; }
.ch-o.own .ch-b { padding-bottom: 2px; }
.ch-o.own > .d { padding: 0 10px; margin-left: 28px; }
@media (hover: hover) { button.ch-o:hover, .ch-b:hover { background: var(--row-hover); } }
.ch-o[aria-checked="true"], .ch-o.own.on { background: var(--row-selected); }
.ch-o:focus-visible, .ch-b:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.r { width: 14px; height: 14px; margin-top: 3px; border-radius: 50%; box-shadow: inset 0 0 0 1.5px var(--line-2); }
[aria-checked="true"] > .r { box-shadow: inset 0 0 0 4px var(--teal); }
.t { font-size: 13.5px; font-weight: 600; }
.d { grid-column: 2; display: flex; flex-wrap: wrap; align-items: center; gap: 0 4px; font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.d .field.n { box-sizing: border-box; width: 52px; height: 30px; margin: 0 4px; padding: 0 4px; text-align: center; font-weight: 600; font-variant-numeric: tabular-nums; }
.pp-note { min-height: 18px; margin: 0; padding: 0 10px; font-size: 12px; color: var(--danger); }
.pp-foot { margin: 4px 4px 2px; padding: 8px 6px 4px; border-top: 1px solid var(--line); font-size: 12px; line-height: 1.45; color: var(--ink-3); }
@media (pointer: coarse) { .ch-o, .ch-b { min-height: 44px; } .d .field.n { height: 44px; } }
</style>
