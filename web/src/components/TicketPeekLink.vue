<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, inject } from 'vue'
import { useRouter } from 'vue-router'
import { TICKET_PEEK } from '../lib/ticketPeek'
import { normalKey } from '../lib/ticketLinks'

// A real link to a ticket. A plain click opens the app peek when one is
// provided; ctrl/cmd, shift, alt and middle click keep the browser's meaning.
const props = defineProps<{ ticketKey: string; href: string; tip?: string }>()
const peek = inject(TICKET_PEEK, null)
const router = useRouter()
const current = computed(() => !!peek && peek.openKey.value === normalKey(props.ticketKey))

function onClick(event: MouseEvent) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  if (peek) peek.open(props.ticketKey, event.currentTarget as HTMLElement)
  else void router.push(props.href)
}
</script>

<template>
  <a :href="href" :aria-current="current ? 'true' : undefined" :data-tip="tip" @click="onClick"><slot>{{ ticketKey }}</slot></a>
</template>
