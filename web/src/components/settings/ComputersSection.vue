<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Computers (AEON-1171, design AEON-1149): where agents run. One table since
// AEON-1174: a row per computer, a column per account, and New starts last. A cell is one
// account signed in on that computer, said in one word, and opens the computer's panel at
// that sign-in; a column head opens the account. The New starts chip changes only the mode,
// with the panel's own write (every other policy field kept), and offers Undo. Past six
// accounts the computer column stays in view while the table scrolls sideways; on a phone
// each computer is a stack of rows. Needs you lists the sign-ins with a problem and pending
// cleanup, and is absent when nothing needs you. Shared with Accounts and Capacity.
import { computed, nextTick, onBeforeUnmount, ref, toRefs, watch } from 'vue'
import type { PairingView } from '../../lib/agentPairing'
import type { OverviewAccount, SignInReference } from '../../lib/accountsOverview'
import { computerLine, shortIdentity, signinCell } from '../../lib/accountsGlance'
import { HostCapacityWriteError, mayChangeNewStarts, NEW_STARTS_MODES, NEW_STARTS_NAME, newStartsChoices, newStartsFoot, newStartsLabel, newStartsWrite, samePolicy, saveHostPolicy, type HostCapacityMode, type HostCapacityPolicy } from '../../lib/hostCapacity'
import { toast } from '../../lib/toast'
import { useAccountsSettings } from '../../lib/useAccountsSettings'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import AccountsPanel from './AccountsPanel.vue'
import NeedsYouList from './NeedsYouList.vue'
import SettingsPopover, { type SettingsMenuItem } from './SettingsPopover.vue'

const ctx = useAccountsSettings('computers')
const { agents, capacity, accounts, computers, manage, open, computer, owner, retained, needs, allReady, show, actAttention, references } = toRefs(ctx)

const line = (c: PairingView) => computerLine(c, agents.value.now)
const cell = (s: SignInReference) => signinCell(s, capacity.value.rows.find(row => row.id === s.enrollment.account_id), agents.value.now)
// Names that clamp show in full on hover and on keyboard focus (Tick, AEON-1173 review): a column
// head's tip is the vendor and the whole address; a cell's tip starts with the sign-in's full name,
// and a cell without one names it through the clipped label (the phone rows).
const signinName = (a: OverviewAccount, s: SignInReference) => `${a.vendor} · ${a.records.find(r => r.id === s.enrollment.account_id)?.label ?? a.identity}`
const selected = (c: PairingView) => open.value && computer.value?.computer_id === c.computer_id
// The computer column stays in view while the table scrolls sideways: always past six
// accounts (design AEON-1149), and in a window too narrow for fewer. Measured when the
// table's size changes, never while someone works inside it.
const wrap = ref<HTMLElement>(), overflows = ref(false)
const sizes = new ResizeObserver(() => { const w = wrap.value; overflows.value = !!w && w.scrollWidth > w.clientWidth + 1 })
watch(wrap, (element, before) => { if (before) sizes.unobserve(before); if (element) { sizes.observe(element); if (element.firstElementChild) sizes.observe(element.firstElementChild) } })
onBeforeUnmount(() => sizes.disconnect())

// ---------- New starts: the chip on each row ----------
// A save the next read has not brought yet: the chip shows what the server confirmed, at
// the revision the write produced (the server bumps it by one in the same write).
const confirmed = ref(new Map<string, { revision: number; policy: HostCapacityPolicy }>())
watch(computers, list => {
  const keep = new Map([...confirmed.value].filter(([id, saved]) => (list.find(c => c.computer_id === id)?.revision ?? 0) < saved.revision))
  if (keep.size !== confirmed.value.size) confirmed.value = keep
})
function current(c: PairingView): { policy?: HostCapacityPolicy; revision?: number } {
  const saved = confirmed.value.get(c.computer_id ?? '')
  return saved && (c.revision ?? 0) < saved.revision ? saved : { policy: c.host_capacity?.policy, revision: c.revision }
}
const busy = ref('')
/** The chip is a control for the owner on a connected computer that reports; otherwise its words only. */
const editable = (c: PairingView) => mayChangeNewStarts(c, manage.value) && c.connectivity === 'online' && !!current(c).policy && !!current(c).revision
const chip = (c: PairingView) => { const p = current(c).policy; return p ? newStartsLabel(p) : '' }
// Every mode's words share the chip's one cell, so switching never resizes it (AEON-541).
const sizers = (c: PairingView) => { const p = current(c).policy; return p ? NEW_STARTS_MODES.filter(m => m !== p.mode).map(m => newStartsLabel(p, m)) : [] }
function stopped(c: PairingView) { return c.computer_state === 'revoked' ? `No new starts on ${c.computer_name}: it was removed` : c.computer_state === 'draining' ? `New starts are blocked on ${c.computer_name} while it drains` : `New starts on ${c.computer_name} are not reported` }

