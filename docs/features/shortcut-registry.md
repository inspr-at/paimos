# Shared shortcut runtime

The Agents keyboard entries and the migrated header shortcuts are defined in
`web/src/lib/shortcuts.ts`. AppHeader installs the dispatcher in capture for app
keys and sequences, and in bubble for page single keys. Chat and focused controls
can consume page keys first; the dispatcher respects `defaultPrevented`. Components
attach a handler with `onAction(id, handler, { enabled, when })` and dispose it
on unmount. A handler returns `false` when the action does not apply; its key then
continues to the focused control. Capture the selected record when acting, and
retain the action's existing permission and confirmation checks.

`enabled()` controls execution and hints. `when(event)` restricts a handler to
its declared page, list, row or focused divider. `shortcutKeys(id)`,
`shortcutHint(id)` and `shortcutHelp()` resolve the same bindings; an entry
without an enabled handler has no hint. `shortcutCaps(binding)` supplies the
existing KeyCap component with chords and sequence steps. Defaults remain the
resolved bindings in release 130. Person overrides and their editor belong to
AEON-1202.

Search (`f`, `/`), layout (`s`), filter, grouping and divider entries are ready
for their owning Agents packages to attach. This package migrates the existing
Agents navigation, open, pause, resume and close handlers, plus the header's
palette, shortcut help and place sequences. It does not invent search or split
controls ahead of those packages.

Plain keys ignore fields, contenteditable, open menus and dialogs, consumed
events and IME composition. Cmd/Ctrl+K retains its existing palette behavior.
Matching uses `event.key` for German shifted and numpad `/`; only non-Latin
letters fall back to `event.code`. A place sequence may finish through the
release menu its prefix opened. Fields, unrelated layers, pointer actions,
window blur, identity changes, detached owners and expiry cancel the sequence.
The conflict test rejects duplicate keys in overlapping registry scopes.
