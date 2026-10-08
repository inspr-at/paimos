# Model preferences

`aeon model resolve --ticket AEON-123` resolves the ticket's work kind,
complexity and role through Default → You → Project preferences and prints a
why line. `aeon model prefs [--project KEY]` reads the effective matrix. Both
commands are read-only; role-only `model resolve build` retains its existing
advisory response.

In the web app, open **Model preferences** from **More agent actions** or a project's
agents popover. The **Why this model?** planning cell opens its kind of work.
Choose Automatic, follow new versions or pin a version at Default, You or
Project. Kinds can be added or removed at Default and Project; system kinds stay.
Row locks have a Set by menu and an Option/Alt-click shortcut. A looser provider
choice below a lock is allowed with a warning. Retired versions are hidden;
review qualification and provider evidence explain disabled picker choices.
Save progress, failures and refreshed conflicts appear beside the pinned footer
actions, in a reserved two-line slot that wraps and scrolls for longer messages.
Opening reset confirmation clears stale feedback and announces the reset scope.
Keyboard resets return focus to the row’s model chip; removing a kind returns
focus to Everything else. Adding a kind retains focus during save and returns
focus to Add a kind of work afterward.

Model-cell hovers retain the planned and measured details and show the
**Why this model?** action. These buttons do not add sequential tab stops to the list.

The API exposes `/api/model-preferences`, level and row PUT/DELETE routes,
`/api/work-kinds` and profile retirement at `/api/models/{id}/retire`.
Use a level's returned `revision` for edits and the DELETE `revision` query
parameter (0 for an absent level). People edit their canonical You slice;
Default and Project changes require `model_prefs.manage`, and agents only read.
Provider locks permit lower choices and flag a loosening in the view and trace.
Tightening stamps active runs without stopping turns; `running_outside` identifies
starting/running turns outside the new setting. Stamps never loosen. EU/local
routes require valid account evidence; without it, work waits with `residency`.

Ticket properties choose Kind of work from the active default and project work
kinds, including Security, and confirm suggested complexity. Planning resolves
each ticket's placement with its canonical person assignee, falling back to the
viewer's canonical You setting for unassigned or agent-assigned tickets. Work-start
estimate snapshots use only the assignee; registered sessions and dispatched runs
save their starter's work placement separately from the model that actually runs.
Operator keys have no You setting. With an empty matrix, existing planning gaps
and prices remain unchanged; Security now uses the ticket's role route and rate.
Work-kind lists use `limit`/`cursor` pagination; editor writes reject oversized
matrices or atomic re-stamp scopes. See `api/openapi.yaml` for the contract.
