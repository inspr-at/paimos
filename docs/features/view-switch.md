# View switches

Every place with views opens the way the person left it (AEON-1453). One generic
`ViewSwitch` (`web/src/components/ViewSwitch.vue`) wraps the existing segmented look
(tabs, the project's sections or the round `.seg`) and adds only the person's default;
`useViewSwitch` (`web/src/lib/viewSwitch.ts`) remembers.

- **The view used last** opens the next visit, on every device: it is kept per person
  and workspace on the server, one preference key per switch (`view:<name>`, with the
  project id where the switch belongs to a project). Nothing is kept in browser storage.
- **An explicit default** wins over the view used last. Right-click a segment (desktop),
  hold it (touch), or press Shift+F10 or the context-menu key on a focused segment: a
  small menu offers **Open here by default**, or **Stop opening here by default** when it
  already is. The default segment carries a small pin in a reserved slot beside the label,
  centred on it, or as a badge inside an icon-only segment (it never moves a
  segment); its tooltip reads "<Label> · default". Setting a default never switches the
  view on screen; it is for the next visit.
- **A link wins once.** `?view=`, a tab address or `?mode=` opens that view without
  changing the default or the view used last.

| Switch | Key | Notes |
|---|---|---|
| Projects: List · Cards | `view:projects` | `projects.view` counts as used last until replaced. |
| Project sections | `view:project-section:<project>` | A bare `/p/KEY` opens the default or last used section; Settings is never remembered as used last. |
| Ticket list: List · Outline · Graph | `view:tickets:<project>` | The default may also be a saved view (`saved:<id>`), set from the saved view's own menu (right-click). `list:<project>.defaultView` ("Open <project> with it") counts until a default is set or stopped here; it is never rewritten. |
| Knowledge: Entries · Graph | `view:knowledge:<project>` | |
| Delivery: Numbers · Flow | `view:delivery` | Window and level keep their values in `delivery:numbers`, with their defaults beside them. |
| Release history: EN · DE, Highlights · Details | `view:releases.lang`, `view:releases` | A language this browser kept before moves to the server once and leaves the browser. |
| Settings › Access tabs | `view:access` | The tabs keep their own look; `/settings/access/<tab>` is the link. |
| Standalone session Events · Terminal | `view:session-expert` | |
| Quote editor, docked side panel | `view:quote.pane` | Details or Format, or closed; `quote-editor.pane` counts until replaced. |

The session panel tabs and their "Open on Lead by default" pin follow in a second package.
