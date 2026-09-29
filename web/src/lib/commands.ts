// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'

// Cross-page commands: the palette and the account menu ask, the page that can act listens.
export type Command = { name: 'new-ticket'; projectKey: string } | { name: 'new-knowledge'; projectKey: string } | { name: 'shortcuts' } | { name: 'releases'; version?: string } | { name: 'palette' } | { name: 'new-quote' } | { name: 'log-time'; nodeId?: string }
export const command = ref<{ command: Command; at: number } | null>(null)
export function run(next: Command) { command.value = { command: next, at: Date.now() } }
export function consume() { command.value = null }
