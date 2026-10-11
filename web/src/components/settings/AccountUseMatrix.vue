<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1054: Settings › Accounts › "Where accounts may work" (concept AEON-1044
// §2.5). Rows are accounts, columns are work contexts; a tick allows one account
// to work on the projects of one context. "For new things" folds the three rules
// for what new things get (AEON-1173, design AEON-1149); New model versions shows
// read-only there, because Auto-update in Models is its editor. The corner, row and
// column boxes save in bulk. Every save carries the matrix revision it was made on;
// the server answers 409 for a competing change and nothing is saved, so the matrix
// is read again. Undo, in the save's message or with U, sends the inverse of the
// last save with the revision that save returned. Running work outside the matrix
// keeps running.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { HARNESS_LABEL } from '../../lib/agentState'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import { vClipTip } from '../../directives/clipTip'
import {
  allowedSet, applyCells, billingLabel, cellKey, columnState, confirmMatrix, contextNames, createContext, failureText, modelRuleLabel, MODELS_LINK, named,
  nextFromTri, PERMISSION, readMatrix, rowState, ruleLabel, RULES, rulesPreview, saveCells, saveRules, tickable, triState, updateContext,
  type Bulk, type CellResult, type Matrix, type RuleDef, type RuleKey, type Rules, type UseCell, type WorkContext,
} from '../../lib/accountUse'
import { matrixNeedFacts, workContextLink } from '../../lib/accountsNeeds'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

const session = useSession(), agents = useAgents()
const person = computed(() => session.identity?.principal.kind === 'person')
const allowed = computed(() => person.value && can(PERMISSION))
const scope = useIdentityScope(() => allowed.value)
const reads = scope.lane()

const matrix = ref<Matrix | null>(null), set = ref<Set<string>>(new Set()), state = ref<'loading' | 'ready' | 'error'>('loading')
const busy = ref(''), said = ref(''), confirmedHere = ref(false)
/** The last save of this person on this screen, a cell set or one rule: Undo is valid only while nothing changed since. */
type UndoStep = { cells: UseCell[] } | { rule: RuleKey; value: string }
const last = ref<{ owner: string; revision: number; undo: UndoStep; what: string } | null>(null)
/** A context action belongs to the context and matrix revision on screen when the menu or form opened; a later change answers 409. */
const menu = ref<{ context: WorkContext; revision: number; anchor: HTMLElement } | null>(null)
const form = ref<{ mode: 'add' | 'rename'; context?: WorkContext; revision: number; anchor: HTMLElement; name: string; error: string } | null>(null)
/** For new things starts folded; it grows downward and nothing above it moves. */
const rulesOpen = ref(false), rulesMoving = ref(false), rulesInstant = ref(false)
/** The row "Choose where" brought to the person, tinted for a moment. */
const flash = ref(''), pendingChoose = ref('')
let flashTimer: ReturnType<typeof setTimeout> | undefined

const columns = computed(() => matrix.value ? tickable(matrix.value.contexts) : [])
const rows = computed(() => matrix.value?.accounts ?? [])
const holding = computed(() => matrix.value?.contexts.find(c => c.kind === 'holding'))
const owners = computed(() => new Map(agents.accounts.map(a => [a.id, a.owner_person_name || ''])))
const revision = computed(() => matrix.value?.rules.revision ?? 0)
const canUndo = computed(() => !!last.value && last.value.owner === scope.owner.value && last.value.revision === revision.value && !busy.value)
const truncated = computed(() => !!matrix.value && (!!matrix.value.next_account || !!matrix.value.next_context))
const outside = computed(() => (matrix.value?.running_outside ?? []).map(run => ({ ...run, account: rows.value.find(a => a.id === run.account_id) })))
const names = computed(() => contextNames(matrix.value?.contexts ?? []))
const preview = computed(() => matrix.value ? rulesPreview(matrix.value.rules, names.value) : '')
const defaultContext = computed(() => matrix.value?.contexts.find(c => c.kind === 'default'))
const waiting = computed(() => matrixNeedFacts(matrix.value).holding)
/** The corner box: every shown account in every shown context. Its save covers all of them, shown or not. */
const allState = computed(() => triState(rows.value.flatMap(a => columns.value.map(c => cellKey(a.id, c.id))), set.value))

function load(quiet = false, report = !quiet) {
  if (!scope.owner.value) { matrix.value = null; return }
  if (!quiet) state.value = matrix.value ? 'ready' : 'loading'
  const floor = revision.value
  void reads.run(({ after, signal }) => after(readMatrix(signal), page => {
    // A read that started before our own save can answer after it; never step back.
    if (page.rules.revision < floor && matrix.value) return
    matrix.value = page; set.value = allowedSet(page.cells); state.value = 'ready'
    if (pendingChoose.value) { const id = pendingChoose.value; pendingChoose.value = ''; void nextTick(() => choose(id)) }
  }), {
    failed: () => {
      if (!matrix.value) { state.value = 'error'; return }
      if (!report) return
      said.value = 'The matrix could not be read again. The last state read stays visible and may be out of date.'
      toast(said.value, { tone: 'error' })
    },
  })
}
function sub(account: Matrix['accounts'][number]) {
  return [account.plan || 'Plan not reported', billingLabel(account.billing_mode), owners.value.get(account.id) ? `Owner ${owners.value.get(account.id)}` : 'No owner linked'].join(' · ')
}
const accountName = (a: { label: string; harness: string }) => a.label || HARNESS_LABEL[a.harness] || a.harness

/** One write at a time. The owner and revision are captured when the person acts; a later answer for another owner is dropped by the scope. */
function write<T>(what: string, send: (expected: number, signal: AbortSignal) => Promise<T>, done: (result: T) => void, expected?: number) {
  const current = matrix.value
  if (!current || busy.value) return false
  busy.value = what; reads.cancel()
  void scope.run(({ after, signal }) => after(send(expected ?? current.rules.revision, signal), result => { done(result) }), {
    failed: error => {
      // Honest result: nothing was saved. What was shown at once goes back to the last
      // state read at once; the server state is read again, and a failed read says so.
      const text = failureText(error, what)
      said.value = text; toast(text, { tone: 'error' })
      last.value = null
      set.value = allowedSet(matrix.value?.cells ?? [])
      load(true, true)
    },
    settled: () => { busy.value = '' },
  })
  return true
}
function saved(result: CellResult, what: string, undoing = false) {
  const m = matrix.value
  if (!m) return
  set.value = applyCells(set.value, result.changes)
  // A person's save for an account ends its wait, as the server records it.
  const touched = new Set(result.changes.map(c => c.account_id))
  matrix.value = {
    ...m, rules: { ...m.rules, revision: result.revision },
    accounts: m.accounts.map(a => a.awaiting && touched.has(a.id) ? { ...a, awaiting: false } : a),
    cells: [...set.value].map(key => { const [account_id, context_id] = key.split(':'); return { account_id: account_id!, context_id: context_id!, allowed: true } }),
  }
  const changed = result.changes.filter((c, i) => c.allowed !== result.undo[i]?.allowed).length
  said.value = undoing ? `Undone: ${what}.` : `${what}: ${changed === 1 ? '1 cell changed' : `${changed} cells changed`}.`
  last.value = undoing ? null : { owner: scope.owner.value, revision: result.revision, undo: { cells: result.undo }, what }
  toast(said.value, undoing ? {} : { key: 'account-use', timeout: 10_000, action: { label: 'Undo', run: undo } })
  load(true)
}
function cells(change: { changes: UseCell[] } | { bulk: Bulk }, what: string) {
  return write(what, (expected, signal) => saveCells(expected, change, signal), result => saved(result, what))
}
function undo() {
  const step = last.value
  if (!step || !canUndo.value) { if (step) toast('The matrix changed since, so nothing was undone.', { tone: 'error' }); return }
  const back = step.undo
  if ('cells' in back) { write(`Undo ${step.what}`, (_expected, signal) => saveCells(step.revision, { changes: back.cells }, signal), result => saved(result, step.what, true)); return }
  const def = RULES.find(d => d.key === back.rule)
  if (def) write(`Undo ${step.what}`, (_expected, signal) => saveRules(step.revision, { [back.rule]: back.value }, signal), rules => ruleSaved(def, rules, '', true))
}
function toggleCell(event: Event, accountId: string, contextId: string) {
  const input = event.target as HTMLInputElement, want = input.checked
  const a = rows.value.find(r => r.id === accountId), c = columns.value.find(col => col.id === contextId)
  if (!a || !c || !cells({ changes: [{ account_id: accountId, context_id: contextId, allowed: want }] }, `${want ? 'Allowed' : 'Not allowed'} ${accountName(a)} in ${c.name}`)) { input.checked = !want; return }
  // Shown at once; a failed save reads the server state again.
  set.value = applyCells(set.value, [{ account_id: accountId, context_id: contextId, allowed: want }])
}
function bulkRow(event: Event, accountId: string) {
  const input = event.target as HTMLInputElement, a = rows.value.find(r => r.id === accountId)
  const now = rowState(accountId, columns.value, set.value), want = nextFromTri(now)
  // The box shows the saved state until the server answers.
  input.checked = now === 'all'; input.indeterminate = now === 'some'
  if (a) cells({ bulk: { scope: 'account', account_id: accountId, allowed: want } }, `${want ? 'Allowed' : 'Not allowed'} ${accountName(a)} everywhere`)
}
function bulkColumn(event: Event, context: WorkContext) {
  const input = event.target as HTMLInputElement
  const now = columnState(context.id, rows.value, set.value), want = nextFromTri(now)
  input.checked = now === 'all'; input.indeterminate = now === 'some'
  cells({ bulk: { scope: 'context', context_id: context.id, allowed: want } }, `${want ? 'Allowed every account' : 'Allowed no account'} in ${context.name}`)
}
function bulkAll(event: Event) {
  const input = event.target as HTMLInputElement
  const now = allState.value, want = nextFromTri(now)
  input.checked = now === 'all'; input.indeterminate = now === 'some'
  cells({ bulk: { scope: 'all', allowed: want } }, want ? 'Allowed every account everywhere' : 'Allowed no account anywhere')
}

const choices = (def: RuleDef) => [def.auto, def.ask]
/** Only the one rule travels: the save never carries new_models or the rules it leaves alone. */
function setRule(def: RuleDef, value: string) {
  const m = matrix.value
  if (!m || m.rules[def.key] === value || !choices(def).includes(value)) return
  const was = m.rules[def.key]
  write(`${def.title}: ${ruleLabel(def, value)}`, (expected, signal) => saveRules(expected, { [def.key]: value }, signal), rules => ruleSaved(def, rules, was))
}
function ruleSaved(def: RuleDef, rules: Rules, was: string, undoing = false) {
  // The persisted rules are shown, never the requested ones.
  if (matrix.value) matrix.value = { ...matrix.value, rules }
  const now = ruleLabel(def, rules[def.key])
  said.value = undoing ? `Undone: ${def.title} is ${now} again.` : `${def.title}: ${now}. Saved and recorded in the audit log.`
  last.value = undoing ? null : { owner: scope.owner.value, revision: rules.revision, undo: { rule: def.key, value: was }, what: `${def.title}: ${now}` }
  toast(said.value, undoing ? {} : { key: 'account-use', timeout: 10_000, action: { label: 'Undo', run: undo } })
}
function switchKeys(event: KeyboardEvent, def: RuleDef) {
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key) || event.metaKey || event.ctrlKey || event.altKey || !matrix.value) return
  event.preventDefault()
  const all = choices(def), at = all.indexOf(matrix.value.rules[def.key])
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? all.length - 1 : (at + (['ArrowRight', 'ArrowDown'].includes(event.key) ? 1 : all.length - 1)) % all.length
  const group = event.currentTarget as HTMLElement | null, value = all[next]!
  setRule(def, value)
  void nextTick(() => group?.querySelectorAll<HTMLButtonElement>('[role=radio]')[next]?.focus())
}
const reducedMotion = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
function toggleRules() { rulesInstant.value = false; rulesMoving.value = !reducedMotion(); rulesOpen.value = !rulesOpen.value }
function settleRules(event: TransitionEvent) { if (event.target === event.currentTarget && event.propertyName === 'grid-template-rows') rulesMoving.value = false }
/**
 * "Choose where" from Needs you: For new things opens at once (no motion, so the row stays where the
 * scroll puts it), the account's row comes to the middle, tinted for a moment, and its first box takes focus.
 */
