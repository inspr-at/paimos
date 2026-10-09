# Model registry card

Settings › Models ends with the **Model registry**: every model PAIMOS can run, one line each. It replaces the Catalog freshness fold of the old Models page; the old `#model-refresh` and Agents `#models` links open it.

Everyone who can read models sees the list: the model's name (with its version), its note, then harness · route · slug · source, and its own thinking levels. A line shows **New** and "off until used" while a discovered model has not been taken into use, **Retiring 31 Oct** while a retirement date is scheduled, and "was 6, taken on 3 Oct" when auto-update took a newer version of a line on. Levels follow the server's effort order.

People with `models.manage` edit; everyone else reads.

- **Auto-update** switches `auto_add_profiles`. The whole settings object is written back, so the settings this page does not show keep their values. A failed save puts the switch back and says so in the strip.
- **Check now** needs `models.refresh`. It skips the refresh interval, and the server holds a five-minute cooldown. The button keeps one size through *Check now*, *Checking…* and *Checked · again in 5 min*; a 429 shows the wait the server names. With vendor model lists switched off, the message says nothing new could be found rather than "up to date". A check whose sources are stale or limited says discovery was incomplete or partial. A newer version of a line already in use is "taken on" only when the check names the accepted model, and it is not called a new model. Several effort profiles of one accepted model count as one version. Profiles added without that acceptance, including a disabled discovery under a known line, are reported as profiles added.
- **Add model** opens the form under the strip. pi through OpenRouter takes any OpenRouter slug (`qwen/qwen3-coder` is stored as `openrouter/qwen/qwen3-coder`); gemini and OpenCode are in the harness list. One level is one `POST /api/models`; several levels are registered in order with one `PUT /api/models/lines/{harness}/{model}` afterwards. If that second write fails, the line exists with its first level and the form says so; nothing is reported as saved.
- **Edit** opens under its entry and creates a new version through the same `PUT`. Name, note and levels can change; an auto-discovered entry keeps its harness, route and slug, a hand-added one can change route and slug. The save sends the registry revision together with the profiles from that same usage read. Typing on the list from before that read, when the line has changed, is a conflict: the form closes, the list reloads, and the draft is not kept. A stale revision does the same. **Undo** writes the earlier values back at the revision that edit returned.
- **Remove** lives inside Edit. It asks `GET /api/models/lines/{harness}/{model}/usage` first and names each use with that use's own replacement and effort. A use with no qualified fallback says so. It cannot go ahead without that answer. Removal retires every level; **Undo** restores them.

Everything saves at once with a toast and Undo. Esc leaves a field, then closes the remove question, then the form; ⌘↵ (Ctrl+↵ elsewhere) saves from a field.

Parked for expert mode and not rebuilt here: refresh interval, agent reports, vendor API discovery and its keys, and choosing an older version of a model line.
