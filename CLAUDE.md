# Gosha

Gosha is a self-hosted Go service. It picks up Ready tickets from Linear, implements them with Claude Code agents using project context mirrored from Notion, reviews and fixes the changes, waits for CI, merges, and reports back on the ticket.

- Design: [docs/DESIGN.md](docs/DESIGN.md). This is the source of truth for behaviour. If an implementation changes behaviour, update the design in the same PR.
- Implementation order: [docs/ROADMAP.md](docs/ROADMAP.md).
- Module path: `github.com/galimru/gosha`.

## Rules

- **Host-agnostic.** Gosha runs on macOS and Linux. Never assume a specific machine, user, package manager or service manager, and never inspect the local machine to drive design decisions.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/) **with a scope**:

```
<type>(<scope>): <summary>
```

- **type:** `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `build`, `ci`, `chore`
- **scope:** the area touched, usually the package: `cli`, `config`, `linear`, `notion`, `github`, `gitx`, `agent`, `pipeline`, `store`, `notify`, `daemon`, `service`, `release`, `flock`, `docs`
- **summary:** imperative, lowercase, no trailing period, ≤ 72 characters
- Breaking changes: `!` after the scope (`feat(config)!: …`) plus a `BREAKING CHANGE:` footer

Examples:
```
feat(linear): poll Ready tickets ordered by priority
fix(gitx): inject installation token per push
docs(design): describe sandbox fallback
```

## Pull requests

- **Title:** same format as a commit (`<type>(<scope>): <summary>`).
- **Body:**

```markdown
## Why
The problem being solved and why it matters now: the user-visible issue, the gap, or the roadmap item.

## What
How it's approached: the key changes and decisions, plus anything a reviewer should look at closely.
```

Keep both sections short and concrete. Link the roadmap milestone or design section when relevant.
