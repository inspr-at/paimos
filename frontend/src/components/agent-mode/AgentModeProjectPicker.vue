<!-- PAI-818 — searchable authorized project switcher shared with voice catalog. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { VoiceProjectRef } from '@/composables/agent-mode/agentModeVoiceIntent'

const props = defineProps<{
  modelValue: number | null
  projects: readonly VoiceProjectRef[]
  activeTotals: ReadonlyMap<number, number> | null
}>()
const emit = defineEmits<{ 'update:modelValue': [value: number | null] }>()
const { t } = useI18n()

const root = ref<HTMLElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const open = ref(false)
const query = ref('')
const activeIndex = ref(0)

const ordered = computed(() =>
  [...props.projects]
    .filter((project) => project.projectKey !== '' || project.projectName !== '')
    .sort(
      (left, right) =>
        left.projectKey.localeCompare(right.projectKey) ||
        left.projectName.localeCompare(right.projectName) ||
        left.projectId - right.projectId,
    ),
)

const selected = computed(
  () => ordered.value.find((project) => project.projectId === props.modelValue) ?? null,
)
const options = computed<Array<VoiceProjectRef | null>>(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  const matches =
    needle === ''
      ? ordered.value
      : ordered.value.filter((project) =>
          `${project.projectKey} ${project.projectName}`.toLocaleLowerCase().includes(needle),
        )
  return [null, ...matches]
})
const buttonLabel = computed(() =>
  selected.value
    ? [selected.value.projectKey, selected.value.projectName].filter(Boolean).join(' · ')
    : t('agentMode.filters.allProjects'),
)

function close() {
  open.value = false
  query.value = ''
  activeIndex.value = 0
  document.removeEventListener('pointerdown', onOutside)
}

function onOutside(event: PointerEvent) {
  if (!root.value?.contains(event.target as Node)) close()
}

async function toggle() {
  if (open.value) return close()
  open.value = true
  activeIndex.value = Math.max(
    0,
    options.value.findIndex((project) => project?.projectId === props.modelValue),
  )
  document.addEventListener('pointerdown', onOutside)
  await nextTick()
  searchInput.value?.focus()
}

function choose(project: VoiceProjectRef | null) {
  emit('update:modelValue', project?.projectId ?? null)
  close()
}

function move(delta: number) {
  if (options.value.length === 0) return
  activeIndex.value = (activeIndex.value + delta + options.value.length) % options.value.length
}

function onSearchInput() {
  activeIndex.value = options.value.length > 1 ? 1 : 0
}

function onSearchKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    move(event.key === 'ArrowDown' ? 1 : -1)
  } else if (event.key === 'Enter') {
    event.preventDefault()
    choose(options.value[activeIndex.value] ?? null)
  } else if (event.key === 'Escape') {
    event.preventDefault()
    close()
  }
}

onBeforeUnmount(() => document.removeEventListener('pointerdown', onOutside))
</script>

<template>
  <div ref="root" class="am-project-picker">
    <button
      type="button"
      class="am-project-picker__trigger"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-label="t('agentMode.filters.projectOpen')"
      @click="toggle"
      @keydown.esc.prevent="close"
    >
      <AppIcon name="folder" :size="12" aria-hidden="true" />
      <span>{{ buttonLabel }}</span>
      <AppIcon :name="open ? 'chevron-up' : 'chevron-down'" :size="12" aria-hidden="true" />
    </button>

    <div v-if="open" class="am-project-picker__popover">
      <label class="am-project-picker__search">
        <AppIcon name="search" :size="12" aria-hidden="true" />
        <span class="am-sr-only">{{ t('agentMode.filters.projectSearch') }}</span>
        <input
          ref="searchInput"
          v-model="query"
          type="search"
          role="combobox"
          aria-autocomplete="list"
          aria-controls="am-project-picker-options"
          :aria-expanded="open"
          :placeholder="t('agentMode.filters.projectSearch')"
          @keydown="onSearchKeydown"
          @input="onSearchInput"
        />
      </label>
      <div id="am-project-picker-options" class="am-project-picker__options" role="listbox">
        <button
          v-for="(project, optionIndex) in options"
          :key="project?.projectId ?? 'all'"
          type="button"
          role="option"
          :data-project-id="project?.projectId"
          :data-active-total="
            project && activeTotals?.has(project.projectId)
              ? activeTotals.get(project.projectId)
              : undefined
          "
          :aria-selected="(project?.projectId ?? null) === modelValue"
          :class="{ 'is-active': optionIndex === activeIndex }"
          @pointerenter="activeIndex = optionIndex"
          @click="choose(project)"
        >
          <AppIcon
            :name="(project?.projectId ?? null) === modelValue ? 'check' : 'folder'"
            :size="12"
            aria-hidden="true"
          />
          <span>
            <strong>{{
              project
                ? [project.projectKey, project.projectName].filter(Boolean).join(' · ')
                : t('agentMode.filters.allProjects')
            }}</strong>
            <small v-if="project && activeTotals?.has(project.projectId)">
              {{
                activeTotals.get(project.projectId) === 0
                  ? t('agentMode.filters.projectEmpty')
                  : t('agentMode.filters.projectCount', { n: activeTotals.get(project.projectId)! })
              }}
            </small>
          </span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.am-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
.am-project-picker {
  position: relative;
  flex: 0 1 270px;
  min-width: 180px;
}
.am-project-picker__trigger {
  display: grid;
  width: 100%;
  height: 30px;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px;
  padding: 0 9px;
  border: 1px solid var(--am-line);
  border-radius: 9px;
  background: var(--am-surface);
  color: var(--am-ink);
  font: inherit;
  font-size: 12px;
  text-align: left;
}
.am-project-picker__trigger span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.am-project-picker__trigger:focus-visible {
  outline: 2px solid var(--am-focus);
  outline-offset: 2px;
}
.am-project-picker__popover {
  position: absolute;
  z-index: 20;
  top: calc(100% + 6px);
  left: 0;
  width: min(360px, calc(100vw - 40px));
  padding: 7px;
  border: 1px solid var(--am-line-strong);
  border-radius: 12px;
  background: var(--am-surface);
  box-shadow: 0 14px 36px color-mix(in srgb, var(--am-ink) 18%, transparent);
}
.am-project-picker__search {
  display: flex;
  height: 32px;
  align-items: center;
  gap: 7px;
  padding: 0 8px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
}
.am-project-picker__search input {
  min-width: 0;
  flex: 1;
  border: 0;
  outline: 0;
  background: transparent;
  color: var(--am-ink);
  font: inherit;
  font-size: 12px;
}
.am-project-picker__options {
  display: grid;
  max-height: min(360px, 60vh);
  gap: 2px;
  margin-top: 6px;
  overflow: auto;
}
.am-project-picker__options button {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  align-items: center;
  gap: 6px;
  padding: 7px 8px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--am-ink);
  font: inherit;
  text-align: left;
}
.am-project-picker__options button.is-active,
.am-project-picker__options button:hover {
  background: color-mix(in srgb, var(--am-select) 8%, var(--am-surface));
}
.am-project-picker__options button:focus-visible {
  outline: 2px solid var(--am-focus);
  outline-offset: -2px;
}
.am-project-picker__options span {
  display: grid;
  min-width: 0;
}
.am-project-picker__options strong {
  overflow: hidden;
  font-size: 11.5px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.am-project-picker__options small {
  color: var(--am-muted);
  font-size: 10px;
}
@media (max-width: 640px) {
  .am-project-picker {
    flex-basis: 100%;
    max-width: none;
  }
}
</style>
