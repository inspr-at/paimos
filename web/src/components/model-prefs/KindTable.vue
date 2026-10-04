<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import KindRow from './KindRow.vue'
import { visibleKinds, providerWarning, type ModelPreferences, type PrefLevel } from '../../lib/modelPrefs'
const props = defineProps<{ doc: ModelPreferences; level: PrefLevel; busy: boolean; archiveKind: string }>()
defineEmits<{ pick: [kind: string, bucket: 'normal' | 'complex']; reset: [kind: string]; lock: [kind: string]; archive: [kind: string]; remove: [kind: string]; keep: [] }>()
const rows = computed(() => visibleKinds(props.doc.kinds, props.level).flatMap(kind => {
  const row = props.doc.views[props.level]?.rows.find(r => r.kind_id === kind.id)
  return row ? [{ kind, row: { ...row, warnings: [...row.warnings, providerWarning(props.doc.views[props.level]!.residency)].filter(Boolean) }, ownLocked: props.doc.levels[props.level]?.rows.find(r => r.kind_id === kind.id)?.locked ?? false }] : []
}))
</script>
<template>
  <table class="kind-table" aria-label="Models by kind of work"><thead><tr><th scope="col">Kind of work</th><th scope="col">Normally</th><th scope="col">If it’s complex</th><th scope="col">Set by</th><th scope="col"><span class="sr-only">Reset</span></th></tr></thead><tbody>
    <KindRow v-for="item in rows" :key="item.kind.id" v-bind="item" :level="level" :choices="doc.views[level]?.choices" :busy="busy" :confirming="archiveKind === item.kind.id" :editable="doc.can[`edit_${level}`]" @pick="bucket => $emit('pick', item.kind.id, bucket)" @reset="$emit('reset', item.kind.id)" @lock="$emit('lock', item.kind.id)" @archive="$emit('archive', item.kind.id)" @remove="$emit('remove', item.kind.id)" @keep="$emit('keep')" />
  </tbody></table>
</template>
<style scoped>
.kind-table { width: 100%; table-layout: fixed; border-collapse: collapse; } thead th { text-align: left; padding: 0 4px 6px; font: 500 10px/1.4 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); } th:first-child { width: 23%; } th:nth-child(2), th:nth-child(3) { width: 26%; } th:nth-child(4) { width: 21%; } th:last-child { width: 30px; }
@media (max-width: 600px) { .kind-table, tbody { display: block; } thead { display: none; } }
</style>
