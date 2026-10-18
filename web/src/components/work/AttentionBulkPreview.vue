<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { AttentionBulkAction, AttentionBulkPreview } from '../../lib/attention'
import { statusMeta } from '../../lib/work'
import FloatingPanel from './FloatingPanel.vue'
import StatusIcon from './StatusIcon.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const props = defineProps<{ anchor: HTMLElement; name: string; action: AttentionBulkAction; preview: AttentionBulkPreview; locale: string }>()
const emit = defineEmits<{ close: [restore: boolean]; run: [exclude: string[]] }>()
const words = (en: string, de: string) => props.locale === 'de' ? de : en
const excluded = ref(new Set<string>())
const count = computed(() => props.preview.moves.reduce((sum, move) => sum + (excluded.value.has(move.id) ? 0 : move.count), 0))
const skipped = computed(() => props.preview.skipped.reduce((sum, skip) => sum + skip.count, 0))
const title = computed(() => words(`${props.action === 'apply' ? 'Apply' : 'Dismiss'} all in ${props.name}`, `Alles ${props.action === 'apply' ? 'anwenden' : 'verwerfen'} in ${props.name}`))
const phone = ref(window.innerWidth <= 600)
const resized = () => { phone.value = window.innerWidth <= 600 }
onMounted(() => window.addEventListener('resize', resized))
onBeforeUnmount(() => window.removeEventListener('resize', resized))
const stateLabel = (state: string) => words(statusMeta(state).label, ({ new: 'Neu', backlog: 'Backlog', open: 'Offen', in_progress: 'In Arbeit', blocked: 'Blockiert', done: 'Erledigt', delivered: 'Ausgeliefert', accepted: 'Abgenommen', cancelled: 'Abgebrochen' } as Record<string, string>)[state] || statusMeta(state).label)
function toggle(id: string, on: boolean) { if (on) excluded.value.delete(id); else excluded.value.add(id) }
watch(count, value => {
  if (value) return
  const active = document.activeElement
  if (active instanceof HTMLElement && active.classList.contains('run')) active.closest('.preview-actions')?.querySelector<HTMLButtonElement>('button:not(.run)')?.focus({ preventScroll: true })
})
</script>
<template>
  <FloatingPanel :anchor="anchor" align="end" :width="520" :tallest="640" :sheet="phone" :label="title" cycle @close="restore => emit('close', restore)">
    <section class="bulk-preview" :class="{ phone }" data-attention-preview>
      <header><h3><AppIcon :name="action === 'apply' ? 'check' : 'close'" :size="16" />{{ title }}</h3></header>
      <div class="preview-actions">
        <button type="button" class="btn sm primary run" :class="{ hidden: count === 0 }" :aria-hidden="count === 0 || undefined" :tabindex="count === 0 ? -1 : 0" :data-autofocus="count === 0 ? undefined : true" :disabled="count === 0" @click="emit('run', [...excluded])"><span>{{ words(action === 'apply' ? 'Apply' : 'Dismiss', action === 'apply' ? 'Anwenden' : 'Verwerfen') }}</span><b>{{ count }}</b></button>
        <button type="button" class="btn sm ghost" :data-autofocus="count === 0 ? true : undefined" @click="emit('close', true)">{{ words('Cancel', 'Abbrechen') }}<KeyCap k="esc" /></button>
      </div>
      <div class="preview-body">
        <p class="lead" v-if="action === 'apply'">{{ words(`Moves ${count} tickets in ${name}.`, `Ändert ${count} Tickets in ${name}.`) }} {{ skipped ? words(`${skipped} are skipped.`, `${skipped} werden übersprungen.`) : '' }}</p>
        <p class="lead" v-else>{{ words(`${count} suggestions go away. The tickets stay as they are.`, `${count} Vorschläge verschwinden. Die Tickets bleiben, wie sie sind.`) }}</p>
        <p v-if="preview.truncated" class="limit" role="status">{{ words(`Only the first ${preview.limit} of ${preview.total} suggestions are included.`, `Nur die ersten ${preview.limit} von ${preview.total} Vorschlägen sind enthalten.`) }}</p>
        <div class="moves" role="group" :aria-label="words('Proposed changes', 'Vorgeschlagene Änderungen')">
          <label v-for="move in preview.moves" :key="move.id" class="move" :class="{ excluded: excluded.has(move.id) }" :title="move.sample_keys.join(', ')">
            <input type="checkbox" :checked="!excluded.has(move.id)" :aria-label="`${move.id}: ${move.count}`" @change="toggle(move.id, ($event.target as HTMLInputElement).checked)" />
            <span class="move-label"><template v-if="move.to === 'no_release_needed'"><AppIcon name="check" :size="13" /><span>{{ words('No release needed', 'Kein Release nötig') }}</span></template><template v-else-if="move.to !== 'release'"><StatusIcon :state="move.from" :size="13" /><span>{{ stateLabel(move.from) }}</span><AppIcon name="arrow" :size="12" /><StatusIcon :state="move.to" :size="13" /><span>{{ stateLabel(move.to) }}</span></template><template v-else><AppIcon name="box" :size="13" /><span>{{ words(`Add to ${move.release_title || move.release_id}`, `Zu ${move.release_title || move.release_id} hinzufügen`) }}</span></template></span><b>{{ move.count }}</b>
          </label>
        </div>
        <details v-if="skipped" class="skips"><summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />{{ words(`Skipped ${skipped}: why`, `${skipped} übersprungen: warum`) }}</summary><ul><li v-for="skip in preview.skipped" :key="skip.reason"><span class="keys">{{ skip.sample_keys.join(', ') }}</span> · {{ skip.count }} · {{ skip.reason }}</li></ul></details>
        <p class="foot">{{ words('Undo stays available for 10 seconds; each ticket’s Activity keeps the record.', 'Rückgängig geht 10 Sekunden lang; die Aktivität jedes Tickets hält es fest.') }}</p>
      </div>
    </section>
  </FloatingPanel>
