<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A column header's figures (AEON-1358, design r4b §3.2): the sort hint first, unchanged; then a hairline, the
// column's fixed number of figure lines and a footer. While the view is counted the same box shows "Counting…".
// Hovers only read the view's figures object; nothing is computed or fetched here.
import { computed } from 'vue'
import type { ProjectFigures } from '../../lib/api'
import type { ColumnId } from '../../lib/columns'
import { columnTip, figureColumn } from '../../lib/workFigures'
import { fill, workFiguresText } from '../../lib/workFiguresText'
import MeasuredMark from './MeasuredMark.vue'
import PriorityIcon from './PriorityIcon.vue'

// hint replaces the plain sort hint (the group order and the order inside groups, AEON-1390); title names a group whose
// own figures these are (AEON-1388), in place of the hint.
const props = defineProps<{ columnId: string; label: string; sortable: boolean; figures: ProjectFigures | null; failed: string | null; hiddenRows: boolean; now: number; modelName?: (id: string) => string; hint?: string; title?: string }>()
const text = workFiguresText()
const def = computed(() => figureColumn(props.columnId, props.figures))
const tip = computed(() => props.figures && def.value ? columnTip(props.columnId as ColumnId, props.figures, { hiddenRows: props.hiddenRows, now: props.now, text, modelName: props.modelName }) : null)
const hint = computed(() => props.hint ?? `${fill(text.sortBy, { column: props.label.toLowerCase() })}\n${text.shiftSort}`)
</script>

<template>
  <p v-if="title" class="wf-ttl">{{ title }}</p>
  <p v-else-if="sortable" class="wf-hint">{{ hint }}</p>
  <template v-if="def">
    <div v-if="sortable || title" class="wf-rule" aria-hidden="true" />
    <template v-if="tip">
      <template v-for="(line, index) in tip.lines" :key="index">
        <div v-if="line" class="wf-line" :class="{ head: line.head, sub: line.sub }">
          <span>
            <i v-if="line.lead?.kind === 'swatch'" class="wk-sw" :class="line.lead.fill || 'sw-open'" aria-hidden="true" />
            <span v-else-if="line.lead?.kind === 'priority'" class="wf-mark" aria-hidden="true"><PriorityIcon :priority="line.lead.priority" :size="12" /></span>
            <span v-else-if="line.lead?.kind === 'measured'" class="wf-mark ok" aria-hidden="true"><MeasuredMark /></span>
            <span v-else-if="line.lead?.kind === 'estimated'" class="wf-mark" aria-hidden="true">~</span>
            <span v-else-if="line.lead?.kind === 'dash'" class="wf-mark" aria-hidden="true">–</span>
            <span>{{ line.label }}</span>
          </span>
          <span class="wf-v">{{ line.value }}<small v-if="line.note">{{ line.note }}</small></span>
        </div>
        <div v-else class="wf-line" aria-hidden="true"><span /><span class="wf-v" /></div>
      </template>
      <p class="wf-foot">{{ tip.foot }}</p>
    </template>
    <template v-else>
      <div class="wf-line wait"><span><span>{{ failed ?? text.counting }}</span></span><span class="wf-v" /></div>
      <div v-for="index in def.lines - 1" :key="index" class="wf-line" aria-hidden="true"><span /><span class="wf-v" /></div>
      <p class="wf-foot">&nbsp;</p>
    </template>
  </template>
</template>