const menuOpen = ref(false), anchor = ref<HTMLElement | null>(null), menuFor = ref(''), menuContext = ref('')
const recordKey = (c: PairingView) => `${owner.value}/${c.computer_id}:${current(c).revision}:${current(c).policy?.mode}`
const menuComputer = computed(() => computers.value.find(c => c.computer_id === menuFor.value))
const menuItems = computed<SettingsMenuItem[]>(() => {
  const c = menuComputer.value, p = c && current(c).policy
  if (!c || !p) return []
  return [
    ...newStartsChoices(p, c.host_capacity?.signals?.cores).map(o => ({ id: o.mode, label: o.title, detail: o.detail, checked: o.mode === p.mode, disabled: busy.value === c.computer_id })),
    { id: 'panel', label: `More options in ${c.computer_name}’s panel`, detail: newStartsFoot(p), icon: 'sliders', separated: true },
  ]
})
async function openNewStarts(c: PairingView, event: Event) {
  if (!editable(c) || busy.value === c.computer_id) return
  if (menuOpen.value && menuFor.value === c.computer_id) { menuOpen.value = false; return }
  menuOpen.value = false
  anchor.value = event.currentTarget as HTMLElement; menuFor.value = c.computer_id!; menuContext.value = recordKey(c)
  // Publish the record first: the shared popover closes when its context changes.
  const context = menuContext.value
  await nextTick()
  if (menuContext.value === context && editable(c)) menuOpen.value = true
}
async function chooseNewStarts(id: string, context: string | undefined) {
  const c = menuComputer.value
  if (!c) return
  // The choice belongs to the computer as it was when the menu opened.
  if (context !== recordKey(c)) { toast(`${c.computer_name} changed. Open New starts again.`, { tone: 'error' }); return }
  if (id === 'panel') { await show.value('computer', c.computer_id!); return }
  if ((NEW_STARTS_MODES as readonly string[]).includes(id)) await setMode(c, id as HostCapacityMode)
}
function failure(error: unknown, name: string, undoing = false) {
  const status = error instanceof HostCapacityWriteError ? error.status : -1
  if (status === 409) return `${name} changed elsewhere. New starts ${undoing ? 'was not undone' : 'was not saved'}; it shows the current setting now.`
  if (status === 403) return `Only the owner of ${name} can change its New starts.`
  if (status === 0) return `New starts on ${name} could not be confirmed. Refresh the computer.`
  return `New starts on ${name} ${undoing ? 'could not be undone' : 'could not be saved'}. Please try again.`
}
async function write(c: PairingView, revision: number, policy: HostCapacityPolicy, undoing: boolean): Promise<HostCapacityPolicy | null> {
  const id = c.computer_id!, identity = owner.value
  busy.value = id
  try {
    const saved = await saveHostPolicy(id, { expected_revision: revision, policy })
    if (identity !== owner.value) return null
    confirmed.value = new Map(confirmed.value).set(id, { revision: revision + 1, policy: saved })
    return saved
  } catch (error) {
    if (identity === owner.value) toast(failure(error, c.computer_name, undoing), { tone: 'error' })
    return null
  } finally {
    if (busy.value === id) busy.value = ''
    void ctx.refresh()
  }
}
async function setMode(c: PairingView, mode: HostCapacityMode) {
  const now = current(c), before = now.policy
  const request = before && now.revision ? newStartsWrite({ revision: now.revision, host_capacity: { ...c.host_capacity!, policy: before } }, mode) : null
  if (!before || !request || busy.value) return
  const saved = await write(c, request.expected_revision, request.policy, false)
  if (!saved) return
  const id = c.computer_id!, name = c.computer_name, revision = request.expected_revision + 1
  toast(`New starts on ${name}: ${NEW_STARTS_NAME[saved.mode]} · running agents keep working`, { action: { label: 'Undo', run: () => { void undo(id, name, revision, saved, before) } } })
}
/** Undo writes the earlier policy back, only while the computer still holds what this save wrote. */
async function undo(id: string, name: string, revision: number, saved: HostCapacityPolicy, before: HostCapacityPolicy) {
  const c = capacity.value.computers.find(x => x.computer_id === id)
  const now = c && current(c)
  if (!c || busy.value || now?.revision !== revision || !samePolicy(now.policy, saved)) { toast(`${name} changed since. Nothing was undone; New starts shows the current setting.`, { tone: 'error' }); return }
  if (await write(c, revision, before, true)) toast(`New starts on ${name}: ${NEW_STARTS_NAME[before.mode]} again`)
}
watch(owner, () => { menuOpen.value = false; confirmed.value = new Map(); busy.value = '' })
</script>

