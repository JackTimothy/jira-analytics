# Delivery Analytics

A slimmed-down, opinionated view of how work actually moved through a sprint.

Each project draws from one issue-tracker project and one or more code
repositories. The app is **opinionated about process** — it assumes a specific
way of working — and **generic about data sources**: nothing about any one
company's tracker, repositories or people appears in this repository.

## The first feature: sprint retrospective timeline

For a chosen sprint, every sub-task gets a row showing the states it occupied
over time, grouped by parent work item, with a toggle between all sub-tasks in
the sprint and only those in the committed scope.

### The seven states

| State | Meaning |
| --- | --- |
| **To Do** | Not started: no branch, and the tracker status implies nothing underway |
| **In Progress** | A branch exists, but review has not meaningfully begun — no open pull request, or it is a draft, or nobody has been asked to look |
| **Review Requested** | An open, non-draft pull request with human reviewers outstanding |
| **Feedback Given** | At least one human reviewer has commented or requested changes |
| **Approved** | Every human reviewer has approved |
| **Blocked** | The tracker status says Blocked |
| **Done** | The tracker status is terminal, or a linked pull request merged |

Precedence is explicit and ordered: **Done → Blocked → Approved → Feedback Given
→ Review Requested → In Progress → To Do**. First match wins.

Two orderings are load-bearing rather than stylistic:

- **Blocked outranks the code-host states**, so work someone flagged as blocked
  stays visible even while its pull request sits open. A retrospective is about
  finding where time went.
- **The review outcomes are tested before In Progress.** A code host removes a
  reviewer from the "requested" list the moment they submit, so a
  "nobody is requested" test placed earlier would misread every fully reviewed
  pull request as still in progress.

Reviewers that are bots or configured AI accounts are excluded throughout, so an
automated approval never stands in for a person's.

### Committed scope

A parent is in the sprint's committed scope when it **has a due date on or
before the sprint's end**. One comparison covers every way work is legitimately
committed:

| Case | Parent due date | In scope |
| --- | --- | --- |
| Committed for this sprint | equals the sprint end | yes |
| Carried over, due date preserved from an earlier sprint | before the sprint end | yes |
| Pulled forward for an urgent business need | before the sprint end | yes |
| Pulled in opportunistically, never committed | none | no |
| Belongs to a later sprint | after the sprint end | no |

The absent due date is what separates the fourth row from the rest, so the view
depends on the convention that work pulled in outside the original commitment
does not get a due date.

Due dates are calendar days and sprints end at an instant, so the comparison
resolves the sprint end to the day it falls on **in the project's timezone**.
Without that, every sprint ending near midnight UTC is misclassified.

## Running it

### Prerequisites

- Go 1.24+
- Node 18, 20 or 22
- A Jira API token and a GitHub token with read access to your repositories

### Configure

```sh
cp projects.example.yaml projects.yaml   # projects.yaml is gitignored
$EDITOR projects.yaml                    # fill in your tracker project and repos
```

Credentials come from the environment, never from `projects.yaml`. Either
export them, or put them in a `.env` file, which is read on startup:

```sh
cp .env.example .env    # .env is gitignored
$EDITOR .env
```

Exported variables win over the file, so a value can be overridden for a single
run without editing anything:

```sh
JIRA_API_TOKEN=other-token go run ./cmd/server
```

Point `ENV_FILE` elsewhere to read a different file. Optional settings: `PORT`
(default 8080), `PROJECTS_FILE` (default `projects.yaml`), `WEB_DIR` (default
`web/dist`), `GITHUB_BASE_URL` (for GitHub Enterprise).

### Run

```sh
(cd web && npm install && npm run build)   # or `npm run dev` for a live client
go run ./cmd/server
```

Then open <http://localhost:8080>. In development, `npm run dev` serves the
client on Vite's port and proxies the API to `localhost:8080`.

## API

```
GET   /api/v1/projects
GET   /api/v1/projects/{projectID}
PATCH /api/v1/projects/{projectID}/settings          {"timezone": "America/New_York"}
GET   /api/v1/projects/{projectID}/sprints
GET   /api/v1/projects/{projectID}/sprints/{sprintID}/retrospective?scope=all|committed
```

