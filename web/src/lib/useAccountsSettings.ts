// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1171: Settings › Accounts, Computers and Capacity are three sections over
// one set of data (AEON-1149). Each section opens this once: the reads and their
// poll, the docked panel (an account or a computer) with its menus and
// confirmations, the deep links, and what needs the person in every part. While
// one of the three is open it publishes each part's count for the Settings nav
// and the open part's count for the footer.
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from './api'
import { accountName } from './accountCascade'
import { approveAccountCapacity, type AgentAccount } from './agents'
import { confirmAction } from './confirm'
import { can, myPermissions } from './authz'
import { overviewAccounts, type OverviewAccount, type SignInReference } from './accountsOverview'
import { computerRemoval, describeComputerStatus, disconnectComputer, disconnectEnrollment, pairingPermissions, removeComputer, type PairingView } from './agentPairing'
import { machinesForAdd } from './addAccount'
import { DEFAULT_QUOTA_THRESHOLDS, isProblemSignin, quotaWindowWarns, signinStatus, windowLabel } from './accountsGlance'
import { accountsPartNeeds, matrixNeedFacts, NO_MATRIX_FACTS, partCounts, workContextLink, type AccountsPart, type MatrixNeedFacts } from './accountsNeeds'
import { PERMISSION as USE_PERMISSION, readMatrix } from './accountUse'
import { hostCapacityReason } from './hostCapacity'
import { daysSummary, HARNESS_NAME, ownOverride, poolOfRow, reserveLevel, timeLabel, when, type CapacityWindow } from './capacity'
import { DEFAULT_DAILY_POINTS, type DailyPlanFields } from './dailyLimits'
import { nextStale, pct, periodWords, quotaBar, quotaCaption, quotaInput, quotaLabel, type PlanClock } from './quotaBar'
import { validQuotaThresholds, type QuotaWarningSettings } from './quotaWarnings'
import { toast } from './toast'
import { usePoller } from './usePolledData'
import { signinKey, useVerification } from './useVerification'
import { accountsNeeds, settingsNeeds } from './footerProviders'
import { useAgents } from '../stores/agents'
import { useCapacity } from '../stores/capacity'
import { useSession } from '../stores/session'
import type { NeedsYouItem } from '../components/settings/NeedsYouList.vue'
import type { SettingsMenuItem } from '../components/settings/SettingsPopover.vue'

// The last thresholds and matrix facts per person: moving between the parts keeps
// the counts steady until the next read instead of dropping to the defaults.
const remembered: { owner: string; thresholds: QuotaWarningSettings; facts: MatrixNeedFacts } = { owner: '', thresholds: { ...DEFAULT_QUOTA_THRESHOLDS }, facts: NO_MATRIX_FACTS }

type MenuTarget = { kind: 'computer'; computer: PairingView } | { kind: 'signin'; signin: SignInReference } | { kind: 'account'; account: OverviewAccount } | { kind: 'cleanup'; computer: PairingView }

