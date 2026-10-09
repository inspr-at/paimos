<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1054: Settings › Accounts › "Where accounts may work" (concept AEON-1044
// §2.5). Rows are accounts, columns are work contexts; a tick allows one account
// to work on the projects of one context. The four switches above the matrix
// decide what new things get. Every save carries the matrix revision it was made
// on; the server answers 409 for a competing change and nothing is saved, so the
// matrix is read again. Undo sends the inverse cells of the last save with the
// revision that save returned. Running work outside the matrix keeps running.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { HARNESS_LABEL } from '../../lib/agentState'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import { vClipTip } from '../../directives/clipTip'
import {
  allowedSet, applyCells, billingLabel, cellKey, columnState, confirmMatrix, createContext, failureText, nextFromTri, PERMISSION, readMatrix,
  rowState, ruleValues, saveCells, saveRules, switchChoices, SWITCHES, tickable, updateContext,
  type Bulk, type CellResult, type Matrix, type RuleValues, type SwitchDef, type UseCell, type WorkContext,
} from '../../lib/accountUse'
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
/** The last save of this person on this screen: Undo is valid only while nothing changed since. */
const last = ref<{ owner: string; revision: number; undo: UseCell[]; what: string } | null>(null)
const menu = ref<{ context: WorkContext; anchor: HTMLElement } | null>(null)
const form = ref<{ mode: 'add' | 'rename'; context?: WorkContext; anchor: HTMLElement; name: string; error: string } | null>(null)

const columns = computed(() => matrix.value ? tickable(matrix.value.contexts) : [])
const rows = computed(() => matrix.value?.accounts ?? [])
const holding = computed(() => matrix.value?.contexts.find(c => c.kind === 'holding'))
const owners = computed(() => new Map(agents.accounts.map(a => [a.id, a.owner_person_name || ''])))
const revision = computed(() => matrix.value?.rules.revision ?? 0)
const canUndo = computed(() => !!last.value && last.value.owner === scope.owner.value && last.value.revision === revision.value && !busy.value)
const truncated = computed(() => !!matrix.value && (!!matrix.value.next_account || !!matrix.value.next_context))
const outside = computed(() => (matrix.value?.running_outside ?? []).map(run => ({ ...run, account: rows.value.find(a => a.id === run.account_id) })))

function load(quiet = false) {
  if (!scope.owner.value) { matrix.value = null; return }
  if (!quiet) state.value = matrix.value ? 'ready' : 'loading'
  const floor = revision.value
  void reads.run(({ after, signal }) => after(readMatrix(signal), page => {
    // A read that started before our own save can answer after it; never step back.
    if (page.rules.revision < floor && matrix.value) return
    matrix.value = page; set.value = allowedSet(page.cells); state.value = 'ready'
  }), { failed: () => { if (!matrix.value) state.value = 'error'; else if (!quiet) toast('The matrix could not be read again. The last state stays visible.', { tone: 'error' }) } })
}
function sub(account: Matrix['accounts'][number]) {
  return [account.plan || 'Plan not reported', billingLabel(account.billing_mode), owners.value.get(account.id) ? `Owner ${owners.value.get(account.id)}` : 'No owner linked'].join(' · ')
}
const accountName = (a: { label: string; harness: string }) => a.label || HARNESS_LABEL[a.harness] || a.harness

/** One write at a time. The owner and revision are captured when the person acts; a later answer for another owner is dropped by the scope. */
function write<T>(what: string, send: (expected: number, signal: AbortSignal) => Promise<T>, done: (result: T) => void) {
  const current = matrix.value
  if (!current || busy.value) return false
  busy.value = what; reads.cancel()
  void scope.run(({ after, signal }) => after(send(current.rules.revision, signal), result => { done(result) }), {
    failed: error => {
      // Honest result: nothing was saved. A conflict shows the server's current state.
      const text = failureText(error, what)
      said.value = text; toast(text, { tone: 'error' })
      last.value = null
      if (error instanceof APIError && error.status === 409) load(true)
      else { set.value = allowedSet(matrix.value?.cells ?? []); load(true) }
    },
    settled: () => { busy.value = '' },
  })
  return true
}
function saved(result: CellResult, what: string, undoing = false) {
  const m = matrix.value
  if (!m) return
  set.value = applyCells(set.value, result.changes)
  matrix.value = { ...m, rules: { ...m.rules, revision: result.revision }, cells: [...set.value].map(key => { const [account_id, context_id] = key.split(':'); return { account_id: account_id!, context_id: context_id!, allowed: true } }) }
  const changed = result.changes.filter((c, i) => c.allowed !== result.undo[i]?.allowed).length
  said.value = undoing ? `Undone: ${what}.` : `${what}: ${changed === 1 ? '1 cell changed' : `${changed} cells changed`}.`
  last.value = undoing ? null : { owner: scope.owner.value, revision: result.revision, undo: result.undo, what }
  toast(said.value, undoing ? {} : { key: 'account-use', timeout: 10_000, action: { label: 'Undo', run: undo } })
  load(true)
}
function cells(change: { changes: UseCell[] } | { bulk: Bulk }, what: string) {
  return write(what, (expected, signal) => saveCells(expected, change, signal), result => saved(result, what))
}
function undo() {
  const step = last.value
  if (!step || !canUndo.value) { if (step) toast('The matrix changed since, so nothing was undone.', { tone: 'error' }); return }
  write(`Undo ${step.what}`, (_expected, signal) => saveCells(step.revision, { changes: step.undo }, signal), result => saved(result, step.what, true))
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
const bulkAll = (want: boolean) => cells({ bulk: { scope: 'all', allowed: want } }, want ? 'Allow all' : 'Allow none')

function setRule(def: SwitchDef, value: string) {
  const m = matrix.value
  if (!m || m.rules[def.key] === value) return
  const values: RuleValues = { ...ruleValues(m.rules), [def.key]: value }
  const label = switchChoices(def, value).find(c => c.value === value)?.label ?? value
  write(`${def.title}: ${label}`, (expected, signal) => saveRules(expected, values, signal), rules => {
    // The persisted rules are shown, never the requested ones.
    if (matrix.value) matrix.value = { ...matrix.value, rules }
    last.value = null
    said.value = `${def.title}: ${switchChoices(def, rules[def.key]).find(c => c.value === rules[def.key])?.label}. Saved and recorded in the audit log.`
    toast(said.value)
  })
}
function switchKeys(event: KeyboardEvent, def: SwitchDef) {
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key) || event.metaKey || event.ctrlKey || event.altKey || !matrix.value) return
  event.preventDefault()
  const choices = switchChoices(def, matrix.value.rules[def.key]), at = choices.findIndex(c => c.value === matrix.value!.rules[def.key])
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? choices.length - 1 : (at + (['ArrowRight', 'ArrowDown'].includes(event.key) ? 1 : choices.length - 1)) % choices.length
  setRule(def, choices[next]!.value)
  void nextTick(() => (event.currentTarget as HTMLElement | null)?.querySelectorAll<HTMLButtonElement>('[role=radio]')[next]?.focus())
}
function confirm() {
  write('Confirmation', (expected, signal) => confirmMatrix(expected, signal), rules => {
    if (matrix.value) matrix.value = { ...matrix.value, rules }
    last.value = null; confirmedHere.value = true; said.value = 'Confirmed. The matrix stays as it is.'; toast(said.value)
  })
}

