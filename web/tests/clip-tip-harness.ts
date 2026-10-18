// SPDX-License-Identifier: AGPL-3.0-only
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import '../src/styles/tokens.css'
import '../src/styles/base.css'
import ClipTipHarness from './fixtures/ClipTipHarness.vue'
import { useSession } from '../src/stores/session'
import { useProjects } from '../src/stores/projects'
import { recents } from '../src/lib/recents'
import { longName } from './clip-tip-fixtures'

const app = createApp(ClipTipHarness)
app.use(createPinia())
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: ClipTipHarness }] })
app.use(router)
await useSession().refresh()
await useProjects().load()
recents.splice(0, recents.length,
  { type: 'ticket', key: 'PHAROS-14', title: longName, state: 'backlog', kind: 'ticket', projectKey: 'PHAROS' },
  { type: 'ticket', key: 'PHAROS-15', title: 'Kurz', state: 'backlog', kind: 'ticket', projectKey: 'PHAROS' },
)
app.mount('#app')
