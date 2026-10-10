<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getNode } from '../../lib/api'
import { listEntries, listPeriods, PERIOD_HISTORY_START, type CostRate, type TimeEntry, type TimePeriod } from '../../lib/business'
import { plural } from '../../lib/work'
import { addDays, dayKey, isoWeek, periodLabel, startOfWeek, weekDays, weekLabel, WEEKDAYS } from '../../lib/week'
import { formatClock, formatSpan } from '../../components/business/duration'
import { formatAmount } from '../../components/business/money'
import { useBusiness } from '../../stores/business'
import { useCustomers } from '../../stores/customers'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AppIcon from '../../components/business/BizIcon.vue'
import BusinessPage from '../../components/business/BusinessPage.vue'
import { settingsLink } from '../../lib/settings'
import SetupCard from '../../components/business/SetupCard.vue'

// A calm desk for the business side: your week of hours and where it went, the
// periods waiting for an admin's approval, and the rates in force.
const business = useBusiness()
const customers = useCustomers()
const projects = useProjects()
const session = useSession()
const router = useRouter()
const now = ref(Date.now())
const managing = ref(false)
const me = computed(() => session.identity?.principal.id ?? '')

// ---------- Your week ----------
const week = startOfWeek(new Date())
const days = weekDays(week)
const today = dayKey(new Date())
const entries = ref<TimeEntry[]>([])
const periods = ref<TimePeriod[]>([])
const periodSeconds = ref(new Map<string, number>())
const nodes = ref(new Map<string, { key: string; title: string }>())
const hoursLoaded = ref(false)
const hoursError = ref('')
async function loadHours() {
  hoursError.value = ''
  try {
    const start = week.getTime(), end = addDays(week, 7).getTime()
    const mine = await listPeriods(me.value, { since: new Date(start).toISOString(), until: new Date(end).toISOString() })
    const overlapping = mine.filter(p => Date.parse(p.starts_at) < end && Date.parse(p.ends_at) > start)
    const found = (await Promise.all(overlapping.map(p => listEntries({ period_id: p.id })))).flat()
    entries.value = found.filter(e => Date.parse(e.started_at) >= start && Date.parse(e.started_at) < end)
    const ids = [...new Set(entries.value.map(e => e.node_id))]
    const resolved = await Promise.all(ids.map(id => getNode(id).then(n => [id, { key: n.key, title: n.title }] as const).catch(() => [id, { key: '—', title: 'Deleted node' }] as const)))
    nodes.value = new Map(resolved)
    if (business.admin) {
      periods.value = await listPeriods(undefined, { since: PERIOD_HISTORY_START })
      const waiting = periods.value.filter(p => p.state === 'open' && Date.parse(p.ends_at) <= now.value)
      const sums = await Promise.all(waiting.map(async p => [p.id, (await listEntries({ period_id: p.id })).reduce((s, e) => s + e.duration_seconds, 0)] as const))
      periodSeconds.value = new Map(sums)
    }
    hoursLoaded.value = true
  } catch (e) { hoursError.value = e instanceof Error ? e.message : 'Hours could not be loaded.' }
}
const perDay = computed(() => days.map(day => entries.value.filter(e => dayKey(new Date(e.started_at)) === dayKey(day)).reduce((sum, e) => sum + e.duration_seconds, 0)))
const weekTotal = computed(() => perDay.value.reduce((a, b) => a + b, 0))
const maxDay = computed(() => Math.max(8 * 3600, ...perDay.value))
const byTicket = computed(() => {
  const map = new Map<string, number>()
  for (const e of entries.value) map.set(e.node_id, (map.get(e.node_id) ?? 0) + e.duration_seconds)
  return [...map.entries()].sort((a, b) => b[1] - a[1]).slice(0, 5).map(([id, seconds]) => ({ id, seconds }))
})
const topSeconds = computed(() => byTicket.value[0]?.seconds ?? 1)
function ticketHref(nodeId: string) {
  const key = nodes.value.get(nodeId)?.key ?? ''
  const project = projects.byRouteKey(key.split('-')[0] ?? '')
  return project ? `/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(key)}` : ''
}
const waiting = computed(() => periods.value.filter(p => p.state === 'open' && Date.parse(p.ends_at) <= now.value).sort((a, b) => a.ends_at.localeCompare(b.ends_at)))

