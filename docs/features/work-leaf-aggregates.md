# Work leaf aggregates (AEON-651)

Default and live work lists include migrated `work` nodes. Parents show the sum
of eligible leaf estimates separately from their editable planned estimate;
cancelled and archived leaves retain their own displayed estimate and sort by it,
but contribute nothing to ancestor totals. Descendant node and session events
invalidate loaded parent projections in the affected project even when parent
status and revision stay unchanged. Membership changes also refresh former leaves.
When an uncached descendant moves, its source project is unknown; held work rows
across projects are conservatively refreshed through authorized reads. Reads still
respect pinned editors and reject responses overtaken by newer hints.
Late usage reports also invalidate the session's currently bound readable work
node and held parents, including after a session stops. Planning sorts resolve
displayed work rows as well as eligible descendant leaves, preserving a closed
leaf's route, token and cost projections across sort changes.
Current `work` nodes keep route provenance, human checks, completion benefits,
project moves, graph links and person-only roadmap publication. Future frozen
release notes capture work members using the existing public-field whitelist.
Tests use the current starter catalog; historic mixed-kind cases define their own
tenant kinds.
Lists without sized work skip calibration reads that cannot produce an estimate,
while still resolving displayed routes and retaining historical usage.
Progress tooltips and accessibility labels share ETA's completion rule: weighted
or rounded 100% still says “work remains” while leaves are open or their completion
is unknown. The displayed percentage and estimate coverage remain visible.

The unreleased 1230 migration bounds every shared scope traversal to 4096 input
roots and 50000 distinct root/node pairs, counting overlapping roots against the
expansion budget before leaf facts or sessions are read. Cycles are deduplicated
and depth is not silently truncated. Aggregate readers and node detail reads use
five-second context deadlines, preserving shorter caller deadlines. Exceeding a
budget or deadline returns an explicit failure (node APIs: 503), never a partial
aggregate. Larger lists should narrow their scope. RLS, exact-money permissions,
historical spend and session identities are unchanged.

Work vocabulary (AEON-655) is managed in Workspace settings, independently of appearance. A leaf always uses the leaf name (default Ticket); parents use their project-relative level (Epic, Story, then Level N). Only live work children make a parent; a project resets depth. Naming and the parent status explanation use the existing `work-parent-status` flag, whose production activation remains owned by AEON-429. Create a work item; nesting decides its name. Lead status scripts must write leaves only.

| Surface | Before migration | After work-kind migration |
| --- | --- | --- |
| REST `kind_id` | Configured Epic/Ticket/Task UUID | Canonical Work UUID; retired UUIDs are rejected, never guessed |
| REST list `kind=epic,ticket,task` | Requested old kinds | Each old slug also matches Work (including exclusions); use `shape=parent` / `shape=leaf` and `depth=N` for structure |
| REST `epic` projection/filter | Nearest Epic/subtree | Nearest Work ancestor/subtree; property name retained for compatibility |
| Outcomes and cross-family reviews | Ticket outcomes; Ticket/Task reviews | Work retains existing history, outcome recording and review requests; routes and payload field names stay compatible |
| REST node/list/tree/Graph | Existing fields retained | Additive `is_leaf`, `depth`, `level_name`, `level_icon`; non-work nodes omit shape fields |
| CLI `issue --type work,epic,ticket,task` | Old types remain accepted | Old type names resolve to canonical Work for creation/listing; same-kind updates are no-ops |
| CLI JSON | Original `type` | Canonical `type=work`, plus `is_leaf`, `depth`, `level_name`, `level_icon` |
| MCP `issue_list`, `issue_get`, `issue_create` | Same configured-kind paths as CLI | Same aliases and JSON as CLI; `parent` on creation decides nesting |
| Saved views | Existing kind filters retained | Old kind slugs match Work; Parents/Leaves and depth round-trip through URL and saved filters |

Vocabulary writes require a person with `settings.manage`, check the current permission under the tenant fence, compare the supplied revision, and append an event. At most 32 parent levels and 60 characters per name are accepted. Empty values keep stable names and icons. Historical sessions, event snapshots and Decision Desk identities are unchanged. Integration seam: AEON-429 must provide its feature storage/service; AEON-652 owns server-side lifecycle enforcement. This package does not activate flags or change external lead scripts.

AEON-655 Outline roots load incrementally with one unified cursor. Page sizes remain fixed even when inserted or expanded work consumes retention capacity. The browser retains at most 5000 lazy work items and reports an incomplete Outline at that limit; filtered paths resolve at most 2000 ancestor reads in batches of eight, with cycle detection and explicit incomplete-result messages for unreadable ancestors, resource limits or interrupted reads. Drag nested work onto the Project root destination above the rows to return it to the project level. Reload and Save vocabulary are serialized, and person/tenant changes discard old responses. Detail-panel completion uses canonical descendant-leaf state facets, excluding cancelled leaves; failed aggregate reads show unavailable progress instead of direct-child counts. CLI/MCP type aliases resolve kinds only and never inject provenance into strict user field schemas.

AEON-655 completion validation covers migrated `work` and legacy `ticket` records. Creating or moving a work item into Done, Accepted or Delivered requires both pill and benefit texts in English and German, including when hidden from release notes. Already completed history remains editable; reopening restores the next-completion requirement. The workspace offers the same benefit reading section, editor and completion prompt for work items. Outline rendering traverses explicit frames rather than the JavaScript call stack; a 3600-row path (1800 ancestors plus 1800 matches) has regression coverage.

Release image publication runs two gates before attestation and release index
publication. First, the pushed digest is walked to its platform manifest, whose
image config digest must equal the smoked build's config digest exactly; that
config includes `created` and history, so both exports share `SOURCE_DATE_EPOCH`
from `git show -s --format=%ct HEAD` and use `rewrite-timestamp`. Second, the
runtime comparison reads architecture, OS, variant, ordered rootfs diff IDs and
runtime configuration from `docker image inspect` for the pushed image and the
image that passed smoke. Only this runtime comparison excludes image IDs,
`created`, history and provenance index digests; a build that differs only in
its clock passes the runtime comparison but fails the config digest gate.
The PDF visual diff gate returns
exit 2 for any missing page, including blank pages, independently of pixel tolerance.
