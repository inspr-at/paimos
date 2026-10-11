# Models v2 migration

Models owns work choices, fallback orders and model pauses. Migration 1333 carries saved Build ladders into Default when it has no saved chain, Build-hard into its Stuck situation, and explicitly saved review ladders into its Review situation. Automatic review and Quick jobs retain their qualification rules.

Future ladder holds become pauses on the matching harness and model, across native levels. Row constraints preserve the workspace or project, author, timestamp and reason: pin-bottom entries become chain tails and Not-allowed entries become never rules. Saved Wait dial settings become personal rows with no fallback and a Wait ending; other people's choices remain independent.

One `model.settings_migrated` event per existing tenant records the retained source snapshot, each moved item and its resulting settings. Source ladders remain intact for the deprecation release. Registry discovery and catalog upgrades no longer seed or append ladder entries. Successor edits carry order and situation references, native efforts, never rules and pauses.

`GET /api/models/routes` remains a bounded compatibility response derived from Models, with editing disabled. `PUT /api/models/routes` returns 410 Gone. Choices are edited through model preferences; pause editing is supplied by the Models v2 contract package.
