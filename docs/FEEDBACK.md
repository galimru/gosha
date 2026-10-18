# Gosha — Feedback

Design points that aren't in [DESIGN.md](DESIGN.md) yet. DESIGN.md describes what is built or being built. This file holds what comes next: mostly simplifications of what's already there. Each point says what DESIGN.md says today, what changes, and its status.

When a point is implemented, the PR that implements it merges it into DESIGN.md (and its decisions log) and removes it from here. A rejected point stays here with the reason, so it isn't proposed again.

Statuses: **proposed** (open for discussion), **agreed** (to implement), **rejected** (kept as is, with the reason).

## 4. Report the deploy instead of blocking on it

**Status:** agreed

**Today (§8.1, §8.7):** a failed deploy moves a merged ticket to Blocked, which adds a "blocked after merge" pickup mode and "Ready without a comment re-checks the deploy".

**Change:** always deliver; the Delivered comment says deploy ✅/❌ with a link. A failed deploy is handled by the normal follow-up (comment → Ready). §1.1 already says CI/CD owns deploy.

## 5. One channel for human instructions

**Status:** agreed

**Today (§1.4 vs §8.3, §8.5):** §1.4 says Linear is the only place a human instructs Gosha, but human PR comments are also collected as instructions (PR cursor, check before every verdict and before merge).

**Change:** Linear only, matching §1.4. Drops the PR cursor and PR feedback collection.

## 6. One round budget

**Status:** rejected

**Proposed:** one `max_rounds` for every address pass in a pickup, instead of `review.max_rounds` and `checks.max_rounds`.

**Why rejected:** checks always come last, so a shared budget lets a long review loop use up the rounds CI needs, and one flaky test then blocks a PR that was nearly done. The two loops also fail differently (review: no agreement; CI: a failure the agent can't fix), so they need different limits. `agent.timeout` already caps the total cost.

**DESIGN.md fix instead (§8 feedback table):** with human PR feedback gone (point 5), the counting rule is simply: every reviewer `request_changes` uses a review round, and every CI or conflict fix uses a check round.

## 7. Base sync through GitHub

**Status:** agreed

**Today (§8.5):** the base is merged in the bare clone with `git merge-tree --write-tree` and `git commit-tree`, which needs git ≥ 2.38. Ubuntu 22.04 LTS ships 2.34 and Debian 11 ships 2.30, so `gosha doctor` fails on those hosts.

**Change:** when the PR is behind, call `PUT /pulls/{n}/update-branch` (with `expected_head_sha`), fetch the new head into the bare clone and re-run checks. On a conflict (422), run the implementer in a fresh checkout as today. The pinned-SHA merge stays. The git version floor goes away (`merge-base --is-ancestor` works on any git).

**Trade-offs:**
- The API is asynchronous: Gosha polls until the PR head changes.
- A conflict comes back without a file list, so `.Feedback.Conflicts` goes away; the implementer finds the conflicts when it runs `git merge`.
- It rewrites working M4 code; the gain is host compatibility, not less code.

## 8. Simpler expected checks

**Status:** agreed

**Today (§8.4):** Gosha parses `.github/workflows/` triggers to decide if a check must appear. `RunsOnPR` (`internal/gitx/merge.go`) ignores `branches:` and `paths:` filters, so a repo whose only workflow is `deploy.yml` on push to `main` blocks every ticket.

**Change:** if `checks.required` is set, gate on those names. Otherwise, whatever appears within `start_grace` must pass, and nothing appearing counts as a pass.

## 13. Config snapshot per pickup

**Status:** rejected

**Proposed:** a run keeps the config it was picked up with, like the frozen context, and validation only gates new pickups.

**Why rejected:** the code already reads the config once per pass (`Pass` in `internal/pipeline/ticket.go`) and takes the ticket as far as it can with it, so a change on `main` doesn't affect a pass in progress. A change only matters when an interrupted run continues in a later pass (usage limit, restart, Blocked → Ready): a valid new config is used, which is what you want after a fix, and an invalid one pauses the project until `main` is fixed. Snapshotting would add storing each run's config in the database, for no real gain.

## 14. Telegram for alerts only

**Status:** agreed

**Today (§9):** ~15 event types, most of which repeat the PR timeline, plus a daily digest that doubles as a liveness signal.

**Change:** paused, Blocked, delivered, usage limit and crash only, plus the daily digest, which stays as a summary of the day (delivered, blocked, queue). For noticing that Gosha has died, add an optional heartbeat URL ping instead of relying on the digest. `--dry-run` reports to stdout only.

## 15. Notion refresh at pickup only

**Status:** agreed

**Today (§6.2):** refresh before every pickup, every 15 minutes while idle, and on every poll while paused.

**Change:** refresh only at pickup; a Notion pause is retried at the next pickup. Prefer a full refetch over rename bookkeeping where they conflict. The markdown mirror itself stays: agents work best with files they can grep, every pass sees the same frozen copy, and no Notion token reaches the agent.

## 16. Shorter Delivered comment

**Status:** agreed

**Today (§3):** lists every review round, fix round and check, gathered incrementally into `runs.report`.

**Change:** summary, PR link, assumptions/`left_out` and the deploy line. The rest is on the PR.
