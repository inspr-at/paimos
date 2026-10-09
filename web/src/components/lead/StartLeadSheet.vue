<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { listPairingComputers, type PairingView } from '../../lib/agentPairing'
import { canLaunchLead, leadLaunchReason, LEAD_LAUNCH_OFF, LEAD_WORDS, modelByRole, readLeadSettings, writeLeadSettings, type LeadSettings } from '../../lib/lead'
import { closeLeadSheet, leadOverlay } from '../../lib/leadOverlay'
import { toast } from '../../lib/toast'
import { useProjectLeads } from '../../stores/projectLeads'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// AEON-741: Start lead is one decision. Host Automatic by default, model by
// role, limits read-only from the dial. Actions sit above content that grows
// (pattern A); phones get a full-height sheet with the bar pinned (pattern B).
const props = defineProps<{ projects: string[]; showProject?: boolean }>()
const w = LEAD_WORDS
const leads = useProjectLeads(), projectStore = useProjects(), queue = useWorkQueue(), session = useSession()
const dialog = ref<HTMLDialogElement>(), startButton = ref<HTMLButtonElement>()
const chosen = ref(props.projects[0] ?? '')
const project = computed(() => projectStore.byId(chosen.value))
const key = computed(() => project.value?.routeKey ?? 'this project')
const settings = ref<LeadSettings | null>(null), settingsFor = ref('')
const computers = ref<PairingView[] | null>(null)
const hostOpen = ref(false), host = ref<'auto' | string>('auto'), hostNote = ref('')
const dial = ref<number | null>(null)
const busy = ref(false), error = ref('')
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
let generation = 0, submission = 0
const identity = () => `${session.identity?.tenant.id}:${session.identity?.principal.id}`

const queued = computed(() => queue.snapshots[chosen.value]?.items.length ?? null)
const owner = computed(() => !!settings.value && !settings.value.details_redacted)
const savedHost = computed(() => settings.value?.overrides?.allowed_host_ids?.length === 1 ? settings.value.overrides.allowed_host_ids[0]! : 'auto')
const choices = computed(() => (computers.value ?? []).filter(c => c.computer_id && c.computer_state && c.computer_state !== 'revoked'))
const hostName = (id: string) => choices.value.find(c => c.computer_id === id)?.computer_name ?? 'the chosen computer'
const model = computed(() => modelByRole(settings.value))
const selectedLead = computed(() => leads.views[chosen.value]?.lead)
const launchAvailable = computed(() => canLaunchLead(selectedLead.value) && settings.value?.automatic_launch_enabled === true)
const launchReason = computed(() => settings.value?.automatic_launch_enabled === false ? LEAD_LAUNCH_OFF : leadLaunchReason(selectedLead.value))

async function load() {
  const turn = ++generation, who = identity(), id = chosen.value
  error.value = ''; settings.value = null; settingsFor.value = id; host.value = 'auto'; hostOpen.value = false; hostNote.value = ''
  void queue.load(id)
  await Promise.allSettled([
    leads.loadLead(id),
    readLeadSettings(id).then(value => { if (turn === generation && who === identity() && id === chosen.value) { settings.value = value; host.value = savedHost.value } }),
    computers.value ? Promise.resolve() : listPairingComputers().then(list => { if (turn === generation && who === identity()) computers.value = list }).catch(() => { if (turn === generation) computers.value = [] }),
    dial.value !== null ? Promise.resolve() : api('/agents/plan').then(async r => { if (r.ok && turn === generation && who === identity()) dial.value = (await r.json() as { total: number }).total }),
  ])
}
watch(chosen, () => { void load() })
watch(() => session.identity?.principal.id, () => close())

