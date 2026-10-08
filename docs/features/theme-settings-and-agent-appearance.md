# Theme settings and agent appearance

Settings › Theme has separate Colours and Agents cards sharing Save and Discard.
The Agents card uses the nine shipped renderers, with native or chosen ring/size,
hover, five state palettes and inactive dimming. Both light and dark previews
show a live row and Working, Waiting, Throttled, Problem and Idle. Draft appearance
stays in the previews until a successful save; reduced motion stills artwork.
Personal › Agents retains heartbeat warnings and estimate display.

Migration 1235 copies saved personal appearance into an owner-only theme based
on the workspace default and selects it, unless the person already saved an
explicit theme choice. Legacy avatar/palette names and drawn geometry retain
existing appearance; linked aliases keep physical ownership for unlink. Original
preference rows remain for rollback and previous binaries, and heartbeat settings
are unchanged. Creation and selection are audited with a private audience.
Optional `agents.dim_inactive` and `agents.inactive_opacity` preserve inactive
appearance; omitted values mean true and 55 percent. Runtime reads the active
theme and drops stale appearance responses after identity changes.
