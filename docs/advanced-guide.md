# Packetcode Advanced Guide

This guide is for users who already know how to start packetcode and want to operate it deliberately: choose providers per task, control reasoning and permissions, fan work out to background agents, recover their results, and understand what is stored or sent to a model.

It documents the current `main` branch. Features marked **Limit** are not implemented yet; they are not configuration hints or hidden commands. For a shorter introduction, start with [Getting Started](getting-started.md). For every configuration field, see [Configuration](configuration.md).

## 1. Mental Model

Packetcode is one foreground coding session plus a bounded background job system.

```text
terminal input
    |
    v
Bubble Tea app ---- provider registry ---- active model stream
    |                    |
    |                    +---- OpenAI / Codex / Anthropic / ... / Ollama
    |
    +---- foreground agent ---- native tools ---- project root
    |          |                  |
    |          |                  +---- permission policy and approvals
    |          +---- session transcript, usage, backups
    |
    +---- jobs manager ---- background sessions
    |          +---- read-only jobs inspect the project root
    |          +---- write jobs use isolated git worktrees
    |
    +---- workflow engine ---- ordered phases and parallel job fan-out
    |
    +---- MCP manager / hooks / optional statusline process
```

The foreground agent owns the visible conversation. Background agents have independent sessions, context, provider/model selection, usage, cancellation, and transcripts. A completed background result is never silently copied into the foreground model context: you choose whether to inspect, inject, collect, or ignore it.

Provider streams and background callbacks enter the UI through messages; the terminal event loop remains the single owner of visible UI state. Finalized turns go to terminal-native scrollback. Only the active response/tool operation stays in the live region.

## 2. State and Project Roots

Packetcode resolves the current directory to the containing git repository root when possible. Native file tools are rooted there. Important user state lives under `~/.packetcode/`:

| Path | Purpose |
| --- | --- |
| `config.toml` | Providers, behavior, permissions, MCP, hooks, and statusline configuration. |
| `sessions/` | Foreground and background conversation transcripts. |
| `jobs/` | Persisted background-job snapshots and bounded artifact manifests. |
| `worktrees/` | Isolated checkouts for write-capable background jobs. |
| `commands/` | User Markdown prompt commands. |
| `workflows/` | User workflow definitions. |
| `theme.toml` | Optional semantic color overrides. |
| `cost-tally.json` | Cumulative usage/cost high-water marks by session. |

Project-specific commands and workflows live at `.packetcode/commands/` and `.packetcode/workflows/`. Project definitions override user definitions of the same name. Built-in slash commands cannot be shadowed.

Codex subscription credentials are separate: packetcode reads the Codex CLI store at `$CODEX_HOME/auth.json`, or `~/.codex/auth.json` when `CODEX_HOME` is unset.

## 3. Starting Deliberate Sessions

Command-line flags are session overrides; they do not rewrite the saved default unless an in-app command explicitly persists a setting.

```bash
# Use configured defaults
packetcode

# Select a provider and model for this run
packetcode --provider codex --model gpt-5.6-sol

# Start in a named permission profile
packetcode --permission-mode read-only
packetcode --permission-mode accept-edits
packetcode --permission-mode auto

# Deliberate bypass for this run
packetcode --trust

# Resume by full session id (or use /sessions inside the TUI)
packetcode --resume <session-id>

# Build/version diagnostics
packetcode --version
packetcode doctor
packetcode doctor --json
packetcode doctor --check providers,permissions
packetcode run --permission-mode read-only --json "summarize this repository"
packetcode skills list
packetcode acp
packetcode sugar login
```

Supported command-line permission names are `ask`, `accept-edits`, `auto`, `read-only`, and `bypass`. `doctor --check` accepts a section or exact check ID, can be repeated, and can contain comma-separated values.

The public command families are `run`, `doctor`, `skills`, `acp`, and `sugar`.
`packetcode run [--provider NAME] [--model MODEL] [--permission-mode MODE]
[--resume ID] [--json] <prompt...>` executes one non-interactive turn through
the runtime shared with the TUI and ACP paths. The prompt is positional; stdin,
`--trust`, `--computer`, and ephemeral-session modes are intentionally absent.
An unavailable approval fails closed with exit 3, cancellation exits 130, and
plain stdout contains only the sanitized final response. JSON is one
`schema_version: 1` document with outcome, session/provider/model identity,
output, total elapsed milliseconds, per-run input/output/cache usage, and an
error on failure. Use `--help` for the exact command-local flags.
When a session exists, failure diagnostics include its ID and an interactive
resume command. Review saved history from the same working directory before
continuing, since completed actions are not rolled back. Plain stdout remains
empty on failure; JSON may contain incomplete output with `ok: false`.

