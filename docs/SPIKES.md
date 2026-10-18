# Gosha — M0 spike findings

Answers to the questions in [ROADMAP.md](ROADMAP.md) M0, checked against real services on 2026-10-18. The spike code was throwaway and isn't kept; this file and the DESIGN.md changes are what remain.

**Environment:** `claude` 2.1.302 on macOS (arm64, regular user). GitHub App installed on a private scratch repo. Linear OAuth app in a scratch team.

**Linux was not verified.** The Claude runner spike was only run on macOS. Linux support (bubblewrap sandbox) is best effort until it's tested on a real host; see [Open items](#open-items).

## Summary

| Question | Answer | Design change |
|---|---|---|
| Headless `claude -p` with a setup token | **Yes** | none |
| Isolated settings directory | **Yes** (`CLAUDE_CONFIG_DIR`) | name the variable |
| Sandbox blocks `~/.gosha`, allows worktree, temp, caches | **Partial**: holds for Bash, but in bypass mode the Write/Edit tools can write anywhere | default permission mode becomes `dontask` with an allowlist (§7.1) |
| Result file written and parsed, retry nudge | **Yes** (`--resume`) | agents need a git identity in their environment |
| Usage-limit detection | **Yes**, structured (`rate_limit_event`) | §8.11 uses it |
| `auto` mode headless | **Yes**, no legitimate step blocked | stays an option |
| Process control | **Yes** (process group kill) | none |
| Linear app actor | **Yes**, and `client_credentials` makes the browser flow unnecessary | client id/secret instead of a token (§5, §8.6) |
| GitHub App | **Yes**, all 13 checks | details in §8.4, §8.6 |

## 1. Claude runner

Every run used a scrubbed environment (`PATH`, `HOME`, `TMPDIR`, `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_OAUTH_TOKEN` only), its own process group, `--output-format stream-json --verbose`, and generated settings passed with `--settings`.

### 1a. Setup-token auth: yes
- `CLAUDE_CODE_OAUTH_TOKEN` alone authenticates headless runs. The init event reports `apiKeySource: none` (subscription, not an API key).
- `--bare` is **not usable**: it accepts only `ANTHROPIC_API_KEY` and never reads OAuth tokens.

### 1b. Isolated settings: yes
- `CLAUDE_CONFIG_DIR=<run>/claude-config` makes Claude ignore `~/.claude`. A marker in the user's `CLAUDE.md` wasn't seen, and the user's `SessionStart`/`UserPromptSubmit` hooks didn't fire.
- The worktree's own `CLAUDE.md` still loads, which is what we want.

### 1c. Sandbox: partial → fixed with `dontask`
Settings used:

```json
{
  "permissions": {
    "allow": ["Read", "Bash", "WebFetch", "WebSearch", "Task", "TodoWrite", "ToolSearch",
              "Edit(//<worktree>/**)", "Edit(//<run tmp>/**)"],
    "deny":  ["Read(~/.gosha/**)", "Edit(~/.gosha/**)", "Edit(//<context>/**)"]
  },
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "autoAllowBashIfSandboxed": true,
    "allowUnsandboxedCommands": false,
    "filesystem": {
      "allowWrite": ["<worktree>", "<run tmp>", "~/.cache", "~/go", "~/.npm", "~/Library/Caches"],
      "denyRead":   ["~/.gosha"],
      "denyWrite":  ["<context>"]
    },
    "network": { "allowedDomains": ["*"] }
  }
}
```

Results (✅ = behaved as intended):

| Step | bypass | dontask |
|---|---|---|
| Bash: write worktree, `~/.cache`, `~/go/pkg`, `~/.npm` | ✅ | ✅ |
| Bash: read `~/.gosha` | ✅ blocked | ✅ blocked |
| Read tool: read `~/.gosha` | ✅ blocked (deny rule) | ✅ blocked |
| Bash: write outside the allowlist | ✅ blocked | ✅ blocked |
| **Write tool: write outside the allowlist** | ❌ **written** | ✅ blocked |
| Bash with `dangerouslyDisableSandbox: true` | ✅ ignored | ✅ ignored |
| Read frozen context / write to it (Bash and Write tool) | ✅ / ✅ blocked | ✅ / ✅ blocked |
| Network + `go build` + codegen script | ✅ | ✅ |

