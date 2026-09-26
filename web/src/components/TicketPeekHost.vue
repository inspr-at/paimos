<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import ReleaseTicketPanel from './releases/ReleaseTicketPanel.vue'

// The app's ticket dock: the same side panel the release history and a project
// use, fixed to the right of whatever view is open. Esc and the ticket keys
// (e, s, p, a, r, c) are handled by the shell so they win over the page.
const props = defineProps<{ ticketKey: string; backLabel?: string }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()
const now = ref(Date.now())
const root = ref<HTMLElement>()
const panel = ref<InstanceType<typeof ReleaseTicketPanel>>()
const clock = setInterval(() => { now.value = Date.now() }, 30_000)
onBeforeUnmount(() => clearInterval(clock))
onMounted(() => { void panel.value?.focus() })

const shortcuts: Record<string, keyof Pick<InstanceType<typeof ReleaseTicketPanel>, 'startEdit' | 'openStatus' | 'openPriority' | 'openAssignee' | 'openLink' | 'focusComposer'>> = {
  e: 'startEdit', s: 'openStatus', p: 'openPriority', a: 'openAssignee', r: 'openLink', c: 'focusComposer',
}
defineExpose({
  requestClose: () => panel.value?.requestClose(),
  focus: () => panel.value?.focus(),
  contains: (target: EventTarget | null) => target instanceof Node && !!root.value?.contains(target),
  shortcut: (key: string) => {
    const name = shortcuts[key]
    const target = panel.value
    if (!name || !target) return false
    target[name]()
    return true
  },
})
void props
</script>

<template>
  <div ref="root" class="ticket-peek-host">
    <ReleaseTicketPanel
      ref="panel" layout="dock" :ticket-key="ticketKey" :now="now" :back-label="backLabel" open-in-project
      @close="emit('close')" @navigate="path => router.push(path)"
    />
  </div>
</template>
