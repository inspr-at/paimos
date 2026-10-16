<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { displayLanguage } from '../../lib/displayLanguage'
import { searchWork } from '../../lib/ticketSearch'
import { useAgents } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import { chatWords } from './sessionChat'

// The phone composer's "+" (AEON-1438): references only, never attachments. It
// inserts a ticket key or a session name into the draft. Controls stay on top;
// results grow downward in fixed-height rows, with their status below them.
const props = defineProps<{ anchor: HTMLElement; projectId: string; sessionId: string }>()
const emit = defineEmits<{ pick: [text: string]; close: [restore: boolean] }>()
const agents = useAgents()
const uid = useId()
const words = computed(() => chatWords[displayLanguage()])
const step = ref<'menu' | 'ticket' | 'session'>('menu')
const panel = ref<HTMLElement>()
const query = ref('')
const tickets = ref<{ id: string; key: string; title: string }[]>([])
const searching = ref(false), failed = ref(false)
const sessions = computed(() => agents.views
  .filter(view => view.session.project_id === props.projectId && view.session.id !== props.sessionId && !view.session.stopped_at && !view.session.archived_at)
  .slice(0, 20).map(view => ({ id: view.session.id, name: view.name, key: view.ticket?.key ?? '' })))
let search: AbortController | undefined
let timer: ReturnType<typeof setTimeout> | undefined
watch(query, value => {
  if (timer !== undefined) clearTimeout(timer)
  search?.abort()
  const term = value.trim().slice(0, 200)
  if (!term) { tickets.value = []; searching.value = false; failed.value = false; return }
  timer = setTimeout(() => { timer = undefined; void find(term) }, 200)
})
async function find(term: string) {
  const controller = new AbortController()
  search = controller
  searching.value = true; failed.value = false
  try {
    const { listed, hits } = await searchWork(term, { within: props.projectId, signal: controller.signal })
    if (search !== controller) return
    const seen = new Set<string>()
    tickets.value = [...listed, ...hits].filter(node => node.key && !seen.has(node.id) && seen.add(node.id)).slice(0, 8).map(node => ({ id: node.id, key: node.key!, title: node.title }))
  } catch {
    if (search === controller && !controller.signal.aborted) { tickets.value = []; failed.value = true }
  } finally {
    if (search === controller) searching.value = false
  }
}
onBeforeUnmount(() => { search?.abort(); if (timer !== undefined) clearTimeout(timer) })
async function go(next: 'menu' | 'ticket' | 'session') {
  step.value = next
  await nextTick()
  panel.value?.querySelector<HTMLElement>('input, [role="menuitem"]')?.focus({ preventScroll: true })
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="320" :label="words.addReference" cycle field-escape @close="emit('close', $event)">
    <div ref="panel" class="references">
      <div v-if="step === 'menu'" class="ref-list" role="menu" :aria-label="words.addReference">
        <button type="button" role="menuitem" class="ref-item" @click="go('ticket')"><AppIcon name="ticket" :size="18" /><span>{{ words.referenceTicket }}</span></button>
        <button type="button" role="menuitem" class="ref-item" @click="go('session')"><AppIcon name="agent" :size="18" /><span>{{ words.referenceSession }}</span></button>
      </div>
      <template v-else>
        <div class="ref-head">
          <button type="button" class="icon-btn flat ref-back" :aria-label="words.back" @click="go('menu')"><AppIcon name="chevron-left" :size="16" /></button>
          <strong>{{ step === 'ticket' ? words.referenceTicket : words.referenceSession }}</strong>
        </div>
        <template v-if="step === 'ticket'">
          <label class="sr-only" :for="`${uid}-q`">{{ words.searchTickets }}</label>
          <input :id="`${uid}-q`" v-model="query" class="field ref-search" type="search" maxlength="200" autocomplete="off" :placeholder="words.searchTickets" />
          <div class="ref-list" role="menu" :aria-label="words.searchTickets">
            <button v-for="ticket in tickets" :key="ticket.id" type="button" role="menuitem" class="ref-item" @click="emit('pick', ticket.key)"><span class="ref-key">{{ ticket.key }}</span><span class="ref-title" :title="ticket.title">{{ ticket.title }}</span></button>
          </div>
          <p v-if="failed" class="ref-status" role="alert">{{ words.ticketsFailed }}</p>
          <p v-else-if="query.trim() && !searching && !tickets.length" class="ref-status" role="status">{{ words.noTickets }}</p>
        </template>
        <template v-else>
          <div class="ref-list" role="menu" :aria-label="words.referenceSession">
            <button v-for="session in sessions" :key="session.id" type="button" role="menuitem" class="ref-item" @click="emit('pick', session.name)"><AppIcon name="agent" :size="16" /><span class="ref-title" :title="session.name">{{ session.name }}</span></button>
          </div>
          <p v-if="!sessions.length" class="ref-status" role="status">{{ words.noSessions }}</p>
        </template>
      </template>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.references { display: grid; gap: 6px; }
.ref-head { display: flex; align-items: center; gap: 6px; min-height: 44px; font-size: 14px; }
.ref-back { width: 44px; height: 44px; }
.ref-search { height: 44px; font-size: 16px; }
.ref-list { display: grid; }
.ref-item { display: flex; align-items: center; gap: 12px; width: 100%; min-width: 0; height: 44px; padding: 0 12px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); font: inherit; font-size: 15px; text-align: left; }
.ref-item > svg { flex: none; color: var(--ink-2); }
@media (hover: hover) { .ref-item:hover { background: var(--row-hover); } }
.ref-item:focus-visible { outline: 2px solid var(--primary-line); outline-offset: -2px; }
.ref-key { flex: none; font: 600 13px var(--mono); color: var(--teal-ink); }
.ref-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ref-status { margin: 0; padding: 4px 12px 8px; font-size: 13px; color: var(--ink-2); }
</style>
