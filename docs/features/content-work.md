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

Marked work is omitted from release notes, including frozen captures and
backfilled notes. The existing hide setting remains independent. Marking work
clears a previous missed-release flag; the autopilot never creates that flag for
marked work. Benefit completion requirements remain in force.

The CLI supports the mark on creation and update:

```sh
aeon issue create --project EXAMPLE --title "Edit a scene" --no-release-needed true
aeon issue update EXAMPLE-12 --no-release-needed false
```

`issue get --json` exposes `no_release_needed` when true. API callers read the
boolean in node fields and write it with the existing revision-checked node
mutation endpoints. Strings, numbers and null are rejected. Changing the mark
does not alter the configured acceptance delay or release policy for software.