// ---------- Rates in force ----------
const todayUTC = new Date().toISOString().slice(0, 10)
const inForce = (rate: CostRate) => rate.effective_from <= todayUTC && (!rate.effective_until || rate.effective_until > todayUTC)
const rateRows = computed(() => business.costUnits
  .filter(unit => !['cancelled', 'archived', 'done'].includes(unit.node.state))
  .map(unit => ({ unit, rates: unit.rates.filter(inForce).sort((a, b) => ['hour', 'day', 'item'].indexOf(a.unit) - ['hour', 'day', 'item'].indexOf(b.unit)) }))
  .sort((a, b) => a.unit.node.title.localeCompare(b.unit.node.title)))
const ratesInForce = computed(() => rateRows.value.reduce((n, row) => n + row.rates.length, 0))
const money = (rate: CostRate) => { try { return formatAmount(rate.bill_amount, rate.currency) } catch { return rate.bill_amount } }

const settled = ref({ costs: false, crm: false })
// The summary appears once every part it counts is in, in one piece rather than
// growing a clause at a time.
const summary = computed(() => {
  if ((business.open.hours && !hoursLoaded.value && !hoursError.value) || (business.open.costs && !settled.value.costs) || (business.open.crm && !settled.value.crm)) return ''
  const parts: string[] = []
  if (business.open.hours && hoursLoaded.value) parts.push(weekTotal.value ? `${formatSpan(weekTotal.value)} logged this week` : 'Nothing logged this week')
  if (business.open.hours && business.admin && hoursLoaded.value) parts.push(waiting.value.length ? `${plural(waiting.value.length, 'period')} to approve` : 'nothing to approve')
  if (business.open.costs && business.costUnitsLoaded) parts.push(`${plural(ratesInForce.value, 'rate')} in force`)
  if (business.open.crm && customers.items) parts.push(plural(customers.items.filter(c => !c.archived).length, 'customer'))
  return parts.join(' · ')
})
async function load() {
  await business.loadPlugins()
  if (business.open.costs) void business.loadCostUnits(true).catch(() => {}).finally(() => { settled.value = { ...settled.value, costs: true } })
  if (business.open.hours) { void loadHours(); void projects.load() }
  if (business.open.crm) void customers.load().finally(() => { settled.value = { ...settled.value, crm: true } })
  if (business.anyOpen && business.staff) void business.loadPrincipals()
}
watch(() => [business.open.costs, business.open.hours, business.open.crm], (value, before) => { if (before && value.join() !== before.join()) void load() })
let clock: ReturnType<typeof setInterval> | undefined
onMounted(() => { void load(); clock = setInterval(() => { now.value = Date.now() }, 60_000) })
onBeforeUnmount(() => clearInterval(clock))
const offered = computed(() => business.anyOpen)
</script>