<template>
  <AccountsPanel :ctx="ctx">
    <template #head><header class="page-head"><h2>Where agents run</h2><p>Each computer keeps its own sign-ins; a cell is one account signed in on one computer.</p></header></template>
    <NeedsYouList :items="needs.computers" hide-when-calm :show-calm="retained > 0" :aside="allReady ? 'Everything else is working.' : 'Based on the last available reports.'" @action="actAttention" />
    <section class="zone ct-zone" aria-labelledby="computers-title">
      <div class="zone-head"><h3 id="computers-title">Computers <span class="count">{{ computers.length }}</span></h3><p>Sign-ins and New starts, a row per computer</p><RouterLink v-if="manage" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" />Connect a computer</RouterLink></div>
      <div v-if="computers.length" ref="wrap" class="ct-wrap" tabindex="0" role="region" aria-label="Computers and their sign-ins; scroll sideways for more accounts">
        <table class="ct" :class="{ many: accounts.length > 6 || overflows }" :style="{ '--ct-accounts': accounts.length }" aria-label="Computers and their sign-ins">
          <thead><tr>
            <th scope="col" class="c-pc"><span class="ct-corner">Computer</span></th>
            <th v-for="a in accounts" :key="a.id" scope="col" class="c-acc"><button type="button" class="ct-ah" :data-tip="`${a.vendor} · ${a.identity}`" :aria-label="`${a.vendor} · ${a.identity}`" @click="show('account', a.id, $event)"><b><HarnessMark :harness="a.harness" :size="18" /><span>{{ a.vendor }}</span></b><small>{{ shortIdentity(a.identity) }}</small></button></th>
            <th scope="col" class="c-ns"><span class="ct-nsh">New starts</span></th>
          </tr></thead>
          <tbody>
            <tr v-for="c in computers" :key="c.computer_id!" :data-row="c.computer_id" :class="{ dim: line(c).tone === 'off', current: selected(c) }">
              <th scope="row"><button type="button" class="pc" :data-computer="c.computer_id" :aria-expanded="selected(c)" :aria-current="selected(c)" @click="show('computer', c.computer_id!, $event)"><AppIcon name="monitor" /><span class="who"><b v-clip-tip>{{ c.computer_name }}</b><small><span class="dot" :class="line(c).tone" /><span v-clip-tip>{{ line(c).text }}</span></small></span></button></th>
              <td v-for="a in accounts" :key="a.id" :class="{ none: !references(a, c).length }">
                <button v-for="s in references(a, c)" :key="s.enrollment.account_id" type="button" class="si" :class="cell(s).tone" :data-signin="s.enrollment.account_id" :data-tip="cell(s).tip ? `${signinName(a, s)} on ${c.computer_name}: ${cell(s).tip}` : undefined" :aria-label="`${signinName(a, s)} on ${c.computer_name}: ${cell(s).word}${cell(s).tip ? `. ${cell(s).tip}` : ''}`" @click="show('computer', c.computer_id!, $event, s.enrollment.account_id)"><span class="cl" aria-hidden="true"><HarnessMark :harness="a.harness" :size="16" /><span v-clip-tip="signinName(a, s)">{{ a.vendor }} · {{ shortIdentity(a.identity) }}</span></span><span class="sv"><span class="dot" :class="cell(s).tone" /><span class="w">{{ cell(s).word }}</span></span></button>
                <span v-if="!references(a, c).length" class="si none"><AppIcon name="minus" :size="14" /><span class="sr-only">{{ a.vendor }} · {{ a.identity }} is not signed in on {{ c.computer_name }}</span></span>
              </td>
              <td class="nsc"><span class="cl" aria-hidden="true">New starts</span>
                <button v-if="editable(c)" type="button" class="ns" :data-new-starts="c.computer_id" aria-haspopup="menu" aria-expanded="false" :aria-busy="busy === c.computer_id || undefined" :aria-label="`New starts on ${c.computer_name}: ${chip(c)}. Change`" @click="openNewStarts(c, $event)"><AppIcon name="sliders" :size="13" /><span class="ns-say"><span>{{ chip(c) }}</span><span v-for="(words, i) in sizers(c)" :key="i" class="sizer" aria-hidden="true">{{ words }}</span></span><AppIcon name="chevron" :size="12" /></button>
                <span v-else-if="chip(c) && c.computer_state === 'connected'" class="ns ro" :data-new-starts="c.computer_id">{{ chip(c) }}</span>
                <span v-else class="ns ro none"><AppIcon name="minus" :size="14" /><span class="sr-only">{{ stopped(c) }}</span></span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="!computers.length && capacity.computersLoaded" class="empty">No paired computers yet.</p>
      <div class="ct-foot">
        <p v-if="computers.length === 100" class="foot">First 100 computers shown.</p>
        <p class="foot"><AppIcon name="shield" :size="13" /><span>Every sign-in stays on its computer. PAIMOS stores status and timestamps, never the vendor credential.</span></p>
        <p v-if="computers.length" class="foot"><AppIcon name="sliders" :size="13" /><span><b>New starts</b> decides when a computer takes on another agent. Only new starts wait; running agents are never stopped.</span></p>
      </div>
    </section>
    <SettingsPopover v-model:open="menuOpen" :anchor="anchor" :label="menuComputer ? `New starts on ${menuComputer.computer_name}` : 'New starts'" :heading="menuComputer ? `New starts on ${menuComputer.computer_name}` : 'New starts'" :items="menuItems" :context-key="menuContext" :busy="!!busy" @select="chooseNewStarts" />
  </AccountsPanel>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
