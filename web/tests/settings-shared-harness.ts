// SPDX-License-Identifier: AGPL-3.0-only
import { createApp } from 'vue'
import '../src/styles/tokens.css'
import '../src/styles/base.css'
import Harness from './fixtures/SettingsSharedHarness.vue'
createApp(Harness).mount('#app')
