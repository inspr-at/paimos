<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { controlBlocked } from '../../lib/agentState'
import { controlPermitted } from '../../lib/managedControl'
import type { Approval, SessionControl } from '../../lib/agents'
import { closeLeadPanel, leadOverlay } from '../../lib/leadOverlay'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useAgents, type SessionView } from '../../stores/agents'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import { useRouter } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import SessionPanel from '../agents/SessionPanel.vue'
import LeadContent from './LeadContent.vue'

// Project and ticket entry points share the Agents panel. A project with no
// session yet has real Lead content only; it never fabricates a session/chat.
const props = defineProps<{ projectId: string; projectKey: string; routeKey: string }>()
const agents = useAgents(), leads = useProjectLeads(), auth = useSession(), router = useRouter()
const lead = computed(() => leads.views[props.projectId]?.lead)
const selected = computed(() => {
  const row = lead.value?.session_id ? agents.sessionById(lead.value.session_id) : null
  return row ? agents.viewOf(row) : undefined
})
let epoch = 0, detail = 0
const loading = ref(true)
async function readSelected() {
  const turn = ++detail, scope = epoch, project = props.projectId, id = lead.value?.session_id
  loading.value = !!id
  if (!id) return
  try { await agents.loadSessionDetail(project, id) } catch { /* The panel exposes its unavailable state. */ }
  finally { if (turn === detail && scope === epoch) loading.value = false }
}
watch(() => [props.projectId, auth.identity?.tenant.id, auth.identity?.principal.id], async () => {
  const turn = ++epoch, project = props.projectId
  detail++; loading.value = true
  await leads.load(project)
  if (turn === epoch) await readSelected()
}, { immediate: true })
watch(() => lead.value?.session_id, () => { void readSelected() })
onBeforeUnmount(() => { epoch++ })
const controlBlock = (view: SessionView, kind: SessionControl['kind']) => controlBlocked(view.session, kind, view.name, controlPermitted(view.session, { person: auth.identity?.principal.kind === 'person', can }), agents.controls[view.session.id])
async function control(view: SessionView, kind: SessionControl['kind']) {
  const turn = epoch, id = view.session.id
  if (controlBlock(view, kind)) return
  if (kind === 'stop' && !await confirmAction({ title: `Stop ${view.name}?`, danger: true, confirmLabel: 'Stop session', body: 'Ends this session after its current step. It cannot be resumed.' })) return
  if (turn !== epoch || selected.value?.session.id !== id || controlBlock(selected.value, kind)) return
  try { await agents.control(view, kind); if (turn === epoch && selected.value?.session.id === id) toast(`${kind === 'stop' ? 'Stop' : 'Interrupt'} requested.`) }
  catch (e) { if (turn === epoch) toast(e instanceof Error ? e.message : 'The request failed.', { tone: 'error' }) }
}
function review(approval: Approval) { closeLeadPanel(false); void router.push({ path: '/decision-desk', query: { item: `a:${approval.id}` } }) }
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || event.key !== 'Escape' || event.altKey || event.ctrlKey || event.metaKey || leadOverlay.start || leadOverlay.pause) return
  const target = event.target as HTMLElement
  event.preventDefault(); event.stopPropagation()
  if (target.closest('input,textarea,select,[contenteditable="true"]')) target.blur()
  else closeLeadPanel()
}
</script>

<template>
  <SessionPanel v-if="lead?.session_id" class="project-lead-panel" :section="leadOverlay.section" @keydown="keys" :view="selected" :loading="loading && !selected" :now="agents.now" :can-write="can('harness.control', projectId)" :control-block="controlBlock" @close="closeLeadPanel()" @control="control" @review="review" />
  <aside v-else class="lead-panel" aria-label="Lead details" @keydown="keys">
    <header class="pane-bar"><h2>{{ projectKey }} lead</h2><button type="button" class="icon-btn" aria-label="Close details" @click="closeLeadPanel()"><AppIcon name="close" :size="15" /></button></header>
    <LeadContent :project-id="projectId" :project-key="projectKey" :route-key="routeKey" />
  </aside>
</template>

<style scoped>
.project-lead-panel { z-index: 40; }
.lead-panel { position: fixed; z-index: 40; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h, 0px) + 10px); width: min(560px, calc(100vw - 20px)); display: flex; flex-direction: column; min-width: 0; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: var(--surface-raised); box-shadow: var(--shadow-pop), var(--shadow); }
.pane-bar { display: flex; align-items: center; gap: 12px; padding: 14px 14px 10px 16px; border-bottom: 1px solid var(--line); }
.pane-bar h2 { flex: 1; margin: 0; font-size: 17px; }
@media (min-width: 1100px) { .lead-panel { width: var(--panel-w); } }
@media (max-width: 720px) {
  .lead-panel { z-index: 58; inset: 0; width: auto; border-radius: 0; border: 0; background: var(--canvas); }
  .pane-bar { padding-top: max(8px, env(safe-area-inset-top)); }
  .pane-bar .icon-btn { width: 44px; height: 44px; }
}
</style>
