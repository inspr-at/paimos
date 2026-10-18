<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { APIError, getNode, listNodes } from '../../lib/api'
import { conflictEntry, createEntry, createPeriod, deleteEntry, findEntryEvent, listEntries, listPeriods, PERIOD_HISTORY_START, undoEntryEvent, updateEntry, type EntryPatch, type TimeEntry, type TimePeriod } from '../../lib/business'
import { command, consume } from '../../lib/commands'
import { dismiss, toast } from '../../lib/toast'
import { absoluteTime, plural } from '../../lib/work'
import { addDays, dayKey, isoWeek, parseDayKey, periodLabel, startOfWeek, timeOfDay, weekDays, weekLabel, WEEKDAYS } from '../../lib/week'
import { formatClock, formatSpan } from '../../components/business/duration'
import { sumAmounts } from '../../components/business/money'
import { useBusiness } from '../../stores/business'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AppIcon from '../../components/business/BizIcon.vue'
import BusinessPage from '../../components/business/BusinessPage.vue'
import LogTimeBar, { type LogRequest, type LogTicket } from '../../components/business/LogTimeBar.vue'
import MoneyText from '../../components/business/MoneyText.vue'
import PeriodPanel from '../../components/business/PeriodPanel.vue'
import PickerMenu, { type PickOption } from '../../components/business/PickerMenu.vue'

// Hours by week for one person (or agent): what was logged on which ticket and
// day, a fast line to log more, and for admins the periods waiting for approval.
const business = useBusiness()
const projects = useProjects()
const session = useSession()
const route = useRoute()
const router = useRouter()
const logBar = ref<InstanceType<typeof LogTimeBar>>()
const me = computed(() => session.identity?.principal.id ?? '')
const view = computed<'week' | 'approvals'>(() => route.query.view === 'approvals' && business.admin ? 'approvals' : 'week')
const person = computed(() => typeof route.query.person === 'string' && business.admin ? route.query.person : me.value)
const weekStart = computed(() => startOfWeek(typeof route.query.week === 'string' ? parseDayKey(route.query.week) ?? new Date() : new Date()))
const days = computed(() => weekDays(weekStart.value))
const weekEnd = computed(() => addDays(weekStart.value, 7))
const thisWeek = computed(() => dayKey(weekStart.value) === dayKey(startOfWeek(new Date())))
const todayKey = dayKey(new Date())
const personInfo = computed(() => business.principals.find(p => p.id === person.value))
const isAgent = computed(() => personInfo.value?.kind === 'agent')
const personName = computed(() => person.value === me.value ? 'You' : business.nameOf(person.value))
function go(patch: Record<string, string | undefined>) {
  const query = { ...route.query, ...patch }
  for (const key of Object.keys(query)) if (!query[key]) delete query[key]
  void router.replace({ query })
}
function shiftWeek(step: number) { const next = addDays(weekStart.value, step * 7); go({ week: dayKey(next) === dayKey(startOfWeek(new Date())) ? undefined : dayKey(next) }) }

// ---------- Nodes (tickets) by id ----------
const nodes = ref(new Map<string, { key: string; title: string }>())
async function resolveNodes(ids: string[]) {
  const missing = [...new Set(ids)].filter(id => !nodes.value.has(id))
  if (!missing.length) return
  const found = await Promise.all(missing.map(id => getNode(id).then(n => [id, { key: n.key, title: n.title }] as const).catch(() => [id, { key: '—', title: 'Deleted node' }] as const)))
  const next = new Map(nodes.value)
  for (const [id, info] of found) next.set(id, info)
  nodes.value = next
}
function ticketHref(nodeId: string) {
  const key = nodes.value.get(nodeId)?.key ?? ''
  const project = projects.byRouteKey(key.split('-')[0] ?? '')
  return project ? `/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(key)}` : ''
}

// ---------- Week ----------
const periods = ref<TimePeriod[]>([])
const entries = ref<TimeEntry[]>([])
const weekState = ref<'loading' | 'ready' | 'error'>('loading')
const weekError = ref('')
let generation = 0
async function loadWeek() {
  const request = ++generation
  const principal = person.value
  if (!principal) return
  weekState.value = 'loading'
  try {
    const start = weekStart.value.getTime(), end = weekEnd.value.getTime()
    const list = await listPeriods(principal, { since: new Date(start).toISOString(), until: new Date(end).toISOString() })
    const inWeek = list.filter(p => Date.parse(p.starts_at) < end && Date.parse(p.ends_at) > start)
    const found = (await Promise.all(inWeek.map(p => listEntries({ period_id: p.id })))).flat()
      .filter(e => Date.parse(e.started_at) >= start && Date.parse(e.started_at) < end)
      .sort((a, b) => a.started_at.localeCompare(b.started_at))
    await resolveNodes(found.map(e => e.node_id))
    if (request !== generation) return
    periods.value = list; entries.value = found; weekState.value = 'ready'
  } catch (e) {
    if (request !== generation) return
    weekState.value = 'error'; weekError.value = e instanceof Error ? e.message : 'Hours could not be loaded.'
  }
}
const weekPeriods = computed(() => periods.value.filter(p => Date.parse(p.starts_at) < weekEnd.value.getTime() && Date.parse(p.ends_at) > weekStart.value.getTime()))
const closed = computed(() => weekPeriods.value.length > 0 && weekPeriods.value.every(p => p.state === 'approved'))
const perDay = computed(() => days.value.map(day => entries.value.filter(e => dayKey(new Date(e.started_at)) === dayKey(day)).reduce((s, e) => s + e.duration_seconds, 0)))
const total = computed(() => perDay.value.reduce((a, b) => a + b, 0))
const amounts = computed(() => {
  const by = new Map<string, string[]>()
  for (const e of entries.value) by.set(e.currency, [...(by.get(e.currency) ?? []), e.amount])
  return [...by.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([currency, list]) => ({ currency, amount: sumAmounts(list) }))
})
const grid = computed(() => {
  const rows = new Map<string, number[]>()
  for (const e of entries.value) {
    const row = rows.get(e.node_id) ?? Array(7).fill(0)
    row[(new Date(e.started_at).getDay() + 6) % 7] += e.duration_seconds
    rows.set(e.node_id, row)
  }
  return [...rows.entries()].map(([id, cells]) => ({ id, cells, total: cells.reduce((a, b) => a + b, 0) })).sort((a, b) => b.total - a.total)
})
const byDay = computed(() => days.value.map((day, i) => ({ day, i, list: entries.value.filter(e => dayKey(new Date(e.started_at)) === dayKey(day)) })).filter(group => group.list.length).reverse())
const suggestions = computed<LogTicket[]>(() => grid.value.map(row => ({ id: row.id, key: nodes.value.get(row.id)?.key ?? '', title: nodes.value.get(row.id)?.title ?? '' })).filter(t => t.key))
const canLog = computed(() => !isAgent.value && !closed.value && (person.value === me.value || business.admin) && business.staff)

