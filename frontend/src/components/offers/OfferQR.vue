<script setup lang="ts">
import { computed } from 'vue'
import qrcode from 'qrcode-generator'
const props = defineProps<{ url?: string; preview?: boolean }>()
const qr = computed(() => {
  const code = qrcode(0, 'M')
  code.addData(props.preview ? 'ANGEBOT VORSCHAU - KEIN KUNDENLINK' : (props.url ?? ''), 'Byte')
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
        <b>Angebot online ansehen und annehmen</b><span>PDF und Annahme im Kundenbereich</span>
        <span v-if="preview" class="preview-link">Kundenlink folgt beim Finalisieren</span>
        <a v-else-if="url" :href="url" rel="noreferrer"
          >{{ url.slice(0, url.lastIndexOf('/') + 1) }}<wbr /><span class="token">{{
            url.slice(url.lastIndexOf('/') + 1)
          }}</span></a
        >
      </div>
      <div class="qr-code">
        <svg
          :viewBox="`0 0 ${qr.size} ${qr.size}`"
          role="img"
          :aria-label="
            preview ? 'QR-Vorschau ohne gültigen Kundenlink' : 'QR-Code zum Kundenangebot'
          "
          shape-rendering="crispEdges"
        >
          <rect width="100%" height="100%" fill="white" />
          <path :d="qr.path" fill="black" />
        </svg>
        <span
          v-if="preview"
          class="preview-badge"
          title="Wird beim Finalisieren durch den echten Kundenlink ersetzt."
          >Vorschau</span
        >
      </div>
    </div>
  </div>
</template>
<style scoped>
.offer-document .qr .box {
  width: 100%;
  grid-template-columns: minmax(0, 1fr) 26mm;
  text-align: left;
}
.token {
  white-space: nowrap;
}

.cap a,
.preview-link {
  display: block;
  max-width: 136mm;
  overflow-wrap: anywhere;
  font: 7.5pt monospace;
  color: inherit;
  text-decoration: none;
}
.qr-code {
  position: relative;
  width: 26mm;
  height: 26mm;
}
.preview-badge {
  position: absolute;
  inset: 0;
  margin: auto;
  width: max-content;
  height: max-content;
  padding: 1.3mm 2mm;
  border: 0.25mm solid var(--muted);
  border-radius: 1mm;
  background: rgb(255 255 255 / 90%);
  color: var(--ink);
  font: 600 9pt var(--sans);
  print-color-adjust: exact;
}
</style>
