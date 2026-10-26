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
| 7 | Feedback | Simpler design from the M0–M6 feedback |
| 8 | Production | Runs unattended as a service |
| 9 | End-to-end | Every workflow verified on real GitHub, Linear and Notion |

## M0 — Spikes

Throwaway code. The goal is answers, not features. Any "no" updates DESIGN.md before M1 starts.

**Status: done** (2026-10-18). Findings in [SPIKES.md](SPIKES.md). Changes it caused: agents default to `dontask` mode with an allowlist instead of bypass, and Linear uses the `client_credentials` grant. Linux was not verified and stays best effort (re-checked in M9).

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
- **After the merge,** Gosha posts a short Delivered comment (PR link, merge commit, diff size, the implementer's summary, review and check rounds, the checks that passed), moves the ticket to In Review and finishes the run. There's no deploy watch yet, and the full Delivered comment (DESIGN §3) comes in M6. (M6 adds both.)
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

**Status: built** (2026-10-18), pending the end-to-end check below on a real Notion workspace. Behaviour that is specific to M5, until the daemon exists (M8):
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

**Status: built** (2026-10-18), pending the end-to-end check below on a real project.

Changes from the plan:
- **A follow-up with no new human comment goes Blocked** at once, asking what to change, instead of running the implementer on a ticket that's already shipped. The follow-up's implementer is told about the earlier PR (`.FollowUp`: link, merge commit, summary, deploy result).
- **A follow-up comes from any merged PR,** Gosha's or a human's. Its PR is titled `NXT-12: <title> (follow-up)` and links the earlier one.
- **Ready after a failed deploy** looks at the deploy again when there's no new comment (a human may have re-run it), and opens a follow-up when there is one. The deploy run is found by the merge commit's `head_sha`; none within `deploy.timeout` goes Blocked. `gosha validate` warns when the workflow file isn't on the default branch.
- **The Delivered comment's data is kept on the run** (`runs.report`) as it's gathered, so a delivery resumed after a restart reports everything. The implementer result has a new optional `left_out` list for "deliberately left out", shown in the PR body and the Delivered comment.
- **The merged branch and checkout are removed before the deploy watch,** not at delivery.
- **`/restart` lines aren't passed to agents,** and ticket comments posted during a pickup reach its next implementer pass (address or fix), not only the next pickup.

- Deploy watch, Delivered comment, In Review transition
- Follow-up PRs from In Review → Ready, `/restart`
- Human ticket comments as instructions on every pickup

**Done when:** the full lifecycle in DESIGN §2 works, including follow-ups and Blocked → resume.

## M7 — Feedback

Simplifies what M0–M6 built, from a review of the design after M6. Each point became a commit that updated DESIGN.md; the two rejected proposals (a single round budget, a config snapshot per pickup) are recorded in the DESIGN.md decisions log.

**Status: built** (2026-10-18), in one PR with a commit per point, pending the end-to-end check below on a real project. Gosha isn't live yet, so nothing is migrated: existing migrations were edited in place, and an old `gosha.db` or `~/.gosha/projects/*/worktrees/` should be deleted.

Changes from the plan:
- **The reviewer keeps any Bash,** but in the sandbox it can write only its pass's temp directory, so its clone is read-only. With the sandbox off, only the removed edit tools and the `auto` classifier stop it from writing.
- **A Notion access pause is recorded as such** (`project_state.notion`), so other passes don't clear it; only a pickup's refresh does.
- **A tree change refetches every page** (a page added, renamed, moved or removed, or a title changed); content edits still fetch only the changed pages.
- **`gosha auth status` and `gosha service` didn't exist yet,** so point 18 removed `auth linear` and moved its checks into `doctor`; `service` stays in M8.
- **The daily digest is still only configured;** it's built in M8 with the daemon. The heartbeat ping is in `gosha run`'s loop.

- **Sandbox:** agent workspaces in `workspaces/` (1), `agent.sandbox` picks the permission mode (2), no `agent.setup` (3), read-only reviewer (17)
- **Pipeline:** report the deploy instead of blocking on it (4), Linear as the only instruction channel (5), base sync through GitHub's update-branch (7), simpler expected checks (8), no `/restart` (11)
- **Config:** fixed branch names (9), `.gosha/instructions.md` instead of prompt overrides (10), secrets by fixed key names (12)
- **Reporting:** Telegram for alerts and the daily digest, plus a heartbeat ping (14), Notion refresh at pickup only (15), shorter Delivered comment (16)
- **CLI:** offline `validate`, host checks in `doctor` (18)

**Done when:** every agreed point is in DESIGN.md, and a ticket still goes Ready → delivered on a real project.

## M8 — Production

**Status: built** (2026-10-18), in one PR with a commit per item, pending the "done when" week below and the end-to-end checks in M9. The migrations were squashed into one baseline, so an old `gosha.db` should be deleted; from here on migrations are append-only.

Changes from the plan:
- **Verifying the agent sandbox on Linux moved to M9,** which runs Gosha on a Linux VPS anyway. Linux stays best effort until then.
- **Panics are retried, not paused:** a supervisor recovers, alerts once, and polls again after 5 minutes. Exiting would take down every project, and a "crashed" state would need clearing rules for no gain, since steps are idempotent.
- **A `concurrency` slot covers one agent pass,** not a whole pickup, so a project waiting on CI doesn't hold one. Waiting for a slot doesn't spend `agent.timeout`.
- **A `run.lock` per project** keeps the daemon and `gosha run` from running the same project, and a `cleanup.lock` keeps cleanup off a workspace a pickup is about to claim.
- **Only the daemon and `gosha run` rotate the log;** short commands append, so they never rotate it from under the daemon. Log lines were already tagged with their project, so no per-project loggers were needed.
- **The digest's Ready counts come from each project's last poll,** so the digest never fetches a repo concurrently with its supervisor.
- **A run replaced in the same pickup** (PR closed unmerged, or merged by hand) still loses only its checkout: the new run uses the same workspace, frozen context included. Leftover workspaces with no run are removed after `retention`.
- **`gosha init` asks for the required fields** (name, Linear team, Notion page) instead of taking flags, and refuses to write a config `validate` would reject. Scripts write the file directly.

- Daemon: multiple projects, global scheduler, panic isolation
- Crash recovery, cancel and pause (Backlog/Todo/Canceled), usage-limit pause/resume
- `gosha service install|uninstall|start|stop|restart|status` (systemd + launchd)
- Log rotation, daily digest
- ~~Verify the agent sandbox on Linux~~ (moved to M9)
- Remaining CLI: `init`, `config show`, `logs`
- Releases (DESIGN §11.4): GoReleaser builds macOS and Linux binaries (amd64, arm64) with `main.version` set, and a workflow run by hand publishes a GitHub release from `main` when it has a `feat`, `fix` or `perf` commit since the last tag. Versions stay `0.x`: `feat` and breaking changes bump the minor, everything else the patch
- Install and upgrade (DESIGN §11.4): `install.sh` (download, checksum, install to `~/.local/bin`), `gosha upgrade` (replace the binary, restart the service), README getting started switched to `install.sh`
- Stable schema before the first release (DESIGN §10): squash `001`–`005` into one baseline migration; afterwards migrations are append-only, `gosha.db` is backed up before applying one, and a database newer than the binary is refused
- Retention (DESIGN §11.3): workspaces removed when a run ends any way (not only on merge), idle Blocked workspaces and finished runs' transcripts removed after `retention`, disk use in `gosha doctor`, and `gosha prune [--dry-run] [--older-than]` to run the same cleanup on demand
- Docs cleanup:
  - README reorganized into a clear structure (what Gosha is, requirements, install, setup, usage, links to the design)
  - DESIGN cleaned up: current behaviour only, with milestone notes and superseded decisions removed or moved to the decisions log
  - Code comments stop referencing DESIGN (no `DESIGN §x` pointers); comments explain the code on their own

**Done when:** Gosha runs as a service on a VPS for a week across two projects without intervention beyond Linear.

## M9 — End-to-end

Every milestone so far was built against unit tests and fakes, and M1–M7 are each pending their end-to-end check. M9 runs all of them, and everything else in DESIGN §2, on real services. Results and anything that had to change go in [E2E.md](E2E.md), the same way spikes went in SPIKES.md. A failure is fixed (and DESIGN.md updated if behaviour changes) before M9 is done.

- **Host setup** (DESIGN §11.7): `gosha setup` for Claude, Linear, Notion, Telegram (chat id from the bot's updates) and the GitHub App (manifest flow, with a manual fallback); `doctor` points at it; README getting started switched to it. Both test hosts below are set up with it. *Built (2026-10-25), pending its run on the test hosts*
- **Test setup,** kept for later regressions:
  - a GitHub repo with a small app, a CI workflow and a deploy workflow, with the Gosha GitHub App installed
  - a Linear team with Gosha's states, a Notion root page with a few spec pages, a Telegram channel
  - Gosha installed from a release with `install.sh` and running as a service, on macOS and on a Linux VPS
- **Lifecycle** (DESIGN §2): Ready → delivered and In Review; In Review → comment → Ready opens a follow-up PR; Blocked → answer → Ready resumes on the same PR; PR closed → Ready restarts; Backlog/Todo pauses and Ready resumes; Canceled closes the PR and deletes the branch
- **Pipeline:** priority and blocked-by ordering, a review → fix round, a failing check → fix round, a moved base (with and without a conflict), a push touching `.github/workflows/` going Blocked, a failed deploy reported on the ticket
- **Context:** an agent reads the Notion pages relevant to its ticket; edits, renames and moves between pickups show up in the mirror; removing Notion access pauses the project
- **Linux sandbox** (moved from M8): bubblewrap and `socat` as the only extra packages, the sandbox's read and write rules matching macOS, root refusal, and `TestRealClaude` with a setup token (SPIKES open items)
- **Recovery:** the daemon killed during implement, review and checks resumes each run; a usage limit pauses and resumes (capturing the real payload, SPIKES open items); an invalid config on the default branch pauses the project
- **Operations:** two projects at once, Telegram alerts, the daily digest and heartbeat, `gosha upgrade` during a run, retention cleanup

**Done when:** every scenario above passes on macOS and Linux, with the results in E2E.md.

## After M4: build Gosha with Gosha

Once tickets can go Ready → merged, the remaining milestones are filed as Linear tickets in a `gosha` project and implemented by Gosha itself.
