<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — detail lever (semantic zoom 1 / 10 / 100). Detail 10 ships
  here; 1 (PAI-806) and 100 (PAI-807) render through their seams.
-->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'

export type DetailLevel = 1 | 10 | 100

defineProps<{ level: DetailLevel }>()
const emit = defineEmits<{ 'update:level': [level: DetailLevel] }>()
const { t } = useI18n()
const LEVELS: DetailLevel[] = [1, 10, 100]
</script>

<template>
  <div class="am-lever" role="group" :aria-label="t('agentMode.detail.level')">
    <span class="am-lever-label">{{ t('agentMode.detail.level') }}</span>
    <div class="am-lever-options">
      <button
        v-for="l in LEVELS"
        :key="l"
        type="button"
        class="am-lever-btn"
        :class="{ 'is-active': l === level }"
        :aria-pressed="l === level ? 'true' : 'false'"
        :title="t(`agentMode.detail.levelTitle.${l}`)"
        @click="emit('update:level', l)"
      >
        {{ l }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.am-lever { display: inline-flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-muted); }
.am-lever-label { white-space: nowrap; }
.am-lever-options {
  display: inline-flex;
  padding: 2px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--bg-card);
}
.am-lever-btn {
  min-width: 34px;
  padding: 3px 8px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--text-muted);
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px;
}
.am-lever-btn.is-active {
  background: color-mix(in srgb, var(--text) 9%, var(--bg-card));
  color: var(--text);
  font-weight: 600;
}
.am-lever-btn:focus-visible { outline: 2px solid var(--text); outline-offset: 1px; }
</style>
