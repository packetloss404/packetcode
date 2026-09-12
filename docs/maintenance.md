# Maintaining Packetcode

Updated 2026-09-11. This guide favors small, verifiable changes. Larger product
work stays in [BACKLOG.md](../BACKLOG.md); the latest hardening evidence is in
[the September 8 review](audit/hardening-2026-09-08.md).

## Start with the failure

1. Run `git status --short --branch` and `git log -5 --oneline`. Preserve
   unrelated local work. Start a `codex/` branch for a bounded fix.
2. Capture the failing command or test, its full error, OS, and `go version`.
   Remove credentials and conversation contents from shared evidence.
3. Reproduce with the smallest relevant package test before editing. A timeout
   under load is a signal to inspect deadlines and teardown, not to remove the
   assertion. Use `internal/testwait` for asynchronous test deadlines.
4. Make one behavior change, add a regression test that fails without it, and
   run the package test. Keep a short changelog entry describing user behavior.
5. Run the merge checks below and inspect CI before treating the change as done.

## Merge checks

Run these sequentially to avoid competing process-heavy test suites:

```powershell
go mod verify
go vet ./...
go test ./...
go test -race -count=1 ./...
```

CI also checks lint, vulnerabilities, all six build targets, smoke tests on
Windows/macOS/Linux, TUI goldens, and a release dry run. A local test pass does
not establish those other platforms passed. Inspect the actual run:

```powershell
gh run list --branch main --limit 3
gh run view <run-id> --log-failed
```

Useful focused tests:

| Changed area | Command |
| --- | --- |
| Permissions or skill lifecycle | `go test ./internal/permissions ./internal/skills ./internal/app` |
| Job records or shutdown | `go test -race ./internal/jobs ./internal/workflow` |
| MCP or ACP lifecycle | `go test -race ./internal/mcp ./internal/acp` |
| File tools or secret-file refusal | `go test ./internal/tools` |
| Provider protocol or context | `go test ./internal/provider/... ./internal/agent` |
| Signing summary | `sh scripts/test-signing-config.sh` |
| Installer signature behavior | `bash scripts/test-install-verify.sh` |

Shell-script checks require Bash/sh (Git Bash works for the signing-summary
check on Windows). `make tui-golden-check` requires the documented Linux,
macOS, or WSL PTY environment; see [the TUI harness](tui-parity-harness.md).

## Protect state and permissions

- A job rejected with `persistence_failed` has not started. Check free disk
  space and the configured jobs directory. A shutdown persistence error means
  a record still needs saving; restore storage and retry shutdown while the
  process is alive. Do not delete records as a generic repair.
- Skill grants are temporary foreground authority. Keep user policy changes
  in the session policy, and do not copy grants into trust snapshots, queued
  turns, or background-job policies.
- Prefix rules cannot safely interpret every shell expression. An approval
  prompt for an expansion or escaped command is intentional. Do not widen the
  rule to the entire shell to silence it.
- Code intelligence must use the same dotenv-file exclusions as file reads,
  including resolved symlinks and discovered files.
- Cancellation during an MCP write can leave a partial JSON frame. The client
  aborts that transport; restart the configured server once its cause is fixed.
  Avoid automatic retry of a tool call that may already have had effects.

## Keep recovery simple

- A foreground failure pauses queued prompts. Inspect `/queue`, remove unwanted
  entries with `/queue drop N`, and use `/queue resume` while idle. New prompts
  join the paused queue; `/queue clear` lets you start fresh.
- A failed headless run prints its session ID and an interactive resume command
  when a session exists. Review saved history from the same directory before
  continuing. Completed tool actions are not automatically undone.
- `/jobs resubmit ID` starts a new run of recovered work using its full job ID.
  It preserves the previous record, and concurrent resubmit requests cannot
  launch duplicate successors.
- For a stopped MCP server, `/mcp status NAME` and `/mcp tools NAME` point to
  logs and manual reconnection. Editing the configuration requires restarting
  Packetcode; reconnecting does not repeat failed tool calls.

Use `packetcode doctor` for configuration/state diagnosis; see
[operational runbooks](runbooks.md) for credential and storage procedures.
For a confirmed regression, revert the specific fix commit on a branch and
run its tests, rather than resetting main or removing saved state. Do not
revert a permission or secret-file protection merely to remove an approval.

Provider catalog updates, backup retention, new MCP transports, and the TUI v2
migration remain separate work. None is needed to keep the current binary
running. The existing audit's external trust questions still need an explicit
decision: ACP client trust, repository-authored instructions, custom HTTP
provider endpoints, and default installer signature requirements.

For cost tracking, start a new session when switching models. The existing
tally attributes cumulative session tokens to the last model; mixed-model
session estimates can therefore be wrong. Provider usage remains the source
for actual billed charges. The review records this limitation for a future
per-model accounting change.