The foreground and ACP registries include root-scoped file/search/code-
intelligence tools, `execute_command`, a bounded HTTP(S) `fetch`, per-session
`todo_write`, and skill retrieval. Fetch validates the post-DNS destination on every
connection and redirect, blocks private/loopback targets by default, disables
ambient proxies, caps headers/body/time, and frames returned content as
untrusted evidence. It is not a download or general web-search surface, and is
intentionally unavailable to background agents whose URL choices are not being
watched directly.

## 4. Provider Strategy

Packetcode registers providers that are usable from the current configuration. Hosted providers requiring API keys are omitted when their key is absent. `codex` and `ollama` are keyless from packetcode's perspective: Codex uses its OAuth file and Ollama uses a reachable daemon.

Useful session commands:

```text
/provider                 open the provider picker
/provider codex           switch to a configured provider
/provider add openai      add or update credentials
/model                    open the active provider's model picker
/model gpt-5.6-sol        switch directly by model id
```

`Ctrl+P` opens the provider picker. `/model` is the portable model-picker path; `Alt+M` also works when the terminal reports Alt distinctly. `Ctrl+M` is intentionally unbound under Bubble Tea v1 because terminals encode it as carriage return and it can be mistaken for Enter. The picker model list comes from the active account where possible; curated fallback catalogs keep selection usable when a provider's list endpoint is unavailable. The next model request is still authoritative.

### API keys and custom endpoints

For a built-in provider, `PACKETCODE_<SLUG>_API_KEY` overrides `api_key` in the config. Non-alphanumeric slug characters become underscores. Custom OpenAI-compatible providers can name a different variable with `api_key_env`.

```toml
[providers.lab]
type = "openai_compatible"
display_name = "Lab Gateway"
base_url = "https://models.example.internal/v1"
api_key_env = "LAB_MODEL_TOKEN"
default_model = "coder"

[[providers.lab.models]]
id = "coder"
context_window = 65536
supports_tools = true
input_per_1m = 0
output_per_1m = 0
```

Static model entries are fallbacks for endpoints whose `/models` response is missing or incomplete. Use HTTPS for hosted gateways. Plain HTTP is suitable only for endpoints you control on a local or private network.