Findings:
- **The OS sandbox only wraps Bash.** File tools (Write, Edit) follow permission rules. In bypass mode everything not explicitly denied is allowed, so the Write tool can write anywhere the OS user can.
- **`--permission-mode dontAsk --permission-prompts none`** with explicit allow rules is just as unattended and denies everything not listed. It passed every step.
- **`failIfUnavailable: true`** makes Claude refuse to start without a working sandbox, which is the "refuse to run unsandboxed" behaviour from §7.1.
- **Writable directories must exist before the run.** The sandbox won't let a process create the missing parent of an allowed directory (Go failed to create `~/Library/Caches/go-build` when `~/Library` didn't exist).
- **Toolchain caches depend on the OS:** Go's build cache is `~/Library/Caches/go-build` on macOS and `~/.cache/go-build` on Linux.
- The Grep/Glob tools aren't present in this build; searching goes through Bash, which is sandboxed.

### 1d. Result file: yes
- The agent wrote JSON to a path in the run's temp dir (outside the worktree), and Gosha-side parsing with unknown fields rejected worked.
- A deliberately broken result was detected, and `--resume <session id>` with a "fix your result file" nudge repaired it in the same session.
- **Agents need a git identity:** with no global git config, `git commit` fails ("Author identity unknown"). Setting `GIT_AUTHOR_NAME/EMAIL` and `GIT_COMMITTER_NAME/EMAIL` to the bot identity fixes it.
- Claude adds a `Co-Authored-By: Claude …` trailer to its commits by default.

### 1e. Usage limits: yes, structured
Every run emits a `rate_limit_event`:

```json
{ "type": "rate_limit_event",
  "rate_limit_info": {
    "status": "allowed", "resetsAt": 1792334400, "rateLimitType": "five_hour",
    "unifiedWindows": {
      "five_hour": { "utilization": 0.07, "resetsAt": 1792334400 },
      "seven_day": { "utilization": 0.01, "resetsAt": 1792692000 } } } }
```

The `result` event also has `is_error`, `api_error_status` and `permission_denials`. Gosha can read the reset time from JSON instead of parsing text, and can see utilization before a limit is hit. **Not yet observed:** the exact `status` value and result event when a limit is actually reached (no quota was burned to force one). The parser should treat any `status` other than `allowed` as limited. (M2: `allowed_warning`, which Claude Code reports when close to a limit while still serving requests, is treated as allowed too.)

### 1f. `auto` mode: yes
`--permission-mode auto --permission-prompts none` runs headless. Over 3 runs × 6 legitimate steps (`ls`, `go build`, codegen + `go vet`, `git status`, `curl`, cache write), the classifier blocked nothing.

### 1g. Root: not run
Needs a Linux host (see Open items).

### 1h. Process control: yes
Starting claude with `Setpgid` and sending SIGTERM (then SIGKILL after 5s) to the negative pgid stopped claude and the `sleep` it had started. No processes were left behind.

### 1i. Sandbox paths inside ~/.gosha (M2)
Checked during M2 on macOS (`claude` 2.1.302), because ticket checkouts and run temp dirs live under `~/.gosha`, which the sandbox must otherwise hide.

| Layer | Setup | Result |
|---|---|---|
| OS sandbox | `denyRead: [X]`, `allowWrite: [X/w/t]` | ❌ Bash can write `X/w/t` but **not read** it |
| OS sandbox | `denyRead: [X]`, `allowRead` + `allowWrite: [X/w/t]` | ✅ `X/w/t` readable and writable; `X/secret`, `X/w/other` still blocked |
| Permission rules | allow `Read`, `Edit(X/w/t/**)`; deny `Read(X/**)`, `Edit(X/**)` | ❌ Read and Write tools denied **inside** `X/w/t` too: a deny always beats an allow |
| Permission rules | deny every entry of `X` *around* `X/w/t` (`X/secret`, `X/w/other`, …) | ✅ `X/w/t` readable and writable with the file tools; the rest denied |

