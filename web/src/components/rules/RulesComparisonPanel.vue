<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { toast } from '../../lib/toast'
import { HARNESS_LABEL, listComparisons, type RulesComparison } from '../../lib/rules'

const props = defineProps<{ projectId: string }>()
const state = ref<'wait' | 'ready' | 'error'>('wait')
const rows = ref<RulesComparison[]>([])

const lines = computed(() => rows.value.flatMap(row => {
  const label = row.harness === 'claude-code' || row.harness === 'codex' ? HARNESS_LABEL[row.harness] : ''
  if (!label) return []
  const counts = row.counts
  return [`${label} · both ${counts.both}, only local ${counts.only_local}, only merged ${counts.only_merged}, differs ${counts.differs}`]
}))

async function load(projectId: string) {
  state.value = 'wait'
  rows.value = []
  try {
    const body = await listComparisons(projectId)
    if (projectId !== props.projectId) return
    rows.value = body.comparisons ?? []
    state.value = 'ready'
  } catch {
    if (projectId !== props.projectId) return
    rows.value = []
    state.value = 'error'
  }
}

watch(() => props.projectId, projectId => { void load(projectId) }, { immediate: true })

async function copy() {
  try {
    await navigator.clipboard.writeText(`aeon rules compare --harness codex --repo . --project ${props.projectId} --role builder`)
    toast('Compare command copied')
  } catch {
    toast('Could not copy the compare command', { tone: 'error' })
  }
}
</script>

<template>
  <section v-if="state !== 'wait'" class="loaded" aria-label="Loaded vs published">
    <h3>Loaded vs published</h3>
    <p v-if="state === 'error'" class="quiet">Loaded-file comparison is unavailable.</p>
    <ul v-else-if="lines.length" class="counts">
      <li v-for="line in lines" :key="line">{{ line }}</li>
    </ul>
    <template v-else>
      <p class="quiet">No loaded-file comparison yet.</p>
      <button type="button" class="btn sm ghost" @click="copy">Copy compare command</button>
    </template>
  </section>
</template>

<style scoped>
.loaded { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; min-width: 0; max-width: 100%; padding: 12px 14px; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.loaded h3 { margin: 0; color: var(--ink-2); font-size: 13px; font-weight: 650; }
.quiet { margin: 0; color: var(--ink-3); font-size: 13px; line-height: 1.45; }
.counts { display: flex; flex-direction: column; gap: 4px; margin: 0; padding: 0; list-style: none; color: var(--ink-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.loaded .btn { max-width: 100%; white-space: normal; }
</style>