const root = ref<HTMLElement>()
function choose(accountId: string) {
  if (!matrix.value) { pendingChoose.value = accountId; return }
  if (!rulesOpen.value) { rulesInstant.value = true; rulesMoving.value = false; rulesOpen.value = true }
  void nextTick(() => {
    const behavior = reducedMotion() ? 'auto' : 'smooth'
    const row = [...(root.value?.querySelectorAll<HTMLElement>('[data-use-row]') ?? [])].find(r => r.dataset.useRow === accountId)
    if (!row) { root.value?.scrollIntoView({ block: 'start', behavior }); return }
    flash.value = accountId; clearTimeout(flashTimer); flashTimer = setTimeout(() => { flash.value = '' }, 2000)
    row.scrollIntoView({ block: 'center', behavior })
    row.querySelector<HTMLInputElement>('input[data-use-cell]')?.focus({ preventScroll: true })
  })
}
defineExpose({ choose })
function confirm() {
  write('Confirmation', (expected, signal) => confirmMatrix(expected, signal), rules => {
    if (matrix.value) matrix.value = { ...matrix.value, rules }
    last.value = null; confirmedHere.value = true; said.value = 'Confirmed. The matrix stays as it is.'; toast(said.value)
  })
}

function openMenu(context: WorkContext, event: MouseEvent) { menu.value = { context, revision: revision.value, anchor: event.currentTarget as HTMLElement } }
function closeMenu(restore = true) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowUp', 'ArrowDown'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role^=menuitem]:not(:disabled)')], at = items.indexOf(event.target as HTMLButtonElement)
  event.preventDefault(); items[(at + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
function toggleNever() {
  const snap = menu.value
  if (!snap) return
  closeMenu()
  const { context } = snap, never = context.new_accounts_override !== 'deny'
  write(`${context.name}: new accounts ${never ? 'never' : 'follow the rule'}`, (expected, signal) => updateContext(context, expected, { new_accounts_override: never ? 'deny' : null }, signal), contextSaved, snap.revision)
}
async function archive() {
  const snap = menu.value, owner = scope.owner.value
  if (!snap) return
  closeMenu(false)
  const { context } = snap
  const ok = await confirmAction({ title: `Archive ${context.name}?`, body: 'Its ticks are kept but no longer count. Projects in this context get no account until they move to another context. Running work continues.', confirmLabel: 'Archive', danger: true })
  // The context as it was when the menu opened: a change meanwhile is refused by the server, never overwritten.
  if (ok && owner === scope.owner.value) write(`Archived ${context.name}`, (expected, signal) => updateContext(context, expected, { archived: true }, signal), contextSaved, snap.revision)
}
function contextSaved(result: { context: WorkContext; revision: number }) {
  const m = matrix.value
  if (!m) return
  const contexts = result.context.archived_at ? m.contexts.filter(c => c.id !== result.context.id) : m.contexts.some(c => c.id === result.context.id) ? m.contexts.map(c => c.id === result.context.id ? result.context : c) : [...m.contexts, result.context]
  matrix.value = { ...m, contexts, rules: { ...m.rules, revision: result.revision } }
  last.value = null; form.value = null
  said.value = `Saved ${result.context.name}.`; toast(said.value)
  load(true)
}
function openForm(mode: 'add' | 'rename', anchor: HTMLElement, context?: WorkContext) {
  // A rename belongs to the revision the menu opened on.
  const at = context && menu.value?.context.id === context.id ? menu.value.revision : revision.value
  closeMenu(false)
  form.value = { mode, context, revision: at, anchor, name: context?.name ?? '', error: '' }
}
function submitForm() {
  const f = form.value
  if (!f) return
  const name = f.name.trim()
  if (!name || name.length > 128) { f.error = 'A name of 1 to 128 characters is needed.'; return }
  const context = f.context
  const adding = f.mode === 'add' || !context
  write(adding ? `Added ${name}` : `Renamed to ${name}`, (expected, signal) => adding ? createContext(expected, name, signal) : updateContext(context, expected, { name }, signal), contextSaved, adding ? undefined : f.revision)
}
function formKeys(event: KeyboardEvent) {
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  if (event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) { event.preventDefault(); submitForm() }
}

// Arrow keys move between the boxes of the matrix; Space toggles the focused box.
function gridKeys(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  if (!target.matches('input[data-r]') || event.metaKey || event.ctrlKey || event.altKey) return
  const delta = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] }[event.key]
  if (!delta) return
  event.preventDefault()
  const r = Number(target.dataset.r) + delta[0]!, c = Number(target.dataset.c) + delta[1]!
  ;(event.currentTarget as HTMLElement).querySelector<HTMLInputElement>(`input[data-r="${r}"][data-c="${c}"]`)?.focus()
}
// U undoes the last save, outside text fields and while no menu or dialog is open.
function pageKeys(event: KeyboardEvent) {
  if (event.key.toLowerCase() !== 'u' || event.metaKey || event.ctrlKey || event.altKey || event.repeat || event.defaultPrevented) return
  const t = event.target as HTMLElement | null
  if (t && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName) && !['checkbox', 'radio', 'button'].includes((t as HTMLInputElement).type))) return
  if (menu.value || form.value || document.querySelector('dialog[open]') || !canUndo.value) return
  event.preventDefault(); undo()
}
// Whether the matrix scrolls sideways is decided when it appears or the frame resizes, never while ticking.
const scroller = ref<HTMLElement>(), overflowing = ref(false)
let sizes: ResizeObserver | null = null
watch(scroller, element => {
  sizes?.disconnect(); sizes = null
  if (!element) return
  const measure = () => { overflowing.value = element.scrollWidth > element.clientWidth + 1 }
  sizes = new ResizeObserver(measure); sizes.observe(element); measure()
})
const onFocus = () => { if (!busy.value) load(true) }
onMounted(() => { load(); window.addEventListener('keydown', pageKeys); window.addEventListener('focus', onFocus) })
onBeforeUnmount(() => { sizes?.disconnect(); clearTimeout(flashTimer); window.removeEventListener('keydown', pageKeys); window.removeEventListener('focus', onFocus) })
// Another person or workspace: drop the matrix, the Undo and any open form.
watch(() => scope.owner.value, owner => { matrix.value = null; set.value = new Set(); last.value = null; menu.value = null; form.value = null; said.value = ''; busy.value = ''; confirmedHere.value = false; flash.value = ''; pendingChoose.value = ''; if (owner) load() })
</script>

