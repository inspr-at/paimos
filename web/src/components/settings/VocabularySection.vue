<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getKinds, type Kind } from '../../lib/api'
import { WORK_KINDS } from '../../lib/ticketList'
import AppIcon from '../AppIcon.vue'
import AgentNamesCard from './AgentNamesCard.vue'
import SettingsCard from './SettingsCard.vue'
import WorkVocabularyCard from './WorkVocabularyCard.vue'

// Vocabulary: agent names, shared workspace names and the configured ticket types.
const kinds = ref<Kind[] | null>(null)
const error = ref('')
const work = computed(() => (kinds.value ?? []).filter(kind => (WORK_KINDS as readonly string[]).includes(kind.slug)))
const ICON: Record<string, 'epic' | 'ticket' | 'task'> = { epic: 'epic', ticket: 'ticket', task: 'task' }
const labelOf = (slug: string) => kinds.value?.find(kind => kind.slug === slug)?.label ?? slug
const fieldsOf = (kind: Kind) => Object.keys((kind.field_schema?.properties as Record<string, unknown> | undefined) ?? {}).length
async function load() {
  error.value = ''
  try { kinds.value = (await getKinds()).items } catch { error.value = 'The ticket types could not be loaded.' }
}
onMounted(load)
</script>

<template>
  <div class="section">
    <AgentNamesCard />
    <WorkVocabularyCard />
    <SettingsCard title="Ticket types" icon="layers" anchor="ticket-types">
      <template #lead>The kinds of work in every project, with the key prefix each one gets.</template>
      <div v-if="!kinds && !error" class="set-skeleton" role="status" aria-label="Loading ticket types"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
      <p v-else-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}<button type="button" class="btn sm" @click="load">Try again</button></p>
      <template v-else>
        <ul class="types">
          <li v-for="kind in work" :key="kind.id">
            <span class="type-icon" aria-hidden="true"><AppIcon :name="ICON[kind.slug] ?? 'ticket'" :size="14" /></span>
            <span class="type-name">{{ kind.label }}</span>
            <span class="key-badge prefix">{{ kind.short_prefix }}</span>
            <span class="type-meta">
              <!-- No list means any type may sit inside; an empty list means none. -->
              <template v-if="kind.allowed_child_kinds === null">Can hold any type</template>
              <template v-else-if="kind.allowed_child_kinds.length">Can hold {{ kind.allowed_child_kinds.map(labelOf).join(', ') }}</template>
              <template v-else>Holds no other types</template>
              <template v-if="fieldsOf(kind)"> · {{ fieldsOf(kind) }} {{ fieldsOf(kind) === 1 ? 'field' : 'fields' }}</template>
            </span>
          </li>
        </ul>
        <p class="set-note"><AppIcon name="info" :size="14" />Editing ticket types, their fields and the statuses a project uses arrives here next.</p>
      </template>
    </SettingsCard>
  </div>
</template>

<style scoped>
.section { display: grid; gap: 14px; }
.types { display: grid; margin: 0; padding: 0; list-style: none; }
.types li { display: grid; grid-template-columns: 28px minmax(90px, max-content) auto 1fr; align-items: center; gap: 10px; min-height: 44px; border-top: 1px solid var(--line); }
.types li:first-child { border-top: 0; }
.type-icon { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 8px; background: var(--surface-2); color: var(--ink-2); }
.type-name { font-weight: 600; font-size: 13.5px; color: var(--ink); }
.prefix { height: 20px; font-size: 10.5px; }
.type-meta { font-size: 12.5px; color: var(--ink-2); }
@media (max-width: 600px) {
  .types li { grid-template-columns: 28px 1fr auto; row-gap: 2px; padding: 8px 0; }
  .type-meta { grid-column: 2 / -1; }
}
</style>
