<script setup lang="ts">
import { ref, watch } from 'vue'

import { DEFAULT_BRAND_LOGO } from '@/composables/brandingAssets'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  src?: string | null
  alt?: string
}>(), { src: null, alt: '' })

const resolvedSrc = ref(DEFAULT_BRAND_LOGO)
const visible = ref(true)
let fallbackAttempted = false

watch(
  () => props.src,
  (src) => {
    resolvedSrc.value = src?.trim() || DEFAULT_BRAND_LOGO
    visible.value = true
    fallbackAttempted = false
  },
  { immediate: true },
)

function onError() {
  if (!fallbackAttempted && resolvedSrc.value !== DEFAULT_BRAND_LOGO) {
    fallbackAttempted = true
    resolvedSrc.value = DEFAULT_BRAND_LOGO
    return
  }
  // The canonical asset failing means the deployment is incomplete. Hide the
  // image instead of retrying the same URL in an error loop.
  visible.value = false
}
</script>

<template>
  <img
    v-if="visible"
    v-bind="$attrs"
    :src="resolvedSrc"
    :alt="alt"
    :data-logo-fallback="fallbackAttempted ? 'true' : 'false'"
    @error="onError"
  />
</template>
