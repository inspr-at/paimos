<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { canRemoveLead, LEAD_WORDS, type ProjectLead } from '../../lib/lead'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

const props = defineProps<{ projectId: string; projectKey: string; lead: ProjectLead }>()
const leads = useProjectLeads(), session = useSession(), router = useRouter()
const menu = ref<HTMLElement | null>(null)
const confirmation = ref<{ project: string; revision: number; viewer: string } | null>(null)
const error = ref('')
const viewer = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
const removable = computed(() => session.identity?.principal.kind === 'person' && can('harness.control', props.projectId) && canRemoveLead(props.lead))
const available = computed(() => !!props.lead.session_id || removable.value)
const busy = computed(() => !!leads.busy[props.projectId])
const label = computed(() => `More ${props.projectKey} ${LEAD_WORDS.l} actions`)
function close(restore = false) {
  if (restore) menu.value?.focus()
  menu.value = null; confirmation.value = null; error.value = ''
}
watch([() => props.projectId, () => props.lead.revision, () => props.lead.session_id, viewer, removable], () => close())
function toggle(event: MouseEvent) { if (menu.value) close(); else menu.value = event.currentTarget as HTMLElement }
function cancel() { confirmation.value = null; error.value = '' }
function openSession() { const id = props.lead.session_id; close(); if (id) void router.push(`/agents/${id}`) }
async function remove() {
  if (!removable.value || busy.value) return
  if (!confirmation.value) {
    confirmation.value = { project: props.projectId, revision: props.lead.revision, viewer: viewer.value }
    error.value = ''
    return
  }
  const target = confirmation.value
  const current = () => target.project === props.projectId && target.revision === props.lead.revision && target.viewer === viewer.value && confirmation.value === target
  if (!current()) { close(); return }
  try {
    const result = await leads.remove(target.project, target.revision)
    if (current() && result) close(true)
  } catch (e) {
    if (current()) error.value = e instanceof Error ? e.message : 'The lead could not be removed.'
  }
}
</script>

<template>
  <button v-if="available" type="button" class="icon-btn lead-menu-trigger" :aria-label="label" aria-haspopup="dialog" :aria-expanded="!!menu" data-act="lead-menu" @click="toggle"><AppIcon name="more" /></button>
  <FloatingPanel v-if="menu" :anchor="menu" align="end" :width="300" :label="label" cycle @close="close">
    <div class="lead-menu">
      <button v-if="lead.session_id" type="button" class="session-action" @click="openSession"><AppIcon name="agent" /><span>Open session</span></button>
      <template v-if="removable">
        <div class="remove-actions">
          <button type="button" class="btn remove-action" :aria-disabled="busy" :aria-describedby="`remove-lead-note-${projectId}`" @click="remove"><AppIcon name="trash" :size="15" />Remove</button>
          <button type="button" class="btn" :class="{ concealed: !confirmation }" :disabled="busy || !confirmation" @click="cancel">Cancel</button>
        </div>
        <p :id="`remove-lead-note-${projectId}`" class="remove-note" aria-live="polite">{{ confirmation ? `Remove the ${projectKey} ${LEAD_WORDS.l}? Queued work stays queued.` : 'Only a lead that never started can be removed.' }}</p>
        <p v-if="error" class="remove-error" role="alert">{{ error }}</p>
      </template>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.lead-menu { display: grid; }
.session-action { display: flex; align-items: center; gap: 10px; padding: 10px; border: 0; background: transparent; color: var(--ink); text-align: left; cursor: pointer; }
.session-action:hover, .session-action:focus-visible { background: var(--row-hover); }
.remove-actions { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
.remove-action { color: var(--danger); }
.remove-action[aria-disabled="true"] { opacity: .55; cursor: default; }
.concealed { visibility: hidden; }
.remove-note, .remove-error { margin: 8px 4px 4px; color: var(--ink-2); font-size: 12.5px; line-height: 1.5; overflow-wrap: anywhere; }
.remove-error { color: var(--danger); }
@media (pointer: coarse) { .lead-menu-trigger, .remove-actions .btn, .session-action { min-height: 44px; } .lead-menu-trigger { min-width: 44px; } }
</style>
