# Gosha — Roadmap

Implementation order for the design in [DESIGN.md](DESIGN.md). The riskiest assumptions are tested first, and the first end-to-end result (ticket → PR) comes as early as possible.

| # | Milestone | Outcome |
|---|---|---|
| 0 | Spikes | Risky assumptions confirmed, or the design corrected |
| 1 | Skeleton | Gosha sees Ready tickets and reports what it would do |
| 2 | Ticket → PR | First real use: tickets become PRs |
| 3 | Review loop | PRs are reviewed and fixed |
| 4 | Checks & merge | Fully automatic up to merge |
| 5 | Project context | Agents work from the Notion specs |
| 6 | Deliver | Full ticket lifecycle |
| 7 | Production | Runs unattended as a service |

## M0 — Spikes

Throwaway code. The goal is answers, not features. Any "no" updates DESIGN.md before M1 starts.

**Status: done** (2026-10-18). Findings in [SPIKES.md](SPIKES.md). Changes it caused: agents default to `dontask` mode with an allowlist instead of bypass, and Linear uses the `client_credentials` grant. Linux was not verified and stays best effort (re-check before M7).

- **Claude runner** (DESIGN §7, §7.1), on macOS **and** an Ubuntu VPS, as a non-root user:
  - headless `claude -p` in bypass mode authenticates with a `claude setup-token` token
  - an isolated settings directory: the user's `~/.claude` has no effect
  - the sandbox allows writes to the worktree, temp and toolchain caches, and **blocks reads of `~/.gosha`**
  - the result file is written and parsed
  - a usage-limit error can be detected
  - `auto` mode: does it work headless, and how often does it block legitimate steps?
- **Linear app actor** (DESIGN §8.6): an OAuth app with `actor=app` can comment, change state and add attachments as "Gosha", and the headless paste-back flow works.
- **GitHub App** (DESIGN §8.6): an installation token can push over HTTPS, a push or PR triggers Actions, a push touching `.github/workflows/` is rejected without the Workflows permission, and squash-merge with `sha` works.

## M1 — Skeleton

- Global config (`~/.gosha/config.yaml`), project config read from `origin/<default>` in a bare clone, fail-closed loading
- `${VAR}` secrets: shared + per-project files, literal-secret rejection, redaction
- SQLite store
- Root refusal
- Linear client: `client_credentials` token minting, poll Ready, transitions, comments, attachments, state mapping with defaults, state-type detection
- Telegram notifier (async queue)
- CLI: `add`, `remove`, `list`, `validate`, `doctor`, `auth linear`, `secrets edit`, `status`
- Dry-run pickup: lists Ready tickets (priority, then age; blocked-by respected) and what would happen

**Done when:** `gosha run <project> --dry-run` on a real project reports correct pickups in Telegram.

## M2 — Ticket → PR

- gitx: bare clone, fetch, worktrees, push with an injected App token
- GitHub App auth and API client (go-github)
- Claude runner as validated in M0 (generated settings, `dontask` allowlist, sandbox, scrubbed env, process group), implementer prompt (`implement.md` default + repo override), result contract
- Branch name template, PR creation, Linear attachment, In Progress → Blocked on failure
- `gosha run <project>`: foreground loop for one project; `--once` for a single ticket

**Done when:** moving a ticket to Ready yields a PR from `gosha[bot]` with no manual steps.

## M3 — Review loop

- Reviewer run (edit/write tools removed), PR review with inline comments and verdict
- Address step (`address.md`) for reviewer and human PR comments, implementer session resume
- `review.max_rounds`, Blocked comment

**Done when:** a PR goes through at least one review → fix round on its own.

## M4 — Checks & merge

- Expected-check detection, start grace, wait for completion
- Failed-job log extraction, CI feedback into the address step, `checks.max_rounds`
- Base sync and conflict resolution
- Squash-merge pinned to the checked SHA, branch and worktree cleanup

**Done when:** a Ready ticket ends merged with green checks, untouched by a human.

## M5 — Project context (Notion mirror)

- Page tree → file tree, `README.md`/`INDEX.md`, front-matter, link rewriting
- Incremental refresh with renames, moves and deletes (`.manifest.json`)
- Per-run frozen copy, read-only through the sandbox
- `gosha context refresh|path|show`

**Done when:** an agent's transcript shows it reading the relevant Notion pages for a ticket.

## M6 — Deliver

- Deploy watch, Delivered comment, In Review transition
- Follow-up PRs from In Review → Ready, `/restart`
- Human ticket comments as instructions on every pickup

**Done when:** the full lifecycle in DESIGN §2 works, including follow-ups and Blocked → resume.

## M7 — Production

- Daemon: multiple projects, global scheduler, panic isolation
- Crash recovery, cancel and pause (Backlog/Todo/Canceled), usage-limit pause/resume
- `gosha service install|uninstall|start|stop|restart|status` (systemd + launchd)
- Log rotation, daily digest
- Verify the agent sandbox on Linux (bubblewrap, required packages, root refusal); see SPIKES.md open items
- Remaining CLI: `init`, `config show`, `auth status`, `logs`

**Done when:** Gosha runs as a service on a VPS for a week across two projects without intervention beyond Linear.

## After M4: build Gosha with Gosha

Once tickets can go Ready → merged, the remaining milestones are filed as Linear tickets in a `gosha` project and implemented by Gosha itself.
