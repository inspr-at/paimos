// SPDX-License-Identifier: AGPL-3.0-only
// Login names identify a local CLI instance, not an agent. Never put a key in
// the command, URL, storage, or shell history: login reads it at a hidden prompt.
// `auth login` stores the instance under --name; the global --instance flag only
// selects an instance that already exists, so the command leaves it out. The
// name is the workspace's slug (AEON-730), else the host name.
export function agentLoginCommand(origin: string, workspace?: string): string {
  const url = new URL(origin)
  // Brackets and colons in an IPv6 hostname must not become a leading dash:
  // the CLI would read `--name -…` as another flag.
  const safe = (value: string) => value.replace(/[\[\]]/g, '').replace(/[^a-zA-Z0-9._-]/g, '-').replace(/^[-.]+/, '').slice(0, 64)
  const name = safe(workspace ?? '') || safe(url.hostname) || 'aeon'
  const quote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`
  return `paimos auth login --name ${name} --url ${quote(url.origin)}`
}