export function useAccountsSettings(part: AccountsPart) {
  const agents = useAgents(), capacity = useCapacity(), session = useSession(), route = useRoute(), router = useRouter()
  const owner = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
  const manage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
  const permissions = computed(() => pairingPermissions({ permissions: [...myPermissions()], principalKind: session.identity?.principal.kind }))
  const computers = computed(() => capacity.computers.filter(c => c.computer_id && !c.archived_at))
  const accounts = computed(() => overviewAccounts(agents.accounts, capacity.rows, computers.value))
  const machines = computed(() => machinesForAdd(computers.value))
  const add = ref(false), open = ref(false), opener = ref<HTMLElement | null>(null)
  const selected = ref<{ kind: 'account' | 'computer'; id: string } | null>(null), focusSignIn = ref('')
  const computer = computed(() => selected.value?.kind === 'computer' ? computers.value.find(c => c.computer_id === selected.value?.id) : undefined)
  // An account is selected by one of its records, so the panel follows that
  // login when confirming or stopping a shared quota regroups it.
  function accountOf(id: string) { return accounts.value.find(a => a.id === id) ?? accounts.value.find(a => a.records.some(r => r.id === id)) }
  const account = computed(() => selected.value?.kind === 'account' ? accountOf(selected.value.id) : undefined)
  const selectedKey = computed(() => `${owner.value}/${selected.value?.kind}/${selected.value?.id}`)
  const title = computed(() => computer.value?.computer_name ?? account.value?.vendor ?? 'Details')
  const known = remembered.owner === owner.value
  const thresholds = ref<QuotaWarningSettings>(known ? remembered.thresholds : { ...DEFAULT_QUOTA_THRESHOLDS })
  const busy = ref(false)
  // The section root, for focusing a sign-in row inside the open panel.
  let root: HTMLElement | null = null
  const bindRoot = (element: unknown) => { root = element instanceof HTMLElement ? element : null }
  let generation = 0
  function reset() { generation++; selected.value = null; open.value = false; add.value = false; menuOpen.value = false; renameOpen.value = false; busy.value = false; focusSignIn.value = ''; thresholds.value = { ...DEFAULT_QUOTA_THRESHOLDS }; facts.value = NO_MATRIX_FACTS; plan.value = null; planClock.value = null; planFailed.value = false; void readThresholds(); void readFacts(); void readPlan() }
  watch(owner, reset)
  watch(manage, allowed => { if (!allowed) { menuOpen.value = false; add.value = false } })
  watch(selectedKey, () => { menuOpen.value = false; renameOpen.value = false; busy.value = false })
  watch([computer, account], () => { if (selected.value && !computer.value && !account.value) open.value = false })
  function computerState(c: PairingView) { return c.computer_state === 'revoked' ? `Removed${c.local_cleanup === 'pending' ? ' · cleanup pending' : ''}` : describeComputerStatus(c).stateLabel }
  function status(c: PairingView, id: string) {
    return signinStatus(c, id, capacity.rows.find(row => row.id === id), agents.now)
  }
  function references(a: OverviewAccount, c: PairingView) { return a.signins.filter(s => s.computer.computer_id === c.computer_id) }
  function mayVerify(s: SignInReference) { return manage.value && s.enrollment.can_verify === true && s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && s.computer.verification_capabilities?.[s.enrollment.harness]?.supported === true }
  const allSignins = computed(() => accounts.value.flatMap(a => a.signins))
  const verification = useVerification(selectedKey, allSignins, mayVerify, () => capacity.refreshComputers())
  const verificationBusy = (s: SignInReference) => verification.pending(s)
  const verificationMessage = (s: SignInReference) => verification.message(s)
  const retained = computed(() => verification.retained.value.length)
  const vendorName = (h: string) => HARNESS_NAME[h] ?? 'Agent'
  const expired = (s: SignInReference) => s.enrollment.verification_state === 'expired' && !s.enrollment.verification_expired_ready
  // One definition of what blocks a sign-in, shared with the Agents page's status line (AEON-782).
  const problemSignins = computed(() => accounts.value.flatMap(a => a.signins.filter(s => isProblemSignin(s) || verification.retained.value.some(x => signinKey(x) === signinKey(s))).map(s => ({ account: a, signin: s }))))

  // ---------- Needs you, per part (AEON-1149 spec c) ----------
  const facts = ref<MatrixNeedFacts>(known ? remembered.facts : NO_MATRIX_FACTS)
  watch([thresholds, facts], ([t, f]) => { Object.assign(remembered, { owner: owner.value, thresholds: t, facts: f }) }, { immediate: true })
  const choosesWhere = computed(() => session.identity?.principal.kind === 'person' && can(USE_PERMISSION))
  const needs = computed<Record<AccountsPart, NeedsYouItem[]>>(() => {
    const signins: NeedsYouItem[] = problemSignins.value.map(({ account: a, signin: s }) => ({ id: `verify:${s.computer.computer_id}:${s.enrollment.account_id}`, name: `${a.vendor} verification on ${s.computer.computer_name}`, detail: verification.message(s) || (expired(s) ? 'Verification expired. New agents wait until the sign-in passes again.' : status(s.computer, s.enrollment.account_id)), count: isProblemSignin(s) ? 1 : 0, icon: 'alert', action: { label: manage.value && mayVerify(s) ? 'Verify again' : 'Details', fixesProblem: manage.value && mayVerify(s), disabled: verificationBusy(s) || busy.value, busy: verificationBusy(s) } }))
    for (const c of computers.value.filter(c => c.computer_state === 'revoked' && c.local_cleanup === 'pending')) signins.push({ id: `cleanup:${c.computer_id}`, name: `${c.computer_name} removed · cleanup pending`, detail: 'New work is blocked. Local sign-ins are deleted when the computer comes back online.', count: 1, icon: 'shield', tone: 'waiting', action: { label: manage.value ? 'Finish cleanup…' : 'Details' } })
    return {
      accounts: accountsPartNeeds({ accounts: accounts.value, thresholds: thresholds.value, now: agents.now, facts: facts.value, chooses: !!choosesWhere.value, vendorOf: vendorName }),
      computers: signins,
      // The plan card is Capacity's only item; it shows in its own form at the top of Capacity.
      capacity: planCard.value ? [{ id: 'plan', name: planCard.value.title, detail: planCard.value.text, count: 1 }] : [],
    }
  })
  const counts = computed(() => partCounts(needs.value))

  // ---------- The quota bar (AEON-1172): one picture of every limit, from the viewer's plan and the readings ----------
  // Today's limit, the floor and Boost come with the viewer's own plan (GET /agents/plan,
  // daily_state); accounts outside it show their reading only, never a guessed limit.
  // A failed read keeps the last plan but says so, and its doors go stale on time
  // (freshUntil, from each reading's own age): the bars never promise room the server would refuse.
  const plan = ref<DailyPlanFields | null>(null), planClock = ref<PlanClock | null>(null), planFailed = ref(false)
  let planRead: AbortController | null = null
  async function readPlan() {
    planRead?.abort()
    const identity = owner.value
    if (session.identity?.principal.kind !== 'person') { plan.value = null; planClock.value = null; planFailed.value = false; return }
    const controller = new AbortController(); planRead = controller
    try {
      const asked = Date.now()
      const response = await api('/agents/plan', { signal: controller.signal })
      if (!response.ok) throw new Error('plan')
      const body = await response.json() as DailyPlanFields
      if (planRead === controller && identity === owner.value) { plan.value = body; planClock.value = { asked, server: Date.parse(response.headers.get('Date') ?? '') }; planFailed.value = false }
    } catch { if (planRead === controller && !controller.signal.aborted && identity === owner.value) planFailed.value = true }
    finally { if (planRead === controller) planRead = null }
  }
  // The clock moves when the first fresh door goes stale too, not only with the next read.
  let staleTimer: ReturnType<typeof setTimeout> | undefined
  watch(() => nextStale(plan.value, planClock.value, agents.now), at => { clearTimeout(staleTimer); if (at !== null) staleTimer = setTimeout(() => agents.tick(), Math.max(0, at - Date.now())) }, { immediate: true })
  onBeforeUnmount(() => clearTimeout(staleTimer))
  /** The long window is the daily one; a 5-hour reading alone still beats "not reported". An estimate is no reading. */
  function longWindow(a: OverviewAccount) {
    const read = a.windows.filter(w => w.reading.source !== 'estimate')
    return read.find(w => w.reading.window_kind !== '5h' && !w.reading.bucket) ?? read.find(w => w.reading.window_kind !== '5h') ?? read[0] ?? null
  }
  const allowed = (r: AgentAccount) => r.state === 'available' && r.ongoing_use_approved !== false
  function quotaOf(a: OverviewAccount) {
    const ids = new Set(a.records.map(r => r.id))
    const daily = plan.value?.daily_state?.[a.harness]?.accounts.find(d => ids.has(d.account_id)) ?? null
    const window = longWindow(a), record = a.records.find(r => r.id === daily?.account_id) ?? a.records[0]
    const settings = plan.value?.daily?.[a.harness]
    const boost = !!settings?.boost_today && Date.parse(settings.boost_today.until) > agents.now
    const bar = quotaBar(quotaInput({ daily, window, boost, rule: record ? capacity.byAccount.get(record.id)?.limit : null, override: poolOf(a)?.override ?? '', off: !a.records.some(allowed), now: agents.now, clock: planClock.value, dayEnds: plan.value?.daily_until }))
    const period = periodWords(window)
    const low = a.windows.some(w => quotaWindowWarns(w, thresholds.value, agents.now))
    const problem = problemSignins.value.find(p => p.account.id === a.id && isProblemSignin(p.signin))
    let caption = quotaCaption(bar, { period, resetsAt: daily?.resets_at ?? window?.reading.resets_at, now: agents.now, timezone: capacity.timezone, low })
    if (problem) caption = { text: `${caption.text.split(' · ')[0]} · needs verifying on ${problem.signin.computer.computer_name}`, tone: 'warn', icon: 'alert' }
    return { bar, caption, label: quotaLabel(bar, `${a.vendor} · ${a.identity}`, period), atLimit: settings?.at_limit, window, daily }
  }
  const quotas = computed(() => new Map(accounts.value.map(a => [a.id, quotaOf(a)])))
  const quota = (a: OverviewAccount) => quotas.value.get(a.id) ?? quotaOf(a)
  /** The panel's Daily limit row: the harness's pace from the viewer's plan; empty outside it. */
  function paceWords(a: OverviewAccount) {
    const d = plan.value?.daily?.[a.harness]
    if (!d || !quota(a).daily || !quota(a).bar.known) return ''
    const def = plan.value?.daily_default_points ?? DEFAULT_DAILY_POINTS
    const base = d.pace.mode === 'everything' ? 'Use everything before the reset' : `+${d.pace.points_per_day ?? def} percentage points a day${d.pace.points_per_day == null ? ', the default' : ''}`
    const boost = d.boost_today && Date.parse(d.boost_today.until) > agents.now ? `. Boost today: up to ${pct(d.boost_today.limit_used_pct)} % used, until midnight` : ''
    return `${base}${boost}. The same for every ${a.vendor} account; set in Capacity.`
  }
  function computerSummary(c: PairingView) {
    if (c.computer_state === 'revoked') return computerState(c)
    if (c.computer_state === 'draining') return 'New starts blocked · running agents finish'
    const p = problemSignins.value.find(p => p.signin.computer.computer_id === c.computer_id && isProblemSignin(p.signin))
    return p ? `${p.account.vendor} needs verifying` : c.host_capacity?.reason ? `Waiting: ${hostCapacityReason(c.host_capacity.reason)}` : 'No sign-in needs attention'
  }
  /** "Everything else is working" only while every report is fresh and every sign-in is ready. */
  const allReady = computed(() => capacity.loaded && capacity.computersLoaded && !capacity.stale && !capacity.computersStale && accounts.value.every(a => a.signins.every(s => status(s.computer, s.enrollment.account_id) === 'Ready')))

  async function show(kind: 'account' | 'computer', id: string, event?: Event, signin?: string) {
    if (event?.currentTarget instanceof HTMLElement && !open.value) opener.value = event.currentTarget
    selected.value = { kind, id: kind === 'account' ? accountOf(id)?.records[0]?.id ?? id : id }; focusSignIn.value = signin ?? ''; open.value = true
    await nextTick()
    if (signin) { const target = root?.querySelector<HTMLElement>(`.pane [data-signin="${signin}"]`); target?.focus({ preventScroll: true }); target?.scrollIntoView({ block: 'nearest' }) }
  }
  async function actAttention(id: string) {
    const p = problemSignins.value.find(p => id === `verify:${p.signin.computer.computer_id}:${p.signin.enrollment.account_id}`)
    if (p) { if (mayVerify(p.signin)) await verify(p.signin); else await show('computer', p.signin.computer.computer_id!, undefined, p.signin.enrollment.account_id); return }
    if (id.startsWith('cleanup:')) { await show('computer', id.slice(8)); return }
    if (id.startsWith('quota:')) { await show('account', id.slice(6)); return }
    if (id.startsWith('new:')) {
      // Owners and admins tick where it may work; others read the account.
      if (!choosesWhere.value) { if (accountOf(id.slice(4))) await show('account', id.slice(4)); return }
      // The matrix opens For new things and brings the account's row to the person (AEON-1173).
      if (revealWhere) { revealWhere(id.slice(4)); return }
      document.getElementById('account-use')?.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
      return
    }
    const first = facts.value.holding.first[0]
    if (id === 'holding' && first) void router.push(workContextLink(first))
  }

  // ---------- Reads: accounts, capacity and computers every 20 s; thresholds and matrix facts with them ----------
  async function refresh() {
    void readFacts()
    const planned = readPlan()
    try {
      await agents.refreshAccounts(); await capacity.load()
      const result = await capacity.refreshComputers()
      if (!result.ok && capacity.computersLoaded) toast('Computer status could not refresh. The previous report stays visible.', { tone: 'error' })
    } finally {
      // The clock moves with every read, even when reads fail, so an old plan goes stale on time.
      await planned; agents.tick()
    }
  }
  let thresholdTurn = 0
  async function readThresholds() {
    const turn = ++thresholdTurn, identity = owner.value
    if (!session.identity || !can('account.read')) return
    try {
      const response = await api('/settings/quota-warnings')
      if (!response.ok) return
      const body: QuotaWarningSettings = await response.json()
      if (turn === thresholdTurn && identity === owner.value && validQuotaThresholds(body)) thresholds.value = body
    } catch { /* the defaults keep the early and urgent lines the server starts with */ }
  }
  // Only people who may choose where accounts work can read the matrix; for others nothing waits there.
  let factsRead: AbortController | null = null
  async function readFacts() {
    factsRead?.abort()
    const identity = owner.value
    if (!choosesWhere.value) { facts.value = NO_MATRIX_FACTS; return }
    const controller = new AbortController(); factsRead = controller
    const timer = setTimeout(() => controller.abort(), 10_000)
    try {
      const page = await readMatrix(controller.signal)
      if (factsRead === controller && identity === owner.value) facts.value = matrixNeedFacts(page)
    } catch { /* a failed read keeps the last facts; the matrix itself reports its own failure */ }
    finally { clearTimeout(timer); if (factsRead === controller) factsRead = null }
  }
  watch(choosesWhere, () => { void readFacts() })
  const poller = usePoller(refresh, 20_000)
  // Returning to the window reads again at once (an account added in a terminal
  // shows up); the poller coalesces it with a read already in flight.
  function onFocus() { poller.tick(true); void readThresholds() }
  onMounted(() => { poller.start(true); void readThresholds(); window.addEventListener('focus', onFocus) })
  // The nav counts stay while the next part opens; Settings clears them when it closes or the person changes.
  onBeforeUnmount(() => { generation++; thresholdTurn++; factsRead?.abort(); planRead?.abort(); poller.stop(); window.removeEventListener('focus', onFocus); settingsNeeds.value = 0 })

  // ---------- Deep links ----------
  watch(() => route.hash, hash => {
    if (part === 'accounts' && hash === '#add-account' && manage.value) { add.value = true; void router.replace({ path: route.path, query: route.query, hash: '' }) }
  }, { immediate: true })
  // An approval link (?verify_account=<account id>) opens the computer that holds the sign-in.
  function openVerify(id: unknown) {
    const s = typeof id === 'string' ? allSignins.value.find(s => s.enrollment.account_id === id) : undefined
    if (s) void show('computer', s.computer.computer_id!, undefined, id as string)
  }
  watch(() => route.query.verify_account, openVerify, { immediate: true })
  watch(accounts, () => { if (!open.value) openVerify(route.query.verify_account) })
  // The Agents page's status line, Needs you rows and grid open a panel here (AEON-782):
  // ?computer=<id>[&signin=<account id>] or ?account=<id>. The link is spent once it opened.
  function openFromLink() {
    const { computer: c, account: a, signin: sg, ...rest } = route.query
    const computerId = typeof c === 'string' ? c : '', accountId = typeof a === 'string' ? a : ''
    if (!computerId && !accountId) return
    const found = computerId ? computers.value.some(x => x.computer_id === computerId) : !!accountOf(accountId)
    if (!found && !(capacity.loaded && capacity.computersLoaded)) return
    void router.replace({ path: route.path, query: rest, hash: route.hash })
    if (!found) return
    if (computerId) void show('computer', computerId, undefined, typeof sg === 'string' ? sg : undefined)
    else void show('account', accountOf(accountId)!.id)
  }
  watch([() => route.query.computer, () => route.query.account, accounts, computers, () => capacity.loaded, () => capacity.computersLoaded], openFromLink, { immediate: true })

  // ---------- Capacity: the pacing editors and the one-time plan card ----------
  // Capacity registers how its pacing editors take focus; elsewhere the editors are one part away.
  let revealPacing: (() => void) | null = null
  const bindPacing = (reveal: (() => void) | null) => { revealPacing = reveal }
  // Accounts registers how "Choose where" reaches an account's row in Where accounts may work.
  let revealWhere: ((accountId: string) => void) | null = null
  const bindWhere = (reveal: ((accountId: string) => void) | null) => { revealWhere = reveal }
  /** "I'm away…" from a learned suggestion: the panel closes and the pacing editors take focus. */
  async function goPacing() {
    open.value = false
    if (part === 'capacity' && revealPacing) { await nextTick(); revealPacing(); return }
    void router.push({ path: '/settings/capacity', hash: '#when' })
  }
  const cardClosed = ref(false), planBusy = ref(false)
  watch(owner, () => { cardClosed.value = false; planBusy.value = false })
  const planCard = computed(() => {
    if (!manage.value || cardClosed.value || !capacity.loaded || !capacity.schedulesLoaded || !capacity.pools.length || capacity.reserveConfirmed) return null
    const s = capacity.schedule
    const on = s.week.filter(d => d.on)
    const hours = on.length && on.every(d => d.start === on[0].start && d.end === on[0].end) ? `${timeLabel(on[0].start)}–${timeLabel(on[0].end)}` : 'in your hours'
    const level = reserveLevel(s)
    return capacity.hasUserSchedule
      ? { title: 'New:', text: `agents now leave you room while you work, about ${level}% of every limit.`, secondary: 'Turn off', primary: 'Keep' }
      : { title: "Here's the plan.", text: `Agents work alongside you ${daysSummary(s.week)}, ${hours}, pace each account to its reset, and leave you about ${level}% of every limit while you work.`, secondary: 'Change', primary: 'Looks right' }
  })
  async function confirmPlan(mode: 'auto' | 'off') {
    const identity = owner.value
    if (planBusy.value || !manage.value) return
    planBusy.value = true
    try {
      await capacity.confirmReserve(mode)
      if (identity === owner.value) toast(mode === 'auto' ? 'Saved. Agents leave you room while you work.' : 'Turned off. Agents may use everything.')
    } catch (e) { if (identity === owner.value) toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
    finally { if (identity === owner.value) planBusy.value = false }
  }
  function cardSecondary() {
    if (capacity.hasUserSchedule) { void confirmPlan('off'); return }
    cardClosed.value = true
    revealPacing?.()
  }

  async function verify(s: SignInReference) { if (!busy.value) await verification.request(s) }

  // ---------- The shared popover binds every confirmation to its original record and revision ----------
  function cleanupRemoval(c: PairingView) {
    const removal = computerRemoval(c, permissions.value)
    if (!removal.allowed) return removal
    if (c.accounting_state !== 'settled' || c.enrollments.some(e => e.accounting_state !== 'settled')) return { allowed: false, reason: 'Run accounting is not confirmed yet.' }
    return removal
  }
  const renameOpen = ref(false), renameName = ref('')
  const menuOpen = ref(false), menuAnchor = ref<HTMLElement | null>(null), menuTarget = ref<MenuTarget | null>(null), menuItems = ref<SettingsMenuItem[]>([]), menuContext = ref(''), menuError = ref('')
  function currentTarget(t: MenuTarget): MenuTarget | undefined {
    if (t.kind === 'account') { const account = accounts.value.find(a => a.id === t.account.id); return account && { kind: 'account', account } }
    const computer = computers.value.find(c => c.computer_id === (t.kind === 'signin' ? t.signin.computer.computer_id : t.computer.computer_id))
    if (!computer) return undefined
    if (t.kind !== 'signin') return { kind: t.kind, computer }
    const enrollment = computer.enrollments.find(e => e.account_id === t.signin.enrollment.account_id)
    return enrollment && { kind: 'signin', signin: { computer, enrollment } }
  }
  function menuKey(t: MenuTarget) { return `${selectedKey.value}/${t.kind}/${t.kind === 'account' ? `${t.account.records.map(r => `${r.id}:${r.link_revision ?? ''}`).join(',')}|${t.account.signins.map(s => `${s.computer.computer_id}:${s.computer.revision}:${s.enrollment.account_id}`).join(',')}` : t.kind === 'signin' ? `${t.signin.computer.computer_id}:${t.signin.computer.revision}:${t.signin.enrollment.account_id}` : `${t.computer.computer_id}:${t.computer.revision}`}` }
  async function openMenu(t: MenuTarget, event: Event) {
    if (!manage.value) return
    menuTarget.value = t; menuAnchor.value = event.currentTarget as HTMLElement; menuContext.value = menuKey(t); menuError.value = ''
    if (t.kind === 'signin') {
      const s = t.signin, vendor = accounts.value.find(a => a.records.some(r => r.id === s.enrollment.account_id))?.vendor ?? s.enrollment.harness
      menuItems.value = [{ id: 'verify', label: 'Verify again', detail: `Run a short check on ${s.computer.computer_name}.`, icon: 'refresh', disabled: !mayVerify(s) || verificationBusy(s) }, { id: 'signout', label: 'Sign out here…', detail: `Only on ${s.computer.computer_name}.`, icon: 'logout', confirmation: { title: `Sign out of ${vendor} on ${s.computer.computer_name}?`, effect: 'New agents stop starting with this sign-in. This computer deletes its local sign-in when it receives the request.', keeps: 'Sign-ins on other computers and the vendor account stay untouched. Running work is not reported as stopped.', action: 'Sign out here' } }]
    } else if (t.kind === 'computer') {
      menuItems.value = [{ id: 'rename', label: 'Rename…', icon: 'edit', detail: 'Change the name shown in PAIMOS.' }, { id: 'remove', label: 'Remove computer…', detail: 'Blocks its sign-ins, even while offline.', icon: 'trash', confirmation: { title: `Remove ${t.computer.computer_name}?`, effect: 'New work stops and its sign-ins are blocked immediately. Local deletion stays pending until the computer confirms.', keeps: 'Accounts on other computers keep working. Pair this computer again to use it later.', action: 'Remove computer' } }]
    } else if (t.kind === 'cleanup') {
      menuItems.value = [{ id: 'cleanup', label: 'Finish cleanup…', disabled: !cleanupRemoval(t.computer).allowed, detail: cleanupRemoval(t.computer).reason || 'Use only after vendor sign-out or wiping the computer.', confirmation: { title: `Finish cleanup without ${t.computer.computer_name}?`, effect: 'Do this only after signing out of all devices at the vendors, or wiping the computer. PAIMOS then forgets it from this list.', keeps: 'Accounts on other computers are unaffected. Unsettled run accounting is kept and cannot be bypassed.', action: 'Finish cleanup' } }]
    } else {
      const reader = quotaReader(t.account)
      menuItems.value = [{ id: 'read', label: 'Read quota now', icon: 'refresh', disabled: !reader, detail: reader ? `Ask ${reader.computer.computer_name} for a fresh reading.` : 'Needs a verified sign-in with a quota reader.' }, { id: 'everywhere', label: 'Sign out everywhere…', icon: 'logout', disabled: !t.account.signins.some(s => s.enrollment.state !== 'revoked'), confirmation: { title: `Sign out of ${t.account.vendor} everywhere?`, effect: 'PAIMOS blocks this account on every paired computer at once. Local sign-ins are deleted as the computers reconnect.', keeps: 'Other accounts, the vendor subscription and vendor sessions outside PAIMOS are untouched.', action: 'Sign out everywhere' } }, removeItem(t.account)]
    }
    // Publish the captured record before opening. The shared popover closes on
    // context changes; changing its context and opening in one render would
    // correctly invalidate that newly opened menu.
    const context = menuContext.value
    await nextTick()
    if (manage.value && menuContext.value === context && context === menuKey(t)) menuOpen.value = true
  }
  function quotaReader(a: OverviewAccount) { return a.signins.find(s => s.enrollment.state === 'connected' && s.computer.computer_state === 'connected' && s.computer.connectivity === 'online' && status(s.computer, s.enrollment.account_id) === 'Ready' && a.records.find(r => r.id === s.enrollment.account_id)?.reading_support !== 'none') }
  async function renameComputer() {
    const t = menuTarget.value, screen = selectedKey.value, identity = owner.value
    if (t?.kind !== 'computer' || !manage.value || busy.value || menuContext.value !== menuKey(t)) return
    const name = renameName.value.trim()
    if (!name || name.length > 128) { menuError.value = 'Use a name of 1–128 characters.'; return }
    busy.value = true
    try {
      const response = await api(`/agent-pairing/computers/${t.computer.computer_id}/name`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, expected_revision: t.computer.revision }) })
      if (!response.ok) throw new Error('Computer changed or the name could not be saved. Reopen Rename.')
      if (identity !== owner.value || screen !== selectedKey.value) return
      renameOpen.value = false; toast('Computer name saved.'); await refresh()
    } catch (error) { if (identity === owner.value && screen === selectedKey.value) menuError.value = error instanceof Error ? error.message : 'Name could not be saved.' }
    finally { if (identity === owner.value && screen === selectedKey.value) busy.value = false }
  }
  async function menuAction(id: string, context: string | undefined) {
    const t = menuTarget.value, identity = owner.value, turn = generation, screen = selectedKey.value
    if (!t || busy.value || !manage.value) return
    // A record that changed since its menu opened (a poll, another tab) is said, never acted on.
    if (context !== menuContext.value || context !== menuKey(t)) { fail('This record changed. Reopen its menu.'); return }
    // The key of the record as it is now, not as captured: a regrouped account
    // (other logins), a newer computer revision or a gone sign-in all differ.
    const current = currentTarget(t)
    if (!current || menuKey(current) !== context) { fail('This record changed. Reopen its menu.'); return }
    if (t.kind === 'account' && id === 'remove-account') { await removeAccount(t.account); return }
    if (id === 'rename' && t.kind === 'computer') { renameName.value = t.computer.computer_name; await nextTick(); renameOpen.value = true; return }
    if (id === 'verify' && t.kind === 'signin') { menuOpen.value = false; await verify(t.signin); return }
    busy.value = true
    try {
      if (id === 'signout' && t.kind === 'signin') await disconnectEnrollment(t.signin.computer, t.signin.enrollment.account_id, 'revoke_now', permissions.value)
      else if (id === 'remove' && t.kind === 'computer') await disconnectComputer(t.computer, 'revoke_now', permissions.value)
      else if (id === 'cleanup' && t.kind === 'cleanup') await removeComputer(t.computer, permissions.value)
      else if (id === 'everywhere' && t.kind === 'account') {
        const targets = t.account.signins.filter(s => s.enrollment.state !== 'revoked' && s.computer.computer_state !== 'revoked').map(s => ({ computer_id: s.computer.computer_id, account_id: s.enrollment.account_id, expected_revision: s.computer.revision }))
        const response = await api('/agent-pairing/accounts/sign-out', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ targets }) })
        if (!response.ok) throw new Error('Sign-ins changed or sign-out failed. Refresh and review the account again.')
      } else if (id === 'read' && t.kind === 'account') {
        const s = quotaReader(t.account), r = s && t.account.records.find(r => r.id === s.enrollment.account_id)
        if (!r || r.link_revision === undefined) throw new Error('No verified quota reader is available.')
        const response = await api(`/agent-accounts/${r.id}/check`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ binding_revision: r.link_revision, idempotency_key: crypto.randomUUID() }) })
        if (!response.ok) throw new Error('Quota reading could not be requested. Try again later.')
      } else return
      if (identity !== owner.value || turn !== generation || screen !== selectedKey.value) return
      menuOpen.value = false
      toast(id === 'read' ? 'Quota reading requested. The computer will report the result.' : id === 'cleanup' ? 'Computer removed from the list. History stays.' : 'New work is blocked. Local cleanup remains pending until confirmed.')
      await refresh()
    } catch (error) { if (identity === owner.value && turn === generation && screen === selectedKey.value) fail(error instanceof Error ? error.message : 'The action failed.') }
    finally { if (identity === owner.value && turn === generation && screen === selectedKey.value) busy.value = false }
  }
  /** A plain menu item closes before it runs: its failure is a toast, a confirmation's stays in place. */
  function fail(message: string) { if (menuOpen.value) menuError.value = message; else toast(message, { tone: 'error' }) }

  // ---------- A Sprint or Hold set before they were retired (AEON-786 → AEON-1149) ----------
  // Nobody can set a new one; one in force shows in the account panel until it ends,
  // with End now. It belongs to the pool the account paces with (its harness or group).
  function poolOf(a: OverviewAccount) {
    const row = a.rows[0] ?? (a.records[0] ? { id: a.records[0].id, groupId: a.records[0].group_id ?? '', harness: a.records[0].harness } : undefined)
    return row ? poolOfRow(capacity.pools, row) : undefined
  }
  function heldOf(a: OverviewAccount) {
    const pool = poolOf(a), kind = ownOverride(pool)
    if (!pool || (kind !== 'hold' && kind !== 'sprint')) return null
    const until = pool.overrideUntil, alone = pool.rows.length <= 1
    return {
      key: `${pool.id}:${kind}:${until}`, kind,
      title: kind === 'sprint' ? 'Sprint until reset' : until ? `Held until ${when(until, agents.now, capacity.timezone)}` : 'Held until you end it',
      text: kind === 'sprint'
        ? `Set before Sprint was retired. Agents may use everything left on ${alone ? 'this account' : pool.name} until it resets; then the daily limit paces it again.`
        : `Set before Hold was retired. Agents leave ${alone ? 'this account' : pool.name} alone until then; “Agents may use it” replaces Hold.`,
    }
  }
  /** End now removes the override with the same schedule write; the shown hold must still be the one in force. True once it ended. */
  async function endHeld(a: OverviewAccount, seen: string): Promise<boolean> {
    const identity = owner.value, turn = generation, pool = poolOf(a), held = heldOf(a)
    if (!manage.value || busy.value) return false
    if (!pool || !held || held.key !== seen) { toast('This hold changed. The panel shows it as it is now.', { tone: 'error' }); return false }
    busy.value = true
    try {
      await capacity.setPoolOverride(pool.id, '')
      if (identity !== owner.value || turn !== generation) return false
      toast(held.kind === 'sprint' ? `${pool.name}: Sprint ended · the daily limit paces it again` : `${pool.name}: hold ended · agents may use it again`)
      return true
    } catch (error) { if (identity === owner.value && turn === generation) toast(error instanceof Error ? error.message : 'That did not save. Please try again.', { tone: 'error' }); return false }
    finally { if (identity === owner.value && turn === generation) busy.value = false }
  }

  // ---------- Remove an account (AEON-402, moved here in AEON-786): named, history stays ----------
  function removeItem(a: OverviewAccount): SettingsMenuItem {
    const ids = new Set(a.records.map(r => r.id))
    const queued = Object.values(agents.runs).filter(r => r.status === 'queued' && (ids.has(r.account_id ?? '') || ids.has(r.requested_account_id ?? ''))).length
    const hosts = [...new Set(a.signins.filter(s => s.enrollment.state !== 'revoked').map(s => s.computer.computer_name))]
    const effect = [
      'It leaves Accounts and agents stop using it.',
      a.records.length > 1 ? `All ${a.records.length} logins that share this quota are removed.` : '',
      hosts.length ? `Its binding on ${hosts.join(', ')} is disconnected.` : '',
      queued ? `${queued === 1 ? 'One queued run' : `${queued} queued runs`} for it ${queued === 1 ? 'is' : 'are'} cancelled.` : '',
    ].filter(Boolean).join(' ')
    return { id: 'remove-account', label: 'Remove account…', icon: 'trash', separated: true, detail: 'Agents stop using it. Its runs and history stay.', confirmation: { title: `Remove ${a.vendor} · ${a.identity}?`, effect, keeps: 'Its runs and history stay. Other accounts and the vendor subscription are untouched.', action: 'Remove account' } }
  }
  async function removeAccount(a: OverviewAccount) {
    const identity = owner.value, turn = generation, screen = selectedKey.value
    if (busy.value || !manage.value) return
    busy.value = true
    let removed = 0
    try {
      for (const record of a.records) { await agents.removeAccount(record); removed++ }
      if (identity !== owner.value || turn !== generation) return
      menuOpen.value = false
      toast(`Removed ${a.vendor} · ${a.identity}. Its runs and history stay.`)
    } catch (error) {
      if (identity !== owner.value || turn !== generation) return
      const reason = error instanceof Error && /still working/i.test(error.message) ? `A run is still working on ${a.identity}. Remove it once that run ends.` : error instanceof Error ? error.message : 'The account was not removed. Please try again.'
      // Partial removals are said as such; the list refreshes to what the server has.
      fail(removed ? `Removed ${removed} of ${a.records.length} logins. ${reason}` : reason)
    } finally { if (identity === owner.value && turn === generation && screen === selectedKey.value) busy.value = false }
  }

  // Pause, resume and first approval stay with each login (AccountsCard). Resume
  // approves ongoing use first, as the Agents desk does.
  async function setAccount(record: AgentAccount, state: AgentAccount['state']) {
    const identity = owner.value
    if (!manage.value) return
    if (state === 'draining') {
      const ok = await confirmAction({ title: `Drain ${accountName(record)}?`, body: 'Running work finishes; no new runs start on this account until you resume it.', confirmLabel: 'Drain account' })
      if (!ok || identity !== owner.value || !manage.value) return
    }
    if (state === 'available') await approveAccountCapacity(record.id)
    await agents.setAccount(record, state)
  }
  // The nav counts every part; the footer says the open part's count (AEON-785).
  watch(counts, now => { accountsNeeds.value = now; settingsNeeds.value = now[part] }, { immediate: true })
  const primarySignin = computed(() => computer.value ? problemSignins.value.find(p => p.signin.computer.computer_id === computer.value?.computer_id)?.signin : account.value ? problemSignins.value.find(p => p.account.id === account.value?.id)?.signin : undefined)

  return reactive({
    part, agents, capacity, owner, manage, computers, accounts, machines, add, open, opener, computer, account, selectedKey, title, thresholds, busy, focusSignIn,
    bindRoot, computerState, status, references, mayVerify, verificationBusy, verificationMessage, retained, vendorName, expired,
    needs, counts, allReady, quota, paceWords, planFailed, heldOf, endHeld, computerSummary, windowLabel: (w: CapacityWindow) => windowLabel(w),
    show, actAttention, refresh, verify, goPacing, bindPacing, bindWhere, planCard, planBusy, confirmPlan, cardSecondary,
    cleanupRemoval, renameOpen, renameName, menuOpen, menuAnchor, menuItems, menuContext, menuError, openMenu, renameComputer, menuAction,
    setAccount, primarySignin,
  })
}
export type AccountsSettings = ReturnType<typeof useAccountsSettings>
