<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HarnessSession } from '../../lib/agents'
import AppIcon from '../AppIcon.vue'
import { useSessionRemoval } from './sessionRemoval'

// The session panel's quiet Remove, beside Interrupt, Stop and Recover. Rows
// offer the same action from their overflow menu (see SessionList).
// quick (No heartbeat, Lost contact, stopped): one click and an undo toast.
const props = defineProps<{ session: HarnessSession; label: string; hideTrigger?: boolean; quick?: boolean }>()
const { busy, canRemove, removeOne } = useSessionRemoval()
function remove() { void removeOne(props.session, props.label, props.quick) }
defineExpose({ remove, busy })
</script>

<template>
  <button
    v-if="canRemove(session) && !hideTrigger" type="button" class="btn sm ghost remove-session" :aria-label="`Remove ${label}`"
    :data-tip="quick ? 'Remove · undo right after' : undefined" :disabled="busy" @click.stop="remove"
  ><AppIcon name="trash" :size="14" />{{ quick ? 'Remove' : 'Remove…' }}</button>
</template>
