// SPDX-License-Identifier: AGPL-3.0-only
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'
import Harness from './fixtures/ModelsSettingsHarness.vue'
import SettingsView from '../src/views/SettingsView.vue'
import { vClipTip } from '../src/directives/clipTip'
import '../src/styles/tokens.css'
import '../src/styles/base.css'
import '../src/styles/settings.css'
const router = createRouter({ history: createWebHistory(), routes: [
  { path: '/tests/models-settings-harness.html', redirect: to => ({ path: '/settings/models', query: to.query, hash: to.hash }) },
  { path: '/settings/models/board', redirect: '/settings/models' },
  { path: '/settings/:section', component: SettingsView },
] })
const app = createApp(Harness).use(createPinia()).use(router)
app.directive('clip-tip', vClipTip)
await router.isReady()
app.mount('#app')