// ---------- Logging ----------
const logging = ref(false)
// Phones keep the entry form folded behind one button until it is needed.
const phoneQuery = window.matchMedia('(max-width: 720px)')
const phone = ref(phoneQuery.matches)
const logOpen = ref(!phone.value)
const onPhone = (event: MediaQueryListEvent) => { phone.value = event.matches }
phoneQuery.addEventListener('change', onPhone)
async function openLog() { logOpen.value = true; await nextTick(); logBar.value?.focus() }
const preset = ref<LogTicket | null>(null)
function stackAfter(day: Date) {
  const same = entries.value.filter(e => dayKey(new Date(e.started_at)) === dayKey(day))
  const last = same.reduce((max, e) => Math.max(max, Date.parse(e.ended_at)), 0)
  if (!last) return 9 * 60
  const end = new Date(last)
  return end.getHours() * 60 + end.getMinutes() + (end.getSeconds() ? 1 : 0)
}
async function log(request: LogRequest) {
  const principal = person.value
  const started = new Date(request.startedAt)
  const ended = new Date(started.getTime() + request.seconds * 1000)
  if (dayKey(ended) !== dayKey(request.day) && ended.getHours() + ended.getMinutes() > 0) { toast('That entry would run past midnight. Split it across two days.', { tone: 'error' }); return }
  logging.value = true
  try {
    let period = periods.value.find(p => p.principal_id === principal && Date.parse(p.starts_at) <= started.getTime() && Date.parse(p.ends_at) >= ended.getTime())
    if (period?.state === 'approved') { toast(`${periodLabel(period.starts_at, period.ends_at)} is approved and closed. Log it in another period.`, { tone: 'error' }); return }
    if (!period) {
      const clash = weekPeriods.value.find(p => Date.parse(p.starts_at) < ended.getTime() && Date.parse(p.ends_at) > started.getTime())
      if (clash) { toast(`That time falls across the edge of the period ${periodLabel(clash.starts_at, clash.ends_at)}. Choose a time inside it.`, { tone: 'error' }); return }
      period = await createPeriod({ principal_id: principal, starts_at: weekStart.value.toISOString(), ends_at: weekEnd.value.toISOString() })
      periods.value = [...periods.value, period]
    }
    const entry = await createEntry({ period_id: period.id, cost_unit_node_id: request.costUnitId, currency: request.currency, principal_id: principal, node_id: request.ticket.id, started_at: started.toISOString(), ended_at: ended.toISOString(), note: request.note })
    nodes.value = new Map(nodes.value).set(request.ticket.id, { key: request.ticket.key, title: request.ticket.title })
    entries.value = [...entries.value, entry].sort((a, b) => a.started_at.localeCompare(b.started_at))
    logBar.value?.reset()
    toast(`Logged ${formatSpan(entry.duration_seconds)} on ${request.ticket.key}.`)
  } catch (e) {
    if (e instanceof APIError && e.status === 409) { toast(e.message.includes('rate') ? 'This cost unit needs exactly one hourly rate in force on that day.' : 'The period changed or closed meanwhile. The week is reloaded.', { tone: 'error' }); void loadWeek() }
    else toast(`Not logged: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
  } finally { logging.value = false }
}

// ---------- Corrections ----------
// An entry changes while its period is open: its author or an admin corrects it
// in place (only what changed is sent, against the revision it was read at) or
// deletes it with a short server-side undo. Approved periods stay as approved.
const UNDO_MS = 6000
const editing = ref<string | null>(null)
// The version the draft corrects; after a conflict, the newer entry.
const editBase = ref<TimeEntry | null>(null)
const conflict = ref<TimeEntry | null>(null)
const saving = ref(false)
const cursor = ref<string | null>(null)
const entriesCard = ref<HTMLElement>()
const byStart = (a: TimeEntry, b: TimeEntry) => a.started_at.localeCompare(b.started_at)
const periodOf = (e: TimeEntry) => periods.value.find(p => p.id === e.period_id) ?? allPeriods.value.find(p => p.id === e.period_id)
const keyOf = (e: TimeEntry) => nodes.value.get(e.node_id)?.key ?? 'the ticket'
// Why an entry can't change, or '' when it can.
function lockReason(e: TimeEntry): string {
  const period = periodOf(e)
  if (period?.state === 'approved') return `Locked: approved${period.approval ? ` by ${business.nameOf(period.approval.approved_by_principal_id)}` : ''}.`
  if (!business.staff) return 'Locked: only team members correct hours.'
  if (e.principal_id !== me.value && !business.admin) {
    return business.principals.find(p => p.id === e.principal_id)?.kind === 'agent' ? 'Locked: only an admin corrects an agent’s entries.' : `Locked: only ${business.nameOf(e.principal_id)} or an admin can change it.`
  }
  return ''
}
// When every entry shares one lock, the card says it once, in full.
const lockedAll = computed(() => {
  const reasons = new Set(entries.value.map(lockReason))
  const only = reasons.size === 1 ? [...reasons][0] : ''
  return only.startsWith('Locked: approved') ? `${only.slice(0, -1)}, so these entries can’t change.` : only
})
const editable = computed(() => entries.value.some(e => !lockReason(e)))
const flat = computed(() => byDay.value.flatMap(group => group.list))
const tabStop = computed(() => flat.value.some(e => e.id === cursor.value) ? cursor.value : flat.value[0]?.id ?? null)
async function focusEntry(id: string | null) {
  if (!id) return
  cursor.value = id
  await nextTick()
  entriesCard.value?.querySelector<HTMLElement>(`[data-entry="${CSS.escape(id)}"]`)?.focus()
}
function syncPeriod(periodId: string, change: (list: TimeEntry[]) => TimeEntry[]) {
  const list = periodEntries.value.get(periodId)
  if (list) periodEntries.value = new Map(periodEntries.value).set(periodId, change(list))
}
// Every correction bumps its period's revision on the server; mirror it so an
// open review re-reads the entries digest.
function bump(periodId: string) {
  const up = (p: TimePeriod) => p.id === periodId ? { ...p, revision: p.revision + 1 } : p
  periods.value = periods.value.map(up); allPeriods.value = allPeriods.value.map(up)
}
function putEntry(next: TimeEntry) {
  const inWeek = Date.parse(next.started_at) >= weekStart.value.getTime() && Date.parse(next.started_at) < weekEnd.value.getTime() && next.principal_id === person.value
  entries.value = [...entries.value.filter(x => x.id !== next.id), ...(inWeek ? [next] : [])].sort(byStart)
  syncPeriod(next.period_id, list => [...list.filter(x => x.id !== next.id), next].sort(byStart))
}
function dropEntry(e: TimeEntry) {
  entries.value = entries.value.filter(x => x.id !== e.id)
  syncPeriod(e.period_id, list => list.filter(x => x.id !== e.id))
}
function correctionProblem(e: unknown): string {
  if (!(e instanceof APIError)) return e instanceof Error ? e.message : 'unknown error'
  if (e.status === 403) return 'only the author or an admin can change this entry.'
  if (/rate/.test(e.message)) return 'this cost unit needs exactly one hourly rate on that day.'
  if (/telemetry|agent-run/.test(e.message)) return 'an agent run’s ticket and time come from the run.'
  if (/period|interval/.test(e.message)) return 'the time has to fit inside the entry’s period.'
  return e.message
}
const approvedMeanwhile = (e: unknown) => e instanceof APIError && e.status === 409 && /approved|immutable/.test(e.message)
function startEdit(e: TimeEntry) {
  const reason = lockReason(e)
  if (reason) { toast(reason); return }
  editing.value = e.id; editBase.value = e; conflict.value = null; cursor.value = e.id
}
// Leaving the draft after a conflict shows the newer entry.
function stopEdit() {
  const id = editing.value
  if (conflict.value) putEntry(conflict.value)
  editing.value = null; editBase.value = null; conflict.value = null
  void focusEntry(id)
}
async function save(patch: EntryPatch, ticket: LogTicket) {
  const base = editBase.value
  if (!base || saving.value) return
  if (!Object.keys(patch).length) { stopEdit(); return }
  saving.value = true
  try {
    const next = await updateEntry(base, patch)
    nodes.value = new Map(nodes.value).set(ticket.id, { key: ticket.key, title: ticket.title })
    conflict.value = null
    putEntry(next); bump(next.period_id)
    editing.value = null; editBase.value = null
    toast(`Saved ${formatSpan(next.duration_seconds)} on ${ticket.key}.`)
    void focusEntry(next.id)
  } catch (e) {
    const newer = conflictEntry(e)
    if (newer) { await resolveNodes([newer.node_id]); conflict.value = newer; editBase.value = newer; return }
    if (e instanceof APIError && e.status === 404) { dropEntry(base); editing.value = null; editBase.value = null; toast('This entry was deleted meanwhile.', { tone: 'error' }); return }
    if (approvedMeanwhile(e)) { editing.value = null; editBase.value = null; toast('The period was approved meanwhile, so this entry is locked now.', { tone: 'error' }); void loadWeek(); return }
    toast(`Not saved: ${correctionProblem(e)}`, { tone: 'error' })
  } finally { saving.value = false }
}
type Deletion = 'deleted' | 'gone' | 'failed'
function remove(listed: TimeEntry) {
  const base = editing.value === listed.id && editBase.value ? editBase.value : listed
  const reason = lockReason(base)
  if (reason) { toast(reason); return }
  const order = flat.value, at = order.findIndex(x => x.id === base.id)
  const after = order[at + 1]?.id ?? order[at - 1]?.id ?? null
  if (editing.value === base.id) { editing.value = null; editBase.value = null; conflict.value = null }
  dropEntry(base)
  const key = keyOf(base)
  let toastId = 0
  const done: Promise<Deletion> = deleteEntry(base).then(() => { bump(base.period_id); return 'deleted' as const }, err => {
    dismiss(toastId)
    if (err instanceof APIError && err.status === 404) return 'gone' as const
    const newer = conflictEntry(err)
    if (newer) { putEntry(newer); toast('This entry changed meanwhile, so it was not deleted. The newer version is shown.', { tone: 'error' }); return 'failed' as const }
    putEntry(base)
    if (approvedMeanwhile(err)) { toast('The period was approved meanwhile, so this entry is locked.', { tone: 'error' }); void loadWeek() }
    else toast(`Not deleted: ${correctionProblem(err)}`, { tone: 'error' })
    return 'failed' as const
  })
  toastId = toast(`Deleted ${formatSpan(base.duration_seconds)} on ${key}.`, { timeout: UNDO_MS, action: { label: 'Undo', run: () => void undoRemove(base, done, toastId) } })
  void focusEntry(after)
}
// Undo shows the entry again at once and reverses the deletion on the server.
async function undoRemove(e: TimeEntry, done: Promise<Deletion>, toastId: number) {
  dismiss(toastId)
  putEntry(e)
  try {
    const outcome = await done
    if (outcome === 'gone') { dropEntry(e); return }
    if (outcome === 'failed') return
    const event = await findEntryEvent(e.node_id, e.id, 'time_entry.deleted')
    if (event === null) throw new Error('no deletion to undo')
    const restored = await undoEntryEvent(event)
    putEntry(restored ?? e); bump(e.period_id)
    toast(`Restored ${formatSpan(e.duration_seconds)} on ${keyOf(e)}.`)
    void focusEntry(e.id)
  } catch (err) {
    dropEntry(e)
    toast(err instanceof APIError && err.status === 409 ? 'The entry could not be restored: its period was approved or it changed meanwhile.' : 'The entry could not be restored.', { tone: 'error' })
    void loadWeek()
  }
}
function rowKey(event: KeyboardEvent, e: TimeEntry) {
  if (event.defaultPrevented) return
  // Escape from the conflict notice leaves the draft too (the form handles its own).
  if (editing.value === e.id) { if (event.key === 'Escape') { event.preventDefault(); stopEdit() } return }
  if (event.target !== event.currentTarget || event.metaKey || event.ctrlKey || event.altKey) return
  const order = flat.value, at = order.findIndex(x => x.id === e.id)
  const move = (id: string | undefined) => { event.preventDefault(); void focusEntry(id ?? e.id) }
  if (event.key === 'e' || event.key === 'Enter') { event.preventDefault(); startEdit(e) }
  else if (event.key === 'Delete' || event.key === 'Backspace') { event.preventDefault(); remove(e) }
  else if (event.key === 'ArrowDown' || event.key === 'j') move(order[at + 1]?.id)
  else if (event.key === 'ArrowUp' || event.key === 'k') move(order[at - 1]?.id)
  else if (event.key === 'Home') move(order[0]?.id)
  else if (event.key === 'End') move(order[order.length - 1]?.id)
}
function rowClick(event: MouseEvent, e: TimeEntry) {
  if ((event.target as HTMLElement).closest('a, button, input, select, form') || editing.value === e.id) return
  if (window.getSelection()?.toString()) return
  if (lockReason(e)) { cursor.value = e.id; toast(lockReason(e)); return }
  startEdit(e)
}
watch([person, weekStart], () => { editing.value = null; editBase.value = null; conflict.value = null; cursor.value = null })

// ---------- People ----------
const personMenu = ref<HTMLElement | null>(null)
const personOptions = computed<PickOption[]>(() => [
  { value: me.value, label: 'You', note: business.nameOf(me.value), icon: 'user' },
  ...business.timeKeepers.filter(p => p.id !== me.value).map(p => ({ value: p.id, label: p.name, icon: (p.kind === 'agent' ? 'agent' : 'user') as 'agent' | 'user', hint: p.kind === 'agent' ? 'agent' : undefined })),
])
function choosePerson(option: PickOption) { personMenu.value = null; go({ person: option.value === me.value ? undefined : option.value }) }

// ---------- Approvals ----------
const allPeriods = ref<TimePeriod[]>([])
const periodEntries = ref(new Map<string, TimeEntry[]>())
const approvalsState = ref<'loading' | 'ready' | 'error'>('loading')
const showApproved = ref(false)
async function loadApprovals() {
  approvalsState.value = 'loading'
  try {
    const list = await listPeriods(undefined, { since: PERIOD_HISTORY_START })
    const relevant = list.filter(p => p.state === 'open' || showApproved.value)
    const found = await Promise.all(relevant.map(async p => [p.id, await listEntries({ period_id: p.id })] as const))
    periodEntries.value = new Map(found)
    await resolveNodes(found.flatMap(([, list]) => list.map(e => e.node_id)))
    allPeriods.value = list
    approvalsState.value = 'ready'
  } catch { approvalsState.value = 'error' }
}
const now = Date.now()
const ready = computed(() => allPeriods.value.filter(p => p.state === 'open' && Date.parse(p.ends_at) <= now).sort((a, b) => a.ends_at.localeCompare(b.ends_at)))
const runningPeriods = computed(() => allPeriods.value.filter(p => p.state === 'open' && Date.parse(p.ends_at) > now))
const approved = computed(() => allPeriods.value.filter(p => p.state === 'approved').sort((a, b) => b.ends_at.localeCompare(a.ends_at)).slice(0, 20))
const periodId = computed(() => typeof route.query.period === 'string' ? route.query.period : '')
const openPeriod = computed(() => allPeriods.value.find(p => p.id === periodId.value) ?? periods.value.find(p => p.id === periodId.value) ?? null)
const openEntries = computed(() => periodEntries.value.get(periodId.value) ?? (openPeriod.value ? entries.value.filter(e => e.period_id === periodId.value) : []))
const panelLoading = ref(false)
watch(periodId, async id => {
  if (!id || periodEntries.value.has(id)) return
  panelLoading.value = true
  try { const list = await listEntries({ period_id: id }); periodEntries.value = new Map(periodEntries.value).set(id, list); await resolveNodes(list.map(e => e.node_id)) }
  finally { panelLoading.value = false }
})
function summaryOf(period: TimePeriod) {
  const list = periodEntries.value.get(period.id) ?? []
  const by = new Map<string, string[]>()
  for (const e of list) by.set(e.currency, [...(by.get(e.currency) ?? []), e.amount])
  return { count: list.length, seconds: list.reduce((s, e) => s + e.duration_seconds, 0), amounts: [...by.entries()].map(([currency, a]) => ({ currency, amount: sumAmounts(a) })) }
}
function approvedPeriod(period: TimePeriod) {
  allPeriods.value = allPeriods.value.map(p => p.id === period.id ? period : p)
  periods.value = periods.value.map(p => p.id === period.id ? period : p)
  if (view.value === 'week') void loadWeek()
}
function openReview(period: TimePeriod) { go({ period: period.id }) }
function closeReview() { go({ period: undefined }) }

// ---------- Summary ----------
const summary = computed(() => view.value === 'approvals'
  ? approvalsState.value === 'ready' ? (ready.value.length ? `${plural(ready.value.length, 'period')} waiting for approval` : 'Nothing waits for approval') : ''
  : weekState.value === 'ready' ? `Week ${isoWeek(weekStart.value)} · ${weekLabel(weekStart.value)} · ${total.value ? `${formatSpan(total.value)} logged` : 'nothing logged'}` : '')

// ---------- Keyboard and commands ----------
function typing(target: EventTarget | null) { return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)) }
function keydown(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || typing(event.target)) return
  if (document.querySelector('dialog[open], .floating')) return
  if (event.key === 'Escape' && periodId.value) { event.preventDefault(); closeReview(); return }
  if (view.value !== 'week') return
  if (event.key === 'ArrowLeft' || event.key === '[') { event.preventDefault(); shiftWeek(-1) }
  else if (event.key === 'ArrowRight' || event.key === ']') { event.preventDefault(); shiftWeek(1) }
  else if (event.key === 't') { event.preventDefault(); go({ week: undefined }) }
  else if (event.key === 'l' && canLog.value) { event.preventDefault(); void openLog() }
}
async function presetFromKey(key: string) {
  try {
    const page = await listNodes({ q: key, sort: 'key', limit: 5 })
    const hit = page.items.find(item => item.key.toUpperCase() === key.toUpperCase())
    if (hit) preset.value = { id: hit.id, key: hit.key, title: hit.title }
  } catch { /* the picker stays empty */ }
}
async function presetFromNode(nodeId: string) {
  try { const n = await getNode(nodeId); preset.value = { id: n.id, key: n.key, title: n.title } } catch { /* the picker stays empty */ }
}
watch(command, value => {
  if (value?.command.name !== 'log-time') return
  const nodeId = value.command.nodeId
  consume()
  go({ view: undefined, person: undefined, week: undefined })
  if (nodeId) void presetFromNode(nodeId)
  void openLog()
}, { immediate: true })
watch(() => route.query.log, value => {
  if (!value) return
  const node = typeof route.query.node === 'string' ? route.query.node : ''
  const key = typeof route.query.ticket === 'string' ? route.query.ticket : ''
  go({ log: undefined, node: undefined, ticket: undefined })
  if (node) void presetFromNode(node)
  else if (key) void presetFromKey(key)
  setTimeout(() => void openLog(), 400)
}, { immediate: true })

watch([person, weekStart], () => { if (business.open.hours) void loadWeek() })
watch(view, value => { if (value === 'approvals') void loadApprovals(); else void loadWeek() })
watch(showApproved, () => { if (view.value === 'approvals') void loadApprovals() })
onMounted(async () => {
  window.addEventListener('keydown', keydown)
  await business.loadPlugins()
  if (!business.open.hours) return
  void business.loadCostUnits(); void business.loadPrincipals(); void projects.load()
  if (view.value === 'approvals') void loadApprovals()
  else void loadWeek()
  if (business.admin && view.value !== 'approvals') void listPeriods(undefined, { since: PERIOD_HISTORY_START }).then(list => { allPeriods.value = list }).catch(() => undefined)
})
onBeforeUnmount(() => { window.removeEventListener('keydown', keydown); phoneQuery.removeEventListener('change', onPhone) })
const waitingCount = computed(() => allPeriods.value.filter(p => p.state === 'open' && Date.parse(p.ends_at) <= now).length)
</script>

<template>
  <BusinessPage title="Hours" area="hours" :panel-open="!!openPeriod">
    <template #summary><span v-if="summary">{{ summary }}</span><span v-else class="skeleton summary-skeleton" /></template>

    <div class="toolbar">
      <div v-if="business.admin" class="seg view-seg" role="radiogroup" aria-label="View">
        <button type="button" role="radio" :aria-checked="view === 'week'" @click="go({ view: undefined, period: undefined })"><AppIcon name="calendar" :size="13" />Week</button>
        <button type="button" role="radio" :aria-checked="view === 'approvals'" @click="go({ view: 'approvals', period: undefined })"><AppIcon name="seal" :size="13" />Approvals<span v-if="waitingCount" class="badge">{{ waitingCount }}</span></button>
      </div>
      <template v-if="view === 'week'">
        <button v-if="business.admin" type="button" class="btn sm person-btn" aria-haspopup="dialog" :aria-expanded="!!personMenu" :aria-label="`Hours of ${personName}. Choose a person or agent`" @click="personMenu = personMenu ? null : ($event.currentTarget as HTMLElement)">
          <AppIcon :name="isAgent ? 'agent' : 'user'" :size="13" />{{ personName }}<AppIcon name="chevron" :size="12" class="chev" />
        </button>
        <div class="week-nav" role="group" aria-label="Week">
          <button type="button" class="icon-btn sm" aria-label="Previous week" aria-keyshortcuts="ArrowLeft" data-tip="Previous week · Left arrow" @click="shiftWeek(-1)"><AppIcon name="chevron-left" :size="14" /></button>
          <span class="week-label"><b>Week {{ isoWeek(weekStart) }}</b><span>{{ weekLabel(weekStart) }}</span></span>
          <button type="button" class="icon-btn sm" aria-label="Next week" aria-keyshortcuts="ArrowRight" data-tip="Next week · Right arrow" @click="shiftWeek(1)"><AppIcon name="chevron-right" :size="14" /></button>
          <button v-if="!thisWeek" type="button" class="btn sm ghost" aria-keyshortcuts="t" @click="go({ week: undefined })">This week</button>
        </div>
      </template>
      <label v-else class="switch approved-switch"><input v-model="showApproved" type="checkbox" /><span>Show approved</span></label>
    </div>

    <!-- ---------- Week ---------- -->
    <template v-if="view === 'week'">
      <section class="card glass-card week-card" aria-label="Week">
        <div class="period-strip" :class="{ ok: closed }">
          <template v-if="weekState !== 'ready'"><span class="skeleton strip-skel" /></template>
          <template v-else-if="!weekPeriods.length">
            <AppIcon name="calendar" :size="14" /><span>No period for this week yet. {{ canLog ? 'The first entry opens one, Monday to Sunday.' : '' }}</span>
          </template>
          <template v-else>
            <template v-for="p in weekPeriods" :key="p.id">
              <span class="period-chip" :class="p.state"><AppIcon :name="p.state === 'approved' ? 'lock' : 'calendar'" :size="12" />{{ periodLabel(p.starts_at, p.ends_at) }}</span>
              <span v-if="p.state === 'approved'" class="strip-text">Approved{{ p.approval ? ` by ${business.nameOf(p.approval.approved_by_principal_id)}` : '' }}. Closed for new entries and corrections.</span>
              <span v-else class="strip-text">Open · waiting for an admin’s approval once the week is done.</span>
              <button v-if="business.admin" type="button" class="link-btn" @click="go({ period: p.id })">{{ p.state === 'approved' ? 'View' : 'Review' }}</button>
            </template>
          </template>
        </div>

        <div v-if="canLog && (logOpen || !phone)" class="log-wrap"><LogTimeBar ref="logBar" :days="days" :suggestions="suggestions" :busy="logging" :preset="preset" :default-start-minutes="stackAfter" :quiet="!!editing" @log="log" /></div>
        <div v-else-if="canLog" class="log-closed"><button type="button" class="btn log-open" @click="openLog"><AppIcon name="plus" :size="14" />Log time</button></div>
        <p v-else-if="isAgent" class="agent-note"><AppIcon name="agent" :size="14" />An agent’s time comes from its finished runs, one entry per run.</p>

        <div v-if="weekState === 'loading'" class="grid-skeleton" aria-hidden="true"><span v-for="i in 4" :key="i" class="skeleton" /></div>
        <p v-else-if="weekState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />{{ weekError }} <button type="button" class="btn sm" @click="loadWeek">Try again</button></p>
        <div v-else class="grid-scroll" tabindex="0" role="region" aria-label="Weekly hours, scroll for all days" @keydown.left.stop @keydown.right.stop>
          <table class="week-grid" aria-label="Hours per ticket and day">
            <thead>
              <tr>
                <th scope="col" class="c-ticket">Ticket</th>
                <th v-for="(day, i) in days" :key="i" scope="col" class="c-day" :class="{ today: dayKey(day) === todayKey, weekend: i > 4 }"><span class="d-name">{{ WEEKDAYS[i] }}</span><span class="d-date">{{ day.getDate() }}</span></th>
                <th scope="col" class="c-total">Total</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in grid" :key="row.id">
                <th scope="row" class="c-ticket"><div class="t-cell">
                  <RouterLink v-if="ticketHref(row.id)" class="ticket-chip" :to="ticketHref(row.id)" :data-tip="`Open ${nodes.get(row.id)?.key}`">{{ nodes.get(row.id)?.key }}</RouterLink>
                  <span v-else class="ticket-chip plain">{{ nodes.get(row.id)?.key }}</span>
                  <span class="t-title">{{ nodes.get(row.id)?.title }}</span>
                </div></th>
                <td v-for="(cell, i) in row.cells" :key="i" class="c-day mono" :class="{ today: dayKey(days[i]) === todayKey, weekend: i > 4, empty: !cell }">{{ cell ? formatClock(cell) : '·' }}</td>
                <td class="c-total mono">{{ formatClock(row.total) }}</td>
              </tr>
              <tr v-if="!grid.length" class="empty-row"><td :colspan="9">{{ canLog ? 'Nothing logged this week. Log your first entry above.' : 'Nothing logged this week.' }}</td></tr>
            </tbody>
            <tfoot v-if="grid.length">
              <tr>
                <th scope="row" class="c-ticket">Per day</th>
                <td v-for="(seconds, i) in perDay" :key="i" class="c-day mono" :class="{ today: dayKey(days[i]) === todayKey, weekend: i > 4, empty: !seconds }">{{ seconds ? formatClock(seconds) : '·' }}</td>
                <td class="c-total mono grand">{{ formatClock(total) }}</td>
              </tr>
            </tfoot>
          </table>
        </div>
        <footer v-if="amounts.length" class="week-foot"><span class="foot-label">Billable at the day’s rates</span><MoneyText v-for="row in amounts" :key="row.currency" :amount="row.amount" :currency="row.currency" strong /></footer>
      </section>

      <section v-if="byDay.length" ref="entriesCard" class="card glass-card entries-card" aria-labelledby="entries-title">
        <header class="card-head">
          <h2 id="entries-title">Entries</h2>
          <span v-if="lockedAll" class="sub lock-note"><AppIcon name="lock" :size="12" />{{ lockedAll }}</span>
          <span v-else class="sub">Click an entry to correct it while its period is open.</span>
        </header>
        <div v-for="group in byDay" :key="group.i" class="day-group">
          <p class="day-head"><span>{{ WEEKDAYS[group.i] }} {{ group.day.getDate() }}</span><span class="mono">{{ formatClock(perDay[group.i]) }}</span></p>
          <ul class="entries" :aria-label="`${WEEKDAYS[group.i]} ${group.day.getDate()}`">
            <li
              v-for="e in group.list" :key="e.id" class="entry" :class="{ editing: editing === e.id, locked: !!lockReason(e) }" :data-entry="e.id"
              :tabindex="editing !== e.id && tabStop === e.id ? 0 : -1" :aria-describedby="editing === e.id ? undefined : `entry-help-${e.id}`"
              @keydown="rowKey($event, e)" @click="rowClick($event, e)" @focus="cursor = e.id"
            >
              <template v-if="editing === e.id && editBase">
                <div v-if="conflict" class="conflict" role="alert">
                  <AppIcon name="alert" :size="14" />
                  <p class="c-text">
                    <b>This entry changed while you were editing.</b>
                    Now <span class="mono">{{ timeOfDay(conflict.started_at) }}–{{ timeOfDay(conflict.ended_at) }}</span> · {{ keyOf(conflict) }} · {{ business.costUnit(conflict.cost_unit_node_id)?.node.title ?? 'Cost unit' }} · <span class="mono">{{ formatClock(conflict.duration_seconds) }}</span><template v-if="conflict.note"> · {{ conflict.note }}</template>.
                    Saving again applies only what you changed.
                  </p>
                  <button type="button" class="btn sm" @click="stopEdit">Use the newer entry</button>
                </div>
                <LogTimeBar :days="days" :suggestions="suggestions" :busy="saving" :entry="e" :entry-ticket="{ id: e.node_id, key: keyOf(e), title: nodes.get(e.node_id)?.title ?? '' }" :bounds="periodOf(e)" :save-label="conflict ? 'Save mine' : 'Save'" @save="save" @cancel="stopEdit" @remove="remove(e)" />
              </template>
              <template v-else>
                <span class="e-time mono" :data-tip="absoluteTime(e.started_at)">{{ timeOfDay(e.started_at) }}–{{ timeOfDay(e.ended_at) }}</span>
                <RouterLink v-if="ticketHref(e.node_id)" class="ticket-chip" :to="ticketHref(e.node_id)" tabindex="-1">{{ nodes.get(e.node_id)?.key }}</RouterLink>
                <span v-else class="ticket-chip plain">{{ nodes.get(e.node_id)?.key }}</span>
                <span class="e-text"><span class="e-title">{{ nodes.get(e.node_id)?.title }}</span><span class="e-meta"><AppIcon name="tag" :size="11" />{{ business.costUnit(e.cost_unit_node_id)?.node.title ?? 'Cost unit' }}<template v-if="e.note"> · {{ e.note }}</template></span></span>
                <span v-if="e.source === 'agent_run'" class="agent-chip">Agent run</span>
                <span class="e-dur mono">{{ formatClock(e.duration_seconds) }}</span>
                <span class="e-amount"><MoneyText :amount="e.amount" :currency="e.currency" /></span>
                <span class="e-act">
                  <span v-if="lockReason(e)" class="lock" :data-tip="lockReason(e)"><AppIcon name="lock" :size="13" /></span>
                  <template v-else>
                    <button type="button" class="icon-btn sm flat" tabindex="-1" :aria-label="`Edit ${timeOfDay(e.started_at)} on ${keyOf(e)}`" data-tip="Edit · e" @click="startEdit(e)"><AppIcon name="edit" :size="13" /></button>
                    <button type="button" class="icon-btn sm flat del" tabindex="-1" :aria-label="`Delete ${timeOfDay(e.started_at)} on ${keyOf(e)}`" data-tip="Delete · Del" @click="remove(e)"><AppIcon name="trash" :size="13" /></button>
                  </template>
                </span>
                <span :id="`entry-help-${e.id}`" class="sr-only">{{ lockReason(e) || 'Press e to edit, Delete to delete.' }}</span>
              </template>
            </li>
          </ul>
        </div>
      </section>
      <p v-if="weekState === 'ready'" class="hint"><kbd class="keycap">l</kbd> log time · <kbd class="keycap"><AppIcon name="arrow-left" /></kbd><kbd class="keycap"><AppIcon name="arrow" /></kbd> week · <kbd class="keycap">t</kbd> this week<template v-if="editable"> · <kbd class="keycap">e</kbd> edit entry · <kbd class="keycap">Del</kbd> delete</template></p>
    </template>

    <!-- ---------- Approvals ---------- -->
    <template v-else>
      <div v-if="approvalsState === 'loading'" class="card glass-card"><div class="grid-skeleton"><span v-for="i in 3" :key="i" class="skeleton" /></div></div>
      <p v-else-if="approvalsState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Periods could not be loaded. <button type="button" class="btn sm" @click="loadApprovals">Try again</button></p>
      <template v-else>
        <section v-for="group in [{ id: 'ready', title: 'Waiting for approval', list: ready, note: 'Ended, not approved yet.' }, { id: 'running', title: 'Still running', list: runningPeriods, note: 'Open for entries.' }, ...(showApproved ? [{ id: 'approved', title: 'Approved', list: approved, note: 'Closed.' }] : [])]" :key="group.id" class="card glass-card periods-card" :aria-labelledby="`${group.id}-title`">
          <header class="card-head"><h2 :id="`${group.id}-title`">{{ group.title }}</h2><span class="count mono">{{ group.list.length }}</span><span class="sub">{{ group.note }}</span></header>
          <p v-if="!group.list.length" class="empty-line">{{ group.id === 'ready' ? 'Nothing waits for approval.' : group.id === 'running' ? 'No open period is running.' : 'No approved period yet.' }}</p>
          <ul v-else class="period-rows" :aria-label="group.title">
            <li v-for="p in group.list" :key="p.id">
              <button type="button" class="period-row" :class="{ open: periodId === p.id }" @click="openReview(p)">
                <span class="p-who"><AppIcon :name="business.principals.find(x => x.id === p.principal_id)?.kind === 'agent' ? 'agent' : 'user'" :size="13" />{{ business.nameOf(p.principal_id) }}</span>
                <span class="p-when">{{ periodLabel(p.starts_at, p.ends_at) }}</span>
                <span class="p-count">{{ plural(summaryOf(p).count, 'entry', 'entries') }}</span>
                <span class="p-time mono">{{ formatSpan(p.approval?.total_seconds ?? summaryOf(p).seconds) }}</span>
                <span class="p-amount"><MoneyText v-for="a in summaryOf(p).amounts" :key="a.currency" :amount="a.amount" :currency="a.currency" /></span>
                <AppIcon name="chevron-right" :size="13" class="go" />
              </button>
            </li>
          </ul>
        </section>
      </template>
    </template>

    <PickerMenu v-if="personMenu" :anchor="personMenu" title="Hours of" :options="personOptions" :current="person" placeholder="Find a person or agent…" @choose="choosePerson" @close="personMenu = null" />
  </BusinessPage>
  <PeriodPanel v-if="openPeriod" :period="openPeriod" :entries="openEntries" :nodes="nodes" :loading="panelLoading" @close="closeReview" @approved="approvedPeriod" @reload="view === 'approvals' ? loadApprovals() : loadWeek()" />
</template>

<style scoped>
.summary-skeleton { display: inline-block; width: 260px; }
.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; min-height: 48px; margin-bottom: 12px; }
.view-seg button { gap: 6px; }
.badge { display: inline-grid; place-items: center; min-width: 17px; height: 17px; padding: 0 5px; border-radius: 999px; background: var(--gold); color: var(--gold-on); font: 700 10px/1 var(--mono); }
.person-btn { gap: 7px; }
.chev { color: var(--ink-3); }
.week-nav { display: inline-flex; align-items: center; gap: 6px; }
.week-label { display: inline-flex; align-items: baseline; gap: 8px; min-width: 190px; justify-content: center; font-size: 13px; color: var(--ink-2); }
.week-label b { color: var(--ink); font-weight: 650; }
.approved-switch { margin-left: auto; }
.card { overflow: clip; margin-bottom: 16px; container-type: inline-size; }
.period-strip { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-height: 46px; padding: 10px 18px; border-bottom: 1px solid var(--line); font-size: 12.5px; color: var(--ink-2); }
.period-strip > svg { color: var(--ink-3); }
.period-chip { display: inline-flex; align-items: center; gap: 6px; height: 22px; padding: 0 9px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-weight: 600; }
.period-chip.approved { background: var(--ok-bg); box-shadow: inset 0 0 0 1px var(--ok-line); color: var(--ok); }
.strip-text { color: var(--ink-2); }
.strip-skel { width: 280px; }
.link-btn { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.link-btn:hover { background: var(--row-hover); }
.link-btn:focus-visible { box-shadow: var(--focus-ring); }
.log-wrap { padding: 14px 18px 6px; border-bottom: 1px solid var(--line); background: var(--surface-sunken); }
.log-closed { padding: 10px 12px; border-bottom: 1px solid var(--line); }
.log-open { width: 100%; height: 44px; }
.agent-note { display: flex; align-items: center; gap: 8px; padding: 12px 18px; border-bottom: 1px solid var(--line); font-size: 13px; }
.grid-scroll { overflow-x: auto; }
.week-grid { width: 100%; min-width: 720px; border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 13px; }
.week-grid th, .week-grid td { height: 40px; padding: 0 10px; border-bottom: 1px solid var(--line); text-align: right; font-weight: 400; }
.week-grid thead th { height: 44px; font: 500 10px/1.2 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); border-bottom: 1px solid var(--line-2); font-variant-ligatures: none; }
.week-grid .c-ticket { text-align: left; padding-left: 18px; width: 38%; }
.week-grid thead .c-ticket { vertical-align: middle; }
.c-day { width: 7.5%; }
.d-name { display: block; }
.d-date { display: block; margin-top: 2px; font-size: 12px; letter-spacing: 0; color: var(--ink-2); }
.today { background: var(--aqua-wash); }
thead .today .d-name, thead .today .d-date { color: var(--teal-ink); font-weight: 700; }
.weekend:not(.today) { background: var(--surface-sunken); }
.week-grid td.empty { color: var(--ink-3); opacity: .6; }
.mono { font-family: var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.c-total { width: 80px; font-weight: 650; color: var(--ink); padding-right: 18px !important; }
tbody th.c-ticket { font-weight: 400; }
.t-cell { display: flex; align-items: center; gap: 10px; min-width: 0; }
.t-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); }
.ticket-chip { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); text-decoration: none; font-variant-ligatures: none; white-space: nowrap; }
a.ticket-chip:hover { text-decoration: underline; }
.ticket-chip:focus-visible { box-shadow: var(--focus-ring); }
/* Phones: the chip keeps its look (a finger reaches 44 px through base.css); the
   rows are tall enough that neighbouring chips' reach never overlaps. */
@media (max-width: 600px) { .ticket-chip { height: 28px; } .week-grid tbody th, .week-grid tbody td { height: 48px; } }
.ticket-chip.plain { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
tfoot th, tfoot td { border-bottom: 0 !important; font-weight: 650; color: var(--ink); }
tfoot th.c-ticket { font: 500 10px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.grand { font-size: 14px; }
.empty-row td { height: 64px; text-align: center; color: var(--ink-3); font-size: 13px; }
.week-foot { display: flex; align-items: center; justify-content: flex-end; gap: 14px; padding: 10px 18px 12px; border-top: 1px solid var(--line); font-size: 14px; }
.foot-label { font-size: 12px; color: var(--ink-3); }
.grid-skeleton { display: grid; gap: 14px; padding: 18px; }
.grid-skeleton .skeleton { height: 12px; }
.inline-error { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 12px 18px; padding: 8px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.card-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 10px; padding: 14px 18px 10px; }
.card-head h2 { font-size: 15px; font-weight: 650; }
.sub { font-size: 12.5px; color: var(--ink-3); }
.count { display: inline-grid; place-items: center; min-width: 22px; height: 20px; padding: 0 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 11px; color: var(--ink-2); }
.day-group { padding: 0 18px 10px; }
.day-head { display: flex; justify-content: space-between; padding: 8px 2px 6px; border-bottom: 1px solid var(--line-2); font-size: 12px; font-weight: 650; color: var(--ink-2); }
.entries { margin: 0; padding: 0; list-style: none; }
.entry { position: relative; display: grid; grid-template-columns: 96px max-content minmax(0, 1fr) auto 56px 130px 60px; grid-template-areas: "time chip text agent dur amount act"; align-items: center; gap: 12px; min-height: 44px; border-bottom: 1px solid var(--line); font-size: 13px; }
.entry:last-child { border-bottom: 0; }
.entry:focus { outline: none; }
.entry:focus-visible { border-radius: 8px; box-shadow: var(--focus-ring); }
.entry:not(.locked):not(.editing) { cursor: pointer; }
@media (hover: hover) { .entry:not(.editing):not(.locked):hover { background: var(--row-hover); } }
.entry > .e-time { grid-area: time; } .entry > .ticket-chip { grid-area: chip; } .entry > .e-text { grid-area: text; } .entry > .agent-chip { grid-area: agent; }
.entry > .e-dur { grid-area: dur; } .entry > .e-amount { grid-area: amount; } .entry > .e-act { grid-area: act; }
.e-act { display: inline-flex; align-items: center; justify-content: flex-end; gap: 2px; }
.e-act .icon-btn { display: inline-grid; place-items: center; opacity: 0; transition: opacity .12s; }
.entry:hover .e-act .icon-btn, .entry:focus-visible .e-act .icon-btn, .entry:focus-within .e-act .icon-btn { opacity: 1; }
@media (hover: none) { .e-act .icon-btn { opacity: 1; } }
.e-act .del:hover { color: var(--danger); }
.lock { display: inline-grid; place-items: center; width: 28px; height: 28px; color: var(--ink-3); }
.lock-note { display: inline-flex; align-items: center; gap: 6px; }
.lock-note svg { color: var(--ink-3); }
.entry.editing { display: block; margin: 0 -8px; padding: 12px 8px 8px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); }
.conflict { display: flex; align-items: flex-start; gap: 10px; margin-bottom: 12px; padding: 10px 12px; border-radius: 10px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 35%, transparent); color: var(--ink); font-size: 12.5px; }
.conflict > svg { flex-shrink: 0; margin-top: 2px; color: var(--warn-ink); }
.c-text { flex: 1; min-width: 0; line-height: 1.5; }
.c-text b { font-weight: 650; }
.e-time { font-size: 12px; color: var(--ink-2); }
.e-text { display: grid; min-width: 0; }
.e-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.e-meta { display: block; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: 12px; color: var(--ink-3); }
.e-meta svg { display: inline-block; vertical-align: -1px; margin-right: 5px; }
.agent-chip { height: 18px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 9.5px/18px var(--mono); letter-spacing: .06em; text-transform: uppercase; }
.e-dur { text-align: right; font-size: 12.5px; }
.e-amount { text-align: right; font-size: 12.5px; }
.hint { display: flex; align-items: center; justify-content: center; gap: 5px; padding: 8px 0 4px; font-size: 12px; color: var(--ink-3); }
.hint .keycap + .keycap { margin-left: 2px; }
.periods-card .card-head { padding-bottom: 8px; }
.empty-line { padding: 4px 18px 18px; font-size: 13px; color: var(--ink-3); }
.period-rows { margin: 0; padding: 0 6px 8px; list-style: none; border-top: 1px solid var(--line); }
.period-row { display: grid; grid-template-columns: minmax(150px, 1.2fr) minmax(140px, 1fr) 90px 80px minmax(120px, auto) 14px; align-items: center; gap: 12px; width: 100%; min-height: 48px; margin-top: 4px; padding: 6px 12px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
@media (hover: hover) { .period-row:hover { background: var(--row-hover); } }
.period-row.open { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.period-row:focus-visible { box-shadow: var(--focus-ring); }
.p-who { display: inline-flex; align-items: center; gap: 8px; font-weight: 650; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.p-who svg { color: var(--ink-3); flex-shrink: 0; }
.p-when, .p-count { color: var(--ink-2); font-size: 12.5px; }
.p-time { text-align: right; }
.p-amount { display: grid; justify-items: end; }
.go { color: var(--ink-3); }
@container (max-width: 760px) { .entry { grid-template-columns: 88px max-content minmax(0, 1fr) 48px 60px; grid-template-areas: "time chip text dur act"; } .entry .agent-chip, .e-amount { display: none; } }
@container (max-width: 720px) { .period-row { grid-template-columns: minmax(0, 1fr) auto 14px; grid-template-areas: "who time go" "when amount go"; row-gap: 2px; } .p-who { grid-area: who; } .p-when { grid-area: when; } .p-time { grid-area: time; } .p-amount { grid-area: amount; } .p-count { display: none; } .go { grid-area: go; } }
@media (max-width: 720px) {
  .toolbar { gap: 8px; }
  .view-seg { order: -1; }
  .week-nav { width: 100%; justify-content: space-between; }
  .week-nav .icon-btn { width: 40px; height: 40px; }
  .week-label { min-width: 0; flex-direction: column; align-items: center; gap: 0; }
  .person-btn { height: 40px; }
  .log-wrap { padding: 12px; }
  .week-grid { min-width: 420px; }
  .week-grid .c-ticket { width: 84px; padding-left: 10px; }
  .week-grid thead .c-ticket { font-size: 0; }
  .t-cell .t-title { display: none; }
  .c-day { width: auto; padding: 0 2px !important; font-size: 11.5px; }
  .d-date { font-size: 10.5px; }
  .c-total { width: 56px; padding-right: 10px !important; }
  .day-group { padding: 0 12px 8px; }
  .entry { grid-template-columns: max-content minmax(0, 1fr) 48px 28px; grid-template-areas: "time text dur act" "chip text dur act"; padding: 6px 0; }
  .entry > .ticket-chip { justify-self: start; }
  .e-act .del { display: none; }
  .entry.editing { margin: 0 -12px; padding: 12px; }
  .conflict { flex-wrap: wrap; }
  .c-text { flex: 1 1 calc(100% - 30px); }
  .conflict .btn { margin-left: 24px; }
  .hint { display: none; }
}
</style>
