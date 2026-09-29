<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Agents → Usage (AEON-301): one page for capacity and usage. "Now" is the
// capacity band (the Agents desk's pools, read-only); below it, what agents got
// done in the range, what it took, and where the waste is. A figure nobody
// reported is left out or replaced by the one sentence that says so.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import TicketPeekLink from '../components/TicketPeekLink.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import CapacityBand from '../components/usage/CapacityBand.vue'
import DonePerDay from '../components/usage/DonePerDay.vue'
import StatusIcon from '../components/work/StatusIcon.vue'
import { reworkDetail, reworkPercent } from '../lib/deliveryRating'
import { loadUsageDashboard } from '../lib/usageDashboard'
import { compactCount, formatCount, formatWhen, rangeBounds, type UsageDashboard, type WasteItem, type WorkGroup, type WorkTicket } from '../lib/usageFormat'
import { usePoller } from '../lib/usePolledData'
import { breakdown, coverage, duration, harnessName, plural, tiles, wasteWords, type Breakdown } from '../lib/usageWork'
import { statusMeta } from '../lib/work'
import { useAgents } from '../stores/agents'
import { useCapacity } from '../stores/capacity'
import { useProjects } from '../stores/projects'
import { useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const projects = useProjects()
const session = useSession()
const agents = useAgents()
const capacity = useCapacity()
const dashboard = ref<UsageDashboard | null>(null)
const loadedDays = ref<number | null>(null)
const loadedProject = ref<string | null>(null)
const loading = ref(true)
const error = ref('')
const ranges = [7, 30, 90] as const

const days = computed(() => ranges.find(value => String(value) === route.query.days) ?? 30)
const projectId = computed(() => typeof route.query.project === 'string' ? route.query.project : '')
const openProjects = computed(() => projects.projects.filter(project => !project.archived))
// Figures stay visible only for the selection they were loaded with.
const visible = computed(() => dashboard.value && loadedDays.value === days.value && loadedProject.value === projectId.value ? dashboard.value : null)
const work = computed(() => visible.value?.work ?? null)
const showCapacity = computed(() => capacity.state !== 'forbidden' && agents.accountsState !== 'forbidden')

function setQuery(patch: { days?: number; project?: string }) {
  const next: Record<string, string> = {}
  const chosen = patch.days ?? days.value
  if (chosen !== 30) next.days = String(chosen)
  const project = patch.project !== undefined ? patch.project : projectId.value
  if (project) next.project = project
  void router.replace({ path: '/agents/usage', query: next })
}

// A slower read for an earlier project or range must not paint, fail, or
// finish over the selection the user has now. Leaving the page cancels it.
let generation = 0
let controller: AbortController | null = null
function stale(request: number, signal: AbortSignal, cause?: unknown) {
  return request !== generation || signal.aborted || (cause instanceof Error && cause.name === 'AbortError')
}
async function load() {
  const request = ++generation
  controller?.abort()
  const current = controller = new AbortController()
  const requestedDays = days.value
  const requestedProject = projectId.value
  loading.value = true
  error.value = ''
  const bounds = rangeBounds(requestedDays, new Date())
  try {
    const next = await loadUsageDashboard({ ...bounds, project: requestedProject || undefined }, current.signal)
    if (stale(request, current.signal)) return
    dashboard.value = next
    loadedDays.value = requestedDays
    loadedProject.value = requestedProject
  } catch (cause) {
    if (stale(request, current.signal, cause)) return
    error.value = 'Usage could not be loaded right now.'
  } finally {
    if (!stale(request, current.signal)) loading.value = false
  }
}

// Capacity is "now": it refreshes on its own, like the Agents desk.
async function loadCapacity() {
  await Promise.all([agents.refreshAccounts(), capacity.load()])
  agents.now = Math.max(agents.now, Date.now())
}
const poller = usePoller(loadCapacity, 60_000, { invalidate: () => capacity.invalidate() })
onMounted(() => { void projects.load(); void loadCapacity(); poller.start() })
onBeforeUnmount(() => {
  poller.stop()
  generation++
  controller?.abort()
  controller = null
})
watch([days, projectId], () => { void load() }, { immediate: true })

const rangeTip = computed(() => visible.value ? `${formatWhen(visible.value.from)} – ${formatWhen(visible.value.to)}, UTC days` : undefined)
const summary = computed(() => visible.value ? tiles(visible.value) : [])

// ---------- Tickets ----------
const allTickets = ref(false)
const TICKETS_SHOWN = 8
const ticketRows = computed(() => (work.value?.tickets ?? []).slice(0, allTickets.value ? undefined : TICKETS_SHOWN))
const ticketsHidden = computed(() => (work.value?.tickets.length ?? 0) - ticketRows.value.length)
const ticketsBeyond = computed(() => work.value ? work.value.tickets_worked - work.value.tickets.length : 0)
const tokenTickets = computed(() => (work.value?.tickets ?? []).filter(t => t.tokens !== null).length)
function ticketHref(t: { project_id: string; project_key: string; key: string }) {
  const key = projects.byId(t.project_id)?.routeKey ?? t.project_key
  return `/p/${encodeURIComponent(key)}/${encodeURIComponent(t.key)}`
}
function ticketFacts(t: WorkTicket) {
  return [
    t.released ? 'Released' : statusMeta(t.state).label,
    plural(t.sessions, 'session'),
    t.tokens !== null ? `${compactCount(t.tokens)} tokens` : '',
  ].filter(Boolean).join(' · ')
}

// ---------- Waste ----------
const wasteRows = computed(() => work.value?.waste.rows ?? [])
const wasteBeyond = computed(() => work.value ? work.value.waste.total - wasteRows.value.length : 0)
const words = (item: WasteItem) => wasteWords(item, Date.parse(visible.value?.generated_at ?? '') || Date.now())
function wasteWho(item: WasteItem) {
  return [item.label, harnessName(item.harness), item.model].filter(Boolean).join(' · ')
}

// ---------- Breakdown ----------
const by = ref<Breakdown>('harness')
const tabs: { id: Breakdown; label: string }[] = [{ id: 'harness', label: 'Harness' }, { id: 'model', label: 'Model' }, { id: 'project', label: 'Project' }]
const groups = computed(() => work.value ? breakdown(work.value, by.value) : { rows: [], note: '' })
const showTokens = computed(() => groups.value.rows.some(g => g.tokens !== null))
function groupLabel(g: WorkGroup) { return by.value === 'harness' ? harnessName(g.label) : g.label }
function rework(g: WorkGroup) {
  if (by.value !== 'harness') return null
  const found = visible.value?.ratings?.by_harness?.find(r => r.label === g.key)
  const value = found ? reworkPercent(found.exceptions, found.deliveries) : ''
  return value ? { value, tip: reworkDetail(found!.exceptions, found!.deliveries) } : null
}
const showRework = computed(() => groups.value.rows.some(g => rework(g)))
</script>

<template>
  <section class="usage-page" :aria-busy="loading" aria-labelledby="usage-title">
    <header class="page-head">
      <div>
        <p class="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</p>
        <h1 id="usage-title">Usage</h1>
      </div>
      <RouterLink class="context-link" to="/agents"><AppIcon name="arrow-left" :size="13" />Agents</RouterLink>
    </header>

    <template v-if="showCapacity">
      <h2 class="band-label eyebrow">Now</h2>
      <CapacityBand />
    </template>

    <div class="range-head">
      <h2 class="band-label eyebrow" :data-tip="rangeTip">Last {{ days }} days</h2>
      <div class="toolbar">
        <div class="seg" role="group" aria-label="Time range">
          <button v-for="value in ranges" :key="value" type="button" :aria-pressed="days === value" @click="setQuery({ days: value })">{{ value }} days</button>
        </div>
        <label class="project">
          <span class="sr-only">Project</span>
          <select class="field" :value="projectId" @change="setQuery({ project: ($event.target as HTMLSelectElement).value })">
            <option value="">All projects</option>
            <option v-for="project in openProjects" :key="project.id" :value="project.id">{{ project.title }}</option>
          </select>
        </label>
      </div>
    </div>

    <p v-if="loading" class="sr-only" role="status">{{ visible ? 'Updating usage' : 'Loading usage' }}</p>
    <div v-if="error" class="notice" role="alert">
      <AppIcon name="alert" :size="16" />
      <p>{{ error }}</p>
      <button type="button" class="btn sm" @click="load()">Try again</button>
    </div>
    <div v-else-if="loading && !visible" class="tiles" aria-hidden="true">
      <div v-for="n in 4" :key="n" class="tile glass-card"><span class="skeleton big" /><span class="skeleton" /></div>
    </div>

    <template v-if="visible && work">
      <p v-if="visible.truncated" class="notice">
        <AppIcon name="alert" :size="16" />
        <span>This range has more sessions than one read holds, so the figures cover the first 5,000.</span>
      </p>
      <p v-if="!work.sessions" class="quiet-line">No agent sessions in the last {{ days }} days.</p>
      <template v-else>
        <ul class="tiles" :class="{ updating: loading }" aria-label="Summary">
          <li v-for="tile in summary" :key="tile.key" class="tile glass-card" :class="[`tile-${tile.key}`, { reason: tile.value === null }]" :data-tip="tile.tip">
            <p v-if="tile.value !== null" class="figure"><b class="num">{{ tile.value }}</b> <span>{{ tile.label }}</span></p>
            <p v-else class="figure reason-text">{{ tile.label }}</p>
            <p v-if="tile.detail" class="detail">{{ tile.detail }}</p>
            <p v-if="tile.note" class="note">{{ tile.note }}</p>
          </li>
        </ul>

        <section class="glass-card card" aria-labelledby="done-title">
          <header class="card-head">
            <h2 id="done-title" class="card-title">Done per day</h2>
            <span class="card-meta">tickets finished with an agent</span>
          </header>
          <p v-if="!work.done" class="empty">No ticket was finished in this range yet.</p>
          <DonePerDay v-else :days="work.days" />
        </section>

        <div class="pair">
          <section class="glass-card card tickets-card" aria-labelledby="tickets-title">
            <header class="card-head">
              <h2 id="tickets-title" class="card-title">Tickets</h2>
              <span class="card-meta">{{ formatCount(work.tickets_worked) }} worked · {{ formatCount(work.tickets_done) }} done<template v-if="tokenTickets"> · tokens for {{ formatCount(tokenTickets) }}</template></span>
            </header>
            <p v-if="!work.tickets.length" class="empty">No session in this range was bound to a ticket.</p>
            <ol v-else class="list">
              <li v-for="t in ticketRows" :key="t.id" class="ticket">
                <span class="state" :data-tip="t.released ? 'Released' : statusMeta(t.state).label"><StatusIcon :state="t.state" :size="14" /></span>
                <div class="main">
                  <TicketPeekLink class="ticket-link" :ticket-key="t.key" :href="ticketHref(t)" :tip="`${t.key} ${t.title}`"><span class="key">{{ t.key }}</span> {{ t.title }}</TicketPeekLink>
                  <p class="sub">{{ ticketFacts(t) }}</p>
                </div>
                <span class="time num" :data-tip="t.timed_sessions < t.sessions ? coverage(t.timed_sessions, t.sessions, 'timed for') : undefined">{{ t.timed_sessions ? duration(t.agent_seconds) : '' }}</span>
              </li>
            </ol>
            <button v-if="ticketsHidden > 0" type="button" class="more-btn" @click="allTickets = true">Show {{ formatCount(ticketsHidden) }} more</button>
            <p v-else-if="allTickets && ticketsBeyond > 0" class="foot">and {{ plural(ticketsBeyond, 'more ticket') }} with less agent time</p>
          </section>

          <section class="glass-card card waste-card" aria-labelledby="waste-title">
            <header class="card-head">
              <h2 id="waste-title" class="card-title">Waste</h2>
              <span v-if="wasteRows.length" class="card-meta">most agent time first</span>
            </header>
            <p v-if="!wasteRows.length" class="empty">No stuck, failed or empty runs in this range.</p>
            <ol v-else class="list">
              <li v-for="item in wasteRows" :key="item.kind + item.session_id + (item.ticket_id ?? '')" class="waste">
                <div class="main">
                  <p class="reason" :data-tip="wasteWho(item) || undefined"><b>{{ words(item).title }}</b> <span>{{ words(item).detail }}</span></p>
                  <p v-if="item.ticket_key" class="sub">
                    <TicketPeekLink class="inline-link" :ticket-key="item.ticket_key" :href="ticketHref({ project_id: item.project_id, project_key: item.project_key, key: item.ticket_key })" :tip="`${item.ticket_key} ${item.ticket_title ?? ''}`"><span class="key">{{ item.ticket_key }}</span> {{ item.ticket_title }}</TicketPeekLink>
                  </p>
                </div>
                <span class="time num">{{ item.agent_seconds !== null ? duration(item.agent_seconds) : '' }}</span>
                <RouterLink v-if="words(item).action === 'session'" class="open" :to="`/agents/${item.session_id}`" :aria-label="`Open session: ${words(item).title}${item.ticket_key ? `, ${item.ticket_key}` : ''}`" data-tip="Open session"><AppIcon name="chevron-right" :size="15" /></RouterLink>
                <TicketPeekLink v-else-if="item.ticket_key" class="open" :ticket-key="item.ticket_key" :href="ticketHref({ project_id: item.project_id, project_key: item.project_key, key: item.ticket_key })" tip="Open ticket"><AppIcon name="chevron-right" :size="15" /><span class="sr-only">Open ticket {{ item.ticket_key }}</span></TicketPeekLink>
              </li>
            </ol>
            <p v-if="wasteBeyond > 0" class="foot">and {{ formatCount(wasteBeyond) }} more</p>
          </section>
        </div>

        <section class="glass-card card breakdown" aria-labelledby="breakdown-title">
          <header class="card-head">
            <h2 id="breakdown-title" class="card-title">Breakdown</h2>
            <div class="seg" role="group" aria-label="Group by">
              <button v-for="tab in tabs" :key="tab.id" type="button" :aria-pressed="by === tab.id" @click="by = tab.id">{{ tab.label }}</button>
            </div>
          </header>
          <p v-if="!groups.rows.length" class="empty">No session in this range registered a model.</p>
          <table v-else class="grid">
            <caption class="sr-only">By {{ by }}</caption>
            <thead>
              <tr>
                <th>{{ tabs.find(t => t.id === by)?.label }}</th>
                <th class="r">Done</th>
                <th class="r opt">Sessions</th>
                <th class="r">Agent time</th>
                <th v-if="showTokens" class="r opt">Tokens</th>
                <th v-if="showRework" class="r opt">Rework</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in groups.rows" :key="g.key">
                <td class="name">
                  <span class="with-mark"><HarnessMark v-if="by === 'harness'" :harness="g.key" :size="14" /><span class="ellipsis" :title="groupLabel(g)">{{ groupLabel(g) }}</span></span>
                </td>
                <td class="r num" :class="{ zero: !g.done }">{{ formatCount(g.done) }}</td>
                <td class="r num opt">{{ formatCount(g.sessions) }}</td>
                <td class="r num" :data-tip="g.timed_sessions < g.sessions ? coverage(g.timed_sessions, g.sessions, 'timed for') : undefined">{{ g.timed_sessions ? duration(g.agent_seconds) : '' }}</td>
                <td v-if="showTokens" class="r num opt" :data-tip="g.tokens !== null ? coverage(g.usage_reported_sessions, g.sessions) : undefined">{{ g.tokens !== null ? compactCount(g.tokens) : '' }}</td>
                <td v-if="showRework" class="r num opt" :data-tip="rework(g)?.tip">{{ rework(g)?.value ?? '' }}</td>
              </tr>
            </tbody>
          </table>
          <p v-if="groups.note" class="foot">{{ groups.note }}</p>
        </section>
      </template>
    </template>
  </section>
</template>

<style scoped>
.usage-page { width: 100%; max-width: 1240px; margin: 0 auto; padding: 22px var(--gutter) 40px; }
.page-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; margin-bottom: 20px; }
.page-head h1 { margin-top: 6px; }
.context-link { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border-radius: 999px; color: var(--teal-ink); font-size: 13px; font-weight: 600; white-space: nowrap; }
@media (hover: hover) { .context-link:hover { background: var(--row-hover); } }
.context-link:focus-visible { box-shadow: var(--focus-ring); }
.band-label { margin: 0 0 8px 2px; }
.range-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px 16px; margin: 26px 0 12px; }
.range-head .band-label { margin: 0 0 0 2px; cursor: default; }
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 12px; }
.project .field { min-width: 12rem; height: 32px; }
.notice { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; padding: 12px 14px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); font-size: 13.5px; }
.notice svg { flex-shrink: 0; }
.notice p { flex: 1; }
.quiet-line { padding: 18px 2px; color: var(--ink-2); font-size: 14px; }

.tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin: 0 0 14px; padding: 0; list-style: none; transition: opacity .15s ease; }
.tiles.updating { opacity: .6; }
.tile { display: flex; flex-direction: column; gap: 4px; min-width: 0; min-height: 104px; padding: 16px 18px; }
.figure { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; color: var(--ink-2); font-size: 13.5px; }
.figure b { color: var(--ink); font: 650 28px/1.1 var(--font); letter-spacing: -.02em; }
.reason-text { color: var(--ink); font-size: 15px; font-weight: 600; line-height: 1.35; }
.detail { color: var(--ink-2); font-size: 13px; line-height: 1.45; text-wrap: pretty; }
.note { margin-top: auto; padding-top: 4px; color: var(--ink-3); font-size: 12px; }
.tile .skeleton { width: 70%; }
.tile .skeleton.big { width: 40%; height: 26px; margin-bottom: 8px; }
.num { font-variant-numeric: tabular-nums; }

.card { padding: 16px 18px; margin-bottom: 14px; min-width: 0; }
.card-head { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 12px; margin-bottom: 12px; }
.card-title { font: 600 15px/1.3 var(--font); letter-spacing: 0; }
.card-meta { color: var(--ink-3); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.card-head .seg { margin-left: auto; }
.empty { color: var(--ink-2); font-size: 13px; }
.foot { margin-top: 8px; color: var(--ink-3); font-size: 12.5px; }
.pair { display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); align-items: start; column-gap: 14px; }