<template>
  <section v-if="allowed" id="account-use" ref="root" class="zone use" aria-labelledby="account-use-title">
    <div id="where" class="zone-head"><h3 id="account-use-title">Where accounts may work</h3><p>A tick lets one account work on the projects of one context. Only owners and admins change this.</p></div>
    <p v-if="state === 'loading'" class="empty" role="status">Loading where accounts may work…</p>
    <p v-else-if="state === 'error'" class="empty" role="alert">Where accounts may work could not be read. <button type="button" class="btn sm" @click="load()">Try again</button></p>
    <template v-else-if="matrix">
      <div v-if="matrix.rules.confirmation_required || confirmedHere" class="confirm" role="region" aria-label="Confirm the matrix">
        <p><b>Looks right?</b> The matrix was carried over so every account kept working as before. Confirm once that it fits; change ticks any time.</p>
        <button v-if="matrix.rules.confirmation_required" type="button" class="btn sm primary" data-use-confirm :disabled="!!busy" @click="confirm">Looks right</button>
        <span v-else class="done"><AppIcon name="check" :size="14" />Confirmed</span>
      </div>

      <div class="nt" :class="{ open: rulesOpen, moving: rulesMoving, instant: rulesInstant }">
        <button type="button" class="nt-sum" data-use-rules :aria-expanded="rulesOpen" aria-controls="use-rules" @click="toggleRules"><AppIcon name="chevron-right" :size="14" class="chev" /><b>For new things</b><span class="nt-pv">{{ preview }}</span></button>
        <div id="use-rules" class="nt-fold" role="region" aria-label="For new things" :inert="!rulesOpen || undefined" @transitionend="settleRules" @transitioncancel="settleRules">
          <div class="nt-in">
            <div class="nt-rows">
              <div v-for="def in RULES" :key="def.key" class="nt-r" :data-use-rule="def.key">
                <div class="nt-t">
                  <b :id="`use-rule-${def.key}`">{{ def.title }}</b>
                  <!-- Both values' words share one cell: the chosen one shows, so a switch never changes the row's height. -->
                  <div class="nt-say">
                    <div v-for="value in choices(def)" :key="value" class="say" :class="{ off: matrix.rules[def.key] !== value }" :aria-hidden="matrix.rules[def.key] !== value || undefined" :data-use-say="matrix.rules[def.key] === value ? def.key : undefined">
                      <p>{{ named(def.say[value] ?? '', names) }}</p>
                      <p class="ex">For example, <span>{{ named(def.example[value] ?? '', names) }}</span></p>
                    </div>
                  </div>
                </div>
                <div class="seg" role="radiogroup" :aria-labelledby="`use-rule-${def.key}`" :data-use-switch="def.key" @keydown="switchKeys($event, def)">
                  <button v-for="value in choices(def)" :key="value" type="button" role="radio" :aria-checked="matrix.rules[def.key] === value" :tabindex="matrix.rules[def.key] === value ? 0 : -1" :aria-disabled="!!busy" @click="setRule(def, value)">{{ ruleLabel(def, value) }}</button>
                </div>
              </div>
              <div class="nt-r ro" data-use-rule="new_models">
                <div class="nt-t"><b>New model versions</b><p>Whether a newer version of a model you use runs by itself. It is Auto-update in the model registry.</p></div>
                <span class="nt-v"><b data-use-models>{{ modelRuleLabel(matrix.rules.new_models) }}</b><span aria-hidden="true">·</span><RouterLink class="link-btn go" :to="MODELS_LINK">set in Models<AppIcon name="chevron-right" :size="12" /></RouterLink></span>
              </div>
            </div>
          </div>
        </div>
      </div>
      <p class="sr-only" role="status" aria-live="polite">{{ said }}</p>

      <div v-if="rows.length && columns.length" ref="scroller" class="matrix-scroll" tabindex="-1">
        <table class="grid" aria-label="Where accounts may work" :aria-busy="!!busy" :style="{ '--cols': columns.length }" @keydown="gridKeys">
          <thead><tr>
            <th scope="col" class="corner">
              <span class="corner-in"><label class="tb"><input type="checkbox" class="tri" :data-r="0" :data-c="0" data-use-all :checked="allState === 'all'" :indeterminate="allState === 'some'" aria-label="Every account in every context" @change="bulkAll" /></label>Account<span class="ph">· tick where each may work</span></span>
            </th>
            <th v-for="(c, ci) in columns" :key="c.id" scope="col" :data-use-col="c.id">
              <div class="col-head">
                <label class="tb"><input type="checkbox" class="tri" :data-r="0" :data-c="ci + 1" :checked="columnState(c.id, rows, set) === 'all'" :indeterminate="columnState(c.id, rows, set) === 'some'" :aria-label="`Every account in ${c.name}`" @change="bulkColumn($event, c)" /></label>
                <span class="col-name"><b v-clip-tip>{{ c.name }}</b><small v-if="c.new_accounts_override === 'deny'" v-clip-tip>new accounts: never</small></span>
                <button type="button" class="icon-btn flat" :aria-label="`More for ${c.name}`" :aria-expanded="menu?.context.id === c.id" @click="openMenu(c, $event)"><AppIcon name="more" :size="14" /></button>
              </div>
            </th>
          </tr></thead>
          <tbody>
            <tr v-for="(a, ri) in rows" :key="a.id" :data-use-row="a.id" :class="{ new: a.awaiting, flash: flash === a.id }">
              <th scope="row">
                <div class="row-head">
                  <label class="tb"><input type="checkbox" class="tri" :data-r="ri + 1" :data-c="0" :checked="rowState(a.id, columns, set) === 'all'" :indeterminate="rowState(a.id, columns, set) === 'some'" :aria-label="`${accountName(a)} in every context`" @change="bulkRow($event, a.id)" /></label>
                  <HarnessMark :harness="a.harness" />
                  <span class="who"><b><span class="nm">{{ accountName(a) }}</span><span v-if="a.awaiting" class="tag" data-use-new>New · ask first</span></b><small>{{ sub(a) }}</small></span>
                </div>
              </th>
              <td v-for="(c, ci) in columns" :key="c.id">
                <label class="cell"><input type="checkbox" :data-r="ri + 1" :data-c="ci + 1" :data-use-cell="`${a.id}:${c.id}`" :checked="set.has(cellKey(a.id, c.id))" @change="toggleCell($event, a.id, c.id)" /><span class="cl" aria-hidden="true">{{ c.name }}</span><span class="sr-only">{{ accountName(a) }} in {{ c.name }}</span></label>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="rows.length && columns.length && overflowing" class="scroll-hint"><AppIcon name="arrow" :size="14" />Scroll sideways for all {{ columns.length }} contexts</p>
      <p v-else-if="!rows.length || !columns.length" class="empty">{{ !rows.length ? 'No accounts yet. New accounts follow the rule above.' : 'No contexts yet.' }}</p>
      <div class="addrow"><button type="button" class="add" data-use-add :disabled="!!busy" @click="openForm('add', $event.currentTarget as HTMLElement)"><AppIcon name="plus" :size="15" /><span>Add context</span></button></div>
      <div class="foot-notes">
        <p v-if="defaultContext" class="foot"><AppIcon name="info" :size="13" /><span><b>{{ defaultContext.name }}</b> also covers work without a project.</span></p>
        <p v-if="holding" class="foot" data-use-holding><AppIcon name="lock" :size="13" /><span><b>{{ holding.name }}</b> holds projects waiting for a context; no account works there.<template v-if="waiting.first[0]"> Waiting now: <b>{{ waiting.first[0].name }}</b><template v-if="waiting.count > 1"> and {{ waiting.count - 1 === 1 ? '1 more' : `${(waiting.count - 1).toLocaleString('en-GB')} more` }}</template> · <RouterLink class="link-btn" :to="workContextLink(waiting.first[0])">{{ waiting.count > 1 ? 'Choose their contexts' : 'Choose its context' }}</RouterLink></template></span></p>
        <p class="foot"><AppIcon name="rollback" :size="13" /><span>Every change saves at once and says so; Undo sits in its message (or press U).</span></p>
        <p v-if="truncated" class="foot" role="note"><AppIcon name="info" :size="13" /><span>Showing the first 200 accounts and contexts. The corner box, rows and columns still apply to all of them.</span></p>
      </div>

      <section v-if="outside.length" class="outside" aria-labelledby="use-outside-title">
        <h4 id="use-outside-title">Running outside the matrix</h4>
        <p class="lead">This work started before the change and keeps running. New work follows the ticks above.</p>
        <ul>
          <li v-for="run in outside" :key="run.run_id" :data-use-outside="run.run_id"><HarnessMark v-if="run.account" :harness="run.account.harness" /><span><b>{{ run.account ? accountName(run.account) : 'Account on another page' }}</b><small class="mono">Run {{ run.run_id.slice(0, 8) }}</small></span></li>
        </ul>
        <p v-if="matrix.running_outside_truncated" class="foot" role="note"><AppIcon name="info" :size="13" /><span>Only the first 200 runs are listed.</span></p>
      </section>
    </template>

    <FloatingPanel v-if="menu" :anchor="menu.anchor" :width="260" align="end" :label="`More for ${menu.context.name}`" @close="closeMenu">
      <div class="row-menu" role="menu" @keydown="menuKeys">
        <button type="button" role="menuitem" :disabled="!!busy" @click="openForm('rename', menu.anchor, menu.context)"><AppIcon name="edit" :size="14" />Rename…</button>
        <button type="button" role="menuitemcheckbox" :aria-checked="menu.context.new_accounts_override === 'deny'" :disabled="!!busy" @click="toggleNever"><AppIcon :name="menu.context.new_accounts_override === 'deny' ? 'check' : 'square'" :size="14" />New accounts: never</button>
        <template v-if="menu.context.kind === 'regular'"><div class="separator" /><button type="button" role="menuitem" class="danger" :disabled="!!busy" @click="archive"><AppIcon name="archive" :size="14" />Archive…</button></template>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="form" :anchor="form.anchor" :width="320" align="end" :label="form.mode === 'add' ? 'Add a context' : `Rename ${form.context?.name}`" cycle field-escape @close="form = null">
      <form class="name-form" @submit.prevent="submitForm" @keydown="formKeys">
        <label>{{ form.mode === 'add' ? 'Name of the new context' : 'Name' }}<input v-model="form.name" class="field" maxlength="128" data-autofocus :disabled="!!busy" /></label>
        <p v-if="form.mode === 'add'" class="hint">{{ matrix?.rules.new_contexts === 'allow' ? 'Every account starts allowed here (New contexts: Automatic).' : 'No account starts allowed here (New contexts: Ask first).' }}</p>
        <p class="hint error" role="alert">{{ form.error }}</p>
        <div class="buttons"><button class="btn primary" type="submit" :disabled="!!busy">Save <KeyCap k="mod" /><KeyCap k="enter" /></button><button class="btn" type="button" @click="form = null">Cancel</button></div>
      </form>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.zone { margin-top: 30px; }
