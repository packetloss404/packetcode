# packetcode User Manual

packetcode is a keyboard-first coding agent that runs in your terminal. It keeps your conversation, tool approvals, provider and model controls, background agents, and saved sessions in one interface while leaving completed output in normal terminal scrollback.

This manual starts with the shortest path to a useful session, then introduces the more powerful features as you need them.

## 1. Install and Start

packetcode requires Go 1.26.0 or newer when building from source.

### macOS or Linux release install

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | bash
```

To install into your user path without `sudo`:

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | INSTALL_DIR="$HOME/.local/bin" bash
```

### Build from source

```bash
make build
./bin/packetcode
```

On Windows PowerShell:

```powershell
$commit = git rev-parse --short HEAD
go build -trimpath -ldflags "-s -w -X main.version=dev -X main.commit=$commit" -o bin/packetcode.exe ./cmd/packetcode
.\bin\packetcode.exe
```

Run packetcode from the project directory you want it to work in. If the directory belongs to a Git repository, packetcode uses the repository root as its working boundary.

### First-run setup

The first run asks you to:

1. Choose a provider.
2. Enter an API key if that provider needs one.
3. Choose a model.

The choices are saved in `~/.packetcode/config.toml`. Packetcode writes mode `0600` on POSIX systems; Windows protection is best-effort and follows the account's inherited ACLs.

For an OpenAI Codex-enabled ChatGPT plan, sign in with the official Codex CLI first:

```bash
codex login
packetcode --provider codex
```

Choose **Sign in with ChatGPT** during `codex login`. packetcode reuses `~/.codex/auth.json`; no API key is pasted into packetcode.

For a local Ollama model:

```bash
ollama serve
packetcode --provider ollama
```

### Startup options

```text
packetcode
packetcode --provider codex
packetcode --provider codex --model gpt-5.6-sol
packetcode --resume <session-id>
packetcode --permission-mode ask
packetcode --permission-mode accept-edits
packetcode --permission-mode auto
packetcode --permission-mode read-only
packetcode --trust
packetcode --version
packetcode doctor
packetcode doctor --json
packetcode run --permission-mode read-only --json "summarize this repository"
packetcode skills list
packetcode acp
packetcode sugar login
```

`--provider` and `--model` override the saved default for this launch. `--resume` accepts a saved session ID. `--trust` starts in Bypass Permissions mode, so use it only in a project you trust.

The default invocation opens the TUI. The five public command families are
`run` (one headless agent turn), `doctor` (diagnostics), `skills` (Agent Skills
management), `acp` (the local stdio protocol server), and `sugar` (the optional
Sugar integration). Run `packetcode --help` or a command's `--help` for current
flags.

### Headless execution

Use `run` from scripts, CI, benchmarks, or another agent:

```text
packetcode run [--provider NAME] [--model MODEL] [--permission-mode MODE] [--resume ID] [--json] <prompt...>
```

It shares provider, session, tool, policy, MCP, and compatibility setup with the
TUI and ACP paths. The prompt must be positional; stdin, `--trust`,
`--computer`, and an ephemeral-session mode are intentionally absent. If a tool
needs interactive approval, the run fails closed with exit 3 instead of
approving or hanging. Cancellation exits 130. Plain stdout is the sanitized
final response only. `--json` writes one `schema_version: 1` object containing
`ok`, session/provider/model identity, output, `elapsed_ms`, per-run
input/output/cache usage, and `error` on failure.

Failures after session creation include the session ID and an interactive
`packetcode --resume ID` recovery command on stderr. Open it from the same
directory to review saved history before continuing. Completed tool actions
are not rolled back, and recovery does not automatically repeat the prompt.
Approval-blocked runs need an interactive decision; changing to a broader
permission mode is not required to inspect the saved session.

## 2. Your First Conversation

Type a request at the bottom prompt and press `Enter`:

```text
Explain how this project is structured.
```

packetcode can inspect the repository, search code, edit files, execute
commands, load skills, maintain a per-session todo list, and fetch bounded
HTTP(S) evidence. Fetched content is explicitly untrusted, blocks private and
loopback targets by default, and is capped by redirect, header, body, and time
limits. Actions run immediately, ask for approval, or are denied according to
the permission mode shown in the footer.

A good first editing request is specific about the outcome and verification:

