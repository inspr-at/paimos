<script setup lang="ts">
import { ref, watch } from 'vue'
import { Minus, Plus } from 'lucide-vue-next'

import {
  decrementPaimos6Zoom,
  incrementPaimos6Zoom,
  isCanonicalPaimos6Zoom,
  type Paimos6ZoomBand,
} from '@/v6/sessionHomeZoom'

const props = defineProps<{
  modelValue: string
  band: Paimos6ZoomBand
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const draft = ref(props.modelValue)

watch(() => props.modelValue, (value) => {
  draft.value = value
})

function commit() {
  if (!isCanonicalPaimos6Zoom(draft.value)) {
    draft.value = props.modelValue
    return
  }
  emit('update:modelValue', draft.value)
}

function choose(value: string) {
  draft.value = value
  emit('update:modelValue', value)
}
</script>

<template>
  <fieldset class="p6-zoom" aria-describedby="p6-zoom-help" style="min-width: 0; max-width: 100%; box-sizing: border-box;">
    <legend>Semantic zoom</legend>
    <div class="p6-zoom-actions">
      <button
        type="button"
        aria-label="Zoom out by one"
        :disabled="modelValue === '1'"
        @click="choose(decrementPaimos6Zoom(modelValue))"
      >
        <Minus :size="14" aria-hidden="true" />
      </button>
      <label>
        Zoom value
        <input
          v-model="draft"
          type="text"
          inputmode="numeric"
          pattern="[1-9][0-9]*"
          maxlength="64"
          autocomplete="off"
          spellcheck="false"
          @change="commit"
          @keydown.enter.prevent="commit"
        >
      </label>
      <button type="button" aria-label="Zoom in by one" @click="choose(incrementPaimos6Zoom(modelValue))">
        <Plus :size="14" aria-hidden="true" />
      </button>
      <output aria-live="polite">{{ band }}</output>
    </div>
    <div class="p6-landmarks" aria-label="Zoom landmarks">
      <button v-for="value in ['1', '10', '100', '1000']" :key="value" type="button" @click="choose(value)">
        {{ value }}{{ value === '1000' ? '+' : '' }}
      </button>
    </div>
    <p id="p6-zoom-help">Enter any positive whole number. The sample stays bounded; the zoom has no product maximum.</p>
  </fieldset>
</template>

<style scoped>
.p6-zoom {
  min-width: 0;
  max-width: 100%;
  margin: 0;
  padding: 12px 14px;
  border: 1px solid #d7e0da;
  border-radius: 13px;
  background: rgba(251, 252, 250, 0.84);
}
.p6-zoom legend { padding: 0 5px; color: #53645b; font-size: 10px; font-weight: 750; letter-spacing: 0.07em; text-transform: uppercase; }
.p6-zoom-actions { display: flex; min-width: 0; max-width: 100%; align-items: end; flex-wrap: wrap; gap: 7px; }
.p6-zoom-actions label { display: grid; min-width: 0; flex: 1 1 160px; gap: 4px; color: #59655e; font-size: 9px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; }
.p6-zoom-actions input { box-sizing: border-box; width: 100%; min-width: 0; min-height: 34px; padding: 6px 9px; border: 1px solid #bdcbc3; border-radius: 8px; color: #31443a; background: #fff; font: 600 12px/1 "JetBrains Mono", monospace; }
.p6-zoom-actions button,
.p6-landmarks button { min-height: 34px; border: 1px solid #cbd7d0; border-radius: 8px; color: #425a4d; background: #fff; }
.p6-zoom-actions button { display: inline-grid; width: 34px; place-items: center; }
.p6-zoom-actions button:disabled { cursor: not-allowed; opacity: 0.42; }
.p6-zoom-actions output { align-self: center; padding: 4px 7px; border-radius: 999px; color: #315b47; background: #edf6f0; font-size: 9px; font-weight: 750; text-transform: uppercase; }
.p6-landmarks { display: flex; max-width: 100%; flex-wrap: wrap; gap: 5px; margin-top: 8px; }
.p6-landmarks button { min-height: 25px; padding: 3px 8px; font: 600 9px/1 "JetBrains Mono", monospace; }
.p6-zoom button:hover { border-color: #8eaa9a; }
.p6-zoom button:focus-visible,
.p6-zoom input:focus-visible { outline: 3px solid rgba(47, 107, 82, 0.3); outline-offset: 2px; }
.p6-zoom p { margin-top: 7px; color: #68756e; font-size: 9px; line-height: 1.45; }
</style>
