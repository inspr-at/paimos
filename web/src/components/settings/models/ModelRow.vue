<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import { lineFallback, pickText, showsEffort, type RowView } from '../../../lib/modelsSimple'

// One row: label | picker | lock | remove, then a "can't run" line under it when the pick cannot run right now.
const props = defineProps<{ row: RowView; harness?: string; expanded: boolean; lockTip: string; instead: string; reason: string }>()
const emit = defineEmits<{ open: [button: HTMLElement]; reset: []; remove: [] }>()
const name = computed(() => props.row.isDefault ? 'Default for all work' : props.row.label)
const model = computed(() => props.row.entry?.name ?? lineFallback(props.row.line) ?? 'Not set')
const effort = computed(() => showsEffort(props.row.entry) ? props.row.effort : null)
const summary = computed(() => pickText(props.row.entry, props.row.effort, lineFallback(props.row.line) || 'Not set'))
const mark = computed(() => props.row.entry?.harness ?? props.harness ?? '')
</script>
<template>
  <div class="row" :class="{ def: row.isDefault }" :data-row="row.key" :data-column="row.column">
    <div class="rl">
      <div class="rt">
        <template v-if="row.isDefault"><span class="rn"><b>Default</b> · all work</span><span class="hint">Used unless a row below overrides it.</span></template>
        <span v-else class="rn">{{ row.label }}</span>
      </div>
      <span v-if="row.mine" class="mine">yours<template v-if="row.reset"> · <button type="button" class="link-btn" data-reset :aria-label="`Reset ${name} to the workspace default`" @click="emit('reset')">reset</button></template></span>
    </div>
    <button v-if="row.editable" :id="`pk-${row.key}`" type="button" class="pick" :data-pick="row.key" aria-haspopup="listbox" :aria-expanded="expanded" :aria-label="`${name}: ${summary.replace(' · ', ', ')}. Change`" @click="emit('open', $event.currentTarget as HTMLElement)">
      <span class="hg" aria-hidden="true"><HarnessMark :harness="mark" :size="13" /></span>
      <span class="pn">{{ model }}<span v-if="effort" class="pe"> · {{ effort }}</span></span>
      <AppIcon class="chev" name="chevron" :size="14" />
    </button>
    <span v-else :id="`pk-${row.key}`" class="pick ro" :data-pick="row.key">
      <span class="hg" aria-hidden="true"><HarnessMark :harness="mark" :size="13" /></span>
      <span class="pn">{{ model }}<span v-if="effort" class="pe"> · {{ effort }}</span></span>
    </span>
    <div class="c3"><span v-if="row.lock" class="lock" data-lock tabindex="0" role="img" :data-tip="lockTip" :aria-label="`Locked for everyone. ${lockTip}`"><AppIcon name="lock" :size="15" /></span></div>
    <div class="c4"><button v-if="row.remove" type="button" class="icon-btn flat x" data-remove :aria-label="`Remove ${row.label}; it uses the default again`" data-tip="Use the default again" @click="emit('remove')"><AppIcon name="close" :size="14" /></button></div>
    <p v-if="row.unavailable" class="na" data-unavailable><AppIcon name="alert" :size="14" /><span><b>Can’t run right now</b> · {{ instead ? `${instead} runs instead` : 'work waits' }} · {{ reason }}</span></p>
  </div>
</template>
