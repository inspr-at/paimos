<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { APIError, api } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { useSession } from '../../stores/session'

const props = defineProps<{ projectId: string; nodeId: string; de: boolean }>()
interface QueueRound { id: string; key: string; slug: string; pull_request: number | null; kind: string; round_number: number; state: string; hold_reason: string | null; reason: string; estimate_minutes: number }
interface QueuePage { items: QueueRound[]; settings: { mode: 'off' | 'shadow'; freeze: boolean; held_slugs: string[]; held_pull_requests: number[]; release_set: string[] }; next_cursor: string | null }
const session = useSession()
const read = computed(() => can('delivery_queue.read', props.projectId))
const scope = createScope(() => scopeOwner(session.identity) ? `${scopeOwner(session.identity)}/${props.projectId}/${props.nodeId}` : '')
const reader = scope.lane(), result = ref<QueuePage | null>(null), error = ref('')
async function load() {
  if (!read.value) return
  const path = `/projects/${encodeURIComponent(props.projectId)}/delivery-queue?ticket=${encodeURIComponent(props.nodeId)}&limit=20`
  return reader.run(({ after, signal }) => after(api(path, { signal }).then(async response => {
    if (!response.ok) throw new APIError(response.status, 'Delivery queue could not be loaded.')
    return response.json() as Promise<QueuePage>
  }), value => { result.value = value; error.value = '' }), { failed: () => { error.value = props.de ? 'Die Arbeitswarteschlange konnte nicht geladen werden.' : 'Delivery queue could not be loaded.' } })
}
watch([() => props.projectId, () => props.nodeId, () => scopeOwner(session.identity), read], () => { scope.reset(); result.value = null; error.value = ''; void load() }, { immediate: true, flush: 'sync' })
const stopAccess = onAccessChange(change => { if (change === 'reset') { scope.reset(); result.value = null; error.value = '' } else void load() })
const poll = setInterval(() => { void load() }, 30_000)
onBeforeUnmount(() => { clearInterval(poll); stopAccess(); scope.dispose() })
function kind(value: string) { return (props.de ? { first_build: 'Erstbau', fix: 'Korrektur', merge: 'Merge', land: 'Abschluss' } : { first_build: 'First build', fix: 'Fix', merge: 'Merge', land: 'Land' })[value as 'first_build'] ?? value }
function state(value: string) { return (props.de ? { queued: 'Wartend', claimed: 'Beansprucht', running: 'Laufend', done: 'Erledigt', parked: 'Geparkt' } : { queued: 'Queued', claimed: 'Claimed', running: 'Running', done: 'Done', parked: 'Parked' })[value as 'queued'] ?? value }
function reason(value: string) {
  const en: Record<string, string> = { release_freeze: 'Outside the frozen release set', round_hold: 'Round on hold', slug_hold: 'Branch on hold', pull_request_hold: 'Pull request on hold', delivery_hold: 'Delivery on hold', slug_running: 'Another round owns this branch', admission_unavailable: 'Admission unavailable', target_unavailable: 'Work item unavailable', target_not_leaf: 'Work item has children', already_merged: 'Already merged' }
  const de: Record<string, string> = { release_freeze: 'Außerhalb der eingefrorenen Liefermenge', round_hold: 'Runde angehalten', slug_hold: 'Branch angehalten', pull_request_hold: 'Pull Request angehalten', delivery_hold: 'Lieferung angehalten', slug_running: 'Eine andere Runde beansprucht diesen Branch', admission_unavailable: 'Startprüfung nicht verfügbar', target_unavailable: 'Arbeitsauftrag nicht verfügbar', target_not_leaf: 'Arbeitsauftrag enthält Unteraufträge', already_merged: 'Bereits gemergt' }
  return (props.de ? de : en)[value] ?? (['queued', 'managed', 'admitted', 'shadow_running', 'shadow_done'].includes(value) ? '' : value)
}
function holdReason(round: QueueRound) {
  if (round.state === 'done') return ''
  if (round.hold_reason) return round.hold_reason
  const settings = result.value?.settings
  if (settings?.held_slugs?.includes(round.slug)) return reason('slug_hold')
  if (round.pull_request && settings?.held_pull_requests?.includes(round.pull_request)) return reason('pull_request_hold')
  if (settings?.freeze && !(settings.release_set ?? []).some(prefix => round.slug?.startsWith(prefix))) return reason('release_freeze')
  return reason(round.reason)
}
</script>
<template>
  <div v-if="read && (result?.items.length || error)" class="work-queue" :aria-label="de ? 'Arbeitswarteschlange' : 'Delivery work queue'">
    <h4>{{ de ? 'Arbeitswarteschlange' : 'Delivery work queue' }}</h4>
    <p v-if="result">{{ result.settings.mode === 'off' ? de ? 'Ausgeschaltet' : 'Off' : de ? 'Schattenmodus · startet keine Arbeit' : 'Shadow mode · does not start work' }}{{ result.settings.freeze ? de ? ' · Liefermenge eingefroren' : ' · Release set frozen' : '' }}</p>
    <ol v-if="result?.items.length">
      <li v-for="round in result.items" :key="round.id">
        <div class="round"><strong>{{ kind(round.kind) }} {{ round.round_number }}</strong><span>{{ state(round.state) }} · {{ round.estimate_minutes }} min</span></div>
        <p v-if="holdReason(round)">{{ holdReason(round) }}</p>
      </li>
    </ol>
    <p v-if="result?.next_cursor" role="status">{{ de ? 'Die ersten 20 Runden werden angezeigt. Weitere Runden sind vorhanden.' : 'Showing the first 20 rounds. More rounds are available.' }}</p>
    <p v-if="error" role="status">{{ error }}</p>
  </div>
</template>
<style scoped>
.work-queue { margin-top:16px; color:var(--ink-2); font-size:12.5px; overflow-wrap:anywhere; }
h4 { margin:0; color:var(--ink); font-size:13px; } p { margin:4px 0; line-height:1.45; }
ol { list-style:none; margin:8px 0 0; padding:0; } li { padding:8px 0; border-bottom:1px solid var(--line); }
.round { display:flex; flex-wrap:wrap; justify-content:space-between; gap:4px 12px; } strong { font-weight:600; color:var(--ink); }
</style>