</template>
<style scoped>
.bulk-preview { display: flex; flex-direction: column; max-height: calc(var(--floating-max) - 12px); padding: 10px; text-align: left; font-weight: 400; }
h3 { display: flex; align-items: baseline; gap: 8px; font-size: 15px; overflow-wrap: anywhere; } h3 svg { flex: none; align-self: center; }
.preview-actions { display: flex; flex-wrap: wrap; gap: 8px; padding: 12px 0; flex: none; }
.hidden { visibility: hidden; pointer-events: none; }
.preview-actions .btn.primary { box-shadow: none; }
.run b { min-width: 4ch; text-align: right; font-family: var(--mono); }
.preview-body { min-height: 0; overflow: auto; overscroll-behavior: contain; }
.lead { font-size: 13px; color: var(--ink-2); line-height: 1.5; }
.moves { display: grid; margin-top: 10px; gap: 2px; }
.move { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 44px; padding: 4px 6px; color: var(--ink); border-radius: 6px; cursor: pointer; font-size: 13px; }
.move:hover { background: var(--row-hover); } .move input { width: 16px; height: 16px; margin: 0; accent-color: var(--teal); }
.move-label { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; overflow-wrap: anywhere; } .move-label svg { flex: none; }
.move b { font: 600 12px/1 var(--mono); } .excluded { color: var(--ink-3); }
.skips { margin-top: 10px; border-block: 1px solid var(--line); font-size: 12px; color: var(--ink-2); }
.skips summary { display: flex; align-items: center; gap: 6px; width: 100%; padding: 10px 0; cursor: pointer; } .skips ul { margin: 0; padding: 4px 0 12px 18px; line-height: 1.6; overflow-wrap: anywhere; } .keys { font-family: var(--mono); }
.foot, .limit { margin-top: 12px; color: var(--ink-3); font-size: 12px; line-height: 1.5; }
.phone { height: 100%; max-height: 100%; padding: 12px; } .phone .preview-body { flex: 1; } .phone .preview-actions { order: 2; border-top: 1px solid var(--line); padding-bottom: calc(10px + env(safe-area-inset-bottom)); } .phone header { padding-bottom: 12px; }
@media (max-width: 600px), (pointer: coarse) { .preview-actions .btn, .skips summary { min-height: 44px; } }
</style>
