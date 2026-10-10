# Models v2 resolver

Live model choices use Settings → Models for build, Quick jobs, review, escalation and ticket route reads. Build follows the ticket kind’s row or Default. Build-hard uses the Stuck situation and requires a strong or frontier model. A workspace pick lock also freezes the order and effort; otherwise personal rows precede workspace rows.

Scout and mechanical work try fast models by catalog input plus output list price, with registry order breaking ties. Missing prices follow known prices. If no fast model qualifies, Default runs. Each model step tries its registered routes before the next model, translating effort to each route’s nearest native level and resolving ties upward.

Account eligibility retains project access, live harness health, model grants, schedules, approval, residency and daily caps. Missing quota is unknown and does not imply exhaustion. A capped route can use another registered route of the same model. When the visible order is exhausted, `owner_required` is true and the existing trace preserves the first cause, including its known reset time when sharing permits disclosure. No role ladder is appended.

Review uses an explicit Review situation of the author’s kind, or the automatic family order. It excludes the author’s family, and security review also excludes Anthropic. Strong and frontier reviewers run at their highest registered native level, at least the xhigh equivalent. Account approval, native Grok enrollment and additional project family policy remain required. Escalation retains work-area restrictions, episode exclusions and live escalation-room checks.

The reversible design decisions are `modelprefs.ModelsSmallSituation` (currently `small`, while rows remain `first`) and `modelregistry.ModelsReviewHighestNative` (currently true). Ticket estimates and fix rounds select situations; CLI resolution without a ticket uses Default, situation `first`, and the key’s canonical person. Ticket resolution uses the ticket’s placement fields.

S1 provides the resolver and in-memory end/per-step effort controls. S2/S3 own persistence migration, new situation/order writes, cause-code fields and the public v2 contract. The existing resolution description and trace situation enum receive minimal contract updates. No migration or endpoint is added here.