<style scoped>
/* AEON-1174 (design AEON-1149): the Computers table. Hairlines, no boxes; the row is the unit. */
.ct-zone { container: computers / inline-size; }
.ct-wrap { overflow-x: auto; margin: 0 -4px; padding: 0 4px; }
.ct-wrap:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: var(--radius-row); }
.ct { width: 100%; min-width: calc((var(--ct-accounts) * 5.25rem + 10.5rem) / .74); border-collapse: separate; border-spacing: 0; table-layout: fixed; }
.ct th, .ct td { padding: 0; border-bottom: 1px solid var(--line); text-align: left; font-weight: inherit; vertical-align: middle; }
.ct thead th { height: 60px; border-bottom-color: var(--line-2); }
.ct .c-pc { width: 26%; }
.ct .c-ns { width: 10.5rem; }
.ct-corner, .ct-nsh { display: block; padding: 0 12px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.ct-nsh { padding: 0 8px; }
.ct-ah { display: grid; gap: 3px; width: 100%; min-width: 0; min-height: 52px; padding: 4px 8px; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--ink); text-align: left; }
.ct-ah:hover { background: var(--row-hover); }
.ct-ah b { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 12.5px; font-weight: 650; white-space: nowrap; }
.ct-ah b > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.ct-ah small { min-width: 0; font-size: 11px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pc { display: grid; grid-template-columns: 18px minmax(0, 1fr); align-items: center; column-gap: 10px; width: 100%; min-height: 66px; padding: 8px 12px; border: 0; background: transparent; color: var(--ink); text-align: left; }
.pc:hover, .si:hover { background: var(--row-hover); }
.pc > svg { color: var(--ink-2); }
.pc .who { display: grid; min-width: 0; }
.pc b { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; font-weight: 650; }
.pc small { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 12px; color: var(--ink-2); }
.pc small > span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ct tr.current th, .ct tr.current td { background: var(--row-selected); }
.si { display: flex; align-items: center; gap: 7px; width: 100%; min-height: 66px; padding: 6px 8px; border: 0; background: transparent; color: var(--ink-2); font-size: 12.5px; text-align: left; white-space: nowrap; }
.si.warn { color: var(--warn-ink); font-weight: 600; }
.si.none { color: var(--ink-3); cursor: default; }
.si.none:hover { background: transparent; }
.si + .si { min-height: 33px; }
.si { overflow: hidden; }
.sv { display: inline-flex; align-items: center; gap: 7px; min-width: 0; }
.sv .w { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.dot.off { background: transparent; box-shadow: inset 0 0 0 1.8px var(--ink-3); }
.dot.warn { background: var(--gold); }
.ct tr.dim .pc b, .ct tr.dim .si { opacity: .62; }
.cl, .nsc > .cl { display: none; }
.ns { display: inline-flex; align-items: center; gap: 6px; height: 32px; margin-left: 8px; padding: 0 10px 0 12px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.ns:hover, .ns[aria-expanded="true"] { background: var(--row-hover); }
.ns[aria-busy="true"] { cursor: progress; opacity: .6; }
.ns > svg { flex: none; color: var(--ink-3); }
.ns-say { display: grid; }
.ns-say > * { grid-area: 1 / 1; }
.ns .sizer { visibility: hidden; }
.ns.ro { box-shadow: none; padding-left: 8px; color: var(--ink-2); font-weight: 500; cursor: default; }
.ns.ro.none { color: var(--ink-3); }
/* Past six accounts the computer column stays in view while the table scrolls sideways. */
.ct.many th[scope="row"], .ct.many .c-pc { position: sticky; left: 0; z-index: 1; background: var(--canvas); box-shadow: 1px 0 0 var(--line); }
.ct.many tr.current th[scope="row"] { background: linear-gradient(var(--row-selected), var(--row-selected)), var(--canvas); }
.ct-foot { display: grid; gap: 8px; padding-top: 14px; }
.foot { display: flex; align-items: flex-start; gap: 8px; margin: 0; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
.foot svg { flex: none; margin-top: 3px; }
.foot b { color: var(--ink-2); font-weight: 600; }
/* A phone or a narrow window: each computer is a stack of rows; nothing scrolls sideways. From
   1200 px the docked panel narrows this column instead of covering it: the table stays a table
   (scrolling sideways), so opening a panel never restacks the rows under the pointer (AEON-541). */
@media (max-width: 1199.98px) { @container computers (max-width: 560px) {
  .ct-wrap { overflow: visible; margin: 0; padding: 0; }
  .ct, .ct tbody, .ct tr, .ct th, .ct td { display: block; width: auto; min-width: 0; }
  .ct thead { display: none; }
  .ct tbody tr { display: grid; grid-template-columns: minmax(0, 1fr); padding: 6px 0 12px; border-bottom: 1px solid var(--line); }
  .ct th, .ct td { border: 0; }
  .ct td.none { display: none; }
  .pc { min-height: 56px; padding: 6px 4px; }
  .si { justify-content: space-between; min-height: 44px; padding: 0 4px 0 32px; border-radius: var(--radius-row); }
  .si + .si { min-height: 44px; }
  .cl { display: inline-flex; align-items: center; gap: 8px; min-width: 0; color: var(--ink); font-weight: 500; }
  .cl > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .ct td.nsc { display: flex; align-items: center; justify-content: space-between; min-height: 52px; padding: 0 0 0 32px; }
  .nsc > .cl { display: inline; font-size: 12.5px; color: var(--ink-2); }
  .ns { margin-left: 0; }
  .ct.many th[scope="row"] { position: static; background: transparent; box-shadow: none; }
} }
@media (pointer: coarse) { .ns, .si, .pc, .ct-ah { min-height: 44px; } }
</style>
