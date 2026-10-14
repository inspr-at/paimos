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
// stays put opens the tab itself; a drag away cancels. The press remembers its
// ticket and opens nothing once the link shows another one.
const props = defineProps<{ ticketKey: string; href: string; tip?: string }>()
const peek = inject(TICKET_PEEK, null)
const router = useRouter()
const current = computed(() => !!peek && peek.openKey.value === normalKey(props.ticketKey))

const PRESS_SLOP = 8
type Press = { id: number; x: number; y: number; key: string; href: string; released: boolean; opened: boolean }
// One modified pointer press, kept until its click or the next press.
let press: Press | null = null

function modifiedPrimary(event: MouseEvent | PointerEvent) {
  return event.button === 0 && (event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey
}

function appPath(href: string) {
  return href.startsWith('/') && !href.startsWith('//')
}

function openTab(started: Press) {
  if (started.opened || started.key !== normalKey(props.ticketKey)) return
  started.opened = true
  window.open(started.href, '_blank', 'noopener')
}

function onPointerDown(event: PointerEvent) {
  press = null
  if (event.defaultPrevented || !modifiedPrimary(event) || !appPath(props.href)) return
  const target = event.currentTarget as { setPointerCapture?: (id: number) => void } | null
  if (target && typeof target.setPointerCapture === 'function') {
    try { target.setPointerCapture(event.pointerId) } catch { /* a click that still lands opens the tab */ }
  }
  press = { id: event.pointerId, x: event.clientX, y: event.clientY, key: normalKey(props.ticketKey), href: props.href, released: false, opened: false }
}

function onPointerUp(event: PointerEvent) {
  const started = press
  if (!started || started.released || started.id !== event.pointerId) return
  started.released = true
  if (event.defaultPrevented || !modifiedPrimary(event)) return
  if (Math.hypot(event.clientX - started.x, event.clientY - started.y) > PRESS_SLOP) return
  // A click that still lands may run before or after this microtask; either
  // way the press opens one tab. A moved row never delivers that click.
  queueMicrotask(() => openTab(started))
}

function onClick(event: MouseEvent) {
  if (event.button !== 0 || event.shiftKey || event.altKey) return
  // Keyboard and script clicks carry detail 0 and never belong to a press.
  const started = event.detail > 0 ? press : null
  press = null
  if (event.metaKey || event.ctrlKey) {
    // Without a press the browser opens its own tab, every time.
    if (!started || event.defaultPrevented) return
    event.preventDefault()
    openTab(started)
    return
  }
  if (event.defaultPrevented) return
  event.preventDefault()
  if (peek) peek.open(props.ticketKey, event.currentTarget as HTMLElement)
  else void router.push(props.href)
}
</script>

<template>
  <a :href="href" :aria-current="current ? 'true' : undefined" :data-tip="tip" @pointerdown="onPointerDown" @pointerup="onPointerUp" @click="onClick"><slot>{{ ticketKey }}</slot></a>
</template>