function pickHost(id: string, disabled: boolean) {
  if (disabled) { hostNote.value = `${hostName(id)} is being removed; it can’t run agents until it is connected again.`; return }
  host.value = id
  hostNote.value = id === 'auto' ? `Automatic: the ${w.l} runs where there is room now; its workers go wherever the checks pass.` : `The ${w.l} stays on ${hostName(id)}. If it is offline, it waits instead of moving.`
}
function choose(id: string) { if (!busy.value) chosen.value = id }
// Submission state is its own: switching what loads never strands the sheet busy.
async function start() {
  if (busy.value || !launchAvailable.value) return
  const turn = generation, mine = ++submission, who = identity(), id = chosen.value, lead = leads.views[id]?.lead
  if (!lead) { error.value = `The ${w.l} could not be read. Try again.`; return }
  if (lead.state !== 'none' && !(lead.state === 'cannot_start' && lead.reason === 'owner_revoked')) { error.value = `${key.value} already has a ${w.l}. Open it from the project.`; return }
  busy.value = true; error.value = ''
  try {
    // A changed host is saved first; if that fails, nothing is requested.
    if (owner.value && settings.value && host.value !== savedHost.value) {
      const overrides = { ...(settings.value.overrides ?? {}), allowed_host_ids: host.value === 'auto' ? null : [host.value] }
      const saved = await writeLeadSettings(id, settings.value.revision, overrides)
      if (turn !== generation || who !== identity()) return
      settings.value = saved
    }
    const next = await leads.start(id)
    if (turn !== generation || who !== identity() || !next) return
    toast(`${key.value} ${w.l} requested`, { timeout: 5200 })
    close(false)
  } catch (e) {
    if (turn === generation && who === identity()) error.value = e instanceof Error ? e.message : `The ${w.l} was not started.`
  } finally { if (mine === submission) busy.value = false }
}
function close(restore = true) { generation++; dialog.value?.close(); closeLeadSheet(restore) }
function keydown(event: KeyboardEvent) {
  const field = (event.target as HTMLElement).matches('input,textarea,select,[contenteditable="true"]')
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); if (field) (event.target as HTMLElement).blur(); else close() }
  else if (event.key === 'Enter' && (mac ? event.metaKey : event.ctrlKey) && !event.altKey && !event.shiftKey) { event.preventDefault(); event.stopPropagation(); void start() }
}
onMounted(async () => { dialog.value?.showModal(); await nextTick(); startButton.value?.focus(); void load() })
onBeforeUnmount(() => { generation++ })
// Keep the identity of the sheet's project when the overlay changes underneath it.
watch(() => leadOverlay.start, value => { if (!value) generation++ })
</script>

