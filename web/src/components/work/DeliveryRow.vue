<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import DeliveryTrack from './DeliveryTrack.vue'
import { vClipTip } from '../../lib/clipTip'
import { LEAD_WORDS } from '../../lib/lead'
import { minutesSince, nextAction, overdue, ownerLabel, pullURL, stateLabel, stepIndex, type DeliveryItem, type DeliveryLanguage } from '../../lib/delivery'
const props = withDefaults(defineProps<{ item: DeliveryItem; canManage: boolean; now: number; lang?: DeliveryLanguage; busy?: boolean; lifted?: boolean }>(), { lang: 'en', busy: false, lifted: false })
defineEmits<{ lift: [item: DeliveryItem] }>()
const de = computed(() => props.lang === 'de')
const late = computed(() => overdue(props.item, props.now))
const variant = computed(() => props.item.state === 'held' ? 'held' : props.item.state === 'queue_failed' ? 'failed' : late.value ? 'late' : 'normal')
const time = computed(() => {
  const age = minutesSince(props.item.state_since, props.now)
  if (props.item.state === 'merged') return de.value ? `Vor ${age} Min. gemergt · Nichts mehr zu tun` : `Merged ${age} min ago · Nothing left to do`
  if (late.value) { const n = minutesSince(props.item.deadline_at!, props.now); return de.value ? `Lieferfrist um ${n} Min. überschritten` : `Past its deadline by ${n} min` }
  const deadline = props.item.deadline_at ? Math.max(0, Math.ceil((Date.parse(props.item.deadline_at) - props.now) / 60_000)) : null
  return de.value ? `seit ${age} Min. in diesem Schritt · ${deadline === null ? props.item.state === 'held' ? 'keine Frist, solange angehalten' : 'keine Frist in diesem Schritt' : `Frist in ${deadline} Min.`}` : `${age} min in this step · ${deadline === null ? props.item.state === 'held' ? 'no deadline while held' : 'no deadline in this step' : `deadline in ${deadline} min`}`
})
</script>
<template>
  <li class="delivery-row" :class="[variant, { merged: item.state === 'merged' }]" :data-delivery-id="item.id">
    <div class="top">
      <a v-if="pullURL(item)" class="pr" :href="pullURL(item)" target="_blank" rel="noopener noreferrer"><AppIcon name="merge" :size="14" />#{{ item.pull_request }}<AppIcon name="external" :size="11" /></a>
      <span v-else class="pr">#{{ item.pull_request }}</span>
      <span v-clip-tip="`${item.repository} · ${item.branch}`" tabindex="0" class="where">{{ item.repository }} · {{ item.branch }}</span>
      <span class="state"><AppIcon :name="item.state === 'held' ? 'pause' : item.state === 'queue_failed' ? 'alert' : item.state === 'merged' ? 'check' : 'clock'" :size="13" />{{ stateLabel(item.state, lang) }}</span>
      <button v-if="canManage && (item.state === 'held' || lifted)" type="button" class="btn sm" :disabled="busy || lifted" @click="$emit('lift', item)"><span class="btn-label"><span>{{ de ? 'Halt aufheben' : 'Lift hold' }}</span><span aria-hidden="true" class="btn-label-size">{{ de ? 'Wird aufgehoben…' : 'Lifting…' }}</span></span></button>
    </div>
    <template v-if="item.state !== 'merged'">
      <DeliveryTrack :step="stepIndex(item.state, item.held_from_state)" :variant="variant" :lang="lang" />
      <div class="meta"><p class="next"><span class="lbl">{{ de ? 'Als Nächstes' : 'Next' }}</span><strong>{{ ownerLabel(item.owner, LEAD_WORDS.S, lang) }}</strong> · {{ nextAction(item, lang) }}</p><p class="time"><AppIcon name="clock" :size="13" />{{ time }}</p></div>
      <p v-if="item.state === 'held'" class="note"><AppIcon name="pause" :size="14" /><span>{{ de ? 'Angehalten seit' : 'Held since' }} <time :datetime="item.state_since">{{ new Date(item.state_since).toLocaleString(lang) }}</time>: “{{ item.held_reason }}”<br v-if="!canManage" /><span v-if="!canManage">{{ de ? 'Nur Personen mit „Lieferstatus verwalten“ können den Halt aufheben.' : 'Only people with Manage delivery status can lift a hold.' }}</span></span></p>
      <p v-if="item.state === 'queue_failed'" class="note"><AppIcon name="alert" :size="14" /><span>{{ de ? `Stand ${item.head_sha.slice(0, 8)} ist in der Merge-Queue gescheitert. Eine Korrektur pushen, um erneut einzureihen.` : `Head ${item.head_sha.slice(0, 8)} failed in the merge queue. Push a fix to queue again.` }} <a v-if="pullURL(item)" :href="`${pullURL(item)}/checks`" target="_blank" rel="noopener noreferrer">{{ de ? 'Prüfungen öffnen' : 'Open checks' }}<AppIcon name="external" :size="11" /></a></span></p>
    </template>
    <p v-else class="time merged-time">{{ time }}</p>
  </li>