<template>
  <BusinessPage title="Business">
    <template #summary>
      <span v-if="summary">{{ summary }}</span>
      <span v-else-if="business.plugins && !offered">{{ business.admin ? 'Not set up yet' : 'Not enabled for this workspace' }}</span>
      <span v-else class="summary-loading"><span class="skeleton summary-skeleton" /><span class="skeleton summary-skeleton line2" /></span>
    </template>
    <template v-if="business.admin && offered" #actions>
      <button type="button" class="btn sm" :aria-pressed="managing" @click="managing = !managing"><AppIcon name="sliders" :size="14" />Manage parts</button>
      <RouterLink class="btn sm ghost" :to="settingsLink('business')">Business settings<AppIcon name="arrow" :size="13" /></RouterLink>
    </template>

    <div v-if="!offered" class="intro">
      <SetupCard v-if="business.admin" variant="intro" />
      <div v-else class="closed glass-card">
        <span class="state-icon"><AppIcon name="briefcase" :size="18" /></span>
        <h2>Business is not set up for this workspace</h2>
        <p>Customers, quotes, hours and rates appear here once a workspace admin enables them.</p>
      </div>
    </div>

    <div v-else class="layout">
      <div class="main-col">
        <SetupCard v-if="managing || (business.admin && !business.allOpen)" :variant="managing ? 'manage' : 'intro'" @done="managing = false" />

        <section v-if="business.open.hours" class="card glass-card" aria-labelledby="week-title">
          <header class="card-head">
            <h2 id="week-title">Your week</h2>
            <span class="sub">Week {{ isoWeek(week) }} · {{ weekLabel(week) }}</span>
            <span class="spacer" />
            <RouterLink class="more-link" to="/business/hours">Open week<AppIcon name="arrow" :size="13" /></RouterLink>
          </header>
          <p v-if="hoursError" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />{{ hoursError }}</p>
          <div class="week-body">
            <div class="week-left">
              <p class="week-total"><b class="mono">{{ hoursLoaded ? formatSpan(weekTotal) : '—' }}</b><span>{{ weekTotal ? 'logged' : 'nothing logged yet' }}</span></p>
              <div class="bars" role="list" aria-label="Hours per day">
                <div v-for="(day, i) in days" :key="i" class="bar-col" :class="{ today: dayKey(day) === today }" role="listitem" :aria-label="`${WEEKDAYS[i]} ${day.getDate()}: ${perDay[i] ? formatSpan(perDay[i]) : 'nothing logged'}`" :data-tip="`${WEEKDAYS[i]} ${day.getDate()}: ${perDay[i] ? formatSpan(perDay[i]) : 'nothing logged'}`">
                  <span class="bar-track"><i :style="{ transform: `scaleY(${perDay[i] / maxDay})` }" /></span>
                  <span class="bar-day">{{ WEEKDAYS[i].slice(0, 2) }}</span>
                </div>
              </div>
            </div>
            <div class="week-right">
              <p class="eyebrow">Where the time went</p>
              <div v-if="!hoursLoaded" class="rows-skeleton" aria-hidden="true"><span v-for="i in 3" :key="i" class="skeleton" /></div>
              <p v-else-if="!byTicket.length" class="quiet">No time on tickets yet this week.</p>
              <ul v-else class="tickets" aria-label="Time per ticket">
                <li v-for="row in byTicket" :key="row.id" class="ticket-row">
                  <RouterLink v-if="ticketHref(row.id)" class="ticket-chip" :to="ticketHref(row.id)">{{ nodes.get(row.id)?.key }}</RouterLink>
                  <span v-else class="ticket-chip plain">{{ nodes.get(row.id)?.key }}</span>
                  <span class="t-title">{{ nodes.get(row.id)?.title }}</span>
                  <span class="t-bar" aria-hidden="true"><i :style="{ width: `${Math.round(row.seconds / topSeconds * 100)}%` }" /></span>
                  <span class="t-time mono">{{ formatClock(row.seconds) }}</span>
                </li>
              </ul>
            </div>
          </div>
          <footer class="card-foot">
            <span class="foot-note">Log with the ticket, the cost unit and a duration like 1h30.</span>
            <button type="button" class="btn sm" @click="router.push('/business/hours?log=1')"><AppIcon name="plus" :size="13" />Log time</button>
          </footer>
        </section>

        <section v-if="business.open.hours && business.admin" class="card glass-card" aria-labelledby="waiting-title">
          <header class="card-head">
            <h2 id="waiting-title">Waiting for approval</h2>
            <span v-if="hoursLoaded" class="count mono">{{ waiting.length }}</span>
            <span class="spacer" />
            <RouterLink class="more-link" to="/business/hours?view=approvals">Approvals<AppIcon name="arrow" :size="13" /></RouterLink>
          </header>
          <div v-if="!hoursLoaded" class="rows-skeleton" aria-hidden="true"><span v-for="i in 2" :key="i" class="skeleton" /></div>
          <p v-else-if="!waiting.length" class="empty">Nothing waits for approval. A period appears here once its week has ended.</p>
          <ul v-else class="period-rows" aria-label="Periods waiting for approval">
            <li v-for="p in waiting.slice(0, 6)" :key="p.id">
              <RouterLink class="period-row" :to="`/business/hours?view=approvals&period=${p.id}`">
                <span class="p-who"><AppIcon :name="business.principals.find(x => x.id === p.principal_id)?.kind === 'agent' ? 'agent' : 'user'" :size="13" />{{ business.nameOf(p.principal_id) }}</span>
                <span class="p-when">{{ periodLabel(p.starts_at, p.ends_at) }}</span>
                <span class="p-time mono">{{ formatSpan(periodSeconds.get(p.id) ?? 0) }}</span>
                <AppIcon name="chevron-right" :size="13" class="go" />
              </RouterLink>
            </li>
          </ul>
        </section>
      </div>

      <aside v-if="business.open.costs" class="side-col" aria-label="Rates">
        <section class="card glass-card" aria-labelledby="rates-title">
          <header class="card-head">
            <h2 id="rates-title">Rates in force</h2>
            <span class="spacer" />
            <RouterLink class="more-link" to="/business/rates">All rates<AppIcon name="arrow" :size="13" /></RouterLink>
          </header>
          <div v-if="!business.costUnitsLoaded" class="rows-skeleton" aria-hidden="true"><span v-for="i in 3" :key="i" class="skeleton" /></div>
          <p v-else-if="!rateRows.length" class="empty">No cost unit yet. {{ business.admin ? 'Add one under Rates to price hours.' : 'A workspace admin adds them under Rates.' }}</p>
          <ul v-else class="rate-rows" aria-label="Cost units and rates in force">
            <li v-for="row in rateRows" :key="row.unit.node.id" class="rate-row">
              <span class="r-mark" aria-hidden="true"><AppIcon name="tag" :size="12" /></span>
              <span class="r-name">{{ row.unit.node.title }}</span>
              <span v-if="row.rates.length" class="r-rates">
                <span v-for="rate in row.rates" :key="rate.id" class="r-rate"><b class="mono">{{ money(rate) }}</b> <span class="r-cur">{{ rate.currency }}</span>/{{ rate.unit === 'hour' ? 'h' : rate.unit }}</span>
              </span>
              <span v-else class="r-none">No rate in force</span>
            </li>
          </ul>
        </section>
      </aside>
    </div>
  </BusinessPage>
