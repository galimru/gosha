# Gosha — Feedback

Design points that aren't in [DESIGN.md](DESIGN.md) yet. DESIGN.md describes what is built or being built. This file holds what comes next: mostly simplifications of what's already there. Each point says what DESIGN.md says today, what changes, and its status.

When a point is implemented, the PR that implements it merges it into DESIGN.md (and its decisions log) and removes it from here. A rejected point stays here with the reason, so it isn't proposed again.

Statuses: **proposed** (open for discussion), **agreed** (to implement), **rejected** (kept as is, with the reason).

## 6. One round budget

**Status:** rejected

**Proposed:** one `max_rounds` for every address pass in a pickup, instead of `review.max_rounds` and `checks.max_rounds`.

**Why rejected:** checks always come last, so a shared budget lets a long review loop use up the rounds CI needs, and one flaky test then blocks a PR that was nearly done. The two loops also fail differently (review: no agreement; CI: a failure the agent can't fix), so they need different limits. `agent.timeout` already caps the total cost.

## 13. Config snapshot per pickup

**Status:** rejected

**Proposed:** a run keeps the config it was picked up with, like the frozen context, and validation only gates new pickups.

**Why rejected:** the code already reads the config once per pass (`Pass` in `internal/pipeline/ticket.go`) and takes the ticket as far as it can with it, so a change on `main` doesn't affect a pass in progress. A change only matters when an interrupted run continues in a later pass (usage limit, restart, Blocked → Ready): a valid new config is used, which is what you want after a fix, and an invalid one pauses the project until `main` is fixed. Snapshotting would add storing each run's config in the database, for no real gain.
