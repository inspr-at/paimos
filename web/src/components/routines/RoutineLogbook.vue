<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { getRoutineRun, listRoutineRuns, recurrenceHistory, resolveRoutineRoute } from '../../lib/api'
import { getMembers } from '../../lib/access'
import { can } from '../../lib/authz'
import { budgetWords, changeWords, effortWord, mayWords, reasonWords, recurrenceZone, routeWords, recurrenceName, routineState, triggerWords, waitReasonWords, when, type HistoryFilter, type Recurrence, type RecurrenceHistoryEntry, type RecurrenceResult } from '../../lib/recurrences'
import { HISTORY_PAGE, RUNS_PAGE, RUN_LOOK, checkedDetail, checkedRuns, runLine, runState, runTitle, scopeOf, usageShort, type RoutineRun } from '../../lib/routineRuns'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { statusMeta } from '../../lib/work'
import { useSession } from '../../stores/session'
import AppIcon, { type IconName } from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import RunChain from './RunChain.vue'

// The routine logbook (AEON-1192): RecurrenceHistory extended with runs. Its head and
// Pause, Run now and Edit… are RecurrenceHistory's; Upcoming is one fact among Who, May,
// Budget, Runs on and Owner. Agent routines list S21 runs (Runs · Changes), each opening
// into its chain; ticket-only routines keep today's filters (All · Created · Skipped ·
// Changes). One lane per routine and filter: a late page for another routine, filter or
// person never alters the record on screen, and a failed page keeps what is shown. A
// linked run (?run=) older than the loaded pages is read on its own and shown first.
const props = withDefaults(defineProps<{ item: Recurrence; projectKey: string; mayManage: boolean; busy: boolean; back?: boolean; more?: boolean; openRun?: string }>(), { back: false, more: false, openRun: '' })
const emit = defineEmits<{ toggle: []; run: []; edit: []; consent: []; back: []; more: [anchor: HTMLElement]; gone: [] }>()
type LogFilter = 'runs' | HistoryFilter
const session = useSession(), uid = useId()
const scope = useIdentityScope(), pages = scope.lane(), olderPages = scope.lane(), routes = scope.lane(), names = scope.lane(), links = scope.lane()
const agent = computed(() => !!props.item.definition?.assignment)
const zone = computed(() => recurrenceZone(props.item.trigger))
const at = (value: string) => when(value, zone.value)
const filters = computed<[LogFilter, string][]>(() => agent.value ? [['runs', 'Runs'], ['changes', 'Changes']] : [['all', 'All'], ['created', 'Created'], ['skipped', 'Skipped'], ['changes', 'Changes']])
const defaultFilter = (): LogFilter => agent.value ? 'runs' : 'all'
const filter = ref<LogFilter>(defaultFilter())
const runs = ref<RoutineRun[]>([]), entries = ref<RecurrenceHistoryEntry[]>([]), cursor = ref<string | number | null>(null), loadedMore = ref(false)
const loading = ref(false), failure = ref(''), refreshFailure = ref(''), older = ref<'' | 'busy' | 'failed'>(''), olderReason = ref(''), gone = ref(false)
const opened = ref(new Set<string>()), defaulted = ref(false)
const linkedRun = ref<RoutineRun | null>(null), linkNote = ref<'' | 'missing' | 'failed'>(''), linkReason = ref('')
const shown = computed(() => filter.value === 'runs' ? runs.value.length : entries.value.length)
// A running run says how long so far; the clock moves once a minute.
const now = ref(Date.now()), clock = setInterval(() => { now.value = Date.now() }, 60_000)
onBeforeUnmount(() => clearInterval(clock))

