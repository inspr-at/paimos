<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, inject } from 'vue'
import { useRouter } from 'vue-router'
import { TICKET_PEEK } from '../lib/ticketPeek'
import { normalKey } from '../lib/ticketLinks'

// A real link to a ticket. A plain click opens the app peek when one is
// provided. Shift, alt and middle click keep the browser's own meaning.
// Ctrl/cmd opens a new tab. A live row can move between press and release, so
// the browser never sees a click and drops that tab (AEON-1327). A press that
// stays put opens the tab itself; a drag away cancels.
const props = defineProps<{ ticketKey: string; href: string; tip?: string }>()
const peek = inject(TICKET_PEEK, null)
const router = useRouter()
const current = computed(() => !!peek && peek.openKey.value === normalKey(props.ticketKey))

const PRESS_SLOP = 8
let press: { id: number; x: number; y: number } | null = null
let tabOpened = false

function modifiedPrimary(event: MouseEvent | PointerEvent) {
  return event.button === 0 && (event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey
}

function appPath(href: string) {
  return href.startsWith('/') && !href.startsWith('//')
}

function openTab(href: string) {
  if (tabOpened || !appPath(href)) return
  tabOpened = true
  window.open(href, '_blank', 'noopener')
}

function onPointerDown(event: PointerEvent) {
  press = null
  tabOpened = false
  if (event.defaultPrevented || !modifiedPrimary(event) || !appPath(props.href)) return
  const target = event.currentTarget as { setPointerCapture?: (id: number) => void } | null
  if (target && typeof target.setPointerCapture === 'function') {
    try { target.setPointerCapture(event.pointerId) } catch { /* a click that still lands opens the tab */ }
  }
  press = { id: event.pointerId, x: event.clientX, y: event.clientY }
}

function onPointerUp(event: PointerEvent) {
  const started = press
  press = null
  if (!started || started.id !== event.pointerId || event.defaultPrevented || !modifiedPrimary(event)) return
  if (Math.hypot(event.clientX - started.x, event.clientY - started.y) > PRESS_SLOP) return
  // Click listeners run in this turn, before the microtask. They own the tab
  // when the release still hits the link. A moved row never delivers that click.
  const href = props.href
  queueMicrotask(() => openTab(href))
}

function onClick(event: MouseEvent) {
  if (event.button !== 0 || event.shiftKey || event.altKey) return
  if (event.metaKey || event.ctrlKey) {
    if (event.defaultPrevented || !appPath(props.href)) return
    event.preventDefault()
    openTab(props.href)
    return
  }
  tabOpened = false
  if (event.defaultPrevented) return
  event.preventDefault()
  if (peek) peek.open(props.ticketKey, event.currentTarget as HTMLElement)
  else void router.push(props.href)
}
</script>

<template>
  <a :href="href" :aria-current="current ? 'true' : undefined" :data-tip="tip" @pointerdown="onPointerDown" @pointerup="onPointerUp" @click="onClick"><slot>{{ ticketKey }}</slot></a>
</template>
