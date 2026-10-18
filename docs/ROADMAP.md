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

**Status: built** (2026-10-18), pending the end-to-end check below on a real project. GitHub App auth and part of gitx moved here from M2 because `add` must clone private repos. Follow-up detection (a merged PR exists) needs the PR API and stays in M2; the M1 dry-run reports fresh, fresh (restart) or resume.

- Global config (`~/.gosha/config.yaml`), project config read from `origin/<default>` in a bare clone, fail-closed loading
- `${VAR}` secrets: shared + per-project files, literal-secret rejection, redaction
- SQLite store
- GitHub App installation tokens and a gitx subset (bare clone, fetch, `git show`) with per-command token injection, so `add` can clone private repos
- Root refusal
- Linear client: `client_credentials` token minting, poll Ready, transitions, comments, attachments, state mapping with defaults, state-type detection
- Telegram notifier (async queue)
- CLI: `add`, `remove`, `list`, `validate`, `doctor`, `auth linear`, `secrets edit`, `status`
- Dry-run pickup: lists Ready tickets (priority, then age; blocked-by respected) and what would happen

**Done when:** `gosha run <project> --dry-run` on a real project reports correct pickups in Telegram.

## M2 — Ticket → PR

**Status: built** (2026-10-18), pending the end-to-end check below on a real project. Behaviour that is specific to M2, until review, checks and merge exist:
- **After the PR opens,** the ticket stays In Progress and the run parks at step `review` for M3. A parked run doesn't keep the project busy, so the next Ready ticket is picked up.
- **Resume** (Ready with an open run) continues the implementer session in the existing checkout only when no PR exists yet. With an open PR the ticket goes Blocked ("arrives in M3"). A PR merged by hand makes the next Ready a follow-up; a PR closed unmerged restarts.
- **Interrupted runs** (usage limit, Ctrl-C) leave the ticket In Progress; the next `gosha run` continues them before picking up anything new.

Changes from the plan: the ticket "worktree" is a self-contained local clone (DESIGN §8.2), and the sandbox and permission rules take different shapes (DESIGN §7.1, SPIKES §1i).

- gitx: ticket checkouts, push with an injected App token (bare clone and fetch landed in M1)
- GitHub API client (go-github) for PRs (App auth landed in M1)
- Claude runner as validated in M0 (generated settings, `dontask` allowlist, sandbox, scrubbed env, process group), implementer prompt (`implement.md` default + repo override), result contract
- Branch name template, PR creation, Linear attachment, In Progress → Blocked on failure
- `gosha run <project>`: foreground loop for one project; `--once` for a single ticket

**Done when:** moving a ticket to Ready yields a PR from `gosha[bot]` with no manual steps.

## M3 — Review loop

**Status: built** (2026-10-18), pending the end-to-end check below on a real project. Behaviour that is specific to M3, until checks and merge exist:
- **After the reviewer approves,** the run parks at step `checks` and the ticket stays In Progress. A parked run doesn't keep the project busy, so the next Ready ticket is picked up. Runs that M2 parked at `review` are active again, so their PRs get reviewed on the first pass after the upgrade.
- **Parked runs aren't watched.** Human PR comments are collected before every review verdict, but a comment on a PR parked at `checks` waits until the ticket is next picked up (move it to Ready). (M4 no longer parks at `checks`.)

Changes from the plan:
- **The reviewer runs in a fresh throwaway clone** of the pushed head each round (`worktrees/<ticket>-review`), with `agent.setup` run in it. Its Bash can still write there, but nothing it does reaches the implementer's checkout or the branch.
- **The reviewer sees the previous round** (its findings and the implementer's reply) in a new session, so it checks the fixes instead of starting over.
- **Address replies are PR comments,** and reviewer comments on lines outside the diff go into the review body instead of failing the review.
- **`agent.timeout` is one budget per pickup,** shared by every implement, review and address pass in it.

- Reviewer run (edit/write tools removed), PR review with inline comments and verdict
- Address step (`address.md`) for reviewer and human PR comments, implementer session resume
- `review.max_rounds`, Blocked comment

**Done when:** a PR goes through at least one review → fix round on its own.

## M4 — Checks & merge

**Status: built** (2026-10-18), pending the end-to-end check below on a real project. Behaviour that is specific to M4, until deploy watch and delivery exist (M6):
- **After the merge,** Gosha posts a short Delivered comment (PR link, merge commit, diff size, the implementer's summary, review and check rounds, the checks that passed), moves the ticket to In Review and finishes the run. There's no deploy watch yet, and the full Delivered comment (DESIGN §3) comes in M6.
- **Runs that M3 parked at `checks` are active again,** so they wait for checks and merge on the first pass after the upgrade. Only Blocked runs are parked now.

Changes from the plan:
- **Checks are waited for in-line,** keeping the ticket the project's active one, and the wait doesn't spend `agent.timeout` (now agent working time only).
- **A CI fix or conflict resolution is re-reviewed** before checks run again. The re-review only takes a review round if it requests changes.
- **The base is merged in the bare clone** with `git merge-tree` (git ≥ 2.38, checked by `gosha doctor`). Conflicts are resolved by the implementer in a fresh checkout.
- **Checks that never start** (e.g. a misnamed `checks.required`) go Blocked at once instead of costing a fix round. A fix that commits nothing goes Blocked with the implementer's explanation.
- **A push by someone else** after the checks makes GitHub refuse the pinned merge, and the ticket goes Blocked rather than back to checks. New human PR feedback is addressed before merging.

- Expected-check detection, start grace, wait for completion
- Failed-job log extraction, CI feedback into the address step, `checks.max_rounds`
- Base sync and conflict resolution
- Squash-merge pinned to the checked SHA, branch and worktree cleanup

**Done when:** a Ready ticket ends merged with green checks, untouched by a human.

## M5 — Project context (Notion mirror)

**Status: built** (2026-10-18), pending the end-to-end check below on a real Notion workspace. Behaviour that is specific to M5, until the daemon exists (M7):
- **The idle refresh runs from `gosha run`'s poll loop** (every 15 minutes, and on every poll while the project is paused), not from a daemon-wide refresher.

Changes from the plan:
- **Search detects changes, but the tree comes from the pages themselves** (their child page blocks), so a page search hasn't indexed yet is still mirrored. Only changed pages are fetched; pages fetched within two minutes of their last edit are fetched once more, because Notion rounds edit times to the minute.
- **Page bodies are cached with links as page ids** (`.cache/`), so a rename or move rewrites every link to the page without fetching the linking pages again.
- **The frozen copy is per pickup,** at `runs/<ticket>/context/`, shared by every agent pass in it.
- **Notion without access to the root page pauses the project** (fail closed). Any other Notion failure uses the last mirror; with no mirror yet, the project waits and retries.
- **A project resumes only once its config, Linear and Notion checks all pass,** so a pause for one reason isn't cleared by another check passing.
- **`_linked/`** (pages linked from tickets but outside the tree) is deferred.

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
