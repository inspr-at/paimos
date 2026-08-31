<script setup lang="ts">
import { CircleDot } from 'lucide-vue-next'
</script>

<template>
  <div class="p6-source-rail" aria-label="Paimos sources">
    <span class="p6-source is-active">
      <CircleDot :size="12" aria-hidden="true" /> Paimos · active source
    </span>
    <span class="p6-source is-dim" aria-disabled="true">Pharos · dim source</span>
    <span class="p6-source is-dim" aria-disabled="true">Janus · dim source</span>
  </div>
</template>

<style scoped>
.p6-source-rail {
  display: flex;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 7px;
}
.p6-source {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 9px;
  border: 1px solid #d8e2dc;
  border-radius: 999px;
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.055em;
  text-transform: uppercase;
}
.p6-source.is-active { color: #2d6048; background: #edf6f0; }
.p6-source.is-dim { border-style: dashed; color: #6c7771; background: rgba(255, 255, 255, 0.42); opacity: 0.64; }

@media (max-width: 680px) {
  .p6-source-rail { gap: 5px; }
  .p6-source { padding: 4px 7px; font-size: 8.5px; }
}
</style>
