# Packetcode Maintainer Handoff

Updated: 2026-09-11

The latest security and lifecycle hardening is documented in
[the September 8 review](docs/audit/hardening-2026-09-08.md). For routine fixes,
test commands, and recovery during the next month, start with
[the maintenance guide](docs/maintenance.md). The architecture notes below
remain useful; Git and the current CI run are authoritative for shipped work.
For implementation contracts and regression locations, read
[the developer guide](docs/development.md).

## Latest verified behavior baseline

The September 11 usability pass is merged as `7f7ecb3`.
[All 16 CI jobs passed](https://github.com/packetloss404/packetcode/actions/runs/34664539030),
including Windows/macOS/Linux tests, Linux race checking, six build targets,
smoke tests, lint, vulnerabilities, TUI goldens/protocol safety, and release dry
run. Local full tests, race tests, vet, and module verification also passed.
This documentation update follows that behavior baseline; inspect Git for the
current tip rather than treating the hash as permanently current.

- Failures in a foreground turn or compaction pause and retain queued prompts.
  New prompts join the paused queue. `/queue resume` continues while idle;
  `/queue clear` discards pending work and clears the pause.
- Ctrl+C prevents self-paced continuation even with buffered success. Stopping
  a loop removes queued iterations. Loop ownership is claimed after compaction,
  only when the actual agent turn starts.
- Approvals name their session scope and revocation path. Shell allowances
  match exact command text in any working directory; other tool allowances
  cover all arguments and paths. Decoded controls are escaped for display
  without changing execution arguments.
- Headless failures report saved-session recovery instructions on stderr.
  JSON retains partial output and the original error; plain stdout stays empty
  on failure. Completed tool actions are not rolled back.
- Job recovery shows full IDs, distinguishes abandoned work from queued jobs
  cancelled before starting, and prevents concurrent duplicate resubmissions.
  MCP recovery explains logs, manual reconnection, and configuration reload.

The remaining priority issues are per-model cost accounting, backend read
bounds/SFTP cancellation repros, and broader offline provider replay fixtures.
Use a new session when switching models if cost attribution matters. See
[BACKLOG.md](BACKLOG.md); these items were not fixed by the usability pass.

Historical audit baseline: `main` and `origin/main` began at `d646094` (`Merge
fix/help-lists-commands: help names what dispatch runs`), version
`v0.5.1-127-gd646094`. This handoff also describes the integrated headless-run
and shared-runtime work after that baseline; use the current log for its final
commit identity. This is a capability/priority handoff, not a substitute for
Git.

This file is the quickest way to resume Packetcode work without reconstructing
the repository's recent history. Read it together with [README.md](README.md),
[CHANGELOG.md](CHANGELOG.md), and [BACKLOG.md](BACKLOG.md). The current commit,
branch, and working-tree state remain authoritative:

```powershell
git status -sb
git log -5 --oneline --decorate
```

## Current State

Packetcode is a pre-1.0, keyboard-first Go coding agent with a Claude
Code-inspired Bubble Tea TUI. It supports multiple hosted and local providers,
OpenAI Codex subscription authentication, permission-gated native tools,
foreground sessions, background agents, isolated write worktrees, workflows,
loops, MCP servers, hooks, themes, and native/custom statuslines.

The executable exposes `run`, `doctor`, `skills`, `acp`, and `sugar` command
families. `run` is the headless one-turn path for automation and benchmarks; it
removes the requirement for a benchmark client to speak ACP and therefore
provides a direct engine baseline without protocol lifecycle/permission round
trips.

The current `main` branch includes the recent interaction-polish and
hardening pass:

- Caret-accurate `@file` completion, bounded multiline input, portable newline
  bindings, visual-row history navigation, draft-first Ctrl+C, and native
  scrollback clearing.
- One-row responsive statuslines, asynchronous Git branch refresh, event-driven
  approvals, stable Agent View focus, live background transcripts, and visible
  agent reasoning activity.
- Terminal-output sanitization that prevents tool/provider text from enabling
  mouse modes, changing the clipboard, moving the cursor, or corrupting the
  renderer with split control sequences.
- Codex `/effort` support using catalog-advertised reasoning levels, persistent
  configuration, and a compact status indicator.
- Background-agent fan-out guidance, structured self-paced loop stops,
  workflow hardening, per-server MCP restart, and PacketADE/BridgeCode evidence
  documentation.
- A reviewed user manual, advanced guide, printable cheat sheet, and
  self-contained offline HTML5 manual.
- Reviewed TUI text-and-style cell goldens at 72×24 and 100×30,
  post-SIGWINCH live-resize and terminal-mode safety checks, a cross-platform
  layout matrix, and an explicit supported-terminal contract. Built-in status
  content recomposes at the new width. The model picker uses `Alt+M`; queued
  prompt whitespace and modal keyboard/geometry ownership are preserved.

The 2026-07-31 pass added:

- **PCH4 — abandoned-job resubmit.** `/jobs resubmit [id]` re-runs work a
  previous app exit abandoned. It spawns a *new* job and never claims the old
  process resumed; the abandoned record keeps its state and evidence, and the
  pair links via `ResubmitOf`/`ResubmittedAs`. See
  [docs/bridgecode-plus-hardening-loop-2026-07-27.md](docs/bridgecode-plus-hardening-loop-2026-07-27.md).
- **PCMP1/PCMP2 — Packet Computers registry (Milestone A).** `internal/computers`
  plus read-only `/computers`. Registry-only: no daemon, no transport, and a
  stored status is never shown as a live probe. See
  [docs/packet-computers-loop.md](docs/packet-computers-loop.md) and
  [docs/feature-packet-computers.md](docs/feature-packet-computers.md).
- **Packet Control split to PacketADE.** Phases 1–2 are implemented there
  (`D:\projects\PacketADE\dev\packet-control-loop.md`). No packetcode work is
  scheduled; if it ever lands here it must consume that manifest schema.

The 2026-08-01 pass added:

- **PCH3 — versioned workflow verifier/retry.** Workflow TOML requires
  `schema_version = 1`; `/workflows validate <name>` checks a definition
  without execution. Optional read-only step verifiers use a fail-closed
  `packetcode-workflow-verdict-v1` block and hard retry caps. Every work and
  verifier attempt consumes the same agent and token budgets. See
  [docs/workflows.md](docs/workflows.md).
- **PCH5 — Streamable HTTP MCP trust gate.** The approved v1 contract and
  fail-closed validator now pin exact origins/ports, address classes, bounded
  bodyless same-origin GET/HEAD redirects, disabled ambient proxies, system-
  root TLS, atomically bound target-only environment credentials, credential-
  bound output provenance/redaction,
  bounded response/event/header/output sizes, per-call approval and revocation,
  bounded timeout, and manual reconnect. Streamable HTTP itself is still
  disabled.
- **Approval safety review.** Plan mode is now a hard read-only floor and
  explicit denies cannot be weakened by later session allows. Running jobs keep
  snapshot-bound explicit prompts even if foreground trust broadens, while a
  later deny can still revoke them. Mode transitions advance queued approvals,
  `/trust off` is a no-op when already off and otherwise preserves session
  rules, and remembered background rules use the unannotated tool name.

The August 14–31 passes materially changed the product and supersede the old
"Bubble Tea v2 next" recommendation:

- Cached-input counts now flow through providers, sessions, `/cost`, jobs, and
  statusline JSON, and cost estimation discounts cache reads instead of billing
  all input at the fresh-token rate.
- The shared agent loop has bounded no-progress detection, a bounded HTTP(S)
  `fetch` tool with post-DNS SSRF enforcement and untrusted-evidence framing,
  and per-session `todo_write`; background plans persist and appear in Agent
  View.
- Skills grew from five builtins into user/project/foreign discovery,
  user/model invocation controls, resource loading, bounded trusted
  `allowed-tools`, CLI management, and explicit notes for unsupported dynamic
  syntax.
- Local command teardown now reports structured kill evidence. POSIX
  process-group release and Windows Job Objects are covered; MCP process release
  uses the same containment primitives, while SSH teardown is still explicitly
  unconfirmed.
- ACP gained saved-session, model, usage, permission, MCP, project-file, and
  Markdown prompt-command extensions plus explicit close. Skills reach ACP
  agents through the shared index/tool path, though user-invocable skills are
  not yet in its command catalogue.
- Release archives are signed/attested and dry-run in CI; on-disk formats have
  an executable compatibility contract; config reports unknown/newer fields;
  and session/job publication is fsync-backed.
- `packetcode --help` now names all five command families and shares one command
  table with dispatch. Help requests exit successfully.
- `packetcode run` executes one non-interactive turn through runtime
  construction shared with the TUI and ACP. It supports provider/model,
  permission-mode, session resume, and versioned JSON; unavailable approval
  fails closed with exit 3 and cancellation exits 130. Plain stdout is only the
  sanitized final response.
- The controlled [`run` versus ACP benchmark](docs/benchmarks/run-vs-acp-2026-09-01.md)
  matched output, provider/tool calls, token counts, and zero approvals. Median
  end-to-end time was 4.152 s for `run` and 4.027 s for ACP; provider variance
  exceeded the 3% path difference. ACP setup was about 79 ms, so there is no
  measured protocol or permission penalty to optimize.

## Start Here

For normal operation:

```powershell
go run ./cmd/packetcode --provider codex --model gpt-5.6-sol
```

For an optimized Windows executable:

```powershell
$packetVersion = git describe --tags --always --dirty
$packetCommit = git rev-parse --short HEAD
$env:CGO_ENABLED = "0"
go build -trimpath `
  -ldflags "-s -w -X main.version=$packetVersion -X main.commit=$packetCommit" `
  -o bin\packetcode.exe ./cmd/packetcode
.\bin\packetcode.exe --version
.\bin\packetcode.exe doctor --check version
```

`bin/` and `*.exe` are intentionally ignored. Rebuild local binaries rather
than committing them.

The common Codex launch is:

```powershell
codex login
.\bin\packetcode.exe --provider codex --model gpt-5.6-sol
```

Inside Packetcode:

```text
/provider codex
/model gpt-5.6-sol
/effort high
/agents
/workflows
/help
```

## Documentation Map

- [README.md](README.md): project overview, installation, daily commands, and
  the canonical documentation index.
- [docs/development.md](docs/development.md): implementation contracts,
  regression map, validation, and documentation upkeep.
- [docs/maintenance.md](docs/maintenance.md): routine tests and recovery.
- [docs/handoff.md](docs/handoff.md): dated audit status and open trust decisions.
- [docs/manual.md](docs/manual.md): progressive everyday user manual.
- [docs/advanced-guide.md](docs/advanced-guide.md): architecture, permissions,
  agent orchestration, context, MCP, statusline, security, and advanced
  operating patterns.
- [docs/cheat-sheet.md](docs/cheat-sheet.md): compact terminal reference.
- [docs/packetcode-manual.html](docs/packetcode-manual.html): offline,
  responsive HTML5 synthesis with printable quick-reference mode.
- [docs/configuration.md](docs/configuration.md): configuration fields and
  examples.
- [docs/providers.md](docs/providers.md): provider authentication, model
  discovery, Codex, custom endpoints, and Ollama.
- [docs/security.md](docs/security.md): permission profiles and trust
  boundaries.
- [docs/feature-background-agents.md](docs/feature-background-agents.md) and
  [docs/feature-agent-view.md](docs/feature-agent-view.md): job orchestration
  and result lifecycle.
- [docs/workflows.md](docs/workflows.md): versioned workflow schema, verifier
  contract, retries, budgets, and offline validation.
- [docs/feature-packet-computers.md](docs/feature-packet-computers.md): the
  computer registry as it actually ships, and an explicit list of what does
  not work yet.
- [docs/mcp.md](docs/mcp.md): MCP configuration and runtime management.
- [docs/mcp-http-trust-contract.md](docs/mcp-http-trust-contract.md): approved
  Streamable HTTP design gate; the transport is not shipped.
- [docs/troubleshooting.md](docs/troubleshooting.md): common setup and runtime
  failures.
- [BACKLOG.md](BACKLOG.md): unshipped work only.

When behavior changes, update the focused reference, the relevant manual
section, README if the user-facing surface changed, and `CHANGELOG.md`.
Regenerate or manually reconcile the offline HTML manual when its source
material changes.

## Architecture Map

The TUI entry wiring is in `cmd/packetcode/main.go`, ACP in
`cmd/packetcode/acp.go`, and headless execution in
`cmd/packetcode/run_command.go`. Their provider/session/tool/policy/MCP setup is
centralized in `cmd/packetcode/runtime.go`; TUI-only remote computer, jobs,
workflows, and presentation layers remain above it. Important packages:

| Area | Location | Responsibility |
| --- | --- | --- |
| Shared runtime | `cmd/packetcode/runtime.go` | Provider/session/tool/policy/MCP construction shared by TUI, ACP, and headless run. |
| TUI orchestration | `internal/app` | Bubble Tea state, input routing, slash commands, provider/model switching, sessions, approvals, jobs, and workflows. |
| Foreground agent | `internal/agent` | Provider/tool loop, approvals, cancellation, context estimation, and compaction. |
| Providers | `internal/provider` | Provider contract, registry, model metadata, streaming events, Codex auth/Responses behavior, and hosted/local adapters. |
| Native tools | `internal/tools` | Root-scoped filesystem/search/shell tools and provider definitions. |
| Permissions | `internal/permissions` | Profiles, matching rules, allow/ask/deny decisions, and remembered approvals. |
| Background jobs | `internal/jobs` | Job lifecycle, isolated sessions, write worktrees, transcripts, artifacts, persistence, and nested agents. |
| Workflows | `internal/workflow` | Versioned schema, ordered phases, parallel fan-out, fail-closed verification, bounded retries, cancellation, bindings, and token boundaries. |
| MCP | `internal/mcp` | Stdio server startup, discovery, namespaced adapters, calls, logs, restart, and the transport-independent future HTTP trust validator/output boundary. |
| Persistence | `internal/session`, `internal/config`, `internal/cost` | Sessions, backups, user paths/configuration, and usage/cost tallies. |
| TUI components | `internal/ui/components` | Conversation, input, topbar, approvals, pickers, Agent View, workflow view, and transcripts. |
| Display safety | `internal/ui/terminaltext` | Stateful sanitization of untrusted terminal text. |
| Tool output spill | `internal/toolout` | Per-session store that caps oversized tool results at the agent chokepoint and serves the remainder through opaque handles. |

The foreground App owns visible state. Provider streams, background jobs, and
workflow updates enter it through Bubble Tea messages. Finalized messages are
committed to terminal-native scrollback; only active content remains in the
live region.

## State and Security Boundaries

User state is under `~/.packetcode/`:

| Path | Contents |
| --- | --- |
| `config.toml` | Providers, behavior, permissions, MCP, hooks, and statusline. |
| `sessions/` | Foreground and background transcripts. |
| `jobs/` | Persisted job snapshots and artifact metadata. |
| `worktrees/` | Isolated checkouts for write-capable jobs. |
| `commands/` | User prompt commands. |
| `skills/` | User Agent Skills in packetcode's native layout. |
| `workflows/` | User workflow definitions. |
| `theme.toml` | Optional semantic colors. |
| `cost-tally.json` | Persisted usage and cost data. |

Codex subscription credentials remain in the official Codex store:
`$CODEX_HOME/auth.json` or `~/.codex/auth.json`.

Permission policy is a decision layer, not an operating-system sandbox.
Allowed commands, hooks, statusline commands, and MCP servers run as the user.
Direct `/spawn` is already an explicit user action; model-initiated
`spawn_agent` remains policy-gated. Write jobs operate in separate Git
worktrees and are never merged or removed automatically.

Do not commit secrets, local PTY captures, generated binaries, `.env` files, or
machine-specific Codex/Packetcode state.

## Interaction Caveats

- `/model` is the portable model-picker path; `Alt+M` also works when the
  terminal reports Alt distinctly. `Ctrl+M` is intentionally unbound under
  Bubble Tea v1 because terminals encode it as carriage return/Enter.
- Backslash-Enter works in every input state. Ctrl+J inserts a newline while
  completion is closed and moves the completion selection while its popup is
  open; `Alt+Enter` also works when Alt is reported distinctly.
- Esc dismisses ordinary overlays, but in an approval prompt it means **Reject this request**
  and rejects the action.
- Finalized chat output is in terminal-native scrollback. Use the terminal or
  tmux scroll controls; `/transcript` opens saved session content.
- Completion notifications mark background results seen. Injecting or
  collecting is still explicit; results are never silently copied into
  foreground model context.
- Output sanitization protects terminal state, not the safety of the underlying
  command.

## Verification Baseline

Run these sequentially before merging behavior changes:

```powershell
go mod verify
go vet ./...
go test ./...
go test -race -count=1 ./...
golangci-lint run ./...
```

For TUI changes:

```powershell
go build -o bin\packetcode.exe ./cmd/packetcode
.\bin\packetcode.exe --tui-fixture=normal
.\bin\packetcode.exe --tui-fixture=agents
```

On Linux, macOS, or WSL with `scripts/requirements-tui.txt` installed:

```sh
make tui-golden-check
```

The PTY harness is documented in
[docs/tui-parity-harness.md](docs/tui-parity-harness.md). Captures under
`testdata/tui/captures/` are ignored because they may contain local account or
machine data.

For documentation changes, check:

- Markdown relative targets and code-fence balance.
- HTML5 doctype, unique IDs, internal anchors, and absence of remote assets in
  `docs/packetcode-manual.html`.
- Command syntax against `internal/app/keymap.go`, the slash registry and
  handlers, and `cmd/packetcode` flags.

## Recommended Next Work

The authoritative queue is [BACKLOG.md](BACKLOG.md). The highest-leverage next
steps are:

1. Address per-model cost accounting and reproduce backend read-bound/SFTP
   cancellation risks from the September 8 review. Keep each fix bounded and
   preserve compatibility with existing records.
2. Add broader end-to-end smoke coverage for first-run setup, provider
   switching, session resume, approvals, agents, workflows, and MCP. The run
   command already has contract tests and build/CI help smoke coverage.
3. Return to the Bubble Tea v2 migration for enhanced key reporting and
   synchronized output against the committed golden/protocol contract; add tall
   approval/tool fixtures and a native ConPTY evidence lane alongside it.
4. Continue other v1 readiness work: provider catalogue freshness, opt-in live
   contract tests, and bounded model-facing output storage.
5. If Streamable HTTP MCP is selected, implement it only against
   `packetcode-mcp-http-trust-v1`; independently, add workflow pipeline stages,
   safe worktree apply/cleanup assistance, and transcript search/jump-to-latest.
6. Continue the PacketADE/BridgeCode ledger from
   [docs/bridgecode-feature-truth-2026-07-27.md](docs/bridgecode-feature-truth-2026-07-27.md)
   and
   [docs/bridgecode-plus-hardening-loop-2026-07-27.md](docs/bridgecode-plus-hardening-loop-2026-07-27.md).

Avoid mixing these into one large change. Prefer bounded branches with focused
tests and a short changelog entry.

## Resume Checklist

```text
1. Read HANDOFF.md, README.md, CHANGELOG.md, and BACKLOG.md.
2. Run git status -sb and inspect the latest commits.
3. Run packetcode doctor and the relevant focused tests.
4. Choose one bounded backlog item.
5. For independent research/review, fan out read-only agents.
6. Serialize overlapping writes; use isolated write jobs where appropriate.
7. Update focused docs, README/CHANGELOG when user-facing, and this handoff if
   the architecture or recommended next work changes.
8. Run the verification baseline before publishing.
```

The earlier reconciliation started at `d646094`; the latest behavior baseline
is recorded at the top of this file. Check the current
branch, log, and working tree for the published tip before resuming; do not
discard unrelated or parallel edits to make the checkout look clean.