function reset() {
  pages.cancel(); olderPages.cancel()
  runs.value = []; entries.value = []; cursor.value = null; loadedMore.value = false
  failure.value = ''; refreshFailure.value = ''; older.value = ''; olderReason.value = ''; loading.value = false
}
function load(mode: 'first' | 'refresh' | 'older' = 'first') {
  const id = props.item.id, chosen = filter.value, before = mode === 'older' ? cursor.value ?? undefined : undefined
  if (mode === 'older' && before === undefined) return
  const current = () => props.item.id === id && filter.value === chosen
  if (mode === 'older') { older.value = 'busy'; olderReason.value = '' } else { refreshFailure.value = ''; if (mode === 'first') { loading.value = true; failure.value = '' } }
  const lane = mode === 'older' ? olderPages : pages
  const handlers = {
    failed: (error: unknown) => {
      if (!current()) return
      if ((error as { status?: number }).status === 404) { gone.value = true; emit('gone'); return }
      const reason = error instanceof Error ? error.message : 'the server didn’t answer'
      if (mode === 'older') { older.value = 'failed'; olderReason.value = reason }
      else if (shown.value) refreshFailure.value = reason
      else failure.value = reason
    },
    settled: () => { if (!current()) return; if (mode !== 'older') loading.value = false; if (older.value === 'busy') older.value = '' },
  }
  if (chosen === 'runs') void lane.run(({ after, signal }) => after(listRoutineRuns(id, before as string | undefined, signal), raw => {
    const page = checkedRuns(raw)
    if (!current()) return
    if (mode === 'older') { runs.value = [...runs.value, ...page.items.filter(run => !runs.value.some(known => known.id === run.id))]; cursor.value = page.next_cursor; loadedMore.value = true }
    else if (mode === 'refresh' && loadedMore.value) { const fresh = new Set(page.items.map(run => run.id)); runs.value = [...page.items, ...runs.value.filter(run => !fresh.has(run.id))] }
    else { runs.value = page.items; cursor.value = page.next_cursor }
    if (!defaulted.value) openDefault()
    else if (mode === 'refresh' && pinned.value) readLinked(false)
  }), handlers)
  else void lane.run(({ after, signal }) => after(recurrenceHistory(id, chosen, before as number | undefined, signal), page => {
    if (!Array.isArray(page?.items)) throw new Error('The history answer was not a history page.')
    if (!current()) return
    if (mode === 'older') { entries.value = [...entries.value, ...page.items.filter(entry => !entries.value.some(known => known.id === entry.id))]; cursor.value = page.next_cursor; loadedMore.value = true }
    else if (mode === 'refresh' && loadedMore.value) { const fresh = new Set(page.items.map(entry => entry.id)); entries.value = [...page.items, ...entries.value.filter(entry => !fresh.has(entry.id))] }
    else { entries.value = page.items; cursor.value = page.next_cursor }
  }), handlers)
}
const expandable = (run: RoutineRun) => run.state !== 'skipped'
// The first open run is the one that needs attention (or the newest); a linked run wins.
function openDefault() {
  if (props.openRun) { defaulted.value = true; openLinked(); return }
  if (!runs.value.length) return
  defaulted.value = true
  openFirst()
}
function openFirst() {
  const first = runs.value.find(run => ['waiting_for_person', 'failed', 'running', 'held'].includes(run.state)) ?? runs.value[0]
  opened.value = first && expandable(first) ? new Set([first.id]) : new Set()
}
// A linked run opens where it is listed. Older than the loaded pages, it is read on its
// own lane, bound to this routine and this link, and shown first until Show older lists it.
const pinned = computed(() => linkedRun.value && !runs.value.some(run => run.id === linkedRun.value!.id) ? linkedRun.value : null)
function openLinked() {
  links.cancel(); linkedRun.value = null; linkNote.value = ''; linkReason.value = ''
  const want = props.openRun
  if (!want) return
  const listed = runs.value.find(run => run.id === want)
  if (listed) opened.value = expandable(listed) ? new Set([want]) : new Set()
  else readLinked(true)
}
function readLinked(open: boolean) {
  const id = props.item.id, want = props.openRun
  const current = () => props.item.id === id && props.openRun === want
  if (!want) return
  linkNote.value = ''
  void links.run(({ after, signal }) => after(getRoutineRun(id, want, signal), raw => {
    const found = checkedDetail(raw, want).run
    if (!current()) return
    if (found.recurrence_id !== id) throw new Error('the run belongs to another routine')
    linkedRun.value = { ...found, state: runState(found.state) }
    if (open) opened.value = expandable(linkedRun.value) ? new Set([want]) : new Set()
  }), { failed: error => {
    if (!current()) return
    if ((error as { status?: number }).status === 404) linkNote.value = 'missing'
    else { linkNote.value = 'failed'; linkReason.value = error instanceof Error ? error.message : 'the server didn’t answer' }
    if (open && !opened.value.size) openFirst()
  } })
}
function toggleRun(id: string) { const next = new Set(opened.value); if (next.has(id)) next.delete(id); else next.add(id); opened.value = next }
function forget() { reset(); links.cancel(); opened.value = new Set(); defaulted.value = false; gone.value = false; linkedRun.value = null; linkNote.value = ''; linkReason.value = '' }
// Another routine on screen starts from nothing, synchronously, before any read.
watch(() => props.item.id, () => { forget(); filter.value = defaultFilter() }, { flush: 'sync' })
watch(agent, () => { if (!filters.value.some(([key]) => key === filter.value)) filter.value = defaultFilter() }, { flush: 'sync' })
watch(filter, () => { reset() }, { flush: 'sync' })
// Another person or workspace: nothing read for the previous one stays.
watch(() => scope.owner.value, () => { forget(); if (scope.owner.value) load('first') }, { flush: 'sync' })
// The link moved on (back, forward or another link to this routine): that run opens instead.
watch(() => props.openRun, want => {
  if (!want) { links.cancel(); linkedRun.value = null; linkNote.value = ''; return }
  if (!agent.value) return
  if (filter.value !== 'runs') { defaulted.value = false; filter.value = 'runs'; return }
  if (defaulted.value) openLinked()
})
watch([() => props.item.id, filter], () => load('first'), { immediate: true })
// The list's poll shows a new revision or run: read the newest page again, keeping older pages.
watch(() => [props.item.id, `${props.item.revision}/${props.item.occurrence_count}/${props.item.last_run?.id ?? ''}/${props.item.last_run?.state ?? ''}`] as const, (next, previous) => { if (previous && next[0] === previous[0] && next[1] !== previous[1]) load('refresh') })

