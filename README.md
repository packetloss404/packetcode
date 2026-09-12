# packetcode

A keyboard-first, multi-provider coding agent for the terminal, with a Claude Code-inspired TUI and first-class OpenAI Codex subscription support.

> Status: pre-1.0 and under active development. Documentation describes the current `main` branch.

packetcode keeps the conversation, tools, approvals, background agents, and workflows in one terminal interface. It can inspect and edit the current project, execute commands, delegate work, connect to MCP tools, and use hosted or local models without routing through OpenCode.

The native foreground/ACP tool set also includes a bounded HTTP(S) `fetch` for untrusted web
evidence, a per-session `todo_write` plan, and a no-progress loop detector. A
fetch is not a download or web-search API: it blocks private/loopback targets by
default, caps redirects/headers/body/time, disables ambient proxies, and labels
the returned content as untrusted before it reaches the model.
Background agents intentionally do not receive `fetch`; their plans and loop
guard remain independent per job.

## Interface

<p align="center">
  <img src="docs/images/packetcode-chat.png" alt="Packetcode chat interface using the Codex provider, with the prompt composer and native statusline" width="100%">
</p>

Permission-aware tool execution and the full background Agent workspace:

<p align="center">
  <a href="docs/images/packetcode-approval.png"><img src="docs/images/packetcode-approval.png" alt="Packetcode command approval interface" width="49%"></a>
  <a href="docs/images/packetcode-agents.png"><img src="docs/images/packetcode-agents.png" alt="Packetcode background Agent View" width="49%"></a>
</p>

These screenshots illustrate the layout and may precede the latest labels.
Current approval wording is described below; reviewed text-and-style snapshots
live in `testdata/tui/golden/packetcode/` and are checked by CI.

## Install

Install the latest macOS or Linux release:

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | bash
```

Install without `sudo`:

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | INSTALL_DIR="$HOME/.local/bin" bash
```

Install the latest Windows release:

```powershell
& ([scriptblock]::Create((Invoke-WebRequest https://raw.githubusercontent.com/packetloss404/packetcode/main/install.ps1).Content))
```

The Windows installer defaults to
`%LOCALAPPDATA%\Programs\PacketCode\bin` and does not silently modify `PATH`.
PacketADE also checks that documented location.

