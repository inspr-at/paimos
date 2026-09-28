<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { removeSession, removeStaleSessions, type HarnessSession } from '../../lib/agents'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ session?: HarnessSession; label?: string; projectIds?: string[]; menu?: boolean }>()
const emit = defineEmits<{ opened: [] }>()
const agents = useAgents()
const identity = useSession()
const router = useRouter()
const busy = ref(false)
const projects = computed(() => (props.projectIds ?? []).filter(id => can('harness.read', id)))
const allowed = computed(() => identity.identity?.principal.kind === 'person' && (props.session
  ? !props.session.archived_at && can('harness.read', props.session.project_id)
  : projects.value.length > 0))
async function open() {
  if (busy.value || !allowed.value) return
  // Capture the target before a row menu closes or polling replaces its props.
  const target = props.session
  const projectIds = [...projects.value]
  const label = props.label || target?.display_label || 'this session'
  const decision = confirmAction({
    title: target ? `Remove ${label} from Agents?` : 'Remove all stopped/stale sessions?',
    body: target ? 'The process is not stopped; late heartbeats are ignored.' : 'Remove sessions without an accepted heartbeat for over 15 minutes; processes are not stopped.',
    confirmLabel: target ? 'Remove' : 'Remove all stopped/stale',
  })
  emit('opened')
  if (!await decision) return
  busy.value = true
  let count = 0
  try {
    if (target) {
      const result = await removeSession(target, 'Removed from Agents by a person')
      agents.recordRemoval(result.session)
      count++
      if (router.currentRoute.value.params.sessionId === target.id) await router.replace('/agents')
    } else {
      for (const project of projectIds) {
        const result = await removeStaleSessions(project, 'Removed from Agents: no accepted heartbeat for over 15 minutes')
        for (const item of result.items) {
          agents.recordRemoval(item.session)
          count++
          if (router.currentRoute.value.params.sessionId === item.session.id) await router.replace('/agents')
        }
      }
    }
    toast(count ? `${count === 1 ? 'Record removed' : `${count} records removed`}; ${count === 1 ? 'process not stopped' : 'processes not stopped'} by removal. History is in Removed.` : 'No sessions are eligible for removal.')
  } catch (error) {
    toast(`${count ? `${count} records removed. ` : ''}${error instanceof Error ? error.message : 'Removal failed. Please retry.'}`, { tone: 'error' })
  } finally { busy.value = false }
}
</script>

<template>
  <button v-if="allowed" type="button" :role="menu ? 'menuitem' : undefined" :class="menu ? 'menu-item' : 'btn sm remove-session'" :aria-label="session ? `Remove ${label || session.display_label || 'session'}` : undefined" :disabled="busy" @click.stop="open">
    <AppIcon name="close" :size="14" /><span>{{ session ? 'Remove' : 'Remove all stopped/stale' }}</span>
  </button>
</template>

<style scoped>
.remove-session { min-height: 40px; position: relative; z-index: 1; pointer-events: auto; white-space: nowrap; }
</style>