An unrecognised timezone is rejected with 422 rather than quietly defaulted: the
wrong zone silently changes which work counts as committed.

## Architecture

Clean Architecture with a strict inward dependency rule.

```
cmd/server              composition root — the only place that names a concrete adapter
internal/domain         entities and pure logic; no third-party imports at all
internal/usecase        interactors, and the ports they depend on
internal/adapter/…      jira, github, configstore, httpapi
internal/infra/…        transport and configuration plumbing
web/                    React client
```

Ports are declared in `internal/usecase` by the code that consumes them, not
beside the adapters that implement them. That is what keeps the arrows pointing
inward — an adapter imports the use case package, never the reverse — so
replacing Jira with another tracker means writing one adapter and changing one
line of wiring.

### Reconstructing history

The state definitions are predicates over a *snapshot*, but a retrospective
needs them over *time*. So the app never reads current state: it gathers
timestamped events from both sources and replays them.

```
tracker changelog ⟶ ┐
                    ├─ events ⟶ fold ⟶ Facts at T ⟶ Derive ⟶ State at T ⟶ intervals
code host activity ⟶ ┘
```

`Derive` is one pure, total function used for every instant. It and the fold
carry the whole feature, have no I/O, and are where the tests concentrate.

### Colour

The seven states are **not** seven independent identities. Five are an ordered
progression and are encoded as a single-hue ordinal ramp; the two that mean
"stop and look" use reserved status colours. That is what makes the chart
colourblind-safe — seven independent hues cannot clear the separation floors at
once, and the obvious semantic choice, green for done against red for blocked,
is the single worst pair under deuteranopia.

A table view exists alongside the chart, and every state is labelled in the
legend, so identity never rests on colour alone.

### Which sprints appear

Sprints are read from the project's own issues, not from a board. A board's
sprint list belongs to the board: one that is shared, or simply old, carries
sprints from other teams with nothing in the response to distinguish them.
Sprint membership recorded on an issue is unambiguous, so scoping by it is
correct by construction and needs no board configured.

The scan costs one request per hundred issues and is cached for a few minutes,
since the set of sprints changes at most once a sprint.

Every issue query is scoped to the project for the same reason — a sprint on a
shared board holds other teams' issues, and a retrospective that mixed them in
would be worse than useless.

### Finding the code

Work is linked to an issue by the issue key, from two directions.

**Pull requests come first**, matched on head branch and then on title. This is
what finds finished work: merging deletes the head branch by default, so
anything branch-led would go blind on exactly the sub-tasks that completed. The
title survives — a squash merge leaves `Title (#123)` behind — which is why it
is the fallback. A code host cannot filter pull requests by issue key
server-side, so the scan is bounded by the sprint window plus a lookback, since
a pull request opened well before the sprint and left untouched still describes
the state the sprint opened in.

**Then branches that no pull request covers**, found with one prefix query per
project key — work in progress, where a branch exists and review has not
started. Two properties of the server-side ref match are handled explicitly,
both verified against the live API: it is **case sensitive**, so the lowercase
prefix is queried too, and it matches **raw characters, not whole tokens**, so a
query for `PROJ-1` also returns `PROJ-10-…`. Every candidate goes back through
the key parser, which reads whole keys and rejects those.

A sub-task with neither a pull request nor a branch shows up as a
`no linked branch or pull request found` warning rather than a silently empty
row.

### Known approximation

Code hosts record no branch-creation timestamp, so the earliest commit stands in
for it — taken from the pull request where there is one, since that keeps
working after the branch is deleted, and from comparing refs otherwise. That is
the one value in the whole timeline that is inferred rather than observed, and
it is confined to the code-host adapter.

## Tests

```sh
go test ./...
(cd web && npm run build)   # tsc runs as part of the build
```

`internal/integration` wires the real adapters, interactors and handlers against
fake tracker and code-host servers. The unit tests prove each layer in
isolation; those prove the layers fit.
