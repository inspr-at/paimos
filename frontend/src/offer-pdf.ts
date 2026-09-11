import { createApp, h, nextTick } from 'vue'
import OfferDocument from '@/components/offers/OfferDocument.vue'
import type { PublicOffer } from '@/components/offers/types'

// Dedicated print entry: no router, authentication, analytics or external requests.
async function render() {
  const response = await fetch('/payload', { credentials: 'omit', cache: 'no-store' })
  if (!response.ok) throw new Error('PDF data unavailable')
  const data = (await response.json()) as { offer: PublicOffer; publicUrl: string }
  let overflow = ''
  const app = createApp({
    render: () =>
      h(OfferDocument, {
        offer: data.offer,
        publicUrl: data.publicUrl,
        onOverflow: (value: string) => {
          overflow = value
        },
      }),
  })
  app.mount('#app')
  await document.fonts.ready
  await nextTick()
  await new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  )
  if (!overflow && document.querySelector('.sheet .page')) document.body.dataset.pdfReady = 'true'
}
void render()