// ---------- Facts: Who, May, Budget, Runs on, Owner, Upcoming ----------
const route = ref<{ text: string; harness: string } | null>(null), routeState = ref<'loading' | 'ready' | 'none' | 'failed'>('none')
watch(() => agent.value ? `${props.item.id}/${props.item.definition?.assignment?.role}/${props.item.project_id}` : '', key => {
  route.value = null
  const role = props.item.definition?.assignment?.role, projectId = props.item.project_id
  if (!key || !role) { routes.cancel(); routeState.value = 'none'; return }
  routeState.value = 'loading'
  void routes.run(({ after, signal }) => after(resolveRoutineRoute(role, projectId, signal), result => {
    route.value = result.profile ? { text: routeWords(result.profile), harness: result.profile.harness } : null
    routeState.value = result.profile ? 'ready' : 'none'
  }), { failed: () => { routeState.value = 'failed' } })
}, { immediate: true })
const me = computed(() => session.identity?.principal.id ?? '')
const ownerId = computed(() => props.item.definition?.owner_principal_id ?? props.item.created_by_principal_id ?? '')
const mine = computed(() => !!ownerId.value && ownerId.value === me.value)
const ownerName = ref('')
watch(ownerId, id => {
  ownerName.value = ''
  if (!id || id === me.value || !can('members.read')) { names.cancel(); return }
  void names.run(({ after }) => after(getMembers(), list => { if (ownerId.value === id) ownerName.value = list.people.find(person => person.principal_id === id)?.name ?? '' }), { failed: () => {} })
}, { immediate: true })
const ownerWords = computed(() => mine.value ? 'You' : ownerName.value || 'Its owner')
const kind = computed(() => scopeOf(props.item))
const scopeWord = computed(() => kind.value === 'project' ? props.projectKey : kind.value === 'personal' ? 'yours' : 'workspace')
const assignment = computed(() => props.item.definition?.assignment)
const policy = computed(() => props.item.execution_policy)
const consented = computed(() => !!policy.value && policy.value.consent_revision > 0 && policy.value.consent_revision === props.item.revision)
const who = computed(() => {
  const a = assignment.value
  if (!a) return 'Just the ticket · people or the queue pick it up'
  const effort = effortWord(a.role)
  return routeState.value === 'ready' && route.value ? `An agent · ${effort} · today ${route.value.text}` : routeState.value === 'none' ? `An agent · ${effort} · no model is set for it yet` : `An agent · ${effort}`
})
const runsOn = computed(() => {
  const a = assignment.value
  if (!a) return ''
  const where = `${a.runtime_requirements.needs_browser ? 'A computer with a browser' : 'A computer'} ${mine.value ? 'you' : ownerName.value || 'its owner'} allowed`
  const as = kind.value === 'personal' ? (mine.value ? 'as you' : `as ${ownerName.value || 'its owner'}`) : consented.value ? 'as the agent picked at consent' : 'its agent is picked at consent'
  return `${where} · ${as}`
})
const EVENT_NEXT: Record<string, string> = { 'release.published': 'After the next release', 'node.done': 'When matching work next reaches Done', 'knowledge.changed': 'When matching knowledge next changes', 'external.tag': 'With the next signed external tag', 'external.deploy': 'With the next signed external deploy' }
const state = computed(() => routineState(props.item))
const consentWait = computed(() => agent.value && policy.value?.wait_reason === 'execution_consent_required')
const upcoming = computed(() => props.item.paused ? (consentWait.value ? 'Paused until agents may run it' : 'Paused · Resume to continue') : props.item.trigger.kind === 'event' ? EVENT_NEXT[props.item.trigger.event || 'release.published'] ?? 'With the next event' : props.item.next_at ? at(props.item.next_at) : 'No next date')
const holdWords = computed(() => {
  const wait = policy.value?.wait_reason ?? ''
  return wait && wait !== 'execution_consent_required' ? waitReasonWords(wait, { project: props.projectKey }) : props.item.last_run?.reason || 'New runs wait until their conditions are met.'
})
const holdCode = computed(() => { const wait = policy.value?.wait_reason ?? ''; return wait && wait !== 'execution_consent_required' ? wait : '' })
const readOnlyTip = 'Changing it needs Manage recurring work'

