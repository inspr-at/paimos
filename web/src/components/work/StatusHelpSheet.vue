<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getStatusHelp } from '../../lib/api'
import { myWorkspaceRole } from '../../lib/authz'
import { defaultStatusHelp, days, ruleCopy, ruleLive } from '../../lib/statusDefinitions'
import { statusHelpRequest } from '../../lib/statusHelp'
import AppIcon from '../AppIcon.vue'
import StatusIcon from './StatusIcon.vue'
import TicketTypeIcon from './TicketTypeIcon.vue'
import { recurringWord } from '../../lib/recurrenceMarker'
import { useProfile } from '../../stores/profile'

const id = useId()
const profile = useProfile()
const recurringLabel = computed(() => recurringWord(profile.profile?.locale || navigator.language))
const recurringSample = { id: 'sample', project_id: 'sample', project_key: 'sample', number: 4, retired: false, trigger: { kind: 'time' as const, rrule: 'FREQ=WEEKLY;BYDAY=MO' } }
const dialog = ref<HTMLDialogElement>()
const closeButton = ref<HTMLButtonElement>()
const help = ref(defaultStatusHelp())
const loading = ref(false)
const error = ref('')
const router = useRouter()
const admin = computed(() => ['owner', 'admin'].includes(myWorkspaceRole()?.key ?? ''))
let controller: AbortController | undefined
let opener: HTMLElement | null = null
watch(statusHelpRequest, async request => {
  controller?.abort()
  if (!request) { dialog.value?.close(); return }
  opener = request.opener
  help.value = defaultStatusHelp(); error.value = ''; loading.value = true
  const active = new AbortController(); controller = active
  await nextTick()
  if (active.signal.aborted) return
  if (!dialog.value?.open) dialog.value?.showModal()
  closeButton.value?.focus()
  try {
    const value = await getStatusHelp(request.projectId, active.signal)
    if (!active.signal.aborted) help.value = value
  } catch {
    if (!active.signal.aborted) error.value = 'Could not load live workspace limits. Showing the defaults.'
  } finally { if (!active.signal.aborted) loading.value = false }
})
onBeforeUnmount(() => controller?.abort())
function close(restore = true) {
  statusHelpRequest.value = null
  dialog.value?.close()
  if (restore && opener?.isConnected) opener.focus({ preventScroll: true })
}
async function changeLimits() {
  close(false)
  await router.push('/settings/workspace#status-autopilot')
  await nextTick()
  const card = document.getElementById('status-autopilot')
  card?.scrollIntoView({ block: 'start', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  card?.querySelector<HTMLElement>('input:not(:disabled), button:not(:disabled)')?.focus({ preventScroll: true })
}
const source = computed(() => help.value.project_name
  ? help.value.autopilot.project_mode === 'inherit' ? `${help.value.project_name} follows the workspace` : `Set for ${help.value.project_name}; the workspace has it ${help.value.autopilot.enabled ? 'on' : 'off'}`
  : 'This workspace')
const triageWord = computed(() => { const mode = help.value.triage.mode; return mode[0].toUpperCase() + mode.slice(1) })
</script>

<template>
  <dialog ref="dialog" class="sheet" :aria-labelledby="`${id}-title`" @cancel.prevent="close()" @click="event => { if (event.target === dialog) close() }">
    <div class="sheet-card">
      <header><h2 :id="`${id}-title`">What the statuses mean</h2><button ref="closeButton" type="button" class="icon-btn sm" aria-label="Close the status help" @click="close()"><AppIcon name="close" :size="14" /></button></header>
      <p class="lead">One set of definitions for people and agents. The limits are this workspace’s; Status autopilot moves tickets by them, and every move is logged with its reason and can be undone.</p>
      <p v-if="loading" class="load-note" role="status">Loading live limits…</p>
      <p v-else-if="error" class="load-note" role="status">{{ error }}</p>
      <p v-else-if="help.limits_source === 'defaults'" class="load-note">Default limits; workspace settings are not present yet.</p>
      <div class="autos" :aria-label="`Autopilot in ${help.project_name ?? 'this workspace'}`">
        <div class="auto-fact"><span class="af-icon"><AppIcon name="sparkle" :size="14" /></span><span class="af-text"><span class="af-name">Status autopilot <span class="chip" :class="{ teal: help.autopilot.effective_enabled }">{{ help.autopilot.effective_enabled ? 'On' : 'Off' }}</span></span><span class="af-sub">{{ source }}</span></span></div>
        <div class="auto-fact"><span class="af-icon"><AppIcon name="inbox" :size="14" /></span><span class="af-text"><span class="af-name">Triage autopilot <span class="chip" :class="{ teal: help.triage.mode !== 'off' }">{{ triageWord }}</span></span><span class="af-sub">{{ help.triage.available ? source : 'Planned; not active yet' }}</span></span></div>
        <button v-if="admin" type="button" class="btn sm autos-action" @click="changeLimits">Change limits</button><span v-else class="admins-only autos-action">Only workspace admins change these.</span>
      </div>
      <table class="defs">
        <colgroup><col class="col-st"><col><col class="col-by"><col class="col-rule"></colgroup>
        <thead><tr><th scope="col">Status</th><th scope="col">Meaning</th><th scope="col">Set by</th><th scope="col">Automatic rule</th></tr></thead>
        <tbody>
          <template v-for="def in help.definitions" :key="def.state">
            <tr v-if="def.state === 'cancelled'" class="queued"><td class="st"><span class="st-cell"><AppIcon name="inbox" :size="14" />{{ help.queued.label }}</span></td><td class="meaning">{{ help.queued.meaning }}</td><td data-label="Set by">Work queue</td><td data-label="Automatic rule">Open or Blocked in the queue; not a status</td></tr>
            <tr v-if="def.state === 'cancelled'" class="recurring"><td class="st"><span class="st-cell"><TicketTypeIcon kind="ticket" :recurrence="recurringSample" />{{ recurringLabel }}</span></td><td class="meaning">{{ (help.recurring ?? defaultStatusHelp().recurring)?.meaning }}</td><td data-label="Set by">Recurring work</td><td data-label="Automatic rule">A marker, not a status</td></tr>
            <tr v-if="def.state === 'cancelled'" class="group"><td colspan="4"><span class="eyebrow">Exits</span></td></tr>
            <tr :data-status="def.state"><td class="st"><span class="st-cell"><StatusIcon :state="def.state" />{{ def.label }}</span></td><td class="meaning">{{ def.meaning }}</td><td data-label="Set by">{{ def.set_by }}</td><td data-label="Automatic rule">
              <div v-if="def.state === 'blocked'" class="rule-line">Blocker required</div>
              <div v-for="key in def.rules" :key="key" class="rule-line" :class="{ 'rule-off': !ruleLive(help, key) }" :data-rule="key">
                <span v-if="!ruleLive(help, key)" class="off-chip">Off</span>
                <template v-if="ruleCopy(key).event">{{ ruleCopy(key).event }}</template>
                <template v-else>{{ ruleCopy(key).before }} <span class="limit">{{ days(help.autopilot.rules[key].days!) }}</span> <AppIcon v-if="ruleCopy(key).arrowAfterLimit" name="arrow" :size="11" class="rule-arrow" /> {{ ruleCopy(key).after }}<template v-if="ruleCopy(key).to"> <AppIcon name="arrow" :size="11" class="rule-arrow" /> {{ ruleCopy(key).to }}</template></template>
              </div>
              <div v-if="def.state === 'accepted'" class="rule-line">Final</div><span v-else-if="!def.rules.length" class="faint">—</span>
            </td></tr>
          </template>
        </tbody>
      </table>
      <div class="flag-explain"><span class="hc-sample"><AppIcon name="person-check" :size="14" /><span><strong>Needs a human check:</strong> Touch ID on the paired Mac</span></span><p>A ticket can carry a human check: something only a person can confirm. Automatic moves to Delivered and Accepted skip a flagged ticket and leave a comment; once someone marks it checked, the rules apply again.</p></div>
      <p class="help-foot">Agents read the same definitions from the API and <code>aeon status help --json</code>.</p>
    </div>
  </dialog>
</template>

<style scoped>
.sheet { width: min(860px, calc(100vw - 24px)); max-width: none; max-height: calc(100dvh - 48px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.sheet::backdrop { background: var(--scrim); backdrop-filter: blur(3px); }
.sheet-card { max-height: calc(100dvh - 48px); overflow: auto; overscroll-behavior: contain; padding: 22px 26px 20px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
header { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 6px; }
h2 { font-size: 19px; }
.lead { font-size: 13.5px; max-width: 70ch; }
.load-note { color: var(--ink-3); font-size: 12px; margin-top: 8px; }
.autos { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)) auto; align-items: center; gap: 8px 10px; margin-top: 16px; padding: 10px 12px; border-radius: 12px; background: var(--surface-2); }
.auto-fact { display: flex; align-items: center; gap: 10px; min-width: 0; }
.af-icon { display: grid; place-items: center; flex-shrink: 0; width: 28px; height: 28px; border-radius: 9px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.af-text { display: grid; min-width: 0; }
.af-name { display: flex; align-items: center; gap: 8px; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.af-sub { font-size: 12px; color: var(--ink-2); overflow-wrap: anywhere; }
.autos-action { justify-self: end; }
.admins-only { font-size: 12px; color: var(--ink-3); }
.defs { width: 100%; margin-top: 14px; border-collapse: collapse; font-size: 13px; }
.defs th { padding: 0 10px 6px 0; text-align: left; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); border-bottom: 1px solid var(--line-2); }
.defs td { padding: 8px 10px 8px 0; vertical-align: top; border-bottom: 1px solid var(--line); color: var(--ink-2); line-height: 1.45; }
.defs tr:last-child td { border-bottom: 0; }
.defs td.st { white-space: nowrap; color: var(--ink); font-weight: 600; }
.st-cell { display: inline-flex; align-items: center; gap: 8px; min-height: 19px; }
.defs td.meaning { color: var(--ink); }
.defs .group td { padding: 14px 0 4px; border-bottom: 1px solid var(--line-2); }
.col-st { width: 128px; } .col-by { width: 150px; } .col-rule { width: 30%; }
.limit { display: inline-flex; align-items: center; height: 19px; padding: 0 6px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink); font: 500 11.5px/1 var(--mono); font-variant-numeric: tabular-nums; white-space: nowrap; }
.rule-off { color: var(--ink-3); }
.rule-off .limit { color: var(--ink-3); background: transparent; }
.off-chip { display: inline-flex; align-items: center; height: 18px; margin-right: 4px; padding: 0 6px; border-radius: 999px; box-shadow: inset 0 0 0 1px var(--line-2); font: 500 10px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); vertical-align: 1px; }
.rule-line + .rule-line { margin-top: 4px; }
.rule-arrow { display: inline-block; vertical-align: -1px; margin: 0 2px; }
.flag-explain { display: grid; gap: 8px; margin-top: 16px; padding: 12px 14px; border-radius: 12px; background: var(--surface-2); }
.flag-explain p { font-size: 13px; line-height: 1.5; }
.hc-sample { display: inline-flex; align-items: center; gap: 8px; justify-self: start; max-width: 100%; padding: 5px 10px; border-radius: 8px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 28%, transparent); font-size: 12.5px; color: var(--ink); }
.hc-sample svg { color: var(--gold-ink); }
.hc-sample span { min-width: 0; overflow-wrap: anywhere; }
.help-foot { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; margin-top: 14px; font-size: 12px; color: var(--ink-3); }
.help-foot code { padding: 2px 6px; border-radius: 6px; background: var(--code-bg); color: var(--ink-2); font-size: 11.5px; }
@media (max-width: 700px) {
  .sheet-card { padding: 18px; } .autos { grid-template-columns: minmax(0, 1fr); } .autos-action { justify-self: start; }
  .defs colgroup { display: none; } .defs thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .defs, .defs tbody, .defs tr, .defs td { display: block; width: auto; } .defs tr { padding: 10px 0; border-bottom: 1px solid var(--line); }
  .defs tr:last-child, .defs .group { border-bottom: 0; } .defs td { padding: 2px 0; border: 0; }
  .defs td[data-label]::before { content: attr(data-label); display: block; margin-top: 4px; font: 500 10px/1.6 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
  .defs .group td { padding: 6px 0 0; border-bottom: 0; }
}
</style>