.use { container: use / inline-size; }
.zone-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 10px; scroll-margin-top: 80px; }
.zone-head h3 { font-size: 18px; font-weight: 600; }
.zone-head p { font-size: 12px; color: var(--ink-3); }
.empty { color: var(--ink-3); font-size: 13px; padding: 14px 0; }
.confirm { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 16px; min-height: 52px; padding: 10px 14px; margin-bottom: 14px; border-radius: var(--radius-s); background: var(--row-selected); font-size: 13px; color: var(--ink-2); }
.confirm p { flex: 1 1 280px; }
.confirm .done { display: inline-flex; align-items: center; gap: 6px; height: 28px; color: var(--teal-ink); font-weight: 600; font-size: 12.5px; }

/* For new things: one quiet line; opened, the three rules and the read-only model line. It grows downward only. */
.nt { margin-bottom: 18px; border-radius: 14px; background: var(--surface-sunken); }
.nt-sum { display: grid; grid-template-columns: 14px auto minmax(0, 1fr); align-items: center; column-gap: 10px; width: 100%; min-height: 52px; padding: 8px 16px 8px 14px; border: 0; border-radius: 14px; background: transparent; color: var(--ink); text-align: left; }
@media (hover: hover) { .nt-sum:hover { background: var(--row-hover); } }
.nt-sum:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.nt-sum .chev { color: var(--ink-3); transition: transform .2s ease; }
.nt.open .nt-sum .chev { transform: rotate(90deg); }
.nt-sum b { font-size: 13.5px; font-weight: 650; white-space: nowrap; }
.nt-pv { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-2); }
.nt.open .nt-pv { visibility: hidden; }
.nt-fold { display: grid; grid-template-rows: 0fr; transition: grid-template-rows .24s ease; }
.nt.open .nt-fold { grid-template-rows: 1fr; }
.nt.instant .nt-fold, .nt.instant .chev { transition: none; }
/* The body clips only while folded or moving, so focus rings inside it are never cut off once open. */
.nt-in { min-height: 0; overflow: hidden; }
.nt.open:not(.moving) .nt-in { overflow: visible; }
/* Folded and settled, the body is hidden as well as inert: nothing in it is shown, read out or focused. */
.nt:not(.open):not(.moving) .nt-in { visibility: hidden; }
.nt-rows { display: grid; padding: 0 16px 8px 38px; }
.nt-r { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: start; column-gap: 20px; padding: 14px 0; }
.nt-r + .nt-r { border-top: 1px solid var(--line); }
.nt-t { display: grid; gap: 3px; min-width: 0; }
.nt-t b { font-size: 13.5px; font-weight: 600; color: var(--ink); }
.nt-t p { font-size: 12.5px; line-height: 1.55; color: var(--ink-2); max-width: 58ch; }
.nt-say { display: grid; }
.nt-say > .say { grid-area: 1 / 1; display: grid; align-content: start; gap: 3px; }
.nt-say > .say.off { visibility: hidden; }
.nt-t .ex { color: var(--ink-3); }
.nt-t .ex span { color: var(--ink-2); }
.nt-r .seg { margin-top: 1px; background: var(--seg-bg); }
.nt-r .seg button { height: 30px; min-width: 88px; padding: 0 12px; white-space: nowrap; }
.nt-r .seg button[aria-checked=true] { background: var(--seg-on); color: var(--teal-ink); }
.nt-r .seg button[aria-disabled=true] { cursor: progress; }
.nt-r.ro { align-items: center; }
.nt-v { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--ink-2); white-space: nowrap; }
.nt-v b { color: var(--ink); font-weight: 600; }
.link-btn { padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-weight: 500; text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 2px; cursor: pointer; }
.link-btn:hover { text-decoration-color: currentColor; }
.link-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
.link-btn.go { display: inline-flex; align-items: center; gap: 2px; text-decoration: none; }