// ---------- History entries (Changes, and ticket-only routines) ----------
const occurrence = (entry: RecurrenceHistoryEntry): RecurrenceResult | null => entry.after && 'outcome' in entry.after ? entry.after : null
const sentence = (value: string) => value.trim().replace(/\.$/, '')
function entryLook(entry: RecurrenceHistoryEntry): { icon: IconName; change: boolean; before: string; key: string; after: string; small: string } {
  const action = entry.type.slice('recurrence.'.length), result = occurrence(entry), time = at(entry.at)
  if (action === 'occurred' && result) {
    const state = entry.node ? ` · ${statusMeta(entry.node.state).label.toLowerCase()}` : result.node_id ? ' · ticket no longer available' : ''
    return { icon: 'ticket', change: false, before: `#${result.number} · Created `, key: entry.node?.key ?? '', after: `${entry.node ? '' : 'a ticket'}${state}`, small: `${time}${result.occurrence_key.startsWith('manual:') ? ' · Run now' : ''}` }
  }
  if (action === 'skipped' && result) return { icon: 'not', change: false, before: `#${result.number} · Skipped · ${sentence(reasonWords(result.reason))}`, key: '', after: '', small: time }
  const verb = ({ created: 'Set up', updated: 'Changed', paused: 'Paused', resumed: 'Resumed', deleted: 'Deleted' } as Record<string, string>)[action] ?? action.replaceAll('_', ' ')
  const changes = action === 'updated' && entry.after && 'template' in entry.after ? changeWords(entry.before, entry.after) : ''
  return { icon: ({ created: 'repeat', paused: 'pause', resumed: 'play' } as Record<string, IconName>)[action] ?? 'edit', change: true, before: `${verb} by ${entry.actor_name}${changes ? `: ${changes}` : ''}`, key: '', after: '', small: time }
}
const emptyWords = computed(() => {
  if (filter.value === 'runs') return consentWait.value ? 'No runs yet. The first one follows consent; see Changes for how it was set up.' : 'No runs yet.'
  return `Nothing ${filter.value === 'created' ? 'created' : filter.value === 'skipped' ? 'skipped' : filter.value === 'changes' ? 'changed' : 'here'} yet.`
})
const entryRows = computed(() => entries.value.map(entry => ({ entry, look: entryLook(entry) })))
const runRow = (run: RoutineRun, linked = false) => ({ run, look: RUN_LOOK[run.state], title: runTitle(run, now.value), line: `${runLine(run, at(run.created_at), true)}${linked ? ' · the linked run, older than the runs below' : ''}`, usage: usageShort(run) })
const runRows = computed(() => [...(pinned.value ? [runRow(pinned.value, true)] : []), ...runs.value.map(run => runRow(run))])
const ticketLink = (key: string) => `/p/${encodeURIComponent(props.projectKey)}/${encodeURIComponent(key)}`
</script>

