// SPDX-License-Identifier: AGPL-3.0-only
// Login names identify a local CLI instance, not an agent. Never put a key in
// the command, URL, storage, or shell history: login reads it at a hidden prompt.
export function agentLoginCommand(origin: string): string {
  const url = new URL(origin)
  // Brackets and colons in an IPv6 hostname must not become a leading dash:
  // the CLI would read `--instance -…` as another flag.
  const name = url.hostname.replace(/[\[\]]/g, '').replace(/[^a-zA-Z0-9._-]/g, '-').replace(/^-+/, '').slice(0, 64) || 'aeon'
  const quote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`
  return `paimos --instance ${name} auth login --name ${name} --url ${quote(url.origin)}`
}