```text
Add validation to the signup handler and run the focused tests.
```

Completed turns move into your terminal's normal scrollback. The small live area at the bottom is reserved for the current response, tool activity, prompt, context gauge, and permission mode.

### Everyday keys

| Key | Action |
| --- | --- |
| `Enter` | Send the prompt. |
| `Ctrl+J` or `\` then `Enter` | Insert a portable newline; `Alt+Enter` also works when Alt is reported distinctly. While completion is open, `Ctrl+J` moves its selection instead. |
| `\` then `Enter` | Portable newline fallback. |
| `Shift+Enter` | Insert a newline only when the terminal maps it to `Ctrl+J`; otherwise use a portable fallback. |
| `Up` / `Down` | Recall prompt history when the caret is on the first/last visual input row. |
| `Shift+Tab` | Cycle Manual → Accept Edits → Auto → Plan. |
| `Left` on an empty, idle prompt | Open Agent View. |
| `Ctrl+P` | Open the provider picker. |
| `/model` | Open the model picker; `Alt+M` also works when Alt is reported distinctly. |
| `Ctrl+C` | Cancel active work, clear a draft, or quit from an empty prompt. |
| `Ctrl+D` | Quit from an empty prompt. |
| `Ctrl+L` | Clear visible output while keeping the saved session. |
| `Esc` | Close the current popup, picker, or transcript; in an approval prompt it selects **Reject this request**. |

The input grows to multiple rows as needed. Its default maximum height is 10 rows and is configurable with `max_input_rows`.

### Multiline prompts

Use `\` followed by `Enter` for a newline in every input state. `Ctrl+J` is convenient while completion is closed; `Alt+Enter` also works when the terminal reports Alt distinctly. For example:

```text
Review these areas:
- authentication
- error handling
- tests
```

`Shift+Enter` works only in terminals that map it to `Ctrl+J`. Bubble Tea v1 cannot safely distinguish a true `Shift+Enter` event from ordinary Enter, which is why the portable alternatives remain authoritative.

### File mentions with `@`

Type `@` at a token boundary to search project files. Continue typing to narrow the list, select a match, and send the prompt normally:

```text
Compare @internal/app/app.go with @internal/jobs/manager.go and explain the handoff.
```

The selected file is expanded into bounded context when the prompt is sent. Paths are resolved inside the project root. Avoid attaching the same large file repeatedly; it consumes context each time.

### Slash-command completion

Type `/` to open command completion. Use arrows, `Ctrl+N`/`Ctrl+P`, or `Ctrl+J`/`Ctrl+K` to move, `Tab` to accept, and `Esc` to dismiss the popup without losing what you typed.

To send a normal prompt that begins with a slash, start it with two slashes:

```text
//explain why this is not a command
```

The model receives `/explain why this is not a command` as ordinary prompt text.

### Queue another prompt while work is active

Submitting a prompt during an active turn or compaction queues it instead of interrupting the current work:

```text
/queue
/queue drop 2
/queue resume
/queue clear
```

Queued prompts run in order. `/queue` displays up to the first 20 with one-based indexes.
If a turn or compaction fails, pending prompts pause for review. New prompts
join the paused queue. Use `/queue resume` when idle to continue, `/queue drop N`
to remove an entry, or `/queue clear` to discard pending work and start fresh.

## 3. Permission Modes and Approvals

packetcode's permission policy is a safety decision layer, not an operating-system sandbox. Tools, hooks, MCP servers, and commands still run as your user.

| Footer mode | Config profile | What happens |
| --- | --- | --- |
| Manual | `ask` | Reads and searches run; edits, commands, MCP calls, and model-initiated `spawn_agent` calls ask. |
| Accept Edits | `accept_edits` | File writes and patches run; commands, MCP calls, and model-initiated `spawn_agent` calls ask. |
| Auto | `auto` | File edits and shell commands run; MCP and other approval-gated tools still ask. |
| Plan | `read_only` | Research tools run; mutations are denied and the model is told to propose a plan. |
| Bypass Permissions | `bypass` | Tools run unless an explicit deny rule matches. |

Press `Shift+Tab` to cycle the first four modes. It also works during an active turn, and a newly selected policy can resolve an approval already on screen. It does not interrupt a command that has already started.

An explicit `/spawn` is a direct user command and does not ask for a second spawn approval. Tools called inside that job still follow its permission policy; write-capable jobs remain isolated in worktrees.

Bypass is intentionally outside that cycle. Enable or disable it explicitly:

```text
/trust
/trust on
/trust off
```

Or use `packetcode --trust` at startup. Explicit deny rules continue to apply in Bypass mode.
Enabling Bypass while Plan is active exits Plan atomically; `/trust off`
restores the pre-Bypass profile (the profile that preceded Plan in that case)
and preserves session rules added before or during Bypass. An explicit
`/permissions profile ...` choice also exits Plan.

### Approval menu

When packetcode asks before a tool action, choose:

1. Allow once
2. Allow exact command this session (shell), or allow this tool this session
3. Reject this request

Arrow keys and `Enter` work, as do `1`/`2`/`3` and the legacy `Y`/`A`/`N` shortcuts.
Remembered shell approval matches exact command text in any working directory;
other remembered approvals cover all arguments and paths for that tool.
Running jobs keep their existing policy. Use `/permissions` to inspect rules
or `/permissions reset` to revoke all session rules. Explicit denies still apply.

Inspect or change the session policy with:

```text
/permissions
/permissions profiles
/permissions profile ask
/permissions profile accept-edits
/permissions profile auto
/permissions explain execute_command
/permissions rule execute_command ask
/permissions rule filesystem__* deny
/permissions reset
```

Session changes do not rewrite your saved configuration. `/permissions reset`
revokes remembered and manually added session rules, exits temporary Plan or
Bypass state, and restores the policy loaded at startup.

Explicit deny rules are safety floors. Among other matching rules, the later
rule wins. Plan mode adds its own read-only floor, so an allow or ask rule
cannot authorize a mutating tool until Plan is exited.

### Plan before editing

Use Plan mode for a read-only investigation:

```text
/plan on
Plan the migration and identify every affected file.
/plan off
Implement the plan.
```

Bare `/plan` toggles the mode. You must finish or cancel an active foreground turn before changing Plan mode with the command.

## 4. Providers, Models, and Reasoning Effort

### Built-in providers

| Slug | Authentication |
| --- | --- |
| `codex` | Existing official Codex CLI ChatGPT login. |
| `openai` | OpenAI API key. |
| `anthropic` | Anthropic API key. |
| `gemini` | Google Gemini API key. |
| `minimax` | MiniMax API key. |
| `deepseek` | DeepSeek API key. |
| `grok` | xAI API key. |
| `mistral` | Mistral API key. |
| `openrouter` | OpenRouter API key. |
| `ollama` | No key; reachable Ollama server. |

Custom OpenAI-compatible providers can also be declared in the configuration file.

### Add or switch a provider

Open the searchable provider picker with `Ctrl+P` or `/provider`. Select a row to switch. Press `Ctrl+A` on a provider row to set or update its API key.

Direct forms are:

```text
/provider codex
/provider anthropic
/provider add
/provider add openai
/providers
```

`/providers` is an alias that opens the provider picker. Switching persists the newly active provider and model as the default.

For hosted providers, environment variables override keys stored in the configuration file:

```text
PACKETCODE_OPENAI_API_KEY
PACKETCODE_ANTHROPIC_API_KEY
PACKETCODE_GEMINI_API_KEY
PACKETCODE_MINIMAX_API_KEY
PACKETCODE_DEEPSEEK_API_KEY
PACKETCODE_GROK_API_KEY
PACKETCODE_MISTRAL_API_KEY
PACKETCODE_OPENROUTER_API_KEY
```

Custom slugs default to `PACKETCODE_<NORMALIZED_SLUG>_API_KEY` unless `api_key_env` is configured.

### Switch models

Open the model picker portably with `/model` (`Alt+M` also works when Alt is reported distinctly), or switch directly:

```text
/model gpt-5.6-sol
/models
```

`/models` is an alias that opens the picker. Use the picker when unsure of the exact ID; it loads the catalog available to the active provider/account.

### Codex reasoning effort

For Codex models that advertise reasoning controls:

```text
/effort
/effort low
/effort medium
/effort high
/effort xhigh
/effort max
/effort ultra
/effort default
```

Bare `/effort` shows the current and available levels. Only levels advertised for the active model are accepted. The selection is saved under `[providers.codex]`; `default` (or `auto`) restores the catalog default. Wait for the current turn to finish, or cancel it, before changing effort.

The Codex provider uses subscription authentication and therefore reports tracked API cost as `$0`; ChatGPT plan usage limits still apply.

## 5. Sessions, Context, Cost, and Recovery

packetcode creates and saves a session automatically. The statusline context gauge shows the current request's context occupancy, not cumulative billed tokens.

### Manage sessions

```text
/sessions
/sessions resume <id-or-unique-prefix>
/sessions rename Fix authentication race
/sessions delete <id-or-prefix> --yes
```

The list is newest first. Resume and delete accept a full ID or unique eight-character prefix. Renaming changes the current session. Deletion is irreversible and refuses to proceed without `--yes`; deleting the active session creates a replacement session first.

You can also resume directly at startup:

```bash
packetcode --resume <session-id>
```

### Compact long conversations

```text
/compact
/compact --keep 15
```

Compaction summarizes older history while keeping complete recent exchanges. The default is to preserve 10 recent messages. Automatic compaction runs near the configured context threshold when enough history exists.

Complete session history remains persisted even when older oversized tool results are reduced in the model-facing copy.

### Clear versus delete

`/clear` and `Ctrl+L` clear visible output but keep the saved session. `/transcript` opens the current persisted transcript. Deleting a session is the only one of these operations that removes its saved history.

### Cost

```text
/cost
/cost reset
/cost reset --yes
```

`/cost` shows cumulative tracked API usage, including separate cache
creation/read counts where a provider supplies them. Cache counts are subsets
of input tokens, not additional tokens. Resetting requires confirmation with
`--yes` before the total is cleared.

### Undo the last file edit

```text
/undo
```

packetcode keeps session-scoped backups for native write and patch tools. `/undo` restores the most recent available file backup. It is not a substitute for source control: inspect the result and use Git for durable history.

## 6. Background Agents and Jobs

Background agents are useful for independent research, reviews, or isolated implementation. They have their own provider calls, transcript, usage, and result lifecycle.

### Start an agent

```text
/spawn inspect the authentication flow for race conditions
/spawn --provider gemini --model gemini-2.5-flash audit the API handlers
/spawn --write implement the focused test fix
/spawn --computer production inspect the deployed service
/spawn --computer production --write migrate the app
```

Flags must come before the prompt. A normal `/spawn` job is read-only and shares the selected project root. A remote foreground session inherits its active Packet Computer; from a local session, `--computer <name>` selects a registered SSH computer. Each remote job owns a separate SSH/SFTP connection so parallel agents do not serialize behind one shell.

`/spawn --write` requires a Git repository and creates a separate worktree and branch from the current `HEAD`, locally or on the selected Packet Computer:

```text
~/.packetcode/worktrees/<repo-key>/<job-id>
branch: packetcode-job-<job-id>
```

Uncommitted changes from the foreground checkout are not copied into that worktree. packetcode does not merge or delete completed worktrees automatically.

The resolved computer, endpoint/root identity, and working directory are frozen before a remote job is queued. Nested jobs inherit that target and cannot pivot to another computer. The computer's write and shell policy is a restrictive overlay on the session policy. If a remote worktree cannot be created, the job fails closed instead of editing the registered checkout.

Remote jobs are process-lifetime work, not a durable daemon service. Closing packetcode or losing SSH does not reconnect and continue an agent loop, and detached remote descendants may require operator cleanup. Saved job evidence can be inspected or explicitly resubmitted as a new run.

### Agent View

Open Agent View with `/agents` or Left Arrow from an empty, idle prompt. Open a particular transcript with `/agents <id>`.

Agent View starts in list mode. Press `n` to focus the task composer, type a prompt, and press `Enter` to spawn a read-only agent.

Each job has its own bounded `todo_write` plan. Its row shows completed/total
items and the current in-progress (or next pending) item; the list is persisted
with the job so an abandoned run still records what it was doing.

| Key | Action in Agent View |
| --- | --- |
| `n` | Focus the new-agent task composer. |
| `Up` / `Down`, `j` / `k` | Move between agents. |
| `p` | Peek at output. |
| `Enter` or `o` | Open the selected transcript. |
| `c` | Cancel an active agent. |
| `i` | Inject a completed result into foreground context. |
| `x` or `d` | Ignore a completed result. |
| `Esc` | Clear a composer draft, return to the list, or return to chat. |

Results are not silently added to your foreground conversation. Inject the useful ones, ignore ones you do not need, or let the foreground model explicitly collect delegated results.

### Job commands

```text
/jobs
/jobs <id>
/jobs resubmit
/jobs resubmit <id>
/cancel <id>
/cancel all
```

`/jobs` prints a compact job table; `/jobs <id>` opens the live transcript. Transcripts refresh while jobs run and retain full output beyond the bounded result summary.

Jobs persist under `~/.packetcode/jobs/`. If packetcode is interrupted, previously active jobs recover as cancelled on the next launch; they do not resume automatically.

### Re-running abandoned jobs

`/jobs resubmit` lists jobs that a previous app exit abandoned; `/jobs resubmit <id>` re-runs one.

This starts a **new** job from the abandoned job's saved prompt, provider, and model. Nothing is resumed — the previous process is gone and its agent loop cannot be continued. The original job keeps its cancelled state, its reason, and all of its evidence (artifacts, transcript, worktree references, token and cost totals), and the two records are linked in both directions so the lineage stays inspectable in `/jobs`.

A job can be resubmitted once. Jobs that finished normally are not eligible, and a saved prompt over 32 KiB is refused rather than truncated — a shortened prompt would start a different run than the one you asked to re-run.

### Agent limits

The default limits are four concurrent jobs, depth two, and 32 jobs during one packetcode run. Extra work waits in the queue. Token budgets can stop jobs at completed provider/tool boundaries. These limits are configurable under `[behavior]`.

## 7. Workflows and Loops

### Multi-agent workflows

Workflows arrange sequential phases and parallel fan-out over the background job manager.

```text
/workflows
/workflows list
/workflows validate review
/workflows run review
/workflows run --computer production review
/workflows run review target="the staged diff"
/workflows <run-id>
/workflows stop <run-id>
/workflows stop all
```

`/workflow` is also accepted as an alias. Bare `/workflows` opens the run view. In that view, arrows or `j`/`k` move, `Enter`/`o` expands a row, `c` cancels the selected active run, and `Esc`/`q` returns to chat.

A built-in `review` workflow is available immediately. User definitions live in `~/.packetcode/workflows/*.toml`; local project definitions live in `.packetcode/workflows/*.toml` and take precedence. Remote sessions load built-ins plus local user definitions; remote project workflow discovery is deferred so workflow loading does not block the TUI on SFTP.

`--computer <name>` routes the whole workflow run to one registered computer. Every work agent, retry, and verifier uses that target. Each write-enabled workflow agent receives its own worktree; later steps receive summaries, not another job's unmerged files, so keep one cohesive mutation in one write step.

A compact custom example:

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
fan_out = ["correctness", "security", "performance"]
prompt = "Review {{.inputs.target}} for {{.item}}. Return concrete findings."

[[phases]]
name = "synthesis"

[[phases.steps]]
name = "synthesize"
mode = "single"
prompt = "Synthesize these findings:\n\n{{.steps.review}}"
```

Optional step fields include `provider`, `model`, `system_prompt`, and `allow_write`. A phase may set `continue_on_error = true`.

Every workflow file must declare `schema_version = 1`. Validate a definition
before it spends tokens:

```text
/workflows validate focused-review
```

For a fail-closed verifier and up to two additional attempts, add this beneath
the step it verifies:

```toml
[phases.steps.verify]
prompt = "Check attempt {{.attempt}}. Candidate:\n{{.result}}"
provider = "codex"
model = "gpt-5.6-sol"
pass_contract = "packetcode-workflow-verdict-v1"

[phases.steps.retry]
max = 2
```

Missing or malformed verdicts fail; they never count as success. A step with
no verifier is shown as **unverified**, not passed. Every verifier and retry
counts toward the workflow's agent and token budgets. See
[Workflows](workflows.md) for the complete schema and verdict contract.

### Repeating work with loops

Self-paced loops start another turn as soon as the previous one finishes:

```text
/loop Continue reviewing and fixing the focused tests until complete
```

The model is asked to emit a versioned `packetcode-loop-decision` JSON block
when finished. The legacy `LOOP_DONE` sentinel remains accepted. A self-paced
loop stops after 25 iterations even if neither signal is produced.

This scheduler limit is separate from the agent's no-progress detector. Any
foreground, background, or ACP run stops earlier when the same tool executes
with identical arguments and an identical result often enough inside the
bounded detection window. The diagnostic names the repeated call; a changing
result is treated as progress.

Interval loops run once immediately and then on the interval:

```text
/loop 10m /workflows run review
/loop 30s Check whether the service is healthy
```

The minimum interval is one second. Loop work is queued rather than overlapped with an active foreground turn.
Stopping a loop also removes its queued iterations. Interval ticks skip while
the foreground queue is paused, and Ctrl+C stops self-paced continuation.

Manage loops with:

```text
/loop list
/loop stop <loop-id>
/loop stop all
```

## 8. Local Models with Ollama

Start the Ollama daemon, then launch or switch to the provider:

```bash
ollama serve
packetcode --provider ollama
```

Inside packetcode:

```text
/ollama status
/ollama models
/ollama ps
/ollama pull qwen2.5-coder:14b
/provider ollama
/model qwen2.5-coder:14b
```

Bare `/ollama` shows status. The status includes installed and loaded models, memory-aware recommendations, and recent generation speed when available. A pull can take a while and has a 30-minute command timeout.

The default server is `http://localhost:11434`. Override it for a launch with `PACKETCODE_OLLAMA_HOST` or the standard `OLLAMA_HOST`, or save `host` under `[providers.ollama]`.

Without explicit tuning, packetcode auto-sizes context per request, caps it to model metadata, keeps the model loaded for 30 minutes, and detects tool support. Optional Ollama-only settings are `num_ctx`, `keep_alive`, and `temperature`.

If generation is slow, run `/ollama ps`. Partial GPU or CPU-only placement usually means the model/context is too large for available memory; choose a smaller model or reduce `num_ctx`.

## 9. MCP Servers

MCP servers add tools supplied by external programs. packetcode currently
supports stdio MCP tools; it does not support HTTP/SSE transports or MCP
prompts/resources. A reviewed
[Streamable HTTP trust contract](mcp-http-trust-contract.md) now defines the
required security boundary, but no URL field, transport flag, or HTTP client is
enabled.

Add a server to `~/.packetcode/config.toml`:

```toml
[mcp.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/project"]
enabled = true
timeout_sec = 10
```

Discovered tools are named `<server>__<tool>`, such as `filesystem__read_file`. MCP calls are approval-gated according to the active policy. The server process itself starts as your user and is not sandboxed, so configure only programs and arguments you trust.

Use `env_from` to pass selected secrets from packetcode's environment without writing them into TOML:

```toml
[mcp.example]
command = "example-mcp"
env_from = ["EXAMPLE_TOKEN"]
```

Inspect servers with:

```text
/mcp
/mcp status filesystem
/mcp tools filesystem
/mcp logs filesystem
```

Startup failures do not prevent packetcode from opening. Use
`/mcp restart <name>` to reconnect a crashed server with its startup configuration;
configuration changes still require a PacketCode restart. Logs are stored as
`~/.packetcode/mcp-<name>.log`, and the in-app tail is bounded and redacts
common secret forms.

## 10. Configuration

The main file is `~/.packetcode/config.toml`. This example shows the most commonly useful settings:

```toml
[default]
provider = "codex"
model = "gpt-5.6-sol"

[providers.codex]
default_model = "gpt-5.6-sol"
reasoning_effort = "high"

[providers.ollama]
host = "http://localhost:11434"
default_model = "qwen2.5-coder:14b"

[behavior]
trust_mode = false
auto_compact_threshold = 80
max_input_rows = 10
provider_max_retries = 3
provider_stall_timeout = 60
background_max_concurrent = 4
background_max_depth = 2
background_max_total = 32
background_default_provider = ""
background_default_model = ""
background_token_budget = 0
workflow_token_budget = 0

[permissions]
profile = "ask"

[[permissions.rules]]
tool = "execute_command"
action = "deny"
command_prefix = ["rm", "-rf"]
reason = "refuse broad recursive deletes"
```

Zero token budgets disable budget-based stopping. `provider_max_retries` is the total number of attempts, including the first. `provider_stall_timeout` aborts a provider stream after that many silent seconds.

### Custom OpenAI-compatible endpoint

```toml
[providers.localai]
type = "openai_compatible"
display_name = "LocalAI"
base_url = "http://localhost:8080/v1"
default_model = "coder-large"
api_key_required = false

[[providers.localai.models]]
id = "coder-large"
context_window = 32768
supports_tools = true
```

For a hosted endpoint, leave `api_key_required` true or omitted, use HTTPS, and supply `api_key`, `api_key_env`, or the normalized `PACKETCODE_<SLUG>_API_KEY` variable. Static model entries are fallbacks when `/models` is absent or incomplete.

### Custom prompt commands

Place Markdown commands in:

```text
~/.packetcode/commands/<name>.md
.packetcode/commands/<name>.md
```

Project commands override user commands; built-ins cannot be replaced. The filename becomes the slash command. `$ARGUMENTS` in the body is replaced with text following the command.

Example `.packetcode/commands/review-api.md`:

```markdown
---
description: Review an API surface
---
Review $ARGUMENTS for correctness, compatibility, and security. Return concrete findings.
```

Run it as:

```text
/review-api internal/http
```

### Statusline, hooks, and theme

The native statusline works without configuration. A custom `[statusline]` command receives a JSON snapshot on stdin; `/statusline` shows its current output and `/statusline refresh` reruns it.

Hooks can run on user prompt submission, before a tool, or after a tool. Hooks and statusline commands execute as your user through PowerShell on Windows and `sh -c` elsewhere. Treat their configuration as executable code.

Place a theme override at `~/.packetcode/theme.toml`. A broken theme falls back to the built-in theme rather than blocking startup.

## 11. Troubleshooting

Start with the doctor:

```bash
packetcode doctor
packetcode doctor --json
```

Useful focused checks include:

```bash
packetcode doctor --check providers
packetcode doctor --check permissions
packetcode doctor --check project,state.worktrees
```

### Provider is not configured

Open `Ctrl+P`, select the provider, and press `Ctrl+A`, or use `/provider add <slug>`. Codex instead needs `codex login`; Ollama needs a reachable daemon.

### A model switch fails

Use `/model` and select the exact model ID available to the current account. Then run `packetcode doctor --check providers` if the request still fails.

### Codex login is rejected

Run `codex login` and choose ChatGPT sign-in, not API-key mode. The `codex` provider needs OAuth credentials in `~/.codex/auth.json`; use the `openai` provider for normal API-key billing.

### A tool was unexpectedly denied or allowed

Run `/permissions` and `/permissions explain <tool>`. Explicit session or
configuration rules override profile defaults. Use `/permissions reset` to
revoke session changes and restore the startup policy, or
`/permissions profile ask` to change only the active profile.

### A write-capable agent fails

`/spawn --write` needs a trusted Git repository and worktree support:

```bash
git status
git worktree list
packetcode doctor --check project,state.worktrees
```

The job fails closed rather than editing the foreground checkout.

### An MCP server fails

Use `/mcp`, `/mcp status <name>`, and `/mcp logs <name>`. Check that its executable is on `PATH`, its startup timeout is sufficient, and required secret names appear in `env_from`. Restart packetcode after changing MCP configuration.

### You cannot scroll

Completed output uses terminal-native scrollback. Use your terminal's scroll controls, Shift+PageUp, or tmux copy mode. `/transcript` opens the persisted conversation.

PacketCode keeps mouse tracking and alternate-screen mode disabled. The
validated geometry and platform matrix is documented in
[Supported terminals](supported-terminals.md).

### The context gauge grows quickly

Use `/compact`, avoid repeatedly mentioning large files, inspect large tool/MCP results, and set job/workflow token budgets. Remember that the gauge measures current request occupancy while `/cost` is cumulative.

### An unknown slash command appears

Type `/` or run `/help` for the runtime command list. Check custom command filenames and load errors. Use `//` when the leading slash is intended as prompt text.

## 12. Cancel, Exit, and Clean Up Safely

`Ctrl+C` is deliberately contextual:

- During a foreground turn, it requests cancellation.
- With text in the idle prompt, it clears the draft.
- At an empty idle prompt, it quits packetcode.

`Ctrl+D` quits from an empty prompt. `/exit` and `/quit` also exit. If a picker or full-screen workspace is open, use `Esc` to return to chat first.

When cancellation interrupts a local command, the result names the teardown
mechanism, whether the process tree was confirmed stopped, and any surviving
PIDs. POSIX process groups and Windows Job Objects supply that evidence. SSH
can only signal the channel leader, so remote teardown is reported as
unconfirmed and detached descendants may require operator cleanup.

Background jobs are separate from foreground generation. Cancel them explicitly before exiting when you do not want them to continue during shutdown:

```text
/cancel all
/workflows stop all
/loop stop all
```

packetcode asks running jobs to stop during shutdown. Active jobs found after an unclean exit recover as cancelled; they do not resume. Write-agent worktrees remain on disk for you to inspect, merge, or remove with Git.

Before destructive or broad work, a safe rhythm is:

1. Start in Manual or Plan mode.
2. Inspect the proposed command or diff.
3. Keep the project under Git.
4. Use `/undo` for the latest native file edit and Git for durable rollback.
5. Reserve Bypass Permissions for repositories and instructions you trust.

## Command Reference

| Command | Purpose |
| --- | --- |
| `/provider [add [slug]\|slug]` | Open the picker, add/update a key, or switch provider. |
| `/providers` | Alias that opens the provider picker. |
| `/model [id]` | Open the picker or switch model. |
| `/models` | Alias that opens the model picker. |
| `/effort [default\|low\|medium\|high\|xhigh\|max\|ultra]` | Show or set supported reasoning effort. |
| `/spawn [--computer name] [--provider slug] [--model id] [--write] <prompt>` | Start a local or Packet Computer background agent. |
| `/agents [id]` | Open Agent View or an agent transcript. |
| `/jobs [id]` | List jobs or open a job transcript. |
| `/cancel <id\|all>` | Cancel background work. |
| `/computers` | List registered Packet Computers. |
| `/computers status <name>` | Show a computer's stored record. |
| `/computers ssh <name> <user@host> <root> --fingerprint <SHA256:...>` | Register a pinned SSH computer. |
| `/workflows [run [--computer name] <name>\|validate <name>\|list\|stop [id\|all]\|<id>]` | Validate, run, or inspect local or remote workflows. |
| `/loop [interval] <prompt\|/command>` | Repeat work; use `list` or `stop [id\|all]`. |
| `/queue [drop <n>\|resume\|clear]` | Inspect, resume, or clear queued foreground prompts. |
| `/sessions` | List sessions. |
| `/sessions resume <id>` | Resume by full ID or unique prefix. |
| `/sessions rename <name>` | Rename the current session. |
| `/sessions delete <id> --yes` | Irreversibly delete a session. |
| `/compact [--keep N]` | Summarize older context. |
| `/undo` | Restore the latest native file backup. |
| `/cost [reset [--yes]]` | Show or reset tracked API cost. |
| `/plan [on\|off]` | Toggle read-only planning mode. |
| `/permissions` | Inspect the active tool policy. |
| `/permissions profiles` | List permission profiles. |
| `/permissions profile <name>` | Change the session profile. |
| `/permissions explain <tool>` | Explain a policy decision. |
| `/permissions rule <tool-or-pattern> <ask\|allow\|deny>` | Add a session rule. |
| `/permissions reset` | Revoke session rules and restore startup policy. |
| `/trust [on\|off]` | Show or set Bypass Permissions mode. |
| `/ollama [status\|models\|ps\|pull <model>]` | Inspect or manage Ollama. |
| `/mcp` | List configured MCP servers. |
| `/mcp status <name>` | Show MCP server health. |
| `/mcp tools <name>` | List a server's tools. |
| `/mcp logs <name>` | Show a bounded stderr tail. |
| `/mcp restart <name>` | Reconnect one stdio server with startup configuration. |
| `/statusline [refresh]` | Show or refresh statusline output. |
| `/transcript` | Open the current persisted transcript. |
| `/clear` | Clear visible output without deleting the session. |
| `/help` | Show the live key and command reference. |
| `/exit`, `/quit` | Exit packetcode. |

For exact provider configuration and advanced policy, MCP, hook, and workflow schemas, see the focused reference documents in `docs/`. Inside packetcode, `/help` is the authoritative live command list for the version you are running.
