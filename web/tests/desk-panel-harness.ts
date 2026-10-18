// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1209 removed the Decision Desk panel from the Agents page (AEON-1139); the header's
// Desk split button (AEON-1214) takes its preview. Until then the component is mounted here,
// alone, so its project links and refresh behaviour stay covered.
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'
import '../src/styles/tokens.css'
import '../src/styles/base.css'
import DecisionDeskPanel from '../src/components/agents/DecisionDeskPanel.vue'
import { useSession } from '../src/stores/session'

const app = createApp(DecisionDeskPanel)
app.use(createPinia())
// Links leave the harness for their real paths; the panel stays mounted for every route.
app.use(createRouter({ history: createWebHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] }))
await useSession().refresh()
app.mount('#app')