Install a specific release rather than whatever is newest:

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | VERSION=v0.6.0 bash
```

```powershell
& ([scriptblock]::Create((Invoke-WebRequest https://raw.githubusercontent.com/packetloss404/packetcode/main/install.ps1).Content)) -Version v0.6.0
```

Worth doing for anything unattended. `latest` is whatever the repository
publishes at the moment the script happens to run, which is the opposite of a
reproducible install.

### Download a binary directly

Every release publishes a static binary for each supported platform. These
links resolve to whatever the newest release is, so they do not go stale:

| Platform | Download |
| --- | --- |
| Linux x86-64 | [packetcode-linux-amd64.tar.gz](https://github.com/packetloss404/packetcode/releases/latest/download/packetcode-linux-amd64.tar.gz) |
| Linux arm64 | [packetcode-linux-arm64.tar.gz](https://github.com/packetloss404/packetcode/releases/latest/download/packetcode-linux-arm64.tar.gz) |
| macOS Apple silicon | [packetcode-darwin-arm64.tar.gz](https://github.com/packetloss404/packetcode/releases/latest/download/packetcode-darwin-arm64.tar.gz) |
| macOS Intel | [packetcode-darwin-amd64.tar.gz](https://github.com/packetloss404/packetcode/releases/latest/download/packetcode-darwin-amd64.tar.gz) |
| Windows x86-64 | [packetcode-windows-amd64.zip](https://github.com/packetloss404/packetcode/releases/latest/download/packetcode-windows-amd64.zip) |
| Windows arm64 | [packetcode-windows-arm64.zip](https://github.com/packetloss404/packetcode/releases/latest/download/packetcode-windows-arm64.zip) |

Older versions, release notes, and the checksum and signature files are on the
[releases page](https://github.com/packetloss404/packetcode/releases).

Downloading by hand skips the verification the installers do for you. Nothing
about a release makes that safe to omit, so run the checks yourself —
[docs/releases.md](docs/releases.md) gives the two commands.

The checksum file is signed, but the binaries themselves are not: packetcode
publishes no Apple Developer ID or Authenticode certificate yet. A macOS
download from a browser therefore arrives quarantined and Gatekeeper will
refuse to run it until you clear the flag with
`xattr -d com.apple.quarantine ./packetcode`, and Windows shows a SmartScreen
warning. `install.sh` avoids the macOS case entirely, because a file fetched
with `curl` is never marked quarantined in the first place.

Both installers check the download against `checksums.txt`, and check
`checksums.txt` itself against its Sigstore signature when `cosign` is on
`PATH` — the second is the one that matters, since anyone who could serve you a
modified archive could serve the matching checksum file beside it. They say so
plainly when `cosign` is absent, and refuse outright when a signature is present
and does not verify. Pass `REQUIRE_SIGNATURE=1` (or `-RequireSignature` on
Windows) to make an unverifiable download an error rather than a note.

Signing starts at v0.6.0. Earlier releases publish no signature at all, so
requiring one against those fails rather than passing quietly — which is the
right outcome, and worth knowing before you pin an older version in something
unattended. From v0.6.0 the release also carries a SLSA build-provenance
attestation covering every archive, verifiable with
`gh attestation verify <archive> --repo packetloss404/packetcode`.

See [docs/releases.md](docs/releases.md) for what is published, how to verify a
download by hand, and how release signing is configured.

Build from source with Go 1.26.0 or newer:

```bash
make build
./bin/packetcode
```

Windows:

```powershell
$commit = git rev-parse --short HEAD
go build -trimpath -ldflags "-s -w -X main.version=dev -X main.commit=$commit" -o bin/packetcode.exe ./cmd/packetcode
.\bin\packetcode.exe
```

The first run asks for a provider, API key when required, and model, then writes
`~/.packetcode/config.toml` with user-only permissions. Set an absolute
`PACKETCODE_HOME` to isolate all PacketCode configuration and state in another
data directory.

Three ways to give packetcode a provider key, strongest first: an environment
variable (`PACKETCODE_OPENAI_API_KEY` and friends), a `.env` file at
`~/.packetcode/.env` or `<project>/.env`, or `Ctrl+A` on a row in the provider
picker, which writes to `config.toml`. `/provider` shows which one is in force.
`.env` values are never exported to the shell commands packetcode runs. See
[docs/providers.md](docs/providers.md#env-files).

Optional integrations remain compatible by default and can be disabled
independently in `config.toml`:

```toml
[packet_computers]
enabled = false # no computer registry, SSH, or remote placement

[acp]
enabled = false # reject the optional local stdio ACP server

[sugar]
enabled = false # no built-in Sugar login/provider/cache/Conduit activity
```

The equivalent environment overrides are `PACKETCODE_ACP_ENABLED=false`,
`PACKETCODE_PACKET_COMPUTERS_ENABLED=false` and
`PACKETCODE_SUGAR_ENABLED=false`. Local PacketCode providers, sessions, tools,
background jobs, and workflows continue to operate. `packetcode doctor` and
`/help` report the resolved states.

Sugar is auto-inactive on a fresh non-Sugar install when `enabled` is absent;
existing Sugar configuration remains compatible. None of these gates deletes
saved configuration, credentials, registries, or sessions. PacketCode has no
runtime dependency on PacketADE or Syndicate and remains a standalone terminal
agent when every optional integration is disabled.

The executable has five public command families in addition to the default TUI:

| Command | Purpose |
| --- | --- |
| `packetcode run` | Run one non-interactive agent turn for scripts, CI, and benchmarks. |
| `packetcode doctor` | Read-only diagnostics, with focused checks and JSON output. |
| `packetcode skills` | List, validate, install, and remove Agent Skills. |
| `packetcode acp` | Run the local stdio Agent Client Protocol server. |
| `packetcode sugar` | Sign in to or manage the optional Sugar integration. |

Run `packetcode --help` or `<command> --help` for the current flags. Headless
execution takes the prompt as positional arguments:

```text
packetcode run --provider codex --model gpt-5.6-sol "review the current diff"
packetcode run --permission-mode read-only --json "summarize this repository"
packetcode run --resume <session-id> "continue with the next step"
```

`run` uses the same provider/session/tool/policy runtime as the TUI and ACP
server. It does not read prompts from stdin and has no `--trust`, `--computer`,
or ephemeral-session shortcut. If policy needs an interactive approval it fails
closed with exit 3; cancellation exits 130. Plain stdout contains only the
sanitized final response. `--json` emits one `schema_version: 1` object with
`ok`, `session_id`, `provider`, `model`, `output`, `elapsed_ms`, `usage`, and an
`error` on failure.
When a failed run has a session, stderr gives its ID and an interactive
`packetcode --resume ID` recovery command. Open saved history from the same
directory before continuing; completed tool actions are not rolled back.
Opening the session does not repeat the failed prompt.

To connect the built-in Sugar provider and pull its live Conduit/direct-model catalog:

```text
packetcode sugar login
```

Both flags are optional. The first sign-in on a machine asks which Sugar service
to use and offers the hosted one; press Enter to accept it, or type your own
deployment. The API key is named after the machine's hostname, so a member's key
list says which computer each key belongs to:

```text
Sugar service URL [https://usesugar.dev]:
Open https://usesugar.dev/portal/connect?user_code=NGC4-MSB2
Enter code: NGC4-MSB2
Waiting for approval…
```

On success the service URL is saved, so later sign-ins on that machine skip the
question. Override either default with `--server https://your-sugar-service.example`
or `--name your-name`; `PACKETCODE_SUGAR_BASE_URL` overrides the saved URL for one
machine. Sugar accepts key names of 2-80 characters — Packetcode checks the length
before sending, and reports a client Sugar refuses (a name it will not take, or a
service that does not register `packetcode`) with the fix instead of a bare HTTP
error.

Packetcode opens Sugar's approval page and prints the same short code in the terminal. Sign in as yourself, confirm the code and device name, then approve. Packetcode polls at Sugar's required interval, saves the member-owned revocable key with user-only file permissions, and pulls the live catalog. Add `--no-browser` when you want to open the printed URL manually.

Sugar defaults to `sugar/conduit`; `/model` can pin any model Sugar currently supplies.

## Providers

| Slug | Authentication | Notes |
| --- | --- | --- |
| `sugar` | Member-approved device sign-in | Live Conduit and direct-model catalog from the private Sugar service. |
| `codex` | Existing Codex CLI ChatGPT login | Reuses `~/.codex/auth.json`; no API key. |
| `openai` | API key | OpenAI API. |
| `anthropic` | API key | Anthropic Messages API. |
| `gemini` | API key | Google Gemini API; independent of Gemini CLI login. |
| `minimax` | API key | MiniMax OpenAI-compatible API; MiniMax M3 default. |
| `deepseek` | API key | DeepSeek OpenAI-compatible API. |
| `grok` | xAI API key | xAI API; packetcode does not reuse the consumer Grok subscription login. |
| `mistral` | API key | Mistral API, including Devstral and Codestral models. |
| `openrouter` | API key | OpenRouter model catalog and routing. |
| `ollama` | None | Native Ollama API; defaults to `localhost:11434`. |
| Custom | Optional | Any configured OpenAI-compatible endpoint. |

The `codex` provider is the zero-key path for an OpenAI Codex ChatGPT subscription. Run `codex login`, choose **Sign in with ChatGPT**, then start packetcode with `--provider codex` or select Codex in the provider picker.

See [Providers and models](docs/providers.md) for authentication, model discovery, local Ollama, and custom endpoints.

## SSH Packet Computers

PacketCode can use a registered SSH server as the foreground project
workspace. First verify the server's host-key fingerprint through a trusted
channel, then register it from a normal PacketCode session:

```text
/computers ssh production deploy@example.com /srv/apps/widget --fingerprint SHA256:... --identity ~/.ssh/id_ed25519
```

Start a new remote session:

```bash
packetcode --computer production --provider minimax --model MiniMax-M3
```

The process keeps one pinned SSH connection and SFTP client open. Reading,
searching, listing, writing, patching, and shell commands operate inside the
registered remote root; command `cwd` values remain root-confined. SSH agent
authentication works on Unix and with the Windows OpenSSH agent pipe; explicit
unencrypted identity files and conventional `~/.ssh` keys are also supported.

Remote background agents and workflows are available for process-lifetime
server engineering. A remote foreground session inherits its active computer;
from a local session use `/spawn --computer production ...` or `/workflows run
--computer production review`. Every remote job owns an SSH connection, and a
write job fails closed unless it can create an isolated remote Git worktree.

This is not daemon durability: closing PacketCode does not reconnect or resume
agent loops and cannot guarantee supervision of detached remote descendants.
Remote `/undo`, code-intelligence tools, `@file` expansion, project hooks, and
live heartbeat/status remain unavailable.
See [Packet Computers](docs/feature-packet-computers.md) for security details
and exact boundaries.

## Terminal Workflow

Type a prompt and press `Enter`; use `Ctrl+J` or `\` then `Enter` for a portable newline. `Alt+Enter` also works when the terminal reports Alt distinctly. `Shift+Enter` works only where the terminal maps it to `Ctrl+J`; true shifted-key reporting is reserved for the Bubble Tea v2 migration. Finalized turns are committed to native terminal scrollback while the active response remains in a small live region.

| Key | Action |
| --- | --- |
| `Enter` | Send a prompt. |
| `Ctrl+J` / `\` then `Enter` | Insert a portable newline; `Alt+Enter` also works when Alt is reported distinctly. |
| `Up` / `Down` | Recall prompt history at the first/last input line. |
| `Shift+Tab` | Cycle Manual → Accept Edits → Auto → Plan, including during an active turn. |
| `Left` on an empty idle prompt | Open Agent View. |
| `Ctrl+P` | Open the provider picker. |
| `/model` | Open the model picker; `Alt+M` also works when Alt is reported distinctly. |
| `Ctrl+C` | Cancel the active turn, clear a draft, or quit from an empty prompt. |
| `Ctrl+D` | Quit from an empty prompt. |
| `Ctrl+L` | Clear the visible transcript without deleting the session. |

Prompts submitted during an active turn or compaction queue in order. A failed
turn or compaction pauses pending prompts, and new prompts join that paused
queue. Use `/queue` to review, `/queue drop <n>` to remove an entry,
`/queue resume` while idle to continue, or `/queue clear` to start fresh.

Typing `@` at a token boundary opens project-file completion. The selected `@path` is expanded into bounded, root-scoped file context when the prompt is sent. `@file` expansion is disabled for SSH Packet Computer sessions; use `read_file` there. Typing `/` opens slash-command completion.

## Permission Modes

The mode footer always shows the effective policy:

- **Manual** (`ask`): read-only tools run; edits, commands, MCP tools, and agent spawns ask.
- **Accept Edits** (`accept_edits`): file writes and patches run; shell, MCP, and agent spawns ask.
- **Auto** (`auto`): file edits and shell commands run; MCP and other approval-gated surfaces still ask.
- **Plan** (`read_only` plus plan instruction): research only; mutating tools are denied.
- **Bypass Permissions** (`bypass`): tools run unless an explicit deny rule matches. Enter deliberately with `/trust on` or `--trust`; it is not part of the forward cycle.

Shift+Tab can change mode while a turn is running. The new policy applies to subsequent tool actions and re-evaluates an approval already on screen. A command that has already started is not interrupted.

The approval menu supports arrow keys and numbers:

1. Allow once
2. Allow exact command this session (shell), or allow this tool this session
3. Reject this request

Shell rules match the exact command text in any working directory. Other
tool rules cover all arguments and paths for that tool. Running jobs keep
their existing policy, and explicit denies still apply.

Use `/permissions` to inspect or change the session policy. `/permissions reset`
revokes remembered/session rules and restores the startup policy. See
[Security and permissions](docs/security.md).

## Background Agents and Workflows

`/spawn <prompt>` starts a read-only background agent. `/spawn --write <prompt>` creates an isolated git worktree before allowing writes or commands. Use `--computer <name>` from a local session; remote sessions inherit their active computer. Write jobs never edit the foreground checkout directly.

`/agents` opens the full-screen Agent workspace. It groups agents by needs-input, working, and completed states; supports task entry directly from the bottom prompt; and exposes peek, transcript, cancel, inject, and ignore actions. Results are not silently added to foreground context.
Each job owns a bounded `todo_write` plan; Agent View shows its completed/total
count and current item, and the plan persists with abandoned-job evidence.

After an app exit, running jobs are recorded as abandoned and jobs that never
started as cancelled. `/jobs resubmit` lists eligible saved prompts with full
IDs. Inspect `/jobs <id>` before `/jobs resubmit <id>` starts a new run;
previous work may already have changed files or external services. The old
record is preserved, and concurrent resubmit requests cannot create duplicate
successors.

`/workflows` orchestrates sequential phases and parallel fan-out over the same bounded jobs manager. A built-in review is available immediately:

```text
/workflows run review
/workflows run --computer production review
/workflows run review target="the staged diff"
/workflows validate review
/workflows list
/workflows stop all
```

User workflows live in `~/.packetcode/workflows/*.toml`; local project workflows live in `.packetcode/workflows/*.toml` and take precedence. Remote sessions load built-ins and local user definitions; loading project definitions over SFTP is deferred so the TUI never blocks on workflow discovery.
Workflow TOML is schema-versioned. Optional step verifiers use a fail-closed
structured verdict and bounded retries; verifier jobs and retries count toward
the same agent and token budgets. See [Workflows](docs/workflows.md).

`/loop` repeats normal prompts or slash commands:

```text
/loop Review the current change and continue until complete
/loop 10m /workflows run review
/loop list
/loop stop all
```

Self-paced loops stop on a versioned `packetcode-loop-decision` block or the
legacy `LOOP_DONE` sentinel, and always stop after 25 iterations. Interval
loops run immediately, then on the requested interval; later ticks skip while
foreground work is active or the queue is paused. `/loop stop` removes queued
iterations too. Ctrl+C stops self-paced continuation, including when a success
response was already buffered.

Separately, every agent run has a bounded no-progress detector for repeated
tool calls with identical executed arguments and identical results. It stops
that run with the repeated call named; changing output still counts as progress.

See [Background agents](docs/feature-background-agents.md) and [Agent View](docs/feature-agent-view.md).

## Slash Commands

| Command | Purpose |
| --- | --- |
| `/provider [add [slug]\|slug]` | Open the provider picker, add/update a key, or switch. |
| `/model [id]` | Open the model picker or switch models. |
| `/effort [default\|low\|medium\|high\|xhigh\|max\|ultra]` | Show or set reasoning effort for models that expose it. |
| `/spawn [--computer <name>] [--write] <prompt>` | Start a local or remote background agent. |
| `/agents [id]` | Open Agent View or one transcript. |
| `/jobs [id]` | List jobs or open one transcript. |
| `/jobs resubmit [id]` | Re-run a job abandoned by a previous app exit (new job; the original is not resumed). |
| `/cancel <id\|all>` | Cancel background jobs. |
| `/computers` | List registered Packet Computers. |
| `/computers status <name>` | Show one computer's stored record. |
| `/computers register <name> <root>` | Register a local computer record. |
| `/computers ssh <name> <user@host> <root> --fingerprint <SHA256:...>` | Register a pinned SSH computer. |
| `/computers remove <name> --yes` | Remove a computer record. |
| `/workflows [run [--computer <name>] <name>\|validate <name>\|list\|stop [id\|all]\|<id>]` | Validate, run, and inspect local or remote workflows. |
| `/loop [interval] <prompt\|/command>` | Repeat work; use `list` or `stop`. |
| `/plan [on\|off]` | Toggle read-only planning mode. |
| `/queue [drop <n>\|resume\|clear]` | Inspect, resume, or clear queued prompts. |
| `/sessions` | List, resume, rename, or delete sessions. |
| `/compact [--keep N]` | Summarize older context. |
| `/undo` | Restore the latest file backup. |
| `/cost` | Show or reset tracked API cost. |
| `/permissions` | Inspect or change tool policy; `reset` revokes session rules. |
| `/trust [on\|off]` | Show or set bypass mode. |
| `/ollama [status\|models\|ps\|pull <model>]` | Inspect and manage local/remote Ollama. |
| `/mcp` | Inspect MCP servers, status, tools, and logs. |
| `/statusline [refresh]` | Show or refresh statusline output. |
| `/transcript` | Open the current saved transcript. |
| `/clear` | Clear visible output only. |
| `/help` | Show all keys and commands. |
| `/exit` / `/quit` | Exit packetcode. |

Unknown commands show an error. Prefix a prompt with `//` to send a literal leading slash.

Markdown prompt commands can be installed at `~/.packetcode/commands/<name>.md` or `.packetcode/commands/<name>.md`. Project commands override user commands; built-ins cannot be shadowed.

## Configuration

Minimal hosted configuration:

```toml
[default]
provider = "codex"
model = "gpt-5.6-sol"

[providers.codex]
default_model = "gpt-5.6-sol"

[behavior]
auto_compact_threshold = 80
background_max_concurrent = 4
background_max_depth = 2
background_max_total = 32
background_token_budget = 0
workflow_token_budget = 0

[permissions]
profile = "ask"
```

Zero-config local Ollama defaults to `http://localhost:11434` and automatically selects a bounded `num_ctx`, detects model/tool metadata, and keeps the loaded model warm:

```toml
[default]
provider = "ollama"
model = "qwen2.5-coder:14b"

[providers.ollama]
default_model = "qwen2.5-coder:14b"
```

API keys may be stored in config or provided as `PACKETCODE_<SLUG>_API_KEY`. Environment variables win. See the [full configuration reference](docs/configuration.md).

packetcode names any setting it did not understand — a `schema_version` from a
newer build, or a key no setting matches — at startup and as the
`config.compatibility` check in `packetcode doctor`. It never rewrites
`config.toml`, so a newer file is reported rather than refused. What packetcode
writes itself is governed differently: see the
[compatibility contract](docs/compatibility.md) for every on-disk format, its
version, and what happens when a build meets a file it was not built for.

## Context and Token Use

The statusline context gauge shows current request occupancy, not cumulative billed tokens. Cumulative usage still drives cost. `/cost`, background-job usage, and statusline JSON report cache creation/read tokens separately where providers supply them; those figures are subsets of input, not extra tokens. Automatic compaction includes system prompts and tool-schema estimates, preserves complete recent tool exchanges, and records compaction usage.

To reduce repeated context:

- Older oversized tool results are compacted only in the model-facing copy; the complete result remains in the session and UI.
- Code-intelligence defaults are bounded.
- Anthropic requests mark stable system/tool prefixes for ephemeral prompt caching.
- Background jobs and workflows can use token budgets.

## Skills

A skill is reference material loaded by name. The model loads one mid-turn when
it decides the task calls for it, and you can load one yourself by typing
`/<name>`. Only each skill's name and description sit in the system prompt; the
body enters context only when it is asked for. An unused skill therefore costs
one index line rather than its full text.

packetcode ships skills about its own configuration and reads more from six
directories — its own layout plus the two the wider ecosystem uses, so skills
you already installed for another agent are found where they are:

| Scope | Directories |
| --- | --- |
| Yours | `~/.packetcode/skills/`, `~/.claude/skills/`, `~/.agents/skills/` |
| This repository | `.packetcode/skills/`, `.claude/skills/`, `.agents/skills/` |

Each is a directory holding a `SKILL.md` with a frontmatter `description`. Where
one name appears in two layouts in the same scope, `.packetcode/` wins — that
file was written by someone who knew packetcode would read it.

### Repository skills under another agent's layout are not loaded until you say so

`~/.claude/skills/` is your own directory, so those load on sight. A
**repository's** `.claude/skills/` or `.agents/skills/` does not. It is
discovered, listed by `/skills`, and does nothing else: not offered to the
model, no `/name` command, and the `skill` tool refuses it.

The reason is that this is the one directory you acquire by cloning. A skill is
instructions for the model, its description goes into the system prompt, and its
name would become a command you can type — all from a directory that arrived
with a checkout rather than from anything you installed. So packetcode asks:

```bash
packetcode skills list
```

shows anything pending, and in a session `/skills <name>` shows you where it
came from before you decide. Enable one with `/skills allow <name>`, undo it
with `/skills revoke <name>`.

Approval is per skill, per repository, and bound to the body you approved — if
the repository rewrites that skill afterwards, packetcode asks again rather than
letting the first harmless version buy permanent trust. Approving means "load
this", not "trust this": the body still reaches the model labelled as repository
content.

One asymmetry worth knowing: a repository's own `.packetcode/skills/` still
loads without approval, as it always has. The gate exists to avoid opening a
*new* automatic-loading surface across every repo that already ships
`.claude/skills/`, not to fence off a directory existing projects depend on.

When the same name exists in more than one scope, **your home directory wins
over the repository**, and both win over a built-in. That direction is
deliberate: a project skill is repository content, so if the repo won, opening
a hostile one would silently replace the `deploy` skill you wrote for yourself
with one you have never read — invoked by the same name and the same habit. The
worst case in this direction is that a repository's own skill does not take
effect, which is reported at startup and in `/skills` rather than left to be
inferred. Claude Code orders its scopes the same way.

### Who can invoke a skill

Two optional frontmatter keys decide that, and they are independent. Both
default to permissive, so a skill that sets neither is reachable both ways.

```yaml
---
name: deploy
description: Ship a build to an environment
disable-model-invocation: true
user-invocable: true
---
```

(`name` must match the directory and is required by the Agent Skills spec.
packetcode takes the name from the directory and ignores the field, but a skill
without it fails `skills-ref validate` and will not upload to claude.ai.)

- `disable-model-invocation: true` keeps the skill out of the system-prompt
  index and refuses it at the `skill` tool. The model cannot choose it; you
  still can, with `/<name>`. This is what makes a skill with real-world
  consequences safe to keep beside the others.
- `user-invocable: false` keeps the skill from registering as a typed command.
  It stays background knowledge the model may consult; nobody types it.

A skill can list the tools it expects to use, so a turn that invokes it does not
stop to ask for each:

```yaml
allowed-tools: read_file, execute_command
```

A grant can be narrowed to particular commands, in the form the wider ecosystem
uses. `execute_command` is the only tool this works for, because it is the only
one whose parameters carry a command for a rule to match:

```yaml
allowed-tools: Bash(gh:*), Bash(npm run test:*), execute_command(git status)
```

`Bash(gh:*)` pre-approves commands starting with `gh` and nothing else;
`execute_command(git status)` matches that program byte-for-byte. A prefix grant
never authorises a larger shell program that merely starts with the right word,
so `gh pr list && rm -rf .` still asks.

This is honoured only for **your own** skills — builtin and `~/` scope — never
for a repository's. A project skill pre-approving the tools it then tells the
model to use would be a repo granting itself permission. It converts "ask" to
"allow" and nothing else, so an explicit deny still applies; it lasts only the
turn that invoked the skill; and names that are not packetcode tools grant
nothing and are reported. (packetcode's tool names are its own — `execute_command`,
not `Bash` — so a bare name written for another agent will usually need
translating. A scoped `Bash(...)` does not: the scope bounds what the
translation can produce, so that one is read as `execute_command`.)

`/skills` lists what resolved, with a leading slash on the ones you can type,
and `/skills <name>` says who can reach a particular skill and what resource
files sit beside it. `packetcode skills list` reports the same from the shell.

Typing `/<name>` puts the skill body into the turn as your message, and anything
after the name is appended to it. A `commands/<name>.md` file uses `$ARGUMENTS`
if it has that placeholder; a skill body does not, because for a project skill
that body is repository content and placing the placeholder would let it pull
your words inside its own block.

Built-in commands always win a name collision. **A `commands/<name>.md` file
also wins over a skill of the same name, which is the opposite of Claude Code**
— upstream gives the name to the skill. packetcode prefers the command file on
the grounds that it is one prompt you wrote for this project while a skill may
have arrived with a dependency; if you are porting a project that has both, the
`/name` you get here is not the one you got there. Every displacement is
reported at startup and in `/skills`, so the loser is never silently absent.

(Note that markdown commands themselves still resolve project-over-user, unlike
skills. The two differ because a command file is only ever reached by typing
its name, while a skill is also loaded by the model mid-turn on a description
it matched — so a skill is the one a repository could substitute without you
choosing it.)

A body can refer to those files with `${CLAUDE_SKILL_DIR}`, which expands to
the skill's own directory when the body is handed to the model. (Builtin skills
are embedded in the binary and have no directory, so the variable is left alone
there. `${CLAUDE_PLUGIN_ROOT}` is not substituted: it names a plugin bundle, and
packetcode has no plugin bundles — see [docs/plugins.md](docs/plugins.md).)

A skill may carry resource files beside its body -- `references/`, `categories/`,
`templates/` -- which is how larger published skills keep a method the body only
dispatches to. Loading a skill lists those files; the model reads one by calling
`skill` again with its path. They are served from the resolved skill directory
and confined to it, so this works for user-scope skills that sit outside the
project root without widening what the file tools can reach.

This is the layout used by the wider Agent Skills ecosystem, so skills
published for other agents load here. Some features are unimplemented, and
where they are, packetcode says so rather than passing the text through as
though it had worked:

- `` !`command` `` context injection is **not executed**. Running shell text out
  of a skill body before you see the result is a choice packetcode declines —
  upstream ships a switch to disable it for the same reason. A body using it now
  carries a note beneath it saying nothing ran, so the model does not read
  ``PR diff: !`gh pr diff` `` as a diff it was handed.
- Indexed `$1`/`$2` placeholders are **not substituted**, and are likewise
  reported as unfilled. Plain `$ARGUMENTS` works. A skill's arguments always
  land after the body rather than inside it, because a skill body is framed and
  a project one is repository content.
- `${CLAUDE_PLUGIN_ROOT}` is not substituted: it names a plugin bundle, and
  packetcode has no plugin bundles.
- `allowed-tools` narrowed to particular arguments works for the shell tool and
  nothing else. `Bash(gh:*)` becomes a command-prefix rule; `Read(src/**)` is a
  path glob, which packetcode's policy has no rule shaped like, so it is refused
  rather than widened to the bare tool and the refusal is reported. That scope is
  also the only reason a foreign tool name is translated at all: a bare `Bash`
  carries no bound, matches no registered tool, and grants nothing instead of
  handing a ported skill the whole shell on a guess.
- packetcode refuses a skill with no `description`, which upstream loads by
  falling back to the first paragraph.

Published skills install and load; not all of them work unmodified, and the
ones that do not should tell you why.

`packetcode skills install` is still the way to add a published skill you do
not already have locally:

```bash
packetcode skills install naieum/snitchmarketplace
```

```bash
packetcode skills list
```

`install` takes an `owner/repo` shorthand, a full git URL, or a local path. Add
`--skill NAME` to install a subset, `--project` to write to
`./.packetcode/skills` instead of the user scope, `--force` to overwrite, and
`--ref` to pick a branch or tag. A skill that fails to load is refused with a
reason before anything is copied. Remove one with `packetcode skills remove
NAME`.

Installing a skill does not run it, and nothing in a skill body is an
instruction from you. A project-scope body is labelled as repository content
when it reaches the model, and every action it suggests still passes through
that tool's own approval.

Check the licence of anything you install. Skills are third-party content and
are not all under the same terms as packetcode.

## MCP, Hooks, Themes, and Statusline

- MCP servers are external stdio processes configured under `[mcp.<name>]`; discovered tools use `<server>__<tool>` names and remain policy-gated.
- Streamable HTTP is not enabled. Its reviewed v1 trust contract now pins exact
  origins/address classes, bounded bodyless same-origin redirects and atomically
  bound target-only credential rules, disabled ambient proxies, system-root
  TLS, bounded resources,
  credential-bound untrusted output provenance/redaction, per-call approval,
  revocation, and manual failure recovery before a transport implementation can
  land.
- Hooks run on user prompt submission, before a tool, or after a tool.
- A native Claude Code-style statusline is enabled by default. A custom command can consume a Claude-compatible JSON snapshot.
- `~/.packetcode/theme.toml` overrides semantic colors and provider colors.

See [MCP servers](docs/mcp.md), [Hooks and statusline](docs/hooks-and-statusline.md), and [Theming](docs/feature-theming.md).

## Documentation

- [Maintainer handoff](HANDOFF.md)
- [Developer guide](docs/development.md)
- [Maintenance and recovery](docs/maintenance.md)
- [Audit handoff and remaining decisions](docs/handoff.md)
- [User manual](docs/manual.md)
- [Advanced guide](docs/advanced-guide.md)
- [Terminal cheat sheet](docs/cheat-sheet.md)
- [Offline HTML5 manual](docs/packetcode-manual.html)
- [Getting started](docs/getting-started.md)
- [Providers and models](docs/providers.md)
- [Configuration reference](docs/configuration.md)
- [Security and permissions](docs/security.md)
- [Background agents](docs/feature-background-agents.md)
- [Agent View](docs/feature-agent-view.md)
- [Workflows and verification](docs/workflows.md)
- [Code intelligence](docs/code-intelligence.md)
- [MCP servers](docs/mcp.md)
- [Streamable HTTP MCP trust contract](docs/mcp-http-trust-contract.md)
- [Hooks and statusline](docs/hooks-and-statusline.md)
- [Troubleshooting](docs/troubleshooting.md)
- [TUI parity harness](docs/tui-parity-harness.md)
- [Supported terminals](docs/supported-terminals.md)
- [Packet Computers registry](docs/feature-packet-computers.md)
- [Backlog](BACKLOG.md)
- [Packet Computers and Packet Control proposal](PACKETCOMPUTERS.md) — product
  definition and the full six-phase arc. Packet Control is implemented in
  PacketADE, not here; Packet Computers Phases 1–2 are tracked in
  [docs/packet-computers-loop.md](docs/packet-computers-loop.md).

## Development

Start with [the developer guide](docs/development.md) for code ownership,
lifecycle contracts, focused tests, and documentation upkeep. Read
[HANDOFF.md](HANDOFF.md) for the current baseline and remaining work. Run
process-heavy checks sequentially so compilation and lint do not compete with
timer-sensitive tests.

```bash
make verify
go vet ./...
go test ./...
go test -race -count=1 ./...
make lint
make build
make smoke
make tui-snapshots
make tui-golden-check
```

The PTY commands require Linux, macOS, or WSL and the dependencies in
`scripts/requirements-tui.txt`. Documentation-only edits need link, command,
and consistency checks; changes to runtime behavior also need the relevant
tests and CI checks.

The credential-free `--tui-fixture=<state>` development flag renders deterministic lifecycle states for PTY snapshots without loading config, providers, credentials, sessions, hooks, MCP, or project files.

## License

MIT — see [LICENSE](LICENSE).
