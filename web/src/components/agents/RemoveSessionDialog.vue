<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HarnessSession } from '../../lib/agents'
import AppIcon from '../AppIcon.vue'
import { useSessionRemoval } from './sessionRemoval'

// The session panel's quiet Remove, beside Interrupt, Stop and Recover. Rows
// offer the same action from their overflow menu (see SessionList).
defineProps<{ session: HarnessSession; label: string }>()
const { busy, canRemove, removeOne } = useSessionRemoval()
</script>

<template>
  <button
    v-if="canRemove(session)" type="button" class="btn sm ghost remove-session" :aria-label="`Remove ${label} from Agents`"
    data-tip="Hide this session; its process is not stopped" :disabled="busy" @click.stop="removeOne(session, label)"
  ><AppIcon name="archive" :size="14" />Remove</button>
</template>