So the two layers take different shapes (DESIGN §7.1): the sandbox denies `~/.gosha` and re-allows the run's paths, and the permission rules deny the siblings along the path. `TestRealClaude` in `internal/agent` repeats this check with Gosha's generated settings (`GOSHA_TEST_CLAUDE=1`), and passed on macOS with the user's own login (`GOSHA_TEST_CLAUDE=user`). **Not yet run with an isolated `CLAUDE_CONFIG_DIR` inside `~/.gosha` and a setup token,** which is the production layout.

## 2. Linear app actor: yes

- **Paste-back flow** with `actor=app` works. The token's viewer is the app user "GoshaBot", not the person who authorized it.
- Authorization-code tokens **expire after 24h** and come with a refresh token. Refreshing rotates the refresh token.
- **The `client_credentials` grant works** once enabled on the OAuth app: same GoshaBot actor, a **30-day** token, no refresh token, no browser. Gosha can mint a new token whenever it needs one from the client id and secret.
- With a client-credentials token, everything is attributed to GoshaBot:
  - comment: `user = GoshaBot`
  - state change: issue history `actor = GoshaBot`
  - attachment (link card): `creator = GoshaBot`
- `relations` / `inverseRelations` and state types are readable. Linear's state types are `backlog`, `unstarted`, `started`, `completed`, `canceled`, and also **`duplicate`**.

## 3. GitHub App: yes

All 13 checks passed:

| Check | Result |
|---|---|
| JWT → installation token | ✅ 1h expiry; permissions exactly as §8.6, no `workflows` |
| Bot commit identity | ✅ `goshabot[bot] <340970700+goshabot[bot]@users.noreply.github.com>` |
| Bare clone + worktree + push over HTTPS | ✅ |
| No token in git config or remote URL | ✅ |
| PR opened as `goshabot[bot]` | ✅ |
| Push and PR trigger Actions | ✅ both `push` and `pull_request` runs, check runs visible |
| Failed-job logs via the Actions API | ✅ |
| Push touching `.github/workflows/` | ✅ rejected |
| `COMMENT` review with an inline comment | ✅ |
| Squash-merge with a stale `sha` | ✅ refused, HTTP 409 "Head branch was modified" |
| Squash-merge with the checked `sha` | ✅ merged |
| Branch deletion | ✅ |

Findings:
- **Token injection without argv or files:** pass the header through `GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_n` / `GIT_CONFIG_VALUE_n` (`http.https://github.com/.extraheader = AUTHORIZATION: basic <base64(x-access-token:TOKEN)>`). Add `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` so the user's git config (identity, credential helpers, URL rewrites) never applies.
- **Bare clone refs:** `git clone --bare` maps branches to `refs/heads/*`. For `origin/<default>` to exist (§5.2), set `remote.origin.fetch = +refs/heads/*:refs/remotes/origin/*` and fetch.
- **Bot user id ≠ app id.** The noreply email needs the bot *user* id, from `GET /users/<slug>[bot]`.
- **Workflow push rejection** reads: `refusing to allow a GitHub App to create or update workflow '<path>' without 'workflows' permission`. Gosha can match it and turn it into a clear Blocked reason.
- **Job logs:** the last lines of a job log are post-job cleanup output. The failure is earlier, so Gosha must extract the failing step's section rather than a fixed tail.
- The test workflow had to be committed by a human, which is the intended effect of withholding the Workflows permission.

## Open items

- **Linux runner (best effort, unverified):** sandbox on bubblewrap, required packages (Claude Code's docs list `bubblewrap` and `socat`), root refusal (1g). To be checked before M7 on a real Linux host.
- **Usage-limit payload at an actual limit:** capture the first real occurrence and adjust the parser.
- **Production agent layout:** run `TestRealClaude` with a setup token (`GOSHA_TEST_CLAUDE=1`), so the isolated `CLAUDE_CONFIG_DIR` under `~/.gosha` is exercised (shell snapshots and session files live there, outside what Bash may read).
