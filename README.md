<p align="center">
  <img src="docs/assets/logo.png" alt="Gosha" width="200">
</p>

<h1 align="center">Gosha</h1>

<p align="center">
  A self-hosted tool for autonomous software delivery.
</p>

<p align="center">
  <a href="https://github.com/galimru/gosha/actions/workflows/ci.yml"><img src="https://github.com/galimru/gosha/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
</p>

---

Gosha watches a Linear team for tickets moved to **Ready**. For each one, it implements the change with a Claude Code agent, working from the project's specs mirrored from Notion. A separate agent reviews it, Gosha fixes what the review and CI find, merges, and reports the result back on the ticket.

## How it works

1. You write specs in Notion and tickets in Linear, then move a ticket to **Ready**.
2. Gosha picks it up and an agent implements it, using the ticket and the project's Notion pages as context.
3. A separate reviewer agent checks the change, and the implementer addresses its comments.
4. Gosha waits for the project's CI, feeding any failures back to the implementer.
5. Gosha merges the pull request and posts a summary on the ticket, moving it to **In Review** for you to accept.

- **Linear is the only place you give instructions:** ticket states and ticket comments.
- **Gosha acts as itself:** a GitHub App and a Linear OAuth app, never your personal accounts.
- **Independent verification:** every change gets a separate reviewer agent and the project's own CI before it's merged.
- **Telegram activity feed:** a detailed, read-only log of what Gosha is doing.
- **Multi-project:** one daemon, with each project configured from `.gosha/` in its own repo.
- **Sandboxed agents:** they run in Claude Code's OS sandbox and never get GitHub, Linear or Gosha credentials.

The full behaviour is described in [docs/DESIGN.md](docs/DESIGN.md).

## Requirements

- macOS or Linux (Linux is best effort until the agent sandbox is verified there)
- `git` and the [`claude`](https://docs.claude.com/en/docs/claude-code) CLI on `PATH`; on Linux, also `bubblewrap` and `socat`
- A Claude subscription token from `claude setup-token`
- A GitHub App installed on each project repo, a Linear OAuth app with client credentials, a Notion integration with read access, and a Telegram bot

Run Gosha as a regular user. It refuses to run as root.

## Getting started

```sh
go install github.com/galimru/gosha/cmd/gosha@latest

gosha doctor                     # check the host
gosha secrets edit               # fill ~/.gosha/secrets.env
gosha auth linear                # confirm the Linear app identity
gosha add <github-repo-url>      # register a project (needs .gosha/config.yaml on its default branch)
gosha validate                   # config, credentials, Linear states
gosha run <project> --dry-run    # what Gosha would pick up, reported here and in Telegram
gosha run <project> --once       # take one Ready ticket to a merged PR
gosha run <project>              # keep doing that, in the foreground
```

The global config lives in `~/.gosha/config.yaml` ([§5.1](docs/DESIGN.md#51-global-goshaconfigyaml)), and each project's config in `<repo>/.gosha/config.yaml` ([§5.2](docs/DESIGN.md#52-per-project-repogoshaconfigyaml)).

## Documentation

| Document | Contents |
|---|---|
| [DESIGN.md](docs/DESIGN.md) | Behaviour, configuration, security. The source of truth. |
| [ROADMAP.md](docs/ROADMAP.md) | Milestones and what each one delivers |
| [SPIKES.md](docs/SPIKES.md) | Findings from the M0 spikes against real services |

## Development

```sh
go vet ./...
go test -race ./...
```

| Path | Purpose |
|---|---|
| `cmd/gosha/` | CLI entrypoint and commands |
| `internal/config/` | Global and project config, `${VAR}` secrets, redaction |
| `internal/linear/` | Linear GraphQL client (app actor) |
| `internal/github/` | GitHub App authentication |
| `internal/gitx/` | git CLI wrapper with per-command token injection |
| `internal/notify/` | Telegram activity feed |
| `internal/pipeline/` | Per-ticket pipeline: pickup planning, dry run |
| `internal/store/` | SQLite persistence |
| `internal/tmpl/` | Template data model shared by prompts and branch names |

Commits and PR titles follow Conventional Commits with a scope. See [CLAUDE.md](CLAUDE.md).
