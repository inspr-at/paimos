// SPDX-License-Identifier: AGPL-3.0-only
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'
import Harness from './fixtures/ModelsBoardHarness.vue'
import ModelBoardRoute from '../src/components/settings/models/ModelBoardRoute.vue'
import '../src/styles/tokens.css'
import '../src/styles/base.css'
import '../src/styles/settings.css'
const router = createRouter({ history: createWebHistory(), routes: [{ path: '/:pathMatch(.*)*', component: Harness }, { path: '/settings/models/board', component: ModelBoardRoute }] })
const app = createApp(Harness).use(createPinia()).use(router)
await router.isReady()
app.mount('#app')
