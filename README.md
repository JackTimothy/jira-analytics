# Delivery Analytics

A slimmed-down, opinionated view of how work actually moved through a sprint.

Each project draws from one issue-tracker project and one or more code
repositories. The app is **opinionated about process** — it assumes a specific
way of working — and **generic about data sources**: nothing about any one
company's tracker, repositories or people appears in this repository.

## The first feature: sprint retrospective timeline

For a chosen sprint, every piece of work gets a row showing the states it
occupied over time, grouped by work item, with a toggle between all the work in
the sprint and only what was in the committed scope.

What a row is depends on how a team works, and both shapes can appear in one
chart:

- **A sub-task**, where the team breaks work down. The work item above is a
  header.
- **The work item itself**, where nobody broke it down. The heading carries the
  bar; there is nothing to nest under it.
- **One branch**, where a work item ended up with several. One bar cannot hold
  two review states at once, so each branch gets a row and a warning names the
  item — the split makes the chart honest, but the branches are still a
  discipline problem.

A work item with no rows at all is left out: that now means only one whose rows
all fell outside the sprint window.

Issue types listed in a project's `typesLast` sort to the bottom — a support
queue, chores, whatever a reader scrolls past to reach the work the sprint was
about. Which types those are is a fact about one team's process, so it is
configuration rather than a rule in the code.

### The burndown

The same sprint measured in points rather than in states. It is built from the
parent work items, because estimates live there — a burndown assembled from
sub-tasks would be assembled from nothing — and it follows the same scope
toggle, so switching between all work and the committed scope moves both charts.

Points come off when a work item's own status reaches a done category. Finished
is modelled as a state over time rather than as one timestamp, so an item that
is reopened puts its points back on the chart and takes them off again when it
finishes for good. Work carried over already finished when the sprint opened
counts toward the total but never toward what was left to do.

The ideal line descends only during working hours, flat across nights and
weekends, from the same schedule the timeline's axis uses. A calendar-time ideal
shows a team falling behind every Saturday and catching up every Monday, which
says nothing about them. On the compressed axis it is a straight line, which is
the same fact seen from the other side.

Items with no estimate are named under the chart rather than silently counting
as zero: a burndown quietly missing a third of a sprint is worse than none.

The scope line is flat at the sprint's final total. Work added mid-sprint is
therefore counted from the start, which a stepped scope line read from the
sprint field's own history would fix.

### The seven states

| State | Meaning |
| --- | --- |
| **To Do** | Not started: no branch, and the tracker status implies nothing underway |
| **In Progress** | A branch exists, but review has not meaningfully begun — no open pull request, or it is a draft, or nobody has been asked to look |
| **Review Requested** | An open, non-draft pull request with human reviewers outstanding |
| **Feedback Given** | At least one human reviewer has commented or requested changes |
| **Approved** | Every human reviewer has approved |
| **Blocked** | The tracker status says Blocked |
| **Done** | The tracker status is terminal, or something merged and no pull request is still open |

Precedence is explicit and ordered: **Done → Blocked → Approved → Feedback Given
→ Review Requested → In Progress → To Do**. First match wins.

Done means merged *and nothing still outstanding*, not merely merged. A work
item spread across branches would otherwise end its bar at the first merge while
the rest were still in review, and a sub-task whose follow-up fix is open would
read as finished while somebody waits on a reviewer. A pull request closed
without merging is abandoned rather than outstanding, so giving up on a
superseded attempt never keeps finished work looking unfinished.

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
make run          # build the client if it is stale, then serve on :8080
make dev          # the same, with the client on Vite and hot reload
make test         # go test -race, go vet, gofmt, and the client typecheck
make help         # everything else
```

Then open <http://localhost:8080>.

`run` depends on the built client, and the built client depends on its sources.
That is the point of the Makefile rather than a convenience: the server serves
`web/dist`, which is gitignored, so pulling a change updates the source and
leaves the bundle the browser actually receives untouched — which looks exactly
like a change that did not work.

Everything is still one command away without `make`, if you would rather:

```sh
(cd web && npm install && npm run build)
go run ./cmd/server
```

## API

```
GET   /api/v1/projects
GET   /api/v1/projects/{projectID}
PATCH /api/v1/projects/{projectID}/settings
      {"timezone": "America/New_York",
       "workingHours": {"days": ["monday","tuesday"], "start": "08:00", "end": "18:00"}}
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

Each of the seven states gets its own hue, and the palette is chosen against a
validated separation floor rather than by eye. Every pair — not only
neighbouring ones, since any two states can end up side by side on a timeline —
clears a normal-vision difference of 15 (OKLab ×100). Worst pair 15.9 in light,
16.3 in dark.

The first version was a single-hue ordinal ramp: five steps of blue for the
progression from not-started to merged. It encoded the ordering faithfully and
failed at the only job the chart has. Five blues a few steps apart are not
tellable apart in a bar a few pixels wide, and the reader already has the
ordering — from the legend, and from the shape of the work.

Two slots are reserved status colours and keep their meaning across the app:
amber for feedback outstanding, red for blocked. To Do is the one deliberate
neutral — nothing has happened yet, and being the only desaturated slot is
itself a distinction. It is a mid grey rather than a pale one because a sub-task
sitting in To Do for a week is a finding, not background.

