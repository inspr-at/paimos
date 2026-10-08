<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../../directives/clipTip'
defineProps<{ text: string }>()
</script>
<template><span v-clip-tip="text" class="hover-text">{{ text }}</span></template>
<style scoped>.hover-text { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }</style>
