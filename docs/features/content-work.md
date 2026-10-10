# Content work without a release

Scene edits, theme files and other work created with the tool can be marked
**No release needed** in the ticket panel, beside **Hide from release notes**.
The mark is an optional boolean in `fields.no_release_needed`; omission and
`false` retain the software release path.

When marked work reaches Done, the status autopilot uses the workspace's
`rules.accept.days` delay, counted from the current Done episode. A one-day
policy accepts it after one day without a software release. Pending human checks,
person objections (including undoing automatic acceptance), stored delivery
objections, disabled acceptance and project/workspace automation settings still
apply. Grouping parents continue to derive their status from their leaves.

The release-note builders omit marked work when the capture retains the mark.
The existing hide setting remains independent. Marking work
clears a previous missed-release flag; the autopilot never creates that flag for
marked work. Benefit completion requirements remain in force.

**Deployment is blocked:** live and newly frozen database captures currently
discard `no_release_needed`, so marked content still appears in release notes.
A coordinator-reserved expansion migration must add the mark to the capture
allowlist and retain it alongside `hide_from_release_notes` for deleted or
non-work members before this feature is deployed. Previously frozen captures
retain their original contents; changing a current ticket does not rewrite
historical release notes.

The CLI supports the mark on creation and update:

```sh
aeon issue create --project EXAMPLE --title "Edit a scene" --no-release-needed true
aeon issue update EXAMPLE-12 --no-release-needed false
```

`issue get --json` exposes `no_release_needed` when true. API callers read the
boolean in node fields and write it with the existing revision-checked node
mutation endpoints. Strings, numbers and null are rejected. Changing the mark
does not alter the configured acceptance delay or release policy for software.
