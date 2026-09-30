// SPDX-License-Identifier: AGPL-3.0-only
import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import PublicQuoteView from '../src/public/PublicQuoteView.vue'

export function mountPublicQuote() {
  document.querySelector('#app')?.setAttribute('hidden', '')
  const host = document.createElement('div')
  host.id = 'public-quote-test'
  document.body.append(host)
  createApp({ render: () => h(PublicQuoteView, { publicTenant: 'opaque-selector', token: 'opaque-token' }) }).use(createPinia()).mount(host)
}
