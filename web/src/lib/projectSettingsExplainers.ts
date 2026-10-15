// SPDX-License-Identifier: AGPL-3.0-only
// Approved AEON-1227 map; its computer example is filled from tenant data
// to satisfy the public-source name guard. Every other text is retained.
// AEON-1227 · “How this works”: every explainer in one place (id → { title, what, effect, who, where? }).
// The build reuses this map as is. Plain words, technical, no metaphors; each one names a real example from the project.
// {K} is the project key, {N} a ticket number of that project, {R} its repository, {O} its GitHub owner.
// where: a real place to read the record ({ label, go }); null when no page shows it yet (an open point, never invented).
const EXPL = {
  definitions: { title: 'Definitions',
    what: 'A definition lists the checks a ticket must pass before it enters a status: Ready before In progress, QA before QA, Done before Done. Other statuses have no checks.',
    effect: 'A {K} ticket can enter In progress once its checks pass, for example an estimate and written acceptance criteria. Its next-status line shows how far it is, such as “Ready 1/2”.',
    who: 'Admins set the workspace default under Policies in Settings. A project admin can give a project its own definition.' },
  enforce: { title: 'Agents and people',
    what: 'Agents and people meet the same checks. An agent is stopped when a check fails; a person can go ahead with a reason.',
    effect: 'If a {K} ticket requires an estimate and none is set, PAIMOS refuses an agent’s start and names the missing check in the ticket’s activity. A person can choose “Move anyway”, write why, and the move is logged with the missing checks.',
    who: 'People who may change a ticket’s status can move it anyway.' },
  copy: { title: 'Workspace default or this project',
    what: 'A project follows the workspace default until someone gives it its own definition. Then it keeps a full copy; nothing is merged.',
    effect: 'While {K} uses the default, a workspace change applies at once. After “Change for this project”, {K} edits its copy and this page shows how it differs. Later workspace changes leave the copy intact. Reset drops the copy; tickets keep their status.',
    who: 'Project admins change their project’s copy. Admins change the workspace default.' },
  manual: { title: 'Items a person ticks',
    what: 'A free-text item in a definition, such as “Release notes reviewed”. PAIMOS cannot check it; a person confirms it on each ticket.',
    effect: 'A {K} ticket waits for its owner to confirm the item. The ticket’s person assignee is its owner, otherwise a current project admin. An agent can read the item but cannot tick it.',
    who: 'Project admins add items. Only the item’s current owner or a project admin may tick it.' },
  shadow: { title: 'Shadow mode',
    what: 'Shadow runs one of the coordinator’s machines next to the lead, without any power. The machine makes its decision and PAIMOS stores it as a record; nothing is started, merged or shipped.',
    effect: 'With the delivery queue in Shadow, each finished round gets a record such as “{K}-{N}: second in line, waits for CI”. The lead still decides. People compare these records with what the lead really did, before any machine may act.',
    who: 'The lead or an admin, while a machine is tested. Turn one off to stop its records.',
    where: { label: 'Queue records: each ticket’s panel › Delivery. The other machines’ records: no page yet.', go: 'ticket' } },
  queue: { title: 'Delivery queue',
    what: 'The queue orders finished work rounds (built and reviewed pull requests) for merging and release. In this release it runs only in Shadow.',
    effect: 'It records an order, e.g. “{K}-{N} waits behind {K}-{N2}”. Freeze: it parks every round that isn’t in the release set. Release set: the branches allowed through while frozen. Held: branches or pull requests it skips until you release them.',
    who: 'The lead, around a release. You’d freeze it to stop everything before a risky release.',
    where: { label: 'Each ticket’s panel › Delivery shows its queue round.', go: 'ticket' } },
  reviews: { title: 'Reviews',
    what: 'Plans the review of each finished round: which model family reviews the pull request, following this project’s cross-family review rule.',
    effect: 'In Shadow it records its pick, e.g. “{K}-{N}: review by an OpenAI model, because a Claude model wrote it”, and the fix rounds it would ask for after a finding (at most two). It never approves anything and never starts a reviewer.',
    who: 'The lead, while this machine is tested against the reviews that really ran.',
    where: { label: 'No page shows these records yet.', go: null } },
  shipping: { title: 'Shipping',
    what: 'Plans merges and releases for rounds that passed review and CI.',
    effect: 'In Shadow it records what it would merge and when, e.g. “merge {K}-{N} once CI is green, include it in the next release”. It never merges, tags or deploys anything.',
    who: 'The lead, while shipping is compared with the merges people and the lead really made.',
    where: { label: 'No page shows these records yet.', go: null } },
  routing: { title: 'Routing',
    what: 'Chooses the harness, model and account for each round, from Settings › Models, the work context and each account’s quota.',
    effect: 'In Shadow it records its choice, e.g. “{K}-{N}: Codex on ops account”, next to what the lead really used. Consult (the lead asks it first) and act (it decides) come in a later release.',
    who: 'The lead, while routing is compared with its own choices.',
    where: { label: 'No page shows these records yet.', go: null } },
  admission: { title: 'Admission',
    what: 'Admission is the check before any agent starts: the daily limit, your dial, the computer’s load and the account’s quota.',
    effect: 'With this on, the engine evaluates each start in {K} and records “allow” or “wait” with a reason, e.g. “{K}-{N}: wait, daily limit reached”, plus whether the lead’s own check agreed. It never blocks or starts anything.',
    who: 'Only a person may switch it, usually an admin testing the engine.',
    where: { label: 'No page shows these records yet.', go: null } },
  chat: { title: 'Chat roles',
    what: 'A chat role connects a person with the project’s lead or one worker slot, so messages in project chat reach the right agent session.',
    effect: 'When chat-v1 is on, you bind your own role, e.g. “Markus with the {K} lead”. Nobody binds a role for someone else, and binding starts no session.',
    who: 'Each person for themselves, once an admin turns chat-v1 on in Settings › Workspace › Features.' },
  mbf: { title: 'Mark merged pull requests delivered',
    what: 'A repair action for merges PAIMOS missed, for example while the GitHub App was disconnected.',
    effect: 'You paste up to 100 pull request numbers. PAIMOS asks GitHub which were merged into main and previews the tickets it would move, e.g. “{K}-{N} → Delivered”. Nothing changes until you apply that preview. Tickets a gate refuses, such as open review findings, stay as they are.',
    who: 'Someone who manages delivery, after an outage. Each move appears in the ticket’s history.' },
  backfill: { title: 'Reading history',
    what: 'When a repository is linked, PAIMOS reads its past once through the GitHub API: CI runs, then pull requests, then the job results of each run.',
    effect: 'For {K} it reads the last 30 days in small steps on the server, so you can leave the page. Until a day is read, Delivery marks its windows “partial”, never 0. Changing the repository or a workflow reads again from the start.',
    who: 'It starts by itself when someone links the repository. “Read history again” is for after GitHub was unreadable for a while.' },
  events: { title: 'GitHub App events and history',
    what: 'PAIMOS learns about {R} in two ways. Events: GitHub sends the App a webhook the moment a pull request opens, a CI run finishes or something merges. History: PAIMOS asks the GitHub Actions API for runs from before the link.',
    effect: 'Events keep Delivery live from now on; history fills the days before. If events stop, newer windows show “partial”, never 0.',
    who: 'The App belongs to the workspace setup. An owner of {O} on GitHub installs it on the repository.' },
  actions: { title: 'The Actions read permission',
    what: 'A GitHub App sees only what its permissions allow. Reading CI runs and their jobs needs “Actions: Read”.',
    effect: 'Without it, every history read fails and nothing is stored, so CI numbers on Delivery say “no data yet”, never 0. Once an owner of {O} accepts the permission, the server reads the last 30 days of {K} by itself, or now with Check again. Nothing is lost.',
    who: 'An owner of the GitHub organisation, once, when the App asks for the permission.' },
  checks: { title: 'Required workflow and required checks',
    what: 'The required workflow is the CI workflow whose result decides a merge (here CI). Required checks are the jobs inside it that must pass, such as go test.',
    effect: 'With required checks set, a run that is red only in an optional job still counts as green for {K}-{N}. Delivery counts “green on first try” the same way. GitHub’s own branch protection rules don’t change.',
    who: 'Whoever manages delivery, when the CI setup changes.' },
  deadlines: { title: 'Deadlines',
    what: 'Each deadline is the longest a change may stay in one step before PAIMOS raises an alert. 0 turns that alert off.',
    effect: 'Reviewed, not pushed: the reviewed branch isn’t pushed yet. Pushed, CI not green: CI still runs or is red. CI green, not queued: the pull request isn’t in the merge queue. In the merge queue: it hasn’t merged. Failed in the queue: nobody fixed it. Example: {K}-{N} reviewed at 14:00 and not pushed by 14:30 raises an alert.',
    who: 'Whoever manages delivery. Tighten them when the pipeline is fast.' },
  inherit: { title: 'Workspace default or this project',
    what: 'Some settings have one value for the whole workspace. A project follows it until someone sets its own value here.',
    effect: 'While {K} uses the workspace default, a change on the workspace page applies to {K} at once. After “Change for this project”, {K} keeps its own value and the workspace page lists it under “projects differ”. Reset returns it to the default.',
    who: 'Admins set the workspace default; whoever may change the setting sets the project’s own value.' },
  key: { title: 'Project key',
    what: 'The key is the short name in front of every ticket number, e.g. {K}-{N}. It appears in links, search, branch names, commit messages and the CLI.',
    effect: 'It is set when the project is created. Changing it later, with the old key still working everywhere, comes in a later release.',
    who: 'Whoever creates the project chooses it.' },
  changekey: { title: 'Changing the key',
    what: 'Changing the key renames every ticket number of this project at once, e.g. PHAROS-91 becomes FLEET-91. The number stays.',
    effect: 'PAIMOS keeps the old key as an alias: links, search, the CLI, and branch names or commit messages that say PHAROS-91 still find FLEET-91. The old key stays reserved for this project, so you can switch back. Running agents finish under the old key.',
    who: 'Project admins only, and rarely: after a rename, or when a classic key like PRJ-26 should become a real name.' },
  lifecycle: { title: 'Freeze, Archive, Delete',
    what: 'Three ways to stop a project, from lightest to final.',
    effect: 'Freeze: agents, routines and the delivery queue start nothing new; people still read and edit tickets, and it stays in lists. Archive: the same, and it moves into the Archived group, out of everyday lists and search. Delete: the project and its tickets disappear for everyone; the audit log keeps the record and the key stays reserved.',
    who: 'Project admins. Freeze and Archive undo with one click; Delete asks you to type the key.' },
  lead: { title: 'The lead’s policy',
    what: 'The rules this project’s lead follows when it starts workers: where it runs, which accounts it may use, what kind of work it does and how often it retries.',
    effect: 'Set “Runs on” to {C} and the lead starts only there; its workers still go wherever the checks pass. Lower Recovery to 1 attempt and a failing round of {K}-{N} stops after one retry and asks you on the Decision Desk.',
    who: 'The person who owns the lead, with model-preference rights.' },
  review: { title: 'Cross-family review',
    what: 'Whether a change needs a review by a model of another family (OpenAI, Anthropic, xAI and others) before it counts as reviewed. PAIMOS reads the author’s family from its own run records.',
    effect: 'With “Another model family”, a pull request for {K}-{N} written by a Claude model needs an ok from a non-Anthropic model. “Only these families” limits who may review. Off: any finished ok review counts.',
    who: 'Admins with the review-policy right. A project sets its own when its risk differs.' },
  autopilot: { title: 'Status autopilot',
    what: 'Moves tickets on by themselves when the evidence is there, e.g. to Done when the pull request merged, following the rules in Settings › Autopilot.',
    effect: 'Off for {K}: {K}-{N} stays where it is until a person moves it, even after its merge. On: the workspace rules apply here too.',
    who: 'Admins. A project turns it off while its workflow is unusual.' },
  roles: { title: 'Project roles',
    what: 'Everyone’s workspace role already reaches every project. A project role adds rights in this project only; it never takes anything away.',
    effect: 'Give Mia “Maintainer” in {K} and she may edit its tickets and settings here, while she stays a member everywhere else. Removing the project role returns her to her workspace role.',
    who: 'Project admins.' },
};

export { EXPL as PROJECT_SETTINGS_EXPLAINERS };
export type ProjectSettingsExplainerId = keyof typeof EXPL;

export interface ProjectSettingsExplainerContext {
  projectKey: string
  ticketNumber?: string
  secondTicketNumber?: string
  repository?: string
  computer?: string
}

export function fillProjectSettingsExplainer(value: string, context: ProjectSettingsExplainerContext): string {
  const tickets: Record<string, string> = {
    N: context.ticketNumber ? `${context.projectKey}-${context.ticketNumber}` : `a ${context.projectKey} ticket`,
    N2: context.secondTicketNumber ? `${context.projectKey}-${context.secondTicketNumber}` : `another ${context.projectKey} ticket`,
  }
  const tokens: Record<string, string> = {
    K: context.projectKey,
    R: context.repository ?? 'the linked repository',
    O: context.repository?.split('/')[0] ?? 'the repository’s organisation',
    C: context.computer ?? 'the selected computer',
  }
  return value.replace(/\{K\}-\{(N2?)\}/g, (_, key: string) => tickets[key]!)
    .replace(/\{(K|R|O|C)\}/g, (_, key: string) => tokens[key]!)
}
