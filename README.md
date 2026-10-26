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

Gosha turns your planned work into shipped code. You plan in Linear and write specs in Notion. Once a ticket is ready to build, Gosha takes it from there: it writes the change, has it reviewed, gets CI green, merges it, and tells you on the ticket what shipped.

You decide what gets built and whether it's accepted. Gosha does everything in between, on your own machine or server.

## What you get

- **You only use Linear:** move a ticket to Ready to start it, comment to guide it, move it to Backlog to pause it or to Canceled to drop it.
- **Every change is checked twice:** a separate agent reviews it, and your own CI has to pass before anything is merged.
- **Grounded in your specs:** agents read the ticket and the project's Notion pages, and ask on the ticket when something is unclear instead of guessing.
- **Its own identity:** Gosha works through its own GitHub App and Linear app, never your personal accounts.
- **Safe by default:** agents run in Claude Code's sandbox and never see your GitHub, Linear or Gosha credentials.
- **Many projects, one service:** each project is configured from `.gosha/` in its own repo.
- **Quiet unless it matters:** Telegram tells you when a ticket ships or needs you, plus a short daily digest.

## Requirements

- macOS or Linux, amd64 or arm64 (Linux is best effort until the agent sandbox is verified there)
- `git` and the [`claude`](https://docs.claude.com/en/docs/claude-code) CLI on `PATH`; on Linux, also `bubblewrap` and `socat`
- A regular user to run as: Gosha refuses root
- Accounts and credentials, all Gosha's own:
  - a Claude subscription token from `claude setup-token`
  - a GitHub App installed on each project repo
  - a Linear OAuth app with client credentials
  - a Notion integration with read access to the project's pages
  - a Telegram bot that can post to a private channel

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/galimru/gosha/main/install.sh | sh
```

This installs the latest release into `~/.local/bin`, after checking it against the release's checksums. `sh -s -- --version v0.x.y` pins a release and `--prefix <dir>` picks another directory. With a Go toolchain, `go install github.com/galimru/gosha/cmd/gosha@latest` also works, and `make install` in a checkout installs that checkout's code in the same place, to try a branch before it's released.

Later, `gosha upgrade` replaces the binary with the latest release and restarts the service.

## Setup

**1. The host.** Once, as the user Gosha will run as:

```sh
gosha setup                      # Claude, Linear, Notion, Telegram and the GitHub App
gosha doctor                     # host and credentials
sudo gosha service install       # systemd on Linux, launchd on macOS; runs as you, not root
```

`gosha setup` shows where to create each credential, checks each one before moving on, and writes `~/.gosha/config.yaml`, `secrets.env` and `github-app.pem`. It creates the GitHub App for you with only the permissions Gosha needs: Workflows is left out, so agents can't change your CI. It also finds the Telegram channel's id once you add the bot to the channel as an admin. Run it again any time to replace or re-check a value: Enter keeps what's set.

The service doesn't need a project to start: projects added later are picked up within a minute.

<details>
<summary>Setting up the host by hand</summary>

- **GitHub App:** create one (Settings → Developer settings → GitHub Apps) with these repository permissions: Contents and Pull requests read & write; Checks, Actions and Commit statuses read. Don't grant Workflows. Save its private key as `~/.gosha/github-app.pem` with mode `600`.
- **Linear:** an OAuth application (Settings → API) with *Client credentials* enabled.
- **Notion:** an internal integration with read content access.
- **Telegram:** a bot from @BotFather, made an admin of a private channel with "Post messages".

Then `~/.gosha/config.yaml` (all options in [DESIGN §5.1](docs/DESIGN.md#51-global-goshaconfigyaml)):

```yaml
github_app:
  app_id: 123456
telegram:
  chat_id: -1001234567890
```

And `gosha secrets edit` for `~/.gosha/secrets.env`:

```sh
CLAUDE_CODE_OAUTH_TOKEN=…   # from `claude setup-token`
LINEAR_CLIENT_ID=…
LINEAR_CLIENT_SECRET=…
NOTION_TOKEN=…
TELEGRAM_BOT_TOKEN=…
```

</details>

**2. A project.** Install the GitHub App on the project's repo, and share its root Notion page with the integration. Then, in a checkout of the repo:

```sh
gosha init   # asks for the Linear team and Notion page, writes .gosha/config.yaml
git add .gosha && git commit -m "chore: configure gosha" && git push
```

The config's options are in [DESIGN §5.2](docs/DESIGN.md#52-per-project-repogoshaconfigyaml). Gosha reads the config from the default branch only, so it has to be merged there. Then, on the Gosha host:

```sh
gosha add https://github.com/you/your-repo
gosha doctor <project>           # the project's config, Linear team and Notion access
```

A running service starts on the project's Ready tickets within a minute. To see what it would pick up first, run `gosha run <project> --dry-run` before installing the service, or before moving tickets to Ready.

## Usage

Day to day, you only use Linear: move tickets to **Ready** and read the result on each ticket. The CLI is for the operator:

| Command | What it does |
|---|---|
| `gosha status` | Projects (active or paused, and why), runs in flight, a usage-limit pause |
| `gosha logs [-f] [--project <name>]` | The log, as text |
| `gosha doctor [project]` | Check the host, credentials and projects |
| `gosha setup` | Replace or re-check the host's credentials |
| `gosha config show <project>` | A project's effective config, defaults included |
| `gosha context show <project>` | The Notion page tree agents start from |
| `gosha service status\|start\|stop\|restart` | Control the service |
| `gosha prune [--dry-run]` | Free disk now: idle workspaces and old transcripts |
| `gosha run <project> [--once]` | Run one project in the foreground, for prompt tuning (stop the service first) |
| `gosha upgrade [--check]` | Install the latest release |
| `gosha validate [path]` | Check a repo's `.gosha/config.yaml` offline, e.g. in its CI |

`gosha <command> --help` has the details.

## Documentation

| Document | Contents |
|---|---|
| [DESIGN.md](docs/DESIGN.md) | Behaviour, configuration, security. The source of truth. |
| [ROADMAP.md](docs/ROADMAP.md) | Milestones and what each one delivered |
| [SPIKES.md](docs/SPIKES.md) | Findings from checking the riskiest assumptions against real services |

## Development

```sh
go vet ./...
go test -race ./...
```

| Path | Purpose |
|---|---|
| `cmd/gosha/` | CLI entrypoint and commands |
| `internal/daemon/` | Supervisors for every project, panic isolation, heartbeat |
| `internal/pipeline/` | Per-ticket pipeline, usage-limit pause, digest, cleanup |
| `internal/agent/` | Claude Code runner, prompts, result parsing |
| `internal/config/` | Global and project config, secrets, redaction |
| `internal/linear/` | Linear GraphQL client (app actor) |
| `internal/github/` | GitHub App auth, PRs, reviews, checks, merges |
| `internal/gitx/` | git CLI wrapper with per-command token injection |
| `internal/notion/` | Notion client and page-tree mirror |
| `internal/notify/` | Telegram alerts, heartbeat |
| `internal/service/` | systemd and launchd units |
| `internal/release/` | Release download and verification for `upgrade` |
| `internal/store/` | SQLite persistence |
| `internal/flock/` | File locks shared by gosha processes |
| `internal/tmpl/` | Template data model shared by prompts and branch names |

Releases are published automatically from Conventional Commits on `main`. Commits and PR titles follow Conventional Commits with a scope; see [CLAUDE.md](CLAUDE.md).
