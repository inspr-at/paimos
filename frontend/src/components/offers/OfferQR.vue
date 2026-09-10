<script setup lang="ts">
import { computed } from 'vue'
import qrcode from 'qrcode-generator'
const props = defineProps<{ url: string }>()
const qr = computed(() => {
  const code = qrcode(0, 'M')
  code.addData(props.url, 'Byte')
  code.make()
  const size = code.getModuleCount()
  const squares: string[] = []
  for (let y = 0; y < size; y++)
    for (let x = 0; x < size; x++) {
      if (code.isDark(y, x)) squares.push(`M${x + 4},${y + 4}h1v1h-1z`)
    }
  return { size: size + 8, path: squares.join('') }
})
</script>
<template>
  <div class="qr">
    <div class="box">
      <div class="cap">
        <b>Angebot online ansehen und annehmen</b><span>PDF und Annahme im Kundenbereich</span
        ><a :href="url" rel="noreferrer">{{ url }}</a>
      </div>
      <svg
        :viewBox="`0 0 ${qr.size} ${qr.size}`"
        role="img"
        aria-label="QR-Code zum Kundenangebot"
        shape-rendering="crispEdges"
      >
        <rect width="100%" height="100%" fill="white" />
        <path :d="qr.path" fill="black" />
      </svg>
    </div>
  </div>
</template>
<style scoped>
.cap a {
  display: block;
  max-width: 110mm;
  overflow-wrap: anywhere;
  font: 7.5pt monospace;
  color: inherit;
  text-decoration: none;
}
</style>
