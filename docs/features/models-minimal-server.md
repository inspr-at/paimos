# Models minimal server

The simple Models view reads first-build preferences through `GET /api/model-preferences/simple?for=me|default`. It returns the stored first pick, native effort, personal ownership, workspace lock attribution, exceptions, cross-family review mode, next queued work, unavailable choices with reasons, new lines, the selected profile revision and the workspace rule revision. Existing ranked tails, situations, templates and policies remain active.

Native effort writes use the existing column thinking route with `effort` and `revision`. Personal writes retain `If-Prefs-Person`. A new pick retains a registered effort name or selects the nearest shared effort level, resolving ties upward. Reviews retain xhigh and the cross-family rule.

A person with `models.manage` can edit a harness/model line through `PUT /api/models/lines/{harness}/{model}`. It creates immutable replacement profiles and retires old pins in one transaction, moving affected orders, rules and role routes before appending the audit event. An optional registry revision from the usage preview rejects stale edits. Source is assigned by the server; notes are admin-owned and limited to 80 characters.

`GET /api/models/lines/{harness}/{model}/usage` computes qualified replacements without writing. Members see workspace and their own usages; workspace preference managers can see other people, with project read permissions still enforced. Hidden usages set `incomplete`.

Retirement accepts a future `retire_at`. The profile remains eligible until that exact time; routing excludes it immediately at the boundary, while the scheduler records the normal retirement once. DELETE cancels retirement or restores the pin.

With auto-update enabled, discovery accepts registered successor versions of lines used by orders or rules through AEON-1000's shared successor policy. New and unused lines remain pending for person acceptance. A person-triggered Check now bypasses the configured scheduler interval and observes a five-minute tenant cooldown; 429 includes `retry_after` seconds and `Retry-After`. Agents and scheduled refreshes retain the configured interval.

Fallback checks policy and capability before the shared account qualification, then walks the column, the default order on the deciding layer, and the job's role ladder. It records skip reasons without rewriting stored ranks. Native Grok supports medium, high and xhigh; the confined launcher passes and verifies the selected effort.

Validation: `internal/modelregistry/simple_test.go` covers the eight server additions and fallback; `internal/agentd/grok_effort_test.go` covers native launcher arguments. Both migrations add nullable columns and retain old writers.