See [Providers and Models](providers.md) for every built-in slug and its [configuration examples](providers.md#config-example) for local Ollama tuning.

## 5. Codex Subscription and Reasoning Effort

The `codex` provider is the direct ChatGPT-subscription path; it is not the OpenAI API-key provider.

```bash
# One-time authentication through the official Codex CLI
codex login

# Then start packetcode with the subscription provider
packetcode --provider codex --model gpt-5.6-sol
```

Packetcode rereads `auth.json` for each request. If the access token is rejected as expired, it uses the stored refresh token and atomically updates the same file while preserving unrelated fields. API-key-only Codex CLI auth is not accepted by the subscription provider.

Reasoning-capable Codex models expose an optional controller:

```text
/effort                         show effective and available levels
/effort high                    set and persist for the provider
/effort ultra                   use the highest advertised level
/effort default                 clear the override
```

The current Codex catalog for GPT-5.6 Sol/Terra/Luna advertises `low`, `medium`, `high`, `xhigh`, `max`, and `ultra`. The default is model metadata: for example, the static GPT-5.6 Sol fallback defaults to `low`. Packetcode validates the requested level against the selected model, saves the override as `[providers.codex].reasoning_effort`, and shows the effective level in the native statusline.

```toml
[default]
provider = "codex"
model = "gpt-5.6-sol"

[providers.codex]
default_model = "gpt-5.6-sol"
reasoning_effort = "high"
```

**Limit:** `/effort` only works for providers and models that expose reasoning controls. It refuses changes during an active foreground stream; finish or cancel the turn first. “Ultra” is not intrinsically better for every task—use lower levels for mechanical reads and high levels for architecture, subtle debugging, or synthesis.

## 6. Permission Policies

Permissions decide whether a tool call is allowed, shown for approval, or denied. They are not an operating-system sandbox: allowed commands and external processes run as your user.

| TUI mode | Config profile | Automatic behavior |
| --- | --- | --- |
| Manual | `ask` / `balanced` | Reads/searches/lists; asks for writes, shell, MCP, and model-initiated `spawn_agent`. |
| Accept Edits | `accept_edits` | Also allows native file writes/patches; asks for shell, MCP, and model-initiated `spawn_agent`. |
| Auto | `auto` | Also allows shell; MCP and explicitly gated surfaces still ask. |
| Plan | `read_only` | Allows research tools and denies mutation, with a planning instruction. |
| Bypass Permissions | `bypass` | Allows tools unless an explicit deny rule matches. |

`Shift+Tab` cycles Manual → Accept Edits → Auto → Plan, including during a turn. The new policy applies to subsequent calls and re-evaluates an approval currently on screen. It cannot interrupt a command that has already started. Bypass is intentionally outside this cycle; enter it with `/trust on` or `--trust`.

Direct `/spawn` commands are explicit user actions and do not trigger an additional spawn approval. The resulting agent's own tool calls remain governed by its captured policy, and write jobs still require isolated worktrees.

Inspect or alter session policy without rewriting config:

```text
/permissions
/permissions profiles
/permissions profile ask
/permissions explain execute_command
/permissions rule execute_command ask
/permissions reset
/trust
/trust on
/trust off
```

For persistent fine-grained rules:

```toml
[permissions]
profile = "accept_edits"

[[permissions.rules]]
tool = "execute_command"
action = "deny"
command_prefix = ["rm", "-rf"]
reason = "block broad recursive deletion"

[[permissions.rules]]
tool = "filesystem__*"
action = "ask"
reason = "review calls through the filesystem MCP server"
```

Explicit deny rules are safety floors. Among the remaining matching rules, evaluation runs from last to first, so the last match wins. Plan mode also has a hard read-only floor: allow or ask rules cannot authorize a mutating tool while Plan is active. Exact tool names, suffix wildcards, `mcp:*`, `*`, exact command strings, and tokenized command prefixes are supported. “Approve and remember” records an exact command for `execute_command`; it does not infer a broader shell family. `/permissions reset` revokes remembered/manually added session rules and restores the startup policy.

For the complete matcher syntax and threat model, read [Security and Permissions](security.md).

## 7. Background Agents and Parallel Work

Use agents for independent investigations with clear deliverables. A good fan-out minimizes duplicated context and gives each agent a different evidence target.

```text
/spawn inspect authentication correctness; report file:line findings only
/spawn --provider codex --model gpt-5.6-sol audit concurrency and cancellation
/spawn review docs against the current CLI and list mismatches
/spawn --computer production inspect the deployed service
```

Each job owns a bounded `todo_write` list. Agent View shows completed/total
items and the current in-progress (or next pending) item, and persists the list
with the job record so an abandoned run retains that evidence.

`/spawn` flags must precede the prompt. Extra jobs queue when the concurrency cap is full.

### Remote Packet Computer placement

A remote foreground session defaults its jobs to the active Packet Computer. From a local session, place a job explicitly:

```text
/spawn --computer production audit the server configuration
/spawn --computer production --write migrate the application
```

Packetcode resolves the computer before queueing and freezes its ID, endpoint/root identity, and working directory into the job. Nested agents inherit that binding and cannot pivot to another computer. Every active remote job owns a separate SSH/SFTP connection, preserving parallelism between workflow siblings. Computer write/shell policy only restricts the captured session policy; it never broadens it.

This is process-lifetime execution. There is no remote PacketCode daemon yet, and there is no reconnect-and-continue loop at all — jobs do not survive a PacketCode restart, which is out of scope rather than pending (ruled 2026-08-14; durable execution after the originating app closes belongs to PacketAgent). An app exit or lost SSH connection leaves local evidence that can be inspected or resubmitted as a new job, but it does not resume the original agent. Packetcode also cannot guarantee termination of detached remote descendants.

```toml
[behavior]
background_max_concurrent = 4
background_max_depth = 2
background_max_total = 32
background_default_provider = ""
background_default_model = ""
background_token_budget = 0
workflow_token_budget = 0
```

- `background_max_concurrent` bounds simultaneous jobs.
- `background_max_depth` bounds nested `spawn_agent` delegation.
- `background_max_total` caps jobs created in one process run.
- Empty background provider/model values inherit the active foreground pair.
- `background_token_budget` is checked at completed provider/tool boundaries; `0` disables it.
- `workflow_token_budget` prevents later steps from starting after the aggregate boundary. An already-running parallel step may finish above it.

The model-facing `spawn_agent` tool uses the same manager. A synchronous spawn can wait for a compact result; asynchronous results can be joined with `collect_agent_results`. Built-in profiles classify collection as read-only and allow it automatically, though an explicit session rule can require approval.

### A practical fan-out pattern

1. Ask three read-only agents for non-overlapping analyses: correctness, tests, and docs/security.
2. Continue local inspection while they run.
3. Open `/agents`, peek at active work, and open full transcripts only where useful.
4. Inject or collect the bounded results.
5. Synthesize in the foreground before assigning any write job.

This avoids concurrent writers touching the same files. For a repeatable version, use a parallel workflow.

### Agent View

Open `/agents`, or press Left Arrow from an empty idle prompt. In list mode:

| Key | Action |
| --- | --- |
| `n` | Focus the new-task composer. |
| `Up`/`Down`, `j`/`k` | Move selection. |
| `p` | Peek at output. |
| `Enter` or `o` | Open the transcript. |
| `c` | Cancel an active job. |
| `i` | Inject a terminal result into foreground context. |
| `x` | Ignore a terminal result. |
| `Esc` | Clear a composer draft, return to the list, or close the view. |

Live transcript refresh preserves your scroll position unless you were already at the bottom. Jobs are grouped by needs-input, working, completed, failed, cancelled, and abandoned state.

### Result lifecycle

When the foreground handles a terminal completion notification, it marks that result seen; merely opening a transcript is not the transition. Injecting adds a bounded handoff to the foreground, explicit parent collection marks it consumed, and ignoring marks it ignored. Artifact manifests summarize changed files, commands/tests, searches, child jobs, worktree metadata, bounded previews, and errors. They are pointers and summaries—not replacements for the full job transcript or worktree.

**Limits:** jobs active during an unclean exit recover as cancelled rather than resuming. Arbitrary sub-agent clarification questions are not implemented. Agent output remains in its transcript rather than streaming into foreground chat.

See [Background Agents, Loops, and Workflows](feature-background-agents.md) and [Agent View](feature-agent-view.md).

## 8. Write Jobs and Worktree Results

Use a write job only when its file ownership is clear:

```text
/spawn --write implement the parser fix and run its focused tests
/spawn --provider codex --model gpt-5.6-sol --write update only docs/mcp.md
/spawn --computer production --write migrate the service
```

Write jobs require a usable git repository. They create:

```text
~/.packetcode/worktrees/<repo-key>/<job-id>
branch: packetcode-job-<job-id>
base: current HEAD
```

The job never writes to the foreground checkout. The base is committed `HEAD`; uncommitted foreground changes are not copied into the job worktree. On a Packet Computer, the worktree is created under that remote user's PacketCode state directory. If local or remote worktree creation fails, the job fails closed.

After completion, use the path printed by `/agents` or `/jobs <id>`:

```bash
# Inspect worktree state and changes
git -C <worktree-path> status --short
git -C <worktree-path> diff
git -C <worktree-path> log --oneline --decorate -5

# If the agent made a commit, integrate it deliberately from the foreground repo
git cherry-pick <commit>
```

If the job left uncommitted edits, review and commit them in its worktree, or transfer the diff manually. Packetcode does not merge branches, copy uncommitted edits, remove worktrees, or delete branches automatically. Do not assume a completed result means a commit exists.

When several write jobs are necessary, assign disjoint files or run them sequentially. Parallel worktrees prevent direct checkout corruption, but overlapping changes can still create difficult merges.

## 9. Workflows and Loops

Workflows give repeated orchestration a declarative shape. Phases execute in order; steps within a phase execute in order; a `parallel` step creates one job per `fan_out` item and joins their results.

```text
/workflows list
/workflows validate focused-review
/workflows run review
/workflows run --computer production review
/workflows run review target="the staged diff"
/workflows <run-id>
/workflows stop <run-id>
/workflows stop all
```

In a remote foreground session, work agents, retries, and verifiers inherit the active computer. From a local session, `--computer <name>` routes the entire run to one registered computer. Remote sessions load built-in and local user workflow definitions; remote project definition discovery is deferred to avoid blocking the TUI on SFTP. Each write-enabled workflow agent gets a separate worktree, so later steps see its summary rather than unmerged files; use one write step for a cohesive migration.

Example `.packetcode/workflows/focused-review.toml`:

```toml
schema_version = 1
name = "focused-review"

[inputs]
target = "the current working tree"

[[phases]]
name = "analysis"

[[phases.steps]]
name = "review"
mode = "parallel"
bind = "review"
fan_out = ["correctness", "security", "test gaps"]
prompt = "Review {{.inputs.target}} for {{.item}}. Return evidence with file paths."

[[phases]]
name = "synthesis"

[[phases.steps]]
name = "synthesize"
mode = "single"
prompt = "Deduplicate and prioritize these findings:\n\n{{.steps.review}}"

[phases.steps.verify]
prompt = "Verify attempt {{.attempt}} against the requested review. Candidate:\n{{.result}}"
provider = "codex"
model = "gpt-5.6-sol"
pass_contract = "packetcode-workflow-verdict-v1"

[phases.steps.retry]
max = 2
```

Every workflow TOML file declares `schema_version = 1`; missing, newer, or
unknown schema fields fail validation. Optional work-step fields are
`provider`, `model`, `system_prompt`, and `allow_write`.
`continue_on_error = true` is a phase option.

The verifier is a separate read-only job. It must emit the versioned
`packetcode-workflow-verdict-v1` block with an exact `pass` or `fail` verdict;
missing, malformed, or unknown verdicts fail closed. `retry.max` counts
additional attempts and defaults to zero. Verifier feedback is appended to a
retry, and every work/verifier attempt counts toward both the 16-agent guard
and aggregate token budget. The explicitly selected verifier provider receives
bounded work summaries and artifact previews. A step without `[verify]` is explicitly
**unverified**. See [Workflows](workflows.md) for the schema, contract, and
boundary behavior. Cancellation cascades to all registered children.

Loops repeat a foreground prompt or slash command:

```text
/loop Continue reviewing until complete
/loop 10m /workflows run review
/loop list
/loop stop <id>
/loop stop all
```

Self-paced loops ask for a versioned `packetcode-loop-decision` JSON block and
also accept the legacy `LOOP_DONE` sentinel. They stop after 25 iterations even
when the model never returns a valid stop decision. Interval loops run
immediately and then on the interval. A tick during foreground activity is
queued; loops do not overlap the active foreground turn.

The 25-turn scheduler ceiling is not the only loop guard. Foreground,
background, and ACP runs also share a bounded no-progress detector that stops
repeated tool calls with identical executed arguments and identical results,
and names the repeated call in the error. A changing result is progress.

**Limits:** explicit pipeline stages beyond ordered phases/steps are not
shipped. Verification and retries are process-local orchestration, and a loop
is process-local scheduling—not a durable daemon.

## 10. Context, Compaction, and Cost

Three counters answer different questions:

| Counter | Meaning |
| --- | --- |
| Context occupancy | Estimated/current tokens in the next model-facing request. Can fall after compaction. |
| Session input/output totals | Cumulative provider-reported usage for that session. |
| Cost tally | Cumulative estimated USD from provider/model pricing and session totals. |

Where providers report them, cache creation/read counts flow through session
usage, `/cost`, background jobs, and native/custom statusline snapshots. They
are subsets of input tokens, not addends; the uncached input is the remainder.

The request estimate includes the system prompt, transcript, tool-call arguments/results, tool schemas, and pending additions such as expanded `@file` context. Provider-reported usage replaces the estimate when available. Unknown model context windows produce no meaningful percentage.

```text
/compact                    summarize older context; keep 10 recent messages
/compact --keep 20          preserve a larger recent tail
/cost                       show the top cost entries and total
/cost reset --yes           irreversibly reset the cost tally
```

Automatic compaction runs before a turn when request occupancy crosses `auto_compact_threshold` and there is enough older history to summarize. Compaction:

- makes a real request to the active model and records its usage;
- summarizes older conversation into one assistant message;
- preserves the selected recent tail and complete tool-call/result groups;
- updates the persisted session only after the summary succeeds;
- can be cancelled with `Ctrl+C`.

Older oversized tool results may also be reduced in the model-facing copy while their complete content remains in persisted transcripts. This reduces repeat context without destroying the audit trail.

**Cost caveat:** pricing tables are estimates and unknown/local models may report `$0`. Subscription consumption and provider invoices remain authoritative.

## 11. MCP Servers

MCP servers are configured stdio child processes. Packetcode starts enabled servers during startup, performs `initialize`, sends `notifications/initialized`, loads `tools/list`, and registers each tool as `<server>__<tool>`.

```toml
[mcp.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/project"]
enabled = true
timeout_sec = 20
env_from = ["FILESYSTEM_SERVICE_TOKEN"]

[mcp.filesystem.env]
LOG_LEVEL = "warn"
```

Only a small environment allowlist plus values named by `env_from` and explicit `env` entries reach the child. Explicit `env` wins over `env_from`. The timeout applies to initialization, discovery, and calls.

Operational commands:

```text
/mcp
/mcp status filesystem
/mcp tools filesystem
/mcp logs filesystem
/mcp restart filesystem
```

MCP calls participate in normal permissions. Match all MCP tools with `mcp:*` or one server with `filesystem__*`. Displayed logs use a bounded, redacted tail.

`/mcp restart <name>` reconnects one server using the configuration loaded at
PacketCode startup and refreshes its tool adapters. Configuration changes still
require a PacketCode restart. MCP servers are trusted local programs, not
sandboxed plugins. See [MCP Servers](mcp.md).

Streamable HTTP remains disabled. Its approved
[trust contract](mcp-http-trust-contract.md) requires an exact origin and
address-class allowlist, bounded bodyless same-origin redirect validation,
atomically bound environment-sourced target-only credentials, labelled/
redacted/capped tool-role output, per-call approval, and manual reconnect. A
transport-independent validator pins these decisions,
but the current config still accepts only stdio commands.

### ACP server surface

`packetcode acp` runs the same engine over local stdio. In addition to the ACP
session lifecycle and prompt stream, versioned `_packetcode/*` extensions expose
provider/model overrides, saved-session list/load/rename, usage, permission
mode, configured MCP tools, project file search, Markdown prompt-command
discovery/expansion, and explicit session close. ACP agents receive the same
skill index/tool path as the TUI runtime. User-invocable skills are not yet
listed in `_packetcode/commands/list`; that catalogue currently contains
Markdown prompt commands only.

## 12. Hooks and Statusline

Hooks and custom statusline commands run as your user in the project root: PowerShell on Windows and `sh -c` elsewhere. Treat their configuration as executable code.

```toml
[[hooks.user_prompt_submit]]
command = "cat .packetcode-context 2>/dev/null || true"
timeout_sec = 2

[[hooks.pre_tool_use]]
matcher = "execute_command"
command = "python scripts/guard-command.py"
timeout_sec = 5

[[hooks.post_tool_use]]
matcher = "patch_file"
command = "gofmt -w $(git diff --name-only -- '*.go') 2>/dev/null || true"
timeout_sec = 10
```

- `user_prompt_submit`: successful stdout becomes extra prompt context.
- `pre_tool_use`: a non-zero exit blocks the tool.
- `post_tool_use`: successful stdout is appended to the result; failures are reported in appended hook output.

Hook and statusline stdout/stderr are capped at 64 KiB.

The built-in statusline is active with no configuration. It prioritizes operation state, active jobs, provider/model, reasoning effort, context occupancy, project/branch, and cost while shedding lower-priority segments on narrow terminals.

To replace it:

```toml
[statusline]
command = "jq -r '\"\\(.provider.display_name) · \\(.model.id) · \\(.model.reasoning_effort) · \\(.context_window.used_percentage)%\"'"
timeout_sec = 2
```

The command receives JSON on stdin. Packetcode-native fields include `working_dir`, `provider`, `model.id`, `model.reasoning_effort`, context used/max/percentage, cumulative cost, active jobs, operation state, queued input count, duration, and version. Claude Code-compatible aliases include `cwd`, `model.display_name`, `context_window.context_window_size`, and `context_window.current_usage`.

```text
/statusline
/statusline refresh
```

A failed, empty, or timed-out custom renderer falls back to the native line. See [Hooks and Statusline](hooks-and-statusline.md) for payload schemas and Windows examples.

## 13. Terminal and TUI Behavior

The input is a bounded multiline editor. `max_input_rows` controls its displayed height, not the maximum prompt size.

| Input | Behavior |
| --- | --- |
| `Enter` | Submit. |
| `\` then `Enter` | Insert a portable newline in every input state; `Alt+Enter` also works when Alt is reported distinctly. |
| `Ctrl+J` | Insert a newline while completion is closed; move down when the popup is open. |
| `Shift+Enter` | Insert a newline only when the terminal maps it to `Ctrl+J`. |
| `Up`/`Down` | Recall history at the first/last visual input row. |
| `Ctrl+C` | Cancel active work, otherwise clear a draft, otherwise quit. |
| `Ctrl+D` | Quit from an empty prompt. |
| `Ctrl+L` or `/clear` | Clear visible output without deleting the session. |

Prompt submission during a foreground turn or compaction queues the text. Manage
it with `/queue`, `/queue drop <n>`, `/queue resume`, and `/queue clear`. A failed
turn or compaction pauses pending prompts; new prompts join that paused queue.
Review it before resuming while idle, or clear it to start fresh.

Type `/` for command completion. Use `//` when the intended prompt literally begins with `/`. Type `@` at a token boundary for project-file completion. On submit, selected `@path` mentions expand into bounded, root-scoped file context; the model receives the file contents as part of that turn.

Provider and tool text is sanitized before rendering: terminal control sequences, mouse-mode toggles, clipboard operations, unsafe control bytes, destructive carriage-return progress rewrites, and malformed split UTF-8 are removed or normalized. This protects terminal state, but it does not make the underlying command safe—the permission policy still matters.

Finalized output belongs to native terminal scrollback. Use terminal scrolling, `Shift+PageUp`, or tmux copy mode; `/transcript` opens the saved session. Full-screen pickers, Agent View, workflow view, and transcript view own the keyboard while open.

The committed 72×24/100×30 golden and protocol contract is described in
[Supported terminals](supported-terminals.md) and the
[TUI parity harness](tui-parity-harness.md).

**Limit:** the current terminal stack cannot reliably distinguish every modified Enter sequence. Backslash-Enter works in every input state; `Ctrl+J` works while completion is closed; `Alt+Enter` works when the terminal reports Alt distinctly.

## 14. Security Boundaries

Know which protections are policy, containment, or display hardening:

- Native file tools canonicalize project paths and reject directory/symlink escapes.
- Read tools bound output and avoid binary content where appropriate.
- Permission policy gates tool invocation; it does not sandbox an allowed process.
- `execute_command`, hooks, statusline commands, and MCP servers run with your OS account's authority.
- Cancelled local commands report the teardown mechanism, confirmation state,
  and surviving PIDs. POSIX process groups and Windows Job Objects provide that
  evidence; SSH can only signal the channel leader and is reported as
  unconfirmed.
- Read-only agents use policy denial, while write agents add git-worktree isolation.
- A worktree isolates repository files but not network access, credentials, or arbitrary paths available to an allowed shell command.
- Custom model endpoints receive conversation content, system instructions, tool schemas, and tool results.
- `@file` expansion sends the selected file content to the active provider.
- Output sanitization protects terminal rendering, not data confidentiality.
- Explicit deny rules continue to apply in bypass mode.

Do not enable `--trust` for unfamiliar repositories. Review `.packetcode/`, hooks, workflow definitions, and MCP commands before using permissive modes. Keep secrets in environment variables or provider credential stores; do not attach them with `@` or paste them into prompts.

## 15. Diagnostics and Troubleshooting

Start with a focused doctor report:

```bash
packetcode doctor
packetcode doctor --check config
packetcode doctor --check providers
packetcode doctor --check permissions
packetcode doctor --check project,state.worktrees
packetcode doctor --json > packetcode-doctor.json
```

Common investigations:

### Codex is not available

```bash
codex login
packetcode doctor --check providers
```

Confirm `$CODEX_HOME/auth.json` or `~/.codex/auth.json` contains a ChatGPT login, not only an API key. Packetcode validates availability locally and refreshes reactively on a request.

### A job is queued forever

Open `/agents` and check how many jobs are active. Compare against `background_max_concurrent`; inspect failed jobs for provider or worktree errors. Cancel stale work with `/cancel <id>` or `/cancel all`.

### A write job does not see foreground edits

This is expected: its base is current committed `HEAD`. Commit the necessary base first, or keep the task in the foreground. Do not use a write job as a transparent clone of a dirty checkout.

### Context rises unexpectedly

Run `/cost` and `/compact`, inspect repeated `@file` attachments and large MCP/tool results, and verify context metadata in `/model`. Remember that tool schemas are resent and contribute to occupancy even though they are not normal transcript messages.

### Output looks truncated

Live and model-facing output is bounded. Open `/jobs <id>` for a job transcript or `/transcript` for foreground history; inspect the actual worktree and files for full data.

### MCP or hook failure

Use `/mcp status <name>` and `/mcp logs <name>`. Hooks and custom statusline commands use the platform shell, so test the same command in PowerShell on Windows or `sh` elsewhere. Restart packetcode after MCP config changes.

### Model or provider switch fails

Use the pickers to discover exact IDs, then run `packetcode doctor --check providers`. A fallback model listing does not guarantee the account can invoke the model.

See [Troubleshooting](troubleshooting.md) for more symptom-oriented recipes.

## 16. Advanced Operating Patterns

### Fast foreground, deep background

Use a lower reasoning effort for mechanical foreground work and assign bounded high-effort reviews in parallel:

```text
/effort medium
/spawn audit the change for correctness; cite exact files and lines
/spawn inspect cancellation, races, and cleanup paths
/spawn compare docs and tests against shipped behavior
```

Collect only after completing your own local pass. This reduces idle time and avoids anchoring every reviewer on one interpretation.

### Research, synthesize, then write

1. Enter Plan mode.
2. Fan out read-only investigation.
3. Inject the useful results.
4. Ask the foreground agent for one reconciled design.
5. Switch to Accept Edits or Auto.
6. Implement in one checkout, or assign disjoint write jobs.
7. Run a fresh read-only verification fan-out.

This separates evidence gathering from mutation and makes permission transitions explicit.

### Provider specialization

The foreground provider does not need to match background providers. Keep a long-context or subscription model in the foreground, use a fast model for searches/tests, and reserve deeper reasoning for synthesis:

```text
/spawn --provider gemini --model gemini-2.5-flash inventory all call sites
/spawn --provider codex --model gpt-5.6-sol reason about the concurrency invariant
```

Provider/model IDs must be configured and valid for the associated account. Per-job usage and cost remain separate even when results are injected.

### Guarded automation

Combine an `accept_edits` base profile with explicit command rules and a pre-tool hook. This permits routine patches while forcing shell review and giving organization-specific policy a fail-closed hook. Use workflows for a known finite fan-out; use loops only when repeated foreground evaluation is genuinely needed.

### Context-efficient handoffs

Ask agents for conclusions, evidence paths, commands run, and unresolved risks—not raw logs. Let artifact manifests point to the transcript/worktree. Inject only results that affect the foreground decision, then compact after a major phase boundary rather than after every turn.

## 17. Shipped Behavior vs. Current Limits

| Area | Shipped | Not yet shipped |
| --- | --- | --- |
| Automation | `packetcode run` one-turn execution, session resume, fail-closed approval, versioned JSON with total elapsed time and usage. | Optional provider/tool/approval phase timing for benchmark attribution. |
| Agents | Bounded concurrent jobs, nested depth, persistence, cancellation, live transcripts, result lifecycle. | Resuming active execution after restart; arbitrary clarification questions. |
| Write isolation | Dedicated worktree and branch from committed `HEAD`. | Automatic apply/merge, cleanup, conflict resolution, dirty-checkout cloning. |
| Workflows | Versioned schema, offline validation, sequential phases, single/parallel steps, fan-out join, fail-closed step verifiers, bounded retries, cancellation, budgets. | Explicit pipeline stages and a broader versioned example library. |
| Terminal | Native scrollback, bounded live region, sanitized output, multiline fallbacks. | Uniform true Shift+Enter reporting across terminals. |
| MCP | Stdio startup/discovery/calls, namespaced tools, policies, logs, per-server restart; reviewed Streamable HTTP trust contract/validator. | The Streamable HTTP transport itself, live config reload, prompts/resources. |
| Reasoning | Codex catalog-driven `/effort` controls and status display. | A universal reasoning control for every provider/model. |
| Security | Root-scoped native file tools, approvals/rules, worktree isolation, output hardening. | OS/container sandboxing for allowed shell, hook, statusline, or MCP processes. |

For planned work, consult [Roadmap: Deferred Items](roadmap-deferred.md) and the repository [Backlog](../BACKLOG.md). Treat them as plans, not user-facing guarantees.