<template>
  <section class="lb glass-card" :aria-labelledby="`${uid}-h`" data-logbook>
    <p v-if="gone" class="calm-line" role="status"><AppIcon name="eye" :size="13" /><span><b>This logbook closed.</b> The routine is gone or no longer visible to you, so nothing of it is shown here.</span></p>
    <template v-else>
      <button v-if="back" type="button" class="btn sm ghost lb-back" data-back @click="emit('back')"><AppIcon name="arrow-left" :size="13" />All routines</button>
      <header class="lb-head">
        <div class="lb-t"><p class="eyebrow">Routine · {{ scopeWord }}</p><h2 :id="`${uid}-h`">{{ recurrenceName(item) }}</h2><p>{{ triggerWords(item.trigger) }}</p></div>
        <div class="lb-acts">
          <button type="button" class="btn sm" :disabled="!mayManage || busy" :data-tip="mayManage ? undefined : readOnlyTip" @click="emit('toggle')"><AppIcon :name="item.paused ? 'play' : 'pause'" :size="12" /><span class="btn-label"><span>{{ item.paused ? 'Resume' : 'Pause' }}</span><span aria-hidden="true">{{ item.paused ? 'Pause' : 'Resume' }}</span></span></button>
          <button type="button" class="btn sm" :disabled="!mayManage || busy" :data-tip="mayManage ? undefined : readOnlyTip" @click="emit('run')">Run now</button>
          <button type="button" class="btn sm ghost" :disabled="!mayManage || busy" :data-tip="mayManage ? undefined : readOnlyTip" @click="emit('edit')"><AppIcon name="edit" :size="13" />Edit…</button>
          <button v-if="more" type="button" class="icon-btn sm flat" :aria-label="`More for ${recurrenceName(item)}`" aria-haspopup="menu" @click="emit('more', $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="15" /></button>
        </div>
      </header>
      <div v-if="consentWait" class="lb-ask" role="note"><AppIcon name="person-wait" :size="16" /><div><b>{{ mine ? 'It waits for you to let agents run it.' : `${ownerName || 'Its owner'} decides whether agents may run it.` }}</b><span>{{ mine ? 'Nothing runs until you say so.' : 'Nothing runs until they let agents run it.' }}</span></div><button v-if="mine && mayManage" type="button" class="btn sm primary" @click="emit('consent')">Review and decide…</button></div>
      <div v-else-if="agent && state === 'held'" class="lb-ask" role="note"><AppIcon name="pause" :size="16" /><div><b>Held.</b><span>{{ holdWords }} <span v-if="holdCode" class="code">{{ holdCode }}</span></span></div><button v-if="mayManage" type="button" class="btn sm" @click="emit('edit')">Change…</button></div>
      <dl class="lb-facts">
        <div><dt>Who</dt><dd><HarnessMark v-if="route && routeState === 'ready'" class="hg" :harness="route.harness" :size="13" />{{ who }}</dd></div>
        <template v-if="assignment"><div><dt>May</dt><dd>{{ mayWords(assignment.allowed_actions ?? []) }}</dd></div><div><dt>Budget</dt><dd>{{ budgetWords(assignment.budget) }}</dd></div><div><dt>Runs on</dt><dd>{{ runsOn }}</dd></div></template>
        <div><dt>Owner</dt><dd>{{ ownerWords }} · tickets in {{ projectKey }}</dd></div>
        <div><dt>Upcoming</dt><dd>{{ upcoming }}</dd></div>
      </dl>
      <div class="lb-filter"><p class="eyebrow">Logbook</p><div class="seg" role="radiogroup" aria-label="Show in the logbook"><button v-for="[value, label] in filters" :key="value" type="button" role="radio" :data-lf="value" :aria-checked="filter === value" @click="filter = value">{{ label }}</button></div></div>
      <p v-if="failure" class="m-err" role="alert"><AppIcon name="alert" :size="15" /><span>The logbook couldn’t be read: {{ failure }}. Nothing here changed.</span><button type="button" class="btn sm" @click="load()">Try again</button></p>
      <div v-else-if="loading && !shown" class="runs-loading" role="status" aria-label="Loading the logbook"><span v-for="index in 3" :key="index" class="skeleton" /></div>
      <p v-else-if="!shown && !(filter === 'runs' && pinned)" class="empty">{{ emptyWords }}</p>
      <ol v-if="filter === 'runs' && runRows.length" class="runs">
        <li v-for="{ run, look, title, line, usage } in runRows" :key="run.id" class="run" :class="[look.tone, { open: opened.has(run.id) }]">
          <button type="button" class="run-btn" :data-run="run.id" :aria-expanded="expandable(run) ? opened.has(run.id) : undefined" :disabled="!expandable(run)" @click="toggleRun(run.id)">
            <span class="r-ic" :class="look.tone" aria-hidden="true"><span v-if="look.icon === 'live'" class="pulse" /><AppIcon v-else :name="look.icon as IconName" :size="13" /></span>
            <span class="run-t"><b>{{ title }}</b><small>{{ line }}</small></span>
            <span class="run-use" :class="{ warn: usage.warn }">{{ usage.text }}</span>
            <AppIcon v-if="expandable(run)" class="chev-r" name="chevron-right" :size="13" /><span v-else />
          </button>
          <RunChain v-if="opened.has(run.id)" :recurrence-id="item.id" :run="run" :project-key="projectKey" :zone="zone" />
        </li>
      </ol>
      <ol v-else-if="filter !== 'runs' && entries.length" class="runs">
        <li v-for="{ entry, look } in entryRows" :key="entry.id" class="run row" :class="{ chg: look.change }">
          <span class="r-ic calm" aria-hidden="true"><AppIcon :name="look.icon" :size="12" /></span>
          <span class="run-t"><b>{{ look.before }}<RouterLink v-if="look.key" class="key" :to="ticketLink(look.key)">{{ look.key }}</RouterLink>{{ look.after }}</b><small>{{ look.small }}</small></span>
        </li>
      </ol>
      <p v-if="shown" class="lb-foot">
        <span v-if="older === 'failed'" class="warn-line" role="alert"><AppIcon name="alert" :size="13" /><span>Older entries couldn’t be read ({{ olderReason }}). Shown: the newest {{ shown }}.</span></span>
        <span v-else-if="refreshFailure" class="warn-line" role="alert"><AppIcon name="alert" :size="13" /><span>Newer entries couldn’t be read ({{ refreshFailure }}). Shown: what was read before.</span></span>
        <span v-else>{{ cursor !== null ? `Showing the newest ${shown} · ${filter === 'runs' ? RUNS_PAGE : HISTORY_PAGE} per page` : `Showing all ${shown}` }}</span>
        <button v-if="refreshFailure && older !== 'failed'" type="button" class="btn sm ghost" @click="load('refresh')">Try again</button>
        <button v-else-if="cursor !== null" type="button" class="btn sm ghost" data-older :disabled="older === 'busy'" @click="load('older')"><span class="btn-label"><span>{{ older === 'failed' ? 'Try again' : 'Show older' }}</span><span aria-hidden="true">{{ older === 'failed' ? 'Show older' : 'Try again' }}</span></span></button>
      </p>
      <p v-if="filter === 'runs' && linkNote" class="lb-foot" data-link-note :role="linkNote === 'failed' ? 'alert' : 'status'">
        <span class="warn-line"><AppIcon name="alert" :size="13" /><span>{{ linkNote === 'missing' ? 'The linked run isn’t in this logbook, or isn’t visible to you.' : `The linked run couldn’t be read (${linkReason}).` }}</span></span>
        <button v-if="linkNote === 'failed'" type="button" class="btn sm ghost" @click="readLinked(true)">Try again</button>
      </p>
      <p v-if="agent" class="lb-note"><AppIcon name="info" :size="13" /><span>Accounts never show here. Usage counts each agent session once; unknown or partial usage is never shown as zero.</span></p>
    </template>
  </section>