function openMenu(context: WorkContext, event: MouseEvent) { menu.value = { context, anchor: event.currentTarget as HTMLElement } }
function closeMenu(restore = true) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowUp', 'ArrowDown'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role^=menuitem]:not(:disabled)')], at = items.indexOf(event.target as HTMLButtonElement)
  event.preventDefault(); items[(at + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
function toggleNever(context: WorkContext) {
  closeMenu()
  const never = context.new_accounts_override !== 'deny'
  write(`${context.name}: new accounts ${never ? 'never' : 'follow the switch'}`, (expected, signal) => updateContext(context, expected, { new_accounts_override: never ? 'deny' : null }, signal), contextSaved)
}
async function archive(context: WorkContext) {
  closeMenu(false)
  const ok = await confirmAction({ title: `Archive ${context.name}?`, body: 'Its ticks are kept but no longer count. Projects in this context get no account until they move to another context. Running work continues.', confirmLabel: 'Archive', danger: true })
  if (ok) write(`Archived ${context.name}`, (expected, signal) => updateContext(context, expected, { archived: true }, signal), contextSaved)
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
  closeMenu(false)
  form.value = { mode, context, anchor, name: context?.name ?? '', error: '' }
}
function submitForm() {
  const f = form.value
  if (!f) return
  const name = f.name.trim()
  if (!name || name.length > 128) { f.error = 'A name of 1 to 128 characters is needed.'; return }
  const context = f.context
  write(f.mode === 'add' ? `Added ${name}` : `Renamed to ${name}`, (expected, signal) => f.mode === 'add' || !context ? createContext(expected, name, signal) : updateContext(context, expected, { name }, signal), contextSaved)
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
onBeforeUnmount(() => { sizes?.disconnect(); window.removeEventListener('keydown', pageKeys); window.removeEventListener('focus', onFocus) })
// Another person or workspace: drop the matrix, the Undo and any open form.
watch(() => scope.owner.value, owner => { matrix.value = null; set.value = new Set(); last.value = null; menu.value = null; form.value = null; said.value = ''; busy.value = ''; confirmedHere.value = false; if (owner) load() })
</script>

<template>
  <section v-if="allowed" id="account-use" class="zone use" aria-labelledby="account-use-title">
    <div class="zone-head"><h3 id="account-use-title">Where accounts may work</h3><p>A tick lets one account work on the projects of one context. Only owners and admins change this.</p></div>
    <p v-if="state === 'loading'" class="empty" role="status">Loading where accounts may work…</p>
    <p v-else-if="state === 'error'" class="empty" role="alert">Where accounts may work could not be read. <button type="button" class="btn sm" @click="load()">Try again</button></p>
    <template v-else-if="matrix">
      <div v-if="matrix.rules.confirmation_required || confirmedHere" class="confirm" role="region" aria-label="Confirm the matrix">
        <p><b>Looks right?</b> The matrix was carried over so every account kept working as before. Confirm once that it fits; change ticks any time.</p>
        <button v-if="matrix.rules.confirmation_required" type="button" class="btn sm primary" data-use-confirm :disabled="!!busy" @click="confirm">Looks right</button>
        <span v-else class="done"><AppIcon name="check" :size="14" />Confirmed</span>
      </div>

      <dl class="switches" aria-label="What new things get">
        <div v-for="def in SWITCHES" :key="def.key" class="switch-row">
          <dt><b>{{ def.title }}</b><small>{{ def.hint }}</small></dt>
          <dd>
            <div class="seg" role="radiogroup" :aria-label="def.title" :data-use-switch="def.key" @keydown="switchKeys($event, def)">
              <button v-for="choice in switchChoices(def, matrix.rules[def.key])" :key="choice.value" type="button" role="radio" :aria-checked="matrix.rules[def.key] === choice.value" :tabindex="matrix.rules[def.key] === choice.value ? 0 : -1" :aria-disabled="!!busy" @click="setRule(def, choice.value)">{{ choice.label }}</button>
            </div>
          </dd>
        </div>
      </dl>

      <div class="tools" role="toolbar" aria-label="Matrix actions">
        <button type="button" class="btn sm" data-use-all :disabled="!rows.length || !columns.length" @click="bulkAll(true)"><AppIcon name="check" :size="14" />Allow all</button>
        <button type="button" class="btn sm" data-use-none :disabled="!rows.length || !columns.length" @click="bulkAll(false)"><AppIcon name="minus" :size="14" />Allow none</button>
        <button type="button" class="btn sm" data-use-undo :disabled="!canUndo" :aria-keyshortcuts="'U'" @click="undo"><AppIcon name="rollback" :size="14" />Undo <KeyCap k="U" /></button>
        <button type="button" class="btn sm add" data-use-add :disabled="!!busy" @click="openForm('add', $event.currentTarget as HTMLElement)"><AppIcon name="plus" :size="14" />Add context</button>
      </div>
      <p class="sr-only" role="status" aria-live="polite">{{ said }}</p>

      <div v-if="rows.length && columns.length" ref="scroller" class="matrix-scroll" tabindex="-1">
        <table class="grid" aria-label="Where accounts may work" :aria-busy="!!busy" :style="{ '--cols': columns.length }" @keydown="gridKeys">
          <thead><tr>
            <th scope="col" class="corner">Account</th>
            <th v-for="(c, ci) in columns" :key="c.id" scope="col" :data-use-col="c.id">
              <div class="col-head">
                <input type="checkbox" class="tri" :data-r="0" :data-c="ci + 1" :checked="columnState(c.id, rows, set) === 'all'" :indeterminate="columnState(c.id, rows, set) === 'some'" :aria-label="`Every account in ${c.name}`" @change="bulkColumn($event, c)" />
                <span class="col-name"><b v-clip-tip>{{ c.name }}</b><small v-clip-tip>{{ c.kind === 'default' ? 'Also work without a project' : c.new_accounts_override === 'deny' ? 'New accounts: never' : 'Context' }}</small></span>
                <button type="button" class="icon-btn flat" :aria-label="`More for ${c.name}`" :aria-expanded="menu?.context.id === c.id" @click="openMenu(c, $event)"><AppIcon name="more" :size="14" /></button>
              </div>
            </th>
          </tr></thead>
          <tbody>
            <tr v-for="(a, ri) in rows" :key="a.id" :data-use-row="a.id">
              <th scope="row">
                <div class="row-head">
                  <input type="checkbox" class="tri" :data-r="ri + 1" :data-c="0" :checked="rowState(a.id, columns, set) === 'all'" :indeterminate="rowState(a.id, columns, set) === 'some'" :aria-label="`${accountName(a)} in every context`" @change="bulkRow($event, a.id)" />
                  <HarnessMark :harness="a.harness" />
                  <span class="who"><b>{{ accountName(a) }}</b><small>{{ sub(a) }}</small></span>
                </div>
              </th>
              <td v-for="(c, ci) in columns" :key="c.id">
                <label class="cell"><input type="checkbox" :data-r="ri + 1" :data-c="ci + 1" :data-use-cell="`${a.id}:${c.id}`" :checked="set.has(cellKey(a.id, c.id))" @change="toggleCell($event, a.id, c.id)" /><span class="sr-only">{{ accountName(a) }} in {{ c.name }}</span></label>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="rows.length && columns.length && overflowing" class="scroll-hint"><AppIcon name="arrow" :size="14" />Scroll sideways for all {{ columns.length }} contexts</p>
      <p v-else-if="!rows.length || !columns.length" class="empty">{{ !rows.length ? 'No accounts yet. New accounts follow the switch above.' : 'No contexts yet.' }}</p>
      <p v-if="truncated" class="footnote" role="note"><AppIcon name="info" :size="14" /><span>Showing the first 200 accounts and contexts. Allow all, rows and columns still apply to all of them.</span></p>
      <p v-if="holding" class="footnote"><AppIcon name="lock" :size="14" /><span><b>{{ holding.name }}</b> holds projects waiting for a decision. No account works there until the project gets a context in its settings.</span></p>

      <section v-if="outside.length" class="outside" aria-labelledby="use-outside-title">
        <h4 id="use-outside-title">Running outside the matrix</h4>
        <p class="lead">This work started before the change and keeps running. New work follows the ticks above.</p>
        <ul>
          <li v-for="run in outside" :key="run.run_id" :data-use-outside="run.run_id"><HarnessMark v-if="run.account" :harness="run.account.harness" /><span><b>{{ run.account ? accountName(run.account) : 'Account on another page' }}</b><small class="mono">Run {{ run.run_id.slice(0, 8) }}</small></span></li>
        </ul>
        <p v-if="matrix.running_outside_truncated" class="footnote" role="note"><AppIcon name="info" :size="14" /><span>Only the first 200 runs are listed.</span></p>
      </section>
    </template>

    <FloatingPanel v-if="menu" :anchor="menu.anchor" :width="260" align="end" :label="`More for ${menu.context.name}`" @close="closeMenu">
      <div class="row-menu" role="menu" @keydown="menuKeys">
        <button type="button" role="menuitem" :disabled="!!busy" @click="openForm('rename', menu.anchor, menu.context)"><AppIcon name="edit" :size="14" />Rename…</button>
        <button type="button" role="menuitemcheckbox" :aria-checked="menu.context.new_accounts_override === 'deny'" :disabled="!!busy" @click="toggleNever(menu.context)"><AppIcon :name="menu.context.new_accounts_override === 'deny' ? 'check' : 'square'" :size="14" />New accounts: never</button>
        <template v-if="menu.context.kind === 'regular'"><div class="separator" /><button type="button" role="menuitem" class="danger" :disabled="!!busy" @click="archive(menu.context)"><AppIcon name="archive" :size="14" />Archive…</button></template>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="form" :anchor="form.anchor" :width="320" align="end" :label="form.mode === 'add' ? 'Add a context' : `Rename ${form.context?.name}`" cycle field-escape @close="form = null">
      <form class="name-form" @submit.prevent="submitForm" @keydown="formKeys">
        <label>{{ form.mode === 'add' ? 'Name of the new context' : 'Name' }}<input v-model="form.name" class="field" maxlength="128" data-autofocus :disabled="!!busy" /></label>
        <p v-if="form.mode === 'add'" class="hint">{{ matrix?.rules.new_contexts === 'allow' ? 'Every account starts allowed here (New contexts: allow automatically).' : 'No account starts allowed here (New contexts: ask first).' }}</p>
        <p class="hint error" role="alert">{{ form.error }}</p>
        <div class="buttons"><button class="btn primary" type="submit" :disabled="!!busy">Save <KeyCap k="mod" /><KeyCap k="enter" /></button><button class="btn" type="button" @click="form = null">Cancel</button></div>
      </form>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.zone { margin-top: 30px; }
.zone-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 10px; }
.zone-head h3 { font-size: 18px; font-weight: 600; }
.zone-head p { font-size: 12px; color: var(--ink-3); }
.empty { color: var(--ink-3); font-size: 13px; padding: 14px 0; }
.confirm { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 16px; min-height: 52px; padding: 10px 14px; margin-bottom: 14px; border-radius: var(--radius-s); background: var(--row-selected); font-size: 13px; color: var(--ink-2); }
.confirm p { flex: 1 1 280px; }
.confirm .done { display: inline-flex; align-items: center; gap: 6px; height: 28px; color: var(--teal-ink); font-weight: 600; font-size: 12.5px; }
.switches { display: grid; grid-template-columns: minmax(0, 1fr); margin: 0 0 14px; border-top: 1px solid var(--line); }
.switch-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 6px 16px; min-height: 56px; padding: 6px 0; border-bottom: 1px solid var(--line); }
.switch-row dt { display: grid; gap: 2px; min-width: 0; }
.switch-row dt b { font-size: 13.5px; font-weight: 600; }
.switch-row dt small { font-size: 12px; color: var(--ink-3); }
.switch-row dd { margin: 0; }
.seg { flex-wrap: wrap; background: var(--seg-bg); }
.seg button { height: 30px; padding: 0 12px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.seg button[aria-checked=true] { background: var(--seg-on); color: var(--teal-ink); }
.seg button[aria-disabled=true] { cursor: progress; }
.tools { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 8px; }
.tools .add { margin-left: auto; }
.tools .btn :deep(.keycap) { margin-left: 2px; }
.matrix-scroll { max-width: 100%; overflow-x: auto; }
.grid { width: 100%; border-collapse: collapse; table-layout: fixed; min-width: calc(260px + var(--cols, 2) * 120px); }
.grid th, .grid td { padding: 0; border-bottom: 1px solid var(--line); text-align: left; font-weight: inherit; vertical-align: middle; }
.grid .corner { width: 34%; min-width: 220px; padding: 6px 10px; font: 500 11px var(--mono); color: var(--ink-3); }
.col-head { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 8px; min-height: 54px; padding: 6px 4px 6px 10px; }
.col-name, .who { display: grid; min-width: 0; }
.col-name b, .who b { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
/* Headings wrap to two lines; the clip-tip shows anything longer. */
.col-name b { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.col-name small { font-size: 11.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.who small { font-size: 11.5px; color: var(--ink-3); overflow-wrap: anywhere; }
.row-head { display: grid; grid-template-columns: auto auto minmax(0, 1fr); align-items: center; gap: 10px; min-height: 52px; padding: 6px 10px; }
.cell { display: flex; align-items: center; justify-content: center; width: 100%; min-height: 52px; cursor: pointer; border-radius: var(--radius-row); }
.cell:hover { background: var(--row-hover); }
.grid input[type=checkbox] { width: 18px; height: 18px; margin: 0; accent-color: var(--primary); cursor: pointer; }
.scroll-hint { display: flex; align-items: center; gap: 6px; margin-top: 8px; font-size: 12px; color: var(--ink-3); }
.footnote { display: flex; gap: 8px; margin-top: 12px; font-size: 12.5px; color: var(--ink-3); }
.footnote svg { flex: none; margin-top: 2px; }
.outside { margin-top: 22px; }
.outside h4 { font-size: 14px; font-weight: 600; }
.outside .lead { margin: 2px 0 8px; font-size: 12.5px; color: var(--ink-3); }
.outside ul { list-style: none; margin: 0; padding: 0; border-top: 1px solid var(--line); }
.outside li { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: 10px; min-height: 48px; border-bottom: 1px solid var(--line); }
.outside li span { display: grid; min-width: 0; }
.outside li b { font-size: 13px; }
.outside li small { font-size: 11.5px; color: var(--ink-3); }
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
@media (max-width: 720px) {
  .switch-row { grid-template-columns: minmax(0, 1fr); }
  .tools .add { margin-left: 0; }
  .grid { min-width: calc(160px + var(--cols, 2) * 96px); }
  .grid .corner { width: 160px; min-width: 160px; }
  .row-head { gap: 8px; padding: 6px 8px; }
  .col-head { gap: 6px; padding: 6px 2px 6px 8px; }
}
@media (pointer: coarse) {
  .seg button, .tools .btn { min-height: 44px; }
  .grid input[type=checkbox] { width: 22px; height: 22px; }
}
</style>
