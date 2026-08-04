<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  Licensed under AGPL-3.0-only; see LICENSE.
-->
<script setup lang="ts">
// Dev text transcript source (PAI-704). Speech-to-text is out of scope for
// v1 — this typed/pasted input is the reference TranscriptSource until a mic
// source (Web Speech / external STT pipeline) plugs into the same chunk API.
import { ref } from "vue";

const props = defineProps<{
  disabled?: boolean;
}>();

const emit = defineEmits<{
  (e: "chunk", text: string): void;
}>();

const draft = ref("");

function send() {
  const text = draft.value.trim();
  if (!text || props.disabled) return;
  emit("chunk", text);
  draft.value = "";
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
    e.preventDefault();
    send();
  }
}
</script>

<template>
  <div class="ti-root">
    <textarea
      v-model="draft"
      v-auto-grow
      class="ti-input"
      rows="2"
      :disabled="disabled"
      placeholder="Type or paste what you would say… (⌘⏎ to send)"
      @keydown="onKeydown"
    />
    <button class="ti-send" type="button" :disabled="disabled || !draft.trim()" @click="send">
      Send
    </button>
  </div>
</template>

<style scoped>
.ti-root {
  display: flex;
  gap: 8px;
  align-items: flex-end;
}
.ti-input {
  flex: 1;
  resize: none;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg-card);
  color: var(--text);
  font: inherit;
  font-size: 13px;
}
.ti-input:focus {
  outline: none;
  border-color: var(--brand-blue);
}
.ti-send {
  padding: 8px 16px;
  border: 1px solid var(--brand-blue);
  border-radius: 8px;
  background: var(--brand-blue);
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
}
.ti-send:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
