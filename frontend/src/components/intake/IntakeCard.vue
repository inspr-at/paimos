<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  Licensed under AGPL-3.0-only; see LICENSE.
-->
<script setup lang="ts">
// PAI-715: collapsible right-column card for the Voice Intake workbench.
// Collapsed by default — the header row carries a one-line summary of the
// content (the #summary slot), so the column stays scannable while the
// session grows. Open/closed state persists per card id.
import { ref, watch } from "vue";

import AppIcon from "@/components/AppIcon.vue";
import { LS_INTAKE_CARDS } from "@/constants/storage";

const props = defineProps<{
  id: string;
  title: string;
}>();

function readState(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(LS_INTAKE_CARDS) ?? "{}") as Record<string, boolean>;
  } catch {
    return {};
  }
}

const open = ref(readState()[props.id] === true); // default collapsed

watch(open, (v) => {
  const state = readState();
  state[props.id] = v;
  localStorage.setItem(LS_INTAKE_CARDS, JSON.stringify(state));
});
</script>

<template>
  <section class="vi-card ic-card" :class="{ 'ic-open': open }">
    <header class="ic-head" role="button" tabindex="0" @click="open = !open" @keydown.enter="open = !open">
      <AppIcon :name="open ? 'chevron-down' : 'chevron-right'" :size="14" class="ic-chev" />
      <h2 class="ic-title">{{ title }}</h2>
      <div v-if="!open" class="ic-summary" @click.stop>
        <slot name="summary" />
      </div>
      <div v-if="open" class="ic-extra" @click.stop>
        <slot name="headerExtra" />
      </div>
    </header>
    <div v-if="open" class="ic-body">
      <slot />
    </div>
  </section>
</template>

<style scoped>
.ic-card {
  padding: 12px 14px;
}
.ic-head {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  user-select: none;
  min-height: 24px;
}
.ic-chev {
  flex-shrink: 0;
  color: var(--text-muted);
}
.ic-title {
  margin: 0;
  font-size: 14px;
  white-space: nowrap;
}
.ic-summary {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
  min-width: 0;
  overflow: hidden;
  font-size: 12px;
  color: var(--text-muted);
  cursor: default;
}
.ic-extra {
  margin-left: auto;
  cursor: default;
}
.ic-body {
  margin-top: 10px;
}
</style>
