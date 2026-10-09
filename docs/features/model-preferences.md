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

## Display language (AEON-998)

`web/src/lib/displayLanguage.ts` owns the display-language policy. The shell and
app pages stay in English until their German translation is complete, regardless
of the profile's regional locale, browser language, document language or `lang`
query. Date/number formatting and authored document languages keep their own
preferences. Release history retains AEON-323: explicit `release_lang`, then the
remembered choice, then the profile default; changing it does not translate the
page underneath.

Migration 1295 adds nullable `work_kinds.words_de` and tenant-scoped built-in
`work_display_words` for kinds and situations. It preserves existing English
names, sentences, examples and identities; new tenants receive both languages.
The work-kinds API keeps its legacy English fields. `?lang=en|de` selects the
additive `display_words` field; `words_de` remains available independently.
Untranslated custom kinds fall back to a whole English record. POST/PATCH accept
bounded `words_de` values, while older English-only writes retain German words.
The English editor, save and Undo preserve both. Situation limits GET supplies
both languages and selected display words for its six built-in situations.

Inventory reviewed: KindsOfWorkSection, ModelsSection, SessionChat, SessionTabs,
SessionMessages, SessionPanel, ChatCodeBlock, BoostToday, CrossFamilyReviewCard,
TicketDelivery, TicketTable, NeedsAttentionView, SettingsView, DeliveryView,
AccountsSection, App (update toast), BrandCard, TicketTypeIcon, StatusHelpSheet,
RecurringPill; lib/access, lib/attachCopy, lib/delivery, lib/recurrenceMarker,
lib/releases and lib/tenantBrand. ModelsSection is already English;
ModelBoardRoute no longer exists in this checkout. Components that use delivery,
recurrence and brand wrappers inherit the shared policy through those helpers.
Additional switches consume that decided language: AttentionBulkPreview,
AttentionGroupMenu, DisplayPanel and ListToolbar inherit NeedsAttentionView;
DeliveryRow, DeliveryTrack and FlowView inherit deliveryLanguage. The delivery
copy/formatting modules deliveryFlowModes, deliveryFlowWords, deliveryFlowText,
deliveryFlowData, deliveryNumbers, deliveryNumbersText, deliverySimple and
deliverySimpleText consume that same language. ReleaseDetail and ReleasesSheet
consume the independent releaseLang choice.
PublicReleasesView and PublicRoadmapView have explicit content-language choices.
PublicQuoteView, QuoteDocument, InspectorDocument, lib/quotes/profile and
lib/quotes/publicCopy use authored document languages. These content choices
remain independent; RulesBudgetSection has an explicit tip-content selector.

The static source guard checks future app locale detectors, with a regression
case that introduces a new component switch. Component tests cover German and
English profiles and bilingual save/Undo. Database tests cover migration,
existing edits, new tenant seeds and isolation. API tests cover language
selection, legacy clients, custom fallback and invalid translation bounds.
Playwright spot checks cover Kinds of work, Models, session chat, Tickets and
Needs attention with an actually loaded German profile and stable controls;
evidence uses `testInfo.outputPath()` under `web/test-results/aeon-998-onelang/`.
No layout was added; the existing components are retained (needs Opus design).

AEON-998 validation: all 40 static checks passed with no skips (`--merge-main`,
exit 0). Remote modelregistry, modelprefs, db, dsar and reportercontract tests
passed; the database check includes migration 1295 and tenant isolation.
Targeted helper/guard and DE/EN editor tests passed, as did the five browser
scenarios at 1440, 1024 and 390 pixels in light and dark themes (30 screenshots).
The screenshot paths are indexed in
`web/test-results/aeon-998-onelang/evidence.json`.
