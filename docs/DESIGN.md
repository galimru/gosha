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
- **Telegram is Gosha's activity feed** (read-only for the human): pickup, implementation, review rounds, checks, merge, deploy, usage limits, errors. It's detailed, and it's where to watch Gosha work.
- **Gosha acts as itself, not as the human.** It uses its own GitHub App identity (and a Linear app identity if available), and never a person's credentials.
- **Unattended.** Gosha runs as a background service on any always-on Unix-like host (macOS or Linux): a desktop, a home server or a VPS. Full detail is in the local logs.
- **Host-agnostic.** Nothing in Gosha assumes a particular machine, OS, package manager or service manager.
- **Read-only Notion.** Notion is the human's space. Gosha only mirrors it.
- **Per-project tuning lives in the project repo** (config + prompt templates).

## 2. Human workflow

| Human action (Linear) | Gosha's response |
|---|---|
| Move ticket to **Ready** | Picks it up, moves it to In Progress, delivers, moves it to In Review |
| Read the result comment on a ticket **In Review**, check the deploy, move it to **Done** | Nothing. Done is human-only. |
| In Review → comment what's wrong → **Ready** | Opens a **follow-up PR** on the same ticket, using the comment as instructions |
| Ticket **Blocked** → comment an answer → **Ready** | Continues on the existing branch/PR with the answer |
| Comment `/restart` → **Ready** | Closes the open PR, deletes the branch, starts from scratch |
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
- **Delivered comment** (moving to In Review):
   - a 2–4 line summary of what changed and why (the implementer's first summary)
   - the PR link and diff size
   - how it was checked: what the implementer ran, each review round with its findings (and that they were addressed), check and conflict fix rounds, the checks that passed
   - deployment: workflow result + URL, if configured
   - assumptions made, and anything deliberately left out (the implementer's `assumptions` and `left_out`, from every pass)

  What it reports is recorded on the run as the pipeline goes, so a delivery that resumes after a restart still has all of it.

Everything else (pickup, progress, review rounds, usage limits, errors) goes to Telegram (§9).

## 4. Architecture

A single Go binary with a single long-running daemon that serves every project on the machine.

```
gosha daemon
 ├── scheduler: global agent-slot semaphore (default 1)
 ├── project supervisor "nextdesk"  (goroutine, panic-isolated)
 │     ├── linear poller        (every 30s)
 │     ├── context refresher    (every 15m + before each ticket)
 │     └── ticket pipeline      (at most 1 active ticket per project)
 ├── project supervisor "avisodesk"
 ├── notifier: async Telegram queue (never blocks the pipeline)
 └── store (SQLite): runs, steps, round counters, session ids, cursors
```

Why one daemon rather than one per project: Claude subscription limits and the host's CPU/RAM are shared by all projects. A single scheduler can enforce a global limit on agent runs, and there is one service and one log. If one project is broken (bad config, failing API), only that project's supervisor is affected.

### Packages

```
cmd/gosha/            CLI entrypoint (all commands in §11.5)
internal/config/      global + project config loading and validation
internal/linear/      GraphQL client + app OAuth: poll, transition, comment, attachment, relations
internal/notion/      API client, block → markdown, incremental page-tree mirror (§6)
internal/github/      GitHub REST API via go-github + ghinstallation: App auth, PRs, reviews, comments, check runs, workflow runs, merge
internal/notify/      event types, Telegram sender, rate limiting, retries
internal/gitx/        bare clone, fetch, ticket checkouts, merge, push (shells out to the git CLI; token injected per command, hooks off)
internal/agent/       claude CLI runner, prompt rendering, result parsing
internal/tmpl/        prompt template data model + functions (§5.3)
internal/pipeline/    per-ticket state machine, limits, recovery
internal/store/       SQLite persistence
internal/daemon/      supervisors, scheduler, graceful shutdown
```

**Tooling choices:**
- **No `gh` CLI.** GitHub is accessed through its API from Go (`google/go-github`, with `bradleyfalzon/ghinstallation` for GitHub App tokens). That gives typed responses, automatic token refresh, conditional requests to save rate limit, and no command output to parse.
- **The `git` CLI rather than go-git.** It has full merge and conflict support. The App token is passed in for each command and never stored.
- **Host dependencies stay at two:** `git` (2.38 or newer, for `merge-tree --write-tree`) and `claude`. On Linux, Claude Code's sandbox also needs `bubblewrap` and `socat` (not yet verified, see [SPIKES.md](SPIKES.md#open-items)); `gosha doctor` checks for them.

## 5. Configuration

Configuration is split cleanly in two:
- **Global** (machine-level, shared by every project): where Gosha's files live, how many agents may run at once, and Gosha's own identities (GitHub App, Claude, Telegram).
- **Project** (everything about one project): lives in that project's repo at `.gosha/config.yaml`. The global config contains **no project settings and no project list**.

### 5.1 Global: `~/.gosha/config.yaml`

```yaml
concurrency: 1                 # max simultaneous agent runs across all projects

agent:
  sandbox: true                # default; false = explicit opt-out, see §7.1
  writable: []                 # extra paths agents may write (toolchain caches beyond the defaults, §7.1)

github_app:                    # one app, installed on every project repo
  app_id: 123456
  private_key_file: github-app.pem   # relative to ~/.gosha; chmod 600

telegram:
  chat_id: -1001234567890      # private channel; the bot is an admin with "Post messages"
  digest_at: "09:00"           # daily digest, local time; empty disables it
```

**Project discovery:** the projects Gosha manages are the directories under `~/.gosha/projects/` that contain a `repo/` clone. There's no separate list to keep in sync.
- `gosha add <repo-url>` makes a bare clone into `projects/<name>/repo` and validates its `.gosha/config.yaml`. The URL is needed only here; afterwards the clone's `origin` remote records it.
- `gosha remove <name>` deletes `projects/<name>/` entirely.

The project name comes from the repo's config.

Secrets aren't in any config file; they're read from `secrets.env` by fixed key names (§5.4).

### 5.2 Per project: `<repo>/.gosha/config.yaml`

**Read from the default branch, never from a checkout.** There's no local copy. On every poll, Gosha runs `git fetch origin` in the project's bare clone, then reads the file with `git show origin/<default>:.gosha/config.yaml`; prompt templates are read the same way. (A bare clone has no `origin/*` refs by default, so `gosha add` sets `remote.origin.fetch` to `+refs/heads/*:refs/remotes/origin/*`.) Agents therefore can't change their own rules, and a config change takes effect on the next poll after it reaches the default branch, with no restart.

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
  setup: ["make deps"]         # optional commands run in a fresh checkout (outside the sandbox, §12)
  permission_mode: dontask     # dontask (default) | auto | bypass; see §7.1
```

### 5.3 Prompt templates: `<repo>/.gosha/prompts/`

| File | Role | Notes |
|---|---|---|
| `implement.md` | implementer, first pass | |
| `address.md` | implementer, feedback pass | review comments, CI failures, conflicts, human comments |
| `review.md` | reviewer | fresh context every round |

The binary embeds default templates, and any file present in the repo overrides its default.

**Template data model.** Every prompt uses the same Go `text/template` data and functions, so a field means the same thing everywhere.

| Field | Example | Available in |
|---|---|---|
| `.Project` | `nextdesk` | all |
| `.Ticket.ID` | `NXT-12` | all |
| `.Ticket.Title` | `Add login` | all |
| `.Ticket.Description` | markdown | prompts |
| `.Ticket.URL` | Linear link | prompts |
| `.Ticket.Comments` | human comments since the last run | prompts |
| `.Context.Dir` / `.Context.Index` | path to this pickup's frozen copy of the mirror (§6.2) / contents of its `INDEX.md` | prompts |
| `.Branch` / `.BaseBranch` | `gosha/NXT-12-add-login` / `main` | prompts |
| `.Diff` | diff against the base (cut at 100 KB) | review |
| `.Feedback.Review` / `.Feedback.CI` | review comments and human PR feedback / failed checks with the failing step of each job log | address |
| `.Feedback.Conflicts` | files that conflict with the base, as a list | address |
| `.Feedback.Previous` | the last round's findings and the implementer's reply | review (from round 2) |
| `.FollowUp.PR` / `.URL` / `.MergeSHA` / `.Summary` / `.Deploy` | the ticket's earlier, merged PR: number, link, merge commit, what it did, how its deploy went; empty unless this is a follow-up | implement |
| `.Round` | `2` | review, address |
| `.ResultFile` | path for the result JSON | prompts |

Functions:
- `slug`: lowercase, non-alphanumerics → `-`, trimmed, at most 40 characters
- `lower` / `upper`
- `trunc N`

### 5.4 Secrets

Config files never contain secrets, not even references to them. Gosha reads each secret by a fixed key name:

| Key | Used for |
|---|---|
| `LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET` | Gosha's Linear OAuth app (§8.6) |
| `NOTION_TOKEN` | the Notion integration (§6.3) |
| `CLAUDE_CODE_OAUTH_TOKEN` | agents, from `claude setup-token` (§7) |
| `TELEGRAM_BOT_TOKEN` | the Telegram feed (§9) |

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
├── .cache/                     # each page's rendered body, links still as page ids (Gosha's bookkeeping)
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
- **When:** before every pickup, and every 15 minutes while the project is idle (on every poll while it's paused, so a fixed share is noticed).
- **Incremental:** one paginated search lists every page shared with the integration with its `last_edited_time`. A page's content is fetched again only when that changed, when the page is new, or when it was fetched within two minutes of its last edit (Notion rounds `last_edited_time` to the minute, so a later edit in the same minute wouldn't show).
- **The tree comes from the pages themselves:** a page's child pages are the child page blocks in its content, kept in the manifest in Notion's order. Search only says what changed, so a page search hasn't indexed yet is still mirrored (it's fetched directly).
- **Renames and moves:** `.manifest.json` maps page id → path. Page bodies are cached in `.cache/` with links to other pages kept as page ids, and resolved to relative paths whenever files are written. When a page is renamed or moved in Notion, its file moves and every link to it is rewritten, without fetching the linking pages again. Pages deleted in Notion are deleted from the mirror; links to them, and to pages outside the tree, point at Notion.
- **Blocks → markdown:** paragraphs, headings, lists, to-dos, code, quotes, callouts, toggles (expanded), tables, dividers, bookmarks and links, and mentions. Images and files become a link plus a caption. Anything unsupported becomes `<!-- unsupported: type -->`.
- **Databases aren't mirrored** (for now). A child database appears as `<!-- database "<title>" not mirrored -->` at its position in the parent page. Database support can be added later without changing the tree layout (a database would become a directory, one file per row).
- **Rate limit:** respects Notion's limit (~3 req/s); rate limits and server errors are retried with backoff.
- **Consistency:** before each pickup, a refresh runs and a copy of the mirror (without `.cache/` and the manifest) is frozen into `runs/<ticket>/context/` for that pickup, so its agents see consistent data. A lock (`projects/<project>/context.lock`) keeps a refresh and a freeze, from the daemon or the CLI, from interleaving.
- **Failures:**
  - **No access** (bad token, root page not shared or deleted): a setup problem. The project is **paused**, the same fail-closed way as an invalid config (§5.2), until a refresh succeeds.
  - **Anything else** (Notion down, network, rate limits that don't clear): the last mirror is used and a warning is logged. Only if no mirror was ever built does the project wait and retry without picking anything up.

### 6.3 Access
- **Read-only:** through a Notion internal integration with only the **Read content** capability, shared with the root page.
- **Agent access:** the agent reads the pickup's frozen copy of the mirror through `--add-dir`. The sandbox (§7.1) makes it read-only, and since it's a per-run copy, nothing can change the mirror itself. It's never committed to the repo.

## 7. Agents

Both roles run the `claude` CLI in headless mode on the **Claude subscription**. Authentication uses a long-lived subscription token from `claude setup-token`, passed as `CLAUDE_CODE_OAUTH_TOKEN`. This doesn't depend on an interactive login or an OS keychain, so it works the same under any service manager and any OS user. Gosha removes `ANTHROPIC_API_KEY` from the agent's environment so nothing is ever billed to the API. (`--bare` can't be used: it ignores OAuth tokens.)

Every run uses `--output-format stream-json --verbose`; the stream is saved (redacted) as the run's transcript and read for the session id, the result and usage-limit events (§8.11). The prompt goes in on stdin, never in argv, because ticket text is unbounded and argv is visible to every user in `ps`. Each project has its own Claude settings directory, `projects/<project>/claude/` (§11.3): sessions persist there so `--resume` works across passes, and one project's sessions never show up in another's runs.

| | Implementer | Reviewer |
|---|---|---|
| Working dir | ticket checkout (read/write) | a fresh throwaway clone of the pushed head, `worktrees/<ticket>-review`, removed after the round |
| Context | ticket + human comments + project context + feedback | ticket + project context + diff vs base + the last round's findings and reply |
| Session | resumes its own session across address passes when possible | **always fresh** |
| Tools | all tools (edit, write, shell, local git) | edit/write tools removed; read, search, shell for running tests |
| Output | commits in the checkout + result JSON | result JSON |

**Agents never get GitHub or Linear credentials,** neither in their environment nor on disk they can reach (§7.1). The implementer commits locally, and **Gosha** pushes, opens PRs, posts comments and merges.

### 7.1 Permissions & sandbox
Agents must run **unattended**: nothing may wait for an approval prompt, and nothing a task legitimately needs (`pnpm build`, a codegen script, `ls`) may be silently denied.

Two layers do this, and both are needed (M0 showed the OS sandbox alone isn't enough, see [SPIKES.md](SPIKES.md#1c-sandbox-partial--fixed-with-dontask)):
- the **OS sandbox** confines Bash and every process it starts;
- **permission rules** confine Claude's own file tools (Read, Write, Edit), which run outside the OS sandbox.

Gosha generates both per run into a settings file passed with `--settings`. The settings file lives next to the transcript, outside anything the agent can write.

The two layers need different shapes, because they resolve conflicts differently (checked on macOS, see [SPIKES.md](SPIKES.md#1i-sandbox-paths-inside-gosha-m2)):
- **OS sandbox:** `denyRead` covers all of `~/.gosha`, and `allowRead`/`allowWrite` re-allow the run's checkout and temp directory inside it. A more specific allow wins here.
- **Permission rules:** a deny always beats an allow, so `Read(~/.gosha/**)` would also hide the checkout from the Read tool. Gosha instead denies every entry of `~/.gosha` *around* the run's paths (other projects, `secrets.env`, the database, the bare clone, other tickets' checkouts, transcripts…). These entries are listed when the run starts, with no globs.

- **Permission mode** (`agent.permission_mode`):
  - `dontask` (default): `--permission-mode dontAsk --permission-prompts none`. Only what the generated allow rules list is permitted; everything else is denied without a prompt. Fully unattended. The allow rules are: `Read`, `Bash` (confined by the sandbox), web fetch/search, and `Edit` inside the checkout and the run's temp directory only.
  - `auto`: `--permission-mode auto --permission-prompts none`. Claude's classifier approves or blocks each action. Works headless; in M0 it blocked no legitimate step.
  - `bypass`: `--dangerously-skip-permissions`. Everything not explicitly denied is allowed, so the Write and Edit tools can write anywhere the service user can. Explicit opt-in only; `gosha doctor` warns about it.
- **Deny rules** apply in every mode: no `Read` or `Edit` anywhere in `~/.gosha` except the run's checkout and temp directory, and no `Edit` in the context copy.
- **Reviewer:** its edit and write tools are *removed* (`--disallowedTools Edit,Write,NotebookEdit`), which holds in any mode, and it gets no `Edit` allow rules, so it can read and run tests but not change code. Its Bash can still write in its own clone (tests need that), which is why it runs in a throwaway clone and never in the implementer's checkout.
- **Sandbox:** Gosha enables **Claude Code's built-in sandbox** (OS-level: Seatbelt on macOS, bubblewrap on Linux) for every agent run, with `failIfUnavailable: true` and `allowUnsandboxedCommands: false` (the agent can't ask to run a command outside it):
  - **Writable:** the ticket's checkout, the run's temp directory, and toolchain caches. The default cache list depends on the OS (e.g. `~/.cache`, `~/go`, `~/.npm`, and `~/Library/Caches` on macOS), plus `agent.writable` from the global config. The sandbox can't create the missing parent of an allowed directory, so Gosha creates every writable directory before the run.
  - **Read-only:** the run's frozen context copy.
  - **No access:** `~/.gosha`, i.e. secrets, the GitHub App key, other projects and the database.
  - **Network:** allowed (dependency installs need it).
- **If the sandbox can't be enforced** on a host (unsupported OS setup, missing Linux packages), Claude refuses to start (`failIfUnavailable`) and Gosha **refuses to start agents** unless the operator opts in with `agent.sandbox: false` in the global config. `gosha doctor` warns that agents can then read Gosha's secrets, and that a separate OS user (`--user`, §11.2) is the remaining protection.
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
| Humans on the PR | PR review comments | (counted with review rounds) |
| CI checks | failed check names + tail of failing job logs | `checks.max_rounds` |
| Base branch | merge conflicts | (counted with check rounds) |

All of them go through the same **address** step (`address.md`, data in `.Feedback.*`). Only how feedback is *collected* and how each source is *limited* differs. Every limit is reset when a human moves the ticket back to Ready.

### 8.1 Pickup
1. The poller lists Ready issues in the team.
2. It skips any issue whose "blocked by" issues are not yet **In Review or Done**. (In Review means the code is already merged, so it's safe to build on.)
3. It orders the rest by priority (Urgent first, Linear's "No priority" last), then by creation time, and takes one if the project is idle.
4. Claim: move the ticket to In Progress, record the run, and notify Telegram.
5. Decide the mode:
   - `/restart` in the new comments → close the PR, delete the branch → **fresh**
   - an open Gosha PR exists → **resume**: the round limit resets; new ticket comments or new human PR feedback go to an **address** pass first, otherwise the PR gets a fresh review (§8.3)
   - the ticket's last PR was merged, by Gosha or by hand → **follow-up**: a fresh run on a new branch with suffix `-2`, `-3`, …, whose implementer is told about the earlier PR (`.FollowUp`) and that the new comments say what to change. A follow-up with no new human comment goes **Blocked** at once, asking what to change; no agent runs.
   - a run Blocked after its merge (the deploy failed): with new comments → finish that run and start a **follow-up**; without → look at the deploy again (§8.7)
   - otherwise → **fresh**

   `/restart` lines are commands to Gosha: they're left out of the comments agents get. Ticket comments posted while Gosha works on the ticket are added to the instructions of the next implementer pass in the same pickup.

### 8.2 Implement
1. Refresh the project context and freeze a copy for the run (§6.2).
2. Fetch, then create the ticket checkout `worktrees/<ticket>/` from `origin/<base>` and run `agent.setup` in it (outside the sandbox, with the agent's scrubbed environment; see §12).
   - **The checkout is a self-contained local clone, not a linked `git worktree`.** The agent can write to everything in it, including `.git/`. A linked worktree would share the bare clone's refs, hooks and config, including `origin/<default>` (where config is read from) and the hooks Gosha's own push would run with the token in its environment.
   - **Gosha never runs git inside the checkout once an agent has used it.** It collects the work with `git fetch <checkout>` *from the bare clone*, which runs only upload-pack in the checkout, and then counts commits, measures the diff and pushes from the bare clone. Every Gosha git command also runs with hooks disabled.
   - A new run always starts from a fresh checkout. Later passes of the same run (resume) reuse it, and a lost checkout is rebuilt from the work Gosha already collected.
3. Run the implementer with `implement.md`.
4. If the result is `blocked`, go to **Blocked**.
5. If there are no commits, go to Blocked ("no changes produced").
6. Push and open the PR as the GitHub App: title `NXT-12: <title>`, body = summary + verification + assumptions + ticket link. Add the PR as a Linear attachment.

**Comment cursor:** the ticket's comment cursor moves past the new human comments only once the implementer has actually been given them, so an interrupted pass (usage limit, shutdown) doesn't lose instructions.

**Branch naming.** Always `gosha/<ID>-<slug of the title>`, e.g. `gosha/NXT-12-add-login` (`slug`, §5.3). The ticket ID lets Linear's GitHub integration link the branch.
- **The name is used only when the branch is created.** Gosha stores the branch and PR number in `gosha.db`, and every later step uses that record, never a lookup by name.
- **Suffixes:** follow-up runs on the same ticket, and names that already exist on the remote without being Gosha's, get `-2`, `-3`, … appended. Gosha never overwrites a branch it doesn't own.

### 8.3 Review loop (at most `review.max_rounds`)
1. Run the reviewer on the branch head, in a fresh clone (§7), with `agent.setup` run first. From round 2 its prompt includes the last round's findings and the implementer's reply, so it checks they were resolved instead of starting over.
2. Post the result as a GitHub PR review on that head SHA, with inline comments. The review event is `COMMENT`, with a ✅ or ❌ verdict in the body (see §8.6). Comments GitHub can't anchor (a line outside the diff, a whole file) go into the review body, so one bad line number never fails the review.
3. Collect **human PR feedback** since the run's PR cursor: conversation comments, inline comments and review bodies, from anyone who isn't a bot.
4. `approve`, with no `must` comments and no new human PR feedback → go to **checks**.
5. If this was round `review.max_rounds` → **Blocked**, listing the unresolved `must` comments and human feedback.
6. Otherwise run the implementer with `address.md`, resuming its session in the ticket checkout, and passing the reviewer's comments, the human PR feedback and new human ticket comments. Push if it committed, and reply on the PR with its summary (a reply with no code changes is allowed: the next reviewer sees why). The PR cursor moves past the feedback it was given. Start the next round.

Steps are recorded per round (`review` → `address` → `review` …), with the reviewed SHA and the posted review id, so a run that stops mid-round doesn't run or post the same review twice.

### 8.4 Checks (there's no branch protection, so Gosha decides)
Gosha waits for checks in-line: the ticket stays the project's active ticket while CI runs (up to `checks.timeout`). Waiting doesn't spend `agent.timeout`, which counts only agent passes and `agent.setup`.

For the PR's **head SHA**:
1. **Expected checks:**
   - if `checks.required` is set → those names
   - otherwise, if `.github/workflows/` on the base has workflows that trigger on `pull_request`, `pull_request_target` or `push` → "at least one check must appear"
   - otherwise → no checks, pass immediately
2. **Wait for checks to start:** poll check runs and commit statuses (the newest status per context) every ~15s. An expected check that hasn't appeared within `start_grace` counts as a failure. If the only failures are checks that never started, there's nothing in the code to fix, so the ticket goes **Blocked** at once, naming them.
3. **Wait for checks to finish:** wait until all are completed, then wait another ~30s for late-starting checks before deciding. A completed check passes on `success`, `neutral` or `skipped`. Checks still running at `checks.timeout` count as failed.
4. **Any failure, cancellation or timeout** starts a check round: fetch the failed jobs' logs and cut out the failing step's section (from its `##[group]Run …` line to the first `##[error]`; a job log ends with post-job cleanup, so a plain tail misses the error), then run the implementer with `address.md` (the **fix** step). The round and its feedback are recorded against the failing head, so an interrupted fix continues with the same feedback instead of counting another round. After `checks.max_rounds` rounds → **Blocked**.
5. **After a fix:** if it committed, push, reply on the PR, and go back to review: a **re-review** of the fix. It only takes a `review.max_rounds` slot if it requests changes. Then checks run again on the new head. A fix that commits nothing goes **Blocked** with the implementer's explanation (e.g. a flaky test or an infrastructure problem).

### 8.5 Merge
1. **New human PR feedback** since the run's PR cursor goes to an address pass and back through review and checks first. A human's "wait" on the PR is never merged over.
2. **Base sync:** fetch, and if `origin/<base>` isn't an ancestor of the branch, merge it in. Gosha does this in the bare clone with `git merge-tree --write-tree` and `git commit-tree` (as the bot identity), so it still never runs git in an agent's checkout. A clean merge is pushed and goes back to checks (no review: the PR's own change is the same). The ticket checkout is removed so the next agent pass starts from the merge commit.
3. **Conflicts** are a check round: Gosha rebuilds the ticket checkout from the branch (a fresh clone, which carries the latest base), and the implementer runs `git merge origin/<base>`, resolves the conflicted files and commits the merge. Gosha checks that the base is now an ancestor of the branch (otherwise **Blocked**), pushes, and the change goes through re-review and checks.
4. **Merge** through the API with `github.merge_method` (squash by default), title `NXT-12: <title> (#<n>)`, **with `sha` = the head commit that was checked**. If anything was pushed after the checks ran, GitHub refuses the merge. Gosha only pushes after a check verdict through the steps above, so that push was someone else's: the ticket goes **Blocked** rather than merging code nobody checked.
5. The merge commit is recorded at once, so a run that stops after merging finishes with cleanup and delivery on the next pass instead of being mistaken for a merge by hand.
6. Delete the branch and remove the checkout, then watch the deploy (§8.7) or deliver (§8.8).

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
- Success → report the result + URL in the Delivered comment.
- Failure → **Blocked** with the failed jobs and the failing step of each job's log (as for checks, §8.4). The code is already merged, so a human needs to see this.
- No run within `deploy.timeout`, or still running → **Blocked**, naming the workflow and the merge commit.
- **Ready again:** with no new comment, Gosha looks at the deploy again (a human may have fixed or re-run it): success → deliver, still failing → Blocked again. With a comment, it's a follow-up PR on the merged code (§8.1), and the implementer is shown the failure.
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

## 9. Telegram activity feed

A Telegram bot posts Gosha's activity to a **private channel**. All projects share the channel, and every message starts with the project name.

**Events:**

| Event | Example |
|---|---|
| daemon started / stopped | `gosha v0.3.0 started · 2 projects` |
| project error / paused | `nextdesk · paused at a1b2c3d: .gosha/config.yaml: linear.team: required` (config, Linear team/states, Notion access) |
| project recovered | `nextdesk · checks pass again at a1b2c3d · resumed` |
| picked up | `nextdesk · NXT-12 Add login · picked up (fresh)` |
| context refreshed | `nextdesk · context · 1 added, 3 updated` (only when something changed) |
| implementation done | `NXT-12 · implemented · +210 −35 in 7 files · 14m` + summary |
| PR opened | link |
| review round N | `NXT-12 · review 1/3 ❌ · 2 must, 3 should` |
| address pass | `NXT-12 · addressed 5 comments` |
| checks | `NXT-12 · checks ✅ 4/4` / `❌ test (go 1.27)` → fixing 1/3 |
| merged | `NXT-12 · squash-merged a1b2c3d` |
| deploy | `NXT-12 · deploy ✅ https://…` / `deploy ❌ failure` |
| delivered / blocked | the outcome + a link to the Linear ticket |
| usage limit | `⏸ Claude usage limit, resuming ~14:30` / `▶️ resumed` |
| paused / canceled by human | `NXT-12 · moved to Backlog, stopped` |
| daily digest | `09:00 · yesterday: 4 delivered, 1 blocked · queue: 3 Ready` |

**Rules:**
- Events go through an async queue, so a Telegram outage never blocks or fails delivery. Sending retries with backoff, respects Telegram rate limits, and drops events after ~1h.
- Messages use HTML formatting, with links to the Linear ticket and PR.
- Messages are kept short for phone reading, and long agent summaries are truncated.
- **Daily digest:** Gosha can't report that it has died, so if the digest doesn't arrive, check the host.
- Telegram is **output only**. Gosha takes instructions only from Linear.

## 10. Persistence & recovery

SQLite at `~/.gosha/gosha.db` stores runs (project, ticket, mode, PR, branch, current step, round counters, session ids, timestamps) and the last-seen comment id per ticket.

On startup, for each ticket that is In Progress in Linear **and** has an open run in the db, Gosha re-enters the pipeline at its current step. Steps are idempotent, so they reconcile with the real state of the PR and checks. A ticket that is In Progress with no run record is left untouched (a human moved it there).

## 11. Host & operations

### 11.1 Supported hosts
- **macOS and Linux**, on amd64 and arm64. Gosha is a single static Go binary. Linux is best effort until the agent sandbox is verified there ([SPIKES.md](SPIKES.md#open-items)).
- **External tools it needs:** `git` and the `claude` CLI on `PATH` (on Linux also `bubblewrap` and `socat` for the agent sandbox). Gosha checks them at startup (`gosha doctor`).
- **Windows:** not a target. WSL works as Linux.
- **Host decisions belong to the operator:** keeping the machine awake, restarting after a power failure, disk encryption, remote access. Gosha doesn't touch them. If Gosha has been restarted, it resumes runs (§10).

### 11.2 Which OS user runs Gosha
Gosha runs as an **existing regular user**, normally the one who installs it. Gosha never creates OS accounts. **Root isn't allowed.**

- **Default:** `gosha service install` installs the service for the invoking user (`$SUDO_USER` when run through `sudo`). `--user <name>` picks another existing account.
- **Root is refused:** if the target user would be root, `gosha daemon`, `gosha run` and `gosha service install` all fail, suggesting a regular user be created first (e.g. `adduser deploy` on a VPS). Agents run unattended (§7.1) and execute commands derived from ticket text. As root, those commands would own the machine, which is also why Claude Code refuses bypass mode as root.
- **Want more isolation?** Create a separate regular account yourself and install with `--user <it>`. It's an ordinary account, so nothing special is needed from Gosha.
- **Nothing tied to a person's login:** GitHub goes through the App, Claude through the setup token, everything else through the secrets files. Gosha never uses the user's `gh` login, SSH keys, git identity or OS keychain, so moving Gosha to another user or host is just moving `~/.gosha`.
- **Claude:** the `claude` CLI must be on the service user's `PATH`. The token from `claude setup-token` can be generated anywhere, because it's tied to the Claude subscription, not the OS user.
- **Toolchains:** whatever the agent's tasks need (go, node, make…) must be on the service's `PATH`. `service install` captures it from the user's login shell.
- **Agent environment:** agents run `claude` with Gosha's own settings directory (`CLAUDE_CONFIG_DIR`), so the user's personal `~/.claude` (CLAUDE.md, hooks, MCP servers) doesn't apply to agent runs. The repo's own `CLAUDE.md` still does.

### 11.3 Paths
Everything lives in **one directory**, `~/.gosha` of the running user. `--home` or `GOSHA_HOME` overrides it. Global items sit at the root, and everything about one project sits under `projects/<name>/`.

```
~/.gosha/                                    # chmod 700
├── config.yaml                              # global config (§5.1)
├── secrets.env                              # shared secrets, chmod 600
├── github-app.pem                           # chmod 600
├── gosha.db                                 # runs/state for all projects
├── logs/gosha.log                           # structured, rotated; every line tagged with project
└── projects/
    └── <project>/
        ├── secrets.env                      # optional overrides, chmod 600
        ├── repo/                            # bare clone (presence = project registered)
        ├── worktrees/<ticket>/              # ticket checkout (a local clone), one per active ticket
        ├── worktrees/<ticket>-review/       # the reviewer's throwaway clone, only during a review
        ├── claude/                          # CLAUDE_CONFIG_DIR for this project's agents (sessions)
        ├── context/                         # Notion mirror (INDEX.md + page tree)
        ├── context.lock                     # serialises refreshes and freezes
        └── runs/<ticket>/
            ├── context/                     # this pickup's frozen copy of the mirror (read-only to agents)
            ├── <n>/                         # pass n: the agent's TMPDIR and result file
            ├── <n>-<role>.jsonl             # full agent transcript (redacted); role is implement, review or address
            ├── <n>-settings.json            # generated agent settings
            └── <n>-setup.log                # agent.setup output
```

The project's config is **not** in this tree; it's read from the repo (§5.2). `gosha config show <project>` prints the effective config (repo file + defaults).

### 11.4 Service
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
- **Re-running `install`** is safe: it replaces the unit and restarts the service, which is also how an upgrade is applied.
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

**Moving to another host:** all state lives in `~/.gosha` (§11.3). Stop the service, copy that directory to the new host (`git` and `claude` must already be available there), install the service, and start it. Runs that were in flight resume (§10). Nothing is tied to the original machine: no keychain, no hostnames, no absolute paths outside the user's home.

### 11.5 CLI
Commands act on the `~/.gosha` of the user running them, normally the same user the service runs as.

- `gosha init`: run in a local checkout; writes a starter `.gosha/config.yaml` + default prompts to commit
- `gosha add <repo-url>` / `remove` / `list`
- `gosha config show <project>`: effective config
- `gosha validate [path]`: an offline check of a repo's `.gosha/config.yaml` (it parses, fields are valid, referenced files exist). It needs no secrets or network, so it can run in the project's CI.
- `gosha doctor [project]`: everything about the host: git, claude, the sandbox tools, file permissions, the global config, the GitHub App, and for each project (or the one named) its config on the default branch, its secrets, its Linear team, states and actor (§8.6), and Notion access to its root page
- `gosha status`: projects, active or paused state (and why), the context mirror (pages, last refresh), current ticket and step, usage-limit pause
- `gosha logs [-f] [--project nextdesk]`
- `gosha context refresh <project>`: pull the latest from the context source (Notion) into the local mirror
- `gosha context path <project>`: print where the mirror lives
- `gosha context show <project>`: print `INDEX.md`, the page tree the agent starts from
- `gosha run <project> [--once] [--ticket NXT-12] [--dry-run]`: foreground loop for one project, for development and prompt tuning. `--once` handles a single ticket. `--dry-run` does a single pickup pass and reports what would happen (pickup, queue, skipped tickets and why, mode, branch) to stdout and Telegram, without changing anything in Linear or GitHub.
- `gosha secrets edit`: opens `secrets.env` in `$EDITOR`
- `gosha service install|uninstall|start|stop|restart|status`
- `gosha daemon`

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
- **Known risk: `agent.setup` runs outside the sandbox.** It runs in a fresh checkout of the default branch with the agent's scrubbed environment (no secrets in it), but it can read files under `~/.gosha`. The commands come from the default branch's config, but the code they run (a `Makefile`, `package.json` scripts) can include agent-written code once Gosha merges its own PRs (M4). Mitigations: review, CI, and a separate OS user (§11.2). Keep `agent.setup` minimal, or leave dependency installs to the agent, which runs them sandboxed.
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
- Agents run unattended in `dontask` mode by default (`auto` optional, `bypass` opt-in with a warning): an allowlist confines file tools to the checkout and temp directory, and Claude Code's OS-level sandbox confines Bash and blocks `~/.gosha`. Without a sandbox, Gosha refuses to run agents unless explicitly opted out. (Changed after M0: bypass let file tools write outside the sandbox.)
- Linear identity: an OAuth app using the `client_credentials` grant; no personal key, no browser flow. (Changed after M0.)
- Feedback model: review, human PR comments, CI failures and conflicts all go through the address step, limited by `review.max_rounds` and `checks.max_rounds`.
- Notion mirror: page tree → file tree (`README.md` for pages with children, `INDEX.md` map). Databases aren't mirrored for now.
- Host-agnostic: macOS and Linux. Gosha runs as an existing regular user (the installing user by default) and refuses root; it never creates OS accounts. `gosha service install|uninstall|start|stop|restart|status` manages the systemd or launchd unit.
- Module path: `github.com/galimru/gosha`.
- One template data model (`.Ticket.*`, `.Context.*`, …) shared by every prompt. Branches are always `gosha/<ID>-<slug>`, with no pattern to configure; they're tracked through `gosha.db`. (M7)
- GitHub through its API from Go (go-github); git through the `git` CLI; no `gh` dependency.
- Secrets are read by fixed key names from `projects/<project>/secrets.env`, then `secrets.env`, then the environment. Config files hold no secrets and no references to them. (M7)
- All project config lives in the repo's `.gosha/`. The global config is machine-level only; projects are discovered from `~/.gosha/projects/`.
- A single home directory `~/.gosha`, with a `projects/<name>/` folder per project.
- Project config is read from `origin/<default>` in a bare clone, with no local copy. An invalid or missing config pauses the project (fail closed).
- Telegram is the detailed activity feed, posted to a private channel. Linear only gets the PR attachment plus the Blocked and Delivered comments.
- Daily digest at 09:00 (configurable, can be disabled). It also serves as the sign that Gosha is still running.
- Ticket checkouts are self-contained local clones, never linked worktrees, and Gosha never runs git inside one after an agent has used it: the work is collected by fetching from the bare clone. (M2)
- Sandbox layout: the OS sandbox denies `~/.gosha` and re-allows the run's paths; permission rules deny everything around the run's paths, because a permission deny beats any allow. (M2)
- `agent.setup` is run by Gosha outside the sandbox, as a deliberate, documented risk (§12). (M2)
- Per-project Claude config dir (`projects/<project>/claude/`); prompts go to claude on stdin. (M2)
- The reviewer runs in a fresh throwaway clone of the pushed head each round, so nothing it does reaches the implementer's checkout or the branch. Each round is a new session, but it's shown the last round's findings and the implementer's reply. (M3)
- Human PR feedback (comments, inline comments, review bodies from non-bots) is collected before every review verdict from a per-run cursor, and forces an address pass. Address passes reply on the PR with the implementer's summary. (M3)
- `agent.timeout` is one budget per pickup, shared by every agent pass in it. (M3) It counts agent passes and `agent.setup` only, not waiting for CI. (M4)
- Checks are waited for in-line; the ticket stays the project's active ticket meanwhile. A run is parked only while Blocked. (M4)
- A fix for failed checks or conflicts goes back through review (a re-review that only takes a review round if it requests changes), then checks. (M4)
- The base is merged into the branch in the bare clone (`git merge-tree`, git ≥ 2.38); conflicts are resolved by the implementer in a fresh checkout and count as a check round. (M4)
- Merges are pinned to the checked head; a push by anyone else after the checks blocks the ticket instead of being merged. New human PR feedback is addressed before merging. (M4)
- The Notion mirror is refreshed before every pickup and every 15 minutes when idle. Search detects changes; the tree comes from each page's child page blocks; only changed pages are fetched. Page bodies are cached with links as page ids, so renames and moves rewrite links without re-fetching. (M5)
- Notion without access to the root page pauses the project; any other Notion failure uses the last mirror. Each pickup gets its own frozen copy at `runs/<ticket>/context/`. (M5)
- A project resumes only after the config, Linear and Notion checks all pass, so a pause for one reason isn't cleared by another check passing. (M5)
- Pages linked from tickets but outside the tree (`_linked/`) are deferred. (M5)
- A ticket whose last PR was merged (by Gosha or by hand) is a follow-up when it's Ready again: a new branch, and the implementer is told about the earlier PR. A follow-up with no new human comment goes Blocked without running an agent. (M6)
- The deploy is found by the merge commit's `head_sha` on the base. A failed deploy goes Blocked; Ready without a comment looks at the deploy again, Ready with a comment opens a follow-up. (M6)
- The Delivered comment's contents are recorded on the run as they're gathered (implementer results, each posted review, fix rounds, checks, diff, deploy). The implementer result has an optional `left_out` list. (M6)
- `/restart` lines aren't passed to agents, and ticket comments written during a pickup reach its next implementer pass. (M6)
- `gosha validate` is an offline check of a repo's config, so it can run in the project's CI; `gosha doctor` checks everything about the host, including credentials, Linear and Notion. (M7)
