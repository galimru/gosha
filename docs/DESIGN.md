# Gosha — Design

> Status: **agreed plan**, implemented up to M6 (deliver). Implementation order: [ROADMAP.md](ROADMAP.md). Assumptions checked in M0: [SPIKES.md](SPIKES.md).

## 1. Overview

Gosha is a self-hosted service that turns planned work into shipped code. It watches a Linear team for tickets that are ready for implementation. For each one, it uses an AI coding agent to write the change, puts the change through an independent review, gets it past the project's CI, merges it, and reports the result back on the ticket.

### 1.1 Scope

Gosha owns the part of delivery that sits between *planning* and *CI/CD*:

| Stage | Owner | System |
|---|---|---|
| Discovery, research, specifications | Human | Notion |
| Breaking work into tickets, prioritising, releasing to Ready | Human | Linear |
| Implementation, code review, fixing review and CI feedback, merging | **Gosha** | GitHub |
| Build, test, deploy | The project's existing CI/CD | GitHub Actions |
| Acceptance of the delivered result | Human | Linear |

Gosha doesn't replace the project's CI/CD. It produces the changes that the existing pipeline builds, tests and deploys, and it treats that pipeline's results as the quality gate.

### 1.2 Goals
- **Hands-off delivery.** A ticket moved to Ready is delivered (merged, and deployed where configured) with no further human action, unless Gosha needs clarification.
- **Specification-driven.** Agents work from the ticket and from the project's knowledge base in Notion, not from guesswork.
- **Independent verification.** Every change is reviewed by a separate agent and must pass CI before it's merged.
- **Human control at the boundaries.** Humans decide what gets built and accept what was delivered; Gosha never closes a ticket.
- **Multi-project.** One installation serves any number of projects, each configured and tuned from its own repository.

### 1.3 Non-goals
- Replacing CI/CD, deployment tooling or the project's tests.
- Product or technical decision-making beyond what a ticket and its context specify. Ambiguity is escalated, not guessed.
- Writing to Notion or managing the Linear backlog.

### 1.4 Principles

- **Linear is the only place a human gives Gosha instructions** (ticket states + ticket comments). Linear holds only the *outcome*: Blocked and Delivered.
- **Telegram is for alerts** (read-only for the human): a project paused, a ticket Blocked or delivered, a usage limit, a crash, plus a daily digest. Progress is on the PR and in the logs, not repeated in Telegram.
- **Gosha acts as itself, not as the human.** It uses its own GitHub App identity (and a Linear app identity if available), and never a person's credentials.
- **Unattended.** Gosha runs as a background service on any always-on Unix-like host (macOS or Linux): a desktop, a home server or a VPS. Full detail is in the local logs.
- **Host-agnostic.** Nothing in Gosha assumes a particular machine, OS, package manager or service manager.
- **Read-only Notion.** Notion is the human's space. Gosha only mirrors it.
- **Per-project tuning lives in the project repo** (config + `.gosha/instructions.md`).

## 2. Human workflow

| Human action (Linear) | Gosha's response |
|---|---|
| Move ticket to **Ready** | Picks it up, moves it to In Progress, delivers, moves it to In Review |
| Read the result comment on a ticket **In Review**, check the deploy, move it to **Done** | Nothing. Done is human-only. |
| In Review → comment what's wrong → **Ready** | Opens a **follow-up PR** on the same ticket, using the comment as instructions |
| Ticket **Blocked** → comment an answer → **Ready** | Continues on the existing branch/PR with the answer |
| Close the PR on GitHub → **Ready** | Deletes the branch and starts from scratch |
| Move a ticket Gosha is working on to **Backlog / Todo** | Stops the agent, keeps the branch/PR (pause). Ready later = resume. |
| Move any ticket to **Canceled** | Stops the agent, closes the PR, deletes the branch |

**The rule:** every human comment on the ticket since Gosha's last run is passed to the agent as top-priority instructions.

## 3. Ticket lifecycle

```
            human                 Gosha                     Gosha                     human
Backlog ──► Ready ──► In Progress ─────────────────────► In Review ──────────────► Done
              ▲         │  implement → review ⇄ fix                │
              │         │  → checks ⇄ fix → merge → deploy watch   │
              │         ▼                                          │
              ├──── Blocked  (Gosha: needs a human; comment says why)
              └──────────────────────────────────────────────── (human: follow-up)
```

| State | Moved there by | Meaning |
|---|---|---|
| Ready | human | Go / resume / follow-up |
| In Progress | Gosha | Gosha is working (the whole bot cycle happens here) |
| In Review | Gosha | Shipped (merged + deploy result). Waiting for human acceptance. |
| Done | human only | Accepted |
| Blocked | Gosha | Needs a human. The comment explains what's needed. |
| Canceled | human | Abandon. Uses Linear's built-in Canceled state, so no extra board column is needed (hide canceled issues in the board's display options). |

State names default to the ones above (`Ready`, `In Progress`, `In Review`, `Blocked`). A project only needs `linear.states` in its config if its team uses different names, and it can override any subset. On startup Gosha checks that every state it will use exists in the team, and refuses to run that project if any is missing. Done and Canceled aren't mapped: Gosha recognises them by Linear's built-in state *types* (`completed`, `canceled`), so renaming them doesn't matter. A `duplicate`-type state is treated like Canceled.

**Linear ⇄ GitHub integration:** if installed, its automations (e.g. "PR merged → Done") **must be disabled** for this team, or they will fight Gosha. Keeping the integration only for linking PRs to issues is fine.

### What Gosha writes to Linear (and nothing else)