Three checks are knowingly unmet, and `web/src/theme.css` records why. The
important one: **the palette is not colourblind-safe.** Blue/violet and
green/red are the two worst pairs under deuteranopia and both are in play.
Identity is never carried by colour alone — the legend, the tooltips and the
table view all name the state — but a colourblind-safe palette means giving up
hue separation somewhere, and that is a deliberate revisit rather than an
oversight.

### Which sprints appear

Sprints are read from the project's own issues, not from a board. A board's
sprint list belongs to the board: one that is shared, or simply old, carries
sprints from other teams with nothing in the response to distinguish them.
Sprint membership recorded on an issue is unambiguous, so scoping by it is
correct by construction and needs no board configured.

The scan costs one request per hundred issues and is cached for a few minutes,
since the set of sprints changes at most once a sprint.

Opening a retrospective does not pay for it. Naming one sprint's dates is a
different question from enumerating a project's sprints, and it is answered
directly by id in a single request. Fetching a sprint *by id* carries none of
the ambiguity above — there is no "whose sprint is this" to get wrong when the
id is already in hand.

Every issue query is scoped to the project for the same reason — a sprint on a
shared board holds other teams' issues, and a retrospective that mixed them in
would be worse than useless.

### Speed, and what it depends on

Assembling a retrospective is almost entirely round trips; the replay itself is
microseconds. So the adapters overlap everything that can overlap — the tracker
and the code host, the repositories, the pages of a listing — under a
concurrency bound, because both hosts punish bursts with backoff that costs more
than the parallelism saves. Where overlapping is not enough, the work is batched
instead: a sprint's status history is one search rather than a request per
sub-task, and pull request detail is one GraphQL query per ten rather than three
requests each.

The pull request *listing* stays on REST deliberately. GraphQL pages by cursor,
so each page needs the previous page's `endCursor` — twenty sequential round
trips on a busy repository, where REST takes a page number and can fetch four at
once. GraphQL is used only where it wins: fetching a lot of detail about a known
set of objects. The branch ref listing stays on REST for a different reason —
GraphQL's `refs(query:)` is a fuzzy search rather than the prefix match
`matching-refs` performs, and a silent miss there means a sub-task charted as
never started.

Two things depend on what a particular deployment supports, and each has a
working but expensive fallback:

| Fast path | Fallback | What the fallback costs |
| --- | --- | --- |
| Sprint fetched by id from the Agile API | Scan the project's issues | One request per hundred issues, per retrospective |
| Changelogs attached to the issue search | One request per sub-task | Fifty to seventy requests, per retrospective |
| Pull request detail batched over GraphQL | Three REST requests per pull request | About a hundred requests, per retrospective |
| Story points read from the discovered estimate field | No burndown | The points view is absent, and the log says so |

Every fallback is announced in the log the first time it is taken. A silent
fallback is the worse failure: every build would quietly pay for it and nothing
would say why.

The GraphQL one has two granularities, because GraphQL answers partially by
design. A credential that may not use the API at all is a standing fact, so it
is recorded once and the batch is not attempted again. A single pull request
denied by path, or one whose reviews and timeline exceed what a query carries,
is refetched over REST on its own and the rest of the batch still comes from one
request. Not all tokens can read over GraphQL what they can read over REST —
fine-grained personal access tokens in particular — which is why this is
discovered at runtime rather than configured.

`cmd/probe` answers all of it against the real services before you wonder:

```sh
go run ./cmd/probe -sprint 7354 -repo your-org/your-repo
```

Each build also logs what it cost, which is the number to compare against:

```
retrospective built project=otco sprint=7354 sprint=0.3s parents=0.4s
  subtasks=0.5s history=1.1s code=2.9s total=3.4s requests=41
```

Phases overlap, so they will not sum to the total — that is the overlap working.

The code host reports its own stages the same way, one line per repository,
because "the code host took eight seconds" is not a finding and which of its
four stages took them is:

```
code activity read repo=acme/service pull_listing=2.5s branch_listing=0.7s
  pull_detail=3.6s orphan_branches=0.7s total=6.8s pages=20 pulls=43
  rest_refetches=7 branches=5
```

`pages` and `rest_refetches` are there because they explain the two slowest
stages: how much listing a repository needs, and how many pull requests the
batch could not answer for. The stages overlap in pairs — the two listings
together, then the detail and the orphan branches together — so they do not sum
to the total either.

Two rounds of optimisation went into the wrong stage for want of exactly this
line: removing a hundred requests from `pull_detail` changed the total by
nothing, because `pull_listing` was the whole cost and always had been.

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
go test -race ./...
(cd web && npm run build)   # tsc runs as part of the build
```

`-race` is not optional here. Both adapters fan out, and the concurrency
assertions were each checked against a deliberately serialised build to confirm
they fail there — a test that passes either way is worse than no test.

`internal/integration` wires the real adapters, interactors and handlers against
fake tracker and code-host servers. The unit tests prove each layer in
isolation; those prove the layers fit.
