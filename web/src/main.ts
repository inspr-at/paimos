// SPDX-License-Identifier: AGPL-3.0-only
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import './styles/tokens.css'
import './styles/agent-states.css'
import './styles/base.css'
import App from './App.vue'
import { router } from './router'

import { isRenderError, reportFatal } from './lib/fatal'
import { toast } from './lib/toast'
import { vClipTip } from './directives/clipTip'

const app = createApp(App)
app.directive('clip-tip', vClipTip)
app.use(createPinia())
app.use(router)
// Global error boundary: page-breaking errors show the error page, the rest a toast.
app.config.errorHandler = (error, _instance, info) => {
  console.error(error)
  if (isRenderError(String(info))) reportFatal(error, 'render')
  else toast('Something went wrong with that action. Please try again.', { tone: 'error' })
}
router.onError((error, to) => { console.error(error); reportFatal(error, 'navigation', to.fullPath) })
// Mount even when the first navigation fails, so the error page shows instead of a blank tab.
void router.isReady().catch(() => undefined).then(() => { app.mount('#app'); delete document.documentElement.dataset.appearanceLoading })