<template>
  <dialog ref="dialog" class="lead-sheet" aria-labelledby="start-lead-title" @cancel.prevent="close()" @keydown="keydown">
    <header class="sheet-head"><h2 id="start-lead-title">Start {{ w.l }} for {{ key }}</h2><button type="button" class="icon-btn" aria-label="Close" @click="close()"><AppIcon name="close" /></button></header>
    <div class="sheet-acts">
      <button ref="startButton" type="button" class="btn primary" data-act="start" :aria-disabled="busy || !launchAvailable" :aria-describedby="!launchAvailable ? 'lead-launch-reason' : undefined" @click="start"><AppIcon name="play" :size="15" /><span>Start {{ w.l }}</span><KeyCap k="mod" class="hint" /><KeyCap k="enter" class="hint" /></button>
      <button type="button" class="btn ghost" data-act="cancel" @click="close()">Cancel<KeyCap k="Esc" class="hint" /></button>
    </div>
    <div class="sheet-body">
      <p v-if="!launchAvailable" id="lead-launch-reason" class="intro" data-launch-reason>{{ launchReason }}</p>
      <p v-if="error" class="sheet-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <p class="intro">The {{ w.l }} picks up queued work in {{ key }}, sizes it and starts workers within your limits. You only see its questions, on the Decision Desk.</p>
      <div class="facts-list">
        <div v-if="projects.length > 1" class="fl-row" data-row="project">
          <span class="fl-label">Project</span>
          <p class="fl-value">{{ key }}<small>{{ projects.length }} projects have no {{ w.l }}. One per project.</small></p>
          <div class="choices project-choices" role="radiogroup" aria-label="Project">
            <button v-for="id in projects" :key="id" type="button" class="choice" role="radio" :aria-checked="chosen === id" :aria-disabled="busy && chosen !== id" @click="choose(id)">
              <span class="radio" /><span class="glyph"><AppIcon name="folder" :size="14" /></span><span><b>{{ projectStore.byId(id)?.routeKey ?? '—' }}</b><span class="small">{{ projectStore.byId(id)?.title ?? '' }}</span></span><span />
            </button>
          </div>
        </div>
        <div v-else-if="showProject" class="fl-row" data-row="project"><span class="fl-label">Project</span><p class="fl-value">{{ key }}<small>The only project without a {{ w.l }}.</small></p></div>
        <div class="fl-row" data-row="host">
          <span class="fl-label">Runs on</span>
          <p class="fl-value" v-if="hostOpen">Choose a computer or Automatic<small>Workers still go wherever the checks pass.</small></p>
          <p class="fl-value" v-else-if="host === 'auto'">Automatic<small>Wherever there is room when work starts.</small></p>
          <p class="fl-value" v-else>{{ hostName(host) }}<small>Always this computer. Workers still go wherever the checks pass.</small></p>
          <button v-if="owner && choices.length" type="button" class="btn sm ghost" :aria-expanded="hostOpen" aria-controls="lead-host-choices" data-act="host" @click="hostOpen = !hostOpen"><span class="stack"><span>{{ hostOpen ? 'Done' : 'Change' }}</span><span aria-hidden="true">Change</span></span></button>
          <div v-if="hostOpen" id="lead-host-choices" class="choices">
            <div role="radiogroup" aria-label="Host">
              <button type="button" class="choice" role="radio" :aria-checked="host === 'auto'" @click="pickHost('auto', false)"><span class="radio" /><span class="glyph"><AppIcon name="gauge" :size="14" /></span><span><b>Automatic</b><span class="small">Wherever there is room when work starts</span></span><span class="small faint">Default</span></button>
              <button v-for="c in choices" :key="c.computer_id!" type="button" class="choice" role="radio" :aria-checked="host === c.computer_id" :aria-disabled="c.computer_state === 'draining'" @click="pickHost(c.computer_id!, c.computer_state === 'draining')"><span class="radio" /><span class="glyph"><AppIcon name="monitor" :size="14" /></span><span><b>{{ c.computer_name }}</b><span class="small">{{ c.computer_state === 'draining' ? 'Removed · cleanup pending' : `Connected · ${c.platform}` }}</span></span><span class="small faint">{{ c.computer_state === 'draining' ? 'Can’t run' : 'Ready' }}</span></button>
            </div>
            <p class="choice-note" aria-live="polite">{{ hostNote || `Automatic: the ${w.l} runs where there is room now; its workers go wherever the checks pass.` }}</p>
          </div>
        </div>
        <div class="fl-row" data-row="model"><span class="fl-label">Model</span><p class="fl-value">By role<small>{{ model ? `Today ${model}. ` : '' }}From Model preferences.</small></p></div>
        <div class="fl-row" data-row="works"><span class="fl-label">Works on</span><p class="fl-value">Queued work in {{ key }}<template v-if="queued !== null"> · {{ queued }} now</template><small>In your order. Agents work on leaves; parents follow their children.</small></p></div>
        <div class="fl-row" data-row="limits"><span class="fl-label">Limits</span><p class="fl-value">Your dial<template v-if="dial !== null">: up to {{ dial }} agents at once</template>, all projects together<small>The {{ w.l }} counts as one. Before every start PAIMOS checks the dial, harness limits, account room and host load. If any of them can’t be read, nothing starts.</small></p></div>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.lead-sheet { position: fixed; inset: 12vh auto auto 50%; transform: translateX(-50%); margin: 0; width: min(560px, calc(100vw - 32px)); max-height: calc(100dvh - 12vh - 24px); padding: 0; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: var(--surface-raised); color: var(--ink); box-shadow: var(--shadow-pop); overflow: hidden; }