</template>
<style scoped>
.delivery-row { min-width:0; padding:12px 14px; border-radius:10px; background:var(--chip-bg); box-shadow:inset 0 0 0 1px var(--line); }
.delivery-row.late { background:var(--queue-wait-bg); box-shadow:inset 0 0 0 1px var(--queue-wait-line); }.delivery-row.failed { background:var(--danger-bg); box-shadow:inset 0 0 0 1px var(--danger-line); }.delivery-row.held { background:var(--surface-sunken); }.delivery-row.merged { background:color-mix(in srgb,var(--teal-ink) 5%,var(--chip-bg)); }
.top { display:flex; flex-wrap:wrap; align-items:center; gap:4px 10px; min-height:28px; }
.pr { display:inline-flex; align-items:center; gap:5px; color:var(--ink); font-size:13.5px; font-weight:650; white-space:nowrap; }.pr svg { color:var(--teal-ink); }
.where { flex:1 1 10em; min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font:11.5px/1.4 var(--mono); color:var(--ink-3); }
.state { display:inline-flex; align-items:center; gap:6px; margin-left:auto; font-size:12.5px; font-weight:600; color:var(--ink-2); }.failed .state { color:var(--danger); }.merged .state { color:var(--ok); }
.meta { display:flex; flex-wrap:wrap; align-items:baseline; justify-content:space-between; gap:4px 16px; font-size:12.5px; line-height:1.45; color:var(--ink-2); }.meta p { margin:0; }
.next { flex:1 1 auto; min-width:0; overflow-wrap:anywhere; }.lbl { margin-right:7px; font:500 10px var(--mono); letter-spacing:.14em; text-transform:uppercase; color:var(--ink-3); }.next strong { font-weight:600; color:var(--ink); }
.time { display:inline-flex; align-items:flex-start; gap:6px; color:var(--ink-3); font-variant-numeric:tabular-nums; }.time svg { flex:none; margin-top:2px; }.late .time { color:var(--queue-wait-ink); font-weight:600; }
.note { display:grid; grid-template-columns:auto minmax(0,1fr); gap:8px; margin:10px 0 0; padding-top:10px; border-top:1px solid var(--line); font-size:12.5px; line-height:1.5; color:var(--ink-2); overflow-wrap:anywhere; }.note>svg { margin-top:2px; }.note a { display:inline-flex; align-items:center; gap:4px; color:var(--teal-ink); }
.merged-time { margin:6px 0 0; font-size:12px; }
@container delivery (max-width:479px) { .delivery-row { padding:12px; } }
@media (pointer:coarse) { .top .btn { min-height:44px; } .pr,.where { min-height:44px; display:flex; align-items:center; } }
</style>
