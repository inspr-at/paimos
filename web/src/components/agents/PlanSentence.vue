<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { Sentence } from '../../lib/capacity'
// A generated plan sentence: the lead in bold, figures as tabular numbers.
defineProps<{ sentence: Sentence }>()
</script>

<template>
  <template v-for="(s, i) in sentence.segs" :key="i"><b v-if="s.strong"><span v-if="s.num" class="n">{{ s.text }}</span><template v-else>{{ s.text }}</template></b><span v-else-if="s.num" class="n">{{ s.text }}</span><template v-else>{{ s.text }}</template></template>
</template>