.lead-sheet[open] { display: flex; flex-direction: column; }
.lead-sheet::backdrop { background: var(--scrim); }
.sheet-head { display: flex; align-items: center; gap: 12px; padding: 16px 12px 0 20px; }
.sheet-head h2 { flex: 1; min-width: 0; margin: 0; font: 400 21px/1.25 var(--serif); letter-spacing: -.015em; }
.sheet-acts { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 14px 20px 16px; border-bottom: 1px solid var(--line); flex: none; }
.sheet-acts .btn[aria-disabled="true"] { opacity: .55; cursor: default; }
.sheet-body { overflow: auto; padding: 16px 20px 22px; }
.intro { margin: 0; color: var(--ink); font-size: 14px; }
.sheet-error { display: flex; align-items: center; gap: 8px; margin: 0 0 12px; color: var(--danger); font-size: 13px; }
.facts-list { margin: 16px 0 0; border-top: 1px solid var(--line); }
.fl-row { display: grid; grid-template-columns: 112px minmax(0, 1fr) auto; align-items: start; gap: 4px 14px; padding: 12px 0; border-bottom: 1px solid var(--line); }
.fl-label { padding-top: 1px; font: 500 10.5px/1.6 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.fl-value { min-width: 0; margin: 0; color: var(--ink); font-size: 13.5px; }
.fl-value small { display: block; margin-top: 2px; color: var(--ink-3); font-size: 12.5px; }
.fl-row .btn { align-self: start; margin-top: -5px; }
.stack { display: inline-grid; }
.stack > * { grid-area: 1 / 1; }
.stack > [aria-hidden="true"] { visibility: hidden; }
.choices { grid-column: 1 / -1; margin-top: 6px; }
.project-choices { max-height: calc(52px * 5); overflow: auto; }
.choice { display: grid; grid-template-columns: 18px 28px minmax(0, 1fr) auto; align-items: center; gap: 10px; width: 100%; height: 52px; padding: 0 10px; border: 0; border-radius: var(--radius-row, 8px); background: transparent; color: var(--ink); text-align: left; cursor: pointer; }
.choice:hover:not([aria-disabled="true"]) { background: var(--row-hover); }
.choice[aria-checked="true"] { background: var(--row-selected); }
.choice .radio { width: 16px; height: 16px; border-radius: 50%; box-shadow: inset 0 0 0 1.5px var(--line-2); }
.choice[aria-checked="true"] .radio { box-shadow: inset 0 0 0 5px var(--teal); }
.choice b { display: block; color: var(--ink); font-size: 13.5px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.choice .small { display: block; font-size: 12.5px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.choice .faint { color: var(--ink-3); }
.choice[aria-disabled="true"] { cursor: default; }
.choice[aria-disabled="true"] b, .choice[aria-disabled="true"] .glyph { opacity: .55; }
.choice .glyph { width: 28px; height: 28px; display: grid; place-items: center; border-radius: 50%; background: var(--surface-sunken); color: var(--ink-2); }
.choice-note { min-height: 38px; margin: 0; padding: 8px 10px 0; font-size: 12.5px; color: var(--ink-3); }
@media (max-width: 720px) {
  /* Phone: full height, pinned header, scrolling body, action bar pinned at the bottom (pattern B). */
  .lead-sheet { inset: 0; transform: none; width: 100%; max-width: none; height: 100dvh; max-height: none; border-radius: 0; border: 0; }
  .sheet-head { padding: max(12px, env(safe-area-inset-top)) 8px 10px 16px; border-bottom: 1px solid var(--line); }
  .sheet-body { flex: 1; padding: 16px; }
  .sheet-acts { order: 3; border-bottom: 0; border-top: 1px solid var(--line); padding: 10px 16px calc(10px + env(safe-area-inset-bottom)); flex-direction: row-reverse; }
  .sheet-acts .btn { flex: 1; min-height: 44px; }
  .fl-row { grid-template-columns: minmax(0, 1fr) auto; }
  .fl-label { grid-column: 1 / -1; }
}
@media (pointer: coarse) { .choice { height: 56px; } .hint { display: none; } }
</style>
