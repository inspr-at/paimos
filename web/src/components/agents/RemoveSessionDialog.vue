<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HarnessSession } from '../../lib/agents'
import AppIcon from '../AppIcon.vue'
import { useSessionRemoval } from './sessionRemoval'

// The session panel's quiet Remove, beside Interrupt, Stop and Recover. Rows
// offer the same action from their overflow menu (see SessionList).
const props = defineProps<{ session: HarnessSession; label: string; hideTrigger?: boolean }>()
const { busy, canRemove, removeOne } = useSessionRemoval()
function remove() { void removeOne(props.session, props.label) }
defineExpose({ remove, busy })
</script>

<template>
  <button
    v-if="canRemove(session) && !hideTrigger" type="button" class="btn sm ghost remove-session" :aria-label="`Remove ${label} from Agents`"
    data-tip="Hide this session; its process is not stopped" :disabled="busy" @click.stop="remove"
  ><AppIcon name="archive" :size="14" />Remove</button>
</template>
