<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import type { WorkKind } from '../../lib/workKinds'
import type { KindsText } from '../../lib/workKindsCopy'
const props = defineProps<{ kind: WorkKind; index: number; last: boolean; editable: boolean; busy: boolean; canOrder: boolean; german: boolean; t: KindsText }>()
const emit = defineEmits<{ edit: [kind: WorkKind, anchor: HTMLElement]; archive: [kind: WorkKind, anchor: HTMLElement]; move: [kind: WorkKind, direction: -1 | 1] }>()
const anchor = (event: MouseEvent) => event.currentTarget as HTMLElement
function editRow(event: MouseEvent | KeyboardEvent) {
  if (!props.editable || props.busy || (event.target as HTMLElement).closest('button, a, input, textarea')) return
  emit('edit', props.kind, event.currentTarget as HTMLElement)
}
</script>
<template>
  <li :id="`kind-${kind.slug}`" class="kind" :data-kind="kind.slug" :tabindex="editable ? 0 : -1" @click="editRow" @keydown.enter.self="editRow">
    <div class="kind-head">
      <span class="kind-n"><AppIcon v-if="kind.system === 'other'" name="minus" :size="12" /><template v-else>{{ index + 1 }}</template></span>
      <b v-clip-tip="kind.label" class="kind-name" tabindex="0">{{ kind.label }}</b>
      <span class="count">{{ kind.ticket_count }} {{ german ? 'Tickets' : 'tickets' }}</span>
      <span v-if="editable" class="kind-acts">
        <template v-if="kind.system !== 'other'">
          <button type="button" class="icon-btn sm" :aria-label="german ? `${kind.label} nach oben` : `Move ${kind.label} up`" :disabled="busy || !canOrder || index === 0" @click="emit('move', props.kind, -1)"><AppIcon name="chevron-up" :size="14" /></button>
          <button type="button" class="icon-btn sm" :aria-label="german ? `${kind.label} nach unten` : `Move ${kind.label} down`" :disabled="busy || !canOrder || last" @click="emit('move', props.kind, 1)"><AppIcon name="chevron" :size="14" /></button>
        </template>
        <button type="button" class="btn ghost sm" :disabled="busy" @click="emit('edit', props.kind, anchor($event))"><AppIcon name="edit" :size="14" />{{ t('edit') }}</button>
        <span v-if="kind.system" class="built-in" tabindex="0" :data-tip="t('systemHint')">{{ t('builtIn') }}</span>
        <button v-else type="button" class="btn ghost sm" :disabled="busy" aria-haspopup="dialog" @click="emit('archive', props.kind, anchor($event))"><AppIcon name="archive" :size="14" />{{ t('archive') }}</button>
      </span>
    </div>
    <p class="kind-def">{{ kind.hint }}</p>
    <dl class="kind-facts">
      <div><dt>{{ t('example') }}</dt><dd><ul v-if="kind.examples.length"><li v-for="example in kind.examples" :key="example">{{ example }}</li></ul><span v-else class="faint">{{ t('noExamples') }}</span></dd></div>
      <div><dt>{{ t('recognition') }}</dt><dd>{{ t('area') }} <code>{{ kind.slug }}</code><template v-if="kind.system === 'other'"> <code>{{ german ? '(keiner)' : '(none)' }}</code></template><template v-if="kind.labels.length"> · {{ t('labelWord') }} <code v-for="label in kind.labels" :key="label">{{ label }}</code></template><template v-if="kind.system === 'other'"> · {{ t('otherRecognition') }}</template></dd></div>
    </dl>
  </li>
</template>
<style scoped>
.kind { display: grid; gap: 8px; padding: 16px 0; border-top: 1px solid var(--line); scroll-margin-top: 20px; }
.kind:first-child { border-top: 0; padding-top: 4px; }
.kind-head { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 10px; }
.kind-n { display: inline-grid; place-items: center; min-width: 22px; height: 22px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 600 11px/1 var(--mono); color: var(--ink-2); }
.kind-name { min-width: 0; max-width: 100%; font-size: 15px; font-weight: 650; overflow-wrap: anywhere; }
.count, .built-in { font-size: 10px; text-transform: uppercase; letter-spacing: .03em; color: var(--ink-3); white-space: nowrap; }
.kind-acts { margin-left: auto; display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.kind-def { margin: 0; font-size: 14px; line-height: 1.5; overflow-wrap: anywhere; }
.kind-facts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 24px; margin: 0; }
.kind-facts dt { font-size: 11.5px; font-weight: 600; color: var(--ink-3); }
.kind-facts dd { margin: 2px 0 0; font-size: 13px; line-height: 1.5; color: var(--ink-2); overflow-wrap: anywhere; }
.kind-facts ul { margin: 0; padding-left: 18px; }
code { font: 500 11.5px/1.4 var(--mono); background: var(--code-bg); padding: 1px 4px; border-radius: 4px; color: var(--ink); margin-right: 3px; }
.faint { color: var(--ink-3); }
@media (max-width: 600px) { .kind-facts { grid-template-columns: minmax(0, 1fr); }.kind-acts { margin-left: 0; flex-basis: 100%; } }
@media (pointer: coarse), (max-width: 720px) { button { min-height: 44px; }.icon-btn { min-width: 44px; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
