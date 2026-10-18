<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { APIError, createNode, type ListItem } from '../../lib/api'
import { createRate, listRates, type CostRate, type Unit } from '../../lib/business'
import { toast } from '../../lib/toast'
import { plural } from '../../lib/work'
import { formatAmount, parseAmountInput } from '../../components/business/money'
import { useBusiness, type CostUnit } from '../../stores/business'
import AppIcon from '../../components/business/BizIcon.vue'
import BusinessPage from '../../components/business/BusinessPage.vue'

// Rates per cost unit, each valid from a date. A new rate for the same unit and
// currency closes the one in force at its start; nothing earlier changes, so
// quotes and hours keep the rates they were made with.
const business = useBusiness()
const term = ref('')
const showRetired = ref(false)
const adding = ref('')
const creating = ref(false)
const newName = ref('')
const busy = ref(false)
const error = ref('')
const createInput = ref<HTMLInputElement>()
const form = reactive({ unit: 'hour' as Unit, currency: 'EUR', bill: '', internal: '', from: '', until: '' })
const UNITS: Unit[] = ['hour', 'day', 'item']
const today = new Date().toISOString().slice(0, 10)
const retired = (unit: CostUnit) => ['cancelled', 'archived', 'done'].includes(unit.node.state)
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return business.costUnits.filter(unit => showRetired.value || !retired(unit))
    .filter(unit => !needle || `${unit.node.key} ${unit.node.title}`.toLowerCase().includes(needle))
    .sort((a, b) => Number(retired(a)) - Number(retired(b)) || a.node.title.localeCompare(b.node.title))
})
const retiredCount = computed(() => business.costUnits.filter(retired).length)
const inForce = computed(() => business.costUnits.reduce((n, unit) => n + unit.rates.filter(r => status(r) === 'current').length, 0))
function status(rate: CostRate): 'current' | 'scheduled' | 'ended' {
  if (rate.effective_from > today) return 'scheduled'
  if (rate.effective_until && rate.effective_until <= today) return 'ended'
  return 'current'
}
const STATUS_LABEL = { current: 'In force', scheduled: 'Scheduled', ended: 'Ended' }
function ordered(rates: CostRate[]) {
  const rank = { current: 0, scheduled: 1, ended: 2 }
  return [...rates].sort((a, b) => a.unit.localeCompare(b.unit) || a.currency.localeCompare(b.currency) || rank[status(a)] - rank[status(b)] || b.effective_from.localeCompare(a.effective_from))
}
const dateFormat = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
const day = (value: string | null) => value ? dateFormat.format(new Date(`${value}T00:00:00Z`)) : ''
const money = (value: string, currency: string) => { try { return formatAmount(value, currency) } catch { return value } }
// "2026-10-01", "1.10.2026" or "01/10/2026" as YYYY-MM-DD.
function parseDate(text: string): string | null {
  const value = text.trim()
  let match = /^(\d{4})-(\d{1,2})-(\d{1,2})$/.exec(value)
  let y: number, m: number, d: number
  if (match) { y = +match[1]; m = +match[2]; d = +match[3] }
  else if ((match = /^(\d{1,2})[./](\d{1,2})[./](\d{4})$/.exec(value))) { d = +match[1]; m = +match[2]; y = +match[3] }
  else return null
  const date = new Date(Date.UTC(y, m - 1, d))
  return date.getUTCFullYear() === y && date.getUTCMonth() === m - 1 && date.getUTCDate() === d ? date.toISOString().slice(0, 10) : null
}
const problem = computed(() => {
  if (!/^[A-Z]{3}$/.test(form.currency.trim().toUpperCase())) return 'Currency is a three-letter code like EUR.'
  if (parseAmountInput(form.bill) === null) return 'Bill rate is an amount with at most four decimals.'
  if (parseAmountInput(form.internal || '0') === null) return 'Internal rate is an amount with at most four decimals.'
  if (!parseDate(form.from)) return 'Valid from is a date like 2026-10-01.'
  if (form.until.trim() && (!parseDate(form.until) || parseDate(form.until)! <= parseDate(form.from)!)) return 'Valid until is a later date, or empty.'
  return ''
})
async function startAdd(unit: CostUnit) {
  adding.value = unit.node.id; error.value = ''
  const last = ordered(unit.rates)[0]
  Object.assign(form, { unit: last?.unit ?? 'hour', currency: last?.currency ?? 'EUR', bill: '', internal: '', from: today, until: '' })
  await nextTick(); document.querySelector<HTMLInputElement>(`[data-rate-form="${unit.node.id}"] [data-first]`)?.focus()
}
async function save(unit: CostUnit) {
  if (problem.value || busy.value) { error.value = problem.value; return }
  busy.value = true; error.value = ''
  try {
    await createRate(unit.node.id, {
      unit: form.unit, currency: form.currency.trim().toUpperCase(), bill_amount: parseAmountInput(form.bill)!, internal_amount: parseAmountInput(form.internal || '0')!,
      effective_from: parseDate(form.from)!, effective_until: form.until.trim() ? parseDate(form.until) : null,
    })
    business.setRates(unit.node.id, await listRates(unit.node.id))
    toast(`Rate added to ${unit.node.title}.`)
    adding.value = ''
  } catch (e) {
    error.value = e instanceof APIError && e.status === 409 ? `${e.message.replace(/^./, c => c.toUpperCase())}. Rates for one unit and currency cannot overlap.` : e instanceof Error ? e.message : 'The rate was not added.'
  } finally { busy.value = false }
}
function formKeys(event: KeyboardEvent, unit: CostUnit) {
  if (event.key === 'Enter') { event.preventDefault(); void save(unit) }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); adding.value = '' }
}
async function startCreate() { creating.value = true; await nextTick(); createInput.value?.focus() }
async function create() {
  const name = newName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  try {
    await business.loadKinds()
    const kind = business.kindBySlug('cost_unit')
    if (!kind) throw new Error('This workspace has no cost unit type.')
    const node = await createNode({ kind_id: kind.id, title: name, parent_id: null, state: 'new' })
    const item = { ...node, kind_slug: 'cost_unit', kind_label: kind.label, priority: null, assignee: null, parent: null, children_count: 0, project: null } as ListItem
    business.addCostUnit(item)
    newName.value = ''; creating.value = false
    toast(`Added ${name}. Give it a rate next.`)
    await nextTick(); void startAdd(business.costUnit(node.id)!)
  } catch (e) { toast(`Not added: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
  finally { busy.value = false }
}
onMounted(async () => { await business.loadPlugins(); if (business.open.costs) void business.loadCostUnits(true) })
</script>

<template>
  <BusinessPage title="Rates" area="costs">
    <template #summary>
      <span v-if="business.costUnitsLoaded">{{ business.costUnits.length ? `${plural(business.costUnits.length - retiredCount, 'cost unit')} · ${plural(inForce, 'rate')} in force` : 'No cost units yet' }}</span>
      <span v-else class="skeleton summary-skeleton" />
    </template>

    <div class="toolbar">
      <label class="search-field list-search">
        <AppIcon name="search" :size="14" />
        <input v-model="term" class="field" type="search" placeholder="Find a cost unit" aria-label="Find a cost unit" autocomplete="off" />
      </label>
      <label v-if="retiredCount" class="switch"><input v-model="showRetired" type="checkbox" /><span>Retired</span><span class="mono faint">{{ retiredCount }}</span></label>
      <span class="spacer" />
      <p class="explain">{{ business.admin ? 'A new rate closes the one in force on its start date.' : 'Workspace admins set rates.' }}</p>
      <button v-if="business.admin" type="button" class="btn primary" @click="startCreate"><AppIcon name="plus" :size="14" />New cost unit</button>
    </div>

    <section class="card glass-card" aria-label="Cost units and their rates">
      <div v-if="creating" class="create-row" @keydown.enter.prevent="create" @keydown.esc.stop.prevent="creating = false">
        <AppIcon name="tag" :size="14" class="create-icon" />
        <input ref="createInput" v-model="newName" class="field" placeholder="Cost unit name, like Development" aria-label="New cost unit name" maxlength="512" autocomplete="off" />
        <button type="button" class="btn sm" @click="creating = false">Cancel</button>
        <button type="button" class="btn sm on" :disabled="!newName.trim() || busy" @click="create">Add</button>
      </div>
      <div v-if="!business.costUnitsLoaded" class="sk" aria-hidden="true"><span v-for="i in 4" :key="i" class="skeleton" /></div>
      <div v-else-if="!shown.length && !creating" class="state">
        <span class="state-icon"><AppIcon name="tag" :size="18" /></span>
        <h2>{{ term ? `No cost unit matches “${term}”` : retiredCount ? 'No cost unit in use' : 'No cost units yet' }}</h2>
        <p v-if="!term && retiredCount">{{ plural(retiredCount, 'retired cost unit') }} {{ retiredCount === 1 ? 'is' : 'are' }} hidden. Show {{ retiredCount === 1 ? 'it' : 'them' }} with Retired, or add one for current work.</p>
        <p v-else>A cost unit prices work: Development, Design, Consulting. Each gets bill and internal rates per hour, day or item.</p>
        <div v-if="!term && (business.admin || retiredCount)" class="state-actions">
          <button v-if="retiredCount" type="button" class="btn ghost" @click="showRetired = true">Show retired</button>
          <button v-if="business.admin" type="button" class="btn" @click="startCreate"><AppIcon name="plus" :size="14" />New cost unit</button>
        </div>
      </div>
      <div v-for="unit in shown" :key="unit.node.id" class="unit" :class="{ retired: retired(unit) }">
        <header class="unit-head">
          <span class="unit-mark" aria-hidden="true"><AppIcon name="tag" :size="13" /></span>
          <h2 class="unit-name">{{ unit.node.title }}</h2>
          <span class="key mono">{{ unit.node.key }}</span>
          <span v-if="retired(unit)" class="chip retired-chip">Retired</span>
          <span class="spacer" />
          <button v-if="business.admin && adding !== unit.node.id" type="button" class="link-btn" @click="startAdd(unit)"><AppIcon name="plus" :size="12" />Add rate</button>
        </header>
        <table v-if="unit.rates.length || adding === unit.node.id" class="rates" :aria-label="`Rates of ${unit.node.title}`">
          <thead><tr><th scope="col">Unit</th><th scope="col">Currency</th><th scope="col" class="num">Bill</th><th scope="col" class="num">Internal</th><th scope="col">Valid</th><th scope="col">Status</th></tr></thead>
          <tbody>
            <tr v-for="rate in ordered(unit.rates)" :key="rate.id" :class="status(rate)">
              <td>per {{ rate.unit }}</td>
              <td class="mono">{{ rate.currency }}</td>
              <td class="num mono strong">{{ money(rate.bill_amount, rate.currency) }}</td>
              <td class="num mono">{{ money(rate.internal_amount, rate.currency) }}</td>
              <td class="valid">{{ rate.effective_until ? `${day(rate.effective_from)} – ${day(rate.effective_until)}` : `from ${day(rate.effective_from)}` }}</td>
              <td><span class="status-chip" :class="status(rate)">{{ STATUS_LABEL[status(rate)] }}</span></td>
            </tr>
            <tr v-if="adding === unit.node.id" class="form-row">
              <td colspan="6">
                <div class="rate-form" :data-rate-form="unit.node.id" @keydown="formKeys($event, unit)">
                  <label class="select-label"><span>Unit</span><select v-model="form.unit" class="field select" data-first><option v-for="u in UNITS" :key="u" :value="u">per {{ u }}</option></select><AppIcon name="chevron" :size="12" class="select-chev" /></label>
                  <label><span>Currency</span><input v-model="form.currency" class="field mono" maxlength="3" autocomplete="off" /></label>
                  <label><span>Bill rate</span><input v-model="form.bill" class="field mono" inputmode="decimal" placeholder="95.00" autocomplete="off" /></label>
                  <label><span>Internal rate</span><input v-model="form.internal" class="field mono" inputmode="decimal" placeholder="60.00" autocomplete="off" /></label>
                  <label><span>Valid from</span><input v-model="form.from" class="field mono" placeholder="YYYY-MM-DD" autocomplete="off" /></label>
                  <label><span>Valid until</span><input v-model="form.until" class="field mono" placeholder="open" autocomplete="off" /></label>
                  <div class="form-actions">
                    <p class="form-note" :class="{ warn: !!error }" role="status">{{ error || 'Enter saves · Esc cancels' }}</p>
                    <button type="button" class="btn sm" @click="adding = ''">Cancel</button>
                    <button type="button" class="btn sm on" :disabled="busy" @click="save(unit)">{{ busy ? 'Adding…' : 'Add rate' }}</button>
                  </div>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-else class="no-rates">No rate yet, so quotes and hours cannot use it.</p>
      </div>
    </section>
  </BusinessPage>
</template>

<style scoped>
.summary-skeleton { display: inline-block; width: 220px; }
.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px 14px; min-height: 48px; margin-bottom: 12px; }
.list-search { width: 260px; }
.list-search .field { height: 32px; font-size: 13.5px; }
.faint { color: var(--ink-3); font-size: 11px; }
.spacer { flex: 1; }
.explain { font-size: 12.5px; color: var(--ink-3); }
.card { overflow: clip; container: rates / inline-size; }
.create-row { display: flex; align-items: center; gap: 10px; padding: 12px 18px; border-bottom: 1px solid var(--line); background: var(--row-selected); }
.create-row .field { flex: 1; height: 32px; }
.create-icon { color: var(--teal); }
.sk { display: grid; gap: 14px; padding: 20px; } .sk .skeleton { height: 12px; }
.unit { padding: 4px 0 12px; border-bottom: 1px solid var(--line); }
.unit:last-child { border-bottom: 0; }
.unit.retired { opacity: .7; }
.unit-head { display: flex; align-items: center; gap: 10px; min-height: 48px; padding: 6px 18px 2px; }
.unit-mark { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.unit-name { font-size: 14.5px; font-weight: 650; }
.key { font-size: 11px; color: var(--ink-3); }
.retired-chip { height: 18px; font-size: 10px; text-transform: uppercase; letter-spacing: .08em; }
.link-btn { display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0; height: 26px; padding: 0 10px; white-space: nowrap; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.link-btn:hover { background: var(--row-hover); }
.link-btn:focus-visible { box-shadow: var(--focus-ring); }
.rates { width: calc(100% - 36px); margin: 4px 18px 0; border-collapse: collapse; table-layout: fixed; font-size: 13px; }
.rates th:nth-child(1) { width: 16%; } .rates th:nth-child(2) { width: 11%; } .rates th:nth-child(3), .rates th:nth-child(4) { width: 14%; } .rates th:nth-child(5) { width: 27%; }
.rates th { height: 28px; padding: 0 10px; text-align: left; font: 500 9.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); border-bottom: 1px solid var(--line-2); font-variant-ligatures: none; }
.rates td { height: 36px; padding: 0 10px; border-bottom: 1px solid var(--line); }
.rates tbody tr:last-child td { border-bottom: 0; }
.num { text-align: right; }
.mono { font-family: var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; font-size: 12.5px; }
.strong { font-weight: 650; }
.valid { color: var(--ink-2); white-space: nowrap; }
tr.ended td:not(:last-child) { color: var(--ink-3); }
.status-chip { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.status-chip.current { background: var(--ok-bg); box-shadow: inset 0 0 0 1px var(--ok-line); color: var(--ok); }
.status-chip.scheduled { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.form-row td { height: auto; padding: 10px 0; background: var(--surface-sunken); }
.rate-form { display: grid; grid-template-columns: 1fr 90px 1fr 1fr 1.1fr 1.1fr; gap: 10px; padding: 0 10px; }
.rate-form label { display: grid; gap: 5px; }
.rate-form label span { font: 500 10px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.rate-form .field { height: 32px; font-size: 13px; }
.select-label { position: relative; }
.select { appearance: none; padding-right: 26px; cursor: pointer; }
.select-chev { position: absolute; right: 10px; bottom: 10px; color: var(--ink-3); pointer-events: none; }
.form-actions { grid-column: 1 / -1; display: flex; align-items: center; gap: 8px; }
.form-note { flex: 1; font-size: 12px; color: var(--ink-3); }
.form-note.warn { color: var(--warn-ink); }
.no-rates { padding: 2px 18px 6px 56px; font-size: 12.5px; color: var(--ink-3); }
.state { display: grid; justify-items: center; gap: 8px; padding: 48px 24px 56px; text-align: center; }
.state h2 { font-size: 17px; }
.state p { max-width: 460px; font-size: 13.5px; }
.state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 4px; }
.state .btn { margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
@container rates (max-width: 700px) {
  .rates th:nth-child(4), .rates td:nth-child(4) { display: none; }
  .rate-form { grid-template-columns: 1fr 1fr; }
}
@media (max-width: 720px) {
  .toolbar { gap: 8px; }
  .list-search { flex: 1 1 100%; width: auto; }
  .list-search .field { height: 44px; font-size: 16px; }
  .explain { flex-basis: 100%; order: 5; }
  .unit-head { padding: 6px 12px 2px; }
  /* Phones: one rate per two lines, unit and amount first, validity under them. */
  .rates { width: calc(100% - 16px); margin: 4px 8px 0; }
  .rates thead { display: none; }
  .rates tbody tr:not(.form-row) { display: grid; grid-template-columns: auto auto auto minmax(0, 1fr) auto; grid-template-areas: "unit bill cur . status" "valid valid valid valid valid"; align-items: baseline; column-gap: 8px; padding: 8px 2px; border-bottom: 1px solid var(--line); }
  .rates tbody tr:not(.form-row):last-child { border-bottom: 0; }
  .rates tbody tr:not(.form-row) td { height: auto; padding: 0; border: 0; }
  .rates td:nth-child(1) { grid-area: unit; } .rates td:nth-child(3) { grid-area: bill; } .rates td:nth-child(6) { grid-area: status; }
  .rates td:nth-child(5) { grid-area: valid; font-size: 12px; color: var(--ink-3); }
  .rates td:nth-child(2) { grid-area: cur; font-size: 10.5px; color: var(--ink-3); letter-spacing: .06em; }
  .rates td:nth-child(4) { display: none; }
  .rates .form-row { display: block; }
  .rates .form-row td { display: block; }
  .rate-form .field { height: 44px; font-size: 16px; }
  .no-rates { padding-left: 12px; }
}
</style>