- **PR link:** added as a Linear *attachment* on the ticket (not a comment), so it shows up as a link card.
- **Blocked comment:** what step it was on, what went wrong in plain words, what it needs from you, and links.
- **Delivered comment** (moving to In Review), short on purpose; reviews, fixes and checks are on the PR:
   - the PR link and merge commit
   - a 2–4 line summary of what changed and why (the implementer's first summary)
   - the deploy, if configured: ✅ or ❌ with a link to the workflow run, and for a failure the failing step
   - assumptions made, and anything deliberately left out (the implementer's `assumptions` and `left_out`, from every pass)

  What it reports is recorded on the run as the pipeline goes, so a delivery that resumes after a restart still has all of it.

Progress (reviews, fixes, checks) is on the PR. Alerts (paused, Blocked, delivered, usage limits, crashes) go to Telegram (§9).

## 4. Architecture

A single Go binary with a single long-running daemon that serves every project on the machine.

```
gosha daemon
 ├── agent slots: `concurrency` agent passes at once, across all projects
 ├── project supervisor "nextdesk"  (goroutine, panic-isolated, holds run.lock)
 │     ├── linear poller        (every 30s)
 │     └── ticket pipeline      (at most 1 active ticket per project;
 │                               context refreshed before each pickup)
 ├── project supervisor "avisodesk"
 ├── project scan: every minute, starts supervisors for added projects, stops removed ones
 ├── heartbeat ping (every 30s, if configured)
 ├── notifier: async Telegram queue for alerts (never blocks the pipeline)
 └── store (SQLite): runs, steps, round counters, session ids, cursors
```

Why one daemon rather than one per project: Claude subscription limits and the host's CPU/RAM are shared by all projects. A single scheduler can enforce a global limit on agent runs, and there is one service and one log. If one project is broken (bad config, failing API), only that project's supervisor is affected.

- **Agent slots:** a slot is held for one agent pass (implement, review, address, fix), not for a whole ticket. Waiting for CI or a deploy holds none, so another project's agent can run meanwhile. Time spent waiting for a slot doesn't count toward `agent.timeout`.
- **One process per project:** a supervisor holds `projects/<name>/run.lock` while it runs. `gosha run` takes the same lock, so it refuses a project the daemon is running, and the daemon skips (and retries every minute) a project `gosha run` holds.
- **Panics** are recovered per supervisor: the panic and its stack go to the log, a 💥 alert goes to Telegram, and the supervisor waits 5 minutes and polls again. The open run resumes, because every step is idempotent (§8). Only the first crash in a row alerts; after a pass that succeeds, the next crash alerts again. A ticket that panics every time holds its project until a human moves it out of Ready (e.g. to Backlog); other projects aren't affected.

### Packages

```
cmd/gosha/            CLI entrypoint (all commands in §11.6)
internal/config/      global + project config loading and validation
internal/linear/      GraphQL client + app OAuth: poll, transition, comment, attachment, relations
internal/notion/      API client, block → markdown, incremental page-tree mirror (§6)
internal/github/      GitHub REST API via go-github + ghinstallation: App auth, PRs, reviews, comments, check runs, workflow runs, merge
internal/notify/      Telegram sender, rate limiting, retries; heartbeat ping
internal/gitx/        bare clone, fetch, ticket checkouts, merge, push (shells out to the git CLI; token injected per command, hooks off)
internal/agent/       claude CLI runner, prompt rendering, result parsing
internal/tmpl/        prompt data model + functions (§5.3)
internal/pipeline/    per-ticket state machine, limits, recovery
internal/store/       SQLite persistence
internal/daemon/      supervisors, project scan, panic isolation, heartbeat, graceful shutdown
internal/flock/       advisory file locks shared by every gosha process (run.lock, context.lock)
```

**Tooling choices:**
- **No `gh` CLI.** GitHub is accessed through its API from Go (`google/go-github`, with `bradleyfalzon/ghinstallation` for GitHub App tokens). That gives typed responses, automatic token refresh, conditional requests to save rate limit, and no command output to parse.
- **The `git` CLI rather than go-git.** It has full merge and conflict support. The App token is passed in for each command and never stored.
- **Host dependencies stay at two:** `git` (any version a supported OS ships) and `claude`. On Linux, Claude Code's sandbox also needs `bubblewrap` and `socat` (not yet verified, see [SPIKES.md](SPIKES.md#open-items)); `gosha doctor` checks for them.

## 5. Configuration

Configuration is split cleanly in two:
- **Global** (machine-level, shared by every project): where Gosha's files live, how many agents may run at once, and Gosha's own identities (GitHub App, Claude, Telegram).
- **Project** (everything about one project): lives in that project's repo at `.gosha/config.yaml`. The global config contains **no project settings and no project list**.

### 5.1 Global: `~/.gosha/config.yaml`

```yaml
concurrency: 1                 # max simultaneous agent runs across all projects

agent:
  sandbox: true                # default; false = no sandbox and auto mode, see §7.1
  writable: []                 # extra paths agents may write (toolchain caches beyond the defaults, §7.1)

github_app:                    # one app, installed on every project repo
  app_id: 123456
  private_key_file: github-app.pem   # relative to ~/.gosha; chmod 600

telegram:
  chat_id: -1001234567890      # private channel; the bot is an admin with "Post messages"
  digest_at: "09:00"           # daily digest, local time; empty disables it

heartbeat_url: ""              # optional; GET every 30s, for an outside monitor (§9)
retention: 30d                 # how long finished runs' transcripts and idle Blocked workspaces are kept (§11.3)
```

**Project discovery:** the projects Gosha manages are the directories under `~/.gosha/projects/` that contain a `repo/` clone. There's no separate list to keep in sync.
- `gosha add <repo-url>` makes a bare clone into `projects/<name>/repo` and validates its `.gosha/config.yaml`. The URL is needed only here; afterwards the clone's `origin` remote records it.
- `gosha remove <name>` deletes `projects/<name>/` entirely.

The project name comes from the repo's config.

Secrets aren't in any config file; they're read from `secrets.env` by fixed key names (§5.4).

### 5.2 Per project: `<repo>/.gosha/config.yaml`

**Read from the default branch, never from a checkout.** There's no local copy. On every poll, Gosha runs `git fetch origin` in the project's bare clone, then reads the file with `git show origin/<default>:.gosha/config.yaml`; `.gosha/instructions.md` is read the same way. (A bare clone has no `origin/*` refs by default, so `gosha add` sets `remote.origin.fetch` to `+refs/heads/*:refs/remotes/origin/*`.) Agents therefore can't change their own rules, and a config change takes effect on the next poll after it reaches the default branch, with no restart.

**Invalid or missing config: fail closed.** Gosha never keeps running a project on an outdated config.
- **Invalid config** (bad YAML, failed validation, a missing secret key): the project goes **paused**. No new tickets are picked up. The config is read once at the start of each pass, and a pass uses it to the end. A run that continues in a later pass (usage limit, restart, Blocked → Ready) uses the config on the default branch at that time; if that config is invalid, the run waits, still In Progress, until it's fixed.
- **Telegram** gets one message naming the commit and the error, and another when the project recovers. `gosha status` shows the reason.
- **Recovery:** as soon as a valid config lands on the default branch (and the Linear and Notion checks pass too), the project resumes, including any paused ticket.
- **`.gosha/config.yaml` removed from the default branch:** treated the same way, the project is paused.
- **GitHub unreachable** (fetch fails): not a config problem. Nothing can be pushed or merged anyway, so the project waits and retries.

```yaml
name: nextdesk

linear:
  team: NXT                    # team key
  states:                      # optional; defaults shown, override any subset
    ready: Ready
    in_progress: In Progress
    in_review: In Review
    blocked: Blocked

notion:
  root_page: https://www.notion.so/NextDesk-<id>

github:
  default_branch: main         # optional, autodetected
  merge_method: squash

review:
  max_rounds: 3                # reviewer ⇄ implementer rounds before Blocked

checks:
  required: []                 # optional explicit check names; see §8.4
  start_grace: 3m
  timeout: 30m
  max_rounds: 3                # agent fix rounds for failed checks before Blocked

deploy:                        # optional
  workflow: deploy.yml         # watch this workflow on the merge commit
  url: https://nextdesk.example.com
  timeout: 20m

agent:
  timeout: 60m                 # total agent working time per pickup (waiting for CI doesn't count)
  model: ""                    # optional override
```

### 5.3 Prompts

The prompts are built into the binary:

| Prompt | Role | Notes |
|---|---|---|
| `implement.md` | implementer, first pass | |
| `address.md` | implementer, feedback pass | review comments, CI failures, conflicts, human comments |
| `review.md` | reviewer | fresh context every round |

A repo can add an optional **`.gosha/instructions.md`**, read from the default branch like the config and appended to every prompt under "Project instructions". The repo's own `CLAUDE.md` applies to agents as well. The prompts themselves can't be replaced, so their data (`.Ticket.*`, `.Feedback.*`, `.FollowUp.*`, …, rendered with Go's `text/template`) is internal and can change with Gosha.

### 5.4 Secrets

Config files never contain secrets, not even references to them. Gosha reads each secret by a fixed key name:

| Key | Used for |
|---|---|
| `LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET` | Gosha's Linear OAuth app (§8.6) |
| `NOTION_TOKEN` | the Notion integration (§6.3) |
| `CLAUDE_CODE_OAUTH_TOKEN` | agents, from `claude setup-token` (§7) |
| `TELEGRAM_BOT_TOKEN` | Telegram alerts (§9) |

- **Where values come from** (first match wins):
  1. `~/.gosha/projects/<project>/secrets.env` (optional per-project overrides, e.g. a project in a different Notion workspace)
  2. `~/.gosha/secrets.env` (shared)
  3. the process environment

  The files are `KEY=value` lines with `600` permissions, and they live only on the host. A secrets file (or the GitHub App key) that group or others can read is refused, the same fail-closed way as an invalid config.
- **Missing keys are errors:** a project whose Linear, Notion or Claude key is unset or empty is paused (§5.2), naming the key. It never silently becomes an empty string.
- **Redaction:** resolved secret values are scrubbed from logs, Telegram messages and agent transcripts.

## 6. Project context (Notion mirror)

Gosha mirrors the project's Notion root page and every page beneath it into `~/.gosha/projects/<project>/context/`. The result is a plain markdown tree that the agent can read and grep.

### 6.1 Page tree → file tree

Example Notion structure:
```
NextDesk                 (root page)
├── Product
│   ├── Vision
│   └── Personas
├── Architecture
│   ├── Auth
│   └── Data model
└── Glossary
```

Mirror:
```
context/
├── INDEX.md                    # generated map of the whole tree (agent starts here)
├── README.md                   # content of the root page "NextDesk"
├── product/
│   ├── README.md               # content of "Product"
│   ├── vision.md
│   └── personas.md
├── architecture/
│   ├── README.md
│   ├── auth.md
│   └── data-model.md
├── glossary.md
└── .manifest.json              # page id → path, title, child pages, last_edited_time (Gosha's bookkeeping)
```

**Rules:**
- **A page without child pages** becomes `<slug>.md`.
- **A page with child pages** becomes a directory `<slug>/`. Its own content goes in `<slug>/README.md`, and its children go inside.
- **Root page:** `README.md` at the top of `context/`.
- **Names:** the page title passed through `slug` (lowercase, `-` separated, at most 60 characters). Page icons and emoji are dropped from names, so `🔐 Authentication & Authorization` becomes `authentication-authorization.md`.
  - Sibling pages with the same title all get a short id suffix (the id's last 4 characters, `auth-3f9a.md`), so the name doesn't depend on which came first. So does a page named `README` (or `INDEX` at the top), which would clash with the generated files.
  - An empty title becomes `untitled-<last 8 characters of the id>`.
- **Child pages in the parent's text:** where a child page appears in the parent's body, the markdown gets a relative link to the child's file.
- **Links between pages** (mentions, page links) are rewritten to relative paths inside the mirror. Links to pages outside the mirror keep their Notion URL.
- **Front-matter** on every file:
  ```yaml
  ---
  title: Data model
  icon: 🗂️                       # page emoji icon, if any (never part of the file name)
  notion_id: 1a2b…
  url: https://www.notion.so/…
  last_edited: 2026-10-12T09:14:00Z
  ---
  ```
- **`INDEX.md`:** the full tree in Notion's order, with each page's title, path, first sentence and last-edited date. The agent's prompt points it here first.
- **Pages linked from tickets** but outside the tree (a `_linked/` directory) are not mirrored for now; such links keep pointing at Notion.

### 6.2 Refresh
- **When:** only right before a pickup (including a run that continues after an interruption). An idle project's mirror isn't kept fresh, since nothing reads it until the next pickup; `gosha context refresh` refreshes it by hand.
- **Incremental:** one paginated search lists every page shared with the integration with its `last_edited_time`. A page's content is fetched again only when that changed, when the page is new, or when it was fetched within two minutes of its last edit (Notion rounds `last_edited_time` to the minute, so a later edit in the same minute wouldn't show).
- **The tree comes from the pages themselves:** a page's child pages are the child page blocks in its content, kept in the manifest in Notion's order. Search only says what changed, so a page search hasn't indexed yet is still mirrored (it's fetched directly).
- **Renames, moves, new and deleted pages:** `.manifest.json` maps page id → path. When the tree changes (a page is added, renamed, moved or deleted), every page is fetched again, so links between pages are resolved from fresh content; that's rarer than content edits, and simpler than tracking links. Pages deleted in Notion are deleted from the mirror; links to them, and to pages outside the tree, point at Notion.
- **Blocks → markdown:** paragraphs, headings, lists, to-dos, code, quotes, callouts, toggles (expanded), tables, dividers, bookmarks and links, and mentions. Images and files become a link plus a caption. Anything unsupported becomes `<!-- unsupported: type -->`.
- **Databases aren't mirrored** (for now). A child database appears as `<!-- database "<title>" not mirrored -->` at its position in the parent page. Database support can be added later without changing the tree layout (a database would become a directory, one file per row).
- **Rate limit:** respects Notion's limit (~3 req/s); rate limits and server errors are retried with backoff.
- **Consistency:** before each pickup, a refresh runs and a copy of the mirror (without the manifest) is frozen into the ticket's workspace (`workspaces/<project>/<ticket>/context/`) for that pickup, so its agents see consistent data. A lock (`projects/<project>/context.lock`) keeps a refresh and a freeze, from the daemon or the CLI, from interleaving.
- **Failures:**
  - **No access** (bad token, root page not shared or deleted): a setup problem. The project is **paused**, the same fail-closed way as an invalid config (§5.2). Only a refresh can clear it, so it's retried at the next pickup: the project stays paused until a Ready ticket's pickup finds Notion readable again.
  - **Anything else** (Notion down, network, rate limits that don't clear): the last mirror is used and a warning is logged. Only if no mirror was ever built does the project wait and retry without picking anything up.

### 6.3 Access
- **Read-only:** through a Notion internal integration with only the **Read content** capability, shared with the root page.
- **Agent access:** the agent reads the pickup's frozen copy of the mirror through `--add-dir`. The sandbox (§7.1) makes it read-only, and since it's a per-run copy, nothing can change the mirror itself. It's never committed to the repo.

## 7. Agents

Both roles run the `claude` CLI in headless mode on the **Claude subscription**. Authentication uses a long-lived subscription token from `claude setup-token`, passed as `CLAUDE_CODE_OAUTH_TOKEN`. This doesn't depend on an interactive login or an OS keychain, so it works the same under any service manager and any OS user. Gosha removes `ANTHROPIC_API_KEY` from the agent's environment so nothing is ever billed to the API. (`--bare` can't be used: it ignores OAuth tokens.)

Every run uses `--output-format stream-json --verbose`; the stream is saved (redacted) as the run's transcript and read for the session id, the result and usage-limit events (§8.11). The prompt goes in on stdin, never in argv, because ticket text is unbounded and argv is visible to every user in `ps`. Each project has its own Claude settings directory, `projects/<project>/claude/` (§11.3): sessions persist there so `--resume` works across passes, and one project's sessions never show up in another's runs.

| | Implementer | Reviewer |
|---|---|---|
| Working dir | ticket checkout (read/write) | a fresh clone of the pushed head in the ticket's workspace (`review/`), read-only to it and removed after the round |
| Context | ticket + human comments + project context + feedback | ticket + project context + the last round's findings and reply; it reads the diff with `git diff` |
| Session | resumes its own session across address passes when possible | **always fresh** |
| Tools | all tools (edit, write, shell, local git) | edit/write tools removed; read, search, shell to read the code (`git diff`, `grep`). CI runs the tests. |
| Output | commits in the checkout + result JSON | result JSON |

**Agents never get GitHub or Linear credentials,** neither in their environment nor on disk they can reach (§7.1). The implementer commits locally, and **Gosha** pushes, opens PRs, posts comments and merges.

### 7.1 Permissions & sandbox
Agents must run **unattended**: nothing may wait for an approval prompt, and nothing a task legitimately needs (`pnpm build`, a codegen script, `ls`) may be silently denied.

Two layers do this, and both are needed (M0 showed the OS sandbox alone isn't enough, see [SPIKES.md](SPIKES.md#1c-sandbox-partial--fixed-with-dontask)):
- the **OS sandbox** confines Bash and every process it starts;
- **permission rules** confine Claude's own file tools (Read, Write, Edit), which run outside the OS sandbox.

Gosha generates both per run into a settings file passed with `--settings`. The settings file lives next to the transcript, outside anything the agent can write.

The two layers need different shapes, because they resolve conflicts differently (checked on macOS, see [SPIKES.md](SPIKES.md#1i-sandbox-paths-inside-gosha-m2)):
- **OS sandbox:** `denyRead` covers all of `~/.gosha`, and `allowRead` re-allows the ticket's workspace, `workspaces/<project>/<ticket>/` (§11.3); `allowWrite` re-allows the checkout and temp directory in it. A more specific allow wins here.
- **Permission rules:** a deny always beats an allow, so `Read(~/.gosha/**)` would also hide the checkout from the Read tool. Agents only ever work in `workspaces/`, so Gosha denies every other top-level entry of `~/.gosha` (`projects/`, `secrets.env`, the database, logs, and anything added later). A test checks that no top-level entry but `workspaces/` is left readable, so a new one can't fail open.
- **Trade-off:** the Read tool can see other tickets' workspaces: repo code and frozen context, never credentials.

- **One switch, `agent.sandbox` in the global config, picks both the sandbox and the permission mode.** It's host-level only: no project config, and so no merged PR, can change it.

  | `agent.sandbox` | Sandbox | Permission mode |
  |---|---|---|
  | `true` (default) | on | `dontask`: `--permission-mode dontAsk --permission-prompts none`. Only what the generated allow rules list is permitted; everything else is denied without a prompt. The allow rules are: `Read`, `Bash` (confined by the sandbox), web fetch/search, and `Edit` inside the checkout and the run's temp directory only. |
  | `false` | off | `auto`: `--permission-mode auto --permission-prompts none`. Claude's classifier approves or blocks each action (in M0 it blocked no legitimate step). Without the sandbox, `dontask` would let Bash read secrets and write anywhere while still denying other tools: no protection, same friction. |
- **Deny rules** apply in both modes: no `Read` or `Edit` anywhere in `~/.gosha` outside `workspaces/`, and no `Edit` in the context copy. `Edit` is allowed only in the run's checkout and temp directory.
- **Reviewer:** read-only. Its edit and write tools are *removed* (`--disallowedTools Edit,Write,NotebookEdit`), which holds in any mode, and it gets no `Edit` allow rules. In the sandbox its Bash can read its clone but write only the run's temp directory (for the result file), so it runs no tests or builds: CI does. With the sandbox off, only the removed tools and the `auto` classifier stop it from writing, which is why it still runs in its own clone and never in the implementer's checkout.
- **Sandbox:** Gosha enables **Claude Code's built-in sandbox** (OS-level: Seatbelt on macOS, bubblewrap on Linux) for every agent run, with `failIfUnavailable: true` and `allowUnsandboxedCommands: false` (the agent can't ask to run a command outside it):
  - **Writable:** the ticket's checkout, the run's temp directory, and toolchain caches (the reviewer: only the run's temp directory). The default cache list depends on the OS (e.g. `~/.cache`, `~/go`, `~/.npm`, and `~/Library/Caches` on macOS), plus `agent.writable` from the global config. The sandbox can't create the missing parent of an allowed directory, so Gosha creates every writable directory before the run.
  - **Read-only:** the run's frozen context copy.
  - **No access:** `~/.gosha`, i.e. secrets, the GitHub App key, other projects and the database.
  - **Network:** allowed (dependency installs need it).
- **If the sandbox can't be enforced** on a host (unsupported OS setup, missing Linux packages), Claude refuses to start (`failIfUnavailable`) and Gosha **refuses to start agents** unless the operator opts in with `agent.sandbox: false` in the global config. `gosha doctor` warns that agents then run in `auto` mode and can read Gosha's secrets, and that a separate OS user (`--user`, §11.2) is the remaining protection.
- **Environment:** agents get a scrubbed environment. No Gosha secrets, `ANTHROPIC_API_KEY` or GitHub/Linear tokens. Only:
  - `CLAUDE_CODE_OAUTH_TOKEN`, `PATH`, `HOME`
  - `CLAUDE_CONFIG_DIR` (Gosha's own settings directory, §11.2) and `TMPDIR` (the run's temp directory)
  - `GIT_AUTHOR_NAME/EMAIL` and `GIT_COMMITTER_NAME/EMAIL` set to the bot identity (§8.6), so the implementer can commit without any git config

### Result contract

The agent writes JSON to the path in `.ResultFile`, which lives outside the repo.

Implementer:
```json
{ "status": "done" | "blocked",
  "summary": "what changed and why",
  "verification": "what was run",
  "assumptions": ["..."],
  "left_out": ["what the ticket could be read to ask for but wasn't done, and why"],
  "blocked_reason": "...", "questions": ["..."] }
```

Reviewer:
```json
{ "verdict": "approve" | "request_changes",
  "summary": "...",
  "comments": [{ "path": "x.go", "line": 42, "severity": "must|should|nit", "body": "..." }] }
```

A missing or invalid result gets one retry: the same session is resumed (`--resume <session id>`) with a "fix your result file" nudge that quotes the parse error. If that also fails, the step counts as failed.

## 8. Per-ticket pipeline

Every step is **idempotent**. It first checks the real state (branch, PR, checks), then acts. This is what makes crash recovery and resume work.

**Feedback model.** Gosha treats every signal about a change as *feedback* for the implementer, whatever its source:

| Source | Feedback | Limit |
|---|---|---|
| Reviewer agent | inline review comments + verdict | `review.max_rounds` |
| CI checks | failed check names + tail of failing job logs | `checks.max_rounds` |
| Base branch | merge conflicts | (counted with check rounds) |

All of them go through the same **address** step (`address.md`, data in `.Feedback.*`). Only how feedback is *collected* and how each source is *limited* differs. Every reviewer `request_changes` uses a review round, and every CI or conflict fix uses a check round. Every limit is reset when a human moves the ticket back to Ready.

Humans instruct Gosha only through Linear (§1.4): comments on the PR are for people and aren't read. New ticket comments reach the next implementer pass.

### 8.1 Pickup
1. The poller lists Ready issues in the team.
2. It skips any issue whose "blocked by" issues are not yet **In Review or Done**. (In Review means the code is already merged, so it's safe to build on.)
3. It orders the rest by priority (Urgent first, Linear's "No priority" last), then by creation time, and takes one if the project is idle.
4. Claim: move the ticket to In Progress and record the run.
5. Decide the mode:
   - an open Gosha PR exists → **resume**: the round limit resets; new ticket comments go to an **address** pass first, otherwise the PR gets a fresh review (§8.3)
   - the ticket's last PR was merged, by Gosha or by hand → **follow-up**: a fresh run on a new branch with suffix `-2`, `-3`, …, whose implementer is told about the earlier PR (`.FollowUp`) and that the new comments say what to change. A follow-up with no new human comment goes **Blocked** at once, asking what to change; no agent runs.
   - the ticket's PR was closed without merging → delete the branch → **fresh**
   - otherwise → **fresh**

   Ticket comments posted while Gosha works on the ticket are added to the instructions of the next implementer pass in the same pickup.

### 8.2 Implement
1. Refresh the project context and freeze a copy for the run (§6.2).
2. Fetch, then create the ticket checkout `workspaces/<project>/<ticket>/checkout/` from `origin/<base>`. Gosha runs no commands in it: the implementer installs dependencies itself, sandboxed like the rest of its work (the repo's `CLAUDE.md` says how).
   - **The checkout is a self-contained local clone, not a linked `git worktree`.** The agent can write to everything in it, including `.git/`. A linked worktree would share the bare clone's refs, hooks and config, including `origin/<default>` (where config is read from) and the hooks Gosha's own push would run with the token in its environment.
   - **Gosha never runs git inside the checkout once an agent has used it.** It collects the work with `git fetch <checkout>` *from the bare clone*, which runs only upload-pack in the checkout, and then counts commits, measures the diff and pushes from the bare clone. Every Gosha git command also runs with hooks disabled.
   - A new run always starts from a fresh checkout. Later passes of the same run (resume) reuse it, and a lost checkout is rebuilt from the work Gosha already collected.
3. Run the implementer with `implement.md`.
4. If the result is `blocked`, go to **Blocked**.
5. If there are no commits, go to Blocked ("no changes produced").
6. Push and open the PR as the GitHub App: title `NXT-12: <title>`, body = summary + verification + assumptions + ticket link. Add the PR as a Linear attachment.

**Comment cursor:** the ticket's comment cursor moves past the new human comments only once the implementer has actually been given them, so an interrupted pass (usage limit, shutdown) doesn't lose instructions.

**Branch naming.** Always `gosha/<ID>-<slug of the title>`, e.g. `gosha/NXT-12-add-login` (the title lowercased, non-alphanumerics as `-`, at most 40 characters). The ticket ID lets Linear's GitHub integration link the branch.
- **The name is used only when the branch is created.** Gosha stores the branch and PR number in `gosha.db`, and every later step uses that record, never a lookup by name.
- **Suffixes:** follow-up runs on the same ticket, and names that already exist on the remote without being Gosha's, get `-2`, `-3`, … appended. Gosha never overwrites a branch it doesn't own.

### 8.3 Review loop (at most `review.max_rounds`)
1. Run the reviewer on the branch head, in a fresh clone (§7). From round 2 its prompt includes the last round's findings and the implementer's reply, so it checks they were resolved instead of starting over.
2. Post the result as a GitHub PR review on that head SHA, with inline comments. The review event is `COMMENT`, with a ✅ or ❌ verdict in the body (see §8.6). Comments GitHub can't anchor (a line outside the diff, a whole file) go into the review body, so one bad line number never fails the review.
3. `approve` with no `must` comments → go to **checks**.
4. If this was round `review.max_rounds` → **Blocked**, listing the unresolved `must` comments.
5. Otherwise run the implementer with `address.md`, resuming its session in the ticket checkout, and passing the reviewer's comments and new human ticket comments. Push if it committed, and reply on the PR with its summary (a reply with no code changes is allowed: the next reviewer sees why). Start the next round.

Steps are recorded per round (`review` → `address` → `review` …), with the reviewed SHA and the posted review id, so a run that stops mid-round doesn't run or post the same review twice.

### 8.4 Checks (there's no branch protection, so Gosha decides)
Gosha waits for checks in-line: the ticket stays the project's active ticket while CI runs (up to `checks.timeout`). Waiting doesn't spend `agent.timeout`, which counts only agent passes.

For the PR's **head SHA**:
1. **Expected checks:** if `checks.required` is set, those names must appear. Otherwise whatever appears within `start_grace` must pass, and nothing appearing counts as a pass. Gosha doesn't read the repo's workflow files to guess what should run: their `branches:` and `paths:` filters decide that, and only GitHub applies them.
2. **Wait for checks to start:** poll check runs and commit statuses (the newest status per context) every ~15s. A required check that hasn't appeared within `start_grace` counts as a failure. If the only failures are checks that never started, there's nothing in the code to fix, so the ticket goes **Blocked** at once, naming them.
3. **Wait for checks to finish:** wait until all are completed, then wait another ~30s for late-starting checks before deciding. A completed check passes on `success`, `neutral` or `skipped`. Checks still running at `checks.timeout` count as failed.
4. **Any failure, cancellation or timeout** starts a check round: fetch the failed jobs' logs and cut out the failing step's section (from its `##[group]Run …` line to the first `##[error]`; a job log ends with post-job cleanup, so a plain tail misses the error), then run the implementer with `address.md` (the **fix** step). The round and its feedback are recorded against the failing head, so an interrupted fix continues with the same feedback instead of counting another round. After `checks.max_rounds` rounds → **Blocked**.
5. **After a fix:** if it committed, push, reply on the PR, and go back to review: a **re-review** of the fix. It only takes a `review.max_rounds` slot if it requests changes. Then checks run again on the new head. A fix that commits nothing goes **Blocked** with the implementer's explanation (e.g. a flaky test or an infrastructure problem).

### 8.5 Merge
1. **Base sync:** fetch, and if `origin/<base>` isn't an ancestor of the branch (`git merge-base --is-ancestor`), ask GitHub to merge it in: `PUT /pulls/{n}/update-branch` with `expected_head_sha` set to the checked head. GitHub merges in the background, so Gosha polls the PR until its head changes (up to `checks.timeout`), fetches the new head into the bare clone, and goes back to checks (no review: the PR's own change is the same). The ticket checkout is removed so the next agent pass starts from the merge commit. If the head had moved, GitHub refuses, and the ticket goes **Blocked** as in step 3.
2. **Conflicts** (GitHub answers 422) are a check round: Gosha rebuilds the ticket checkout from the branch (a fresh clone, which carries the latest base), and the implementer runs `git merge origin/<base>`, resolves the conflicts it reports and commits the merge. Gosha checks that the base is now an ancestor of the branch (otherwise **Blocked**), pushes, and the change goes through re-review and checks.
3. **Merge** through the API with `github.merge_method` (squash by default), title `NXT-12: <title> (#<n>)`, **with `sha` = the head commit that was checked**. If anything was pushed after the checks ran, GitHub refuses the merge. Gosha only pushes after a check verdict through the steps above, so that push was someone else's: the ticket goes **Blocked** rather than merging code nobody checked.
4. The merge commit is recorded at once, so a run that stops after merging finishes with cleanup and delivery on the next pass instead of being mistaken for a merge by hand.
5. Delete the branch and remove the ticket's workspace, then watch the deploy (§8.7) or deliver (§8.8).

### 8.6 Identities

**GitHub: the Gosha GitHub App.** Gosha acts on GitHub as a **GitHub App** that the operator creates once (free) and installs on each project repo. PRs, commits, reviews and merges show up as `gosha[bot]`, not as a person.

- **Auth:** the app's private key signs a JWT, which is exchanged for an **installation token** per repo. Installation tokens last 1h, and Gosha refreshes them automatically. No personal `gh` login or token is ever used.
- **Git:** Gosha pushes over HTTPS with the installation token, passed per command through `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n` (an `http.extraheader`), so it never appears in argv or any git config file. `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` keep the user's git config out.
- **Commit identity:** `<slug>[bot] <ID+<slug>[bot]@users.noreply.github.com>`, where ID is the bot *user* id (not the app id), looked up once with `GET /users/<slug>[bot]`.
- **CI:** pushes and PRs made with an app token **do** trigger Actions workflows. Only the built-in `GITHUB_TOKEN` doesn't.
- **App permissions:**
  - Contents: RW
  - Pull requests: RW
  - Checks: R
  - Actions: R
  - Commit statuses: R
  - Metadata: R
- **Workflows permission deliberately not granted:** GitHub then rejects any push that modifies `.github/workflows/` (`refusing to allow a GitHub App to create or update workflow … without 'workflows' permission`), which is a hard guarantee that agents can't change CI or deploy pipelines. Gosha recognises this rejection and goes Blocked with a plain explanation. Grant it later per need.
- **Reviews:** an app can't approve its own PR, so reviewer verdicts are `COMMENT` reviews marked ✅/❌. A free private repo has no branch protection, so an approval wouldn't gate anything anyway. Gosha's own merge gate is what counts. *Optional later:* a second app, "gosha-reviewer", for real `APPROVE` / `REQUEST_CHANGES` reviews.

**Linear: an app actor.** Gosha acts on Linear as an **OAuth app**, so comments, state changes and attachments appear as the app (e.g. "Gosha"), not as a person.
- **Setup:** the operator creates an OAuth application in the Linear workspace (Settings → API) with **Client credentials** enabled, and puts its client id and secret in `secrets.env` (`LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET`).
- **Tokens:** Gosha uses the **`client_credentials` grant** (scopes `read,write,issues:create,comments:create`). No browser or paste-back is needed. Tokens last 30 days and have no refresh token, so Gosha keeps the current one in memory and simply requests a new one on expiry or a 401.
- `gosha doctor` checks the credentials and shows which actor Gosha acts as.

### 8.7 Deploy watch (if configured)
Find the newest run of the `deploy.workflow` file for the merge commit (`head_sha`) on the base branch, polling every ~15s, and wait for it to finish (up to `deploy.timeout`). Like waiting for checks, this doesn't spend `agent.timeout`. A re-run keeps the run's id, so the newest attempt is what counts.
The ticket is **delivered whatever the outcome**: CI/CD owns the deploy (§1.1), and the code is already merged. The Delivered comment reports it:
- Success → ✅, the run and `deploy.url`.
- Failure → ❌, the run, and the failed jobs with the failing step of each job's log (as for checks, §8.4).
- No run within `deploy.timeout`, or still running → ❌, naming the workflow.

A failed deploy is fixed like any other problem with shipped work: a human comments what to change and moves the ticket to Ready, which opens a follow-up PR (§8.1) whose implementer is shown the failure. A deploy that just needs a re-run needs nothing from Gosha.
- `gosha validate` fails when `.github/workflows/<deploy.workflow>` isn't in the repo.

### 8.8 Deliver
Post the **Delivered** comment, move the ticket to In Review, and notify Telegram. The run is complete.

### 8.9 Blocked
Post the **Blocked** comment, move the ticket to Blocked, and notify Telegram. Keep the branch, PR and checkout for resume; the run stays open (step `blocked`), so moving the ticket back to Ready resumes it. Unexpected failures after the claim (an agent crash, an invalid result after the retry, a git or API error) also go Blocked, with the error in the comment. A usage limit or a shutdown doesn't: the ticket stays In Progress and the run continues on the next pass (§8.11, §10).

### 8.10 Cancel / pause
While a ticket is active, the poller re-reads its state every 30s.
- If it has left In Progress because of anyone other than Gosha → cancel the context and kill the claude process group.
- Then: Canceled → close the PR and delete the branch. Any other state → leave everything as it is (pause).

### 8.11 Agent usage limits
Claude's stream reports usage on every run as a `rate_limit_event` (`status`, `resetsAt`, and utilization of the five-hour and seven-day windows). A `status` other than `allowed`, or a failed result caused by the limit, means a usage limit: Gosha pauses **all** agent scheduling until `resetsAt` and then retries the same step. The ticket stays In Progress. This goes to Telegram (hit + resumed) and to the logs, never to Linear. Limit-related time doesn't count toward `agent.timeout`.

## 9. Telegram alerts

A Telegram bot posts **alerts** to a **private channel**: things a human may need to act on. All projects share the channel, and every message starts with the project name. Progress (pickup, reviews, fixes, checks, merge) is on the PR and in the logs, not repeated here.

**Alerts:**

| Alert | Example |
|---|---|
| project paused / recovered | `nextdesk · paused at a1b2c3d: .gosha/config.yaml: linear.team: required` (config, secrets, Linear team/states, Notion access) / `nextdesk · checks pass again at a1b2c3d · resumed` |
| blocked | `NXT-12 · ⛔ blocked while reviewing: …` + links to the ticket and PR |
| delivered | `NXT-12 · ✅ delivered · In Review` (with `· deploy ❌ failure` when the deploy failed) + links |
| usage limit | `⏸ Claude usage limit, resuming ~14:30` / `▶️ resumed` |
| crash | `💥 crashed: … · retrying every 5m0s` (once until a pass succeeds, §4) |
| daily digest | `09:00 · yesterday: 4 delivered, 1 blocked · queue: 3 Ready` |

**Rules:**
- Alerts go through an async queue, so a Telegram outage never blocks or fails delivery. Sending retries with backoff, respects Telegram rate limits, and drops alerts after ~1h.
- Messages use HTML formatting, with links to the Linear ticket and PR, and are kept short for phone reading.
- **Liveness:** Gosha can't report that it has died. With `heartbeat_url` set (§5.1), the daemon GETs that URL every 30s while it runs, so a dead-man's-switch monitor (e.g. a healthchecks service) alerts when the pings stop. The daily digest is a summary, not a liveness signal.
- Telegram is **output only**. Gosha takes instructions only from Linear.

## 10. Persistence & recovery

SQLite at `~/.gosha/gosha.db` stores runs (project, ticket, mode, PR, branch, current step, round counters, session ids, timestamps) and the last-seen comment id per ticket.

On startup, for each ticket that is In Progress in Linear **and** has an open run in the db, Gosha re-enters the pipeline at its current step. Steps are idempotent, so they reconcile with the real state of the PR and checks. A ticket that is In Progress with no run record is left untouched (a human moved it there).

**Schema migrations** are numbered SQL files built into the binary, applied in order at startup, starting from one baseline (`001_init.sql`). They're append-only: a migration is never edited, only followed by a new one, so every `gosha upgrade` (§11.4) keeps the existing `~/.gosha` working. The database records the gosha version that last migrated it. Before applying any migration to an existing database, Gosha writes a consistent copy to `gosha.db.bak-<that version>`. A database newer than the binary (after a downgrade) is refused with a message naming the release it needs.

Daemon-wide values (the usage-limit pause, the last digest date) live in the same database, in a small key/value table.

## 11. Host & operations

### 11.1 Supported hosts
- **macOS and Linux**, on amd64 and arm64. Gosha is a single static Go binary, installed from GitHub releases (§11.4). Linux is best effort until the agent sandbox is verified there ([SPIKES.md](SPIKES.md#open-items)).
- **External tools it needs:** `git` and the `claude` CLI on `PATH` (on Linux also `bubblewrap` and `socat` for the agent sandbox). Gosha checks them at startup (`gosha doctor`).
- **Windows:** not a target. WSL works as Linux.
- **Host decisions belong to the operator:** keeping the machine awake, restarting after a power failure, disk encryption, remote access. Gosha doesn't touch them. If Gosha has been restarted, it resumes runs (§10).

### 11.2 Which OS user runs Gosha
Gosha runs as an **existing regular user**, normally the one who installs it. Gosha never creates OS accounts. **Root isn't allowed.**

- **Default:** `gosha service install` installs the service for the invoking user (`$SUDO_USER` when run through `sudo`). `--user <name>` picks another existing account.
- **Root is refused:** if the target user would be root, `gosha daemon`, `gosha run` and `gosha service install` all fail, suggesting a regular user be created first (e.g. `adduser deploy` on a VPS). Agents run unattended (§7.1) and execute commands derived from ticket text. As root, those commands would own the machine, which is also why Claude Code refuses to skip permissions as root.
- **Want more isolation?** Create a separate regular account yourself and install with `--user <it>`. It's an ordinary account, so nothing special is needed from Gosha.
- **Nothing tied to a person's login:** GitHub goes through the App, Claude through the setup token, everything else through the secrets files. Gosha never uses the user's `gh` login, SSH keys, git identity or OS keychain, so moving Gosha to another user or host is just moving `~/.gosha`.
- **Claude:** the `claude` CLI must be on the service user's `PATH`. The token from `claude setup-token` can be generated anywhere, because it's tied to the Claude subscription, not the OS user.
- **Toolchains:** whatever the agent's tasks need (go, node, make…) must be on the service's `PATH`. `service install` captures it from the user's login shell.
- **Agent environment:** agents run `claude` with Gosha's own settings directory (`CLAUDE_CONFIG_DIR`), so the user's personal `~/.claude` (CLAUDE.md, hooks, MCP servers) doesn't apply to agent runs. The repo's own `CLAUDE.md` still does.

### 11.3 Paths
Everything lives in **one directory**, `~/.gosha` of the running user. `--home` or `GOSHA_HOME` overrides it. Global items sit at the root, everything about one project sits under `projects/<name>/`, and everything agents work in sits under `workspaces/`. Folder names describe their content; only `workspaces/` is reachable by agents (§7.1).

```
~/.gosha/                                    # chmod 700
├── config.yaml                              # global config (§5.1)
├── secrets.env                              # shared secrets, chmod 600
├── github-app.pem                           # chmod 600
├── gosha.db                                 # runs/state for all projects
├── logs/gosha.log                           # structured, rotated; every line tagged with project
├── projects/
    └── <project>/
        ├── secrets.env                      # optional overrides, chmod 600
        ├── repo/                            # bare clone (presence = project registered)
        ├── claude/                          # CLAUDE_CONFIG_DIR for this project's agents (sessions)
        ├── context/                         # Notion mirror (INDEX.md + page tree)
        ├── context.lock                     # serialises refreshes and freezes
        └── runs/<ticket>/
            ├── <n>-<role>.jsonl             # full agent transcript (redacted); role is implement, review or address
            ├── <n>-settings.json            # generated agent settings
            └── checks-<n>.json              # the checks a fix round was given
└── workspaces/<project>/<ticket>/           # everything agents work in; all of it can be rebuilt
    ├── checkout/                            # the implementer's clone, one per active ticket
    ├── review/                              # the reviewer's read-only clone, only during a review
    ├── context/                             # this pickup's frozen copy of the mirror (read-only to agents)
    └── tmp/<n>/                             # pass n: the agent's TMPDIR and result file
```

**Cleanup:**
- A ticket's workspace is removed when its run ends: merged, canceled, or replaced by a restart after its PR was closed unmerged. A Blocked run keeps its workspace for `retention` (§5.1), then loses it; a resume rebuilds it (§8.2).
- A finished run's `runs/<ticket>/` files (transcripts, settings, checks) are removed `retention` after the run ended. The run's row in `gosha.db` stays.
- `gosha remove` removes a project's workspaces with it. A host move can skip `workspaces/`.
- `gosha doctor` reports how much space `workspaces/` and `projects/*/runs/` take.
- The daemon applies these rules on its own. `gosha prune` applies them on demand (e.g. without a running daemon, or to free space now). It never touches an active run's workspace, takes the same locks as the daemon's cleanup, and never deletes run records from `gosha.db`.

The project's config is **not** in this tree; it's read from the repo (§5.2). `gosha config show <project>` prints the effective config (repo file + defaults).

### 11.4 Install & upgrade
- **Releases:** every merge to `main` with a `feat`, `fix` or `perf` commit since the last tag publishes a GitHub release. GoReleaser builds archives for macOS and Linux on amd64 and arm64, plus `checksums.txt`. Versions stay `0.x`: `feat` and breaking changes bump the minor, everything else the patch. `gosha --version` reports it.
- **Install:** `curl -fsSL https://raw.githubusercontent.com/galimru/gosha/main/install.sh | sh`. The script detects the OS and architecture, downloads the latest release, verifies it against `checksums.txt`, and installs `gosha` into `~/.local/bin` (`/usr/local/bin` when run as root). `--prefix <dir>` picks another directory and `--version v0.x.y` pins a release. It needs only `sh`, `curl` or `wget`, `tar`, and `sha256sum` or `shasum`. Installing the binary as root is fine; running Gosha as root isn't (§11.2).
- **With a Go toolchain,** `go install github.com/galimru/gosha/cmd/gosha@latest` also works.
- **Upgrade:** `gosha upgrade` downloads the latest release for the running OS and architecture, verifies its checksum, and replaces its own binary atomically (written next to it, then renamed over it). If the service is installed, it then restarts it, which needs root like `service restart`; in-flight runs resume (§10). Without root, it still replaces the binary and prints the restart command.
  - `--check` only reports whether a newer release exists. `--version v0.x.y` installs a specific release, including an older one.
  - A binary in a directory the user can't write fails with a hint to use `sudo`. A `dev` build (from source) refuses to upgrade itself.

### 11.5 Service
- `gosha daemon` runs in the foreground and logs to a file and stderr. Any service manager can run it.
- `sudo gosha service install` / `sudo gosha service uninstall` register Gosha with the host's service manager. Both need root.

| | Linux (systemd) | macOS (launchd) |
|---|---|---|
| Unit | `/etc/systemd/system/gosha.service` | `/Library/LaunchDaemons/dev.gosha.daemon.plist` |
| Runs as | `User=<service user>` | `UserName=<service user>` |
| Start at boot / restart on failure | `WantedBy=multi-user.target`, `Restart=on-failure` | `RunAtLoad`, `KeepAlive` |
| Install does | write unit → `daemon-reload` → `enable --now` | write plist → `launchctl bootstrap system` |
| Uninstall does | `disable --now` → remove unit → `daemon-reload` | `launchctl bootout` → remove plist |

- **`install` flags:**
  - `--user <name>` (default: the invoking user, i.e. `$SUDO_USER` under sudo, otherwise the current user; must exist and must not be root)
  - `--binary` (default: the resolved path of the running `gosha`)
  - `--home` (default: the target user's `~/.gosha`)
  - `--print` (show the unit file without installing it)
- **Environment:** the unit sets an explicit `PATH`, taken from the target user's login shell at install time, plus `HOME`. That way the agent's tools are found.
- **Before writing anything,** `install` checks that the user exists and isn't root, the binary is executable, `gosha doctor` passes for that user, and the global config is valid.
- **Re-running `install`** is safe: it replaces the unit and restarts the service. Upgrades go through `gosha upgrade` (§11.4).
- **The unit runs the binary by the absolute path it had at install time.** Moving the binary means running `install` again.
- **`uninstall`** stops and removes only the service. Config, secrets and state (§11.3) are left untouched.
- **Day-to-day control:** thin wrappers, so the same commands work on both OSes:

| Command | Linux (systemd) | macOS (launchd) |
|---|---|---|
| `gosha service start` | `systemctl start gosha` | `launchctl bootstrap system <plist>` (if not loaded) + `kickstart` |
| `gosha service stop` | `systemctl stop gosha` | `launchctl bootout system/dev.gosha.daemon` |
| `gosha service restart` | `systemctl restart gosha` | `launchctl kickstart -k system/dev.gosha.daemon` |
| `gosha service status` | `systemctl show` (state, PID, uptime) | `launchctl print` (state, PID) |

  - `start`, `stop` and `restart` need root.
  - `status` doesn't need root. It shows the service manager's view (state, PID, uptime). For what Gosha is doing, use `gosha status`.
  - `stop` sends SIGTERM, so in-flight runs stay resumable.
  - On macOS, `stop` unloads the daemon, so it stays stopped until `start` (KeepAlive would otherwise restart it).
- **Unsupported service manager** (e.g. a Linux host without systemd): `install` fails with a clear message. `--print` plus running `gosha daemon` under your own supervisor still works.
- **SIGTERM** finishes in-flight API calls, kills agents, and leaves runs resumable.
- A container image is a possible later option, not part of the current plan.

**Moving to another host:** all state lives in `~/.gosha` (§11.3). Stop the service, copy that directory to the new host (`git` and `claude` must already be available there), install Gosha (§11.4) and the service, and start it. Runs that were in flight resume (§10). Nothing is tied to the original machine: no keychain, no hostnames, no absolute paths outside the user's home.

### 11.6 CLI
Commands act on the `~/.gosha` of the user running them, normally the same user the service runs as.

- `gosha init`: run in a local checkout; writes a starter `.gosha/config.yaml` to commit
- `gosha add <repo-url>` / `remove` / `list`
- `gosha config show <project>`: effective config
- `gosha validate [path]`: an offline check of a repo's `.gosha/config.yaml` (it parses, fields are valid, referenced files exist). It needs no secrets or network, so it can run in the project's CI.
- `gosha doctor [project]`: everything about the host: git, claude, the sandbox tools, file permissions, the global config, the GitHub App, and for each project (or the one named) its config on the default branch, its secrets, its Linear team, states and actor (§8.6), and Notion access to its root page
- `gosha status`: projects, active or paused state (and why), the context mirror (pages, last refresh), current ticket and step, usage-limit pause
- `gosha logs [-f] [--project nextdesk]`
- `gosha context refresh <project>`: pull the latest from the context source (Notion) into the local mirror
- `gosha context path <project>`: print where the mirror lives
- `gosha context show <project>`: print `INDEX.md`, the page tree the agent starts from
- `gosha run <project> [--once] [--ticket NXT-12] [--dry-run]`: foreground loop for one project, for development and prompt tuning. It takes the project's run lock, so it refuses a project the daemon is running. `--once` handles a single ticket. `--dry-run` does a single pickup pass and prints what would happen (pickup, queue, skipped tickets and why, mode, branch), without changing anything in Linear or GitHub or posting to Telegram.
- `gosha secrets edit`: opens `secrets.env` in `$EDITOR`
- `gosha service install|uninstall|start|stop|restart|status`
- `gosha prune [project] [--dry-run] [--older-than 7d]`: apply the cleanup rules now (§11.3). `--dry-run` lists what would be removed and how much space it frees; `--older-than` overrides `retention` for this run.
- `gosha upgrade [--check] [--version v0.x.y]`: replace the binary with a release and restart the service (§11.4)
- `gosha daemon`: every registered project, in the foreground (what the service runs, §11.5)

## 12. Security

- Ticket and Notion text are **untrusted input** to an agent that can run commands. Mitigations:
  - the agent has no credentials in its environment, and the sandbox blocks `~/.gosha` (§7.1)
  - OS-level sandbox: writes limited to the checkout, temp and toolchain caches
  - never root
  - Gosha does all pushing and merging
  - `.gosha/` is read from the default branch
  - the GitHub App has no Workflows permission
  - the reviewer flags any PR that touches `.gosha/` unless the ticket explicitly asks for it
  - agents use Gosha's own Claude settings directory, not the user's personal one
  - ticket checkouts are separate clones, and Gosha never runs git inside one after an agent has used it (§8.2)
- **Service user** (§11.2): Gosha runs as an existing regular user and refuses root. For extra isolation, the operator can create a separate regular account and install with `--user`.
- Credentials:
  - GitHub: App private key
  - Linear: OAuth app client id + secret (`client_credentials` tokens)
  - Notion: an internal integration with read-only access
  - Claude: setup token
  - Telegram: bot token

## 13. Decisions log

- One GitHub identity: the Gosha GitHub App (no PRs as the human). Reviews are `COMMENT` reviews with a verdict.
- A blocker is satisfied once it's In Review (its code is merged) or Done.
- Linear states: Ready / In Progress / In Review / Blocked, with default names that can be overridden. Done and Canceled are recognised by state type. Only humans move tickets to Done. Backlog/Todo pause, Canceled abandons.
- Agents run unattended in `dontask` mode: an allowlist confines file tools to the checkout and temp directory, and Claude Code's OS-level sandbox confines Bash and blocks `~/.gosha`. Without a sandbox, Gosha refuses to run agents unless explicitly opted out. (Changed after M0: bypass let file tools write outside the sandbox.) The global `agent.sandbox` switch picks both the sandbox and the mode: off means `auto` mode; there's no per-project mode and no bypass. (M7)
- Linear identity: an OAuth app using the `client_credentials` grant; no personal key, no browser flow. (Changed after M0.)
- Feedback model: review, CI failures and conflicts all go through the address step, limited by `review.max_rounds` and `checks.max_rounds`. The two limits stay separate (a single shared budget was considered and rejected in M7): checks always come last, so a shared one would let a long review loop use up the rounds CI needs, and the two loops fail differently (review: no agreement; CI: a failure the agent can't fix). `agent.timeout` already caps the total cost. (M7)
- Notion mirror: page tree → file tree (`README.md` for pages with children, `INDEX.md` map). Databases aren't mirrored for now.
- Host-agnostic: macOS and Linux. Gosha runs as an existing regular user (the installing user by default) and refuses root; it never creates OS accounts. `gosha service install|uninstall|start|stop|restart|status` manages the systemd or launchd unit.
- Module path: `github.com/galimru/gosha`.
- Prompts are built in; a repo adds instructions with `.gosha/instructions.md`, appended to every prompt. (M7)
- Branches are always `gosha/<ID>-<slug>`, with no pattern to configure; they're tracked through `gosha.db`. (M7)
- GitHub through its API from Go (go-github); git through the `git` CLI; no `gh` dependency.
- Secrets are read by fixed key names from `projects/<project>/secrets.env`, then `secrets.env`, then the environment. Config files hold no secrets and no references to them. (M7)
- All project config lives in the repo's `.gosha/`. The global config is machine-level only; projects are discovered from `~/.gosha/projects/`.
- A single home directory `~/.gosha`, with a `projects/<name>/` folder per project.
- Project config is read from `origin/<default>` in a bare clone, with no local copy. An invalid or missing config pauses the project (fail closed).
- The config is read once per pass, not snapshotted per pickup (considered and rejected in M7): a change on the default branch can't affect a pass in progress, a run continuing in a later pass picks up a fixed config, which is what you want, and a snapshot would mean storing each run's config for no real gain.
- Telegram is for alerts only (paused, Blocked, delivered, usage limit, crash, daily digest), posted to a private channel. Linear only gets the PR attachment plus the Blocked and Delivered comments. (M7; until then, Telegram was a detailed activity feed.)
- Daily digest at 09:00 (configurable, can be disabled), as a summary. Liveness is an optional heartbeat URL ping instead. (M7)
- Ticket checkouts are self-contained local clones, never linked worktrees, and Gosha never runs git inside one after an agent has used it: the work is collected by fetching from the bare clone. (M2)
- Sandbox layout: agents only work in `~/.gosha/workspaces/`. The OS sandbox denies `~/.gosha` and re-allows the ticket's workspace; permission rules deny every other top-level entry, because a permission deny beats any allow. (M2, simplified in M7)
- Gosha runs no repo-defined commands outside the sandbox: there's no `agent.setup`, and agents install dependencies themselves. (M7)
- Per-project Claude config dir (`projects/<project>/claude/`); prompts go to claude on stdin. (M2)
- The reviewer runs in a fresh clone of the pushed head each round, so nothing it does reaches the implementer's checkout or the branch. It's read-only: it reads the diff with `git diff`, and CI runs the tests. (M7) Each round is a new session, but it's shown the last round's findings and the implementer's reply. (M3)
- Address passes reply on the PR with the implementer's summary. (M3) Human PR comments aren't read: Linear is the only channel for instructions. (M7)
- `agent.timeout` is one budget per pickup, shared by every agent pass in it. (M3) It counts agent passes only, not waiting for CI. (M4)
- Checks are waited for in-line; the ticket stays the project's active ticket meanwhile. A run is parked only while Blocked. (M4)
- A fix for failed checks or conflicts goes back through review (a re-review that only takes a review round if it requests changes), then checks. (M4)
- The base is merged into the branch by GitHub's update-branch API, so Gosha needs no particular git version (M7; in M4 it used `git merge-tree`, which needs git ≥ 2.38). Conflicts are resolved by the implementer in a fresh checkout and count as a check round. (M4)
- Merges are pinned to the checked head; a push by anyone else after the checks blocks the ticket instead of being merged. (M4)
- The Notion mirror is refreshed only right before a pickup (M7; M5 also refreshed every 15 minutes when idle). Search detects changes; the tree comes from each page's child page blocks; only changed pages are fetched, unless the tree changed, in which case every page is (M7). (M5)
- Notion without access to the root page pauses the project; any other Notion failure uses the last mirror. Each pickup gets its own frozen copy in the ticket's workspace. (M5)
- A project resumes only after the config, Linear and Notion checks all pass, so a pause for one reason isn't cleared by another check passing. (M5)
- Pages linked from tickets but outside the tree (`_linked/`) are deferred. (M5)
- A ticket whose last PR was merged (by Gosha or by hand) is a follow-up when it's Ready again: a new branch, and the implementer is told about the earlier PR. A follow-up with no new human comment goes Blocked without running an agent. (M6)
- The deploy is found by the merge commit's `head_sha` on the base. (M6) A failed deploy is reported (❌) in the Delivered comment rather than blocking the ticket; a follow-up fixes it. (M7)
- The Delivered comment's contents are recorded on the run as they're gathered. The implementer result has an optional `left_out` list. (M6) The comment holds only the summary, PR link, assumptions, left out and deploy line; the rest is on the PR. (M7)
- Ticket comments written during a pickup reach its next implementer pass. (M6) There's no `/restart` command: closing the PR on GitHub and moving the ticket to Ready starts over. (M7)
- `gosha validate` is an offline check of a repo's config, so it can run in the project's CI; `gosha doctor` checks everything about the host, including credentials, Linear and Notion. (M7)
- Without `checks.required`, whatever checks appear within `start_grace` must pass, and none appearing is a pass; Gosha doesn't parse workflow triggers. (M7)