</template>

<style scoped>
.summary-skeleton { display: inline-block; width: 240px; }
/* Phones wrap the summary to two lines; the loading line holds both. */
.summary-loading { display: inline-block; }
.summary-skeleton.line2 { display: none; }
@media (max-width: 600px) {
  .summary-loading { display: grid; gap: 10px; padding: 5px 0; }
  .summary-skeleton, .summary-skeleton.line2 { display: block; }
  .summary-skeleton.line2 { width: 60%; }
}
.intro { max-width: 760px; }
.closed { display: grid; justify-items: center; gap: 8px; padding: 44px 28px; text-align: center; }
.closed h2 { font-size: 17px; }
.closed p { font-size: 13.5px; max-width: 46ch; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.layout { display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 20px; align-items: start; }
.main-col, .side-col { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; min-width: 0; }
.side-col { position: sticky; top: 16px; }
.card { overflow: clip; }
.card-head { display: flex; align-items: center; gap: 10px; min-height: 50px; padding: 12px 18px 10px; }
.card-head h2 { font-size: 15px; font-weight: 650; }
.sub { font-size: 12.5px; color: var(--ink-3); }
.count { display: inline-grid; place-items: center; min-width: 22px; height: 20px; padding: 0 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 11px; color: var(--ink-2); }
.spacer { flex: 1; }
.more-link { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px; margin-right: -10px; border-radius: 999px; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; text-decoration: none; }
.more-link:hover { background: var(--row-hover); }
.more-link:focus-visible { box-shadow: var(--focus-ring); }
.inline-error { display: flex; align-items: center; gap: 8px; margin: 0 18px 12px; padding: 8px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.week-body { display: grid; grid-template-columns: minmax(220px, 300px) minmax(0, 1fr); gap: 28px; padding: 4px 18px 16px; }
.week-total { display: flex; align-items: baseline; gap: 10px; margin-bottom: 12px; font-size: 12.5px; color: var(--ink-3); }
.week-total b { font-size: 28px; font-weight: 500; color: var(--ink); letter-spacing: -.01em; }
.bars { display: grid; grid-template-columns: repeat(7, 1fr); gap: 8px; height: 110px; }
.bar-col { display: grid; grid-template-rows: minmax(0, 1fr) auto; justify-items: center; gap: 6px; }
.bar-track { position: relative; width: 100%; max-width: 26px; height: 100%; border-radius: 7px; background: var(--track); overflow: hidden; }
.bar-track i { position: absolute; inset: 0; transform-origin: bottom; border-radius: 7px; background: linear-gradient(0deg, var(--primary), var(--primary-line)); box-shadow: none; }
.bar-day { font: 500 10.5px/1 var(--mono); letter-spacing: .06em; color: var(--ink-3); text-transform: uppercase; font-variant-ligatures: none; }
.today .bar-day { color: var(--teal-ink); font-weight: 700; }
.today .bar-track { box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.week-right .eyebrow { margin: 6px 0 10px; }
.quiet { font-size: 13px; color: var(--ink-3); }
.tickets { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.ticket-row { display: grid; grid-template-columns: max-content minmax(0, 1fr) 90px 44px; align-items: center; gap: 10px; min-height: 32px; font-size: 13px; }
@media (max-width: 600px) { .ticket-row { min-height: 44px; } }
.ticket-chip { display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11px/1 var(--mono); text-decoration: none; font-variant-ligatures: none; white-space: nowrap; }
a.ticket-chip:hover { text-decoration: underline; }
.ticket-chip:focus-visible { box-shadow: var(--focus-ring); }
.ticket-chip.plain { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.t-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 600px) { .t-title { white-space: normal; overflow: visible; overflow-wrap: anywhere; } }
.t-bar { height: 5px; border-radius: 999px; background: var(--track); overflow: hidden; }
.t-bar i { display: block; height: 100%; border-radius: 999px; background: linear-gradient(90deg, var(--primary), var(--primary-line)); }
.t-time { text-align: right; font-size: 12.5px; }
.mono { font-family: var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.card-foot { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 10px 18px 12px; border-top: 1px solid var(--line); }
.foot-note { font-size: 12px; color: var(--ink-3); }
.empty { padding: 14px 18px 18px; border-top: 1px solid var(--line); font-size: 13.5px; }
.rows-skeleton { display: grid; gap: 14px; padding: 18px; }
.rows-skeleton .skeleton { height: 12px; }
.rows-skeleton .skeleton:nth-child(2n) { width: 70%; }
.period-rows, .rate-rows { margin: 0; padding: 0 6px 8px; list-style: none; border-top: 1px solid var(--line); }
.period-row { display: grid; grid-template-columns: minmax(0, 1fr) auto 80px 14px; align-items: center; gap: 14px; min-height: 46px; margin-top: 4px; padding: 6px 12px; border-radius: 10px; color: var(--ink); text-decoration: none; font-size: 13px; }
@media (hover: hover) { .period-row:hover { background: var(--row-hover); } }
.period-row:focus-visible { background: var(--row-selected); box-shadow: var(--focus-ring); }
.p-who { display: inline-flex; align-items: center; gap: 8px; min-width: 0; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.p-who svg, .go { color: var(--ink-3); flex-shrink: 0; }
.p-when { color: var(--ink-2); font-size: 12.5px; }
.p-time { text-align: right; }
.rate-row { display: grid; grid-template-columns: 26px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 44px; padding: 4px 12px; border-bottom: 1px solid var(--line); }
.rate-row:last-child { border-bottom: 0; }
.r-mark { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 8px; background: var(--code-bg); color: var(--ink-2); }
.r-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; font-weight: 600; }
.r-rates { display: grid; justify-items: end; gap: 1px; font-size: 12px; color: var(--ink-3); }
.r-rate b { font-size: 12.5px; font-weight: 600; color: var(--ink); }
.r-cur { font: 500 10px/1 var(--mono); letter-spacing: .06em; }
.r-none { font-size: 12px; color: var(--warn-ink); }
@media (max-width: 1180px) { .week-body { grid-template-columns: minmax(0, 1fr); gap: 16px; } }
@media (max-width: 1040px) { .layout { grid-template-columns: minmax(0, 1fr); } .side-col { position: static; } }
@media (max-width: 720px) {
  .card-head { flex-wrap: wrap; padding: 12px 14px 8px; row-gap: 2px; }
  .card-head .sub { order: 3; flex-basis: 100%; }
  .week-body { padding: 4px 14px 14px; }
  .ticket-row { grid-template-columns: max-content minmax(0, 1fr) 44px; }
  .t-bar { display: none; }
  .card-foot { flex-direction: column; align-items: stretch; }
  .card-foot .btn { height: 44px; }
  .period-row { grid-template-columns: minmax(0, 1fr) auto 14px; grid-template-areas: "who time go" "when when go"; row-gap: 2px; }
  .p-who { grid-area: who; } .p-when { grid-area: when; } .p-time { grid-area: time; } .go { grid-area: go; }
}
</style>
