<script setup lang="ts">
import { FileText, Check, Clock3, Send } from 'lucide-vue-next'
import { offerStatus } from './types'
defineProps<{ status: string }>()
</script>
<template>
  <span class="offer-status" :class="status"
    ><component
      :is="
        status === 'accepted'
          ? Check
          : status === 'expired'
            ? Clock3
            : status === 'sent'
              ? Send
              : FileText
      "
      :size="14"
      aria-hidden="true"
    />{{ offerStatus(status) }}</span
  >
</template>
<style scoped>
.offer-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 8px;
  border-radius: 6px;
  background: var(--bg);
  color: var(--text-muted);
  font-size: 12px;
  white-space: nowrap;
}
.sent,
.accepted {
  background: var(--bg-hover, #c7efec);
  color: var(--text);
}
.expired {
  color: var(--text-muted);
}
</style>
