# Content-free harness protocol fixtures

These checked-in, sanitized protocol examples contain no login, identity or
completion content. Tests replay files and injected RPCs; no installed harness
or vendor connection is used.

- Codex: public `model_list.rs` remote-only model fixture, projected to its
  camelCase response, including native `focused`, hidden and upgrade metadata:
  https://github.com/openai/codex/blob/main/codex-rs/app-server/tests/suite/v2/model_list.rs
- pi: documented `get_available_models` and `get_available_thinking_levels`
  response envelopes:
  https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/rpc-commands.md
- OpenCode: the public models command emits provider/id followed by pretty JSON:
  https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/cli/cmd/models.ts
- Claude: the existing recorded fake SDK response from
  `TestClaudeBridgeSettingsUseOfficialSetters`; the ID-only row exercises unknown.
- Cursor: an ID/label transcript for the documented `models` command. The
  effort suffix is deliberately NOT evidence of an effort. This is a parser
  regression example, not qualification of a live binary or its wire format:
  https://cursor.com/docs/cli/reference/parameters

Fixture reductions add ID-only rows to exercise missing fields. They do not
assert availability of these models or invent a shared effort scale. Reviewed
formats: 2026-10-11. Unrecognized vendor output remains stale/unknown.