.list { margin: 0; padding: 0; list-style: none; }
.list li { display: flex; align-items: flex-start; gap: 10px; padding: 9px 0; border-bottom: 1px solid var(--line); }
.list li:last-child { border-bottom: 0; }
.state { display: grid; place-items: center; flex: none; width: 18px; height: 20px; }
.main { flex: 1; min-width: 0; }
.ticket-link { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 13.5px; font-weight: 600; line-height: 20px; }
.key { margin-right: 3px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.sub { display: flex; gap: 8px; min-width: 0; margin-top: 2px; color: var(--ink-3); font-size: 12px; }
.sub > * { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.inline-link { color: var(--ink-2); flex: 0 1 auto; }
.who { flex: 1 1 0; }
.time { flex: none; min-width: 64px; color: var(--ink); font-size: 13px; font-weight: 600; line-height: 20px; text-align: right; white-space: nowrap; }
.reason { color: var(--ink-2); font-size: 13px; line-height: 20px; }
.reason b { color: var(--ink); font-weight: 600; }
.open { display: grid; place-items: center; flex: none; width: 28px; height: 28px; margin: -4px -6px 0 -2px; border-radius: 999px; color: var(--ink-3); }
@media (hover: hover) { .open:hover { background: var(--row-hover); color: var(--teal-ink); } }
.open:focus-visible { box-shadow: var(--focus-ring); }
.more-btn { margin-top: 6px; padding: 6px 0; border: 0; background: transparent; color: var(--teal-ink); font-size: 13px; font-weight: 600; cursor: pointer; }
.more-btn:focus-visible { box-shadow: var(--focus-ring); border-radius: 6px; }
@media (hover: hover) { .ticket-link:hover, .inline-link:hover { color: var(--teal-ink); } }
.ticket-link:focus-visible, .inline-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }

.grid { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 13.5px; }
.grid th { height: 30px; padding: 0 8px; text-align: left; font: 500 11.5px/1 var(--font); color: var(--ink-3); border-bottom: 1px solid var(--line-2); white-space: nowrap; }
.grid th:not(:first-child) { width: 104px; }
.grid td { padding: 9px 8px; border-bottom: 1px solid var(--line); }
.grid tr:last-child td { border-bottom: 0; }
@media (hover: hover) { .grid tbody tr:hover td { background: var(--row-hover); } }
.grid th:first-child, .grid td:first-child { padding-left: 0; }
.grid th:last-child, .grid td:last-child { padding-right: 0; }
.r { text-align: right !important; }
.grid .num { white-space: nowrap; }
.zero { color: var(--ink-3); }
.name { min-width: 0; }
.with-mark { display: flex; align-items: center; gap: 8px; min-width: 0; }
.ellipsis { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (prefers-reduced-motion: reduce) { .tiles { transition: none; } }

@media (max-width: 1000px) {
  .pair { grid-template-columns: minmax(0, 1fr); }
  .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 600px) {
  .usage-page { padding: 16px 12px 28px; }
  .page-head { margin-bottom: 14px; }
  .range-head { margin-top: 22px; }
  .toolbar { width: 100%; }
  .toolbar .seg { flex: 1; }
  .toolbar .seg button { flex: 1; height: 36px; }
  .project { flex: 1 1 100%; }
  .project .field { width: 100%; min-width: 0; height: 40px; }
  .tiles { gap: 8px; }
  .tile { min-height: 96px; padding: 12px 13px; }
  .figure b { font-size: 24px; }
  .figure { font-size: 12.5px; }
  .reason-text { font-size: 13.5px; }
  .detail { white-space: normal; font-size: 12.5px; }
  .card { padding: 14px; }
  .opt { display: none; }
  .grid th:not(:first-child) { width: 84px; }
  .ticket-link { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; white-space: normal; }
  .open { width: 40px; height: 40px; margin: -10px -10px -6px -4px; }
  .card-head .seg { margin-left: 0; }
}
</style>