</template>

<style scoped>
.lb { container: logbook / inline-size; min-width: 0; padding: 18px 20px 16px; }
.lb-back { margin: 0 0 8px -6px; }
/* The title keeps at least 16rem; below that the actions wrap under it, never squeezing the name. */
.lb-head { display: flex; align-items: flex-start; flex-wrap: wrap; gap: 8px 12px; margin-bottom: 12px; }
.lb-t { flex: 1 1 16rem; min-width: 0; }
.lb-t h2 { margin-top: 2px; font: 600 18px/1.3 var(--font); overflow-wrap: anywhere; }
.lb-t > p:last-child { margin-top: 2px; font-size: 12.5px; color: var(--ink-3); }
.lb-acts { display: flex; align-items: center; flex-wrap: wrap; justify-content: flex-end; gap: 4px; flex: none; }
.lb-ask { display: grid; grid-template-columns: 16px minmax(0, 1fr) auto; align-items: center; gap: 12px; margin-bottom: 12px; padding: 12px 14px; border-radius: 12px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); font-size: 13px; color: var(--ink-2); }
.lb-ask > svg { color: var(--queue-wait-ink); }
.lb-ask b { display: block; color: var(--ink); font-weight: 650; }
.lb-ask .code { font: 500 11px/1 var(--mono); color: var(--queue-wait-ink); white-space: nowrap; }
.lb-facts { display: grid; grid-template-columns: 1fr 1fr; gap: 0 24px; margin: 0 0 14px; padding: 4px 14px; border-radius: 12px; background: var(--surface-sunken); }
.lb-facts > div { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 7px 0; font-size: 12.5px; }
.lb-facts > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
.lb-facts dt { font: 500 10px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.lb-facts dd { margin: 0; color: var(--ink); line-height: 1.45; overflow-wrap: anywhere; }
.hg { display: inline-grid; margin-right: 6px; vertical-align: -2px; }
.lb-filter { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; min-height: 34px; margin-bottom: 4px; }
.lb-filter .seg button { height: 24px; padding: 0 9px; font-size: 12px; }
.runs { display: grid; margin: 0; padding: 0; list-style: none; }
.run { border-top: 1px solid var(--line); }
.run-btn { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto 14px; align-items: center; gap: 12px; width: 100%; min-height: 56px; padding: 8px 6px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); text-align: left; font: inherit; cursor: pointer; }
@media (hover: hover) { .run-btn:hover:not(:disabled) { background: var(--row-hover); } }
.run-btn:disabled { opacity: 1; cursor: default; }
.run-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.run.open .run-btn { background: var(--surface-sunken); }
.run.bad.open .run-btn { background: var(--danger-bg); }
.run.row { display: grid; grid-template-columns: 24px minmax(0, 1fr); gap: 12px; align-items: center; min-height: 48px; padding: 6px; }
.run.chg .run-t b { font-weight: 500; color: var(--ink-2); }
.r-ic { display: grid; place-items: center; width: 24px; height: 24px; border-radius: 50%; color: var(--ink-3); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); }
.r-ic.ok { color: var(--ok); background: var(--ok-bg); box-shadow: inset 0 0 0 1px var(--ok-line); }
.r-ic.wait { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.r-ic.bad { color: var(--danger); background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.r-ic.live { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.run-t { display: grid; min-width: 0; }
.run-t b { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
.run.bad .run-t b { color: var(--danger); }
.run-t small { font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }
.run-use { font-size: 12px; color: var(--ink-2); white-space: nowrap; font-variant-numeric: tabular-nums; }
.run-use.warn { color: var(--queue-wait-ink); font-weight: 600; }
.chev-r { flex: none; color: var(--ink-3); transition: transform .2s ease; }
[aria-expanded="true"] > .chev-r { transform: rotate(90deg); }
.key { font: 600 11.5px var(--mono); color: var(--teal-ink); text-decoration: none; }
.key:hover { text-decoration: underline; text-underline-offset: 2px; }
.empty { padding: 16px 6px; font-size: 13px; color: var(--ink-3); }
.runs-loading { display: grid; gap: 14px; padding: 18px 6px; }
.runs-loading .skeleton { height: 12px; }
.runs-loading .skeleton:nth-child(2) { width: 80%; }
.runs-loading .skeleton:nth-child(3) { width: 60%; }
.m-err { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; padding: 14px 6px; font-size: 13px; color: var(--ink-2); }
.m-err > svg { flex: none; color: var(--warn-ink); }
.m-err > span { flex: 1 1 240px; }
.lb-foot { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px 12px; margin-top: 2px; padding-top: 10px; border-top: 1px solid var(--line); font-size: 12px; color: var(--ink-3); }
.lb-foot + .lb-foot { margin-top: 0; padding-top: 6px; border-top: 0; }
.warn-line { display: inline-flex; align-items: flex-start; gap: 8px; padding: 4px 10px; border-radius: 10px; color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); line-height: 1.5; }
.warn-line > svg { flex: none; margin-top: 2px; }
.lb-note { display: flex; align-items: flex-start; gap: 8px; margin-top: 10px; font-size: 12px; color: var(--ink-3); }
.lb-note > svg { flex: none; margin-top: 2px; }
.calm-line { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: 10px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); background: var(--surface-sunken); }
.calm-line > svg { flex: none; margin-top: 2px; }
.calm-line b { color: var(--ink); font-weight: 600; }
.pulse { position: relative; display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); }
@media (prefers-reduced-motion: no-preference) {
  .pulse::after { content: ''; position: absolute; inset: -3px; border-radius: 50%; box-shadow: 0 0 0 1.5px var(--teal); opacity: 0; animation: ring 1.8s ease-out infinite; }
  @keyframes ring { 0% { transform: scale(.6); opacity: .8; } 100% { transform: scale(1.5); opacity: 0; } }
}
@media (prefers-reduced-motion: reduce) { .chev-r { transition: none; } }
@container logbook (max-width: 560px) {
  .lb-acts { justify-content: flex-start; }
  .lb-facts { grid-template-columns: minmax(0, 1fr); }
  .lb-facts > div:nth-child(n + 2) { border-top: 1px solid var(--line); }
  .lb-ask { grid-template-columns: 16px minmax(0, 1fr); }
  .lb-ask > .btn { grid-column: 2; justify-self: start; }
  .run-btn { grid-template-columns: 24px minmax(0, 1fr) 14px; }
  .run-use { grid-column: 2; grid-row: 2; }
  .run-btn .chev-r { grid-column: 3; grid-row: 1; }
}
@media (max-width: 600px) { .lb { padding: 14px 14px 12px; } }
@media (pointer: coarse) {
  .lb-filter .seg button { min-width: 44px; height: auto; min-height: 44px; }
  .lb-acts .btn, .lb-foot .btn, .lb-ask .btn, .lb-back { min-height: 44px; }
  .lb-acts .icon-btn { width: 44px; height: 44px; }
  .key { display: inline-flex; align-items: center; min-height: 44px; }
}
</style>