/* The matrix: the corner, column and row boxes save in bulk. */
.matrix-scroll { max-width: 100%; overflow-x: auto; }
.grid { width: 100%; border-collapse: collapse; table-layout: fixed; min-width: calc(260px + var(--cols, 2) * 120px); }
.grid th, .grid td { padding: 0; border-bottom: 1px solid var(--line); text-align: left; font-weight: inherit; vertical-align: middle; }
.grid .corner { width: 34%; min-width: 220px; padding: 6px 10px; }
.corner-in { display: flex; align-items: center; gap: 10px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.corner-in .ph { display: none; }
/* A box's hit area: 28 px around the 18 px box (44 px on touch), without taking more room in the row. */
.tb { display: inline-grid; place-items: center; flex: none; width: 28px; height: 28px; margin: -5px; border-radius: 8px; cursor: pointer; }
@media (hover: hover) { .tb:hover { background: var(--row-hover); } }
.col-head { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 8px; min-height: 54px; padding: 6px 4px 6px 10px; }
.col-name, .who { display: grid; min-width: 0; }
.col-name b, .who b { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
/* Headings wrap to two lines; the clip-tip shows anything longer. */
.col-name b { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.col-name small { font-size: 11.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.who b { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 8px; }
.who .nm { min-width: 0; }
.who small { font-size: 11.5px; color: var(--ink-3); overflow-wrap: anywhere; }
.tag { display: inline-flex; align-items: center; height: 20px; padding: 0 7px; border-radius: 6px; font: 600 9.5px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; white-space: nowrap; color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.row-head { display: grid; grid-template-columns: auto auto minmax(0, 1fr); align-items: center; gap: 10px; min-height: 52px; padding: 6px 10px; }
.cell { display: flex; align-items: center; justify-content: center; width: 100%; min-height: 52px; cursor: pointer; border-radius: var(--radius-row); }
@media (hover: hover) { .cell:hover { background: var(--row-hover); } }
.cell .cl { display: none; }
.grid input[type=checkbox] { width: 18px; height: 18px; margin: 0; accent-color: var(--primary); cursor: pointer; }
/* A new account that asks first: a full tint and the New tag; "Choose where" flashes its row. */
.grid tr.new th, .grid tr.new td { background: color-mix(in srgb, var(--teal) 6%, transparent); }
.grid tr.flash th, .grid tr.flash td { background: var(--row-selected); transition: background .3s ease; }
.scroll-hint { display: flex; align-items: center; gap: 6px; margin-top: 8px; font-size: 12px; color: var(--ink-3); }
.addrow { padding: 8px 0 0; }
.add { display: inline-flex; align-items: center; gap: 8px; min-height: 40px; margin-left: -2px; padding: 0 12px 0 10px; border: 0; border-radius: 10px; background: transparent; color: var(--teal-ink); font-size: 13.5px; font-weight: 600; }
@media (hover: hover) { .add:not(:disabled):hover { background: var(--row-hover); } }
.add:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.foot-notes { display: grid; gap: 8px; padding-top: 14px; }
.foot { display: flex; align-items: flex-start; gap: 8px; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
.foot svg { flex: none; margin-top: 2px; }
.foot b { color: var(--ink-2); font-weight: 600; }
.outside { margin-top: 22px; }
.outside h4 { font-size: 14px; font-weight: 600; }
.outside .lead { margin: 2px 0 8px; font-size: 12.5px; color: var(--ink-3); }
.outside ul { list-style: none; margin: 0; padding: 0; border-top: 1px solid var(--line); }
.outside li { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: 10px; min-height: 48px; border-bottom: 1px solid var(--line); }
.outside li span { display: grid; min-width: 0; }
.outside li b { font-size: 13px; }
.outside li small { font-size: 11.5px; color: var(--ink-3); }
.outside .foot { margin-top: 12px; }
.row-menu { display: grid; gap: 1px; }
.row-menu button { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 34px; padding: 0 10px; text-align: left; border: 0; border-radius: 8px; background: transparent; font-size: 13.5px; color: var(--ink); }
.row-menu button:hover:not(:disabled) { background: var(--row-hover); }
.row-menu .danger { color: var(--danger); }
.separator { height: 1px; margin: 4px 6px; background: var(--line); }
.name-form { display: grid; gap: 10px; padding: 4px; font-size: 13px; }
.name-form label { display: grid; gap: 6px; }
.name-form .field { min-width: 0; min-height: 36px; }
.hint { font-size: 12px; color: var(--ink-3); }
.hint.error { min-height: 16px; color: var(--danger); }
.buttons { display: flex; flex-wrap: wrap; gap: 8px; }
@media (prefers-reduced-motion: reduce) { .nt-fold, .nt-sum .chev, .grid tr.flash th, .grid tr.flash td { transition: none; } }
@media (max-width: 720px) {
  .grid { min-width: calc(160px + var(--cols, 2) * 96px); }
  .grid .corner { width: 160px; min-width: 160px; }
  .row-head { gap: 8px; padding: 6px 8px; }
  .col-head { gap: 6px; padding: 6px 2px 6px 8px; }
}
/* Phone and narrow windows: each account is a stacked row with named context boxes; nothing scrolls sideways. */
@container use (max-width: 560px) {
  .nt-sum { grid-template-columns: 14px minmax(0, 1fr); row-gap: 2px; padding: 10px 14px; }
  .nt-pv { grid-column: 2; white-space: normal; }
  .nt.open .nt-pv { display: none; }
  .nt-rows { padding: 0 14px 8px; }
  .nt-r { grid-template-columns: minmax(0, 1fr); row-gap: 10px; }
  .nt-r .seg { justify-self: start; }
  .nt-v { white-space: normal; flex-wrap: wrap; }
  .matrix-scroll { overflow: visible; }
  .grid, .grid thead, .grid tbody, .grid tr, .grid th, .grid td { display: block; width: auto; min-width: 0; }
  .grid thead tr { display: flex; flex-wrap: wrap; gap: 6px; padding-bottom: 12px; border-bottom: 1px solid var(--line-2); }
  .grid thead th { border: 0; }
  .grid .corner { flex-basis: 100%; width: auto; min-width: 0; padding: 4px 0; }
  .corner-in .ph { display: inline; letter-spacing: 0; text-transform: none; font: 500 12px/1.4 var(--font); color: var(--ink-3); }
  .col-head { min-height: 44px; padding: 0 2px 0 10px; border-radius: 12px; background: var(--surface-sunken); }
  .col-name small { display: none; }
  .grid tbody tr { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); column-gap: 8px; padding: 10px 0 12px; border-bottom: 1px solid var(--line); }
  .grid tbody th { grid-column: 1 / -1; border: 0; }
  .grid tbody td { border: 0; }
  .row-head { min-height: 52px; padding: 4px 0; }
  .cell { justify-content: start; gap: 10px; min-height: 44px; padding: 0 10px; border-radius: 10px; }
  .cell .cl { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; color: var(--ink-2); text-align: left; }
  .scroll-hint { display: none; }
}
@media (max-width: 720px), (pointer: coarse) {
  /* Links inside sentences keep the line height and still give a 44 px target (design §touch). */
  .link-btn { display: inline-flex; align-items: center; justify-content: center; min-height: 44px; min-width: 44px; padding: 0 8px; margin: -12px -8px; vertical-align: middle; }
  .nt-r .seg button, .add { min-height: 44px; }
  .grid input[type=checkbox] { width: 22px; height: 22px; }
  .tb { width: 44px; height: 44px; margin: -11px; }
  .col-head .icon-btn { width: 44px; height: 44px; }
  .cell { min-height: 52px; }
}
</style>
